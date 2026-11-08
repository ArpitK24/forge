package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// requestRecorder wraps FakeProvider and keeps every full request.
type requestRecorder struct {
	*api.FakeProvider
	mu   sync.Mutex
	reqs []api.Request
}

func (r *requestRecorder) Stream(ctx context.Context, req api.Request) (<-chan api.StreamEvent, <-chan error) {
	r.mu.Lock()
	cp := req
	cp.Messages = append([]core.Message(nil), req.Messages...)
	r.reqs = append(r.reqs, cp)
	r.mu.Unlock()
	return r.FakeProvider.Stream(ctx, req)
}

type fakeContext struct {
	block    string
	err      error
	panics   bool
	assemble int
	seen     [][]core.Message
	observed []string
}

func (f *fakeContext) Assemble(_ context.Context, msgs []core.Message) (string, error) {
	f.assemble++
	f.seen = append(f.seen, msgs)
	if f.panics {
		panic("boom")
	}
	return f.block, f.err
}

func (f *fakeContext) Observe(_ context.Context, tu core.ToolUse, result string, isError bool) {
	f.observed = append(f.observed, tu.Name+":"+string(tu.Input)+"->"+result)
}

func runWithContext(t *testing.T, cp ContextProvider) []api.Request {
	t.Helper()
	rec := &requestRecorder{FakeProvider: api.NewFakeProvider(
		api.ScriptToolCallThenText("Bash", []byte(`{"command":"go test ./..."}`), "done")...)}
	toolsList, _ := testToolsList(t)
	tc := testTC(t, core.PermissionBypassPermissions, &core.Config{})
	out := RunQueryLoop(context.Background(), rec,
		[]core.Message{core.NewUserText("fix the failing test")},
		toolsList, tc, Config{
			Model: "fake-model", MaxTurns: 5,
			SystemPrompt: "BASE", AppendSystemPrompt: "+APPEND",
			Context: cp,
		}, nil, newEventSink())
	if out.Kind != OutcomeEndTurn || out.Turns != 2 {
		t.Fatalf("outcome = %v turns %d", out.Kind, out.Turns)
	}
	return rec.reqs
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestNilContextProviderIsByteIdentical pins G2: with no engine, every
// request is exactly what the loop sent before the hook existed.
func TestNilContextProviderIsByteIdentical(t *testing.T) {
	reqs := runWithContext(t, nil)
	if len(reqs) != 2 {
		t.Fatalf("requests = %d", len(reqs))
	}
	for i, r := range reqs {
		if !reflect.DeepEqual(r.System, api.SystemString("BASE+APPEND")) {
			t.Fatalf("request %d system = %+v", i, r.System)
		}
	}
	// A provider that adds nothing must not change a byte either.
	empty := runWithContext(t, &fakeContext{})
	if a, b := mustJSON(t, reqs), mustJSON(t, empty); a != b {
		t.Fatalf("empty provider changed the requests:\n%s\n%s", a, b)
	}
	// And a failing or panicking provider degrades to no context.
	for _, fc := range []*fakeContext{{err: errors.New("index locked")}, {panics: true}} {
		if a, b := mustJSON(t, reqs), mustJSON(t, runWithContext(t, fc)); a != b {
			t.Fatalf("failing provider changed the requests")
		}
	}
}

func TestContextProviderAssemblesEveryTurnAndObservesTools(t *testing.T) {
	fc := &fakeContext{block: "<forge-context>pack</forge-context>"}
	reqs := runWithContext(t, fc)
	if fc.assemble != 2 {
		t.Fatalf("Assemble calls = %d, want one per turn", fc.assemble)
	}
	for i, r := range reqs {
		if want := "BASE+APPEND\n\n<forge-context>pack</forge-context>"; r.System.String != want {
			t.Fatalf("request %d system = %q", i, r.System.String)
		}
	}
	// Turn 2 sees the tool_use and tool_result of turn 1.
	if n := len(fc.seen[1]); n != 3 {
		t.Fatalf("turn-2 messages = %d, want 3", n)
	}
	if !reflect.DeepEqual(fc.observed, []string{`Bash:{"command":"go test ./..."}->tool-output-ok`}) {
		t.Fatalf("observed = %v", fc.observed)
	}
	// The pack is never persisted into the history.
	for _, m := range reqs[1].Messages {
		if strings.Contains(m.AllText(), "forge-context") {
			t.Fatal("context leaked into messages")
		}
	}
}
