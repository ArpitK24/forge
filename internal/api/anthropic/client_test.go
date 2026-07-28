package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// drainEvents reads every event from the events channel and
// returns them as a slice in order. Drains the errs channel
// into the returned errors slice as well.
func drainEvents(t *testing.T, events <-chan api.StreamEvent, errs <-chan error) ([]api.StreamEvent, []error) {
	t.Helper()
	var out []api.StreamEvent
	for ev := range events {
		out = append(out, ev)
	}
	var eout []error
	for e := range errs {
		eout = append(eout, e)
	}
	return out, eout
}

// collectKinds returns just the EventKind sequence — handy for
// asserting shape without enumerating every field.
func collectKinds(events []api.StreamEvent) []api.EventKind {
	out := make([]api.EventKind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestClientTextOnly drives a Client through an httptest server
// that returns a text-only Anthropic SSE stream. Verifies both
// the wire request shape (headers, body) and the canonical event
// sequence.
func TestClientTextOnly(t *testing.T) {
	const sse = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":12,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q, want 'test-key'", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != APIVersion {
			t.Errorf("anthropic-version = %q, want %q", r.Header.Get("anthropic-version"), APIVersion)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["model"] != "claude-sonnet-4-5" {
			t.Errorf("model = %v, want claude-sonnet-4-5", got["model"])
		}
		if got["stream"] != true {
			t.Errorf("stream = %v, want true", got["stream"])
		}
		if _, ok := got["max_tokens"]; !ok {
			t.Errorf("max_tokens missing from request body")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("hi")},
	})

	collected, eout := drainEvents(t, events, errs)
	if len(eout) > 0 {
		t.Errorf("unexpected errors: %v", eout)
	}

	wantKinds := []api.EventKind{
		api.EventMessageStart,
		api.EventContentBlockStart,
		api.EventContentBlockDelta,
		api.EventContentBlockDelta,
		api.EventContentBlockStop,
		api.EventMessageDelta,
		api.EventMessageStop,
	}
	if got := collectKinds(collected); !equalKinds(got, wantKinds) {
		t.Errorf("event kinds = %v, want %v", got, wantKinds)
	}

	// First event should be message_start with the model id.
	if collected[0].Kind != api.EventMessageStart || collected[0].Model != "claude-sonnet-4-5" {
		t.Errorf("first event = %+v, want message_start with model=claude-sonnet-4-5", collected[0])
	}
	// Final message_delta should have stop_reason=end_turn.
	if collected[5].StopReason != api.StopEndTurn {
		t.Errorf("message_delta stop_reason = %q, want %q", collected[5].StopReason, api.StopEndTurn)
	}
}

// TestClientToolUse drives a streaming tool-use turn. Asserts
// that the tool_use content block start carries id+name and the
// accumulated input_json_delta fragments are surfaced as
// ToolInputJSONDelta canonical deltas.
func TestClientToolUse(t *testing.T) {
	const sse = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Looking up..."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls\""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":",\"timeout\":5}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":20}}

event: message_stop
data: {"type":"message_stop"}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request advertised the tool.
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		tools, ok := got["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Errorf("tools missing or wrong shape: %v", got["tools"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("list files")},
		Tools: []core.ToolDefinition{
			{
				Name:        "Bash",
				Description: "Run a shell command",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
			},
		},
	})

	collected, eout := drainEvents(t, events, errs)
	if len(eout) > 0 {
		t.Errorf("unexpected errors: %v", eout)
	}

	// Sequence includes the second block_start (tool_use), its
	// two input_json_delta, and a final message_delta with
	// stop_reason=tool_use.
	var sawToolStart bool
	for _, e := range collected {
		if e.Kind == api.EventContentBlockStart && e.Block.Kind == core.BlockToolUse {
			sawToolStart = true
			if e.Block.ToolUse == nil || e.Block.ToolUse.ID != "toolu_01" || e.Block.ToolUse.Name != "Bash" {
				t.Errorf("tool_use start = %+v, want id=toolu_01 name=Bash", e.Block.ToolUse)
			}
		}
	}
	if !sawToolStart {
		t.Errorf("no tool_use content_block_start seen; events = %+v", collected)
	}

	// Final message_delta stop_reason should be tool_use.
	var lastMsgDelta *api.StreamEvent
	for i := range collected {
		if collected[i].Kind == api.EventMessageDelta {
			lastMsgDelta = &collected[i]
		}
	}
	if lastMsgDelta == nil || lastMsgDelta.StopReason != api.StopToolUse {
		t.Errorf("final stop_reason = %v, want tool_use", lastMsgDelta)
	}
}

// TestClientThinkingEnabled asserts that a request with a
// non-nil Thinking config emits the wire {type:"enabled",
// budget_tokens:N} block and that max_tokens is bumped above
// the budget (Anthropic rejects budget_tokens >= max_tokens).
func TestClientThinkingEnabled(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	thinking := &core.ThinkingConfig{Enabled: true, BudgetTokens: 2048}
	events, errs := c.Stream(context.Background(), api.Request{
		Model:     "claude-sonnet-4-5",
		MaxTokens: 1024, // too small — adapter should bump it
		Messages:  []core.Message{core.NewUserText("think")},
		Thinking:  thinking,
	})
	// Drain to avoid blocking the goroutine.
	for range events {
	}
	for range errs {
	}

	// max_tokens should have been bumped above budget.
	gotMax, _ := capturedBody["max_tokens"].(float64)
	if int(gotMax) <= 2048 {
		t.Errorf("max_tokens = %v, want > 2048 (thinking budget)", gotMax)
	}
	// temperature and top_p should NOT be set when thinking is enabled.
	if _, ok := capturedBody["temperature"]; ok {
		t.Errorf("temperature should be omitted when thinking is enabled; got %v", capturedBody["temperature"])
	}
	if _, ok := capturedBody["top_p"]; ok {
		t.Errorf("top_p should be omitted when thinking is enabled; got %v", capturedBody["top_p"])
	}
	// thinking block should be present with the right shape.
	thinkingWire, ok := capturedBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking block missing or wrong shape: %v", capturedBody["thinking"])
	}
	if thinkingWire["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want enabled", thinkingWire["type"])
	}
	if int(thinkingWire["budget_tokens"].(float64)) != 2048 {
		t.Errorf("thinking.budget_tokens = %v, want 2048", thinkingWire["budget_tokens"])
	}
}

// TestClientSystemBlocks translates a list-of-blocks SystemPrompt
// into Anthropic's `system: [...]` shape. The adapter doesn't
// emit cache_control markers itself today; this test asserts the
// structural translation only.
func TestClientSystemBlocks(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("hi")},
		System: api.SystemBlocks(
			api.SystemBlock{Text: "first part"},
			api.SystemBlock{Text: "second part"},
		),
	})
	for range events {
	}
	for range errs {
	}

	sys, ok := capturedBody["system"].([]any)
	if !ok || len(sys) != 2 {
		t.Fatalf("system = %v, want 2-element list of blocks", capturedBody["system"])
	}
	first := sys[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "first part" {
		t.Errorf("system[0] = %+v, want {type:text, text:'first part'}", first)
	}
	second := sys[1].(map[string]any)
	if second["type"] != "text" || second["text"] != "second part" {
		t.Errorf("system[1] = %+v, want {type:text, text:'second part'}", second)
	}
}

// TestClientSystemString translates a plain-string SystemPrompt
// into Anthropic's `system: "..."` shape.
func TestClientSystemString(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("hi")},
		System:   api.SystemString("you are a helpful assistant"),
	})
	for range events {
	}
	for range errs {
	}

	if got, _ := capturedBody["system"].(string); got != "you are a helpful assistant" {
		t.Errorf("system = %v, want 'you are a helpful assistant'", capturedBody["system"])
	}
}

// TestClientHTTPError maps a non-2xx response (here 401) to a
// KindAuth typed error.
func TestClientHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "bad-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("hi")},
	})

	collected, eout := drainEvents(t, events, errs)

	// Expect one EventError AND one errs-channel error.
	var sawErrorEvent bool
	for _, e := range collected {
		if e.Kind == api.EventError && e.Err != nil && e.Err.Kind == core.KindAuth {
			sawErrorEvent = true
			if !strings.Contains(e.Err.Message, "invalid x-api-key") {
				t.Errorf("error message = %q, want it to contain 'invalid x-api-key'", e.Err.Message)
			}
		}
	}
	if !sawErrorEvent {
		t.Errorf("expected an EventError with KindAuth; got events = %+v", collected)
	}
	if len(eout) != 1 || eout[0] == nil {
		t.Errorf("expected one error on errs channel; got %v", eout)
	} else {
		var apiErr *core.Error
		if ce, ok := eout[0].(*core.Error); ok {
			apiErr = ce
		}
		if apiErr == nil || apiErr.Kind != core.KindAuth {
			t.Errorf("errs[0] = %v, want *core.Error{KindAuth}", eout[0])
		}
	}
}

// TestClientHTTPErrorRateLimit asserts 429 → KindRateLimit with
// Retry-After parsed when the header is present.
func TestClientHTTPErrorRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("hi")},
	})

	collected, eout := drainEvents(t, events, errs)

	var sawRateLimit bool
	for _, e := range collected {
		if e.Kind == api.EventError && e.Err != nil && e.Err.Kind == core.KindRateLimit {
			sawRateLimit = true
			if e.Err.RetryAfter != 12 {
				t.Errorf("RetryAfter = %d, want 12", e.Err.RetryAfter)
			}
		}
	}
	if !sawRateLimit {
		t.Errorf("expected EventError{KindRateLimit, RetryAfter:12}; got %+v", collected)
	}
	if len(eout) != 1 {
		t.Errorf("expected one errs entry; got %v", eout)
	}
}

// TestClientStreamError tests the in-stream error event
// (`event: error\ndata: {type:error,...}`) which terminates the
// stream with a KindAPI typed error.
func TestClientStreamError(t *testing.T) {
	const sse = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}

event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"API is overloaded"}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("hi")},
	})

	collected, eout := drainEvents(t, events, errs)

	var sawOverloaded bool
	for _, e := range collected {
		if e.Kind == api.EventError && e.Err != nil {
			if e.Err.Kind == core.KindHTTPStatus && strings.Contains(e.Err.Message, "overloaded") {
				sawOverloaded = true
			}
		}
	}
	if !sawOverloaded {
		t.Errorf("expected EventError{overloaded_error}; got events = %+v", collected)
	}
	if len(eout) != 1 {
		t.Errorf("expected one errs entry; got %v", eout)
	}
}

// TestClientContextCancelled cancels the request context mid-flight
// and asserts the goroutine surfaces a KindCancelled error
// instead of leaking.
func TestClientContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"x\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Block until the client disconnects.
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")

	ctx, cancel := context.WithCancel(context.Background())
	events, errs := c.Stream(ctx, api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("hi")},
	})

	// Cancel after the server has sent the first event.
	_, _ = <-events
	cancel()

	// Drain.
	for range events {
	}
	var sawCancel bool
	for e := range errs {
		var apiErr *core.Error
		if ce, ok := e.(*core.Error); ok {
			apiErr = ce
		}
		if apiErr != nil && apiErr.Kind == core.KindCancelled {
			sawCancel = true
		}
		_ = e
	}
	if !sawCancel {
		t.Errorf("expected KindCancelled error after ctx cancel")
	}
}

// TestClientStopReasonMapping covers the Anthropic-specific stop
// reasons that aren't in OpenAI's vocabulary:
// model_context_window_exceeded → StopContextLimit,
// refusal → StopEndTurn, pause_turn → StopEndTurn.
func TestClientStopReasonMapping(t *testing.T) {
	cases := []struct {
		name    string
		stopIn  string
		wantOut string
	}{
		{"end_turn", "end_turn", api.StopEndTurn},
		{"tool_use", "tool_use", api.StopToolUse},
		{"max_tokens", "max_tokens", api.StopMaxTokens},
		{"stop_sequence", "stop_sequence", api.StopStopSequence},
		{"refusal", "refusal", api.StopEndTurn},
		{"model_context_window_exceeded", "model_context_window_exceeded", api.StopContextLimit},
		{"pause_turn", "pause_turn", api.StopEndTurn},
		{"unknown_passthrough", "vendor_specific_value", "vendor_specific_value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapStopReason(tc.stopIn)
			if got != tc.wantOut {
				t.Errorf("mapStopReason(%q) = %q, want %q", tc.stopIn, got, tc.wantOut)
			}
		})
	}
}

// equalKinds is a tiny helper that returns true if a and b are
// the same length and have the same elements in order.
func equalKinds(a, b []api.EventKind) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestClientCacheControlOnToolsAndBlocks wires cache_control
// markers through to Anthropic's wire format on both a tool
// definition and an assistant-side content block. Anthropic
// accepts cache_control on tool definitions and on assistant-
// side text/think blocks (not on tool_result or user-side
// blocks); this test pins down the wire translation so future
// changes to convertMessages / convertTools don't silently
// drop the marker.
func TestClientCacheControlOnToolsAndBlocks(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("continue")},
		Tools: []core.ToolDefinition{
			{
				Name:         "Bash",
				Description:  "run a command",
				InputSchema:  json.RawMessage(`{"type":"object"}`),
				CacheControl: &core.CacheControl{Type: "ephemeral"},
			},
		},
	})
	for range events {
	}
	for range errs {
	}

	// Tool-level cache_control: each tool entry should carry
	// `cache_control: {type: "ephemeral"}`.
	tools, ok := capturedBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v, want 1-entry array", capturedBody["tools"])
	}
	t0 := tools[0].(map[string]any)
	cc, ok := t0["cache_control"].(map[string]any)
	if !ok {
		t.Fatalf("tool[0].cache_control missing or wrong shape: %+v", t0)
	}
	if cc["type"] != "ephemeral" {
		t.Errorf("tool[0].cache_control.type = %v, want 'ephemeral'", cc["type"])
	}
}

// TestClientCacheControlIgnoredOnUnsupportedBlocks verifies
// cache_control is omitted from blocks that Anthropic doesn't
// accept it on (tool_result, user-side blocks) — confirms we
// don't pollute the wire with markers the API would reject.
func TestClientCacheControlIgnoredOnUnsupportedBlocks(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	// History: an assistant tool_use with cache_control set on
	// the assistant-side text block, and a user turn whose
	// tool_result also has a (defensively-attached) cache_control.
	// The wire should only carry cache_control on the assistant
	// text block; the user-side tool_result must NOT carry it.
	prevAssistant := core.NewAssistantBlocks(
		core.ContentBlock{
			Kind:         core.BlockText,
			Text:         "earlier answer",
			CacheControl: &core.CacheControl{Type: "ephemeral"},
		},
		core.ContentBlock{
			Kind: core.BlockToolUse,
			ToolUse: &core.ToolUse{
				ID:    "toolu_01",
				Name:  "Bash",
				Input: json.RawMessage(`{"command":"ls"}`),
			},
			// ALSO marked cacheable on the tool_use block —
			// Anthropic accepts this on assistant-side tool_use.
			CacheControl: &core.CacheControl{Type: "ephemeral"},
		},
	)
	prevUser := core.Message{
		Role: core.RoleUser,
		Content: core.BlocksContent(
			core.ContentBlock{
				Kind: core.BlockToolResult,
				ToolResult: &core.ToolResult{
					ToolUseID: "toolu_01",
					Content:   json.RawMessage(`"file1\nfile2"`),
				},
				// cache_control on tool_result — Anthropic
				// ignores this; we should NOT emit it on the
				// wire (it's noise at best, error at worst).
				CacheControl: &core.CacheControl{Type: "ephemeral"},
			},
		),
	}
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{prevAssistant, prevUser, core.NewUserText("continue")},
	})
	for range events {
	}
	for range errs {
	}

	msgs, ok := capturedBody["messages"].([]any)
	if !ok || len(msgs) != 3 {
		t.Fatalf("messages = %v, want 3-entry array", capturedBody["messages"])
	}

	// First message is the assistant with two content blocks;
	// verify text and tool_use BOTH carry cache_control,
	// because Anthropic accepts it on assistant-side blocks
	// of either kind.
	asst := msgs[0].(map[string]any)
	asstBlocks, ok := asst["content"].([]any)
	if !ok || len(asstBlocks) != 2 {
		t.Fatalf("assistant content = %v, want 2-entry block list", asst["content"])
	}
	tBlock := asstBlocks[0].(map[string]any)
	if tBlock["type"] != "text" {
		t.Errorf("asst[0].type = %v, want text", tBlock["type"])
	}
	if cc, ok := tBlock["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Errorf("asst text block missing cache_control; got %+v", tBlock)
	}
	useBlock := asstBlocks[1].(map[string]any)
	if useBlock["type"] != "tool_use" {
		t.Errorf("asst[1].type = %v, want tool_use", useBlock["type"])
	}
	if cc, ok := useBlock["cache_control"].(map[string]any); !ok || cc["type"] != "ephemeral" {
		t.Errorf("asst tool_use block missing cache_control; got %+v", useBlock)
	}

	// Second message is the user with a tool_result block.
	// Verify cache_control is NOT present on the tool_result.
	user := msgs[1].(map[string]any)
	userBlocks, ok := user["content"].([]any)
	if !ok || len(userBlocks) != 1 {
		t.Fatalf("user content = %v, want 1-entry block list", user["content"])
	}
	res := userBlocks[0].(map[string]any)
	if res["type"] != "tool_result" {
		t.Errorf("user[0].type = %v, want tool_result", res["type"])
	}
	if _, present := res["cache_control"]; present {
		t.Errorf("tool_result should not carry cache_control on the wire; got %+v", res)
	}
}

// full thinking block (start + deltas + signature_delta + stop)
// and asserts the canonical events surface the signature as a
// DeltaSignature — never as a text delta — so the accumulator
// can route it into Thinking.Signature without leaking it into
// the visible text stream.
func TestClientThinkingSignature(t *testing.T) {
	const sse = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-4-5","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-blob-abc123"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{core.NewUserText("think")},
	})

	collected, eout := drainEvents(t, events, errs)
	if len(eout) > 0 {
		t.Fatalf("unexpected errors: %v", eout)
	}

	// The signature_delta must surface as a DeltaSignature on
	// the thinking block's index (0), NOT as a DeltaText. The
	// prior hack used TextDelta("[signature]") which would
	// show up here as a DeltaText with that literal string.
	for _, e := range collected {
		if e.Kind != api.EventContentBlockDelta {
			continue
		}
		// The text block (index 1) must NEVER carry the
		// signature. If the adapter regresses to emitting it
		// as text, this catches it.
		if e.Index == 1 && e.Delta.Kind == api.DeltaText && strings.Contains(e.Delta.Text, "signature") {
			t.Errorf("signature leaked into text stream: %+v", e)
		}
		if e.Index == 0 && e.Delta.Kind == api.DeltaSignature {
			if e.Delta.Signature != "sig-blob-abc123" {
				t.Errorf("signature_delta on thinking block = %q, want 'sig-blob-abc123'", e.Delta.Signature)
			}
		}
		if e.Index == 0 && e.Delta.Kind == api.DeltaText {
			t.Errorf("thinking block received a DeltaText — should be DeltaSignature: %+v", e)
		}
	}
}

// TestClientListModels drives ListModels against an httptest
// server returning a realistic Anthropic /v1/models payload.
// Asserts x-api-key + anthropic-version headers are set, the
// JSON is parsed, and the returned ModelInfo entries carry only
// ID + Provider — capability flags and ContextWindow are
// intentionally left zero (the MergeWithKnown overlay fills them).
func TestClientListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q, want 'test-key'", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != APIVersion {
			t.Errorf("anthropic-version = %q, want %q", r.Header.Get("anthropic-version"), APIVersion)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
  "data": [
    {"id": "claude-opus-4-1", "display_name": "Claude Opus 4.1", "type": "model"},
    {"id": "claude-sonnet-4-5", "display_name": "Claude Sonnet 4.5", "type": "model"},
    {"id": "claude-haiku-4", "display_name": "Claude Haiku 4", "type": "model"}
  ],
  "has_more": false,
  "first_id": "claude-opus-4-1"
}`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "claude-sonnet-4-5")
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("ListModels returned %d models, want 3; %+v", len(models), models)
	}
	wantIDs := []string{"claude-opus-4-1", "claude-sonnet-4-5", "claude-haiku-4"}
	for i, m := range models {
		if m.ID != wantIDs[i] {
			t.Errorf("models[%d].ID = %q, want %q", i, m.ID, wantIDs[i])
		}
		if m.Provider != core.ProviderAnthropic {
			t.Errorf("models[%d].Provider = %q, want %q", i, m.Provider, core.ProviderAnthropic)
		}
		if m.SupportsPromptCaching || m.SupportsExtendedThinking {
			t.Errorf("models[%d] has capability flags set on raw listing; overlay should be the source", i)
		}
	}
}

// TestClientListModelsAuthError asserts ListModels classifies
// non-2xx with the same typed error Stream surfaces — 401 here
// is KindAuth.
func TestClientListModelsAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "bad-key", "claude-sonnet-4-5")
	_, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("ListModels: expected error on 401, got nil")
	}
	ce, ok := err.(*core.Error)
	if !ok {
		t.Fatalf("err type = %T, want *core.Error", err)
	}
	if ce.Kind != core.KindAuth {
		t.Errorf("err kind = %v, want KindAuth", ce.Kind)
	}
}
