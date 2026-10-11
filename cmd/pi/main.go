// pi: Go-native port of pi-coding-agent.
//
// Distinct binary name and config root from upstream pi to avoid PATH
// collisions and shared-config corruption. See codingagent.ConfigRoot()
// for the on-disk layout (defaults to ~/.pi, override with $PI_HOME).
//
// Usage:
//
//	pi [options] [prompt]
//
// Options:
//
//	--model <provider/model>    Model to use (default: from settings)
//	--agent <name>              Agent definition to load from <config>/agents/
//	--print <prompt>            Print mode: send prompt and print response, then exit
//	--no-extensions             Disable all extensions
//	-e <path>                   Load extension (can be repeated)
//	--skill <name>              Load a skill
//	--cwd <dir>                 Working directory (default: current dir)
//	--agent-dir <dir>           Agent config directory (default: <config>/agent)
//	--version                   Print version and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"
	"time"

	"pi/internal/lazyregexp"

	"golang.org/x/term"

	"pi/agent"
	"pi/ai"
	"pi/coding"
	"pi/coding/extension"
	"pi/internal/codingagent"
	"pi/internal/codingagent/export"
	"pi/internal/nativeplatform"
	"pi/internal/profiling"
	"pi/internal/resolvepath"
	"pi/internal/termuxenv"
	"pi/tui"
)

// Version is the release line this build reports; UpstreamVersion is the
// upstream Pi behavior pin. The literals live in internal/coding/piversion.
const (
	Version         = coding.Version
	UpstreamVersion = coding.UpstreamVersion

	startupSDKLockTimeout = 2 * time.Second
)

// Build is the git commit/tag, set via -ldflags at build time.
var Build = "dev"

func buildIdentity() string {
	if Build != "dev" {
		return Build
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Build
	}
	return resolveBuildIdentity(Build, info.Main.Version)
}

func resolveBuildIdentity(linkerBuild, moduleVersion string) string {
	if linkerBuild != "dev" {
		return linkerBuild
	}
	if moduleVersion == "" || moduleVersion == "(devel)" {
		return linkerBuild
	}
	return moduleVersion
}

// cliVersionString is the `pi --version` output.
func cliVersionString() string {
	return Version
}

// versionString returns pi's self-identification string for help
// and diagnostics banners.
func versionString() string {
	return fmt.Sprintf("pi %s [%s %s/%s build=%s]",
		Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, buildIdentity())
}

func detailedVersionString() string {
	return fmt.Sprintf("pi: %s\ngo: %s\nplatform: %s/%s\nbuild: %s",
		Version, runtime.Version(), runtime.GOOS, runtime.GOARCH, buildIdentity())
}

// ─── Extension Loading ────────────────────────────────────────────────────────

// ─── Resource Loading ────────────────────────────────────────────────────────────

// loadSkills loads ordered skill inputs, validates descriptions, and returns
// first-wins definitions with collision diagnostics for the interactive listing.
// A missing named skill remains an error; invalid discovered files are warned.
func loadSkills(skillInputs []string, noSkills bool) ([]*codingagent.SkillDef, []extension.ResourceDiagnostic, error) {
	if noSkills || len(skillInputs) == 0 {
		return nil, nil, nil
	}
	var skillDefs []*codingagent.SkillDef
	for _, input := range skillInputs {
		if input == "" {
			continue
		}
		if _, err := os.Stat(input); err == nil {
			loaded, err := codingagent.LoadSkills(codingagent.LoadSkillsOptions{AgentDir: codingagent.DefaultAgentDir(), SkillPaths: []string{input}})
			if err != nil {
				return skillDefs, nil, err
			}
			for _, diagnostic := range loaded.Diagnostics {
				fmt.Fprintf(os.Stderr, "warning: %s: %s\n", diagnostic.Path, diagnostic.Message)
			}
			skillDefs = append(skillDefs, loaded.Skills...)
			continue
		}
		def, err := codingagent.LoadSkill(codingagent.DefaultSkillsDir(), input)
		if err != nil {
			return skillDefs, nil, fmt.Errorf("--skill %q: %w", input, err)
		}
		for _, diagnostic := range codingagent.SkillDiagnostics(def) {
			fmt.Fprintf(os.Stderr, "warning: %s: %s\n", def.Path, diagnostic)
		}
		if strings.TrimSpace(def.Description) != "" {
			skillDefs = append(skillDefs, def)
		}
	}
	defs, diagnostics := codingagent.DeduplicateSkillsWithDiagnostics(skillDefs)
	return defs, diagnostics, nil
}

// ─── Initial Message ──────────────────────────────────────────────────────────

// readPipedStdin returns piped stdin content, trimmed. It returns "" when
// stdin is a terminal or the content is blank. Mirrors upstream main.ts
// readPipedStdin (`data.trim() || undefined`).
//
// The read waits for end of input, so a writer that never closes the pipe
// keeps it waiting, as upstream's does. A termination signal ends the wait:
// upstream reads stdin before print mode registers its signal handlers, so
// the signal's default action stops the process mid-read. It returns ctx's
// error when ctx ends first.
func readPipedStdin(ctx context.Context) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return "", nil
	}
	read := make(chan string, 1)
	go func() {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			read <- ""
			return
		}
		read <- strings.TrimSpace(string(data))
	}()
	select {
	case content := <-read:
		return content, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// buildInitialMessage combines stdin content, @file text, and the first CLI
// message into the initial prompt, and returns the remaining CLI messages,
// which are sent one by one after it. Mirrors upstream cli/initial-message.ts:
// the parts are concatenated in order with no separator, and image files are
// returned as separate content blocks.
func buildInitialMessage(messages []string, fileText string, fileImages []ai.ImageContent, stdinContent string) (string, []ai.ImageContent, []string) {
	var parts []string
	if stdinContent != "" {
		parts = append(parts, stdinContent)
	}
	if fileText != "" {
		parts = append(parts, fileText)
	}
	if len(messages) > 0 {
		parts = append(parts, messages[0])
		messages = messages[1:]
	}
	var images []ai.ImageContent
	if len(fileImages) > 0 {
		images = fileImages
	}
	return strings.Join(parts, ""), images, slices.Clone(messages)
}

// prepareInitialMessage processes @file arguments without resizing and builds the initial prompt. Session selects the resize profile after extension hooks select the request model.
func prepareInitialMessage(cwd string, messages, fileArgs []string, stdinContent string) (string, []ai.ImageContent, []string, error) {
	if len(fileArgs) == 0 {
		initial, images, rest := buildInitialMessage(messages, "", nil, stdinContent)
		return initial, images, rest, nil
	}
	// upstream: packages/coding-agent/src/main.ts:prepareInitialMessage
	processed, err := codingagent.ProcessCLIFileArguments(fileArgs, cwd, codingagent.ProcessFileOptions{AutoResizeImages: new(false)})
	if err != nil {
		return "", nil, nil, err
	}
	initial, images, rest := buildInitialMessage(messages, processed.Text, processed.Images, stdinContent)
	return initial, images, rest, nil
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func runPigPreSessionCommand(args []string) int {
	return -1
}

func runRetiredCommand(args []string) int {
	if len(args) == 0 || args[0] != "resource" {
		return -1
	}
	fmt.Fprintln(os.Stderr, "pi: unknown command resource; use `pi config` to change Resource filters or `pi status --json` to inspect Resources")
	return 2
}

// runStableCLI is the stable entry shared by the default and experimental executables.
func runStableCLI() {
	// PI_PROFILE (internal/profiling) is read once here. Unset, it costs one lookup.
	stopProfiles = profiling.Start()
	defer stopProfiles()
	defer nativeplatform.ShutdownClipboard()
	defer exitOnRenderOverflow()

	termuxenv.Configure()
	// pi additive (D95): record the executable before anything can replace it, so an error can report that this process runs a replaced install.
	binaryPath := guardBinaryIdentity()
	setupCli()

	// Subcommands run before a Package Binary initializes session extensions.
	if len(os.Args) > 1 {
		if code := runAuthCommand(os.Args[1:]); code >= 0 {
			exitProcess(code)
		}
	}
	if len(os.Args) > 1 {
		if code := runLoginCommand(os.Args[1:]); code >= 0 {
			exitProcess(code)
		}
		if code := runStatusCommand(os.Args[1:]); code >= 0 {
			exitProcess(code)
		}
		if code := runRetiredCommand(os.Args[1:]); code >= 0 {
			exitProcess(code)
		}
		switch os.Args[1] {
		case "config":
			exitProcess(runConfigCommand(os.Args[2:]))
			fmt.Println(detailedVersionString())
			exitProcess(0)
		}
		if code := runPigPreSessionCommand(os.Args[1:]); code >= 0 {
			exitProcess(code)
		}
	}

	flags := parseFlags(os.Args[1:])
	trace.Mark("flags-parsed")
	if reportArgDiagnostics(os.Stderr, flags.Diagnostics, term.IsTerminal(int(os.Stderr.Fd()))) {
		exitProcess(1)
	}

	// Pi routes version output before Session validation (main.ts:619-622).
	if flags.Version {
		fmt.Println(cliVersionString())
		exitProcess(0)
	}

	// --session-id conflict check. Mirrors upstream main.ts:217-230 (v0.76.0).
	if flags.SessionID != "" {
		// Validate format. Mirrors upstream assertValidSessionId (session-manager.ts).
		if !isValidSessionID(flags.SessionID) {
			printCLIError("session id must be non-empty, contain only alphanumeric characters, '-', '_', and '.', and start and end with an alphanumeric character")
			exitProcess(1)
		}
		var conflicts []string
		if flags.Session != "" {
			conflicts = append(conflicts, "--session")
		}
		if flags.Continue {
			conflicts = append(conflicts, "--continue")
		}
		if flags.ResumeAny {
			conflicts = append(conflicts, "--resume")
		}
		if len(conflicts) > 0 {
			printCLIError("--session-id cannot be combined with %s", strings.Join(conflicts, ", "))
			exitProcess(1)
		}
	}

	exportOfflineMode(flags.Offline)

	// --list-models is handled after extensions load (below), so
	// extension-contributed providers appear in the catalog, matching upstream
	// which lists against the extension-populated modelRuntime.

	// --export: export session file to HTML and exit.
	// Mirrors upstream main.ts:459-466.
	if flags.Export != "" {
		outputPath := ""
		if len(flags.Args) > 0 {
			outputPath = flags.Args[0]
		}
		result, err := export.ExportFromFile(flags.Export, outputPath)
		if err != nil {
			printCLIError("%v", err)
			exitProcess(1)
		}
		fmt.Fprintf(os.Stderr, "Exported to: %s\n", result)
		exitProcess(0)
	}

	// Non-interactive startup reserves stdout except for plain runtime metadata (Pi main.ts:129-130,638-642).
	if processAppMode(flags) != appModeInteractive && !isPlainRuntimeMetadataCommand(flags) {
		if err := codingagent.TakeOverStdout(); err != nil {
			printCLIError("%v", err)
			exitProcess(1)
		}
	}

	// Upstream main.ts runs validateForkFlags after --version and --export.
	if reportArgDiagnostics(os.Stderr, validateForkFlags(flags), term.IsTerminal(int(os.Stderr.Fd()))) {
		exitProcess(1)
	}

	initialCWD, err := os.Getwd()
	if err != nil {
		printCLIError("get cwd: %v", err)
		exitProcess(1)
	}

	// Set up context with signal handling.
	//
	// SIGTERM and SIGHUP dispose the runtime before cancelling. Print/JSON
	// leave SIGINT to the default process action; interactive mode owns D51.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM)
	if runtime.GOOS != "windows" {
		signal.Notify(sigCh, syscall.SIGHUP)
	}
	defer signal.Stop(sigCh)
	go func() {
		sig := <-sigCh
		if sysSig, ok := sig.(syscall.Signal); ok {
			receivedTerminationSignal.Store(int32(sysSig))
		}
		// pi additive (D102): Pi exits on SIGTERM and SIGHUP without a trace.
		codingagent.RecordExit(codingagent.AgentDir(), "received "+codingagent.SignalName(sig))
		// Give extensions their cleanup event before cancelling: the root
		// context owns the extension subprocesses, so cancelling it kills the
		// very processes that would receive session_shutdown. Upstream's
		// signal-triggered shutdown likewise disposes the runtime first.
		runTerminationShutdownHook()
		cancel()
	}()

	// pi does not use undici's default 300s body/header timeouts. Our
	// streaming HTTP clients are built on Go's net/http with no overall client
	// timeout and ResponseHeaderTimeout explicitly disabled for long-lived SSE
	// streams; provider-specific deadlines still come from context cancellation.

	// Resolve directories.
	// Working directory comes from os.Getwd() (no CLI override; matches upstream).
	cwd := initialCWD
	agentDir := codingagent.AgentDir()
	agentDirForModelOverride = agentDir

	// pi additive (D102): an interactive session leaves a marker that only a clean end removes, so an exit nothing else
	// recorded is reported at the next start.
	if processAppMode(flags) == appModeInteractive {
		codingagent.ReconcileSessionMarkers(agentDir, time.Now())
		codingagent.BeginSessionMarker(agentDir, Version, initialCWD)
		defer codingagent.EndSessionMarker()
	}

	// pi divergence (D88): first-time setup runs on an interactive start when settings.json does not exist yet; observed
	// before anything this run writes creates it.

	// Run one-shot config migrations before loading services/resources so
	// renamed directories (e.g. commands/ → prompts/) are visible on the
	// current startup path. Mirrors upstream startup ordering.
	if _, _, err := codingagent.RunMigrations(cwd, agentDir); err != nil {
		printCLIError("%v", err)
		exitProcess(1)
	}

	// Bootstrap global proxy settings before constructing any provider clients; Session-local settings are resolved after Session selection.
	startupSettingsManager := codingagent.NewSettingsManager(cwd, agentDir)
	if err := ai.ApplyHTTPProxySettings(startupSettingsManager.GetGlobalSettings().HTTPProxy); err != nil {
		printCLIError("%v", err)
		exitProcess(1)
	}
	startupSettingsDiagnostics := codingagent.CollectSettingsDiagnostics(startupSettingsManager)
	sessionDir, err := resolveSessionDir(flags.SessionDir, startupSettingsManager)
	if err != nil {
		printCLIError("%v", err)
		exitProcess(1)
	}
	startupUIOpts := startupUIOptions(flags, processAppMode(flags) == appModeInteractive, initialCWD, agentDir, startupSettingsManager)
	// Pi createSessionManager selects in-memory modes before considering the resume picker.
	if flags.ResumeAny && !flags.NoSession && !flags.Help && flags.ListModels == "" && !flags.ListModelsAll {
		if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
			exitProcess(0)
		}
		manager := newSessionManagerWithDir(initialCWD, sessionDir)
		selected, ok, selectErr := codingagent.SelectStartupSession(
			func(options codingagent.SessionListOptions) ([]codingagent.SessionInfo, error) {
				return manager.ListCurrentSessions(options)
			},
			func(options codingagent.SessionListOptions) ([]codingagent.SessionInfo, error) {
				return manager.ListAllSessions(options)
			},
			startupUIOpts,
		)
		if selectErr != nil {
			printCLIError("select session: %v", selectErr)
			exitProcess(1)
		}
		if !ok {
			// Upstream main.ts:422 console.log(chalk.dim(...)): faint only when
			// chalk's stdout color level is non-zero.
			msg := "No session selected"
			if chalkColorLevel(environMap(os.Environ()), os.Args[1:], true) > 0 {
				msg = "\x1b[2m" + msg + "\x1b[22m"
			}
			_, _ = fmt.Fprintln(os.Stdout, msg)
			exitProcess(0)
		}
		flags.ResumeAny = false
		flags.Session = selected
	}
	startupSession, err := resolveStartupSessionSelection(flags, initialCWD, sessionDir)
	if err != nil {
		_, missing := errors.AsType[*sessionNotFoundError](err)
		_, exists := errors.AsType[*sessionAlreadyExistsError](err)
		_, fileURL := errors.AsType[*sessionPathURLError](err)
		if missing || exists || fileURL {
			color := chalkColorLevel(environMap(os.Environ()), os.Args[1:], term.IsTerminal(int(os.Stdout.Fd()))) > 0
			fmt.Fprintln(os.Stderr, formatStartupSessionError(err, color))
		} else {
			printCLIError("%v", err)
		}
		exitProcess(1)
	}
	if crossProject := startupSession.crossProject; crossProject != nil {
		confirmed, confirmErr := confirmCrossProjectSession(os.Stdin, os.Stdout, crossProject.cwd)
		if confirmErr != nil {
			printCLIError("confirm cross-project session: %v", confirmErr)
			exitProcess(1)
		}
		if !confirmed {
			_, _ = fmt.Fprintln(os.Stdout, "Aborted.")
			exitProcess(0)
		}
		manager := newSessionManagerWithDir(startupSession.runtimeCWD, sessionDir)
		forked, forkErr := manager.ForkFromFile(crossProject.path)
		if forkErr != nil {
			printCLIError("%v", forkErr)
			exitProcess(1)
		}
		startupSession.forkPath = forked.Path()
		startupSession.crossProject = nil
	}
	if issue := startupSession.missingCWD; issue != nil {
		if processAppMode(flags) != appModeInteractive {
			fmt.Fprintf(os.Stderr, "%s\n", issue.Error())
			exitProcess(1)
		}
		selected, ok, selectErr := codingagent.ShowStartupSelector(
			issue.prompt(),
			[]string{"Continue", "Cancel"},
			startupUIOpts,
		)
		if selectErr != nil {
			printCLIError("select session cwd: %v", selectErr)
			exitProcess(1)
		}
		if !ok || selected != 0 {
			exitProcess(0)
		}
		startupSession.runtimeCWD = issue.fallbackCWD
		startupSession.missingCWD = nil
		flags.sessionCwdOverride = new(startupSession.runtimeCWD)
	}
	if flags.Continue && startupSession.resumePath == "" {
		manager := newSessionManagerWithDir(initialCWD, sessionDir)
		fmt.Fprintf(os.Stderr, "warning: no prior session in %s; starting fresh\n", manager.SessionDir())
	}
	cwd = startupSession.runtimeCWD
	sessionDir = startupSession.sessionDir
	// Session selection and its effects precede name normalization and validation (Pi main.ts:680-701).
	sessionName, nameErr := sessionNameFromFlags(flags)
	if nameErr != nil {
		printCLIError("%v", nameErr)
		exitProcess(1)
	}
	if applied, nameErr := startupSession.applyName(sessionName); nameErr != nil {
		printCLIError("set session name: %v", nameErr)
		exitProcess(1)
	} else if applied {
		sessionName = ""
	}
	flags.Name = sessionName
	// The builder is Pi's createRuntime closure: every mode builds its first Session
	// and every replacement Session from the same process-fixed inputs.
	builder := &cliRuntimeBuilder{
		mode: processAppMode(flags), flags: flags, agentDir: agentDir, launchCWD: initialCWD,
		settingsManager: startupSettingsManager, settingsDiagnostics: startupSettingsDiagnostics,
		startupUIOptions: startupUIOpts, trustStore: codingagent.NewProjectTrustStore(agentDir),
		trustByCWD: map[string]bool{},
	}
	buildInput := cliBuildInput{CWD: cwd, Startup: true}
	build, err := builder.buildResources(ctx, buildInput)
	if err != nil {
		reportCLIStartupFailure(err)
		exitProcess(1)
	}
	extensionDiagnostics := build.ExtensionDiagnostics
	startupDiagnostics := slices.Concat(build.StartupDiagnostics, extensionDiagnostics)

	// Resolve model
	// Package model acts as a fallback: --model flag > package.model > settings.defaultModel
	if err := startupSession.loadSession(flags); err != nil {
		printCLIError("open selected session: %v", err)
		exitProcess(1)
	}
	buildInput.Manager = startupSession.manager
	buildInput.Continuing = startupSession.resumePath != "" || startupSession.forkPath != ""
	if err := builder.buildSession(ctx, build, buildInput); err != nil {
		reportCLIStartupFailure(err)
		exitProcess(1)
	}
	flags = build.Flags
	selected, modelErr := build.Selected, build.ModelErr
	// resolveModelScope reports immediately; buildSessionOptions diagnostics follow extension errors in the runtime's report.
	// A quiet startup (true or "header") silences model-scope warnings, so an
	// offline local server does not flood every launch; --verbose prints them.
	if build.Settings.QuietStartup != codingagent.QuietStartupTrue && build.Settings.QuietStartup != codingagent.QuietStartupHeader || flags.Verbose {
		for _, warning := range selected.ScopeWarnings {
			printModelDiagnostic(warning)
		}
	}
	for _, warning := range selected.Warnings {
		startupDiagnostics = append(startupDiagnostics, codingagent.AgentSessionRuntimeDiagnostic{Type: "warning", Message: warning})
	}
	for _, diagnosticErr := range selected.Errors {
		startupDiagnostics = append(startupDiagnostics, codingagent.AgentSessionRuntimeDiagnostic{Type: "error", Message: diagnosticErr.Error()})
	}
	startupDiagnostics = codingagent.DeduplicateDiagnostics(startupDiagnostics)

	// Metadata includes scope warnings and startup settings diagnostics, but suppresses runtime diagnostics and their failure status.
	if flags.Help {
		codingagent.ReportDiagnostics(startupSettingsDiagnostics)
		printHelp(os.Stdout, term.IsTerminal(int(os.Stdout.Fd())), extensionHelpFlags(build.Extensions)...)
		exitProcess(0)
	}
	if flags.ListModels != "" || flags.ListModelsAll {
		codingagent.ReportDiagnostics(startupSettingsDiagnostics)
		printModelList(build.Services.Registry().ModelRegistry, agentDir, flags.ListModels)
		exitProcess(0)
	}
	// RPC owns stdin as JSONL. Other modes read piped input before runtime diagnostics, and metadata commands above never consume it.
	var stdinContent string
	if flags.Mode != "rpc" {
		trace.Mark("stdin-read-start")
		stdinContent, err = readPipedStdin(ctx)
		if err != nil {
			if sig := receivedTerminationSignal.Load(); sig != 0 {
				exitProcess(128 + int(sig))
			}
			printCLIError("read stdin: %v", err)
			exitProcess(1)
		}
		trace.Mark("stdin-read-done")
	}
	initialMessage, initialImages, extraMessages, messageErr := prepareInitialMessage(initialCWD, flags.Args, flags.FileArgs, stdinContent)
	if messageErr != nil {
		printCLIError("%v", messageErr)
		exitProcess(1)
	}
	// main.ts:898 applies the configured theme in every mode before the run starts; the interactive controller applies its own at construction. Print, JSON and RPC runs draw tool output and exports with it.
	if processAppMode(flags) != appModeInteractive {
		initTheme(build.Services.SettingsManager(), agentDir)
	}
	// Only an error diagnostic stops startup; warnings are reported and startup goes on (main.ts:908-916).
	hasRuntimeErrors := modelErr != nil || slices.ContainsFunc(extensionDiagnostics, func(diagnostic codingagent.AgentSessionRuntimeDiagnostic) bool { return diagnostic.Type == "error" })
	if hasRuntimeErrors {
		exitProcess(1)
	}
	stopModelServices = build.Services.Close
	services := build.Services
	settings := build.Settings
	model, noModelWarning := build.Model, build.NoModelWarning
	llamaHost := build.Llama
	projectTrusted := build.ProjectTrusted
	skillDefs := build.skills()
	slr := build.SkillLoad
	promptPaths, themePaths, skillInputs := build.PromptPaths, build.ThemePaths, build.SkillInputs
	skillScopes := build.SkillScopes
	resourceFlags := build.ResourceFlags
	startupSourceResolver := build.SourceResolver
	reloadBuiltinExtensions, builtinExts := build.ReloadBuiltinExtensions, build.BuiltinExtensions
	agentToolNames := build.AgentToolNames
	allowed, activeBuiltin, excludedTools, skipBuiltinTools := build.Allowed, build.ActiveBuiltin, build.ExcludedTools, build.SkipBuiltinTools
	projectCtxFiles, resolvedPrompts := build.ContextFiles, build.ResolvedPrompts
	systemPrompt, systemPromptOptions := build.SystemPrompt, build.SystemPromptOptions
	// No agent-defined beforeToolCall hooks: agent persona system is
	// a pi-extension concern, not built into core.
	var beforeToolCall []agent.BeforeToolCallHook

	// Quiet startup banner so the user always knows which binary launched.
	// Suppressed in --print mode and when quietStartup is true; "header" keeps
	// it (interactive-mode.ts:996, shouldShowStartupHeader at 1409-1412).
	// --verbose overrides quietStartup (mirrors upstream args.ts:239).
	// pi divergence (D2): banner says "pi", not "pi": separate binary
	// and config root avoid collisions with upstream pi.
	showBanner := flags.Print == "" && flags.Mode != "rpc" && (settings.QuietStartup != codingagent.QuietStartupTrue || flags.Verbose)
	var loginOperationalLines []string
	if showBanner {
		identityLine := fmt.Sprintf("Pi %s • config=%s • bin=%s",
			Version, codingagent.ConfigRoot(), binaryPath)
		startupHints := codingagent.StartupKeybindHints(codingagent.NewKeybindingsManager(agentDir))
		loginOperationalLines = append(loginOperationalLines, identityLine, "", startupHints)
	}
	_ = llamaHost
	_ = resourceFlags
	_ = slr
	_ = skillInputs

	// Startup session selection already ran before cwd-bound services. Headless
	// modes reuse the selected file; the interactive path below does the same.
	printResumePath := startupSession.resumePath
	if startupSession.forkPath != "" {
		printResumePath = startupSession.forkPath
	}

	// Print mode
	// Mirrors upstream main.ts resolveAppMode: --print, --mode json, or a
	// stdin or stdout that is not a terminal runs print mode.
	if processAppMode(flags) != appModeInteractive {
		codingagent.ReportDiagnostics(startupDiagnostics)
		// The print/JSON prompt comes from positional args, @files, and stdin.
		// --print is a bare boolean (upstream semantics); the prompt is in
		// initialMessage, which may be empty (upstream then sends nothing).
		mode := "text"
		if processAppMode(flags) == appModeJSON {
			mode = "json"
		}
		// Print and JSON mode expand prompt templates as upstream
		// AgentSession.prompt does, so they load them as RPC mode does.
		host := builder.printHost(build, &printStartup{
			Manager: startupSession.manager, ResumePath: printResumePath, SessionName: sessionName,
			SessionDir: sessionDir, NoSession: flags.NoSession, SessionID: flags.SessionID, CWDOverride: flags.sessionCwdOverride,
		})
		if err := runPrintMode(ctx, host, printModeOptions{Mode: mode, Messages: extraMessages, InitialMessage: initialMessage, InitialImages: initialImages}); err != nil {
			// A run stopped by a termination signal reports 128+signum and
			// stays quiet, matching upstream's print-mode signal handlers.
			if signalErr, ok := errors.AsType[*signalExitError](err); ok {
				exitProcess(signalErr.ExitCode())
			}
			if !errors.Is(err, errPrintModeHandled) {
				printCLIError("%v", err)
			}
			exitProcess(1)
		}
		return
	}

	// Interactive mode reuses the startup-selected Session. Bare --resume still
	// defers to the in-TUI picker until the shared U4 startup selector lands.
	resumePath := startupSession.resumePath
	forkPath := startupSession.forkPath

	// The Session comes from the runtime factory, and /new, /resume, /fork,
	// /clone and /import replace it through the same factory, as Pi's
	// interactive mode does with runtimeHost.
	trace.Mark("pre-runtime")

	settingsManager := services.SettingsManager()
	if err := configureHTTPDispatcherFromSettings(settingsManager); err != nil {
		printCLIError("configure HTTP dispatcher: %v", err)
		exitProcess(1)
	}

	// Create or reopen the Session with the resolved CLI thinking override before binding interactive presentation.
	startOpts := startupSession.startOptions(flags)
	interactiveInputs := builder.interactiveInputs(build, startOpts)
	sessionFactory := newCLISessionFactory(ctx, interactiveInputs, build, func(ctx context.Context, options coding.CreateAgentSessionRuntimeOptions) (cliSessionInputs, *cliBuild, error) {
		next, err := builder.rebuild(ctx, options)
		if err != nil {
			return cliSessionInputs{}, nil, err
		}
		return builder.interactiveInputs(next, coding.SessionStartOptions{}), next, nil
	})
	defer sessionFactory.Close()
	trace.Mark("pre-session")
	startManager, err := coding.SessionManagerFor(services, interactiveInputs.Start)
	if err != nil {
		printCLIError("construct session: %v", err)
		exitProcess(1)
	}
	rt, err := coding.CreateAgentSessionRuntime(ctx, sessionFactory.Factory(), coding.CreateAgentSessionRuntimeOptions{
		CWD: cwd, AgentDir: agentDir, SessionManager: startManager,
	})
	if err != nil {
		printCLIError("construct session: %v", err)
		exitProcess(1)
	}
	defer func() { _ = rt.Close() }()
	stopModelServices = func() { rt.Services().Close() }
	codingSess := rt.Session()
	trace.Mark("session-created")
	// Initial metadata does not emit a runtime session_info_changed notification.
	if sessionName != "" {
		if _, err := codingSess.Inner().AppendSessionInfo(sessionName); err != nil {
			printCLIError("set session name: %v", err)
			exitProcess(1)
		}
	}
	displayResumePath := resumePath
	if forkPath != "" {
		displayResumePath = forkPath
	}
	requestAuthRuntime, err := codingagent.NewRequestAuthRuntime(ctx, codingagent.RequestAuthRuntimeOptions{
		Credentials: services.Auth(),
		AgentDir:    agentDir,
	})
	if err != nil {
		printCLIError("construct request auth runtime: %v", err)
		exitProcess(1)
	}
	interactiveRegistryAllowed, _ := toolRegistryFilters(flags)
	iopts := codingagent.InteractiveOptions{
		ContextUsage: func() (*int, int) {
			usage := rt.Session().ContextUsage()
			if usage == nil {
				return nil, 0
			}
			return usage.Tokens, usage.ContextWindow
		},
		CWD:                 cwd,
		AgentDir:            agentDir,
		SessionDir:          sessionDir,
		Model:               model,
		NoModelWarning:      noModelWarning,
		StartupDiagnostics:  startupDiagnostics,
		Settings:            settings,
		SettingsManager:     settingsManager,
		SystemPrompt:        systemPrompt,
		SystemPromptOptions: systemPromptOptions,
		AllowedTools:        allowed,
		ToolRegistryAllowed: interactiveRegistryAllowed,
		ActiveBuiltinTools:  activeBuiltin,
		ExcludedTools:       excludedTools,
		NoBuiltinTools:      skipBuiltinTools,
		PromptPaths:         promptPaths,
		ThemePaths:          themePaths,
		NoPromptTemplates:   flags.NoPromptTemplates,
		NoThemes:            flags.NoThemes,
		UnknownFlags:        flags.UnknownFlags,
		BeforeToolCall:      beforeToolCall,
		InitialMessage:      initialMessage,
		InitialImages:       initialImages,
		InitialMessages:     extraMessages,
		AppVersion:          Version,
		// pi divergence (D39): standalone-binary self-update notice at startup.
		ResourceSourceInfoProvider: resourceSourceInfoProvider(cwd, agentDir, services.SettingsManager(), flags, startupSourceResolver.Resolve),
		ReloadResourceProvider:     reloadResourceSnapshotProvider(cwd, agentDir, services.SettingsManager(), resourceFlags, skillScopes),
		Verbose:                    flags.Verbose,
		Skills:                     skillDefs,
		SkillDiagnostics:           slr.Diagnostics,
		RebuildSystemPrompt:        systemPromptRebuilder(cwd, agentDir, projectTrusted, flags, agentToolNames),
		BridgeExtensionTools:       coding.BridgeNewRunnerTools,
		SkillPaths:                 skillInputs,
		NoSkills:                   flags.NoSkills,
		ContextFiles:               projectCtxFiles,
		SystemPromptSourcePaths:    resolvedPrompts.sourcePaths,
		SessionHandle:              codingSess,
		ResumePath:                 displayResumePath,
		ModelBuilder: func(spec string) (*ai.Model, error) {
			return coding.BuildModel(spec, services)
		},
		DefaultModelPerProvider: codingagent.DefaultModelPerProvider(),
		ModelLookup:             codingSess.ModelRuntime().GetModel,
		ModelCatalog:            codingSess.ModelRuntime().GetModels,
		ModelClassify:           codingSess.ModelRuntime().Classify,
		ModelGenerateImages:     codingSess.ModelRuntime().GenerateImages,
		RequestAuthRuntime:      requestAuthRuntime,
		ModelRegistry:           services.Registry().ModelRegistry,
		ExtensionRunner:         codingSess.ExtensionRunner(),
		ExtensionContext:        rt.ExtensionContext(),
		Runtime:                 interactiveRuntime{rt},
		ReplacementResources: func(session codingagent.InteractiveSessionHandle) codingagent.InteractiveReplacement {
			current := session.(*coding.Session)
			next, _ := sessionFactory.StateFor(current)
			auth, err := codingagent.NewRequestAuthRuntime(ctx, codingagent.RequestAuthRuntimeOptions{Credentials: next.Services.Auth(), AgentDir: agentDir})
			if err != nil {
				auth = requestAuthRuntime
			}
			return builder.interactiveReplacement(next, current, auth)
		},
		BuiltinExtensions:       builtinExts,
		ReloadBuiltinExtensions: reloadBuiltinExtensions,
		Llama:                   llamaHost,
		OfflineMode:             isOfflineModeEnabled(),
		StartupMark:             trace.Mark,
		LoginHeaderOptions: codingagent.LoginHeaderOptions{
			OperationalLines: loginOperationalLines,
			TrueColor:        tui.SupportsTrueColor(),
		},
		LoginVisible: showBanner,
	}
	// Pi main.ts:943-944 forwards parsed.tuiMode and parsed.useTheme, including an empty theme name.
	iopts.TuiMode = flags.TuiMode
	iopts.InitialThemeSetting = flags.UseTheme
	iopts.TerminateExtensionProcesses = sessionFactory.TerminateProcesses
	interactive := codingagent.NewInteractiveMode(iopts)
	// SIGTERM must reach extensions before the root context is cancelled.
	setTerminationShutdownHook(interactive.ShutdownFromSignal)
	defer setTerminationShutdownHook(nil)

	trace.Mark("pre-interactive")
	if err := interactive.Run(ctx); err != nil {
		if errors.Is(err, codingagent.ErrInteractiveCrashed) {
			exitProcess(1)
		}
		printCLIError("%v", err)
		exitProcess(1)
	}
}

// toPromptContextFiles converts codingagent.ContextFile to the shape
// expected by prompts.Options.ContextFiles.
func toPromptContextFiles(cfs []codingagent.ContextFile) []struct{ Path, Content string } {
	out := make([]struct{ Path, Content string }, len(cfs))
	for i, cf := range cfs {
		out[i] = struct{ Path, Content string }{Path: cf.Path, Content: cf.Content}
	}
	return out
}

// resolveCLIResourceFlags resolves local -e, --skill, --prompt-template and
// --theme paths as main.ts:548-550 does: resolvePath(value, cwd) with no trim.
// An invalid file: URL is an error, as fileURLToPath throws and startup fails.
func resolveCLIResourceFlags(flags CLIFlags, launchCWD string) (CLIFlags, error) {
	resolve := func(paths []string) ([]string, error) {
		out := make([]string, len(paths))
		for i, path := range paths {
			out[i] = path
			if codingagent.IsLocalPath(path) {
				resolved, err := resolvepath.Resolve(path, launchCWD)
				if err != nil {
					return nil, err
				}
				out[i] = resolved
			}
		}
		return out, nil
	}
	var err error
	if flags.Extensions, err = resolve(flags.Extensions); err != nil {
		return flags, err
	}
	if flags.Skills, err = resolve(flags.Skills); err != nil {
		return flags, err
	}
	if flags.PromptTemplates, err = resolve(flags.PromptTemplates); err != nil {
		return flags, err
	}
	if flags.Themes, err = resolve(flags.Themes); err != nil {
		return flags, err
	}
	return flags, nil
}

func resolveSessionDir(flagValue string, sm *codingagent.SettingsManager) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	envName := codingagent.ENV_SESSION_DIR
	if codingagent.UsePiDirs() {
		envName = "PI_CODING_AGENT_SESSION_DIR"
	}
	if envValue := os.Getenv(envName); envValue != "" {
		return codingagent.ExpandTildePath(envValue), nil
	}
	if sm != nil {
		return sm.GetSessionDir()
	}
	return "", nil
}

// configureHTTPDispatcherFromSettings applies the resolved provider request
// settings to the ai package: the effective per-request timeout onto the
// net/http idle dispatcher, and maxRetries/maxRetryDelayMs onto the provider
// retry transport.
func configureHTTPDispatcherFromSettings(sm *codingagent.SettingsManager) error {
	if sm == nil {
		return ai.ConfigureHTTPDispatcher(ai.DefaultHTTPIdleTimeoutMs)
	}
	if err := ai.ApplyHTTPProxySettings(sm.GetGlobalSettings().HTTPProxy); err != nil {
		return err
	}
	// retry.provider.timeoutMs overrides httpIdleTimeoutMs when set
	// (upstream sdk.ts:311).
	timeoutMs, err := sm.GetProviderRequestTimeoutMs()
	if err != nil {
		return err
	}
	if err := ai.ConfigureHTTPDispatcher(timeoutMs); err != nil {
		return err
	}
	// retry.provider.maxRetries / maxRetryDelayMs drive the provider retry
	// transport (upstream sdk.ts passes both into retryProviderRequest).
	pr := sm.GetProviderRetrySettings()
	return ai.ConfigureProviderRetry(pr.MaxRetries, pr.MaxRetryDelayMs)
}

func newSessionManagerWithDir(cwd, sessionDir string) *codingagent.SessionManager {
	if sessionDir != "" {
		return codingagent.NewSessionManagerWithDir(cwd, sessionDir)
	}
	return codingagent.NewSessionManager(cwd)
}

// isValidSessionID validates session ID format.
// Mirrors upstream assertValidSessionId (session-manager.ts).
var validSessionIDPattern = lazyregexp.New(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)

func isValidSessionID(id string) bool {
	return validSessionIDPattern.MatchString(id)
}

// exportOfflineMode mirrors upstream main.ts, which normalizes --offline or a
// truthy PI_OFFLINE to PI_OFFLINE=1 and PI_SKIP_VERSION_CHECK=1 in the process
// environment. Pi extensions read PI_OFFLINE (pi-auto-update skips its
// `pi update` run) and inherit this environment, so pi's own PI_OFFLINE
// alias sets it too.
func exportOfflineMode(flagOffline bool) {
	if flagOffline || truthyEnvFlag(os.Getenv("PI_OFFLINE")) || truthyEnvFlag(strings.TrimSpace(os.Getenv("PI_OFFLINE"))) {
		_ = os.Setenv("PI_OFFLINE", "1")
		_ = os.Setenv("PI_SKIP_VERSION_CHECK", "1")
	}
}

// truthyEnvFlag mirrors upstream main.ts isTruthyEnvFlag: "1", "true" or
// "yes", case-insensitively.
// isOfflineModeEnabled returns true when the PI_OFFLINE (or legacy
// PI_OFFLINE) env var is set to a truthy value (package-manager.ts:29).
func isOfflineModeEnabled() bool {
	if value := os.Getenv("PI_OFFLINE"); value != "" {
		return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PI_OFFLINE")))
	return v == "1" || v == "true" || v == "yes"
}

func truthyEnvFlag(value string) bool {
	lower := strings.ToLower(value)
	return value == "1" || lower == "true" || lower == "yes"
}
