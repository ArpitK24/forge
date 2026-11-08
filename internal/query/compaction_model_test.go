package query

import (
	"context"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// --- Bug 3: summary model follows the session ---

func TestRunQueryLoopSummaryModelDefaultsToSessionModel(t *testing.T) {
	p := &compactProbeProvider{}
	toolsList, _ := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{AutoCompact: true})

	_ = RunQueryLoop(context.Background(), p, longHistory(21), toolsList, tc,
		Config{Model: "probe-model", MaxTurns: 1}, nil, newEventSink())

	got := p.summaries()
	if len(got) != 1 {
		t.Fatalf("summary attempts = %d, want 1", len(got))
	}
	if got[0] != "probe-model" {
		t.Errorf("summary model = %q, want the session model %q", got[0], "probe-model")
	}
}

func TestRunQueryLoopSummaryModelOverride(t *testing.T) {
	p := &compactProbeProvider{}
	toolsList, _ := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{AutoCompact: true})

	_ = RunQueryLoop(context.Background(), p, longHistory(21), toolsList, tc,
		Config{Model: "probe-model", SummaryModel: "cheap-model", MaxTurns: 1}, nil, newEventSink())

	got := p.summaries()
	if len(got) != 1 || got[0] != "cheap-model" {
		t.Errorf("summary models = %v, want [cheap-model]", got)
	}
}
