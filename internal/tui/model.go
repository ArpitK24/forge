package tui

import (
	"fmt"
	"sync"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/commands"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/query"
	"github.com/ArpitK24/forge/internal/tools"
)

// renderedMessage is a message the TUI displays in the scrollable
// message pane. Not every message has content — status lines and
// tool events are rendered differently from assistant text.
type renderedMessage struct {
	// Role is the sender: "user", "assistant", "system", "tool", or "error".
	Role string
	// Text is the rendered content. For assistant messages this
	// is the streamed text; for tool calls it's the result; for
	// errors it's the error text.
	Text string
	// IsStreaming is true for the in-flight assistant message
	// that is still being streamed. The renderer uses this to
	// show a blinking cursor or spinner.
	IsStreaming bool
}

// Model is the bubbletea model. Spec §9.2 — it owns all visible
// state: the message list, input buffer, scroll position, streaming
// flag, status line, help overlay, and permission dialog state.
//
// The model is a pure-value struct (bubbletea's Elm model
// convention). Every state mutation returns a new (Model, tea.Cmd)
// pair; the runtime diffs the rendered output.
//
// IMPORTANT: because bubbletea copies the Model on every Update,
// this struct MUST NOT contain a sync.Mutex (or any type with an
// internal lock) by value — vet flags it as "copies lock value."
// Shared state that needs locking (the conversation history) lives
// behind a pointer (shared *sharedState), so the lock is shared
// across Model copies rather than copied with them. Per PHASE_3.md
// design decision #4.
type Model struct {
	// --- Config and shared state ---
	Config *core.Config
	Cost   *core.CostTracker
	API    api.Provider // nil until the TUI connects
	Width  int
	Height int

	// --- Shared state (pointer so the lock isn't copied) ---
	// shared owns the conversation history guarded by its mutex.
	// Set once by InitialModel; never nil after that.
	shared *sharedState

	// --- Message pane ---
	Messages   []renderedMessage
	Viewport   viewport.Model
	AutoScroll bool // if true, follow new content automatically

	// --- Input ---
	Input        textarea.Model
	InputHistory []string
	// HistoryIndex is the cursor into InputHistory. -1 means
	// "not currently browsing history" (the input shows the
	// user's draft).
	HistoryIndex int
	// HistoryDraft is what was in the input box when the user
	// first pressed Up-arrow. When the user walks forward
	// past the most-recent entry, this is restored. Matches
	// readline / bash behavior so the user never loses what
	// they were typing.
	HistoryDraft string

	// --- Query-loop state ---
	Streaming    bool
	StreamBuffer string // accumulating assistant text
	LoopCancel   func() // cancels the in-flight loop
	LoopRunning  bool   // a loop goroutine is active

	// --- Status line ---
	Status string

	// --- Help overlay ---
	HelpVisible bool

	// --- Quit ---
	Quitting bool

	// --- Permission dialog (Step 5) ---
	// When a tool call needs approval and the mode is
	// PermissionDefault, the query loop blocks on a per-call
	// channel. The TUI shows a dialog; the user's decision
	// is posted back on the channel. Placeholder for now;
	// Step 5 wires it.
	PermissionDialog *PermissionDialogState

	// --- Commands context ---
	CmdCtx *commands.CommandContext

	// --- Bridge to long-lived program state ---
	// Set once by RunProgram. Carries the provider, the assembled
	// system prompt, the event channel, and the loop's cancel
	// function. The Model reads it; program.go owns it.
	bridge *programState

	// currentEventCh is the per-turn event channel created by
	// startQueryLoop. The Update loop's re-subscribe reads from
	// it. Nil when no loop is running.
	currentEventCh chan query.Event
}

// sharedState holds the conversation history guarded by a mutex.
// PHASE_3.md design decision #4: shared []core.Message guarded by
// sync.Mutex, mutex held only for slice read/write. The struct is
// always accessed through a pointer on the Model so the lock is
// shared across Model copies (bubbletea copies the Model on every
// Update — a value mutex would be a vet error).
type sharedState struct {
	mu       sync.Mutex
	messages []core.Message
}

// lockMessages acquires the shared mutex and returns a pointer to
// the message slice plus a done function the caller MUST defer.
// Centralizing the lock here keeps every callsite uniform.
func (s *sharedState) lockMessages() (*[]core.Message, func()) {
	s.mu.Lock()
	return &s.messages, s.mu.Unlock
}

// PermissionDialogState holds the state for the in-TUI permission
// dialog. While this is non-nil, the dialog owns all input — see
// handlePermissionDialog in update.go. The lifecycle is:
//  1. The query loop's CheckPermission closure posts a permRequest
//     on the bridge's per-turn channel.
//  2. The TUI's Update loop drains the channel and builds a
//     PermissionDialogState, stashing it on the Model.
//  3. The user navigates with Tab/Shift+Tab and activates with
//     Enter (or hits Esc to deny).
//  4. respondPermission() invokes Respond (with the chosen decision),
//     nils the field, and (for "always" decisions) appends a
//     core.PermissionRule to the active Config.
type PermissionDialogState struct {
	// ToolName is the tool asking for permission (e.g. "Bash").
	ToolName string
	// Description is a short human-readable summary of the call
	// ("run `rm -rf /tmp/foo` in /home/user/proj"). For Bash this
	// is the actual command line; for Edit/Write it's the file
	// path and the change description.
	Description string
	// Details is optional structured context the renderer can
	// surface. Currently used to carry the Bash command under
	// the "command" key.
	Details map[string]any
	// Focused is 0..3, the index into the button row
	// {Allow, Allow always, Deny, Deny always}. The renderer
	// styles the focused button differently.
	Focused int
	// Respond is the closure the dialog uses to send the user's
	// decision back to the query loop's blocked CheckPermission
	// call. The loop is reading from a buffered channel; the
	// closure writes one value to that channel and returns.
	Respond func(decision core.PermissionDecision)
}

// InitialModel creates a Model with sensible defaults. The caller
// must set Config, Cost, and API before running the program.
func InitialModel(cfg *core.Config, cost *core.CostTracker) Model {
	ta := textarea.New()
	ta.Placeholder = "Type a message or /command…"
	ta.Focus()
	ta.CharLimit = 0 // no artificial limit
	ta.SetWidth(80)
	ta.SetHeight(3) // multi-line input

	vp := viewport.New(80, 20)
	vp.SetContent("")

	m := Model{
		Config:       cfg,
		Cost:         cost,
		Messages:     []renderedMessage{},
		Viewport:     vp,
		AutoScroll:   true,
		Input:        ta,
		InputHistory: []string{},
		HistoryIndex: -1,
		Status:       "Ready",
		shared:       &sharedState{messages: []core.Message{}},
	}

	m.CmdCtx = &commands.CommandContext{
		Config:     cfg,
		Cost:       cost,
		Messages:   m.shared.messages,
		WorkingDir: ".",
	}

	return m
}

// --- bubbletea.Msg types for the query-loop event channel ---

// queryEventMsg wraps a query.Event so bubbletea's runtime can
// deliver it as a tea.Msg. The program.go's loop-channel listener
// sends these.
type queryEventMsg query.Event

// loopFinishedMsg is emitted when the query-loop goroutine exits
// (the per-turn event channel was closed by the goroutine). The
// TUI uses this to reset its streaming state. The actual Outcome
// is recovered from the bridge (m.bridge.LastOutcome), not carried
// on this message, because the channel-close path has no value to
// send.
type loopFinishedMsg struct{}

// permissionRequestMsg wraps a permRequest so bubbletea's runtime
// can deliver it as a tea.Msg. The program.go permReqCh-drain Cmd
// sends these.
type permissionRequestMsg struct{ req permRequest }

// permRequest is the loop→TUI handoff for a permission check. The
// query loop's CheckPermission closure writes one of these to
// the bridge's permReqCh, then blocks on RespondCh until the TUI
// posts the user's decision. This handshake is the entire Step 5
// integration: the closure replaces the AutoPermissionHandler
// with a channel-based bridge.
//
// RespondCh is buffered (size 1) so the closure can complete
// without the dialog handler needing to know whether the TUI
// still has a reference to the value. The single write happens
// once and then the closure returns.
type permRequest struct {
	Request   core.PermissionRequest
	RespondCh chan core.PermissionDecision
}

// appendMessage adds a rendered message to the model's list and
// scrolls to the end if AutoScroll is on. Called from Update.
func (m Model) appendMessage(msg renderedMessage) (Model, tea.Cmd) {
	m.Messages = append(m.Messages, msg)
	if m.AutoScroll {
		m.GotoBottom()
	}
	return m, nil
}

// scrollToBottom forces the viewport to the end.
func (m *Model) scrollToBottom() {
	if m.Viewport.TotalLineCount() > 0 {
		m.Viewport.GotoBottom()
	}
}

// GotoBottom is a convenience that sets AutoScroll and scrolls.
func (m *Model) GotoBottom() {
	m.AutoScroll = true
	m.Viewport.GotoBottom()
}

// setModelStatus updates the status line and triggers a re-render.
func (m Model) setModelStatus(format string, args ...any) (Model, tea.Cmd) {
	m.Status = fmt.Sprintf(format, args...)
	return m, nil
}

// effectiveModel returns the model id to use. Falls back to
// the core default.
func (m *Model) effectiveModel() string {
	if m.Config != nil {
		return m.Config.EffectiveModel()
	}
	return core.DefaultModel
}

// buildQueryConfig maps the TUI's core.Config to a query.Config.
func (m *Model) buildQueryConfig(systemPrompt string) query.Config {
	if m.Config == nil {
		return query.Config{}
	}
	return query.Config{
		Model:              m.effectiveModel(),
		MaxTokens:          m.Config.EffectiveMaxTokens(),
		SystemPrompt:       systemPrompt,
		AppendSystemPrompt: m.Config.AppendSystemPrompt,
		MaxTurns:           m.Config.EffectiveMaxTurns(),
	}
}

// ensureTools returns the tool list. In Phase 3 this is always
// tools.AllTools(); Phase 4 adds MCP tools.
func (m *Model) ensureTools() []tools.Tool {
	return tools.AllTools()
}

// init enters bubbletea's Init lifecycle. We don't need to do
// anything at startup (no background tasks, no spinner).
func (m Model) Init() tea.Cmd {
	return nil
}
