package main

import (
	"pi/coding/extension"
	"pi/internal/codingagent"
)

// skillLoadResult holds the outcome of resolving and loading all skills for a
// session: from --skill flags and convention dirs.
type skillLoadResult struct {
	// Defs are the fully-loaded skill definitions, ready for the system prompt
	// and /skill: command registration.
	Defs []*codingagent.SkillDef
	// Paths are the source paths from which Defs were loaded (for the
	// SkillPaths interactive-mode option and hot-reload).
	Paths       []string
	Diagnostics []extension.ResourceDiagnostic
}

// resolveAndLoadSkills loads every resolved skill input for the session. The
// loadSkills call inside always passes false for noSkills: explicitly-resolved
// inputs always load.
func resolveAndLoadSkills(collectedSkillInputs []string) (*skillLoadResult, error) {
	defs, diagnostics, err := loadSkills(collectedSkillInputs, false)
	return &skillLoadResult{Defs: defs, Paths: collectedSkillInputs, Diagnostics: diagnostics}, err
}
