# Forge

> Agentic coding, forged in your terminal.

A multi-provider terminal coding agent. See [`FORGE_BUILD_SPEC.md`](./FORGE_BUILD_SPEC.md) for the full behavioral spec.

## Status: Phase 2 of 4

- ✅ **Phase 1 — Skeleton:** `core` + `cli` packages, `forge.exe` builds clean, all flags wired.
- ✅ **Phase 2 — Headless agent loop (current):** real headless mode against NVIDIA NIM (OpenAI-compatible), `Bash` tool, settings persistence, three output formats (text / json / stream-json), query loop with tool execution and panic recovery, auto-compaction trigger math.
- ⏳ Phase 3 — TUI REPL with bubbletea, full command set, permission dialogs.
- ⏳ Phase 4 — Multi-provider adapters, MCP, ACP, plugins, Cinder, sessions, branching, compaction, bridge, distribution.

## Security callout

> **Important:** During early planning, a real `nvapi-…` key was accidentally pasted into the chat. **That key is treated as compromised.** The user will rotate it themselves in the NIM console. **Do not commit that key to source, settings, or any file.** Use the `NVIDIA_API_KEY` env var (or `--api-key` on the command line) to provide the rotated key. The literal string `nvapi-…` must never appear in this repository again.

## Build

```bash
# Standard build (links the Go runtime dynamically — works on any
# Windows machine with no Go install required, per spec §0/§15):
go build -o forge.exe ./cmd/forge

# Fully static, stripped, smaller binary:
CGO_ENABLED=0 go build -ldflags="-s -w" -o forge.exe ./cmd/forge
```

The resulting `forge.exe` is a single static Windows binary. No Go runtime, no DLLs, no interpreter. A user can copy it to any Windows machine and run it.

## Test

```bash
go test ./...
```

## Try it

Set your rotated NVIDIA NIM key as an env var, then run a one-shot:

```bash
# PowerShell
$env:NVIDIA_API_KEY = "<rotated key>"

# bash
export NVIDIA_API_KEY="<rotated key>"

# Then a headless one-shot against the default NIM model
./forge.exe -p "what is the current directory?"

# Stream JSON events for programmatic consumption
./forge.exe -p "what time is it?" --output-format=stream-json

# Bypass all permission prompts (CI / scripted runs)
./forge.exe -p "run the test suite" --dangerously-skip-permissions

# Inspect the prompt the model will see
./forge.exe --dump-system-prompt

# Pipe a multi-line prompt
echo "explain this codebase" | ./forge.exe -p
```

## API key precedence

`--api-key` → `FORGE_API_KEY` → `NVIDIA_API_KEY` → `OPENAI_API_KEY`.
The first non-empty value wins. The key is never logged or echoed.

## Layout

```
.
├── FORGE_BUILD_SPEC.md      the spec we're building from
├── PHASE_2.md               live status log for the current phase
├── go.mod                   module: github.com/ArpitK24/forge
├── cmd/forge/               binary entry point
│   ├── main.go              flag parsing + run() entry point
│   └── headless.go          real headless mode (Phase 2)
├── internal/
│   ├── core/                shared types (§2.1–§2.6) + cost tracker
│   ├── cli/                 argument parsing + settings layering
│   ├── api/                 canonical Request/StreamEvent + Provider iface
│   │   └── openai/          OpenAI-compatible adapter (NIM)
│   ├── tools/               built-in tools (Bash in Phase 2)
│   ├── query/               RunQueryLoop + auto-compaction math
│   ├── tui/                 (Phase 3) terminal UI
│   ├── commands/            (Phase 3) slash commands
│   ├── mcp/                 (Phase 4) Model Context Protocol
│   ├── acp/                 (Phase 4) Agent Client Protocol
│   ├── bridge/              (Phase 4) remote session sync
│   ├── buddy/               (Phase 4) Cinder companion
│   └── plugins/             (Phase 4) plugin loader + marketplace
└── docs/                    (later) design docs
```

The dependency graph (spec §1) is strictly one-directional:

```
cli → query → tools → core
        ↓        ↗
       api  →  core
```

## Phase 2 verification checklist

- [x] `go build ./...` clean
- [x] `go vet ./...` clean
- [x] `go test ./...` — all tests pass, hermetic
- [x] `CGO_ENABLED=0 go build -ldflags="-s -w" -o forge.exe ./cmd/forge` builds
- [x] `forge.exe --version` and `forge.exe --help` unchanged
- [x] `forge.exe --dump-system-prompt` prints the real assembled prompt (env block + git status + memory files)
- [x] `forge.exe -p "..."` resolves the API key from env and starts the loop
- [x] Three output formats work: text, json, stream-json
- [x] Bash tool runs against the local shell (Windows: `cmd /c`, Unix: `bash -c`)
- [x] Tool execution panic-recovery + permission denial + max-turns cap all verified in tests
- [x] No leaked `nvapi-…` literal in the source tree (gated by the security callout above)
