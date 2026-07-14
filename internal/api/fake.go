package api

import (
	"context"
	"fmt"

	"github.com/ArpitK24/forge/internal/core"
)

// FakeProvider is a deterministic Provider for tests. It plays
// back a sequence of canned StreamEvent scripts, one per call
// to Stream. Each script is one full assistant turn (text-only,
// or a tool call, or a tool call followed by a final text turn).
//
// Tests drive the query loop through a turn or two by passing
// the right script sequence; no goroutines, no timing, no
// channels-that-aren't-closed. The contract matches a real
// provider: events channel closes on EventMessageStop (or
// EventError), err channel closes after the events goroutine
// returns.
//
// A nil *FakeProvider is a no-op Provider that returns a single
// error event per call. Use it when a test signature requires
// a Provider but the test doesn't care about the response.
type FakeProvider struct {
	// Info is what Info() returns. Tests override it to pin
	// the model metadata for size-of-compaction math.
	InfoValue ModelInfo
	// Scripts is the ordered list of canned turns. Each call to
	// Stream emits the next script. The cycle repeats forever;
	// the query loop's MaxTurns is what stops it.
	Scripts [][]StreamEvent
	// Calls counts how many times Stream has been called.
	Calls int
}

// NewFakeProvider returns a FakeProvider whose Info is set to a
// generic 128k-context model and whose Scripts are the supplied
// canned turns.
func NewFakeProvider(scripts ...[]StreamEvent) *FakeProvider {
	return &FakeProvider{
		InfoValue: ModelInfo{
			ID:            "fake-model",
			Provider:      "fake",
			ContextWindow: 128_000,
			SupportsToolUse: true,
		},
		Scripts: scripts,
	}
}

// Info implements Provider.
func (f *FakeProvider) Info() ModelInfo {
	if f == nil {
		return ModelInfo{ID: "nil-fake", ContextWindow: 128_000}
	}
	return f.InfoValue
}

// Stream implements Provider. The behavior:
//
//   - If the call index exceeds the script count, repeat the
//     last script (this lets tests assert "loop hit max turns
//     while still calling tools" without writing N scripts).
//   - Emit each event in the script, then close the events
//     channel.
//   - The errors channel is always closed empty (no errors).
//   - If ctx is cancelled mid-stream, close the events channel
//     without emitting EventMessageStop. The query loop
//     should interpret this as Cancelled.
//
// We use a small helper goroutine that listens for ctx.Done()
// so the test still respects cancellation without needing a
// real network round-trip.
func (f *FakeProvider) Stream(ctx context.Context, _ Request) (<-chan StreamEvent, <-chan error) {
	events := make(chan StreamEvent, 16)
	errs := make(chan error, 1)

	if f == nil {
		// Nil provider: emit a single error and close.
		errs <- fmt.Errorf("nil FakeProvider: no scripted response")
		close(events)
		close(errs)
		return events, errs
	}

	idx := f.Calls
	f.Calls++
	if idx >= len(f.Scripts) {
		// Replay the last script on subsequent calls. This matches
		// what a deterministic test wants when driving the loop
		// past its scripted turns (e.g. asserting "loop hit
		// max turns while still calling tools").
		if len(f.Scripts) == 0 {
			errs <- fmt.Errorf("FakeProvider: no scripted responses")
			close(events)
			close(errs)
			return events, errs
		}
		idx = len(f.Scripts) - 1
	}
	script := f.Scripts[idx]

	go func() {
		defer close(events)
		defer close(errs)
		for _, ev := range script {
			select {
			case <-ctx.Done():
				return
			case events <- ev:
			}
		}
	}()
	return events, errs
}

// ScriptTextResponse is a convenience that builds a one-turn
// text-only script. The text is emitted as a single delta so
// the query loop sees a complete assistant turn on the first
// call.
//
// This is the simplest canned response: a model that just
// answers, with no tool calls.
func ScriptTextResponse(text string) []StreamEvent {
	return []StreamEvent{
		EventOfMessageStart("fake-model", nil),
		EventOfBlockStart(0, core.TextBlock("")),
		EventOfBlockDelta(0, TextDelta(text)),
		EventOfBlockStop(0),
		EventOfMessageDelta(StopEndTurn, nil),
		EventOfMessageStop(),
	}
}

// ScriptToolCallThenText returns a two-turn canned script:
//
//   1. A tool-call turn that invokes name(args). The query loop
//      is expected to run the tool, append the result, and call
//      Stream again.
//   2. A text-only turn that emits finalText — the "after the
//      tool ran" answer.
//
// Call args as the JSON-encoded tool input (e.g.
// `[]byte(`{"command":"echo hi"}`)`).
//
// The first turn's stop reason is StopToolUse so the loop
// knows to execute the tool before continuing; the second is
// StopEndTurn so the loop returns.
func ScriptToolCallThenText(name string, args []byte, finalText string) [][]StreamEvent {
	toolCallID := "call_1"
	return [][]StreamEvent{
		// Turn 1: tool call.
		{
			EventOfMessageStart("fake-model", nil),
			EventOfBlockStart(0, core.ContentBlock{
				Kind: core.BlockToolUse,
				ToolUse: &core.ToolUse{
					ID:    toolCallID,
					Name:  name,
					Input: args,
				},
			}),
			EventOfMessageDelta(StopToolUse, nil),
			EventOfMessageStop(),
		},
		// Turn 2: final text.
		ScriptTextResponse(finalText),
	}
}

// ScriptAlwaysToolCall is a script where every turn is the
// same tool call. Used to drive the loop past its MaxTurns
// cap without ever producing a final answer.
func ScriptAlwaysToolCall(name string, args []byte) [][]StreamEvent {
	return [][]StreamEvent{
		{
			EventOfMessageStart("fake-model", nil),
			EventOfBlockStart(0, core.ContentBlock{
				Kind: core.BlockToolUse,
				ToolUse: &core.ToolUse{
					ID:    "always_call",
					Name:  name,
					Input: args,
				},
			}),
			EventOfMessageDelta(StopToolUse, nil),
			EventOfMessageStop(),
		},
	}
}
