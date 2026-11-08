package query

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ArpitK24/forge/internal/api/openai"
	"github.com/ArpitK24/forge/internal/core"
)

// --- Bug 1: OpenAI-format usage reaches the loop ------------------

// TestRunQueryLoopOpenAIInputTokensCounted drives the loop through
// the real OpenAI adapter against an httptest server. The server
// only reports usage when the request asks for it via
// stream_options.include_usage (as api.openai.com does), and then
// sends it in a final choices-less chunk. The loop must see the
// prompt tokens: they are what auto-compaction keys off.
func TestRunQueryLoopOpenAIInputTokensCounted(t *testing.T) {
	const textChunks = `data: {"id":"c1","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":""}]}

data: {"id":"c1","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

`
	const usageChunk = `data: {"id":"c1","model":"gpt-test","choices":[],"usage":{"prompt_tokens":4242,"completion_tokens":7,"total_tokens":4249}}

`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, textChunks)
		if body.StreamOptions != nil && body.StreamOptions.IncludeUsage {
			_, _ = io.WriteString(w, usageChunk)
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	client := openai.NewWithHTTP(srv.Client(), srv.URL, "k", "gpt-test")
	toolsList, _ := testToolsList(t)
	out := RunQueryLoop(context.Background(), client,
		[]core.Message{core.NewUserText("hi")}, toolsList,
		testTC(t, core.PermissionBypassPermissions, &core.Config{}),
		Config{Model: "gpt-test", MaxTurns: 2}, nil, newEventSink())

	if out.Kind != OutcomeEndTurn {
		t.Fatalf("Kind = %v, want end_turn (err=%v)", out.Kind, out.Err)
	}
	if out.Usage.InputTokens != 4242 {
		t.Errorf("Usage.InputTokens = %d, want 4242", out.Usage.InputTokens)
	}
	if out.Usage.OutputTokens != 7 {
		t.Errorf("Usage.OutputTokens = %d, want 7", out.Usage.OutputTokens)
	}
}
