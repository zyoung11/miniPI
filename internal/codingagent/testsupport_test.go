package codingagent

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pi/agent"
	"pi/coding/extension"
	"pi/tui"
)

// collisionDiagnostic builds one name-collision resource diagnostic fixture.
func collisionDiagnostic(kind, name, winner, loser string) extension.ResourceDiagnostic {
	return extension.ResourceDiagnostic{
		Type: extension.DiagnosticCollision, Message: `name "` + name + `" collision`, Path: loser,
		Collision: &extension.ResourceCollision{ResourceType: kind, Name: name, WinnerPath: winner, LoserPath: loser},
	}
}

// isolateDisplayHome sets the home directory to one that no other t.TempDir
// path starts with, so a diagnostic that asserts a full temp path sees it
// unabbreviated.
func isolateDisplayHome(t *testing.T) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// writeNamedTheme writes a copy of the dark theme with a new name into dir/file.
func writeNamedTheme(t *testing.T, dir, file, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tui", "theme_dark.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	document["name"] = name
	if data, err = json.Marshal(document); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// onLoop runs fn on the owner loop and waits for it.
func onLoop(m *InteractiveMode, ctx context.Context, fn func()) {
	done := make(chan struct{})
	m.runOnMain(ctx, func() {
		fn()
		close(done)
	})
	<-done
}

// renderListing renders the loaded-resources listing as plain text.
func renderListing(m *InteractiveMode) string {
	lines := m.loadedResourcesContainer.Render(220)
	for i, line := range lines {
		lines[i] = strings.TrimRight(stripANSITest(line), " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// reloadTestMode builds the minimum InteractiveMode the Reload closure touches.
// The editor is real because the closure rebuilds autocomplete on it.
func reloadTestMode(opts InteractiveOptions) *InteractiveMode {
	m := &InteractiveMode{runCtx: context.Background(), opts: opts}
	m.editor = tui.NewEditor()
	m.chatContainer = tui.NewContainer()
	m.loadedResourcesContainer = tui.NewContainer()
	m.tuiInst = tui.NewWithOutput(io.Discard, 80, 24)
	m.agent = agent.NewAgent(agent.AgentOptions{})
	return m
}

// chdirTemp switches to a fresh temporary directory and returns its resolved path.
func chdirTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
