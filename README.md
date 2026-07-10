# Forge

> Agentic coding, forged in your terminal.

A multi-provider terminal coding agent. See [`FORGE_BUILD_SPEC.md`](./FORGE_BUILD_SPEC.md) for the full behavioral spec.

## Status: Phase 1 of 4

- ✅ **Phase 1 — Skeleton (current):** `core` package (errors, messages, config, permissions, constants), `cli` package (all §7.1 flags wired), `forge.exe` binary that builds clean, vets clean, and tests pass.
- ⏳ Phase 2 — Single provider (Anthropic native), `query` agentic loop, `Bash` tool, real headless mode.
- ⏳ Phase 3 — TUI REPL with bubbletea, full command set, permission dialogs.
- ⏳ Phase 4 — Multi-provider adapters, MCP, ACP, plugins, Cinder, sessions, branching, compaction, bridge, distribution.

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

## Run

```bash
./forge.exe --version
./forge.exe --help
./forge.exe -p "what is the current directory?"
echo "refactor the auth module" | ./forge.exe -p
./forge.exe --dump-system-prompt
```

## Layout

```
.
├── FORGE_BUILD_SPEC.md      the spec we're building from
├── go.mod                   module: github.com/ArpitK24/forge
├── cmd/forge/main.go        binary entry point
├── internal/
│   ├── core/                shared types (§2.1–§2.4, §2.6)
│   ├── cli/                 argument parsing (§7.1)
│   ├── api/                 (Phase 2) multi-provider client
│   ├── tools/               (Phase 2) tool implementations
│   ├── query/               (Phase 2) agentic loop
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
At Phase 1 only `core` and `cli` are populated; the rest are empty
directories that get filled in their respective phases.

## Phase 1 verification checklist

- [x] `go build ./...` clean
- [x] `go vet ./...` clean
- [x] `go test ./...` — all tests pass (core + cli)
- [x] Binary is static (CGO_ENABLED=0) and runs on any Windows machine
- [x] All 17 flags from spec §7.1 parse correctly
- [x] All 4 permission modes parse and route correctly
- [x] All 3 output formats parse correctly
- [x] Invalid flags / values produce exit code 2 + usage on stderr
- [x] `--version` prints the spec's identity (§0)
- [x] `--help` documents every flag
- [x] `--dump-system-prompt` shows the resolved config
- [x] Stdin-piped prompts work
