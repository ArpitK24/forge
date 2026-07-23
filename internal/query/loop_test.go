package query

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// --- Helpers used by the loop tests ---

// recordingTool is a test-only Tool that records every Execute
// call and returns a canned result. It also supports a
// configurable panic flag for the panic-recovery test.
type recordingTool struct {
	name      string
	calls     atomic.Int64
	lastInput atomic.Value // string
	result    tools.ToolResult
	panicNext atomic.Bool
}

func (r *recordingTool) Name() string        { return r.name }
func (r *recordingTool) Description() string { return "test tool" }
func (r *recordingTool) PermissionLevel() core.PermissionLevel {
	return core.PermExecute
}
func (r *recordingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`)
}
func (r *recordingTool) Execute(_ context.Context, input json.RawMessage, _ *tools.ToolContext) tools.ToolResult {
	r.calls.Add(1)
	r.lastInput.Store(string(input))
	if r.panicNext.Swap(false) {
		panic("boom from recordingTool")
	}
	return r.result
}

func (r *recordingTool) lastInputString() string {
	v, _ := r.lastInput.Load().(string)
	return v
}

func testToolsList(t *testing.T) ([]tools.Tool, *recordingTool) {
	t.Helper()
	rt := &recordingTool{
		name:   "Bash",
		result: tools.ToolResult{Text: "tool-output-ok"},
	}
	return []tools.Tool{rt}, rt
}

func testTC(t *testing.T, mode core.PermissionMode, cfg *core.Config) *tools.ToolContext {
	t.Helper()
	perm := &core.AutoPermissionHandler{Mode: mode}
	return &tools.ToolContext{
		WorkingDir: ".",
		Permission: perm,
		Cfg:        cfg,
		CheckPermission: func(name, desc string, ro bool) core.PermissionDecision {
			return perm.RequestPermission(core.PermissionRequest{
				ToolName: name, Description: desc, IsReadOnly: ro,
			})
		},
	}
}

func newEventSink() chan Event {
	return make(chan Event, 64)
}

// --- Tests ---

func TestRunQueryLoopEndTurnOnTextOnly(t *testing.T) {
	fp := api.NewFakeProvider(api.ScriptTextResponse("hello world"))
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})
	toolsList, _ := testToolsList(t)

	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("hi")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		nil,
		newEventSink(),
	)
	if out.Kind != OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn", out.Kind)
	}
	if out.Turns != 1 {
		t.Errorf("Turns = %d, want 1", out.Turns)
	}
	if got := out.Message.GetFirstText(); got != "hello world" {
		t.Errorf("final message = %q, want 'hello world'", got)
	}
}

func TestRunQueryLoopToolRoundTrip(t *testing.T) {
	// FakeProvider: turn 1 = tool call to Bash, turn 2 = final text.
	scripts := api.ScriptToolCallThenText("Bash", []byte(`{"command":"echo hi"}`), "tool returned ok")
	fp := api.NewFakeProvider(scripts...)
	toolsList, rec := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})

	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("run echo hi")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		nil,
		newEventSink(),
	)
	if out.Kind != OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn", out.Kind)
	}
	if out.Turns != 2 {
		t.Errorf("Turns = %d, want 2 (tool + final)", out.Turns)
	}
	if rec.calls.Load() != 1 {
		t.Errorf("recordingTool calls = %d, want 1", rec.calls.Load())
	}
	if !contains(rec.lastInputString(), "echo hi") {
		t.Errorf("tool input = %q, want contains 'echo hi'", rec.lastInputString())
	}
	if got := out.Message.GetFirstText(); got != "tool returned ok" {
		t.Errorf("final message = %q, want 'tool returned ok'", got)
	}
}

func TestRunQueryLoopMaxTurnsCap(t *testing.T) {
	// A provider that always calls a tool. The loop should
	// stop at MaxTurns and return OutcomeEndTurn.
	fp := api.NewFakeProvider(api.ScriptAlwaysToolCall("Bash", []byte(`{"command":"echo x"}`))...)
	toolsList, rec := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})

	const maxTurns = 3
	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("loop")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: maxTurns},
		nil,
		newEventSink(),
	)
	if out.Kind != OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn", out.Kind)
	}
	if out.Turns != maxTurns {
		t.Errorf("Turns = %d, want %d (max-turns cap)", out.Turns, maxTurns)
	}
	if rec.calls.Load() != int64(maxTurns) {
		t.Errorf("tool calls = %d, want %d", rec.calls.Load(), maxTurns)
	}
}

func TestRunQueryLoopPermissionDeniedAppendsToolError(t *testing.T) {
	fp := api.NewFakeProvider(api.ScriptToolCallThenText("Bash", []byte(`{"command":"echo x"}`), "fallback")...)
	toolsList, rec := testToolsList(t)
	// Default mode denies non-read-only tools in headless.
	tc := testTC(t, core.PermissionDefault, &core.Config{})

	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("run a denied tool")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		nil,
		newEventSink(),
	)
	// The provider's second turn is "fallback"; the loop should
	// reach it because the tool result (an error from permission
	// denial) is appended as a tool_result and the model then
	// responds. The recording tool should NOT have been called.
	if rec.calls.Load() != 0 {
		t.Errorf("tool was called despite permission denial: calls = %d", rec.calls.Load())
	}
	if out.Kind != OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn", out.Kind)
	}
	// The loop returns end_turn on the first turn when no tool
	// was actually executed (all denied), rather than spinning
	// forever. Turns is 1.
	if out.Turns != 1 {
		t.Errorf("Turns = %d, want 1", out.Turns)
	}
}

func TestRunQueryLoopCancellation(t *testing.T) {
	// A provider that emits a long stream. We cancel mid-stream.
	longScript := []api.StreamEvent{
		api.EventOfMessageStart("fake-model", nil),
		api.EventOfBlockStart(0, core.TextBlock("")),
	}
	for i := 0; i < 50; i++ {
		longScript = append(longScript, api.EventOfBlockDelta(0, api.TextDelta("x")))
	}
	longScript = append(longScript, api.EventOfBlockStop(0), api.EventOfMessageStop())

	// Use a custom provider that emits the script slowly, with
	// a small buffer so cancellation actually has a chance to
	// interleave.
	fp := &cancelTestProvider{script: longScript}
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})
	toolsList, _ := testToolsList(t)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a few events — the script is 54 events, so
	// 50ms into a 20ms-per-event stream is past the third
	// event. The provider's buffer fills, ctx.Done trips, the
	// goroutine returns, the events channel closes, the loop
	// drain exits.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	out := RunQueryLoop(
		ctx,
		fp,
		[]core.Message{core.NewUserText("go")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		nil,
		newEventSink(),
	)
	if out.Kind != OutcomeCancelled {
		t.Errorf("Kind = %v, want OutcomeCancelled", out.Kind)
	}
}

// cancelTestProvider is a Provider that emits a long script
// once. Used to test cancellation behavior.
type cancelTestProvider struct {
	script []api.StreamEvent
	calls  int
}

func (p *cancelTestProvider) Info() api.ModelInfo {
	return api.ModelInfo{ID: "fake-model", ContextWindow: 128_000}
}
func (p *cancelTestProvider) Stream(ctx context.Context, _ api.Request) (<-chan api.StreamEvent, <-chan error) {
	events := make(chan api.StreamEvent, 4)
	errs := make(chan error, 1)
	p.calls++
	go func() {
		defer close(events)
		defer close(errs)
		for _, ev := range p.script {
			// Simulate a real network: pause between events.
			// The small buffer (4) plus this pause means the
			// producer blocks on `events <- ev` quickly, giving
			// cancellation a chance to interleave.
			t := time.NewTimer(20 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			select {
			case <-ctx.Done():
				return
			case events <- ev:
			}
		}
	}()
	return events, errs
}

func TestRunQueryLoopToolPanicRecovery(t *testing.T) {
	// Build a tool that panics on first call. The loop should
	// catch the panic, append a tool_result{is_error: true} with
	// the panic message, and continue.
	panicTool := &recordingTool{
		name:   "Bash",
		result: tools.ToolResult{Text: "ok"},
	}
	panicTool.panicNext.Store(true)
	toolsList := []tools.Tool{panicTool}
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})

	fp := api.NewFakeProvider(api.ScriptToolCallThenText("Bash", []byte(`{}`), "after-panic")...)

	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("trigger panic")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		nil,
		newEventSink(),
	)
	if out.Kind != OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn (loop should survive panics)", out.Kind)
	}
	if got := out.Message.GetFirstText(); got != "after-panic" {
		t.Errorf("final message = %q, want 'after-panic' (recovery should let the loop continue)", got)
	}
	if panicTool.calls.Load() != 1 {
		t.Errorf("panic tool was called %d times, want 1", panicTool.calls.Load())
	}
}

func TestRunQueryLoopUsageAccumulates(t *testing.T) {
	// First turn: a tool call (so the loop continues) with
	// InputTokens=100, OutputTokens=50. Second turn: a text
	// response with InputTokens=110, OutputTokens=60. Together
	// the loop's Usage field should report the sum of both
	// turns and the CostTracker should see two calls.
	script1 := []api.StreamEvent{
		api.EventOfMessageStart("fake-model", &core.UsageInfo{InputTokens: 100}),
		api.EventOfBlockStart(0, core.ContentBlock{
			Kind: core.BlockToolUse,
			ToolUse: &core.ToolUse{
				ID:    "call_1",
				Name:  "Bash",
				Input: []byte(`{"command":"echo first"}`),
			},
		}),
		api.EventOfMessageDelta(api.StopToolUse, &core.UsageInfo{OutputTokens: 50}),
		api.EventOfMessageStop(),
	}
	script2 := []api.StreamEvent{
		api.EventOfMessageStart("fake-model", &core.UsageInfo{InputTokens: 110}),
		api.EventOfBlockStart(0, core.TextBlock("")),
		api.EventOfBlockDelta(0, api.TextDelta("second")),
		api.EventOfBlockStop(0),
		api.EventOfMessageDelta(api.StopEndTurn, &core.UsageInfo{OutputTokens: 60}),
		api.EventOfMessageStop(),
	}
	fp := api.NewFakeProvider(script1, script2)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})
	toolsList, _ := testToolsList(t)

	cost := core.NewCostTracker()
	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("test usage")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		cost,
		newEventSink(),
	)
	if out.Usage.InputTokens != 210 {
		t.Errorf("Usage.InputTokens = %d, want 210 (100+110)", out.Usage.InputTokens)
	}
	if out.Usage.OutputTokens != 110 {
		t.Errorf("Usage.OutputTokens = %d, want 110 (50+60)", out.Usage.OutputTokens)
	}
	if cost.Calls() != 2 {
		t.Errorf("CostTracker.Calls = %d, want 2", cost.Calls())
	}
}

func TestRunQueryLoopEventsAreEmitted(t *testing.T) {
	fp := api.NewFakeProvider(api.ScriptToolCallThenText("Bash", []byte(`{}`), "done")...)
	toolsList, _ := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})

	ch := newEventSink()
	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("ev")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		nil,
		ch,
	)
	_ = out
	close(ch)

	// Drain and classify the events.
	var toolStarts, toolEnds, turnCompletes int
	for ev := range ch {
		switch ev.(type) {
		case ToolStartEvent:
			toolStarts++
		case ToolEndEvent:
			toolEnds++
		case TurnCompleteEvent:
			turnCompletes++
		}
	}
	if toolStarts != 1 {
		t.Errorf("ToolStartEvent count = %d, want 1", toolStarts)
	}
	if toolEnds != 1 {
		t.Errorf("ToolEndEvent count = %d, want 1", toolEnds)
	}
	if turnCompletes != 2 {
		t.Errorf("TurnCompleteEvent count = %d, want 2", turnCompletes)
	}
}

func TestRunQueryLoopEmitsOutcomeEvent(t *testing.T) {
	// Every terminal state of RunQueryLoop MUST emit an
	// OutcomeEvent on the event channel with the same
	// Kind/Usage/Turns/Err as the returned Outcome. The
	// renderers (json, stream-json) rely on this instead of
	// inspecting the return value.
	fp := api.NewFakeProvider(api.ScriptTextResponse("hi"))
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})
	toolsList, _ := testToolsList(t)

	ch := newEventSink()
	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("hi")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		nil,
		ch,
	)
	close(ch)

	// Drain and find the terminal OutcomeEvent.
	var last *OutcomeEvent
	for ev := range ch {
		if oe, ok := ev.(OutcomeEvent); ok {
			// Capture a copy; the event is small enough to
			// copy by value but we'll keep the last one we
			// see.
			oe := oe
			last = &oe
		}
	}
	if last == nil {
		t.Fatal("no OutcomeEvent emitted on the event channel")
	}
	if last.Kind != out.Kind {
		t.Errorf("OutcomeEvent.Kind = %v, want %v (matches returned Outcome.Kind)", last.Kind, out.Kind)
	}
	if last.Turns != out.Turns {
		t.Errorf("OutcomeEvent.Turns = %d, want %d (matches returned Outcome.Turns)", last.Turns, out.Turns)
	}
	if last.Usage != out.Usage {
		t.Errorf("OutcomeEvent.Usage = %+v, want %+v (matches returned Outcome.Usage)", last.Usage, out.Usage)
	}
}

func TestRunQueryLoopNoProviderStreamErrorIsHandled(t *testing.T) {
	// An empty provider script emits just EventMessageStop with
	// no content; the loop should treat it as an end-turn with
	// an empty message rather than a crash.
	fp := api.NewFakeProvider([]api.StreamEvent{api.EventOfMessageStop()})
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})
	toolsList, _ := testToolsList(t)

	out := RunQueryLoop(
		context.Background(),
		fp,
		[]core.Message{core.NewUserText("hi")},
		toolsList,
		tc,
		Config{Model: "fake-model", MaxTurns: 5},
		nil,
		newEventSink(),
	)
	if out.Kind != OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn (empty stream)", out.Kind)
	}
}

// --- helpers ---

// contains is a tiny stdlib-only substring check.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// _ = errors — keep errors import in case future tests need
// it.
var _ = errors.New
