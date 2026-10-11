package codingagent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)


func TestStateDirsRejectPathNamespaces(t *testing.T) {
	for _, namespace := range []string{"", ".", "..", "../marketplace", "marketplace/cache", `marketplace\\cache`} {
		t.Run(namespace, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("StateDir(%q) did not panic", namespace)
				}
			}()
			_ = StateDir(namespace)
		})
	}
}

func TestMarkPathIgnoredByCloudSync_InvokesPlatformCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloud-sync-target")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	scriptPath := ""
	switch runtime.GOOS {
	case "darwin":
		scriptPath = filepath.Join(binDir, "xattr")
	case "linux":
		scriptPath = filepath.Join(binDir, "setfattr")
	default:
		MarkPathIgnoredByCloudSync(path)
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("unexpected command invocation log on %s: %v", runtime.GOOS, err)
		}
		return
	}

	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	MarkPathIgnoredByCloudSync(path)

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	switch runtime.GOOS {
	case "darwin":
		if len(lines) != 2 {
			t.Fatalf("darwin command count = %d, want 2 (%q)", len(lines), lines)
		}
		for i, attr := range []string{"com.dropbox.ignored", "com.apple.fileprovider.ignore#P"} {
			want := "-w " + attr + " 1 " + path
			if lines[i] != want {
				t.Fatalf("call %d = %q, want %q", i, lines[i], want)
			}
		}
	case "linux":
		want := "-n user.com.dropbox.ignored -v 1 " + path
		if len(lines) != 1 || lines[0] != want {
			t.Fatalf("linux calls = %q, want [%q]", lines, want)
		}
	}
}
