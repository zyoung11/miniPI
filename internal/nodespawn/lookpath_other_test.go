//go:build !windows

package nodespawn

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// spawnOutcome is what Node's spawn(file, [], { cwd, env }) does: start a
// program that reports its image path or emit the error code.
type spawnOutcome struct {
	Code  string `json:"code"`
	Image string `json:"image"`
}

func copyProgram(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// Node's spawn(file, [], { cwd, env }) is the oracle for a file without a
// slash. libuv (src/unix/process.c, libuv 1.52.1) execvp's it after replacing
// environ with env, so the PATH of env, not pi's, chooses the program: the
// first executable file in each entry in turn, an empty entry being the
// working directory, and a default path when env has no PATH.
// exec.Command looked the name up in pi's PATH instead.
func TestSetProgramSearchesThePathOfTheChildEnvironment(t *testing.T) {
	nodeBinary, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	only := filepath.Join(root, "only-in-child")
	copyProgram(t, only, "pi-child-path-command", 0o755)
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	notExecutable := filepath.Join(root, "not-executable")
	copyProgram(t, notExecutable, "pi-skipped-command", 0o644)
	copyProgram(t, only, "pi-skipped-command", 0o755)
	cwd := filepath.Join(root, "cwd")
	copyProgram(t, cwd, "pi-cwd-command", 0o755)
	inParent := filepath.Join(root, "only-in-parent")
	copyProgram(t, inParent, "pi-parent-path-command", 0o755)
	t.Setenv("PATH", inParent)
	// hop links to nest/deep, so the kernel resolves hop/.. to nest, while a
	// cleaned hop/../bin is root/bin, which does not exist.
	if err := os.MkdirAll(filepath.Join(root, "nest", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nest", "deep"), filepath.Join(root, "hop")); err != nil {
		t.Fatal(err)
	}
	copyProgram(t, filepath.Join(root, "nest", "bin"), "pi-symlink-command", 0o755)

	cases := []struct {
		name string
		file string
		cwd  string
		env  []string
	}{
		{"the child's PATH finds what Pi's does not", "pi-child-path-command", "", []string{"PATH=" + only}},
		{"Pi's PATH is not searched", "pi-parent-path-command", "", []string{"PATH=" + other}},
		{"an empty PATH is the working directory", "pi-cwd-command", cwd, []string{"PATH="}},
		{"an empty entry is the working directory", "pi-cwd-command", cwd, []string{"PATH=" + other + ":"}},
		{"a relative entry is relative to the working directory", "pi-child-path-command", root, []string{"PATH=other:only-in-child"}},
		{"a file without execute permission is skipped", "pi-skipped-command", "", []string{"PATH=" + notExecutable + ":" + only}},
		{"a directory is skipped", "other", "", []string{"PATH=" + root + ":" + only}},
		{"the child without PATH searches the default path", "sh", "", []string{"A=1"}},
		{"an entry's .. after a symlink is resolved by the kernel", "pi-symlink-command", "", []string{"PATH=" + filepath.Join(root, "hop") + "/../bin"}},
		{"a relative entry's .. after a symlink is resolved by the kernel", "pi-symlink-command", root, []string{"PATH=hop/../bin"}},
		{"the last PATH entry is the child's", "pi-child-path-command", "", []string{"PATH=" + other, "PATH=" + only}},
		{"an environment with no entries", "sh", "", []string{}},
	}
	type input struct {
		File string            `json:"file"`
		Cwd  string            `json:"cwd"`
		Env  map[string]string `json:"env"`
	}
	var inputs []input
	for _, c := range cases {
		env := map[string]string{imageHelper: "1"}
		for _, pair := range c.env {
			name, value := envName(pair)
			env[name] = value
		}
		inputs = append(inputs, input{c.file, c.cwd, env})
	}
	data, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	node := exec.CommandContext(t.Context(), nodeBinary, "-e", `
const { spawnSync } = require("node:child_process");
const cases = JSON.parse(require("node:fs").readFileSync(0, "utf8"));
process.stdout.write(JSON.stringify(cases.map(({ file, cwd, env }) => {
  const result = spawnSync(file, [], { cwd: cwd || undefined, env, encoding: "utf8" });
  if (result.error) return { code: result.error.code };
  return { image: result.stdout.trim() ? JSON.parse(result.stdout) : "" };
})));
`)
	node.Stdin = bytes.NewReader(data)
	out, err := node.Output()
	if err != nil {
		t.Fatalf("node spawn: %v; output %s", err, out)
	}
	var want []spawnOutcome
	if err := json.Unmarshal(out, &want); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := append([]string{imageHelper + "=1"}, c.env...)
			cmd := exec.Command(c.file)
			cmd.Dir = c.cwd
			SetEnv(cmd, env)
			SetProgram(cmd)
			// Extra environment entries reach the child through SetEnv; the
			// list for Node holds the same entries.
			output, err := cmd.Output()
			if want[i].Code != "" {
				if got := goErrorCode(err); got != want[i].Code {
					t.Fatalf("Start error = %v (code %q, output %q); Node emits %s", err, got, output, want[i].Code)
				}
				return
			}
			if err != nil {
				t.Fatalf("%v; Node starts %s", err, want[i].Image)
			}
			var got string
			if len(output) > 0 {
				if err := json.Unmarshal(output, &got); err != nil {
					t.Fatalf("decode %q: %v", output, err)
				}
			}
			if got == "" && want[i].Image == "" {
				return // a system program that reports nothing
			}
			if got, want := realPath(t, got), realPath(t, want[i].Image); got != want {
				t.Errorf("started %s, Node starts %s", got, want)
			}
		})
	}
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// LookPath answers as SetProgram does for a nil-or-explicit environment: a nil
// env is pi's PATH, and an explicit env is its own.
func TestLookPathSearchesThePathOfTheEnvironmentItIsGiven(t *testing.T) {
	root := t.TempDir()
	inChild := filepath.Join(root, "child")
	want := copyProgram(t, inChild, "pi-lookpath-command", 0o755)
	inParent := filepath.Join(root, "parent")
	copyProgram(t, inParent, "pi-lookpath-other", 0o755)
	t.Setenv("PATH", inParent)

	if got, err := LookPath("pi-lookpath-command", "", []string{"PATH=" + inChild}); err != nil || got != want {
		t.Errorf("LookPath in the child's PATH = %q, %v; want %q", got, err, want)
	}
	if _, err := LookPath("pi-lookpath-other", "", []string{"PATH=" + inChild}); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("LookPath of a program only Pi's PATH has = %v; want exec.ErrNotFound", err)
	}
	if got, err := LookPath("pi-lookpath-other", "", nil); err != nil || got != filepath.Join(inParent, "pi-lookpath-other") {
		t.Errorf("LookPath with a nil env = %q, %v; want Pi's PATH to find it", got, err)
	}
	if got, err := LookPath("pi-lookpath-command", root, []string{"PATH=child"}); err != nil || got != filepath.Join("child", "pi-lookpath-command") {
		t.Errorf("LookPath with a relative entry = %q, %v", got, err)
	}
}

// A cmd.Env that names PATH twice reaches the child whole, as useTrampoline
// describes, and the child's execvp searches the PATH its getenv sees, the
// first; so does SetProgram.
func TestSetProgramSearchesTheFirstPathOfADuplicatedEnvironment(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	last := filepath.Join(root, "last")
	copyProgram(t, first, "pi-first-command", 0o755)
	copyProgram(t, last, "pi-last-command", 0o755)
	t.Setenv("PATH", "")

	env := []string{imageHelper + "=1", "PATH=" + first, "PATH=" + last}
	found := exec.Command("pi-first-command")
	found.Env = env
	SetProgram(found)
	if output, err := found.Output(); err != nil {
		t.Errorf("first PATH: %v; output %q", err, output)
	}
	missing := exec.Command("pi-last-command")
	missing.Env = env
	SetProgram(missing)
	if output, err := missing.Output(); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("last PATH: error = %v (output %q); want exec.ErrNotFound", err, output)
	}
}
