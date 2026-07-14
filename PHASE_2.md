# Phase 2 — Headless Agent Loop with NIM (OpenAI-compatible) Provider

> This is a live status log. I update it as I finish each step.
> The work below is governed by the plan in the project spec; this
> file is the running checklist of what's been built and what's
> left to do.

## What Phase 2 is

Phase 2 turns the Phase 1 static `forge.exe` skeleton into a
working **headless agent** that talks to a real model. Per the
README roadmap, Phase 2 ships:

- a single-provider agentic loop (Bash tool + text streaming),
- the OpenAI-compatible HTTP/SSE adapter used against NVIDIA NIM
  (`https://integrate.api.nvidia.com/v1`),
- a real headless mode (`-p` / `--print`) that resolves a model,
  API key, and system prompt, then drives the query loop,
- three output formats (text, json, stream-json),
- settings persistence (`~/.forge/settings.json`),
- auto-compaction trigger math (the real summarizer is Phase 4),
- the OpenAI-compatible adapter that Phase 4 will reuse for
  Anthropic's OpenAI-compatible surface, Azure, LM Studio, etc.

The end state: `forge -p "what is the cwd?" --model
meta/llama-3.3-70b-instruct --api-key ...` works end-to-end
against a real NIM model, streams the answer, and supports the
Bash tool.

## What's built so far

(updated as work progresses)

- [x] **Step 1 — Extend core (context, settings, cost, prompt)**
  - [x] `internal/core/prompt.go` — `BaseSystemPrompt()` (moved
        out of `cmd/forge/main.go`).
  - [x] `internal/core/context.go` — `build_system_context`,
        `build_user_context`, `BuildSystemPrompt`. Walks upward
        to find every `FORGE.md`. Includes a short git
        status/log when the working dir is a repo.
  - [x] `internal/core/settings.go` — `Settings.Load` /
        `Settings.Save` with 0600 perms; `LayerSettings` merger
        (managed > local > project > global).
  - [x] `internal/core/cost.go` — `ModelPricing`,
        `PricingFor`, `EstimateCostUSD`, `CostTracker` with
        lock-free atomic counters and a one-line summary.
  - [x] `internal/core/thinking.go` — `ThinkingConfig`
        (Enabled bool, BudgetTokens int).
  - [x] Default model constant switched to
        `meta/llama-3.3-70b-instruct`; default API base to
        `https://integrate.api.nvidia.com/v1`.
  - [x] `context_test.go`, `settings_test.go`, `cost_test.go`
        all pass.
- [x] **Step 2 — CLI settings loader**
  - [x] `internal/cli/settings_load.go` — `LoadSettings`,
        `LoadLayeredSettings` (global + project + local),
        `ApplySettings` (CLI > settings > default).
  - [x] `settings_load_test.go` covers all four layering cases.
- [x] **Step 3 — internal/api package**
  - [x] `internal/api/doc.go` — package overview.
  - [x] `internal/api/request.go` — canonical `Request`,
        `SystemPrompt` (string or blocks), `SystemBlock`,
        `CacheControl`, `ThinkingConfig` (alias of core's).
  - [x] `internal/api/events.go` — canonical `StreamEvent` tagged
        union (MessageStart, ContentBlockStart/Delta/Stop,
        MessageDelta, MessageStop, Error) with per-event
        `String()` and `JSON()` for NDJSON rendering.
  - [x] `internal/api/provider.go` — `Provider` interface,
        `ModelInfo`, `ModelRegistry` with 4 known models + 128k
        default fallback.
  - [x] `internal/api/fake.go` — `FakeProvider` for tests with
        `ScriptTextResponse`, `ScriptToolCallThenText`, and
        `ScriptAlwaysToolCall` helpers.
  - [x] `internal/api/fake_test.go` — channel closes on
        `EventMessageStop`, scripts emit in order, replay
        behavior on overflow, ctx-cancel handling.
- [x] **Step 4 — internal/tools package**
  - [x] `internal/tools/doc.go`.
  - [x] `internal/tools/tool.go` — `Tool` interface, `ToolContext`,
        `ToolResult`, `AllTools`, `FindTool`.
  - [x] `internal/tools/bash.go` — Bash implementation with
        timeout, truncation, combined stdout+stderr, separate
        exit-code metadata, Unix (`bash -c`) and Windows
        (`cmd /c`) shells.
  - [x] `internal/tools/bash_test.go` — success, non-zero exit,
        timeout, truncation (timeout/cancel tests skip on
        Windows; the Phase-3 hardening pass will use
        CREATE_NEW_PROCESS_GROUP to make kill propagate to
        cmd /c children).
  - [x] `internal/tools/editutil/count.go` — `CountOccurrences`
        helper for the future Edit tool.
  - [x] `internal/tools/editutil/count_test.go`.
- [x] **Step 5 — internal/query package**
  - [x] `internal/query/doc.go`.
  - [x] `internal/query/loop.go` — `RunQueryLoop` (canonical
        spec §5.1 loop body: turn cap, cancellation, event
        drain, accumulate, branch on stop reason, tool
        execution, panic recovery). Tool lookup uses the
        passed-in `toolsList`, not the global registry, so
        tests can inject their own tools.
  - [x] `internal/query/accumulator.go` — `accumulator` rebuilds
        a canonical `core.Message` from the canonical
        `StreamEvent` stream; tolerates out-of-order deltas;
        materializes tool-use input from partial-JSON deltas.
  - [x] `internal/query/compaction.go` —
        `CalculateTokenWarningState`, `ShouldAutoCompact`
        (with circuit-breaker), `CompactConversation` (stub),
        `AutoCompactIfNeeded`.
  - [x] `internal/query/loop_test.go` — EndTurn, tool
        round-trip, max-turns, permission denied, cancellation,
        tool panic recovery, usage accumulation, event
        emission, empty-stream handling.
  - [x] `internal/query/compaction_test.go` — token warning
        boundaries (Ok / Warning / Critical), circuit breaker,
        CompactConversation stub, AutoCompactIfNeeded
        no-op / below-trigger / above-trigger cases.
- [x] **Step 6 — internal/api/openai adapter**
  - [x] `internal/api/openai/doc.go`.
  - [x] `internal/api/openai/client.go` — `New`, `Stream`
        (POST chat/completions, Authorization header,
        temperature, tool definitions, message
        conversion incl. tool_results, HTTP-error
        classification: 401→KindAuth, 429→KindRateLimit
        with Retry-After parsed, 529→retryable, etc.).
  - [x] `internal/api/openai/stream.go` — SSE parser that
        handles text deltas, tool-call deltas (accumulating
        partial JSON, lazy block starts on first id+name),
        finish_reason → canonical StopReason, `data: [DONE]`,
        and the message-delta + message-stop handshake.
  - [x] `internal/api/openai/registry.go` — re-exports of
        `ContextWindowForModel` and `LookupModel` from the
        canonical api package.
  - [x] `internal/api/openai/client_test.go` — text-only
        turn, tool-call turn, HTTP 401 → `KindAuth`, HTTP
        429 with `Retry-After` → `KindRateLimit` + retryable,
        ctx-cancellation, `parseRetryAfter` (numeric +
        date forms). All replay via `httptest.Server` so
        the suite is hermetic.
- [x] **Step 7 — wire real headless mode in main.go**
  - [x] `cmd/forge/main.go` — settings loader wired before
        `ToConfig()`; the layered load is now non-fatal
        (warn-and-continue) and feeds `ApplySettings`.
  - [x] `cmd/forge/headless.go` — `runHeadless` replaces
        `runHeadlessPlaceholder`. Resolves the API key from
        `--api-key` → `FORGE_API_KEY` → `NVIDIA_API_KEY` →
        `OPENAI_API_KEY`; resolves `--api-base` →
        `FORGE_API_BASE` → `core.DefaultAPIBase`; builds
        the system prompt via `core.BuildSystemPrompt`;
        drives the `query.RunQueryLoop`; drains events
        through one of three renderers (text / json /
        stream-json). A signal-driven `context.Context`
        cancels the in-flight stream on Ctrl+C. Missing
        API key surfaces as a friendly error on stderr
        with exit code 2.
  - [x] `--dump-system-prompt` calls
        `core.BuildSystemPrompt` and prints the real
        assembled prompt (env block + git status + memory
        files).
  - [x] New `--api-base` CLI flag added.
- [x] **Step 8 — README + .env.example**
  - [x] Status bumped to "Phase 2 of 4".
  - [x] Security callout about the leaked `nvapi-…` key
        at the top of the README.
  - [x] "Try it" section with the exact NIM command,
        including PowerShell + bash env-var setup.
  - [x] Layout comment includes `api/`, `api/openai/`,
        `tools/`, `query/`, `headless.go`.
  - [x] `.env.example` added with the full precedence
        list and a security note.
- [x] **Step 9 — verification**
  - [x] `go build ./...` clean.
  - [x] `go vet ./...` clean.
  - [x] `go test ./...` — all tests pass, hermetic.
  - [x] `CGO_ENABLED=0 go build -ldflags="-s -w" -o forge.exe ./cmd/forge`
        — static binary builds (~6.8 MB).
  - [x] `./forge.exe --version` and `./forge.exe --help`
        unchanged.
  - [x] `./forge.exe --dump-system-prompt` prints the
        real assembled prompt with platform, cwd, git
        status, and discovered FORGE.md.
  - [x] `./forge.exe -p "..."` with no key returns a
        friendly error on stderr and exit 2.

## What's deferred to later phases

- TUI REPL (Phase 3).
- Slash commands (Phase 3).
- Auto-compaction real summarizer (Phase 4 — stub here,
  trigger math here).
- Cost tracker USD pricing source (Phase 4 — built-in table
  here).
- MCP, ACP, bridge, plugins, Cinder (Phase 4).
- Anthropic-native adapter (Phase 4 — OpenAI-compatible covers
  NIM today).
- File/search tools (Read, Edit, Write, Glob, Grep) (Phase
  2.1 / Phase 3 — Edit helper is shipped in Phase 2 to keep
  the diff small when it lands).
- OAuth / device-code auth flows (Phase 4).
- Linux / macOS distribution (Phase 4).
- Sessions, branching, rewind (Phase 4).
- Hook execution (Phase 4).
- Sub-agents / cron / goal loop (Phase 4).
