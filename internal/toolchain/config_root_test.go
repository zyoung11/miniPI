package toolchain_test

import (
	"path/filepath"
	"testing"

	"pi/internal/codingagent"
	"pi/internal/toolchain"
)

// The managed toolchain lives under the one pi configuration root:
// $PI_HOME, else $XDG_CONFIG_HOME/pi, else ~/.pi, with ~ expanded, as
// codingagent.ConfigRoot resolves it.
func TestConfigRootMatchesPigConfigurationRoot(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	cases := []struct {
		name, pigHome, xdgConfigHome, want string
	}{
		{name: "PI_HOME wins", pigHome: filepath.Join(home, "p"), xdgConfigHome: xdg, want: filepath.Join(home, "p")},
		{name: "PI_HOME tilde", pigHome: "~/p", want: filepath.Join(home, "p")},
		{name: "XDG_CONFIG_HOME", xdgConfigHome: xdg, want: filepath.Join(xdg, "pi")},
		{name: "XDG_CONFIG_HOME tilde", xdgConfigHome: "~/cfg", want: filepath.Join(home, "cfg", "pi")},
		{name: "default", want: filepath.Join(home, ".pi")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("PI_HOME", tc.pigHome)
			t.Setenv("XDG_CONFIG_HOME", tc.xdgConfigHome)
			got, err := toolchain.ConfigRoot()
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || got != codingagent.ConfigRoot() {
				t.Fatalf("toolchain.ConfigRoot() = %q, want %q (codingagent.ConfigRoot() = %q)", got, tc.want, codingagent.ConfigRoot())
			}
		})
	}
}
