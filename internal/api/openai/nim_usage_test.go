package openai

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// collectSSE runs parseSSE over body and returns every event
// and the first error.
func collectSSE(t *testing.T, body string) ([]api.StreamEvent, error) {
	t.Helper()
	events := make(chan api.StreamEvent, 256)
	errs := make(chan error, 2)
	parseSSE(context.Background(), strings.NewReader(body), events, errs, "m")
	close(events)
	close(errs)
	var out []api.StreamEvent
	for ev := range events {
		out = append(out, ev)
	}
	return out, <-errs
}

// finalUsage returns the usage carried by the stream's
// MessageDelta and checks the terminal event order.
func finalUsage(t *testing.T, evs []api.StreamEvent) (string, *core.UsageInfo) {
	t.Helper()
	n := len(evs)
	if n < 2 || evs[n-1].Kind != api.EventMessageStop || evs[n-2].Kind != api.EventMessageDelta {
		kinds := make([]api.EventKind, 0, n)
		for _, e := range evs {
			kinds = append(kinds, e.Kind)
		}
		t.Fatalf("stream must end MessageDelta, MessageStop; got kinds %v", kinds)
	}
	for _, e := range evs[:n-2] {
		if e.Kind == api.EventMessageDelta {
			t.Fatalf("MessageDelta emitted before the end of the stream (usage would be lost)")
		}
	}
	return evs[n-2].StopReason, evs[n-2].Usage
}

// TestParseSSENIMTrailingUsageChunk replays a stream recorded
// from integrate.api.nvidia.com (nvidia/nemotron-3.5-lightning-30b-a3b,
// stream_options.include_usage=true, one tool call). NIM sends
// "usage":null on every content chunk, puts finish_reason on its
// own chunk, and only then sends usage in a chunk whose choices
// array is empty, followed by [DONE]. The MessageDelta must carry
// that trailing usage.
func TestParseSSENIMTrailingUsageChunk(t *testing.T) {
	raw, err := os.ReadFile("testdata/nim_lightning_tool_call.sse")
	if err != nil {
		t.Fatal(err)
	}
	evs, perr := collectSSE(t, string(raw))
	if perr != nil {
		t.Fatalf("parseSSE error: %v", perr)
	}
	stop, u := finalUsage(t, evs)
	if stop != api.StopToolUse {
		t.Errorf("StopReason = %q, want %q", stop, api.StopToolUse)
	}
	if u == nil {
		t.Fatal("MessageDelta.Usage = nil, want NIM's trailing usage")
	}
	if u.InputTokens != 275 || u.OutputTokens != 41 {
		t.Errorf("usage = %d in / %d out, want 275 in / 41 out", u.InputTokens, u.OutputTokens)
	}
	var sawTool bool
	for _, e := range evs {
		if e.Kind == api.EventContentBlockStart && e.Block.ToolUse != nil && e.Block.ToolUse.Name == "Read" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Error("tool_use block for Read not emitted")
	}
}

// TestParseSSEUsageOrderings covers the shapes OpenAI-compatible
// servers use for the final usage, in a text-only answer.
func TestParseSSEUsageOrderings(t *testing.T) {
	const (
		text   = `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"DONE"},"finish_reason":null}],"usage":null}` + "\n\n"
		finish = `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":null}` + "\n\n"
		usage  = `data: {"id":"c","model":"m","choices":[],"usage":{"prompt_tokens":2290,"completion_tokens":29,"total_tokens":2319}}` + "\n\n"
		inline = `data: {"id":"c","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2290,"completion_tokens":29,"total_tokens":2319}}` + "\n\n"
		done   = "data: [DONE]\n\n"
	)
	cases := map[string]string{
		"finish-then-usage-chunk (NIM, OpenAI)": text + finish + usage + done,
		"usage-on-finish-chunk":                 text + inline + done,
		"usage-chunk-before-finish":             text + usage + finish + done,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			evs, err := collectSSE(t, body)
			if err != nil {
				t.Fatalf("parseSSE error: %v", err)
			}
			stop, u := finalUsage(t, evs)
			if stop != api.StopEndTurn {
				t.Errorf("StopReason = %q, want end_turn", stop)
			}
			if u == nil || u.InputTokens != 2290 || u.OutputTokens != 29 {
				t.Errorf("usage = %+v, want 2290 in / 29 out", u)
			}
		})
	}
}
