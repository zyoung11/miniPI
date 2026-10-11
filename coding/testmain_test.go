package coding

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"pi/internal/testenv"
)

func TestMain(m *testing.M) {
	testRoot, err := os.MkdirTemp("", "pi-coding-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create isolated test config root:", err)
		os.Exit(2)
	}
	if err := testenv.ScopeTempDir(testRoot); err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(testRoot)
		os.Exit(2)
	}
	for _, key := range []string{"PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR"} {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "clear inherited %s: %v\n", key, err)
			_ = os.RemoveAll(testRoot)
			os.Exit(2)
		}
	}
	if err := os.Setenv("PI_HOME", filepath.Join(testRoot, "pi")); err != nil {
		fmt.Fprintln(os.Stderr, "set isolated PI_HOME:", err)
		_ = os.RemoveAll(testRoot)
		os.Exit(2)
	}
	code := m.Run()
	if err := os.RemoveAll(testRoot); err != nil {
		fmt.Fprintln(os.Stderr, "remove isolated test config root:", err)
		if code == 0 {
			code = 2
		}
	}
	os.Exit(code)
}
