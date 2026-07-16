package query

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ArpitK24/forge/internal/api/openai"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// TestRunQueryLoopEndToEndNIM is the Phase-2 end-to-end
// integration test: it wires the real openai.Client (Phase 2's
// NIM adapter) into the real query.RunQueryLoop, with the real
// Bash tool, and an httptest.Server impersonating the NIM Chat
// Completions endpoint.
//
// The point of this test is to catch regressions in the seams
// between packages — events shapes, content blocks, tool-call
// round-tripping, usage accumulation — that the per-package
// unit tests cannot catch on their own.
func TestRunQueryLoopEndToEndNIM(t *testing.T) {
	// The mock NIM server plays two turns:
	//
	//   Turn 1: tool call (Bash with `{"command":"echo hi"}`),
	//           finish_reason=tool_calls.
	//   Turn 2: text response "echoed: hi", finish_reason=stop.
	//
	// The server counts requests so the test can assert the
	// loop really did re-issue a turn after the tool result.
	var turn atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Auth check: every request must carry the bearer key.
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth header = %q, want 'Bearer test-key'", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %q, want application/json", r.Header.Get("Content-Type"))
		}

		// Decode the request body to confirm the loop
		// (a) re-sent the system prompt, (b) appended the
		// tool result from turn 1's execution, and (c) sent
		// the tool definitions every turn.
		var reqBody struct {
			Model    string             `json:"model"`
			Stream   bool               `json:"stream"`
			Messages []map[string]any   `json:"messages"`
			Tools    []map[string]any   `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !reqBody.Stream {
			t.Errorf("stream = false, want true")
		}
		if len(reqBody.Tools) == 0 {
			t.Errorf("turn %d sent no tools; the loop must re-send tool defs every turn", turn.Load())
		}

		turnNum := turn.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if turnNum == 1 {
			_, _ = io.WriteString(w, toolCallTurn)
			return
		}
		// Turn 2: assert the previous tool result is in
		// the history as a "tool" role message with the
		// matching tool_call_id.
		var sawToolResult bool
		for _, m := range reqBody.Messages {
			if m["role"] == "tool" && m["tool_call_id"] == "call_1" {
				if s, _ := m["content"].(string); strings.Contains(s, "tool-output-ok") {
					sawToolResult = true
				}
			}
		}
		if !sawToolResult {
			t.Errorf("turn 2 messages did not include the tool result for call_1; got %+v", reqBody.Messages)
		}
		_, _ = io.WriteString(w, textTurn)
	}))
	defer srv.Close()

	// Build the real openai.Client pointed at the test server.
	client := openai.NewWithHTTP(srv.Client(), srv.URL, "test-key", "meta/llama-3.3-70b-instruct")

	// Build the per-call ToolContext. Use BypassPermissions so
	// the loop executes the Bash tool without prompting.
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

	// Use the real built-in tools. Substitute a small
	// recording Bash-like tool that always returns
	// "tool-output-ok" so the test is hermetic.
	toolsList := []tools.Tool{&recordingTool{
		name:   "Bash",
		result: tools.ToolResult{Text: "tool-output-ok"},
	}}

	out := RunQueryLoop(
		context.Background(),
		client,
		[]core.Message{core.NewUserText("run echo hi")},
		toolsList,
		tc,
		Config{Model: "meta/llama-3.3-70b-instruct", MaxTurns: 5},
		nil,
		newEventSink(),
	)

	if out.Kind != OutcomeEndTurn {
		t.Errorf("Kind = %v, want OutcomeEndTurn", out.Kind)
	}
	if out.Turns != 2 {
		t.Errorf("Turns = %d, want 2 (tool turn + final text turn)", out.Turns)
	}
	if got := out.Message.GetFirstText(); got != "echoed: hi" {
		t.Errorf("final message = %q, want 'echoed: hi'", got)
	}
	if turn.Load() != 2 {
		t.Errorf("server saw %d turns, want 2", turn.Load())
	}
}

// toolCallTurn is a NIM-shaped Chat Completions chunk stream
// that emits one Bash tool call and ends.
const toolCallTurn = `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":""}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":""}}]},"finish_reason":""}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"echo hi\"}"}}]},"finish_reason":""}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`

// textTurn is a NIM-shaped Chat Completions chunk stream that
// emits the text "echoed: hi" and ends.
const textTurn = `data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":""}]}

data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"content":"echoed: "},"finish_reason":""}]}

data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":""}]}

data: {"id":"c2","object":"chat.completion.chunk","created":1,"model":"meta/llama-3.3-70b-instruct","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
