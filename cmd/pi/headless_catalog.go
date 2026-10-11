package main

import (
	"context"
	"strings"

	"pi/coding/extension"
	"pi/internal/codingagent"
	"pi/internal/codingagent/llama"
)

// RPCSlashCommand is one slash command as getCommands reports it.
type RPCSlashCommand = codingagent.PiSlashCommand

// headlessCommandRunner executes the extension commands a headless mode lists.
type headlessCommandRunner interface {
	Commands() []extension.ResolvedCommand
	Command(string) (extension.ResolvedCommand, bool)
	ExecuteCommand(context.Context, string, string) bool
	EmitError(*extension.ExtensionError)
}

// headlessCommandCatalog routes the prompts of print and JSON mode as
// upstream AgentSession.prompt does: an extension command runs instead of
// prompting, and other text has skill commands and prompt templates expanded.
// It also lists the commands upstream getCommands reports.
type headlessCommandCatalog struct {
	runner headlessCommandRunner
	// mode is the command context's mode: "print" or "json".
	mode string
	// llama runs the built-in llama.cpp extension's /llama command, which the
	// runner lists with the other extension commands; notify carries its
	// ctx.ui.notify calls to the client.
	llama           *llama.Host
	notify          func(message, kind string)
	promptTemplates []codingagent.PromptTemplate
	skills          []*codingagent.SkillDef
	cwd             string
	agentDir        string
	sourceInfo      map[string]codingagent.ResourceSourceInfo
}

// commands lists the catalog as upstream getCommands does.
func (c headlessCommandCatalog) commands() []RPCSlashCommand {
	return c.slashCatalog().Commands()
}

// slashCatalog is the shared getCommands catalog over this command set.
func (c headlessCommandCatalog) slashCatalog() codingagent.SlashCommandCatalog {
	catalog := codingagent.SlashCommandCatalog{
		PromptTemplates: c.promptTemplates, Skills: c.skills,
		CWD: c.cwd, AgentDir: c.agentDir, SourceInfo: c.sourceInfo,
	}
	if c.runner != nil {
		catalog.Runner = c.runner
	}
	return catalog
}

// extensionCommand resolves a leading extension command invocation.
func (c headlessCommandCatalog) extensionCommand(message string) (string, string, bool) {
	if !strings.HasPrefix(message, "/") {
		return "", "", false
	}
	requestedNameAndArgs := message[1:]
	requestedName, args, found := strings.Cut(requestedNameAndArgs, " ")
	if !found {
		args = ""
	}
	if c.runner == nil {
		return "", "", false
	}
	for _, command := range c.runner.Commands() {
		if strings.TrimPrefix(command.InvocationName, "/") == requestedName {
			return command.InvocationName, args, true
		}
	}
	return "", "", false
}

// expandPrompt expands skill commands and prompt templates in the message.
func (c headlessCommandCatalog) expandPrompt(message string) string {
	if expanded, ok, err := codingagent.ExpandSkillCommand(message, c.skills); ok {
		message = expanded
	} else if err != nil && c.runner != nil {
		c.runner.EmitError(err)
	}
	if expanded, ok := codingagent.ExpandPromptTemplate(message, c.promptTemplates); ok {
		message = expanded
	}
	return message
}

// routePrompt runs a command invocation or returns the expanded prompt.
func (c headlessCommandCatalog) routePrompt(ctx context.Context, message string) (string, bool) {
	if name, args, ok := c.extensionCommand(message); ok {
		return "", c.executeCommand(ctx, name, args)
	}
	return c.expandPrompt(message), false
}

// executeCommand runs a command extensionCommand resolved; the built-in
// llama.cpp extension's /llama runs the llama host with this mode's command
// context.
func (c headlessCommandCatalog) executeCommand(ctx context.Context, name, args string) bool {
	if command, found := c.runner.Command(name); found && c.llama != nil && codingagent.IsLlamaCommand(command) {
		_ = c.llama.HandleCommand(llama.CommandContext{Ctx: ctx, Mode: c.mode, Notify: c.notify})
		return true
	}
	return c.runner.ExecuteCommand(ctx, name, args)
}
