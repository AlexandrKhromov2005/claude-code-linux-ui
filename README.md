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
- Projects = working directory + config + memory + a set of threads. Threads
  share the project's `cwd` and `CLAUDE.md`.
- Cross-thread project memory: after each turn the exchange is folded into a
  shared per-project memory (a cheap background summary) and injected into every
  thread, so a fact stated in one thread is known in the others. Editable and
  toggleable; the manual `memory.md` is merged in too.
- Persistent history: transcripts and the Claude session id survive restarts;
  threads can be resumed.
- File and image attachments via a picker or `@path` references; web uploads are
  streamed, so large archives work (cap configurable).
- Remembered permissions stored per project and passed back to Claude, so an
  approved pattern is not asked again. Deny rules win over allow.
- Per-project model selection (`opus`, `sonnet`, `haiku`, `opusplan`, `fable`, …
  via `--model`) and reasoning effort (`low`/`medium`/`high`/`xhigh`/`max`, plus
  `ultracode`, via `--effort`).
- Optional `--dangerously-skip-permissions` toggle for agent mode ("act without
  asking"), off by default and persisted once enabled.
- History search, thread export to Markdown, themes, and an optional spend
  warning.
- In the web client header: context-window usage, subscription rate-limit
  (5-hour / weekly) status, session cost, and pickers for model and effort.

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

The web header carries the project and model pickers, an effort selector, the
chat/agent toggle and a skip-permissions toggle, plus a context-usage bar,
5-hour / weekly rate-limit chips, and the session cost. Settings (the gear) hold
the theme, spend limit, thread export, the manual project memory, and the
cross-thread auto-memory (view / clear / on-off). From the sidebar you can open
any directory as a project; connecting one opens it in agent mode.

Note: subscription rate-limit chips show the binding window's status and reset
time only — the headless CLI does not expose a percentage. The context bar
reflects the context window, not the subscription limit.

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
  `projects/<slug>/{project.toml, memory.md, auto-memory.md, threads/<id>.json}`.
  `memory.md` is the manual project memory; `auto-memory.md` is the
  automatically maintained cross-thread memory; the two are combined into a
  generated `memory.runtime.md` that is injected into each thread.

Data from the previous `claude-tui` directories is migrated automatically on
first run when the new location does not exist yet.

`config.toml` keys: `claude_bin`, `default_model`, `default_mode`, `theme`,
`last_project`, `budget_warn_usd`, `effort`, `skip_permissions`,
`max_upload_mb` (web upload cap, MB; 0 = built-in 1 GiB),
`auto_memory_disabled`. Environment overrides: `CLAUDE_BIN`, `CLAUDE_TUI_MODEL`.

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
