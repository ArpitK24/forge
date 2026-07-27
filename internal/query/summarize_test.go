package query

import (
	"context"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

func TestSummarizeHeadEmptyHeadIsNoOp(t *testing.T) {
	// Empty head: no API call, no error, empty string.
	out, err := SummarizeHead(context.Background(),
		api.NewFakeProvider(), // no script
		"fake-summarizer", nil)
	if err != nil {
		t.Errorf("SummarizeHead err = %v, want nil on empty head", err)
	}
	if out != "" {
		t.Errorf("SummarizeHead out = %q, want \"\" on empty head", out)
	}
}

func TestSummarizeHeadReturnsSummaryText(t *testing.T) {
	const want = "Earlier messages: foo, bar, baz."
	head := []core.Message{
		core.NewUserText("foo"),
		core.NewAssistantText("bar"),
		core.NewUserText("baz"),
	}
	got, err := SummarizeHead(context.Background(),
		fakeSummaryScript(want),
		"fake-summarizer", head)
	if err != nil {
		t.Fatalf("SummarizeHead err = %v, want nil", err)
	}
	if got != want {
		t.Errorf("SummarizeHead = %q, want %q", got, want)
	}
}

func TestSummarizeHeadPassesNoTools(t *testing.T) {
	// A capture-style fake: verify the request the summarizer
	// sent had no tools attached. We use a custom Provider
	// implementation that records the Request and replies with
	// a canned text response.
	rec := &recordingProvider{
		text: "summary",
	}
	head := []core.Message{core.NewUserText("hi")}
	if _, err := SummarizeHead(context.Background(), rec,
		"fake-summarizer", head); err != nil {
		t.Fatalf("SummarizeHead err = %v, want nil", err)
	}
	if len(rec.lastReq.Tools) != 0 {
		t.Errorf("SummarizeHead request had %d tools, want 0 (no tools allowed)",
			len(rec.lastReq.Tools))
	}
	if rec.lastReq.Model != "fake-summarizer" {
		t.Errorf("SummarizeHead request model = %q, want %q",
			rec.lastReq.Model, "fake-summarizer")
	}
	if !rec.lastReq.System.IsString {
		t.Errorf("SummarizeHead request system is not a string")
	}
	if !strings.Contains(rec.lastReq.System.String, "context-compaction") {
		t.Errorf("SummarizeHead system prompt = %q, want contains 'context-compaction'",
			rec.lastReq.System.String)
	}
}

func TestSummarizeHeadProviderErrorPropagates(t *testing.T) {
	// A provider whose Stream emits an error event. SummarizeHead
	// should surface the error rather than returning an empty
	// string.
	p := api.NewFakeProvider(nil) // nil script → fake "no scripted responses"
	head := []core.Message{core.NewUserText("hi")}
	_, err := SummarizeHead(context.Background(), p, "fake-summarizer", head)
	if err == nil {
		t.Errorf("SummarizeHead err = nil, want non-nil on provider error")
	}
}

// recordingProvider is a tiny Provider impl used by the
// "PassesNoTools" test. It records the last Request and emits
// a canned text response when Stream is called.
type recordingProvider struct {
	text    string
	lastReq api.Request
}

func (r *recordingProvider) Info() api.ModelInfo {
	return api.ModelInfo{ID: "rec", ContextWindow: 128_000, SupportsToolUse: true}
}

func (r *recordingProvider) Stream(_ context.Context, req api.Request) (<-chan api.StreamEvent, <-chan error) {
	r.lastReq = req
	events := make(chan api.StreamEvent, 8)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		events <- api.EventOfMessageStart("rec", nil)
		events <- api.EventOfBlockStart(0, core.TextBlock(""))
		events <- api.EventOfBlockDelta(0, api.TextDelta(r.text))
		events <- api.EventOfBlockStop(0)
		events <- api.EventOfMessageDelta(api.StopEndTurn, nil)
		events <- api.EventOfMessageStop()
	}()
	return events, errs
}
