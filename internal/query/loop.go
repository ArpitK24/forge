package query

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// Config is the per-call configuration for RunQueryLoop. Spec §5.1
// calls this "QueryConfig" — we use a short local name.
type Config struct {
	// Model is the model id, e.g. "meta/llama-3.3-70b-instruct".
	// Required.
	Model string
	// SystemPrompt is the full system prompt (already assembled
	// by the caller via core.BuildSystemPrompt).
	SystemPrompt string
	// AppendSystemPrompt is appended to the per-turn request —
	// kept here for symmetry with Config; the canonical place
	// for the prompt is SystemPrompt. (Phase 2 doesn't split
	// these; both end up in the same `system` field.)
	AppendSystemPrompt string
	// MaxTokens is the per-response output cap.
	MaxTokens int
	// MaxTurns is the cap on agentic turns. Spec §5.1 default 10.
	MaxTurns int
	// Thinking is the extended-reasoning config. nil = off.
	Thinking *core.ThinkingConfig
	// Temperature is the sampling temperature. nil = provider default.
	Temperature *float64
}

// OutcomeKind enumerates the loop's terminal states. Spec §5.1.
type OutcomeKind int

const (
	// OutcomeEndTurn is the normal completion: the model produced
	// text (or made and resolved tool calls) and stopped with
	// end_turn / stop_sequence / etc.
	OutcomeEndTurn OutcomeKind = iota
	// OutcomeMaxTokens means the model hit the per-response
	// max_tokens cap before producing a stop reason.
	OutcomeMaxTokens
	// OutcomeCancelled means the user cancelled (or ctx was
	// cancelled). Not a real error.
	OutcomeCancelled
	// OutcomeError means the loop hit a real error (provider
	// failure, tool panic unrecoverable, etc.).
	OutcomeError
)

// String returns the stable wire-format name.
func (k OutcomeKind) String() string {
	switch k {
	case OutcomeEndTurn:
		return "end_turn"
	case OutcomeMaxTokens:
		return "max_tokens"
	case OutcomeCancelled:
		return "cancelled"
	case OutcomeError:
		return "error"
	default:
		return fmt.Sprintf("outcome-%d", int(k))
	}
}

// Outcome is the loop's terminal state. Returned from
// RunQueryLoop. Callers (the headless wrapper, the TUI, ACP)
// inspect Kind to decide what to do next.
type Outcome struct {
	Kind    OutcomeKind
	Message core.Message // the final assistant message
	Usage   core.UsageInfo
	Turns   int
	// Err is set when Kind == OutcomeError.
	Err *core.Error
}

// Event is the sealed event type the loop emits on the
// eventCh parameter. Consumers (the headless text printer, the
// TUI, ACP's session/update notification) react to these
// without blocking the loop — the channel is buffered (size
// 64) and the loop only blocks on it when the buffer is full.
type Event interface {
	isEvent()
}

// ToolStartEvent fires when a tool call is about to execute.
// Carries the tool name and the model's id for the call.
type ToolStartEvent struct {
	Name string
	ID   string
}

// ToolEndEvent fires after a tool call completes. Result is
// the ToolResult.Text; IsError mirrors ToolResult.IsError.
type ToolEndEvent struct {
	Name    string
	ID      string
	Result  string
	IsError bool
}

// TurnCompleteEvent fires at the end of each turn. The turn
// index is 1-based (the first call to RunQueryLoop is turn 1).
type TurnCompleteEvent struct {
	Turn       int
	StopReason string
}

// StatusEvent is a free-form human-readable status message.
// Used for "compacting conversation..." or similar.
type StatusEvent struct {
	Message string
}

// ErrorEvent fires when the loop encounters a non-fatal error
// (e.g. one tool call failed but the loop continues). The
// terminal error is on Outcome.Err, not here.
type ErrorEvent struct {
	Err *core.Error
}

// StreamEventForward wraps an api.StreamEvent for forwarding
// onto the event channel. The headless wrapper, TUI, and ACP
// session/update notifications need access to the raw
// text/thinking/tool-input deltas in real time — these events
// are how the loop surfaces them. Carrying the whole event
// (not just the delta) preserves the event kind, index, and
// any per-event metadata.
type StreamEventForward struct {
	Event api.StreamEvent
}

// OutcomeEvent is the loop's terminal summary. The loop emits
// it on the event channel immediately before closing, so
// renderers and observers (the json output renderer in
// particular) can see the final Outcome's Kind, Usage, Turns,
// and Err without needing to also receive a return value.
//
// This is how the spec's "headless output formats report the
// final outcome" requirement lands without each consumer
// having to call RunQueryLoop synchronously.
type OutcomeEvent struct {
	Kind   OutcomeKind
	Usage  core.UsageInfo
	Turns  int
	Err    *core.Error
}

func (ToolStartEvent) isEvent()      {}
func (ToolEndEvent) isEvent()        {}
func (TurnCompleteEvent) isEvent()   {}
func (StatusEvent) isEvent()         {}
func (ErrorEvent) isEvent()          {}
func (StreamEventForward) isEvent()  {}
func (OutcomeEvent) isEvent()        {}

// RunQueryLoop is the agentic turn-execution loop. Spec §5.1.
//
// The function is the SINGLE agentic loop implementation in
// the codebase; TUI and ACP both drive this same function. It
// runs synchronously and returns the Outcome when the loop
// terminates (end_turn, max_tokens, cancelled, or error).
//
// Parameters:
//
//   - ctx: cancellation. Cancellation causes the loop to
//     return Outcome{Kind: OutcomeCancelled} as soon as the
//     in-flight stream finishes (or immediately, if no stream
//     is in flight).
//   - client: the Provider. The loop calls Stream() once per
//     turn and drains the returned channels.
//   - messages: the conversation history. The loop APPENDS to
//     this slice (so callers should pass a pointer or use
//     the returned Outcome.Message). The loop never drops
//     messages — every assistant turn is appended before the
//     next turn begins.
//   - tools: the tool list. Pass the output of tools.AllTools()
//     in production; tests can pass a smaller set.
//   - toolCtx: the per-call ToolContext.
//   - cfg: the loop configuration.
//   - cost: the session-wide cost tracker. May be nil (the
//     loop tolerates a nil tracker and just doesn't record).
//   - eventCh: a buffered channel of Events. The loop sends
//     status, tool-start, tool-end, turn-complete, and error
//     events. Pass nil to skip event reporting (e.g. in
//     sub-agent contexts that don't want the events).
func RunQueryLoop(
	ctx context.Context,
	client api.Provider,
	messages []core.Message,
	toolsList []tools.Tool,
	toolCtx *tools.ToolContext,
	cfg Config,
	cost *core.CostTracker,
	eventCh chan<- Event,
) Outcome {
	if eventCh == nil {
		// We still want a non-blocking send. nil channel
		// blocks forever, so substitute a small black-hole.
		eventCh = nilEventSink()
	}

	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = core.DefaultMaxTurns
	}

	var totalUsage core.UsageInfo
	var lastMessage core.Message
	turns := 0

	// finalize emits an OutcomeEvent carrying the final
	// Outcome summary and returns the Outcome itself. Every
	// return path in the loop calls this so renderers and
	// observers always see the same terminal view.
	finalize := func(o Outcome) Outcome {
		sendEvent(eventCh, OutcomeEvent{
			Kind:  o.Kind,
			Usage: o.Usage,
			Turns: o.Turns,
			Err:   o.Err,
		})
		return o
	}

	for {
		turns++

		// 1. Turn cap. Spec §5.1 step 1.
		if turns > maxTurns {
			sendEvent(eventCh, StatusEvent{
				Message: fmt.Sprintf("max turns (%d) reached", maxTurns),
			})
			break
		}

		// 2. Cancellation. Spec §5.1 step 2.
		if err := ctx.Err(); err != nil {
			return finalize(Outcome{
				Kind:    OutcomeCancelled,
				Message: lastMessage,
				Usage:   totalUsage,
				Turns:   turns - 1,
			})
		}

		// 3. Build the request. The system prompt is already
		// assembled by the caller; we wrap it as a single
		// string system block. Tools are passed through.
		req := api.Request{
			Model:       cfg.Model,
			MaxTokens:   cfg.MaxTokens,
			Messages:    messages,
			System:      api.SystemString(cfg.SystemPrompt + cfg.AppendSystemPrompt),
			Tools:       toolDefinitionsFrom(toolsList),
			Temperature: cfg.Temperature,
			Thinking:    convertThinkingConfig(cfg.Thinking),
			Stream:      true,
		}

		// 4. Open the stream.
		events, errs := client.Stream(ctx, req)

		// 5. Drain events into an accumulating core.Message
		// while forwarding every event onto eventCh.
		accum := newAccumulator()
		var streamErr *core.Error
	drain:
		for {
			select {
			case <-ctx.Done():
				return finalize(Outcome{
					Kind:    OutcomeCancelled,
					Message: lastMessage,
					Usage:   totalUsage,
					Turns:   turns - 1,
				})
			case ev, ok := <-events:
				if !ok {
					break drain
				}
				// Forward to subscribers; also accumulate.
				accum.apply(ev)
				sendEvent(eventCh, StreamEventForward{Event: ev})
				if ev.Kind == api.EventError && ev.Err != nil {
					streamErr = ev.Err
				}
			case err, ok := <-errs:
				if !ok {
					// Channel closed without an error. Continue
					// draining events.
					continue
				}
				// Build a *core.Error from the typed error if
				// it isn't already one.
				if err != nil {
					if streamErr == nil {
						streamErr = core.Wrap(core.KindAPI, err, "provider stream")
					}
					sendEvent(eventCh, ErrorEvent{Err: streamErr})
				}
			}
		}

		// If the stream produced an error and no assistant
		// message accumulated, return Error outcome.
		if streamErr != nil && accum.message.Role == 0 {
			return finalize(Outcome{
				Kind:  OutcomeError,
				Usage: totalUsage,
				Turns: turns - 1,
				Err:   streamErr,
			})
		}

		// If the ctx was cancelled mid-stream, the provider
		// goroutine returned early and closed the events
		// channel. Treat that as OutcomeCancelled rather than
		// end_turn, so callers can distinguish "user pressed
		// Ctrl+C" from "model finished naturally."
		if err := ctx.Err(); err != nil {
			return finalize(Outcome{
				Kind:    OutcomeCancelled,
				Message: accum.message,
				Usage:   totalUsage,
				Turns:   turns - 1,
			})
		}

		// 6. Finalize the assistant message and append it to
		// the conversation.
		assistantMsg := accum.message
		if assistantMsg.Role == 0 {
			// No content accumulated — treat as end_turn with
			// an empty assistant message so the loop terminates
			// without appending a malformed message.
			sendEvent(eventCh, TurnCompleteEvent{Turn: turns, StopReason: api.StopEndTurn})
			break
		}
		messages = append(messages, assistantMsg)
		lastMessage = assistantMsg

		// Sum usage. The accumulator tracks per-stream usage;
		// the cost tracker sees the same numbers.
		turnUsage := accum.usage
		totalUsage.InputTokens += turnUsage.InputTokens
		totalUsage.OutputTokens += turnUsage.OutputTokens
		totalUsage.CacheCreationInputTokens += turnUsage.CacheCreationInputTokens
		totalUsage.CacheReadInputTokens += turnUsage.CacheReadInputTokens
		if cost != nil {
			cost.AddUsage(turnUsage, cfg.Model)
		}

		// 6.5. Auto-compaction check. Phase 2's CompactConversation
		// is a stub that returns messages unchanged; the trigger
		// math is still wired up so the wiring is in place for
		// Phase 4.
		if toolCtx != nil && toolCtx.Cfg != nil && toolCtx.Cfg.AutoCompact {
			_ = AutoCompactIfNeeded(ctx, &messages, cfg.Model, turnUsage, cost)
		}

		// 7. Branch on stop reason.
		stopReason := accum.stopReason
		sendEvent(eventCh, TurnCompleteEvent{Turn: turns, StopReason: stopReason})

		switch stopReason {
		case api.StopEndTurn, api.StopStopSequence, "":
			// "anything unrecognized → fire the Stop hook,
			// return EndTurn" (spec §5.1 step 8a). The hook
			// is Phase 4 territory; we just return.
			return finalize(Outcome{
				Kind:    OutcomeEndTurn,
				Message: assistantMsg,
				Usage:   totalUsage,
				Turns:   turns,
			})

		case api.StopToolUse:
			// 8. Execute the tool calls. Spec §5.3.
			results, anyExecuted := executeToolCalls(ctx, toolCtx, toolsList, assistantMsg, eventCh)
			// Append the results as a single new user message
			// carrying all of this turn's tool_result blocks.
			if anyExecuted {
				messages = append(messages, results)
			} else {
				// No tool calls were executed (e.g. all denied
				// by permissions). Return end_turn to avoid an
				// infinite loop.
				return finalize(Outcome{
					Kind:    OutcomeEndTurn,
					Message: assistantMsg,
					Usage:   totalUsage,
					Turns:   turns,
				})
			}
			// Loop back to step 1.

		case api.StopMaxTokens:
			return finalize(Outcome{
				Kind:    OutcomeMaxTokens,
				Message: assistantMsg,
				Usage:   totalUsage,
				Turns:   turns,
			})

		case api.StopCancelled:
			return finalize(Outcome{
				Kind:    OutcomeCancelled,
				Message: assistantMsg,
				Usage:   totalUsage,
				Turns:   turns,
			})

		default:
			// Unknown stop reason: end the turn defensively
			// rather than spinning.
			return finalize(Outcome{
				Kind:    OutcomeEndTurn,
				Message: assistantMsg,
				Usage:   totalUsage,
				Turns:   turns,
			})
		}
	}

	// Reached when turns > maxTurns.
	return finalize(Outcome{
		Kind:    OutcomeEndTurn,
		Message: lastMessage,
		Usage:   totalUsage,
		Turns:   turns - 1,
	})
}

// executeToolCalls runs every ToolUse block in the assistant's
// message in order, with permission check and panic recovery.
// Returns the assembled user message (one core.Message carrying
// all of the ToolResult blocks) and a bool indicating whether
// at least one tool was actually invoked (as opposed to all
// being permission-denied or unknown — in which case the
// caller should not loop back to the model).
func executeToolCalls(
	ctx context.Context,
	tc *tools.ToolContext,
	toolsList []tools.Tool,
	assistant core.Message,
	eventCh chan<- Event,
) (core.Message, bool) {
	uses := assistant.ToolUses()
	if len(uses) == 0 {
		return core.Message{}, false
	}
	blocks := make([]core.ContentBlock, 0, len(uses))
	anyExecuted := false
	for _, tu := range uses {
		// Permission check. Use the tool's declared permission
		// level: PermReadOnly passes auto, others ask.
		decision := core.DecisionAllow
		if tc != nil && tc.CheckPermission != nil {
			isReadOnly := toolIsReadOnly(toolsList, tu.Name)
			decision = tc.CheckPermission(tu.Name, tu.Name+" tool call", isReadOnly)
		}

		if decision == core.DecisionDeny || decision == core.DecisionDenyPermanently {
			result := core.ToolResult{
				ToolUseID: tu.ID,
				Content:   jsonOrText("permission denied"),
				IsError:   true,
			}
			sendEvent(eventCh, ToolEndEvent{
				Name:    tu.Name,
				ID:      tu.ID,
				Result:  "permission denied",
				IsError: true,
			})
			blocks = append(blocks, core.ContentBlock{
				Kind:       core.BlockToolResult,
				ToolResult: &result,
			})
			continue
		}

		// Find the tool. The loop looks up tools in the
		// passed-in toolsList rather than the global registry
		// so tests can inject their own implementations.
		tool, ok := findToolIn(toolsList, tu.Name)
		if !ok {
			result := core.ToolResult{
				ToolUseID: tu.ID,
				Content:   jsonOrText("unknown tool: " + tu.Name),
				IsError:   true,
			}
			sendEvent(eventCh, ToolEndEvent{
				Name:    tu.Name,
				ID:      tu.ID,
				Result:  "unknown tool",
				IsError: true,
			})
			blocks = append(blocks, core.ContentBlock{
				Kind:       core.BlockToolResult,
				ToolResult: &result,
			})
			continue
		}

		// Emit ToolStart, then run.
		sendEvent(eventCh, ToolStartEvent{Name: tu.Name, ID: tu.ID})

		// Spec §3.1 + §16: "errors never panic the process".
		// Wrap Execute in a recover().
		result := safeExecute(ctx, tool, tu.Input, tc)

		// Emit ToolEnd.
		endResultText := result.Text
		if endResultText == "" {
			endResultText = "(no output)"
		}
		sendEvent(eventCh, ToolEndEvent{
			Name:    tu.Name,
			ID:      tu.ID,
			Result:  endResultText,
			IsError: result.IsError,
		})

		// Serialize the result content. If the tool already
		// produced JSON, pass it through; otherwise wrap the
		// text as a JSON string.
		var content json.RawMessage
		if len(result.Text) > 0 && json.Valid([]byte(result.Text)) {
			content = json.RawMessage(result.Text)
		} else {
			content = jsonOrText(result.Text)
		}
		blocks = append(blocks, core.ContentBlock{
			Kind: core.BlockToolResult,
			ToolResult: &core.ToolResult{
				ToolUseID: tu.ID,
				Content:   content,
				IsError:   result.IsError,
			},
		})
		anyExecuted = true
	}

	return core.Message{
		Role:    core.RoleUser,
		Content: core.BlocksContent(blocks...),
	}, anyExecuted
}

// safeExecute wraps tool.Execute in a recover() so a tool
// panic doesn't kill the whole session.
func safeExecute(ctx context.Context, t tools.Tool, input json.RawMessage, tc *tools.ToolContext) (result tools.ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			result = tools.ToolResult{
				Text:    fmt.Sprintf("panic in tool %s: %v", t.Name(), r),
				IsError: true,
			}
		}
	}()
	return t.Execute(ctx, input, tc)
}

// toolIsReadOnly looks up a tool's declared PermissionLevel
// and returns true if it's PermReadOnly (or PermNone, which
// has no side effects). Unknown tools are conservatively
// considered NOT read-only.
func toolIsReadOnly(toolsList []tools.Tool, name string) bool {
	for _, t := range toolsList {
		if t.Name() == name {
			lvl := t.PermissionLevel()
			return lvl == core.PermReadOnly || lvl == core.PermNone
		}
	}
	return false
}

// findToolIn looks up a tool by name in the supplied list.
// Case-insensitive ASCII match (matches tools.FindTool's
// behavior). Returns the tool and true on hit; nil and false
// otherwise.
func findToolIn(toolsList []tools.Tool, name string) (tools.Tool, bool) {
	for _, t := range toolsList {
		if strings.EqualFold(t.Name(), name) {
			return t, true
		}
	}
	return nil, false
}

// toolDefinitionsFrom converts the local Tool list to the
// canonical core.ToolDefinition list the Provider wants.
func toolDefinitionsFrom(toolsList []tools.Tool) []core.ToolDefinition {
	defs := make([]core.ToolDefinition, 0, len(toolsList))
	for _, t := range toolsList {
		defs = append(defs, core.ToolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.InputSchema(),
		})
	}
	return defs
}

// convertThinkingConfig turns a core.ThinkingConfig into an
// api.ThinkingConfig (which is a type alias for the same
// underlying struct). Phase 2's NIM path doesn't honor it;
// we pass it through so Phase 4's Anthropic adapter can pick
// it up. Returns nil for nil input.
func convertThinkingConfig(t *core.ThinkingConfig) *api.ThinkingConfig {
	if t == nil {
		return nil
	}
	return &api.ThinkingConfig{
		Enabled:      t.Enabled,
		BudgetTokens: t.BudgetTokens,
	}
}

// jsonOrText wraps a string in a JSON-quoted string. Used for
// tool result content that is plain text — the spec wants JSON
// for tool_result.content so the model can be sure it's valid.
func jsonOrText(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// sendEvent is a non-blocking event send. If the event channel
// is full, the event is dropped — the loop never blocks on
// consumer backpressure.
func sendEvent(ch chan<- Event, ev Event) {
	select {
	case ch <- ev:
	default:
		// Drop event. TUI consumers should size their channel
		// large enough to keep up; headless mode doesn't care.
	}
}

// nilEventSink returns a channel that accepts and discards
// every send, used when the caller passes nil for eventCh.
func nilEventSink() chan<- Event {
	ch := make(chan Event, 64)
	go func() {
		for range ch {
		}
	}()
	return ch
}
