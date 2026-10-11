package tui

import (
	"strings"
	"testing"
)

// TestExtendedKeyInitDoesNotEnableWin32InputMode pins that pi never turns on
// win32-input-mode (DECSET 9001).
//
// Windows Terminal reports keys as Kitty CSI-u sequences, which pi decodes.
// While an application holds DECSET 9001 open, Windows Terminal instead wraps
// every byte in a win32 input record ("\x1b[Vk;Sc;Uc;Kd;Cs;Rc_"), including its
// own replies to terminal queries. pi has no decoder for that framing, so
// enabling it would deliver those records to the editor as literal text.
//
// Measured on Windows 11 24H2 Windows Terminal: with 9001 requested, the reply
// to pi's own Kitty query arrived as
// "\x1b[0;0;27;1;0;1_\x1b[0;0;91;1;0;1_..." rather than "\x1b[?7u".
//
// If pi ever needs 9001, this test must fail loudly, because a win32 record
// decoder has to land in the same change.
func TestExtendedKeyInitDoesNotEnableWin32InputMode(t *testing.T) {
	if strings.Contains(extendedKeyInit, "9001") {
		t.Fatalf("extendedKeyInit enables win32-input-mode (DECSET 9001): %q\n"+
			"Windows Terminal then frames every byte as \\x1b[Vk;Sc;Uc;Kd;Cs;Rc_, which pi cannot decode.",
			extendedKeyInit)
	}
	// The Kitty negotiation Windows Terminal answers with \x1b[?7u.
	if !strings.Contains(extendedKeyInit, "\x1b[>7u") {
		t.Errorf("extendedKeyInit no longer requests Kitty flags 7: %q", extendedKeyInit)
	}
}
