package codingagent

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"pi/internal/linkerexec"
	"pi/internal/nodepath"
	"pi/internal/resolvepath"
)

// AppName is the binary/CLI name, independent of the selected configuration directories.
const AppName = "pi"

// PackageName is pi's npm package: the launcher that a global npm-family
// install owns and that the signed update manifest names. Mirrors upstream
// PACKAGE_NAME export. automation/release/pig_package.py reads this line, so keep
// the declaration on one line.
const PackageName = "pi"

// CONFIG_DIR_NAME is the default per-project config directory name.
const CONFIG_DIR_NAME = "." + AppName

// UsePiDirs reports whether Pi's configuration directories are in use. The pi
// binary always uses them.
func UsePiDirs() bool {
	return true
}

// ConfigDirName returns the selected per-project configuration directory name.
func ConfigDirName() string {
	if UsePiDirs() {
		return ".pi"
	}
	return CONFIG_DIR_NAME
}

// ENV_AGENT_DIR overrides the config directory.
// Mirrors upstream ENV_AGENT_DIR export.
const ENV_AGENT_DIR = "PI_CODING_AGENT_DIR"

// ENV_SESSION_DIR overrides the session storage directory.
// Mirrors upstream ENV_SESSION_DIR export.
const ENV_SESSION_DIR = "PI_CODING_AGENT_SESSION_DIR"

// APP_TITLE is the process title shown in diagnostics.
const APP_TITLE = "pi"

// AgentDir returns the writable agent directory. Shared mode uses Pi's environment override; otherwise it uses pi's. Both expand a leading tilde.
func AgentDir() string {
	if configured := os.Getenv(agentDirEnvName()); configured != "" {
		return ExpandTildePath(configured)
	}
	return DefaultAgentDir()
}

// AgentDirConfigured reports whether the agent directory comes from its environment variable rather than the default.
func AgentDirConfigured() bool { return os.Getenv(agentDirEnvName()) != "" }

func agentDirEnvName() string {
	if UsePiDirs() {
		return "PI_CODING_AGENT_DIR"
	}
	return ENV_AGENT_DIR
}

// ProjectConfigDir returns the selected workspace-local configuration root.
func ProjectConfigDir(cwd string) string {
	return filepath.Join(cwd, ConfigDirName())
}

// NPMInstallRoot returns the managed npm project for one settings scope.
func NPMInstallRoot(cwd, agentDir string, project bool) string {
	if project {
		return filepath.Join(ProjectConfigDir(cwd), "npm")
	}
	return filepath.Join(agentDir, "npm")
}

// GitInstallRoot returns the managed Git checkout root for one settings scope.
func GitInstallRoot(cwd, agentDir string, project bool) string {
	if project {
		return filepath.Join(ProjectConfigDir(cwd), "git")
	}
	return filepath.Join(agentDir, "git")
}

// CatalogRoot returns the managed catalog materialization root.
func CatalogRoot(agentDir string) string {
	return filepath.Join(agentDir, "catalog")
}

// StateDir returns one additive capability's user-scoped state directory.
// Namespaces are code-owned identifiers, not user-provided paths.
func StateDir(namespace string) string {
	if namespace == "" || namespace == "." || namespace == ".." || filepath.Clean(namespace) != namespace || strings.ContainsAny(namespace, `/\\`) {
		panic("invalid Pi state namespace: " + namespace)
	}
	return filepath.Join(ConfigRoot(), "state", namespace)
}

// ProjectStateDir returns one additive capability's workspace-scoped state directory.
func ProjectStateDir(cwd, namespace string) string {
	if namespace == "" || namespace == "." || namespace == ".." || filepath.Clean(namespace) != namespace || strings.ContainsAny(namespace, `/\\`) {
		panic("invalid Pi state namespace: " + namespace)
	}
	return filepath.Join(ProjectConfigDir(cwd), "state", namespace)
}











// ─── Config Root ──────────────────────────────────────────────────────────────

// ConfigRoot returns the agent configuration root directory (~/.pi/agent).
//
// Resolution order:
//  1. $PI_CODING_AGENT_DIR if set and non-empty
//  2. ~/.pi/agent (default)
func ConfigRoot() string {
	if v := os.Getenv("PI_CODING_AGENT_DIR"); v != "" {
		return ExpandTildePath(v)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pi", "agent")
}

// CanonicalizePath resolves a nonempty path to its absolute canonical filesystem form, following symlinks and drive junctions while preserving Windows volume mount points as directories. It preserves the raw input if resolution fails, including an empty or missing path.
// Mirrors upstream canonicalizePath.
func CanonicalizePath(path string) string {
	if path == "" {
		return path
	}
	absolute, err := nodepath.Resolve(path)
	if err != nil {
		return path
	}
	canonical, err := evalCanonicalPath(absolute)
	if err != nil {
		return path
	}
	return canonical
}

// IsLocalPath reports whether value is a local path rather than a package source, a built-in extension or a remote URL. Bare names, relative paths, and file URLs are local.
// Ports .upstream/v0.99.1/packages/coding-agent/src/utils/paths.ts:45-64 (a `builtin:` value names a built-in extension).
func IsLocalPath(value string) bool {
	trimmed := jsTrim(value)
	for _, prefix := range []string{"npm:", "git:", "github:", "http:", "https:", "ssh:", BuiltinPathPrefix} {
		if strings.HasPrefix(trimmed, prefix) {
			return false
		}
	}
	return true
}

// ResolvePath normalizes input and baseDir, then resolves the input to an absolute path. An empty baseDir uses the process working directory. Invalid file URLs return an error.
// Ports packages/coding-agent/src/utils/paths.ts:102-106.
func ResolvePath(input, baseDir string) (string, error) {
	return resolvepath.Resolve(input, baseDir)
}

// ExpandTildePath expands a leading ~ in a filesystem path.
// Mirrors upstream expandTildePath.
func ExpandTildePath(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, rest)
	}
	return path
}

// resolveAgainstCwd is Pi's resolvePath(filePath, cwd) without input normalization. On Windows Node's isAbsolute accepts a rooted path without a drive (/x or \x), which then resolves on the process's drive, not cwd's.
func resolveAgainstCwd(filePath, cwd string) (string, error) {
	if nodepath.IsAbsolute(filePath) {
		return nodepath.Resolve(filePath)
	}
	return nodepath.Resolve(cwd, filePath)
}

// GetCwdRelativePath returns the path relative to cwd, with the platform
// separator as Pi's path.relative gives it, when filePath resolves inside
// cwd, or "" when it is outside cwd.
func GetCwdRelativePath(filePath, cwd string) string {
	resolvedCwd, err := nodepath.Resolve(cwd)
	if err != nil {
		return ""
	}
	resolvedPath, err := resolveAgainstCwd(filePath, resolvedCwd)
	if err != nil {
		return ""
	}
	relativePath, err := filepath.Rel(resolvedCwd, resolvedPath)
	if err != nil {
		return ""
	}
	isInsideCwd := relativePath == "." || (relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) && !filepath.IsAbs(relativePath))
	if !isInsideCwd {
		return ""
	}
	return relativePath
}

// FormatPathRelativeToCwdOrAbsolute returns a slash-normalized path relative to
// cwd when possible, otherwise the cleaned absolute path.
func FormatPathRelativeToCwdOrAbsolute(filePath, cwd string) string {
	absolutePath, err := resolveAgainstCwd(filePath, cwd)
	if err != nil {
		return filepath.ToSlash(filePath)
	}
	if relativePath := GetCwdRelativePath(absolutePath, cwd); relativePath != "" {
		return filepath.ToSlash(relativePath)
	}
	return filepath.ToSlash(absolutePath)
}

// MarkPathIgnoredByCloudSync best-effort marks a directory as ignored by
// cloud sync providers. Mirrors upstream markPathIgnoredByCloudSync.
func MarkPathIgnoredByCloudSync(path string) {
	var commands [][]string
	switch runtime.GOOS {
	case "darwin":
		commands = [][]string{
			{"xattr", "-w", "com.dropbox.ignored", "1", path},
			{"xattr", "-w", "com.apple.fileprovider.ignore#P", "1", path},
		}
	case "linux":
		commands = [][]string{{"setfattr", "-n", "user.com.dropbox.ignored", "-v", "1", path}}
	default:
		return
	}
	for _, args := range commands {
		cmd := linkerexec.Command(args[0], args[1:]...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		_ = cmd.Run()
	}
}

// pathWithin reports whether path is root itself or under root.
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
