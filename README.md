<pre align="center">
            ╔═╗           ╔═╗ ╔═══════╗ ╔═════╗ 
╔═════════╗ ╚═╝ ╔═══════╗ ╚═╝ ║ ╔═══╗ ║ ╚═╗ ╔═╝ 
║ ╔═╗ ╔═╗ ║ ╔═╗ ║ ╔═══╗ ║ ╔═╗ ║ ╚═══╝ ║   ║ ║   
║ ║ ║ ║ ║ ║ ║ ║ ║ ║   ║ ║ ║ ║ ║ ╔═════╝   ║ ║   
║ ║ ║ ║ ║ ║ ║ ║ ║ ║   ║ ║ ║ ║ ║ ║       ╔═╝ ╚═╗ 
╚═╝ ╚═╝ ╚═╝ ╚═╝ ╚═╝   ╚═╝ ╚═╝ ╚═╝       ╚═════╝ 
</pre>
miniPI is a minimal coding agent for the terminal, built as a single native Go binary. It reads your `~/.pi` configuration directly, starts instantly, and needs no Node.js.

It keeps what matters in a coding agent: the agent loop, tools, skills, JSONL sessions. Everything else — extensions, MCP, RPC mode, package management, self-update — is gone.

## Lineage

miniPI is not a from-scratch project. It descends directly from Pi:

```
pi  →  PiG  →  miniPI
```

- [pi](https://github.com/earendil-works/pi) is the original minimal, extensible agent harness by Mario Zechner and Earendil Works. Its behavior is the reference: miniPI targets upstream pi 1.0.3 semantics.
- [PiG](https://github.com/MichaelKinsy/PiG) is a pure-Go port of pi. miniPI is a heavily trimmed fork of PiG: the Go engine, the TUI and the parity-tested core come from there.
- **miniPI** cuts the fork down to the essentials and rebrands it as what it always was — a `pi`.

## What is inside

- **Agent loop** with tools (`read`, `bash`, `edit`, `write`), compaction, and sub-session support
- **Skills** discovered from `~/.pi/agent/skills`, project `.pi/skills`, and `--skill` paths
- **JSONL sessions** with resume, fork, branch tree and export
- **Fullscreen TUI** with differential rendering, Kitty keyboard protocol, syntax highlighting, markdown, LaTeX, mermaid and images
- **llama.cpp support** with chat-template thinking detection and per-level probing
- **Full `~/.pi` compatibility** — settings, auth, models, themes and keybindings files are shared with pi as-is

## Getting started

Build it:

```bash
./build.sh
```

Then run it in any project:

```bash
cd /path/to/project
pi
```

Sign in to a provider with `/login`, or point at your own endpoints in `~/.pi/agent/models.json`. Local llama.cpp models appear automatically when the server is running.

## Notes

- The TUI runs in fullscreen by default. `PgUp`/`PgDn` scroll by page, `Alt+o`/`Alt+i` scroll up and down, `Ctrl+Home`/`Ctrl+End` jump to the edges, and everything is rebindable in `~/.pi/agent/keybindings.json`.
- Themes are pi theme JSON files. `~/.pi/agent/themes/` is picked up automatically; `dark-legacy` restores the classic pi 0.84 dark palette.
- `quietStartup: "header"` in settings silences offline-provider warnings at launch.
- Version numbers follow miniPI itself (`0.1.0`); behavior targets upstream pi `1.0.3`.

## Credits

miniPI stands on the work of two upstreams: [pi](https://github.com/earendil-works/pi) by Mario Zechner and Earendil Works, and the Go port [PiG](https://github.com/MichaelKinsy/PiG) by Michael Kinsy. The bundled pi source excerpts carry their own license at `tui/widthx/testdata/pi/LICENSE.pi`.

## License

MIT
