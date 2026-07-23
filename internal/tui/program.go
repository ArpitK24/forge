package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/api/openai"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/query"
	"github.com/ArpitK24/forge/internal/tools"
)

// programState holds the long-lived state the TUI's Update loop
// reads but doesn't own directly: the API provider, the system
// prompt, the event channel, and the loop's cancel function. It
// is set up once by RunProgram and shared with the Model via the
// Model.loopBridge field.
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

	// 3. Set up raw mode + alt screen. The restore func is
	// called on every exit path.
	restore, err := setupRawMode()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}

	// 4. Construct the model with the bridge state.
	m := InitialModel(cfg, cost)
	m.bridge = &programState{
		Provider:     provider,
		SystemPrompt: systemPrompt,
		Logger:       logger,
	}
	if providerErr != nil {
		m.Messages = append(m.Messages, renderedMessage{
			Role: "error",
			Text: fmt.Sprintf("API provider unavailable: %v (set NVIDIA_API_KEY / FORGE_API_KEY / OPENAI_API_KEY or pass --api-key)", providerErr),
		})
	}

	// 5. Run the bubbletea program. Wrap in panic-recovery so a
	// crash in Update still restores the terminal.
	p := tea.NewProgram(m, tea.WithAltScreen())
	withPanicRestore(restore, func() {
		if _, err := p.Run(); err != nil {
			logger.Error("bubbletea program exited with error", "err", err)
		}
	})
	restore()
	return nil
}

// buildProvider constructs the OpenAI-compatible provider from the
// resolved Config. Returns an error if the API key is missing; the
// caller decides whether to surface it or proceed without a
// provider (the TUI still works for /commands).
func buildProvider(cfg *core.Config, logger *slog.Logger) (api.Provider, error) {
	apiKey := resolveAPIKey(cfg)
	if apiKey == "" {
		return nil, fmt.Errorf("no API key found")
	}
	base := cfg.APIBase
	if base == "" {
		base = core.DefaultAPIBase
	}
	return openai.New(base, apiKey, cfg.EffectiveModel()), nil
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
		toolsList := tools.AllTools()
		tc := &tools.ToolContext{
			WorkingDir:  cfg.WorkingDir,
			CostTracker: cost,
			Cfg:         cfg,
			SessionID:   "",
			CheckPermission: func(name, desc string, isReadOnly bool) core.PermissionDecision {
				// Phase 3 Step 5 wires the interactive dialog here.
				// For now, defer to the AutoPermissionHandler so
				// the loop runs to completion in non-interactive
				// test contexts.
				h := &core.AutoPermissionHandler{Mode: cfg.PermissionMode}
				return h.RequestPermission(core.PermissionRequest{
					ToolName:    name,
					Description: desc,
					IsReadOnly:  isReadOnly,
				})
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

	// Return the subscribe Cmd: block on the next event, wrap it.
	return waitForEvent(eventCh)
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

// loopCtx is retained for API compatibility but unused now that
// startQueryLoop builds its own cancellable context. Removed from
// the Update flow; kept here in case a future caller wants a
// non-cancellable context.
func (m Model) loopCtx() context.Context {
	return context.Background()
}
