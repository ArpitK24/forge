package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// TestClientTextOnly drives a Client through an httptest
// server that returns a text-only Chat Completions SSE
// stream. The test asserts the canonical event sequence: a
// message start, a text block start, two text deltas, a block
// stop, a message delta, a message stop, and no errors.
func TestClientTextOnly(t *testing.T) {
	const body = `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":""}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"content":"hello "},"finish_reason":""}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":""}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request shape so a regression in
		// buildRequestBody is caught here, not by a stray
		// network call.
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth = %q, want 'Bearer test-key'", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("accept = %q, want text/event-stream", r.Header.Get("Accept"))
		}
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["model"] != "meta/llama-3.3-70b-instruct" {
			t.Errorf("model = %v, want meta/llama-3.3-70b-instruct", got["model"])
		}
		if got["stream"] != true {
			t.Errorf("stream = %v, want true", got["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "meta/llama-3.3-70b-instruct")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "meta/llama-3.3-70b-instruct",
		Messages: []core.Message{core.NewUserText("hi")},
	})

	var collected []api.StreamEvent
	for ev := range events {
		collected = append(collected, ev)
	}
	if err := <-errs; err != nil {
		t.Errorf("errs channel emitted: %v", err)
	}

	// Sequence: message_start, block_start(0,text), block_delta("hello "),
	// block_delta("world"), block_stop(0), message_delta(end_turn),
	// message_stop.
	want := []api.EventKind{
		api.EventMessageStart,
		api.EventContentBlockStart,
		api.EventContentBlockDelta,
		api.EventContentBlockDelta,
		api.EventContentBlockStop,
		api.EventMessageDelta,
		api.EventMessageStop,
	}
	if len(collected) != len(want) {
		t.Fatalf("event count = %d, want %d (events: %+v)", len(collected), len(want), collected)
	}
	for i, ev := range collected {
		if ev.Kind != want[i] {
			t.Errorf("event[%d] kind = %v, want %v", i, ev.Kind, want[i])
		}
	}
	// First delta should be "hello ", second "world".
	if collected[2].Delta.Text != "hello " {
		t.Errorf("first text delta = %q, want 'hello '", collected[2].Delta.Text)
	}
	if collected[3].Delta.Text != "world" {
		t.Errorf("second text delta = %q, want 'world'", collected[3].Delta.Text)
	}
	// Stop reason should be end_turn.
	if collected[5].StopReason != api.StopEndTurn {
		t.Errorf("stop_reason = %q, want end_turn", collected[5].StopReason)
	}
}

// TestClientToolCall drives a Client through an httptest
// server that returns a tool-call SSE stream. The test asserts
// the tool-use block start carries the right id+name and that
// the partial-JSON deltas reconstruct the tool input.
func TestClientToolCall(t *testing.T) {
	const body = `data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":""}]}

data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":""}}]},"finish_reason":""}]}

data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"echo hi\"}"}}]},"finish_reason":""}]}

data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "k", "meta/llama-3.3-70b-instruct")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "meta/llama-3.3-70b-instruct",
		Messages: []core.Message{core.NewUserText("run echo hi")},
		Tools: []core.ToolDefinition{
			{Name: "Bash", Description: "run a command", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
	})

	var collected []api.StreamEvent
	for ev := range events {
		collected = append(collected, ev)
	}
	if err := <-errs; err != nil {
		t.Errorf("errs channel emitted: %v", err)
	}

	// Find the tool-use block start.
	var toolStart *api.StreamEvent
	for i := range collected {
		if collected[i].Kind == api.EventContentBlockStart && collected[i].Block.Kind == core.BlockToolUse {
			toolStart = &collected[i]
			break
		}
	}
	if toolStart == nil {
		t.Fatalf("no tool-use block_start event found in %+v", collected)
	}
	if toolStart.Block.ToolUse.ID != "call_1" {
		t.Errorf("tool_use.id = %q, want call_1", toolStart.Block.ToolUse.ID)
	}
	if toolStart.Block.ToolUse.Name != "Bash" {
		t.Errorf("tool_use.name = %q, want Bash", toolStart.Block.ToolUse.Name)
	}

	// Find a tool-use input delta and verify it carries the partial JSON.
	var sawPartial bool
	for _, ev := range collected {
		if ev.Kind == api.EventContentBlockDelta && ev.Delta.Kind == api.DeltaToolInputJSON {
			if strings.Contains(ev.Delta.PartialJSON, "echo hi") {
				sawPartial = true
			}
		}
	}
	if !sawPartial {
		t.Errorf("no tool_use input_json_delta with 'echo hi' found")
	}

	// Stop reason should be tool_use.
	var msgDelta *api.StreamEvent
	for i := range collected {
		if collected[i].Kind == api.EventMessageDelta {
			msgDelta = &collected[i]
		}
	}
	if msgDelta == nil || msgDelta.StopReason != api.StopToolUse {
		t.Errorf("message_delta stop_reason = %v, want tool_use", msgDelta)
	}
}

// TestClientAuthError verifies a 401 from the provider
// surfaces as a core.Error with KindAuth on the errs channel.
func TestClientAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid api key"}`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "bad", "meta/llama-3.3-70b-instruct")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "meta/llama-3.3-70b-instruct",
		Messages: []core.Message{core.NewUserText("hi")},
	})

	// Drain events (should be empty / just the error event).
	for range events {
	}
	err := <-errs
	if err == nil {
		t.Fatalf("errs channel empty; want auth error")
	}
	ce, ok := err.(*core.Error)
	if !ok {
		t.Fatalf("err type = %T, want *core.Error", err)
	}
	if ce.Kind != core.KindAuth {
		t.Errorf("err kind = %v, want KindAuth", ce.Kind)
	}
}

// TestClientRateLimit verifies a 429 with a Retry-After
// surfaces as KindRateLimit with RetryAfter populated.
func TestClientRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "k", "meta/llama-3.3-70b-instruct")
	events, errs := c.Stream(context.Background(), api.Request{
		Model:    "meta/llama-3.3-70b-instruct",
		Messages: []core.Message{core.NewUserText("hi")},
	})
	for range events {
	}
	err := <-errs
	if err == nil {
		t.Fatalf("errs channel empty; want rate-limit error")
	}
	ce, ok := err.(*core.Error)
	if !ok {
		t.Fatalf("err type = %T, want *core.Error", err)
	}
	if ce.Kind != core.KindRateLimit {
		t.Errorf("err kind = %v, want KindRateLimit", ce.Kind)
	}
	if ce.RetryAfter != 30 {
		t.Errorf("RetryAfter = %d, want 30", ce.RetryAfter)
	}
	if !ce.IsRetryable() {
		t.Errorf("rate-limit error should be retryable")
	}
}

// TestClientCancellation verifies that cancelling the
// context aborts the in-flight request.
func TestClientCancellation(t *testing.T) {
	body := strings.Repeat("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Write slowly so cancellation has a chance to
		// interleave.
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 1000; i++ {
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(5 * time.Millisecond)
		}
		_ = body
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "k", "meta/llama-3.3-70b-instruct")
	ctx, cancel := context.WithCancel(context.Background())
	events, errs := c.Stream(ctx, api.Request{
		Model:    "meta/llama-3.3-70b-instruct",
		Messages: []core.Message{core.NewUserText("hi")},
	})
	// Cancel after a few events.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	for range events {
	}
	// We expect either a KindCancelled error (ctx cancel
	// surface) or a KindAPI "stream ended without [DONE]"
	// error. Both are acceptable — the contract is "don't
	// return success."
	if err := <-errs; err == nil {
		t.Errorf("expected an error after cancellation; got nil")
	}
}

// TestParseRetryAfter covers the two forms (numeric +
// non-numeric).
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"30", 30, false},
		{"  120  ", 120, false},
		{"0", 0, true},
		{"Wed, 21 Oct 2015 07:28:00 GMT", 0, true},
		{"", 0, true},
	}
	for _, c := range cases {
		got, err := parseRetryAfter(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseRetryAfter(%q) err = %v, wantErr = %v", c.in, err, c.wantErr)
			continue
		}
		if got != c.want {
			t.Errorf("parseRetryAfter(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestClientListModels drives ListModels against an httptest
// server returning a realistic OpenAI /v1/models payload. Asserts
// the Authorization header is set, the JSON is parsed, and the
// returned ModelInfo entries carry only ID + Provider — capability
// flags and ContextWindow are intentionally left zero (the
// MergeWithKnown overlay fills them).
func TestClientListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/models" {
			t.Errorf("path = %s, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want 'Bearer test-key'", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
  "object": "list",
  "data": [
    {"id": "gpt-4o", "object": "model", "created": 1, "owned_by": "openai"},
    {"id": "gpt-4o-mini", "object": "model", "created": 1, "owned_by": "openai"},
    {"id": "meta/llama-3.3-70b-instruct", "object": "model", "created": 1, "owned_by": "nvidia"}
  ]
}`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "test-key", "gpt-4o")
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("ListModels returned %d models, want 3; %+v", len(models), models)
	}
	wantIDs := []string{"gpt-4o", "gpt-4o-mini", "meta/llama-3.3-70b-instruct"}
	for i, m := range models {
		if m.ID != wantIDs[i] {
			t.Errorf("models[%d].ID = %q, want %q", i, m.ID, wantIDs[i])
		}
		if m.Provider != core.ProviderOpenAI {
			t.Errorf("models[%d].Provider = %q, want %q", i, m.Provider, core.ProviderOpenAI)
		}
		// Capability flags must be zero — overlay fills them.
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
		_, _ = io.WriteString(w, `{"error":{"message":"bad key","type":"auth_error"}}`)
	}))
	defer srv.Close()

	c := NewWithHTTP(srv.Client(), srv.URL, "bad-key", "gpt-4o")
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
