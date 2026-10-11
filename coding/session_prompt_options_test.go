package coding

import (
	"reflect"
	"testing"

	"pi/coding/extension"
)

// Pi 0.87.1 agent-session.ts:1371-1394 (_rebuildSystemPrompt) builds the base options from the tool registry and the resource loader. The expected values below were observed from the Pi oracle running before_agent_start with `--append-system-prompt APPENDED` and one AGENTS.md in the working directory.
func TestSessionSystemPromptOptionsCarryRegistryAndResources(t *testing.T) {
	resources := &SystemPromptResources{
		AppendSystemPrompt: "APPENDED",
		ContextFiles:       []extension.SystemPromptContextFile{{Path: "/work/AGENTS.md", Content: "project rules here\n"}},
		Skills:             []extension.SystemPromptSkill{{Name: "review", Description: "Review code", FilePath: "/skills/review/SKILL.md", BaseDir: "/skills/review"}},
	}
	h := newRecoveryHarness(t, harnessOptions{defaultTools: true, resources: resources})
	options := h.session.GetSystemPromptOptions()

	if options.CustomPrompt != "" {
		t.Errorf("customPrompt = %q; Pi leaves it undefined without a SYSTEM.md or --system-prompt", options.CustomPrompt)
	}
	if options.AppendSystemPrompt != "APPENDED" {
		t.Errorf("appendSystemPrompt = %q", options.AppendSystemPrompt)
	}
	if !reflect.DeepEqual(options.ContextFiles, resources.ContextFiles) {
		t.Errorf("contextFiles = %+v", options.ContextFiles)
	}
	if !reflect.DeepEqual(options.Skills, resources.Skills) {
		t.Errorf("skills = %+v", options.Skills)
	}
	if want := h.session.ActiveToolNames(); !reflect.DeepEqual(options.SelectedTools, want) {
		t.Errorf("selectedTools = %v want %v", options.SelectedTools, want)
	}
	for name, want := range map[string]string{
		"read":  "Read file contents",
		"bash":  "Execute bash commands (ls, grep, find, etc.)",
		"edit":  "Make precise file edits with exact text replacement, including multiple disjoint edits in one call",
		"write": "Create or overwrite files",
		"grep":  "Search file contents for patterns (respects .gitignore)",
		"find":  "Find files by glob pattern (respects .gitignore)",
		"ls":    "List directory contents",
	} {
		if got := options.ToolSnippets[name]; got != want {
			t.Errorf("toolSnippets[%s] = %q want %q", name, got, want)
		}
	}
	wantGuidelines := map[string][]string{
		"read":  {"Use read to examine files instead of cat or sed."},
		"write": {"Use write only for new files or complete rewrites."},
		"edit": {
			"Use edit for precise changes (edits[].oldText must match exactly)",
			"When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls",
			"Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.",
			"Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions.",
		},
	}
	for name, want := range wantGuidelines {
		if got := options.ToolGuidelines[name]; !reflect.DeepEqual(got, want) {
			t.Errorf("toolGuidelines[%s] = %q want %q", name, got, want)
		}
	}
	// Pi keeps a tool without guidelines out of toolGuidelines (agent-session.ts:3186-3192).
	for name, got := range options.ToolGuidelines {
		if len(got) == 0 {
			t.Errorf("toolGuidelines[%s] is present but empty", name)
		}
	}
}

// A default Session reports no custom prompt and includes registry metadata.
func TestSessionSystemPromptOptionsWithoutResourcesUseRegistry(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{defaultTools: true})
	if got := h.session.GetSystemPromptOptions(); got.CustomPrompt != "" || len(got.ToolSnippets) == 0 {
		t.Fatalf("default prompt options = %+v", got)
	}
}

// Pi's rebuild replaces the resource state; reloaded skills reach the next before_agent_start.
func TestSessionSetSystemPromptResourcesUpdatesBaseOptions(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{defaultTools: true, resources: &SystemPromptResources{CustomPrompt: "custom"}})
	if got := h.session.GetSystemPromptOptions().CustomPrompt; got != "custom" {
		t.Fatalf("customPrompt = %q", got)
	}
	h.session.SetSystemPromptResources(SystemPromptResources{CustomPrompt: "custom", Skills: []extension.SystemPromptSkill{{Name: "s", FilePath: "/s"}}})
	if got := h.session.GetSystemPromptOptions().Skills; len(got) != 1 || got[0].Name != "s" {
		t.Fatalf("skills = %+v", got)
	}
}

// Caller level: a Node extension over the production subprocess wire receives Pi's option content in before_agent_start. forceSystemPrompt reaches a later handler after an earlier one returns systemPrompt (runner.ts:1346-1348).
