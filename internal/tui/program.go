package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/api/selector"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/mcp"
	"github.com/ArpitK24/forge/internal/query"
	"github.com/ArpitK24/forge/internal/tools"
)

// programState holds the long-lived state the TUI's Update loop
// reads but doesn't own directly: the API provider, the system
// prompt, the event channel, the loop's cancel function, and the
// per-turn permission-request channel. It is set up once by
// RunProgram and shared with the Model via the Model.loopBridge
// field.
type programState struct {
	Provider     api.Provider
	SystemPrompt string
	Cancel       context.CancelFunc // cancels the in-flight loop, if any
	Logger       *slog.Logger
	// LastOutcome is stashed by the loop goroutine just before it
	// closes the per-turn event channel. handleLoopFinished reads
	// it to reset the Model's streaming state and pick up the
	// final message history.
	LastOutcome *query.Outcome
	// PermReqCh carries permission-check requests from the
	// in-flight query loop to the TUI. The loop's CheckPermission
	// closure posts a permRequest and blocks on the per-request
	// RespondCh; the TUI's Update drains PermReqCh on every
	// permissionRequestMsg and posts the user's decision back.
	// Closed by the loop goroutine on exit.
	PermReqCh chan permRequest
	// McpManager holds the connected MCP servers (Phase 4 step 8).
	// Constructed in RunProgram and connected synchronously
	// BEFORE tea.NewProgram so the first query loop sees the
	// full tool list. Closed on TUI exit via defer. Nil when
	// the user passed no --mcp-config and settings.json has no
	// McpServers.
	McpManager *mcp.Manager
}

// RunProgram is the TUI entry point. It sets up the terminal,
// constructs the bubbletea program, runs it to completion, and
// restores the terminal on exit (including the panic path).
//
// Spec §9. The function blocks until the user quits (Esc-when-
// empty, Ctrl+D-when-empty, or /exit).
func RunProgram(cfg *core.Config, cost *core.CostTracker, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	// 1. Resolve the API provider. The TUI uses the OpenAI-
	// compatible adapter (NIM by default). If the key is missing
	// we still launch the TUI — the user can run /commands and
	// read the transcript, and the first prompt surfaces a clean
	// error instead of crashing.
	provider, providerErr := buildProvider(cfg, logger)

	// 2. Assemble the system prompt once. The TUI reuses it for
	// every turn; live edits (a future /system command) would
	// rebuild it.
	systemPrompt := core.BuildSystemPrompt(cfg, cfg.WorkingDir)

	// 3. Connect MCP servers (Phase 4 step 8). Constructed
	// synchronously and BEFORE tea.NewProgram so the first
	// query-loop turn sees the full tool list. We treat connect
	// failures as soft: per-server failures are recorded on the
	// manager's Errors() map, the rest of the tools still
	// register, and the user sees a clear message in the
	// transcript. A nil manager (no --mcp-config and no
	// settings.McpServers) is fine — tools list stays as AllTools().
	mcpMgr := mcp.NewManager(cfg.McpServers, logger)
	if len(cfg.McpServers) > 0 {
		connectCtx, cancelConnect := context.WithTimeout(context.Background(), 30*time.Second)
		if err := mcpMgr.Connect(connectCtx); err != nil {
			// Partial connect — record but don't crash.
			logger.Warn("mcp: partial connect", "err", err)
		}
		cancelConnect()
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mcpMgr.Close(closeCtx); err != nil {
			logger.Warn("mcp: close", "err", err)
		}
	}()

	// 4. Set up raw mode + alt screen. The restore func is
	// called on every exit path.
	restore, err := setupRawMode()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}

	// 5. Construct the model with the bridge state.
	m := InitialModel(cfg, cost)
	m.bridge = &programState{
		Provider:     provider,
		SystemPrompt: systemPrompt,
		Logger:       logger,
		McpManager:   mcpMgr,
	}
	// Expose the provider to slash commands that issue their own
	// model calls (currently /compact). The provider is fixed
	// for the lifetime of the session, so a one-time assignment
	// is enough; commands read it from m.CmdCtx.Client via the
	// dispatchCommand path.
	m.CmdCtx.Client = provider
	if providerErr != nil {
		m.Messages = append(m.Messages, renderedMessage{
			Role: "error",
			Text: fmt.Sprintf("API provider unavailable: %v (set NVIDIA_API_KEY / FORGE_API_KEY / OPENAI_API_KEY or pass --api-key)", providerErr),
		})
	}
	// Surface MCP connection failures inline so the user
	// knows which servers didn't come up. (Successful
	// connects are silent — the model will start calling
	// their tools.)
	for name, err := range mcpMgr.Errors() {
		m.Messages = append(m.Messages, renderedMessage{
			Role: "error",
			Text: fmt.Sprintf("MCP server %q failed to connect: %v", name, err),
		})
	}

	// 6. Run the bubbletea program. Wrap in panic-recovery so a
	// crash in Update still restores the terminal.
	p := tea.NewProgram(m, tea.WithAltScreen())
	var finalModel tea.Model
	withPanicRestore(restore, func() {
		finalModel, err = p.Run()
		if err != nil {
			logger.Error("bubbletea program exited with error", "err", err)
		}
	})
	// Save the session BEFORE restoring the terminal — any
	// stderr output we produce will still be readable. We
	// recover the final *Model from p.Run()'s return value;
	// bubbletea passes the last value Update produced, which
	// is the most up-to-date snapshot of the conversation.
	saveSessionOnExit(cfg, logger, finalModel)
	restore()
	return nil
}

// buildProvider constructs the active api.Provider from the
// resolved Config. Phase 4 Step 3: routes through the selector
// so an Anthropic-shaped API base URL picks the Anthropic adapter
// and any other base URL keeps the OpenAI-compatible default.
// Returns an error if the API key is missing; the caller decides
// whether to surface it or proceed without a provider (the TUI
// still works for /commands).
func buildProvider(cfg *core.Config, logger *slog.Logger) (api.Provider, error) {
	apiKey := resolveAPIKey(cfg)
	return selector.Select(cfg, apiKey)
}

// resolveAPIKey mirrors headless.go's precedence:
// --api-key → FORGE_API_KEY → NVIDIA_API_KEY → OPENAI_API_KEY.
func resolveAPIKey(cfg *core.Config) string {
	if cfg != nil && cfg.APIKey != "" {
		return cfg.APIKey
	}
	for _, env := range []string{"FORGE_API_KEY", "NVIDIA_API_KEY", "OPENAI_API_KEY"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return ""
}

// --- Model methods that bridge to programState ---

// startQueryLoop returns a tea.Cmd that spawns the query loop on a
// goroutine and returns each event as a tea.Msg until the loop
// terminates. This is bubbletea's "subscribe to a channel" pattern:
// the Cmd blocks on the event channel, returns one event as a msg,
// and Update re-issues the Cmd to wait for the next.
//
// The loop itself runs on its own goroutine so it never blocks
// the TUI's key handling.
//
// CRITICAL: each turn creates its OWN event channel (not the
// bridge's shared one) because the loop closes the channel when
// it terminates. A shared channel would be poisoned for the next
// turn. The per-turn channel is stashed on the Model as
// currentEventCh so Update's re-subscribe can find it.
func (m Model) startQueryLoop() tea.Cmd {
	if m.bridge == nil || m.bridge.Provider == nil {
		// No provider — surface an error immediately and don't
		// spawn anything.
		m.Streaming = false
		m.LoopRunning = false
		return func() tea.Msg {
			return queryEventMsg(query.ErrorEvent{
				Err: core.New(core.KindAPI, "no API provider configured"),
			})
		}
	}

	// Build a cancellable context for this loop run.
	ctx, cancel := context.WithCancel(context.Background())
	m.bridge.Cancel = cancel
	m.LoopCancel = cancel
	m.LoopRunning = true

	// Per-turn event channel. The loop writes to it; the TUI's
	// subscribe Cmd reads from it. Closed by the goroutine when
	// the loop exits.
	eventCh := make(chan query.Event, 64)
	m.currentEventCh = eventCh

	// Per-turn permission-request channel. The loop's CheckPermission
	// closure posts a permRequest here and blocks on the per-request
	// RespondCh; the TUI's Update drains this channel via
	// waitForPermRequest. Buffered (size 4) so a burst of tool
	// calls in one turn doesn't deadlock; the loop's CheckPermission
	// is still called serially so the buffered slots are rarely all
	// used at once.
	permReqCh := make(chan permRequest, 4)
	m.bridge.PermReqCh = permReqCh

	// Snapshot the messages under the mutex so the loop has a
	// stable starting history.
	m.shared.mu.Lock()
	history := append([]core.Message(nil), m.CmdCtx.Messages...)
	m.shared.mu.Unlock()

	// Snapshot everything the goroutine needs into locals so the
	// closure captures values, not the Model (which is copied per
	// Update call — the Model in the closure would be stale).
	provider := m.bridge.Provider
	sysPrompt := m.bridge.SystemPrompt
	cfg := m.Config
	cost := m.Cost

	// Spawn the loop. It owns its own copy of history and
	// returns the final state via the Outcome; we stash the
	// outcome so handleLoopFinished picks it up.
	go func() {
		defer close(eventCh)
		defer close(permReqCh)
		// toolsList is the union of the built-in tools (Read,
		// Glob, Grep, Write, Edit, Bash) and any tools exposed
		// by connected MCP servers (Phase 4 step 8). When no
		// MCP servers were configured, mcpMgr is nil and
		// Tools() returns nil — append is a no-op for nil.
		toolsList := tools.AllTools()
		if mcpMgr := m.bridge.McpManager; mcpMgr != nil {
			toolsList = append(toolsList, mcpMgr.Tools()...)
		}
		tc := &tools.ToolContext{
			WorkingDir:  cfg.WorkingDir,
			CostTracker: cost,
			Cfg:         cfg,
			SessionID:   "",
			// Step 5: replace the headless AutoPermissionHandler
			// with a channel-based bridge to the TUI. The closure
			// still returns a single PermissionDecision, but it
			// blocks on a per-request channel until the user
			// picks. Short-circuit for the auto-allow modes so
			// the dialog never opens in BypassPermissions /
			// AcceptEdits (matches the spec).
			CheckPermission: func(name, desc string, isReadOnly bool) core.PermissionDecision {
				mode := cfg.PermissionMode
				if mode == core.PermissionBypassPermissions ||
					mode == core.PermissionAcceptEdits {
					return core.DecisionAllow
				}
				// Default / Plan: consult the in-memory rule list
				// first so "always" decisions are honored without
				// re-asking the user.
				if rule, ok := matchPermissionRule(cfg.PermissionRules, name, desc); ok {
					return rule.Decision
				}
				// Hand off to the TUI. Read-only tools are still
				// auto-allowed in Default mode (matches the
				// AutoPermissionHandler behavior), so the dialog
				// only opens for tools that actually need a
				// human decision.
				if isReadOnly {
					return core.DecisionAllow
				}
				req := permRequest{
					Request: core.PermissionRequest{
						ToolName:    name,
						Description: desc,
						Details:     nil,
						IsReadOnly:  isReadOnly,
					},
					RespondCh: make(chan core.PermissionDecision, 1),
				}
				select {
				case permReqCh <- req:
					return <-req.RespondCh
				case <-ctx.Done():
					// Loop was cancelled while the request was in
					// flight (e.g. user hit Esc on the streaming
					// turn, which propagates to the loop's ctx).
					// Deny rather than block forever.
					return core.DecisionDeny
				}
			},
		}
		outcome := query.RunQueryLoop(ctx, provider, history, toolsList, tc, query.Config{
			Model:              cfg.EffectiveModel(),
			MaxTokens:          cfg.EffectiveMaxTokens(),
			SystemPrompt:       sysPrompt,
			AppendSystemPrompt: cfg.AppendSystemPrompt,
			MaxTurns:           cfg.EffectiveMaxTurns(),
		}, cost, eventCh)
		// Stash the outcome on the bridge so the channel-close
		// → loopFinishedMsg path can recover it. The bridge
		// pointer is shared between the goroutine and the
		// TUI's Update (single-threaded). The write happens
		// BEFORE the deferred close(eventCh), and the
		// channel close is what unblocks handleLoopFinished
		// (via waitForEvent's "<-ch" returning ok=false). So
		// the close acts as a happens-before edge: the
		// goroutine's write to LastOutcome is visible to
		// handleLoopFinished when it reads the field. No
		// mutex is needed because the goroutine writes
		// exactly once and then exits, and the reader
		// blocks on the channel close before reading.
		m.bridge.LastOutcome = &outcome
	}()

	// Return two subscribe Cmds: one for the loop event stream
	// and one for the permission-request stream. tea.Batch
	// runs them in parallel; bubbletea re-issues each via its
	// own Update response.
	return tea.Batch(
		waitForEvent(eventCh),
		waitForPermRequest(permReqCh),
	)
}

// waitForEvent blocks until an event arrives on ch, then returns
// it as a tea.Msg. bubbletea re-issues the returned Cmd after
// each msg (via Update's response to queryEventMsg), giving us a
// continuous subscription. When the channel closes (loop done),
// returns loopFinishedMsg so Update resets the streaming state.
func waitForEvent(ch chan query.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return loopFinishedMsg{}
		}
		return queryEventMsg(ev)
	}
}

// waitForPermRequest is the parallel subscription for the
// permission-request channel. The query loop's CheckPermission
// closure posts a permRequest and blocks on its RespondCh; this
// Cmd pulls one off the bridge channel and delivers it as a
// permissionRequestMsg so Update can build the dialog state. On
// channel close (loop done) it returns nil — the per-turn channel
// is already drained, the next permReqCh is created on the next
// turn, so there's nothing to do.
func waitForPermRequest(ch chan permRequest) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return permissionRequestMsg{req: req}
	}
}

// loopCtx is retained for API compatibility but unused now that
// startQueryLoop builds its own cancellable context. Removed from
// the Update flow; kept here in case a future caller wants a
// non-cancellable context.
func (m Model) loopCtx() context.Context {
	return context.Background()
}
