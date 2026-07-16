# Forge

> **Agentic coding, forged in your terminal.**

Forge is an open-source coding agent that preserves your working context across models, providers, and sessions. It runs in your terminal, talks to any OpenAI-compatible API, and ships as a single static binary you can drop on any machine.

[![Go](https://img.shields.io/badge/Go-1.26.5-00ADD8?logo=go&logoColor=white)](#)
[![License](https://img.shields.io/badge/license-Internal-lightgrey)](#)
[![Phase](https://img.shields.io/badge/phase-2%20of%204-yellow)](#)
[![Platform](https://img.shields.io/badge/platform-Windows-0078D6?logo=windows)](#)

---

## ✨ Features

- 🧠 **Agentic loop** — multi-turn reasoning with tool use, panic-recovery, and a configurable turn cap.
- 🔌 **OpenAI-compatible provider** — works with NVIDIA NIM, OpenAI, OpenRouter, Ollama, or any local gateway.
- 🛠️ **Built-in `Bash` tool** — real shell execution (Windows: `cmd /c`, Unix: `bash -c`) with a panic-safe wrapper.
- 📤 **Three output formats** — `text`, `json`, and `stream-json` for both humans and programmatic consumers.
- 💰 **Cost tracking** — input/output/cache tokens + USD estimate, surfaced in every output format.
- 🔁 **Resilient** — exponential backoff with `Retry-After` honored on 429 / 529 / rate-limit responses.
- 📦 **Single static binary** — no Go runtime, no DLLs, no installer. Copy and run.
- 🔐 **Permission model** — `default`, `accept-edits`, `bypass-permissions`, and `plan` modes, scoped per-call.
- 🪶 **Hermetic tests** — every component tested with `httptest`, no network access required.

---

## 📋 Project status

| Phase | Scope | Status |
| ----- | ----- | ------ |
| 1 | Skeleton — `core` + `cli` packages, all flags wired | ✅ Complete |
| 2 | **Headless agent loop (current)** — NIM adapter, `Bash` tool, three output formats, auto-compaction math, transient-error retry | ✅ Complete |
| 3 | TUI REPL with bubbletea, slash commands, permission dialogs | ⏳ Planned |
| 4 | Multi-provider adapters, MCP, ACP, plugins, Cinder, sessions, branching, compaction, bridge, distribution | ⏳ Planned |

---

## 🚀 Quick start

### 1. Build

```bash
# Standard build (links the Go runtime dynamically)
go build -o forge.exe ./cmd/forge

# Fully static, stripped, smaller binary (recommended for distribution)
CGO_ENABLED=0 go build -ldflags="-s -w" -o forge.exe ./cmd/forge
```

The result is a single ~6.5 MB Windows binary with no external dependencies.

### 2. Configure your API key

Forge accepts the key from any of these (first non-empty wins):

```bash
# 1. Command-line flag
./forge.exe -p "hi" --api-key "<key>"

# 2. Environment variables (in this order)
export FORGE_API_KEY="<key>"          # any provider
export NVIDIA_API_KEY="<key>"         # NIM (default)
export OPENAI_API_KEY="<key>"         # any OpenAI-compatible endpoint
```

The key is never logged or echoed to the terminal.

### 3. Run a one-shot

```bash
# Plain text answer to a single question
./forge.exe -p "what is the current directory?"

# Ask the model to do something (use --permission-mode for shell access — see below)
./forge.exe -p "list the Go files in this repo" --permission-mode=accept-edits

# Stream every event as one NDJSON line (great for piping into other tools)
./forge.exe -p "what time is it?" --output-format=stream-json

# Inspect the system prompt the model will see
./forge.exe --dump-system-prompt
```

> ⚠️ **Headless permission modes.** `--permission-mode` defaults to `default`, which **denies any non-read-only tool call** (including `Bash`) without a prompt. Since headless mode has no UI to answer a prompt, the deny is final and the tool result is reported back to the model as `is_error: true`. To actually run shell commands in a one-shot, pass one of:
> ```bash
> # Auto-allow file edits and shell execution
> ./forge.exe -p "run the test suite" --permission-mode=accept-edits
>
> # Auto-allow everything (shorthand: --dangerously-skip-permissions)
> ./forge.exe -p "run the test suite" --permission-mode=bypass-permissions
> ```
> The `--dangerously-skip-permissions` flag is shorthand for `bypass-permissions`.

---

## 📤 Output formats

| Format | Best for | Notes |
| ------ | -------- | ----- |
| `text` (default) | Humans at a terminal | Streams text deltas to stdout; cost summary to stderr |
| `json` | Programmatic consumers that want one document | Single JSON object: `text`, `tool_uses`, `usage`, `turns`, `outcome`, `cost` |
| `stream-json` | Programmatic consumers that want every event as it happens | NDJSON: `stream_event`, `tool_start`, `tool_end`, `turn_complete`, `outcome`, `cost`, `end` |

All three carry the loop's real terminal `outcome` (`end_turn`, `max_tokens`, `cancelled`, or `error`) — no hardcoded values.

---

## ⚙️ Configuration

| Flag | Description | Default |
| ---- | ----------- | ------- |
| `-p`, `--print` | Run headless with a positional prompt (or read from stdin) | — |
| `--api-key` | API key (overrides all env vars) | — |
| `--api-base` | API base URL (overrides `FORGE_API_BASE`) | NIM default |
| `--model` | Model id to send in every request | `meta/llama-3.3-70b-instruct` |
| `--max-tokens` | Per-response output cap (hard ceiling: 32k) | 4096 |
| `--max-turns` | Cap on agentic turns per session | 10 |
| `--output-format` | One of `text`, `json`, `stream-json` | `text` |
| `--permission-mode` | `default` / `accept-edits` / `bypass-permissions` / `plan` | `default` |
| `--dangerously-skip-permissions` | Shorthand for `--permission-mode=bypass-permissions` | — |
| `--system-prompt` | Replace the default system prompt | — |
| `--append-system-prompt` | Append to the default system prompt | — |
| `--cwd` | Working directory for tool execution | `$PWD` |
| `--no-auto-compact` | Disable the auto-compaction trigger math | on |
| `--no-project-memory` | Skip `FORGE.md` discovery | off |
| `--dump-system-prompt` | Print the assembled system prompt and exit | — |
| `-v`, `--verbose` | Raise log level to DEBUG | WARN |

Run `./forge.exe --help` for the canonical, always-up-to-date list.

---

## 🏗️ Project layout

```
.
├── FORGE_BUILD_SPEC.md         the behavioral spec we're building from
├── PHASE_2.md                  (gitignored) live status log
├── go.mod                      module: github.com/ArpitK24/forge
├── cmd/forge/                  binary entry point
│   ├── main.go                 flag parsing + run()
│   ├── headless.go             real headless mode (Phase 2)
│   └── headless_test.go        headless + retry tests
├── internal/
│   ├── core/                   shared types (§2.1–§2.6) + cost tracker
│   ├── cli/                    argument parsing + settings layering
│   ├── api/                    canonical Request/StreamEvent + Provider iface
│   │   └── openai/             OpenAI-compatible adapter (NIM)
│   ├── tools/                  built-in tools (Bash in Phase 2)
│   │   └── editutil/           shared helpers for write/edit tools
│   ├── query/                  RunQueryLoop + auto-compaction math
│   │   └── integration_test.go end-to-end NIM test (httptest)
│   ├── tui/                    (Phase 3) terminal UI
│   ├── commands/               (Phase 3) slash commands
│   ├── mcp/                    (Phase 4) Model Context Protocol
│   ├── acp/                    (Phase 4) Agent Client Protocol
│   ├── bridge/                 (Phase 4) remote session sync
│   ├── buddy/                  (Phase 4) Cinder companion
│   └── plugins/                (Phase 4) plugin loader + marketplace
└── docs/                       (later) design docs
```

The dependency graph (spec §1) is strictly one-directional:

```
cli → query → tools → core
        ↓        ↗
       api  →  core
```

---

## 🧪 Testing

```bash
# All tests (hermetic — no network access required)
go test ./...

# Just the end-to-end NIM integration test
go test -run TestRunQueryLoopEndToEndNIM ./internal/query/...

# Verbose output for one package
go test -v ./internal/query/...
```

Every component is unit-tested in isolation, and the full NIM round-trip is covered by an `httptest`-driven integration test in `internal/query/integration_test.go`.

---

## 🔒 Security

> **Important:** During early planning, a real `nvapi-…` key was accidentally pasted into the chat. **That key is treated as compromised.** The user will rotate it themselves in the NIM console. **Do not commit that key to source, settings, or any file.** Use the `NVIDIA_API_KEY` env var (or `--api-key` on the command line) to provide the rotated key. The literal string `nvapi-…` must never appear in this repository again.

Additional notes:

- API keys are resolved from env vars or the `--api-key` flag; they are never logged, even at `--verbose`.
- The `Bash` tool runs commands as the current user. Use `--permission-mode=bypass-permissions` only in trusted, scripted contexts.
- A `git`-style ignore file (`.forgeignore`) is planned for Phase 3 so the agent can be told which paths to skip.

---

## ❓ Troubleshooting

**`forge: no API key found.`**
Set one of `FORGE_API_KEY`, `NVIDIA_API_KEY`, or `OPENAI_API_KEY` (or pass `--api-key`). See [Quick start → Configure your API key](#2-configure-your-api-key).

**The model returned an answer but didn't run the tool I asked for.**
You are in `--permission-mode=default` (the default), which denies non-read-only tools in headless mode. Pass `--permission-mode=accept-edits` or `--permission-mode=bypass-permissions`.

**`stream-json` output looks like it has extra noise at the end.**
The final lines are `cost` (your usage) and `end` (a heartbeat). They are always emitted so consumers can know the stream is complete.

**Build fails on a Mac / Linux machine.**
The Phase 2 binary is Windows-first. Use the standard `go build -o forge ./cmd/forge` (no `forge.exe`) on non-Windows. Cross-platform support is on the Phase 4 roadmap.

---

## 📜 License

Internal project. All rights reserved until a public license is chosen in a later phase.

---

## ✅ Phase 2 verification checklist

- [x] `go build ./...` clean
- [x] `go vet ./...` clean
- [x] `go test ./...` — all tests pass, hermetic
- [x] `CGO_ENABLED=0 go build -ldflags="-s -w" -o forge.exe ./cmd/forge` builds
- [x] `forge.exe --version` and `forge.exe --help` unchanged
- [x] `forge.exe --dump-system-prompt` prints the real assembled prompt (env block + git status + memory files)
- [x] `forge.exe -p "..."` resolves the API key from env and starts the loop
- [x] Three output formats work: text, json, stream-json — and the `OutcomeEvent` carries the real `outcome`, `turns`, and `usage` instead of a hardcoded `"end_turn"`
- [x] Cost summary is suppressed on stderr in `json` / `stream-json` mode (it lives in the JSON document); printed to stderr only in `text` mode
- [x] Retry on transient errors (`KindRateLimit` / HTTP 429 / HTTP 529) with `Retry-After` honored and exponential backoff (1s, 2s, 4s, 8s + jitter, capped at 4 attempts)
- [x] `Bash` tool runs against the local shell (Windows: `cmd /c`, Unix: `bash -c`)
- [x] Tool execution panic-recovery + permission denial + max-turns cap all verified in tests
- [x] End-to-end NIM integration test (httptest → `openai.Client` → `query.RunQueryLoop`) verifies the tool-call round-trip in `internal/query/integration_test.go`
- [x] No leaked `nvapi-…` literal in the source tree (gated by the security callout above)
