package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// teaDriver stands in for the bubbletea runtime: every Cmd runs
// on its own goroutine and the Msg it returns is fed back
// through Update on the test goroutine, one at a time. That is
// the contract the real runtime gives Update, so a Model that
// loses its subscriptions here loses them in the real TUI too.
type teaDriver struct {
	t    *testing.T
	m    Model
	msgs chan tea.Msg
}

func newTeaDriver(t *testing.T, m Model) *teaDriver {
	t.Helper()
	return &teaDriver{t: t, m: m, msgs: make(chan tea.Msg, 256)}
}

func (d *teaDriver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() { d.msgs <- cmd() }()
}

func (d *teaDriver) send(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
		return
	case tea.BatchMsg:
		for _, c := range msg {
			d.run(c)
		}
		return
	}
	next, cmd := d.m.Update(msg)
	d.m = next.(Model)
	d.run(cmd)
}

// pump feeds messages into Update until done reports true or
// the timeout fires. react runs before every message so a test
// can answer a permission dialog the way a user would.
func (d *teaDriver) pump(timeout time.Duration, done func(Model) bool, react func(*teaDriver)) bool {
	d.t.Helper()
	deadline := time.After(timeout)
	for {
		if done(d.m) {
			return true
		}
		if react != nil {
			react(d)
		}
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-deadline:
			return false
		}
	}
}

func (d *teaDriver) submit(text string) {
	d.m.Input.SetValue(text)
	d.send(tea.KeyMsg{Type: tea.KeyEnter})
}

func newLoopModel(provider api.Provider, workDir string) Model {
	cfg := &core.Config{
		Model:          "fake-model",
		WorkingDir:     workDir,
		PermissionMode: core.PermissionDefault,
	}
	m := InitialModel(cfg, core.NewCostTracker())
	m.Width = 80
	m.Height = 24
	m.computeLayout()
	m.bridge = &programState{Provider: provider, SystemPrompt: "test"}
	return m
}

func turnDone(m Model) bool { return !m.Streaming && !m.LoopRunning }

// TestTUI_TurnFinishes is the regression test for the TUI never
// finishing a turn: startQueryLoop had a value receiver, so the
// per-turn event channel and cancel func were set on a copy and
// Update stopped draining after the first event.
func TestTUI_TurnFinishes(t *testing.T) {
	provider := api.NewFakeProvider(api.ScriptTextResponse("hello there"))
	d := newTeaDriver(t, newLoopModel(provider, t.TempDir()))

	d.submit("hi")
	if !d.m.Streaming {
		t.Fatal("submit should start streaming")
	}
	if !d.pump(5*time.Second, turnDone, nil) {
		t.Fatalf("turn never finished: Streaming=%v LoopRunning=%v Status=%q currentEventCh nil=%v",
			d.m.Streaming, d.m.LoopRunning, d.m.Status, d.m.currentEventCh == nil)
	}
	if d.m.Status != "Ready" {
		t.Errorf("Status = %q, want Ready", d.m.Status)
	}
	if got := len(d.m.CmdCtx.Messages); got != 2 {
		t.Fatalf("history len = %d, want 2 (user + assistant)", got)
	}
	if got := d.m.CmdCtx.Messages[1].GetFirstText(); got != "hello there" {
		t.Errorf("assistant text in history = %q, want %q", got, "hello there")
	}
	last := d.m.Messages[len(d.m.Messages)-1]
	if last.Role != "assistant" || last.Text != "hello there" {
		t.Errorf("last rendered message = %+v, want assistant %q", last, "hello there")
	}
}

// blockingProvider streams nothing until its context is
// cancelled, so a test can press Esc mid-turn.
type blockingProvider struct{ started chan struct{} }

func (p *blockingProvider) Info() api.ModelInfo {
	return api.ModelInfo{ID: "blocking", ContextWindow: 128_000, SupportsToolUse: true}
}

func (p *blockingProvider) Stream(ctx context.Context, _ api.Request) (<-chan api.StreamEvent, <-chan error) {
	events := make(chan api.StreamEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(events)
		defer close(errs)
		select {
		case p.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
	}()
	return events, errs
}

// TestTUI_EscCancelsTurn: Esc while streaming must cancel the
// loop. Before the fix LoopCancel was set on a copy of the
// Model, so Esc was a no-op and the turn never ended.
func TestTUI_EscCancelsTurn(t *testing.T) {
	provider := &blockingProvider{started: make(chan struct{}, 1)}
	d := newTeaDriver(t, newLoopModel(provider, t.TempDir()))

	d.submit("hi")
	select {
	case <-provider.started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider was never called")
	}
	d.send(tea.KeyMsg{Type: tea.KeyEsc})
	if !d.pump(5*time.Second, turnDone, nil) {
		t.Fatalf("Esc did not cancel the turn: Streaming=%v Status=%q LoopCancel nil=%v",
			d.m.Streaming, d.m.Status, d.m.LoopCancel == nil)
	}
	if d.m.Status != "Cancelled" {
		t.Errorf("Status = %q, want Cancelled", d.m.Status)
	}
}
