package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/commands"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/query"
)

// Update is bubbletea's message handler. Every tea.Msg (key press,
// window resize, custom event from the query loop) arrives here.
// We dispatch to the right handler, return the new Model and any
// commands bubbletea should run.
//
// Spec §9.3 governs key handling. The disambiguation rule for Esc
// is critical: Esc cancels a streaming turn OR clears the input OR
// quits, depending on state — never one key always meaning quit.
//
// Step 5: while the permission dialog is open, it owns ALL input.
// The user confirmed the freeze-everything design: no scroll, no
// input dispatch except into the dialog. Esc inside the dialog
// denies the call (does NOT quit the TUI), Tab/Shift+Tab cycle
// focus, Enter activates the focused button.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// If the permission dialog is open, it owns all input until
	// the user decides. This branch is reached on EVERY message
	// type — key presses, loop events, window resizes — and
	// short-circuits the rest of Update. The only messages we
	// still care about while the dialog is open are key presses
	// (to navigate/activate) and any pending permissionRequestMsg
	// that would queue a second dialog (we leave that one on the
	// channel — see openPermissionDialog).
	if m.PermissionDialog != nil {
		if k, ok := msg.(tea.KeyMsg); ok {
			return m.handlePermissionDialogKey(k)
		}
		// Non-key messages (window resize, loop events) are
		// dropped while the dialog is up. This matches the
		// "freeze everything" decision.
		return m, nil
	}

	switch msg := msg.(type) {

	// --- Window resize ---
	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		m.computeLayout()
		return m, nil

	// --- Query-loop events (forwarded from program.go) ---
	case queryEventMsg:
		// Process the event, then re-subscribe on the per-turn
		// channel so bubbletea keeps draining until the loop
		// closes it.
		mm, _ := m.handleQueryEvent(msg)
		m = mm.(Model)
		if m.currentEventCh != nil {
			return m, waitForEvent(m.currentEventCh)
		}
		return m, nil

	case loopFinishedMsg:
		return m.handleLoopFinished(msg)

	// --- Permission request from the loop (Step 5) ---
	case permissionRequestMsg:
		return m.openPermissionDialog(msg)

	// --- Key presses ---
	case tea.KeyMsg:
		return m.handleKey(msg)

	// --- Any other message: pass to the input textarea ---
	default:
		var cmd tea.Cmd
		m.Input, cmd = m.Input.Update(msg)
		return m, cmd
	}
}

// handleKey dispatches a KeyMsg to the right action. Spec §9.3.
func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	km := DefaultKeyMap()

	// Help overlay takes priority over everything when visible.
	if m.HelpVisible {
		switch {
		case keyMatches(k, km.Escape), keyMatches(k, km.CtrlQuestion),
			k.String() == "q", k.String() == "esc":
			m.HelpVisible = false
			return m, nil
		}
		// Any other key: still close the help overlay.
		m.HelpVisible = false
		return m, nil
	}

	// While streaming, only a small set of keys is honored: Esc
	// cancels, PgUp/PgDn scroll. Typing into the input is allowed
	// but doesn't submit.
	if m.Streaming {
		switch {
		case keyMatches(k, km.Escape):
			return m.cancelStreaming()
		case keyMatches(k, km.PageUp):
			m.Viewport.LineUp(m.Viewport.Height)
			m.AutoScroll = false
			return m, nil
		case keyMatches(k, km.PageDown):
			m.Viewport.LineDown(m.Viewport.Height)
			return m, nil
		case keyMatches(k, km.CtrlQuestion):
			m.HelpVisible = true
			return m, nil
		default:
			// Forward to the textarea so the user can type the
			// next message while the current one streams.
			var cmd tea.Cmd
			m.Input, cmd = m.Input.Update(k)
			return m, cmd
		}
	}

	// --- Non-streaming key handling ---

	// Submit.
	if keyMatches(k, km.Enter) {
		return m.submitInput()
	}

	// Context-dependent Esc: cancel > clear > quit.
	if keyMatches(k, km.Escape) {
		if m.Streaming {
			return m.cancelStreaming()
		}
		if m.Input.Value() != "" {
			m.Input.Reset()
			m.HistoryIndex = -1
			return m, nil
		}
		m.Quitting = true
		return m, tea.Quit
	}

	// Dedicated "quit if empty".
	if keyMatches(k, km.CtrlD) {
		if m.Input.Value() == "" {
			m.Quitting = true
			return m, tea.Quit
		}
		// Otherwise act like delete-forward.
		var cmd tea.Cmd
		m.Input, cmd = m.Input.Update(k)
		return m, cmd
	}

	// Toggle help.
	if keyMatches(k, km.CtrlQuestion) {
		m.HelpVisible = !m.HelpVisible
		return m, nil
	}

	// Toggle thinking (in-memory only; Phase 3.1 wires the provider).
	if keyMatches(k, km.CtrlT) {
		if m.Config != nil {
			if m.Config.ThinkingBudget > 0 {
				m.Config.ThinkingBudget = 0
				m.Status = "Thinking: off"
			} else {
				m.Config.ThinkingBudget = 4096
				m.Status = "Thinking: on (4096 tokens)"
			}
		}
		return m, nil
	}

	// History navigation. Up/Down only recall history when the
	// cursor is at the input's edges; otherwise they navigate
	// within the textarea.
	switch k.String() {
	case "up":
		// If we're at the first line of the input, recall prev.
		if m.Input.Line() == 0 {
			prev := m.historyPrev(m.Input.Value())
			if prev != m.Input.Value() {
				m.Input.SetValue(prev)
				m.Input.CursorEnd()
			}
			return m, nil
		}
	case "down":
		// If we're at the last line, recall next.
		if m.Input.Line() >= m.Input.LineCount()-1 {
			next := m.historyNext(m.Input.Value())
			if next != m.Input.Value() {
				m.Input.SetValue(next)
				m.Input.CursorEnd()
			}
			return m, nil
		}
	}

	// Scrolling.
	if keyMatches(k, km.PageUp) {
		m.Viewport.LineUp(m.Viewport.Height)
		m.AutoScroll = false
		return m, nil
	}
	if keyMatches(k, km.PageDown) {
		m.Viewport.LineDown(m.Viewport.Height)
		// If we've reached the bottom, re-enable auto-scroll.
		if m.Viewport.AtBottom() {
			m.AutoScroll = true
		}
		return m, nil
	}

	// Default: forward to the textarea for character input,
	// backspace, arrow keys within the text, etc.
	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(k)
	return m, cmd
}

// submitInput handles Enter. Per spec §9.3: Enter submits, ignoring
// pure-whitespace submissions. If the line starts with "/", it
// dispatches to the command framework; otherwise it pushes a user
// message and spawns the query loop.
func (m Model) submitInput() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.Input.Value())
	if text == "" {
		return m, nil // ignore whitespace-only submissions
	}

	// Save to history.
	hist := m.Input.Value()
	m.historyAppend(hist)
	m.HistoryIndex = -1

	// Reset the input box.
	m.Input.Reset()

	// Slash command?
	if strings.HasPrefix(text, "/") {
		return m.dispatchCommand(text)
	}

	// Normal prompt: push a user message and start the loop.
	m.Messages = append(m.Messages, renderedMessage{
		Role: "user",
		Text: text,
	})

	// Add the canonical user message to the shared history.
	userMsg := core.NewUserText(text)
	m.shared.mu.Lock()
	m.CmdCtx.Messages = append(m.CmdCtx.Messages, userMsg)
	m.shared.mu.Unlock()

	m.Streaming = true
	m.StreamBuffer = ""
	m.Status = "Thinking…"

	return m, m.startQueryLoop()
}

// dispatchCommand runs a slash command via the commands framework.
// The result determines the next action: show a message, clear the
// conversation, exit, etc.
func (m Model) dispatchCommand(raw string) (tea.Model, tea.Cmd) {
	// Update the command context with the current message history.
	m.shared.mu.Lock()
	m.CmdCtx.Messages = append([]core.Message(nil), m.CmdCtx.Messages...)
	m.shared.mu.Unlock()

	res := commands.ExecuteCommand(m.loopCtx(), raw, m.CmdCtx)
	if res == nil {
		// Pass-through: treat as a normal prompt.
		// (This happens for unknown /commands; per spec we send
		// them to the loop rather than erroring.)
		text := strings.TrimSpace(raw)
		m.Messages = append(m.Messages, renderedMessage{
			Role: "user",
			Text: text,
		})
		userMsg := core.NewUserText(text)
		m.shared.mu.Lock()
		m.CmdCtx.Messages = append(m.CmdCtx.Messages, userMsg)
		m.shared.mu.Unlock()
		m.Streaming = true
		m.StreamBuffer = ""
		m.Status = "Thinking…"
		return m, m.startQueryLoop()
	}

	switch res.Kind {
	case commands.ResultMessage:
		m.Messages = append(m.Messages, renderedMessage{
			Role: "system",
			Text: res.Text,
		})
		m.GotoBottom()
		return m, nil

	case commands.ResultError:
		m.Messages = append(m.Messages, renderedMessage{
			Role: "error",
			Text: res.Text,
		})
		m.GotoBottom()
		return m, nil

	case commands.ResultUserMessage:
		// Inject as a user prompt and start the loop.
		text := res.Text
		m.Messages = append(m.Messages, renderedMessage{
			Role: "user",
			Text: text,
		})
		userMsg := core.NewUserText(text)
		m.shared.mu.Lock()
		m.CmdCtx.Messages = append(m.CmdCtx.Messages, userMsg)
		m.shared.mu.Unlock()
		m.Streaming = true
		m.StreamBuffer = ""
		m.Status = "Thinking…"
		return m, m.startQueryLoop()

	case commands.ResultClearConversation:
		m.Messages = nil
		m.StreamBuffer = ""
		m.shared.mu.Lock()
		m.CmdCtx.Messages = nil
		m.shared.mu.Unlock()
		m.Viewport.SetContent("")
		m.Status = "Conversation cleared"
		return m, nil

	case commands.ResultSetMessages:
		m.shared.mu.Lock()
		m.CmdCtx.Messages = res.Messages
		m.shared.mu.Unlock()
		// Rebuild the rendered view from the new history.
		m.Messages = nil
		for _, msg := range res.Messages {
			m.Messages = append(m.Messages, renderedMessage{
				Role: msg.Role.String(),
				Text: msg.AllText(),
			})
		}
		m.GotoBottom()
		return m, nil

	case commands.ResultExit:
		m.Quitting = true
		return m, tea.Quit

	case commands.ResultConfigChange:
		if res.Config != nil {
			m.Config = res.Config
			m.CmdCtx.Config = res.Config
			m.Status = "Config updated"
		}
		return m, nil

	case commands.ResultSilent:
		return m, nil

	default:
		return m, nil
	}
}

// cancelStreaming cancels the in-flight query loop. The loop's
// ctx-done path returns OutcomeCancelled, which we handle in
// handleLoopFinished.
func (m Model) cancelStreaming() (tea.Model, tea.Cmd) {
	if m.LoopCancel != nil {
		m.LoopCancel()
	}
	m.Status = "Cancelling…"
	return m, nil
}

// handleQueryEvent processes an event forwarded from the running
// query loop. This is how streaming text, tool calls, and status
// updates flow into the model. Each query.Event subtype gets its
// own case.
func (m Model) handleQueryEvent(e queryEventMsg) (tea.Model, tea.Cmd) {
	switch ev := query.Event(e).(type) {

	case query.StreamEventForward:
		// A raw stream event (text/thinking/tool-input delta).
		// Extract text deltas and append to the streaming buffer.
		if ev.Event.Kind == api.EventContentBlockDelta {
			if ev.Event.Delta.Kind == api.DeltaText {
				m.StreamBuffer += ev.Event.Delta.Text
				if m.AutoScroll {
					m.Viewport.GotoBottom()
				}
			}
		}
		return m, nil

	case query.StatusEvent:
		m.Status = ev.Message
		return m, nil

	case query.ToolStartEvent:
		m.Messages = append(m.Messages, renderedMessage{
			Role: "tool",
			Text: fmt.Sprintf("→ %s", ev.Name),
		})
		// Flush the streaming buffer into a real assistant message
		// before the tool line appears.
		if m.StreamBuffer != "" {
			m.Messages = append(m.Messages, renderedMessage{
				Role: "assistant",
				Text: m.StreamBuffer,
			})
			m.StreamBuffer = ""
		}
		m.GotoBottom()
		return m, nil

	case query.ToolEndEvent:
		preview := ev.Result
		const maxPreview = 400
		if len(preview) > maxPreview {
			preview = preview[:maxPreview] + "…"
		}
		label := "✓"
		if ev.IsError {
			label = "✗"
		}
		m.Messages = append(m.Messages, renderedMessage{
			Role: "tool",
			Text: fmt.Sprintf("%s %s: %s", label, ev.Name, preview),
		})
		m.GotoBottom()
		return m, nil

	case query.ErrorEvent:
		if ev.Err != nil {
			m.Messages = append(m.Messages, renderedMessage{
				Role: "error",
				Text: ev.Err.Error(),
			})
		}
		m.GotoBottom()
		return m, nil

	case query.TurnCompleteEvent:
		// Turn boundary — flush streaming text so far as an
		// assistant message; the next turn's stream starts fresh.
		if m.StreamBuffer != "" {
			m.Messages = append(m.Messages, renderedMessage{
				Role: "assistant",
				Text: m.StreamBuffer,
			})
			m.StreamBuffer = ""
		}
		m.GotoBottom()
		return m, nil

	case query.OutcomeEvent:
		// The loop's terminal summary. We don't act on it here —
		// handleLoopFinished (triggered by the channel close)
		// does the final reset. Ignore.
		return m, nil
	}
	return m, nil
}

// handleLoopFinished is called when the query loop goroutine exits
// (the per-turn event channel was closed). We reset the streaming
// state, finalize the assistant message, and pull the outcome off
// the bridge.
func (m Model) handleLoopFinished(msg loopFinishedMsg) (tea.Model, tea.Cmd) {
	m.Streaming = false
	m.LoopRunning = false
	m.LoopCancel = nil
	m.currentEventCh = nil

	// Flush any remaining streaming buffer as a finalized
	// assistant message.
	if m.StreamBuffer != "" {
		m.Messages = append(m.Messages, renderedMessage{
			Role: "assistant",
			Text: m.StreamBuffer,
		})
		m.StreamBuffer = ""
	}

	// Recover the outcome the loop goroutine stashed on the bridge.
	var outcome query.Outcome
	if m.bridge != nil && m.bridge.LastOutcome != nil {
		outcome = *m.bridge.LastOutcome
		m.bridge.LastOutcome = nil
	}

	// Update the shared message history from the outcome.
	if len(outcome.Messages) > 0 {
		m.shared.mu.Lock()
		m.CmdCtx.Messages = outcome.Messages
		m.shared.mu.Unlock()
	}

	// Status line reflects the outcome.
	switch outcome.Kind.String() {
	case "end_turn":
		m.Status = "Ready"
	case "cancelled":
		m.Status = "Cancelled"
	case "max_tokens":
		m.Status = "Stopped: max tokens"
	case "error":
		if outcome.Err != nil {
			m.Status = "Error: " + outcome.Err.Error()
		} else {
			m.Status = "Error"
		}
	}

	m.GotoBottom()
	return m, nil
}

// openPermissionDialog is called when the loop's CheckPermission
// closure posts a request. We build the dialog state and stash it
// on the Model; the next Update will see PermissionDialog != nil
// and route all subsequent input into the dialog.
//
// The Respond closure writes the user's decision to the per-request
// channel. Because permReqCh has capacity 4 and the loop calls
// CheckPermission serially, only one dialog can be open at a time;
// this method is therefore a simple swap of the dialog pointer.
func (m Model) openPermissionDialog(p permissionRequestMsg) (tea.Model, tea.Cmd) {
	req := p.req
	m.PermissionDialog = &PermissionDialogState{
		ToolName:    req.Request.ToolName,
		Description: req.Request.Description,
		Details:     req.Request.Details,
		// Default focus on Allow (index 0) — it's the safest
		// "let it through" choice and matches the AutoPermissionHandler
		// behavior in non-strict modes.
		Focused: 0,
		Respond: func(d core.PermissionDecision) {
			req.RespondCh <- d
		},
	}
	// Suspend the loop's auto-scroll while the dialog is up so
	// the message pane doesn't drift under the modal.
	m.AutoScroll = false
	return m, nil
}

// handlePermissionDialogKey routes a key press to the right dialog
// action. Only tea.KeyMsg reaches this method; Update filters out
// non-key messages while the dialog is open.
//
// Esc always denies (regardless of the focused button) — this
// matches spec §9.5 and Claude Code's behavior. Enter activates
// the focused button. Tab cycles forward; Shift+Tab cycles
// backward.
func (m Model) handlePermissionDialogKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "tab":
		m.PermissionDialog.Focused = (m.PermissionDialog.Focused + 1) % 4
		return m, nil
	case "shift+tab":
		// Adding 3 (== -1 mod 4) wraps backward.
		m.PermissionDialog.Focused = (m.PermissionDialog.Focused + 3) % 4
		return m, nil
	case "enter":
		return m.activatePermissionButton()
	case "esc":
		return m.respondPermission(core.DecisionDeny)
	}
	// Any other key is ignored — the dialog is the only thing
	// the user can do right now.
	return m, nil
}

// activatePermissionButton is the Enter handler. It maps the
// currently focused button (0..3) to the corresponding
// PermissionDecision and routes it through respondPermission.
func (m Model) activatePermissionButton() (tea.Model, tea.Cmd) {
	if m.PermissionDialog == nil {
		return m, nil
	}
	idx := m.PermissionDialog.Focused
	if idx < 0 || idx >= len(permissionButtonDecisions) {
		// Defensive: should never happen because Focused is
		// always set by the key handler modulo 4, but if a
		// future caller constructs a dialog with an out-of-
		// range Focused we don't want to panic.
		return m.respondPermission(core.DecisionDeny)
	}
	return m.respondPermission(permissionButtonDecisions[idx])
}

// respondPermission records the user's decision and posts it
// back to the loop. For "always" decisions it also appends a
// core.PermissionRule to the active Config's in-memory rule
// list so subsequent matching calls auto-decide.
//
// The dialog pointer is nilled BEFORE Respond is called so the
// loop's next CheckPermission call (if the model makes a
// follow-up tool call) can post a fresh request without
// short-circuiting on the previous dialog.
func (m Model) respondPermission(d core.PermissionDecision) (tea.Model, tea.Cmd) {
	if m.PermissionDialog == nil {
		return m, nil
	}
	dlg := m.PermissionDialog
	m.PermissionDialog = nil

	// Append a session-scoped rule for the "always" decisions.
	// ArgPattern is the verbatim description the loop passed
	// (for Bash, this is the command line). Phase 3 uses
	// substring matching; Phase 3.1 upgrades to glob.
	if d == core.DecisionAllowPermanently || d == core.DecisionDenyPermanently {
		if m.Config != nil {
			m.Config.PermissionRules = append(m.Config.PermissionRules,
				core.PermissionRule{
					Tool:       dlg.ToolName,
					ArgPattern: dlg.Description,
					Decision:   d,
				})
		}
	}

	// Update the status line so the user has visible feedback
	// after the dialog closes. (The dialog itself disappears;
	// the status line is the next thing they see.)
	switch d {
	case core.DecisionAllow:
		m.Status = "Allowed: " + dlg.ToolName
	case core.DecisionAllowPermanently:
		m.Status = "Allowed (always): " + dlg.ToolName
	case core.DecisionDeny:
		m.Status = "Denied: " + dlg.ToolName
	case core.DecisionDenyPermanently:
		m.Status = "Denied (always): " + dlg.ToolName
	}

	if dlg.Respond != nil {
		dlg.Respond(d)
	}
	return m, nil
}

// keyMatches reports whether a KeyMsg matches a binding. bubbletea's
// key.Binding matches against the KeyMsg's String() representation
// (e.g. "ctrl+d") or its Type name (e.g. "enter", "esc").
func keyMatches(k tea.KeyMsg, b key.Binding) bool {
	for _, kk := range b.Keys() {
		if k.String() == kk || k.Type.String() == kk {
			return true
		}
	}
	return false
}
