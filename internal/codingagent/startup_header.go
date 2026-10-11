// Ports packages/coding-agent/src/modes/interactive/interactive-mode.ts.
package codingagent

import (
	"strings"

	"pi/internal/coding/piversion"
	"pi/tui"
)

// headerContainer keeps host spacing outside the replaceable header component, including when a custom header renders no rows.
func (m *InteractiveMode) headerContainer() *tui.Container {
	if !m.opts.LoginVisible {
		return tui.NewContainer(m.extHeader)
	}
	return tui.NewContainer(tui.NewSpacer(1), m.extHeader, tui.NewSpacer(1))
}

// renderBuiltInHeader renders the current startup help expansion. Verbose seeds this state only at initialization; tool toggles and header restoration subsequently select it.
func (m *InteractiveMode) renderBuiltInHeader(width int) []string {
	theme := tui.ActiveTheme()
	var bindings *tui.TUIKeybindingsManager
	if m.keybindings != nil {
		bindings = m.keybindings.merged
	} else {
		bindings = tui.NewKeybindingsManager(keybindingDefinitionsFor(tui.HostKeybindingPlatform()), nil)
	}
	key := func(action string) string {
		return tui.FormatKeyText(strings.Join(bindings.GetKeys(action), "/"), false)
	}
	rawHint := func(key, description string) string {
		return themeFg(theme.Dim, key) + themeFg(theme.Muted, " "+description)
	}
	hint := func(action, description string) string { return rawHint(key(action), description) }
	version := themeFg(theme.Dim, " v"+piversion.Version)
	logo := themeBold(themeFg(theme.Accent, "pi")) + version
	m.toolMu.Lock()
	expanded, showDetails := m.builtInHeaderExpanded, m.builtInHeaderShowDetails
	m.toolMu.Unlock()
	var instructions string
	if expanded {
		instructions = strings.Join([]string{
			hint("app.interrupt", "to interrupt"),
			hint("app.clear", "to clear"),
			rawHint(key("app.clear")+" twice", "to exit"),
			hint("app.exit", "to exit (empty)"),
			hint("app.suspend", "to suspend"),
			hint("tui.editor.deleteToLineEnd", "to delete to end"),
			hint("app.thinking.cycle", "to cycle thinking level"),
			rawHint(key("app.model.cycleForward")+"/"+key("app.model.cycleBackward"), "to cycle models"),
			hint("app.model.select", "to select model"),
			hint("app.tools.expand", "to expand tools"),
			hint("app.thinking.toggle", "to expand thinking"),
			hint("app.editor.external", "for external editor"),
			rawHint("/", "for commands"),
			rawHint("!", "to run bash"),
			rawHint("!!", "to run bash (no context)"),
			hint("app.message.followUp", "to queue follow-up"),
			hint("app.message.dequeue", "to edit all queued messages"),
			hint("app.clipboard.pasteImage", "to paste image or text"),
			rawHint("drop files", "to attach"),
		}, "\n")
		text := logo + "\n" + instructions
		return tui.NewPaddedText(text, 1, 0, nil).Render(width)
	}
	instructions = strings.Join([]string{
		hint("app.interrupt", "interrupt"),
		rawHint(key("app.clear")+"/"+key("app.exit"), "clear/exit"),
		rawHint("/", "commands"),
		rawHint("!", "bash"),
		hint("app.tools.expand", "more"),
	}, themeFg(theme.Muted, " · "))
	_ = showDetails
	onboarding := themeFg(theme.Dim, "Press "+key("app.tools.expand")+" to show full startup help and loaded resources.")
	text := logo + "\n" + instructions + "\n" + onboarding
	return tui.NewPaddedText(text, 1, 0, nil).Render(width)
}

// builtInHeaderLogoArea is the clickable logo of the built-in header as last drawn.
type builtInHeaderLogoArea struct {
	visible       bool
	column, row   int
	columns, rows int
}

// themeBold mirrors upstream theme.bold.
func themeBold(text string) string {
	return "\x1b[1m" + text + "\x1b[22m"
}
