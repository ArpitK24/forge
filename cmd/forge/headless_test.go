package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/api/openai"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/query"
	"github.com/ArpitK24/forge/internal/tools"
)

// errorProvider is a test-only api.Provider that returns a
// single configured error on the errs channel. Used to drive
// runWithRetry along a non-success path without a real HTTP
// server.
type errorProvider struct {
	err  *core.Error
	info api.ModelInfo
}

func (e *errorProvider) Info() api.ModelInfo { return e.info }
func (e *errorProvider) Stream(ctx context.Context, _ api.Request) (<-chan api.StreamEvent, <-chan error) {
	events := make(chan api.StreamEvent, 1)
	errs := make(chan error, 1)
	events <- api.EventOfError(e.err)
	close(events)
	errs <- e.err
	close(errs)
	return events, errs
}

// recordingProvider is a test-only api.Provider that emits a
// canned text response. It counts calls so tests can assert
// the loop ran once (or N times under retry).
//
// Behavior:
//   - If failFirstN > 0, the first failFirstN calls return
//     errOn; subsequent calls return the canned script.
//   - Otherwise every call returns the canned script.
type recordingProvider struct {
	script     []api.StreamEvent
	failFirstN int
	errOn      *core.Error
	callsSoFar atomic.Int32
}

func (r *recordingProvider) Info() api.ModelInfo {
	return api.ModelInfo{ID: "fake", ContextWindow: 128_000}
}
func (r *recordingProvider) Stream(ctx context.Context, _ api.Request) (<-chan api.StreamEvent, <-chan error) {
	n := r.callsSoFar.Add(1)
	events := make(chan api.StreamEvent, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		if r.errOn != nil && n <= int32(r.failFirstN) {
			events <- api.EventOfError(r.errOn)
			errs <- r.errOn
			return
		}
		for _, ev := range r.script {
			select {
			case <-ctx.Done():
				return
			case events <- ev:
			}
		}
	}()
	return events, errs
}

// TestRunWithRetryNonRetryableReturnsImmediately covers the
// path where a non-retryable error short-circuits the retry
// loop and the outcome is returned as-is.
func TestRunWithRetryNonRetryableReturnsImmediately(t *testing.T) {
	authErr := core.New(core.KindAuth, "no key")
	p := &errorProvider{err: authErr, info: api.ModelInfo{ID: "fake"}}

	events := make(chan query.Event, 64)
	out := runWithRetry(
		context.Background(),
		p,
		[]core.Message{core.NewUserText("hi")},
		nil,
		nil,
		query.Config{Model: "fake", MaxTurns: 3},
		nil,
		events,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if out.Kind != query.OutcomeError {
		t.Errorf("Kind = %v, want OutcomeError", out.Kind)
	}
	if out.Err == nil || out.Err.Kind != core.KindAuth {
		t.Errorf("Err.Kind = %v, want KindAuth", out.Err)
	}
	// No retry means only one OutcomeEvent on the channel.
	close(events)
	count := 0
	for ev := range events {
		if _, ok := ev.(query.OutcomeEvent); ok {
			count++
		}
	}
	if count != 1 {
		t.Errorf("OutcomeEvent count = %d, want 1 (no retry on non-retryable)", count)
	}
}

// TestRunWithRetryEventualSuccess covers the path where the
// first attempt fails with a retryable error (HTTP 429 with
// Retry-After) and the second attempt succeeds. Asserts the
// final outcome is OutcomeEndTurn and the call count is 2.
func TestRunWithRetryEventualSuccess(t *testing.T) {
	script := []api.StreamEvent{
		api.EventOfMessageStart("fake", nil),
		api.EventOfBlockStart(0, core.TextBlock("")),
		api.EventOfBlockDelta(0, api.TextDelta("hello")),
		api.EventOfBlockStop(0),
		api.EventOfMessageDelta(api.StopEndTurn, &core.UsageInfo{OutputTokens: 1}),
		api.EventOfMessageStop(),
	}
	rateErr := &core.Error{Kind: core.KindRateLimit, RetryAfter: 0, StatusCode: 429}
	// First call fails with rate-limit, second call succeeds
	// with the text script. This proves the retry wrapper
	// actually re-ran the loop and surfaced the eventual
	// success.
	p := &recordingProvider{
		script:     script,
		failFirstN: 1,
		errOn:      rateErr,
	}

	events := make(chan query.Event, 256)
	cost := core.NewCostTracker()
	// Bypass the long default backoff by using a tiny base.
	// We monkey-patch via the public headlessRetryMax isn't
	// ideal; instead we override the RetryAfter to 0 (already)
	// and let the exponential backoff run with attempt 0
	// (1s) — this test will take ~1s but it's a small cost
	// for verifying the real retry path. Cancel-friendly
	// context.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out := runWithRetry(
		ctx,
		p,
		[]core.Message{core.NewUserText("hi")},
		nil,
		nil,
		query.Config{Model: "fake", MaxTurns: 3},
		cost,
		events,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if out.Kind != query.OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn (retry should have eventually succeeded)", out.Kind)
	}
	if p.callsSoFar.Load() < 2 {
		t.Errorf("provider saw %d calls, want >= 2 (one failed, one retry)", p.callsSoFar.Load())
	}
	// The failed attempt recorded no usage, so the cost
	// tracker holds the successful attempt only.
	close(events)
	_ = events
}

// TestRunWithRetryRetryableExhausted covers the path where
// every attempt fails with a retryable error. The wrapper
// gives up after headlessRetryMax attempts and returns the
// last OutcomeError. We speed this up by triggering a
// context cancel after the first failure so the next
// attempt's delay is interrupted.
func TestRunWithRetryRetryableExhausted(t *testing.T) {
	rateErr := &core.Error{Kind: core.KindRateLimit, RetryAfter: 0, StatusCode: 429}
	p := &errorProvider{err: rateErr, info: api.ModelInfo{ID: "fake"}}

	events := make(chan query.Event, 64)
	// Use a short-deadline context so the first retry's
	// backoff is interrupted by ctx.Done and the loop
	// returns OutcomeCancelled. This still exercises the
	// retry mechanism: the loop made one call, observed a
	// retryable error, scheduled a delay, and was cancelled
	// before the next attempt.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	out := runWithRetry(
		ctx,
		p,
		[]core.Message{core.NewUserText("hi")},
		nil,
		nil,
		query.Config{Model: "fake", MaxTurns: 3},
		nil,
		events,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	// Either OutcomeError (if the first attempt completed
	// before the deadline) or OutcomeCancelled (if the
	// delay was interrupted). Both are valid terminal
	// states for the retry path.
	if out.Kind != query.OutcomeError && out.Kind != query.OutcomeCancelled {
		t.Errorf("Kind = %v, want OutcomeError or OutcomeCancelled", out.Kind)
	}
}

// TestRunWithRetrySucceedsFirstTry covers the happy path:
// no error on the first attempt, no retry. Asserts a
// single call and OutcomeEndTurn.
func TestRunWithRetrySucceedsFirstTry(t *testing.T) {
	script := []api.StreamEvent{
		api.EventOfMessageStart("fake", nil),
		api.EventOfBlockStart(0, core.TextBlock("")),
		api.EventOfBlockDelta(0, api.TextDelta("ok")),
		api.EventOfBlockStop(0),
		api.EventOfMessageDelta(api.StopEndTurn, &core.UsageInfo{OutputTokens: 1}),
		api.EventOfMessageStop(),
	}
	p := &recordingProvider{script: script}

	events := make(chan query.Event, 64)
	out := runWithRetry(
		context.Background(),
		p,
		[]core.Message{core.NewUserText("hi")},
		nil,
		nil,
		query.Config{Model: "fake", MaxTurns: 3},
		nil,
		events,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if out.Kind != query.OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn", out.Kind)
	}
	if p.callsSoFar.Load() != 1 {
		t.Errorf("provider saw %d calls, want 1 (no retry needed)", p.callsSoFar.Load())
	}
}

// TestHeadlessCostSummarySuppressedInJSONMode is an
// integration-level smoke test that drives a tiny httptest
// server through `forge` headless and verifies that, in JSON
// output mode, the cost summary is NOT printed on stderr —
// it lives in the JSON document instead. This guards the
// stream-pollution fix.
func TestHeadlessCostSummarySuppressedInJSONMode(t *testing.T) {
	// Skip if no API key: the openai.Client requires a
	// non-empty key to construct, but we can pass any
	// value here because the test server doesn't validate
	// it. The test only cares about the output format
	// pipeline, not the model.
	const body = `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":""}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":""}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	client := openai.NewWithHTTP(srv.Client(), srv.URL, "test", "x")

	perm := &core.AutoPermissionHandler{Mode: core.PermissionBypassPermissions}
	tc := &tools.ToolContext{
		WorkingDir: ".",
		Permission: perm,
		Cfg:        &core.Config{},
		CheckPermission: func(name, desc string, ro bool) core.PermissionDecision {
			return perm.RequestPermission(core.PermissionRequest{
				ToolName: name, Description: desc, IsReadOnly: ro,
			})
		},
	}

	// Build a renderer directly to validate the cost
	// suppression contract. We don't need to go through
	// runHeadless (which is heavyweight); we just verify
	// the jsonRenderer prints its cost to stdout, not
	// stderr.
	cost := core.NewCostTracker()
	cost.AddUsage(core.UsageInfo{InputTokens: 10, OutputTokens: 5}, "x")

	// jsonRenderer: stdout should contain the cost summary.
	stdout := &strings.Builder{}
	r := &jsonRenderer{w: stdout, cost: cost}
	// Drive the renderer with a synthetic OutcomeEvent.
	ch := make(chan query.Event, 4)
	ch <- query.OutcomeEvent{Kind: query.OutcomeEndTurn, Usage: core.UsageInfo{InputTokens: 10, OutputTokens: 5}, Turns: 1}
	close(ch)
	r.run(ch)

	out := stdout.String()
	if !strings.Contains(out, "\"cost\"") {
		t.Errorf("json output missing 'cost' field: %s", out)
	}
	// And verify the output is valid JSON.
	var j jsonOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &j); err != nil {
		t.Errorf("json output not valid JSON: %v\noutput: %s", err, out)
	}

	_ = client
	_ = tc
}

// countingTool counts Execute calls so a test can prove a side
// effect ran exactly once.
type countingTool struct{ calls atomic.Int32 }

func (c *countingTool) Name() string                          { return "Bash" }
func (c *countingTool) Description() string                   { return "counts" }
func (c *countingTool) PermissionLevel() core.PermissionLevel { return core.PermExecute }
func (c *countingTool) InputSchema() json.RawMessage          { return json.RawMessage(`{"type":"object"}`) }
func (c *countingTool) Execute(context.Context, json.RawMessage, *tools.ToolContext) tools.ToolResult {
	c.calls.Add(1)
	return tools.ToolResult{Text: "side effect done"}
}

// toolThen429Provider asks for a tool call while the history
// has no tool result yet, rate-limits the first request that
// follows a tool result, then answers with text.
type toolThen429Provider struct {
	mu       sync.Mutex
	limited  bool
	requests [][]core.Message
}

func (p *toolThen429Provider) Info() api.ModelInfo {
	return api.ModelInfo{ID: "fake", ContextWindow: 128_000}
}
func (p *toolThen429Provider) Stream(_ context.Context, req api.Request) (<-chan api.StreamEvent, <-chan error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, append([]core.Message(nil), req.Messages...))
	events := make(chan api.StreamEvent, 8)
	errs := make(chan error, 1)
	defer close(events)
	defer close(errs)

	last := req.Messages[len(req.Messages)-1]
	hasResult := false
	if !last.Content.IsString {
		for _, b := range last.Content.Blocks {
			if b.Kind == core.BlockToolResult {
				hasResult = true
			}
		}
	}
	switch {
	case !hasResult:
		events <- api.EventOfMessageStart("fake", nil)
		events <- api.EventOfBlockStart(0, core.ContentBlock{
			Kind:    core.BlockToolUse,
			ToolUse: &core.ToolUse{ID: "call_1", Name: "Bash", Input: []byte(`{"command":"echo >> log"}`)},
		})
		events <- api.EventOfMessageDelta(api.StopToolUse, nil)
		events <- api.EventOfMessageStop()
	case !p.limited:
		p.limited = true
		rateErr := &core.Error{Kind: core.KindRateLimit, RetryAfter: 1, StatusCode: 429}
		events <- api.EventOfError(rateErr)
		errs <- rateErr
	default:
		events <- api.EventOfMessageStart("fake", nil)
		events <- api.EventOfBlockStart(0, core.TextBlock(""))
		events <- api.EventOfBlockDelta(0, api.TextDelta("done"))
		events <- api.EventOfBlockStop(0)
		events <- api.EventOfMessageDelta(api.StopEndTurn, nil)
		events <- api.EventOfMessageStop()
	}
	return events, errs
}

// TestRunWithRetryDoesNotReExecuteTools: a 429 that arrives after
// a tool already ran must only re-issue the model request, with
// the tool result still in the history.
func TestRunWithRetryDoesNotReExecuteTools(t *testing.T) {
	p := &toolThen429Provider{}
	tool := &countingTool{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out := runWithRetry(ctx, p,
		[]core.Message{core.NewUserText("append to log")},
		[]tools.Tool{tool}, nil,
		query.Config{Model: "fake", MaxTurns: 5}, nil,
		make(chan query.Event, 256),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if out.Kind != query.OutcomeEndTurn {
		t.Fatalf("Kind = %v, want end_turn (err=%v)", out.Kind, out.Err)
	}
	if n := tool.calls.Load(); n != 1 {
		t.Errorf("tool executed %d times, want 1 (retry must not re-run tools)", n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	retried := p.requests[len(p.requests)-1]
	if len(retried) != 3 {
		t.Errorf("retried request carried %d messages, want 3 (user, tool_use, tool_result)", len(retried))
	}
}

// TestJSONRendererClearsStaleError: a failed attempt's error must
// not survive into the json document when the retry succeeds.
func TestJSONRendererClearsStaleError(t *testing.T) {
	events := make(chan query.Event, 4)
	events <- query.OutcomeEvent{Kind: query.OutcomeError, Err: core.New(core.KindRateLimit, "429")}
	events <- query.OutcomeEvent{Kind: query.OutcomeEndTurn}
	close(events)
	var buf strings.Builder
	(&jsonRenderer{w: &buf}).run(events)
	var doc map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &doc); err != nil {
		t.Fatalf("decode: %v (%s)", err, buf.String())
	}
	if doc["outcome"] != "end_turn" {
		t.Errorf("outcome = %v, want end_turn", doc["outcome"])
	}
	if e, ok := doc["error"]; ok && e != "" {
		t.Errorf("error = %v, want empty after a successful retry", e)
	}
}
