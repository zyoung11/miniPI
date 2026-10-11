package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"pi/coding"
	"pi/coding/extension"
	"pi/internal/testbudget"
)

// shortTempDir is a temporary directory whose name, unlike t.TempDir's, does
// not repeat the test name.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "pi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove %s: %v", dir, err)
		}
	})
	return dir
}

// waitReportLines polls report until it holds at least n lines and returns them.
func waitReportLines(t *testing.T, report string, n int, output *ptyOutput) []string {
	t.Helper()
	deadline := time.Now().Add(testbudget.Wait(t))
	for {
		if data, err := os.ReadFile(report); err == nil {
			if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); strings.TrimSpace(string(data)) != "" && len(lines) >= n {
				return lines
			}
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(report)
			t.Fatalf("interactive mode wrote %q, want %d lines; screen tail: %q", data, n, tail(output.since(0), 1500))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// decodeRPCEvent round-trips one wire value through JSON as a client reads it.
func decodeRPCEvent(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// fakeRPCCommandRunner is a headlessCommandRunner test double.
type fakeRPCCommandRunner struct {
	mu       sync.Mutex
	commands []extension.ResolvedCommand
	executed []string
	errors   []*extension.ExtensionError
}

// EmitError records one error.
func (f *fakeRPCCommandRunner) EmitError(err *extension.ExtensionError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, err)
}

// Commands returns a copy of the registered commands.
func (f *fakeRPCCommandRunner) Commands() []extension.ResolvedCommand {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]extension.ResolvedCommand(nil), f.commands...)
}

// Command finds one command by invocation name.
func (f *fakeRPCCommandRunner) Command(name string) (extension.ResolvedCommand, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, command := range f.commands {
		if command.InvocationName == name {
			return command, true
		}
	}
	return extension.ResolvedCommand{}, false
}

// ExecuteCommand records one execution of a registered command.
func (f *fakeRPCCommandRunner) ExecuteCommand(_ context.Context, name, args string) bool {
	if _, ok := f.Command(name); !ok {
		return false
	}
	f.mu.Lock()
	f.executed = append(f.executed, name+" "+args)
	f.mu.Unlock()
	return true
}

// pipeReadResult is one drained pipe read.
type pipeReadResult struct {
	data []byte
	err  error
}

// drainPipe reads r to completion on a goroutine.
func drainPipe(r *os.File) <-chan pipeReadResult {
	done := make(chan pipeReadResult, 1)
	go func() {
		data, err := io.ReadAll(r)
		done <- pipeReadResult{data: data, err: err}
	}()
	return done
}

// captureStdout runs fn with os.Stdout captured and returns what it wrote.
func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	readDone := drainPipe(r)
	fn()
	_ = w.Close()
	result := <-readDone
	_ = r.Close()
	if result.err != nil {
		panic(result.err)
	}
	return string(result.data)
}

// newModelRegistryTestSession builds one no-persist session over the services.
func newModelRegistryTestSession(t *testing.T, services *coding.Services) *coding.Session {
	t.Helper()
	model, err := coding.BuildModel("openai/gpt-5.4", services)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := coding.NewRuntime(coding.RuntimeOptions{Services: services})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	session, err := runtime.New(coding.SessionStartOptions{Model: model, NoSession: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// captureStderr runs fn with os.Stderr captured and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	readDone := drainPipe(r)
	fn()
	_ = w.Close()
	result := <-readDone
	_ = r.Close()
	if result.err != nil {
		t.Fatal(result.err)
	}
	return string(result.data)
}

// writeStartupFixtureFile writes one fixture file, creating its directory.
func writeStartupFixtureFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// captureStdoutStderr runs fn with both standard streams captured and returns
// what it wrote plus its exit code.
func captureStdoutStderr(t *testing.T, fn func() int) (stdout, stderr string, code int) {
	t.Helper()
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = wOut
	os.Stderr = wErr
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()

	outDone := drainPipe(rOut)
	errDone := drainPipe(rErr)
	code = fn()
	_ = wOut.Close()
	_ = wErr.Close()
	out := <-outDone
	if out.err != nil {
		t.Fatal(out.err)
	}
	errOutput := <-errDone
	if errOutput.err != nil {
		t.Fatal(errOutput.err)
	}
	_ = rOut.Close()
	_ = rErr.Close()
	return string(out.data), string(errOutput.data), code
}

// writeResourceLoaderFixture writes one resource fixture file (0600).
func writeResourceLoaderFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// resourceLoaderTheme returns the dark theme document renamed to name.
func resourceLoaderTheme(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../tui/theme_dark.json")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Replace(string(data), `"name": "dark"`, `"name": "`+name+`"`, 1)
}

// runPigStartup runs one startup of the test binary and captures its output.
func runPigStartup(t *testing.T, binary, home, agentDir, cwd, stdin string, args ...string) startupRun {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"--model", "test-faux/echo", "--offline"}, args...)...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "HOME="+home, "PI_HOME="+filepath.Join(home, "pi"), "PI_CODING_AGENT_DIR="+agentDir, "PI_TEST_FAUX=1")
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return startupRun{err: err, stdout: stdout.String(), stderr: stderr.String()}
}

// writeResourceTestFiles writes a fixture file tree under root.
func writeResourceTestFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		path = filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// invalidFileURLMessage is Node's fileURLToPath text for file:///a%2Fb.
func invalidFileURLMessage() string {
	if runtime.GOOS == "windows" {
		return `File URL path must not include encoded \ or / characters`
	}
	return "File URL path must not include encoded / characters"
}

// canonicalTestPath resolves one path to its canonical absolute form.
func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}

// replaceLogLines reads a session replace log with extension instance
// identities replaced by first-seen ordinals (A, B, ...), so a test asserts
// which events shared an extension instance without depending on the random
// values.
func replaceLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replace log: %v", err)
	}
	ordinals := map[string]string{}
	var lines []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			id, ok := strings.CutPrefix(field, "instance=")
			if !ok {
				continue
			}
			if _, seen := ordinals[id]; !seen {
				ordinals[id] = string(rune('A' + len(ordinals)))
			}
			fields[i] = "instance=" + ordinals[id]
		}
		lines = append(lines, strings.Join(fields, " "))
	}
	return lines
}

// tail returns the last n bytes of data.
func tail(data []byte, n int) []byte {
	return data[max(0, len(data)-n):]
}

// fileURLOf builds the file: URL of one path.
func fileURLOf(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// assertNoTopLevelVersionJSON asserts the JSON output exposes no top-level
// format version field.
func assertNoTopLevelVersionJSON(t *testing.T, output string) {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document["version"]; exists {
		t.Fatalf("JSON output exposed a top-level format version: %s", output)
	}
}

// Command shim environment keys for tests that intercept external commands.
const (
	commandShimRealEnv = "PI_TEST_COMMAND_SHIM_REAL"
	commandShimLogEnv  = "PI_TEST_COMMAND_SHIM_LOG"
)

// runCommandShim is the shim side of writeCommandShim. It returns the real
// command's exit code.
func runCommandShim(real, log string) int {
	file, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "command shim log:", err)
		return 2
	}
	_, err = fmt.Fprintln(file, strings.Join(os.Args[1:], " "))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "command shim log:", err)
		return 2
	}
	command := exec.Command(real, os.Args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Env = slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, commandShimRealEnv+"=") || strings.HasPrefix(entry, commandShimLogEnv+"=")
	})
	if err := command.Run(); err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "command shim:", err)
		return 2
	}
	return 0
}
