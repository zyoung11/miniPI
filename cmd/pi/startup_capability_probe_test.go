package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"pi/internal/testbudget"
)

// Package initialization must not detect terminal capabilities: inside tmux, detection runs `tmux display-message` to learn whether hyperlinks are forwarded. Upstream theme.ts reads the built-in themes on demand, so `pi --version` starts no process.
func TestVersionStartsNoTmuxCapabilityProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tmux is a POSIX shell script")
	}
	binary := buildPigBinaryForSignalTest(t)
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "tmux-ran")
	script := "#!/bin/sh\ntouch '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(testbudget.Context(t), binary, "--version")
	command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TMUX=/tmp/pi-test-tmux,1,0", "TERM=screen-256color", "PI_HOME="+t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pi --version: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("pi --version ran tmux during startup")
	}
}
