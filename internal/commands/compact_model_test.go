package commands

import (
	"context"
	"sync"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// modelRecorder answers every request with a short text reply and
// records the model each request asked for.
type modelRecorder struct {
	mu     sync.Mutex
	models []string
}

func (m *modelRecorder) Info() api.ModelInfo { return api.ModelInfo{ID: "rec", ContextWindow: 128_000} }
func (m *modelRecorder) Stream(_ context.Context, req api.Request) (<-chan api.StreamEvent, <-chan error) {
	m.mu.Lock()
	m.models = append(m.models, req.Model)
	m.mu.Unlock()
	events := make(chan api.StreamEvent, 8)
	errs := make(chan error)
	for _, ev := range api.ScriptTextResponse("summary") {
		events <- ev
	}
	close(events)
	close(errs)
	return events, errs
}

// /compact must summarize with the session's model, not a
// hard-coded NIM model that does not exist on other providers.
func TestCompactUsesSessionModel(t *testing.T) {
	rec := &modelRecorder{}
	msgs := make([]core.Message, 0, 15)
	for i := 0; i < 15; i++ {
		msgs = append(msgs, core.NewUserText("m"))
	}
	ctx := testCtx(func(c *CommandContext) {
		c.Config.Model = "gpt-4o"
		c.Client = rec
		c.Messages = msgs
	})
	res := ExecuteCommand(context.Background(), "/compact", ctx)
	if res == nil {
		t.Fatal("/compact returned nil")
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.models) != 1 || rec.models[0] != "gpt-4o" {
		t.Errorf("summary request models = %v, want [gpt-4o]", rec.models)
	}
}
