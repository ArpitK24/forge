package query

import (
	"context"
	"sync"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// --- Bugs 2 + 3: compaction state and summary model ---------------

// compactProbeProvider answers main-loop requests with a tool call
// that reports a huge prompt (so auto-compaction always triggers)
// and fails every summary request. It records the model each
// summary request asked for.
type compactProbeProvider struct {
	mu            sync.Mutex
	summaryModels []string
	mainCalls     int
}

func (p *compactProbeProvider) Info() api.ModelInfo {
	return api.ModelInfo{ID: "probe-model", ContextWindow: 128_000}
}

func (p *compactProbeProvider) Stream(_ context.Context, req api.Request) (<-chan api.StreamEvent, <-chan error) {
	events := make(chan api.StreamEvent, 8)
	errs := make(chan error, 1)
	p.mu.Lock()
	defer p.mu.Unlock()
	if req.System.String == CompactSummaryPrompt {
		p.summaryModels = append(p.summaryModels, req.Model)
		err := core.New(core.KindAPI, "summary model unavailable")
		events <- api.EventOfError(err)
		close(events)
		errs <- err
		close(errs)
		return events, errs
	}
	p.mainCalls++
	events <- api.EventOfMessageStart(req.Model, &core.UsageInfo{InputTokens: 10_000_000})
	events <- api.EventOfBlockStart(0, core.ContentBlock{
		Kind:    core.BlockToolUse,
		ToolUse: &core.ToolUse{ID: "t", Name: "Bash", Input: []byte(`{"command":"true"}`)},
	})
	events <- api.EventOfMessageDelta(api.StopToolUse, nil)
	events <- api.EventOfMessageStop()
	close(events)
	close(errs)
	return events, errs
}

func (p *compactProbeProvider) summaries() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.summaryModels...)
}

// longHistory returns a plain-text history longer than the
// compaction tail so the summarizer is actually called.
func longHistory(n int) []core.Message {
	msgs := make([]core.Message, 0, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			msgs = append(msgs, core.NewUserText("u"))
		} else {
			msgs = append(msgs, core.NewAssistantText("a"))
		}
	}
	return msgs
}

func TestRunQueryLoopCompactionCircuitBreakerTrips(t *testing.T) {
	p := &compactProbeProvider{}
	toolsList, _ := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{AutoCompact: true})

	const turns = 9
	_ = RunQueryLoop(context.Background(), p, longHistory(21), toolsList, tc,
		Config{Model: "probe-model", MaxTurns: turns}, nil, newEventSink())

	if p.mainCalls != turns {
		t.Fatalf("main calls = %d, want %d", p.mainCalls, turns)
	}
	if got := len(p.summaries()); got != core.AutoCompactMaxConsecutiveFailures {
		t.Errorf("summary attempts = %d over %d turns, want %d (breaker should trip)",
			got, turns, core.AutoCompactMaxConsecutiveFailures)
	}
}

func TestRunQueryLoopCompactStatePersistsAcrossCalls(t *testing.T) {
	// The TUI calls RunQueryLoop once per user message; a
	// caller-owned CompactState must carry the breaker across.
	p := &compactProbeProvider{}
	toolsList, _ := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{AutoCompact: true})
	state := &CompactState{}

	for i := 0; i < 5; i++ {
		_ = RunQueryLoop(context.Background(), p, longHistory(21), toolsList, tc,
			Config{Model: "probe-model", MaxTurns: 1, CompactState: state}, nil, newEventSink())
	}
	if got := len(p.summaries()); got != core.AutoCompactMaxConsecutiveFailures {
		t.Errorf("summary attempts = %d over 5 calls, want %d", got, core.AutoCompactMaxConsecutiveFailures)
	}
	if !state.Disabled {
		t.Errorf("state.Disabled = false, want true")
	}
}
