# Forge

> **A context engine for code, in your terminal.**

Forge is a coding agent that gives a language model the **full working context** of your project — file tree, environment, git state, memory files, system prompt, and the conversation history — and turns that context into actions. It runs in your terminal, talks to any OpenAI-compatible API, and ships as a single static binary you can drop on any machine.

Most agents treat context as an afterthought: a system prompt, a tool result, repeat. Forge treats it as the primary product. The system prompt is *assembled* per-run (env + git + `FORGE.md` discovery, layered on top of the user's overrides). Tool results are streamed, structured, and tagged with read-only intent. Every graceful exit writes the conversation to `~/.forge/conversations/` so the next session can pick it up. The point isn't to be a cleverer model — it's to make whatever model you're using *see more of what's actually going on*.

---

## Highlights

- **Interactive REPL with full history.** Bubbletea-powered TUI, multi-line input, up-arrow history with draft preservation, the works. Or run `forge -p "..."` for a one-shot.
- **Provider-agnostic.** Any OpenAI-compatible endpoint — NVIDIA NIM, OpenAI, OpenRouter, Ollama, an internal gateway. The model id is just a string.
- **Six built-in tools.** `Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep`. Read-only tools are auto-allowed; mutating tools prompt for permission.
- **Per-call permission dialog.** The TUI freezes while a decision is pending. `Allow always` / `Deny always` build an in-memory rule list for the rest of the session; plain `Allow` / `Deny` is one-shot.
- **Three output formats.** `text` for humans, `json` for one-shot consumers, `stream-json` for NDJSON pipelines.
- **Cost tracking.** Input / output / cache tokens + USD estimate, surfaced in every output format and in `/cost`.
- **Resilient.** Exponential backoff with `Retry-After` honored on 429 / 529 / rate-limit responses.
- **Sessions on disk.** Every graceful exit writes `~/.forge/conversations/<uuid>.json`. Phase 3.1 adds the `/resume` browser to load them.
- **Single static binary.** No Go runtime, no DLLs, no installer. ~8.5 MB on Windows.

---

## Quick start

### 1. Build

```bash
# Standard build (links the Go runtime dynamically)
go build -o forge.exe ./cmd/forge

# Fully static, stripped, smaller binary (recommended for distribution)
CGO_ENABLED=0 go build -ldflags="-s -w" -o forge.exe ./cmd/forge
```

The result is a single Windows binary with no external dependencies.

### 2. Set an API key

Forge reads the key from the first non-empty source:

```bash
# 1. Command-line flag (wins everything)
forge.exe -p "hi" --api-key "<key>"

# 2. Environment variables (first non-empty)
export FORGE_API_KEY="<key>"          # any provider
export NVIDIA_API_KEY="<key>"         # NIM (default)
export OPENAI_API_KEY="<key>"         # any OpenAI-compatible endpoint
```

The key is never logged or echoed, even at `--verbose`.

### 3. Choose your mode

```bash
# Interactive REPL — multi-turn conversation with full TUI
forge.exe

# One-shot headless — pipe a prompt, get the answer
forge.exe -p "what is the current directory?"

# Let the model act on the repo (file edits + shell)
forge.exe -p "list the Go files in this repo" --permission-mode=accept-edits

# Stream every event as one NDJSON line (great for piping into other tools)
forge.exe -p "what time is it?" --output-format=stream-json

# Inspect the system prompt the model will actually see
forge.exe --dump-system-prompt
```

> ⚠️ **Headless permissions.** In `-p` mode there is no UI to answer a permission prompt, so `--permission-mode=default` (the default) **denies any non-read-only tool call** and reports the denial back to the model as `is_error: true`. To actually run shell commands in a one-shot, pass `--permission-mode=accept-edits` or `--permission-mode=bypass-permissions` (shorthand: `--dangerously-skip-permissions`). The interactive TUI doesn't have this problem — it shows a real dialog.

---

## The TUI

Launch `forge.exe` with no args. The screen splits into a scrollable message pane, a multi-line input, and a status line. Type a question, hit `Enter`, watch the answer stream. Type `/help` to see the slash commands.

```
┌────────────────────────────────────────────────────────────────┐
│ Forge                                                          │
│                                                                │
│ > What files are in this directory?                            │
│                                                                │
│ The current directory contains:                                │
│   • go.mod, go.sum (Go module files)                           │
│   • cmd/forge/main.go, headless.go (binary entry)               │
│   • internal/core, cli, api, tools, query, tui, commands       │
│   ...                                                          │
│                                                                │
│ 4 tools used · 1,247 in / 384 out · $0.0023   [claude-sonnet]  │
├────────────────────────────────────────────────────────────────┤
│ > Type a message or /command…                                  │
└────────────────────────────────────────────────────────────────┘
```

When the model wants to call a tool, the TUI shows a centered permission dialog. Use `Tab` / `Shift+Tab` to move, `Enter` to activate, `Esc` to deny. The dialog freezes everything else while it's up.

```
                ╭─ Permission needed: Bash ─────────────────╮
                │                                          │
                │ rm -rf /tmp/foo                          │
                │                                          │
                │ Command:                                 │
                │   rm -rf /tmp/foo                        │
                │                                          │
                │ ▶ Allow    Allow always    Deny          │
                │             Deny always                  │
                │                                          │
                │  tab next  shift+tab prev  enter act     │
                │  esc deny                               │
                ╰──────────────────────────────────────────╯
```

### Slash commands

| Command | What it does |
| ------- | ------------ |
| `/help [command]` | List all commands (or show help for one) |
| `/clear` | Wipe the visible conversation (keeps the underlying history) |
| `/compact` | Manually trigger context compaction |
| `/status` | Show the active model, message count, cost |
| `/cost` | Show input / output / cache token totals + USD |
| `/model` | Show the active model (Phase 3.1: switch models) |
| `/config` | Dump the resolved config as JSON |
| `/permissions [mode]` | Show or change the permission mode (`default` / `accept-edits` / `bypass-permissions` / `plan`) |
| `/thinking [on\|off\|budget]` | Toggle extended thinking |
| `/init` | Create a `FORGE.md` memory file in the current directory |
| `/diff` | Show the working tree diff (stub — Phase 3.1) |
| `/exit` (alias `/quit`) | Save the session and quit |

The `/dump-config` and `/dump-history` hidden commands are useful for bug reports.

---

## Tools

Forge ships with six tools. Read-only tools (`Read`, `Glob`, `Grep`) are auto-allowed in any permission mode; the others prompt.

| Tool | Purpose | Mutating |
| ---- | ------- | -------- |
| `Bash` | Run a shell command (`cmd /c` on Windows, `bash -c` on Unix) | yes |
| `Read` | Read a file with line numbers and a configurable limit | no |
| `Write` | Create or overwrite a file | yes |
| `Edit` | String-replace edits (single, multi-occurrence, all-occurrences) | yes |
| `Glob` | Find files by pattern (`**/*.go`, etc.) | no |
| `Grep` | Search file contents with regex, file globs, and context lines | no |

The `Edit` and `Write` tools share a small helper layer in `internal/tools/editutil` for the actual file-rewrite logic.

---

## Output formats

| Format | Best for | Notes |
| ------ | -------- | ----- |
| `text` (default) | Humans at a terminal | Streams text deltas to stdout; cost summary to stderr |
| `json` | Programmatic consumers that want one document | One JSON object: `text`, `tool_uses`, `usage`, `turns`, `outcome`, `cost` |
| `stream-json` | Programmatic consumers that want every event as it happens | NDJSON: `stream_event`, `tool_start`, `tool_end`, `turn_complete`, `outcome`, `cost`, `end` |

All three carry the loop's real terminal `outcome` (`end_turn`, `max_tokens`, `cancelled`, or `error`) — no hardcoded values.

---

## Configuration

| Flag | Description | Default |
| ---- | ----------- | ------- |
| `-p`, `--print` | Run headless with a positional prompt (or read from stdin) | — |
| `--api-key` | API key (overrides all env vars) | — |
| `--api-base` | API base URL | NIM default |
| `--model` | Model id to send in every request | `meta/llama-3.3-70b-instruct` |
| `--max-tokens` | Per-response output cap (hard ceiling: 32k) | 4096 |
| `--max-turns` | Cap on agentic turns per session | 10 |
| `--output-format` | One of `text`, `json`, `stream-json` | `text` |
| `--permission-mode` | `default` / `accept-edits` / `bypass-permissions` / `plan` | `default` |
| `--dangerously-skip-permissions` | Shorthand for `--permission-mode=bypass-permissions` | — |
| `--system-prompt` | Replace the default system prompt | — |
| `--append-system-prompt` | Append to the default system prompt | — |
| `--resume` | Resume a saved session (Phase 3.1) | — |
| `--cwd` | Working directory for tool execution | `$PWD` |
| `--no-auto-compact` | Disable the auto-compaction trigger math | on |
| `--no-project-memory` | Skip `FORGE.md` discovery | off |
| `--dump-system-prompt` | Print the assembled system prompt and exit | — |
| `-v`, `--verbose` | Raise log level to DEBUG | WARN |

`forge.exe --help` is the canonical, always-up-to-date reference.

---

## Project layout

```
.
├── cmd/forge/                  binary entry point
│   ├── main.go                 flag parsing + run() dispatch
│   ├── headless.go             -p one-shot mode
│   └── *test.go                hermetic tests
├── internal/
│   ├── core/                   shared types, config, settings, cost tracker, session persistence
│   ├── cli/                    argument parsing + settings layering
│   ├── api/                    canonical Request/StreamEvent + Provider interface
│   │   └── openai/             OpenAI-compatible adapter (NIM by default)
│   ├── tools/                  built-in tools (Bash, Read, Write, Edit, Glob, Grep)
│   │   └── editutil/           shared file-rewrite helpers
│   ├── query/                  RunQueryLoop, auto-compaction, transient-error retry
│   ├── tui/                    bubbletea Model, View, Update, permission dialog
│   ├── commands/               slash-command framework + baseline commands
│   └── integration tests, fixtures, …
└── (private working notes are gitignored)
```

The dependency graph is strictly one-directional — no cycles:

```
cli → query → tools → core
        ↓        ↗
       api  →  core
```

---

## Testing

```bash
# All tests — hermetic, no network access required
go test ./...

# Verbose output for one package
go test -v ./internal/tui/...

# Just the end-to-end NIM integration test
go test -run TestRunQueryLoopEndToEndNIM ./internal/query/...
```

Every component is unit-tested in isolation. The full NIM round-trip is covered by an `httptest`-driven integration test in `internal/query/integration_test.go`. The TUI is exercised through pure-value tests that build a `*Model` and call `Update` / `View` directly — no terminal required.

---

## Security

> **Important:** During early planning, a real `nvapi-…` key was accidentally pasted into the chat. **That key is treated as compromised.** It must be rotated in the NIM console. **Do not commit that key to source, settings, or any file.** Use the `NVIDIA_API_KEY` env var (or `--api-key` on the command line) to provide the rotated key. The literal string `nvapi-…` must never appear in this repository again.

Additional notes:

- API keys are resolved from env vars or the `--api-key` flag; they are never logged, even at `--verbose`.
- The `Bash` tool runs commands as the current user. Use `--permission-mode=bypass-permissions` only in trusted, scripted contexts.
- Session files at `~/.forge/conversations/<id>.json` are written mode 0600. They contain the full conversation including any code the user pasted; treat them with the same care as shell history.

---

## Troubleshooting

**`forge: no API key found.`**
Set one of `FORGE_API_KEY`, `NVIDIA_API_KEY`, or `OPENAI_API_KEY` (or pass `--api-key`).

**The model returned an answer but didn't run the tool I asked for.**
In `-p` mode (headless), `--permission-mode=default` denies all non-read-only tools. Pass `--permission-mode=accept-edits` or `--permission-mode=bypass-permissions`. In the TUI, a dialog will pop — just press `Enter` on `Allow`.

**`stream-json` output looks like it has extra noise at the end.**
The final lines are `cost` (your usage) and `end` (a heartbeat). They are always emitted so consumers can know the stream is complete.

**`go build` fails on a Mac / Linux machine.**
The binary is Windows-first. Use the standard `go build -o forge ./cmd/forge` (no `.exe` suffix) on non-Windows. Cross-platform binaries are on the roadmap.

---

## License

Internal project. All rights reserved until a public license is chosen.
