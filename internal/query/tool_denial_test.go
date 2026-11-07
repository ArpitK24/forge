package query

import (
	"context"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// --- Bug 5: denied / unknown tools are reported to the model ------

func lastToolResult(t *testing.T, msgs []core.Message) *core.ToolResult {
	t.Helper()
	if len(msgs) == 0 {
		t.Fatalf("no messages recorded")
	}
	last := msgs[len(msgs)-1]
	if last.Content.IsString {
		t.Fatalf("last message is plain text, want tool_result")
	}
	for _, b := range last.Content.Blocks {
		if b.Kind == core.BlockToolResult {
			return b.ToolResult
		}
	}
	t.Fatalf("last message has no tool_result block")
	return nil
}

func TestRunQueryLoopUnknownToolReportedToModel(t *testing.T) {
	fp := api.NewFakeProvider(api.ScriptToolCallThenText("NoSuchTool", []byte(`{}`), "recovered")...)
	toolsList, rec := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})

	out := RunQueryLoop(context.Background(), fp,
		[]core.Message{core.NewUserText("call a tool that does not exist")},
		toolsList, tc, Config{Model: "fake-model", MaxTurns: 5}, nil, newEventSink())

	if rec.calls.Load() != 0 {
		t.Errorf("registered tool ran: calls = %d", rec.calls.Load())
	}
	if out.Turns != 2 {
		t.Fatalf("Turns = %d, want 2 (model must see the unknown-tool error)", out.Turns)
	}
	if got := out.Message.GetFirstText(); got != "recovered" {
		t.Errorf("final message = %q, want 'recovered'", got)
	}
	calls := fp.AllMessages()
	tr := lastToolResult(t, calls[1])
	if !tr.IsError || !contains(string(tr.Content), "unknown tool") {
		t.Errorf("tool_result = %s (is_error=%v), want is_error unknown tool", tr.Content, tr.IsError)
	}
}
