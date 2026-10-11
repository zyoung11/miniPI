package main

// Ports packages/coding-agent/src/main.ts createRuntime: the cwd-bound half of the runtime factory.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"

	"pi/agent"
	"pi/ai"
	"pi/coding"
	"pi/coding/extension"
	"pi/internal/codingagent"
	"pi/internal/codingagent/llama"
	"pi/internal/codingagent/prompts"
	"pi/internal/codingagent/tools"
	"pi/tui"
)

// cliStartupFailure is a failed build. Startup reports it in the form the failing step used and exits. A replacement receives it as an ordinary error.
type cliStartupFailure struct {
	message string
	report  func(message string)
}

func (f *cliStartupFailure) Error() string { return f.message }

func cliCLIError(format string, args ...any) *cliStartupFailure {
	return &cliStartupFailure{message: fmt.Sprintf(format, args...), report: func(message string) { printCLIError("%s", message) }}
}

func cliPigError(format string, args ...any) *cliStartupFailure {
	return &cliStartupFailure{message: fmt.Sprintf(format, args...), report: func(message string) { fmt.Fprintf(os.Stderr, "pi: %s\n", message) }}
}

// cliRuntimeBuilder holds the process-fixed inputs of Pi's createRuntime closure and builds the cwd-bound state of one Session runtime: services, resources, extension host, model, tools and system prompt.
type cliRuntimeBuilder struct {
	mode                appMode
	flags               CLIFlags
	agentDir            string
	launchCWD           string
	settingsManager     *codingagent.SettingsManager
	settingsDiagnostics []codingagent.AgentSessionRuntimeDiagnostic
	startupUIOptions    codingagent.StartupUIOptions
	trustStore          *codingagent.ProjectTrustStore
	trustByCWD          map[string]bool
}

// cliBuildInput selects one build.
type cliBuildInput struct {
	CWD string
	// Manager is the Session log the runtime will wrap. Model selection restores from it.
	Manager *coding.SessionManager
	// Continuing reports a resumed, continued or forked Session at startup.
	Continuing bool
	// Startup is true for the process's first build. Replacements never prompt for trust and reuse no preloaded host.
	Startup bool
	// ProjectTrustUI is the live interactive UI a replacement's trust prompt uses (Pi's projectTrustContext, main.ts:751). Without it a replacement never prompts.
	ProjectTrustUI extension.UIContext
}

// cliBuild is everything one Session runtime owns besides the Session itself.
type cliBuild struct {
	CWD            string
	Flags          CLIFlags
	ResourceFlags  CLIFlags
	ProjectTrusted bool
	Services       *coding.Services
	Settings       codingagent.Settings
	Llama          *llama.Host

	StartupDiagnostics   []codingagent.AgentSessionRuntimeDiagnostic
	ExtensionDiagnostics []codingagent.AgentSessionRuntimeDiagnostic

	PromptPaths     []string
	ThemePaths      []string
	SkillInputs     []string
	ExtensionScopes *[]string
	SkillScopes     *[]string
	SourceResolver  *startupExtensionSourceResolver

	ReloadBuiltinExtensions func() []extension.Extension
	BuiltinExtensions       []extension.Extension
	Extensions              []extension.Extension

	Model          *ai.Model
	NoModelWarning string
	// Selected is the model selection with its warnings and errors. ModelErr is its failure: startup reports it after the metadata commands, a replacement fails on it.
	Selected startupModel
	ModelErr error
	// RPCSourceInfo is the resource provenance RPC stamps on extension configs and commands.
	RPCSourceInfo map[string]codingagent.ResourceSourceInfo
	SkillCatalog  codingagent.SlashCommandCatalog
	SkillDefs     []*codingagent.SkillDef

	SkillLoad            *skillLoadResult
	AgentToolNames       []string
	Allowed              map[string]struct{}
	ActiveBuiltin        map[string]struct{}
	ExcludedTools        map[string]struct{}
	SkipBuiltinTools     bool
	ContextFiles         []codingagent.ContextFile
	ResolvedPrompts      resolvedPromptInputs
	PromptOptions        prompts.Options
	SystemPrompt         string
	SystemPromptSections ai.OrderedSections
	SystemPromptOptions  extension.BuildSystemPromptOptions
	BeforeToolCall       []agent.BeforeToolCallHook
}

func (b *cliBuild) skills() []*codingagent.SkillDef { return b.SkillDefs }

// buildResources runs Pi's createAgentSessionServices for the destination cwd: project trust, services, resource discovery and the extension load.
func (b *cliRuntimeBuilder) buildResources(ctx context.Context, in cliBuildInput) (*cliBuild, error) {
	flags := b.flags
	cwd := in.CWD
	resourceFlags, err := resolveCLIResourceFlags(flags, b.launchCWD)
	if err != nil {
		return nil, cliCLIError("%v", err)
	}
	build := &cliBuild{CWD: cwd, ResourceFlags: resourceFlags}
	hasTrustResources := codingagent.HasTrustRequiringProjectResources(cwd)
	projectTrusted := flags.ProjectTrustOverride != nil && *flags.ProjectTrustOverride
	if flags.ProjectTrustOverride == nil && !hasTrustResources {
		projectTrusted = true
	}
	cached, hasCached := b.trustByCWD[cwd]
	if flags.ProjectTrustOverride == nil && hasTrustResources && hasCached {
		projectTrusted = cached
	}
	resolveTrust := flags.ProjectTrustOverride == nil && hasTrustResources && !hasCached

	traceSDKLockWaits()
	trace.Mark("pre-services")
	services, err := coding.NewServices(coding.ServicesOptions{
		CWD:            cwd,
		AgentDir:       b.agentDir,
		ProjectTrusted: new(projectTrusted),
	})
	if err != nil {
		return nil, cliPigError("services init: %v", err)
	}
	build.Services = services
	trace.Mark("services-created")
	settings := services.Settings()

	build.SourceResolver = newStartupExtensionSourceResolver(nil)
	resolver := build.SourceResolver
	failed := true
	defer func() {
		if failed {
			services.Close()
		}
	}()

	if resolveTrust {
		if err := validateConfiguredResourceEntries(cwd, b.agentDir, services.SettingsManager(), false); err != nil {
			return nil, cliCLIError("%v", err)
		}
		trustUI := extension.NoopUIContext
		interactiveTrust := in.Startup && b.mode == appModeInteractive && !flags.Help && flags.ListModels == "" && !flags.ListModelsAll
		if interactiveTrust {
			trustUI = newStartupTrustUI(b.startupUIOptions)
		} else if in.ProjectTrustUI != nil {
			trustUI = in.ProjectTrustUI
		}
		globalDefaultTrust := b.settingsManager.GetGlobalSettings().DefaultProjectTrust
		if globalDefaultTrust == "" {
			globalDefaultTrust = "ask"
		}
		projectTrusted, err = resolveProjectTrusted(ctx, projectTrustResolutionOptions{
			CWD:     cwd,
			Store:   b.trustStore,
			Default: globalDefaultTrust,
			Runner:  nil,
			UI:      trustUI,
			OnExtensionError: func(message string) {
				fmt.Fprintln(os.Stderr, "warning:", message)
			},
		})
		if err != nil {
			return nil, cliCLIError("resolve project trust: %v", err)
		}
		b.trustByCWD[cwd] = projectTrusted
		services.SettingsManager().SetProjectTrusted(projectTrusted)
		settings = services.Settings()
		trace.Mark("trust-resolved")
	}
	build.ProjectTrusted = projectTrusted
	if builtinLlamaEnabled(services.SettingsManager(), resourceFlags) {
		build.Llama = startBuiltInLlama(ctx, services)
	}
	if in.Startup {
		tui.SetCapabilityOverrides(services.SettingsManager().GetTerminalCapabilityOverrides())
	}
	build.StartupDiagnostics = codingagent.DeduplicateDiagnostics(append(slices.Clone(b.settingsDiagnostics), codingagent.CollectSettingsDiagnostics(services.SettingsManager())...))

	if len(flags.Models) > 0 {
		settings.EnabledModels = flags.Models
	}

	if err := validateConfiguredResourceEntries(cwd, b.agentDir, services.SettingsManager(), projectTrusted); err != nil {
		return nil, cliCLIError("%v", err)
	}
	build.PromptPaths = collectPromptPaths(cwd, b.agentDir, services.SettingsManager(), resourceFlags, projectTrusted, resolver.Resolve)
	build.ThemePaths = collectThemePaths(cwd, b.agentDir, services.SettingsManager(), resourceFlags, projectTrusted, resolver.Resolve)
	skillScopes := trustedAmbientScopes(nil)
	if !projectTrusted {
		skillScopes = trustedAmbientScopes(skillScopes)
	}
	build.SkillScopes = skillScopes
	build.SkillInputs = collectSkillInputs(cwd, b.agentDir, services.SettingsManager(), resourceFlags, skillScopes, resolver.Resolve)
	trace.Mark("resources-discovered")

	registry := services.Registry()
	releaseRegistrationRefresh := registry.HoldRegistrationRefresh()
	defer releaseRegistrationRefresh()
	services.ModelRuntime().Refresh(ctx, ai.ModelsRefreshOptions{AllowNetwork: new(false)})

	build.Extensions = nil
	build.BuiltinExtensions = nil
	build.Flags = flags
	build.Settings = settings
	failed = false
	return build, nil
}

// buildSession resolves the model, skills, tools and system prompt against the build's resources, as Pi's createRuntime does before createAgentSessionFromServices.
func (b *cliRuntimeBuilder) buildSession(ctx context.Context, build *cliBuild, in cliBuildInput) error {
	flags := build.Flags
	cwd := build.CWD
	services := build.Services
	settings := build.Settings
	trace.Mark("pre-model")
	modelFlag := flags.Model
	if modelFlag == "" {
	}
	selected, modelErr := selectStartupModel(ctx, startupModelOptions{
		SessionManager: in.Manager,
		CLIProvider:    flags.Provider,
		CLIModel:       modelFlag,
		CLIThinking:    flags.Thinking,
		ScopePatterns:  settings.EnabledModels,
		Continuing:     in.Continuing,
		APIKey:         flags.APIKey,
	}, settings, services)
	build.Selected, build.ModelErr = selected, modelErr
	build.Model = selected.Model
	build.NoModelWarning = selected.ModelFallbackMessage
	if build.Model == nil && build.NoModelWarning == "" {
		build.NoModelWarning = codingagent.FormatNoModelsAvailableMessage()
	}
	if selected.Thinking != "" && flags.Thinking == "" {
		flags.Thinking = selected.Thinking
	}
	trace.Mark("model-resolved")

	registryToolNames := tools.BuiltinToolNames()
	agentToolNames := []string{"read", "bash", "edit", "write"}
	if tools := settings.GetDefaultTools(); tools != nil {
		agentToolNames = tools
	}
	allowed := map[string]struct{}(nil)
	var activeBuiltin map[string]struct{}
	skipBuiltinTools := flags.NoBuiltinTools
	if flags.NoBuiltinTools {
		agentToolNames = []string{}
	}
	// An explicit --tools list outranks --no-tools and --no-builtin-tools, as sdk.ts allowedToolNames = tools ?? (noTools === "all" ? [] : undefined) and initialActiveToolNames = tools ?? (noTools ? [] : defaults).
	switch {
	case flags.Tools != nil:
		allowed = make(map[string]struct{}, len(flags.Tools))
		for _, t := range flags.Tools {
			allowed[t] = struct{}{}
		}
		filtered := make([]string, 0, len(flags.Tools))
		for _, n := range registryToolNames {
			if _, ok := allowed[n]; ok {
				filtered = append(filtered, n)
			}
		}
		agentToolNames = filtered
		skipBuiltinTools = false
	case flags.NoTools:
		allowed = make(map[string]struct{})
		agentToolNames = []string{}
		skipBuiltinTools = false
	default:
		if !flags.NoBuiltinTools {
			activeBuiltin = make(map[string]struct{}, len(agentToolNames))
			for _, t := range agentToolNames {
				activeBuiltin[t] = struct{}{}
			}
		}
	}
	var excludedTools map[string]struct{}
	if len(flags.ExcludeTools) > 0 {
		excludedTools = make(map[string]struct{}, len(flags.ExcludeTools))
		for _, t := range flags.ExcludeTools {
			excludedTools[t] = struct{}{}
		}
		filtered := agentToolNames[:0]
		for _, n := range agentToolNames {
			if _, ok := excludedTools[n]; !ok {
				filtered = append(filtered, n)
			}
		}
		agentToolNames = filtered
	}
	build.Flags = flags
	build.AgentToolNames = agentToolNames
	build.Allowed = allowed
	build.ActiveBuiltin = activeBuiltin
	build.ExcludedTools = excludedTools
	build.SkipBuiltinTools = skipBuiltinTools
	return b.loadPromptResources(build, agentToolNames, resourceSourceInfoProvider(cwd, b.agentDir, services.SettingsManager(), build.ResourceFlags, build.SourceResolver.Resolve)())
}

// loadPromptResources discovers the build's skills and context files and derives the system prompt from them, as the resource loader and AgentSession._rebuildSystemPrompt do. sourceInfo is the provenance of the resolved resource entries. build.SkillInputs, build.Flags and build.ProjectTrusted select what is read; agentToolNames are the tools the prompt lists.
func (b *cliRuntimeBuilder) loadPromptResources(build *cliBuild, agentToolNames []string, sourceInfo map[string]codingagent.ResourceSourceInfo) error {
	cwd := build.CWD
	slr, err := resolveAndLoadSkills(build.SkillInputs)
	if err != nil {
		return cliCLIError("%v", err)
	}
	build.SkillLoad = slr
	build.SkillInputs = slr.Paths
	build.SkillCatalog = codingagent.SlashCommandCatalog{CWD: cwd, AgentDir: b.agentDir, SourceInfo: sourceInfo}
	skillDefs := build.SkillCatalog.WithSkillSources(slr.Defs)
	build.SkillDefs = skillDefs

	toolHints := prompts.DefaultToolSnippets()
	toolGuidelines := tools.DefaultToolGuidelines()
	promptSkills := make([]prompts.Skill, 0, len(skillDefs))
	for _, s := range skillDefs {
		promptSkills = append(promptSkills, prompts.Skill{Name: s.Name, Description: s.Description, Path: s.Path, DisableModelInvocation: s.DisableModelInvocation})
	}
	trace.Mark("pre-system-prompt")
	projectCtxFiles := loadContextFiles(cwd, b.agentDir, build.Flags.NoContextFiles)
	promptCtxFiles := toPromptContextFiles(projectCtxFiles)
	resolvedPrompts := resolvePromptInputs(cwd, b.agentDir, build.Flags, build.ProjectTrusted)
	promptOptions := prompts.Options{
		Cwd:            cwd,
		Tools:          agentToolNames,
		ToolHints:      toolHints,
		ToolGuidelines: toolGuidelines,
		Skills:         promptSkills,
		AppendMode:     "append",
		ContextFiles:   promptCtxFiles,
	}
	if resolvedPrompts.custom != "" {
		promptOptions.CustomPrompt = resolvedPrompts.custom
		promptOptions.AppendMode = "replace"
	}
	promptOptions.AppendSystemPrompt = resolvedPrompts.append

	build.SystemPromptSections = prompts.BuildSystemPromptSections(promptOptions)
	build.SystemPrompt = prompts.BuildDefaultPrompt(promptOptions)

	extContextFiles := make([]extension.SystemPromptContextFile, 0, len(promptCtxFiles))
	for _, cf := range promptCtxFiles {
		extContextFiles = append(extContextFiles, extension.SystemPromptContextFile{Path: cf.Path, Content: cf.Content})
	}
	build.SystemPromptOptions = extension.BuildSystemPromptOptions{
		CustomPrompt:       resolvedPrompts.custom,
		CustomPromptSet:    resolvedPrompts.customSet,
		SelectedTools:      append([]string{}, agentToolNames...),
		ToolSnippets:       toolHints,
		ToolGuidelines:     toolGuidelines,
		AppendSystemPrompt: resolvedPrompts.append,
		Cwd:                cwd,
		ContextFiles:       extContextFiles,
		Skills:             extensionPromptSkills(skillDefs),
	}
	build.ContextFiles = projectCtxFiles
	build.ResolvedPrompts = resolvedPrompts
	build.PromptOptions = promptOptions
	return nil
}

// reportCLIStartupFailure reports a failed startup build in the form its failing step used.
func reportCLIStartupFailure(err error) {
	if failure, ok := errors.AsType[*cliStartupFailure](err); ok {
		failure.report(failure.message)
		return
	}
	printCLIError("%v", err)
}
