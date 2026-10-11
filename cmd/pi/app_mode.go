package main

import (
	"os"

	"pi/coding/extension"

	"golang.org/x/term"
)

// appMode is the run mode selected at startup. Mirrors upstream main.ts AppMode.
type appMode string

const (
	appModeInteractive appMode = "interactive"
	appModePrint       appMode = "print"
	appModeJSON        appMode = "json"
)

// resolveAppMode mirrors upstream main.ts resolveAppMode: --mode json selects
// that mode; --print, a stdin that is not a terminal, or a stdout that is not a
// terminal select print mode; anything else is interactive. `pi "q" > file`
// therefore prints the answer instead of drawing the TUI into the file.
func resolveAppMode(mode string, print, stdinIsTTY, stdoutIsTTY bool) appMode {
	switch {
	case mode == "json":
		return appModeJSON
	case print || !stdinIsTTY || !stdoutIsTTY:
		return appModePrint
	default:
		return appModeInteractive
	}
}

// isPlainRuntimeMetadataCommand preserves stdout only for metadata without explicit mode/print options.
// Ports packages/coding-agent/src/main.ts:129-130.
func isPlainRuntimeMetadataCommand(flags CLIFlags) bool {
	return flags.Print == "" && !flags.modeSet && (flags.Help || flags.ListModelsAll || flags.ListModels != "")
}

// processAppMode resolves the app mode for this process's flags and standard streams.
func processAppMode(flags CLIFlags) appMode {
	return resolveAppMode(flags.Mode, flags.Print != "",
		term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())))
}

// extensionMode maps the app mode to the extension run mode exposed as
// ctx.mode, with "interactive" rendered as ModeTUI.
func (m appMode) extensionMode() extension.ExtensionMode {
	switch m {
	case appModeJSON:
		return extension.ModeJSON
	case appModePrint:
		return extension.ModePrint
	default:
		return extension.ModeTUI
	}
}
