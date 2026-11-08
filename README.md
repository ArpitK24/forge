# Forge

> **A context engine for code, in your terminal.**

[![CI](https://github.com/ArpitK24/forge/actions/workflows/ci.yml/badge.svg)](https://github.com/ArpitK24/forge/actions/workflows/ci.yml)
[![Security](https://github.com/ArpitK24/forge/actions/workflows/security.yml/badge.svg)](https://github.com/ArpitK24/forge/actions/workflows/security.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/ArpitK24/forge)](https://goreportcard.com/report/github.com/ArpitK24/forge)
[![Go Version](https://img.shields.io/github/go-mod/go-version/ArpitK24/forge)](go.mod)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

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
- **Single static binary.** No Go runtime, no DLLs, no installer. ~8.5 MB on every platform — Windows, Linux, macOS (intel and Apple Silicon).

---

## Quick start

### 1. Build

Use the build script to produce static binaries for every
supported platform in one go:

```bash
./scripts/build.sh
ls dist/
# forge-darwin  forge-darwin-arm64  forge-linux  forge-windows.exe
```

Or build just the binary for your host:

```bash
# Windows
go build -o forge.exe ./cmd/forge

# Linux / macOS
go build -o forge ./cmd/forge

# Fully static, stripped, smaller binary (recommended for distribution)
CGO_ENABLED=0 go build -ldflags="-s -w" -o forge(.exe) ./cmd/forge
```

The result is a single static binary with no external
dependencies. Forge supports Windows, Linux, and macOS
(intel and Apple Silicon); CI on every push to `main`
re-runs `go vet`, `go test`, and `scripts/build.sh` on
all three operating systems.

#### Or run it in Docker

```bash
docker build -t forge .
docker run -it --rm -v "$PWD":/workspace -e FORGE_API_KEY forge
```

The image mounts your project at `/workspace` and runs as a non-root user.

### 2. Set an API key

Forge reads the key from the first non-empty source:

```bash
# 1. Command-line flag (wins everything)
./forge -p "hi" --api-key "<key>"        # Linux / macOS
forge.exe -p "hi" --api-key "<key>"      # Windows

# 2. Environment variables (first non-empty)
export FORGE_API_KEY="<key>"          # any provider
export NVIDIA_API_KEY="<key>"         # NIM (default)
export OPENAI_API_KEY="<key>"         # any OpenAI-compatible endpoint
```

The key is never logged or echoed, even at `--verbose`.

### 3. Choose your mode

```bash
# Interactive REPL — multi-turn conversation with full TUI
./forge            # Linux / macOS
forge.exe          # Windows

# One-shot headless — pipe a prompt, get the answer
./forge -p "what is the current directory?"          # Linux / macOS
forge.exe -p "what is the current directory?"        # Windows

# Let the model act on the repo (file edits + shell)
./forge -p "list the Go files in this repo" --permission-mode=accept-edits

# Stream every event as one NDJSON line (great for piping into other tools)
./forge -p "what time is it?" --output-format=stream-json

# Inspect the system prompt the model will actually see
./forge --dump-system-prompt
```

> ⚠️ **Headless permissions.** In `-p` mode there is no UI to answer a permission prompt, so `--permission-mode=default` (the default) **denies any non-read-only tool call** and reports the denial back to the model as `is_error: true`. To actually run shell commands in a one-shot, pass `--permission-mode=accept-edits` or `--permission-mode=bypass-permissions` (shorthand: `--dangerously-skip-permissions`). The interactive TUI doesn't have this problem — it shows a real dialog.

---

## The TUI

Launch `forge` (or `forge.exe` on Windows) with no args. The screen splits into a scrollable message pane, a multi-line input, and a status line. Type a question, hit `Enter`, watch the answer stream. Type `/help` to see the slash commands.

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
| `-m`, `--model` | Model id to send in every request | `nvidia/nemotron-3.5-lightning-30b-a3b` |
| `--max-tokens` | Per-response output cap (hard ceiling: 200,000) | 8192 |
| `--max-turns` | Cap on agentic turns per session | 10 |
| `--output-format` | One of `text`, `json`, `stream-json` | `text` |
| `--permission-mode` | `default` / `accept-edits` / `bypass-permissions` / `plan` | `default` |
| `--dangerously-skip-permissions` | Shorthand for `--permission-mode=bypass-permissions` | — |
| `-s`, `--system-prompt` | Replace the default system prompt | — |
| `--append-system-prompt` | Append to the default system prompt | — |
| `--resume` | Resume a saved session (Phase 3.1) | — |
| `--cwd` | Working directory for tool execution | `$PWD` |
| `--no-auto-compact` | Disable the auto-compaction trigger math | on |
| `--no-project-memory` | Skip `FORGE.md` discovery | off |
| `--mcp-config` | JSON file of MCP servers to connect at startup | — |
| `--plugin-dir` | Directory to scan for plugins, instead of the default (repeatable) | `~/.forge/plugins` |
| `--dump-system-prompt` | Print the assembled system prompt and exit | — |
| `-v`, `--verbose` | Raise log level to DEBUG | WARN |

Flags can go before or after the prompt, so `forge -p "fix the tests" --model <id>` and `forge --model <id> -p "fix the tests"` are the same. Unquoted words are joined into one prompt (`forge -p fix the tests`). Everything after `--` is prompt text, even if it starts with `-`.

`forge --help` (or `forge.exe --help` on Windows) is the canonical, always-up-to-date reference.

### Settings files

Forge also reads JSON settings files. Later layers override earlier ones, and **CLI flags always win over every file**:

1. `~/.forge/settings.json` — your global defaults (trusted)
2. `<project>/.forge/settings.json` — project settings, usually committed
3. `<project>/.forge/settings.local.json` — per-checkout overrides

A missing file simply contributes nothing. Supported keys: `model`, `max_tokens`, `verbose`, `theme`, `auto_compact` (settings can only turn it *off*; `--no-auto-compact` always wins), `permission_mode` (currently a number: `0` default, `1` accept-edits, `2` bypass-permissions, `3` plan), `mcp_servers`, `permission_rules`, and `trusted_projects` (global file only).

**Project settings are untrusted by default.** Both project files live inside the repository, so anyone who can commit to it controls them. Forge therefore ignores `permission_mode`, `mcp_servers` (which launch processes at startup), `permission_rules` and `trusted_projects` from them and prints a warning naming the ignored keys. To allow those keys for a repository you trust, list its absolute path in your **global** `~/.forge/settings.json`:

```json
{
  "trusted_projects": ["/home/me/src/my-repo"]
}
```

Even for a trusted project, an explicit `--permission-mode` / `--dangerously-skip-permissions` or `--mcp-config` flag still wins.

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

## Troubleshooting

**`forge: no API key found.`**
Set one of `FORGE_API_KEY`, `NVIDIA_API_KEY`, or `OPENAI_API_KEY` (or pass `--api-key`).

**`model '…' is unavailable on this provider (http 410)` or `(http 404)`.**
The provider has retired that model or your key can't use it. NVIDIA retires NIM models on a schedule (the old default, `meta/llama-3.3-70b-instruct`, went end-of-life on 2026-08-26). Pick a current model with `--model <id>` or set `"model"` in `~/.forge/settings.json`. `curl -H "Authorization: Bearer $NVIDIA_API_KEY" https://integrate.api.nvidia.com/v1/models` lists what NIM serves; note that a listed model can still 404 for keys that lack access to it.

**The model returned an answer but didn't run the tool I asked for.**
In `-p` mode (headless), `--permission-mode=default` denies all non-read-only tools. Pass `--permission-mode=accept-edits` or `--permission-mode=bypass-permissions`. In the TUI, a dialog will pop — just press `Enter` on `Allow`.

**`stream-json` output looks like it has extra noise at the end.**
The final lines are `cost` (your usage) and `end` (a heartbeat). They are always emitted so consumers can know the stream is complete.

**`go build` produces a binary that won't run on the target OS.**
Run `./scripts/build.sh` instead — it cross-compiles to Linux,
macOS, and Windows from any host. If you need to build for the
current host only, use `go build -o forge ./cmd/forge` on
Linux/macOS and `go build -o forge.exe ./cmd/forge` on Windows.

**The CI build is failing on one platform but not the others.**
Open the failed run on GitHub Actions; the matrix runs
`go vet`, `go test -count=1`, and `./scripts/build.sh` on
ubuntu-latest / macos-latest / windows-latest independently.
A failure on one leg doesn't cancel the others — every
platform's signal is independently useful.

---

## License

Forge is licensed under the [Apache License 2.0](LICENSE). See [NOTICE](NOTICE).

Contributions are welcome: see [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).
