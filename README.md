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
- Subagent visibility. When the model fans work out to subagents, each one is
  listed while it runs: what it is doing, which tool it last used, how long it
  has been working and how long since it last said anything. A subagent goes
  quiet for as long as one step takes, so silence is reported as silence rather
  than as failure — but a long gap is visible, and anything still running when
  the turn ends is marked as cut short instead of spinning forever. The list
  stays on screen after the turn so the outcome is readable (see "Subagents").
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
- Thread handoff: fold a long conversation into a transferable summary and
  continue it in a fresh thread, so the transcript stops being replayed on
  every turn (see "Token cost").
- Supervised background jobs: hand a build, a test sweep or a fuzzing run to the
  server so it outlives the turn — and the server — and reports back when it is
  done (see "Long-running work").
- In the web client header: context-window usage, prompt-cache hit rate and
  session token count, subscription rate-limit (5-hour / weekly) status, session
  cost, connection health, and pickers for model and effort. The sidebar shows
  what each thread has cost so far.

## Long-running work

Each turn is one `claude -p` process, and everything it starts dies with it —
including Claude Code's own background shells. Measured: a script launched with
`run_in_background` stopped a few seconds in, the moment the turn ended. So work
that takes longer than an answer cannot live inside a turn at all.

Such work is handed to the server instead. In agent mode Claude gets five tools
(`mcp__jobs__start`, `wait`, `status`, `logs`, `stop`), and the system prompt
tells it to use them for anything beyond a couple of minutes. A job:

- **runs detached**, in its own session, writing output and its exit status
  straight to disk — so it survives the turn *and* the server being killed;
- **is visible while it runs**: the panel above the transcript shows each job's
  command, elapsed time, last output line, and how long it has been quiet, with
  a stop button and a log view;
- **reports back when it ends**: the conversation that started it is woken with
  the exit code and the tail of the output, in the same session, so work picks
  up where it left off;
- **can be waited on mid-turn**: `mcp__jobs__wait` parks the tool call until
  the job ends and returns the outcome, with nothing spent while parked. This
  is how a subagent — which dissolves with the turn and can never receive the
  wake-up — collects the result of a job it launched. An ending collected
  through wait is not announced a second time.

If the server was down or another project was open when a job finished, the
result is held and delivered the next time that project is opened, rather than
lost. A job that finishes while a turn is still running in its thread does not
barge in either: the report waits for the turn to end, one report per wake-up,
so two turns never race for one session. The wake-up turn is bounded in time
and asks only for a report — it will not start new work on its own. Set
`job_notify_disabled = true` in `config.toml` to keep the supervision and drop
the automatic reply.

The wait call streams its response (headers at once, keepalive comments while
parked) and the jobs MCP entry carries its own request `timeout`. Both matter:
measured against claude 2.1.235, a silent HTTP tool call is otherwise cut at
60 seconds per request and at about five minutes of idle connection, whatever
the tool timeout settings say.

Jobs and their logs live in `~/.local/share/claude-code-linux-ui/jobs/` and are
kept for a week.

## Token cost

Each turn spawns `claude -p --resume`, which replays the whole session. A turn
therefore does not cost a fixed amount — it costs more as the thread grows.
A few things follow from that, and the client is built around them:

- **Watch the cache.** The header shows what share of input tokens came from the
  prompt cache. A settled thread sits high; a sustained low number means the
  prompt is churning and the history is being re-sent at full price. The
  per-thread totals in the sidebar show where the spend actually went.
- **Hand off long threads.** Once the context bar passes half full, the
  `⤳ новый контекст` button folds the thread into a summary and continues in a
  new one. The old thread stays readable.
- **Background upkeep is kept cheap.** Cross-thread memory runs as a stripped
  side call — no tools, no MCP servers, no CLAUDE.md discovery, its own system
  prompt — because running it as a normal `claude -p` cost about 30k input
  tokens per turn for a text-rewriting task. It also skips small talk and
  batches bursts into a single update.
- **Model and effort dominate everything else.** `--model` and `--effort` are in
  the header for that reason; `max` effort on a large model is the most
  expensive thing in the app by a wide margin.
- **Subagents bill through the same turn.** A turn that fans out reports its
  cost several times, each figure covering the whole invocation so far rather
  than the latest slice. Only the difference is added, so the session total is
  what the CLI actually charged and not a multiple of it.

Set `CCLU_DEBUG=1` to log each turn's command line and token usage to stderr.

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

It prints a URL with a bearer token in the fragment; open it in a browser. The
server binds loopback only, authenticates every API request and WebSocket
upgrade with the token, and enforces a strict Host/Origin allowlist. For remote
access use an SSH tunnel; do not expose the port.

The token is stored in `~/.config/claude-code-linux-ui/token` (mode 0600) and
reused across restarts, so a rebuild does not invalidate open tabs — the token
lives in each tab's URL, and a tab holding an old one cannot recover by
reloading. The trade is that a leaked link stays valid until the token is
replaced. To replace it:

    ./claude-code-linux-ui serve --new-token

Every previously issued link stops working. The token is also replaced
automatically if the file is ever found readable by anyone but its owner, and
the server says on startup whether existing links still work.

To embed the built client so `serve` is self-contained:

    cd web && npm install && npm run build && cd ..
    go build -tags embed_ui -o claude-code-linux-ui ./cmd/claude-code-linux-ui

Without `embed_ui` the server runs and the API works, but `/` shows a
placeholder. For frontend development with hot reload, run the Vite dev server
and point the Go server at it:

    cd web && npm run dev          # Vite on :5173
    CCLU_DEV_SERVER=http://localhost:5173 ./claude-code-linux-ui serve

The web header carries the project and model pickers, an effort selector, the
chat/agent toggle and a skip-permissions toggle, plus a context-usage bar, the
session token count with its cache hit rate, a thread-handoff button, 5-hour /
weekly rate-limit chips, the connection-health chip and the session cost.
Settings (the gear) hold the theme, spend limit, thread export, the manual
project memory, and the cross-thread auto-memory (view / clear / on-off). From
the sidebar you can open any directory as a project; connecting one opens it in
agent mode.

The connection chip probes the path every turn depends on: it checks for a live
tunnel interface first and only then handshakes with the Anthropic API through
it, so a failing turn can be told apart from a failing link. The probe sends no
request and no credentials.

Note: subscription rate-limit chips show the binding window's status and reset
time only — the headless CLI does not expose a percentage. The context bar
reflects the context window, not the subscription limit.

## Subagents

A subagent is a nested session the model launches through the Agent tool, and it
runs asynchronously: the tool call returns the moment the subagent starts, so the
model can answer, go quiet, and then answer again when the subagent reports back.
A turn like that therefore finishes more than once, and without a separate
account of it the client looks idle while real work is happening.

Both clients keep that account. The web client shows a panel per turn — one row
per subagent, with its type, its job, what it is doing right now, its tool calls
and tokens — and a dot that beats while the subagent is talking, slows when it
has been quiet for 45 seconds and stops at two and a half minutes ("нет
сигнала"). The terminal client shows the same thing condensed into one line
above the input. Threads with subagents still working carry a `⚙ N` badge in the
sidebar, so a fan-out started in one thread stays visible from another.

A quiet subagent is not a dead one: it only speaks between steps, and a single
long tool call looks exactly like silence from outside. The thresholds are set
past what healthy work looks like, and the wording stops at what is actually
known. The one case that is certain is the turn ending with a subagent still
running — cancelled, or the CLI exiting — and that is marked as such.

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
