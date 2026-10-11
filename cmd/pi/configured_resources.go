package main

import (
	"path/filepath"
	"slices"

	extsource "pi/coding/extension/source"
	"pi/coding/packagecontent"
	"pi/internal/codingagent"
	"pi/internal/resolvepath"
)

func collectStartupThemePaths(cwd, agentDir string, sm *codingagent.SettingsManager) []string {
	global := sm.GetGlobalSettings()
	paths := collectTopLevelResourcePaths(filepath.Join(agentDir, "themes"), global.Themes, "themes")
	return dedupStrings(paths)
}

// ambientSourceEnabled reports whether an ambient skill source participates in
// discovery: an unset scope list means every source (packagemanager.AmbientSourceEnabled).
func ambientSourceEnabled(ambientScopes *[]string, source string) bool {
	return ambientScopes == nil || slices.Contains(*ambientScopes, source)
}

func collectPromptPaths(cwd, agentDir string, sm *codingagent.SettingsManager, flags CLIFlags, projectTrusted bool, resolvers ...extsource.ResolveFunc) []string {
	paths := make([]string, 0)
	// Pi keeps the first same-name prompt after ordering CLI, project, user,
	// then Package resources.
	for _, promptPath := range flags.PromptTemplates {
		paths = append(paths, collectResourceFilesFromPaths([]string{resolveSettingsPath(cwd, promptPath)}, "prompts")...)
	}
	if !flags.NoPromptTemplates {
		paths = append(paths, codingagent.AmbientPromptPaths(cwd, agentDir, sm, projectTrusted)...)
		paths = append(paths, collectPackagePromptPaths(cwd, sm, resolvers...)...)
	}
	return dedupStrings(paths)
}

// collectThemePaths lists theme paths in upstream precedence order, the first
// theme of a name winning: --theme paths, project, user, then Packages.
func collectThemePaths(cwd, agentDir string, sm *codingagent.SettingsManager, flags CLIFlags, projectTrusted bool, resolvers ...extsource.ResolveFunc) []string {
	paths := make([]string, 0)
	for _, p := range flags.Themes {
		paths = append(paths, resolveSettingsPath(cwd, p))
	}
	if !flags.NoThemes {
		if projectRoot := codingagent.ProjectConfigDir(cwd); projectTrusted {
			paths = append(paths, collectTopLevelResourcePaths(filepath.Join(projectRoot, "themes"), sm.GetProjectSettings().Themes, "themes")...)
		}
		paths = append(paths, collectTopLevelResourcePaths(filepath.Join(agentDir, "themes"), sm.GetGlobalSettings().Themes, "themes")...)
		paths = append(paths, collectPackageThemePaths(cwd, sm, resolvers...)...)
	}
	return dedupStrings(paths)
}

// packageManagerHomeDir is Pi's getHomeDir (package-manager.ts).
func packageManagerHomeDir() string { return codingagent.PackageManagerHomeDir() }

// collectSkillInputs appends --skill paths after resolved project, user, and Package skills, as DefaultResourceLoader.reload does. --no-skills keeps the explicit paths alone.
// Ports packages/coding-agent/src/core/resource-loader.ts
func collectSkillInputs(cwd, agentDir string, sm *codingagent.SettingsManager, flags CLIFlags, ambientScopes *[]string, resolvers ...extsource.ResolveFunc) []string {
	var cliInputs []string
	for _, p := range flags.Skills {
		cliInputs = append(cliInputs, collectResourceFilesFromPaths([]string{resolveSettingsPath(cwd, p)}, "skills")...)
	}
	if flags.NoSkills {
		return dedupStrings(cliInputs)
	}

	// Resolved paths follow resourcePrecedenceRank; explicit --skill paths are additionalSkillPaths, not cliEnabledSkills from -e Packages.
	inputs := codingagent.AmbientSkillPaths(cwd, agentDir, sm, ambientSourceEnabled(ambientScopes, "workspace"), ambientSourceEnabled(ambientScopes, "user"))
	inputs = append(inputs, collectPackageSkillPaths(cwd, sm, ambientScopes, resolvers...)...)
	return dedupStrings(append(inputs, cliInputs...))
}

func collectTopLevelResourcePaths(autoDir string, entries []string, kind string) []string {
	return codingagent.TopLevelResourcePaths(autoDir, entries, packagecontent.Kind(kind))
}

// resolveConfiguredResourceEntries lists a settings array's resources. An entry
// that fails to resolve empties the list: validateConfiguredResourceEntries
// reports that error where Pi's resourceLoader.reload() throws, before any
// collector runs.
func resolveConfiguredResourceEntries(entries []string, baseDir string, kind string) []string {
	resolved, err := packagecontent.ResolveConfiguredWithError(entries, baseDir, packagecontent.Kind(kind))
	if err != nil {
		return nil
	}
	return resolved
}

// validateConfiguredResourceEntries returns the first error Pi's packageManager.resolve() throws for configured settings: package identities first (validateConfiguredPackageSources), then the settings extensions, skills, prompts and themes arrays in package-manager.ts:933-958 order (project scope, then user scope, for each type), as resolvePath(entry, baseDir, { trim: true }) throws. An untrusted project contributes no settings.
func validateConfiguredResourceEntries(cwd, agentDir string, sm *codingagent.SettingsManager, projectTrusted bool) error {
	if err := validateConfiguredPackageSources(sm, projectTrusted); err != nil {
		return err
	}
	projectRoot, projectEnabled := codingagent.ProjectConfigDir(cwd), projectTrusted
	project, user := sm.GetProjectSettings(), sm.GetGlobalSettings()
	for _, kind := range []struct {
		project, user []string
	}{
		{project.Extensions, user.Extensions},
		{project.Skills, user.Skills},
		{project.Prompts, user.Prompts},
		{project.Themes, user.Themes},
	} {
		if projectEnabled {
			if err := packagecontent.ValidateConfiguredEntries(kind.project, projectRoot); err != nil {
				return err
			}
		}
		if err := packagecontent.ValidateConfiguredEntries(kind.user, agentDir); err != nil {
			return err
		}
	}
	return nil
}

func filterAutoDiscoveredPaths(paths, overrides []string, baseDir string, kind string) []string {
	return packagecontent.FilterAutomatic(paths, overrides, baseDir, packagecontent.Kind(kind))
}

func collectAutoDiscoveredResourcePaths(dir string, kind string) []string {
	return packagecontent.DiscoverAutomatic(dir, packagecontent.Kind(kind))
}

func collectResourceFilesFromPaths(paths []string, kind string) []string {
	return packagecontent.Collect(paths, packagecontent.Kind(kind))
}

func splitResourcePatterns(entries []string) (plain, patterns []string) {
	return packagecontent.SplitPatterns(entries)
}

func applyResourcePatterns(allPaths, patterns []string, baseDir, kind string) []string {
	return packagecontent.ApplyPatterns(allPaths, patterns, baseDir, packagecontent.Kind(kind))
}

func isEnabledByOverrides(filePath string, patterns []string, baseDir, kind string) bool {
	return packagecontent.EnabledByOverrides(filePath, patterns, baseDir, packagecontent.Kind(kind))
}

// extensionLoadFailureHint mirrors upstream main.ts EXTENSION_LOAD_FAILURE_HINT.
const extensionLoadFailureHint = `Hint: Start without extensions using "` + codingagent.AppName + ` -ne".`

type extensionPathMissingError struct{ path string }

func (e extensionPathMissingError) Error() string {
	return "Extension path does not exist: " + e.path
}

func trustedAmbientScopes(scopes *[]string) *[]string {
	if scopes == nil {
		userOnly := []string{"user"}
		return &userOnly
	}
	trusted := make([]string, 0, len(*scopes))
	for _, scope := range *scopes {
		if scope != "workspace" {
			trusted = append(trusted, scope)
		}
	}
	return &trusted
}

// resolveSettingsPath is Pi's resolvePath(p, baseDir, { trim: true }) for
// already-validated settings entries and already-resolved CLI paths. The
// lenient fallback for an invalid file: URL is unreachable at startup, where
// validateConfiguredResourceEntries and resolveCLIResourceFlags fail first.
func resolveSettingsPath(baseDir, p string) string {
	if p == "" {
		return p
	}
	if resolved, err := resolvepath.ResolveTrimmed(p, baseDir); err == nil {
		return resolved
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Clean(filepath.Join(baseDir, p))
}

func dedupStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, value := range in {
		key := canonicalStatusPath(value)
		if value != "" && !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}

func discoverSkillDir(dir string) []string {
	return packagecontent.DiscoverAgentSkillDirs(dir)
}

func discoverAncestorAgentsSkillDirs(startDir string) []string {
	return codingagent.DiscoverAncestorAgentsSkillDirs(startDir)
}

// collectPackagePromptPaths lists prompt templates contributed by managed
// packages. The minimal port manages no packages.
func collectPackagePromptPaths(cwd string, sm *codingagent.SettingsManager, resolvers ...extsource.ResolveFunc) []string {
	return nil
}

// collectPackageThemePaths lists themes contributed by managed packages. The
// minimal port manages no packages.
func collectPackageThemePaths(cwd string, sm *codingagent.SettingsManager, resolvers ...extsource.ResolveFunc) []string {
	return nil
}

// collectPackageSkillPaths lists skills contributed by managed packages. The
// minimal port manages no packages.
func collectPackageSkillPaths(cwd string, sm *codingagent.SettingsManager, ambientScopes *[]string, resolvers ...extsource.ResolveFunc) []string {
	return nil
}

// validateConfiguredPackageSources resolves every configured package source.
// The minimal port manages no packages, so nothing resolves.
func validateConfiguredPackageSources(sm *codingagent.SettingsManager, projectTrusted bool) error {
	return nil
}
