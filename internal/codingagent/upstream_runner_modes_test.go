package codingagent

import (
	"io"
	"testing"

	"pi/coding/extension"
	"pi/coding/extension/host/inproc"
	"pi/tui"
)

func TestInteractiveRunnerModeTracksUIBinding(t *testing.T) {
	m := &InteractiveMode{newRunner: inproc.NewRunner(nil, t.TempDir())}
	ctx := m.newRunner.CreateCommandContext()
	m.wireInprocContextActions()
	if mode, err := ctx.Mode(); err != nil || mode != extension.ModeTUI {
		t.Fatalf("unmounted mode=%q error=%v", mode, err)
	}
	if hasUI, err := ctx.HasUI(); err != nil || hasUI {
		t.Fatalf("unmounted hasUI=%v error=%v", hasUI, err)
	}
	m.tuiInst = tui.NewWithOutput(io.Discard, 80, 24)
	m.layout = tui.NewContainer()
	m.wireInprocContextActions()
	if mode, err := ctx.Mode(); err != nil || mode != extension.ModeTUI {
		t.Fatalf("mounted mode=%q error=%v", mode, err)
	}
	if hasUI, err := ctx.HasUI(); err != nil || !hasUI {
		t.Fatalf("mounted hasUI=%v error=%v", hasUI, err)
	}
}
