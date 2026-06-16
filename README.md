# claude-code-linux-ui

A terminal client for Claude on Linux: a chat/agent hybrid built on top of the
Claude Code CLI in headless mode (`claude -p`). Conversations are grouped into
projects that share a working directory and context; in agent mode every file
edit and shell command passes through an approval modal before it runs.

The code is split into a UI-agnostic core (`internal/core`) and thin clients on
top of it: a terminal UI (`internal/tui`) and a local web client (`internal/web`
plus a Svelte frontend in `web/`). The core depends on no UI or transport
package.

## Features

- Streaming chat with markdown rendering.
- Two modes, switchable on the fly:
  - `chat` — read-only (`Read`, `Grep`, `Glob`); nothing is mutated.
  - `agent` — full toolset; each non-pre-approved action is shown as a diff
    (edits) or command (Bash) and waits for allow / remember+allow / deny.
- Projects = working directory + config + a memory file + a set of threads.
  Threads share the project's `cwd` and `CLAUDE.md`.
- Persistent history: transcripts and the Claude session id survive restarts;
  threads can be resumed.
- File and image attachments via a picker or `@path` references.
- Remembered permissions stored per project and passed back to Claude, so an
  approved pattern is not asked again. Deny rules win over allow.
- History search, thread export to Markdown, themes, and an optional spend
  warning.

## Requirements

- Linux, a 256-color terminal.
- Go 1.24+ to build.
- The `claude` CLI installed and authenticated (`claude` on `PATH`, or set
  `CLAUDE_BIN`).

## Build

    go build -o claude-code-linux-ui ./cmd/claude-code-linux-ui

To bundle the web client into the binary, build the frontend first and pass the
`embed_ui` tag (see "Web client" below).

## Run

    ./claude-code-linux-ui

On first start in a directory that is not yet a project, the project switcher
offers to use the current folder. Existing projects are reachable with `Ctrl+P`.

## Web client

The same binary can serve a local web UI over the same core:

    ./claude-code-linux-ui serve [addr]    # default 127.0.0.1:8765

It prints a URL with a per-session token in the fragment; open it in a browser.
The server binds loopback only, authenticates every API request and WebSocket
upgrade with the token, and enforces a strict Host/Origin allowlist. For remote
access use an SSH tunnel; do not expose the port.

To embed the built client so `serve` is self-contained:

    cd web && npm install && npm run build && cd ..
    go build -tags embed_ui -o claude-code-linux-ui ./cmd/claude-code-linux-ui

Without `embed_ui` the server runs and the API works, but `/` shows a
placeholder. For frontend development with hot reload, run the Vite dev server
and point the Go server at it:

    cd web && npm run dev          # Vite on :5173
    CCLU_DEV_SERVER=http://localhost:5173 ./claude-code-linux-ui serve

## Modes

`chat` is the default and cannot modify files. Press `Tab` (or `Ctrl+G`, or
`/mode agent`) to switch to `agent`, where the app runs an in-process approval
server: Claude routes each gated tool call back to the modal, which shows the
diff or command. Choose `allow`, `remember+allow` (saves an editable rule to the
project), or `deny`.

## Key bindings

| Key | Action |
| --- | --- |
| `Enter` | send |
| `Ctrl+J` | newline |
| `Tab` / `Ctrl+G` | toggle chat / agent |
| `Ctrl+P` | projects |
| `Ctrl+T` | threads |
| `Ctrl+O` | attach a file |
| `Esc` | cancel the response / close an overlay |
| `PgUp` / `PgDn` | scroll |
| `Ctrl+C` | quit |

In the approval modal: `a` allow, `r` remember+allow, `d` deny.

## Commands

`/project [name]`, `/new`, `/threads`, `/resume <id>`, `/mode chat|agent`,
`/search <text>`, `/export [path]`, `/memory`, `/attach <path>`, `/files
[clear]`, `/detach [N]`, `/theme [name]`, `/budget [usd]`, `/mcp`, `/help`,
`/quit`. Inside a message, `@/path` is passed to Claude Code as is.

## Configuration and data

- Config: `$XDG_CONFIG_HOME/claude-code-linux-ui/config.toml` (defaults to
  `~/.config/claude-code-linux-ui/`).
- Data: `$XDG_DATA_HOME/claude-code-linux-ui/` (defaults to
  `~/.local/share/claude-code-linux-ui/`), laid out as
  `projects/<slug>/{project.toml, memory.md, threads/<id>.json}`.

Data from the previous `claude-tui` directories is migrated automatically on
first run when the new location does not exist yet.

`config.toml` keys: `claude_bin`, `default_model`, `default_mode`, `theme`,
`last_project`, `budget_warn_usd`. Environment overrides: `CLAUDE_BIN`,
`CLAUDE_TUI_MODEL`.

Other MCP servers configured in Claude Code (`~/.claude.json`, `.mcp.json`) are
inherited automatically.

## Notes

The app drives Claude Code on a subscription; from 2026-06-15 usage is billed
against the monthly Agent SDK credit rather than the interactive limit. Set
`/budget <usd>` to be warned once a session crosses a threshold.

Each turn spawns a fresh `claude -p` process and continues the conversation with
`--resume`. A persistent bidirectional stream is a possible future change but is
not used here.

## Tests

    go test ./...

Unit tests cover the project store, event/permission JSON contract, and rule
generation. Integration tests that drive the real `claude` binary are gated
behind `CLAUDE_LIVE=1`.
