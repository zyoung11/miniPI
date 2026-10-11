package codingagent

import "pi/tui"

// Ports packages/coding-agent/src/modes/interactive/interactive-mode.ts:renderProjectTrustWarningIfNeeded.
func (m *InteractiveMode) renderProjectTrustWarningIfNeeded() {
	if m.projectTrusted() || !HasTrustRequiringProjectResources(m.opts.CWD) {
		return
	}
	if !m.chatContainer.IsEmpty() {
		m.chatContainer.Add(tui.NewSpacer(1))
	}
	// pi divergence (D2): the warning names pi's configuration directory and restart command.
	message := "This project is not trusted. Project " + ConfigDirName() + " resources and packages are ignored. Use /trust to save a trust decision, then restart pi."
	m.chatContainer.Add(themedNotice("warning", message, 1))
}
