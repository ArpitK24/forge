package eval

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/ArpitK24/forge/internal/ctxengine/retrieve"
)

func TestScore(t *testing.T) {
	task := Task{ID: "t", GoldFiles: []string{"a", "b"}, GoldFunctions: []string{"a::F"}}
	res := retrieve.Result{
		Files:   []string{"x", "a", "y", "z", "w", "b"},
		Symbols: []string{"x::G", "a::F"},
		Pack:    retrieve.Pack{Files: []string{"x", "b"}, Tokens: 42},
	}
	m := Score(task, res).Metrics
	want := map[string]float64{
		MFileR1: 0, MFileR3: 0.5, MFileR5: 0.5, MFileR10: 1,
		MFuncR5: 1, MFuncR10: 1, MMRR: 0.5, MGoldInPack: 0.5, MPackTokens: 42,
	}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("metrics = %v\nwant      %v", m, want)
	}
	// No gold functions: function recall is undefined, not zero.
	task.GoldFunctions = nil
	if _, ok := Score(task, res).Metrics[MFuncR5]; ok {
		t.Fatal("func recall defined without gold functions")
	}
	// Duplicates in a ranking never count twice.
	if r := recallAt([]string{"a", "a"}, []string{"a", "b"}, 2); r != 0.5 {
		t.Fatalf("recall with duplicates = %v", r)
	}
}

func TestBootstrap(t *testing.T) {
	vals := []float64{0, 1, 1, 0, 1, math.NaN(), 1, 1}
	b := NewBootstrap(len(vals), 1000, 7, 0.95)
	st := b.Mean(vals)
	if st.N != 7 || math.Abs(st.Mean-round6(5.0/7)) > 1e-12 {
		t.Fatalf("stat = %+v", st)
	}
	if !(st.Lo <= st.Mean && st.Mean <= st.Hi) || st.Lo < 0 || st.Hi > 1 {
		t.Fatalf("CI = %+v", st)
	}
	if again := NewBootstrap(len(vals), 1000, 7, 0.95).Mean(vals); again != st {
		t.Fatalf("same seed differs: %+v vs %+v", again, st)
	}
	// A constant difference has a degenerate interval.
	a := []float64{0.1, 0.2, 0.3}
	c := []float64{0.6, 0.7, 0.8}
	mean, lo, hi, n := NewBootstrap(3, 200, 1, 0.95).Diff(a, c)
	if mean != 0.5 || lo != 0.5 || hi != 0.5 || n != 3 {
		t.Fatalf("diff = %v [%v, %v] n=%d", mean, lo, hi, n)
	}
	if st := NewBootstrap(0, 10, 1, 0.95).Mean(nil); st != (Stat{}) {
		t.Fatalf("empty = %+v", st)
	}
}

// fakeSource serves in-memory snapshots keyed by repo@commit.
type fakeSource map[string]retrieve.Snapshot

func (f fakeSource) Snapshot(_ context.Context, repo, commit string) (retrieve.Snapshot, error) {
	s, ok := f[repo+"@"+commit]
	if !ok {
		return nil, fmt.Errorf("no snapshot %s@%s", repo, commit)
	}
	return s, nil
}

func syntheticTasks() ([]Task, fakeSource) {
	snap := retrieve.MapSnapshot{"internal/x/x.go": goBase, "pkg/mod.py": pyBase, "README.md": "Greeter docs\n"}
	src := fakeSource{"o/r@base": snap}
	var tasks []Task
	for i, ps := range []string{
		"Greeter Hello prints the wrong punctuation",
		"Config.load should read files lazily",
		"Bye is too short",
		"something unrelated entirely",
	} {
		tasks = append(tasks, Task{ID: fmt.Sprintf("t%d", i), Repo: "o/r", BaseCommit: "base", ProblemStatement: ps, GoldPatch: fixturePatch})
	}
	tasks = append(tasks, Task{ID: "missing", Repo: "o/r", BaseCommit: "nope", GoldPatch: fixturePatch})
	return tasks, src
}

func TestRunDeterministic(t *testing.T) {
	tasks, src := syntheticTasks()
	cfg := RunConfig{Dataset: "synthetic", Baselines: []string{"b0", "b1-1k", "b1-4k", "b2"}, Budget: 300, Resamples: 500, Seed: 3, Jobs: 4}
	var outs [][]byte
	for i := 0; i < 3; i++ {
		c := cfg
		if i == 2 {
			c.Jobs = 1 // scheduling must not matter
		}
		// Shuffle input order too.
		in := append([]Task(nil), tasks...)
		if i == 1 {
			for l, r := 0, len(in)-1; l < r; l, r = l+1, r-1 {
				in[l], in[r] = in[r], in[l]
			}
		}
		rep, err := Run(context.Background(), in, src, c)
		if err != nil {
			t.Fatal(err)
		}
		b, err := MarshalReport(rep)
		if err != nil {
			t.Fatal(err)
		}
		outs = append(outs, b)
	}
	if !bytes.Equal(outs[0], outs[1]) || !bytes.Equal(outs[0], outs[2]) {
		t.Fatal("reports differ between runs")
	}
}

func TestRunReport(t *testing.T) {
	tasks, src := syntheticTasks()
	rep, err := Run(context.Background(), tasks, src, RunConfig{Dataset: "s", Baselines: []string{"b0", "b2"}, Budget: 4000, Resamples: 100, Seed: 1, Jobs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Tasks != 5 || rep.Scored != 4 || len(rep.Skipped) != 1 || rep.Skipped[0].ID != "missing" {
		t.Fatalf("tasks=%d scored=%d skipped=%v", rep.Tasks, rep.Scored, rep.Skipped)
	}
	b0, b2 := rep.Baselines[0], rep.Baselines[1]
	for _, m := range MetricNames {
		if b0.Aggregate[m].Mean != 0 {
			t.Errorf("b0 %s = %v, want 0", m, b0.Aggregate[m].Mean)
		}
	}
	if b2.Tasks[0].ID != "t0" || b2.Tasks[0].TopFiles[0] != "internal/x/x.go" {
		t.Errorf("b2 t0 = %+v", b2.Tasks[0])
	}
	if b2.Aggregate[MFuncR10].N != 4 {
		t.Errorf("func recall n = %d", b2.Aggregate[MFuncR10].N)
	}
	if len(rep.Paired) != len(MetricNames) || rep.Paired[0].A != "b0" || rep.Paired[0].B != "b2" {
		t.Errorf("paired = %+v", rep.Paired)
	}
	if _, err := Run(context.Background(), tasks, src, RunConfig{Baselines: []string{"b2"}}); err == nil {
		t.Error("zero budget accepted")
	}
	if _, err := Run(context.Background(), tasks, src, RunConfig{Baselines: []string{"bx"}, Budget: 1}); err == nil {
		t.Error("unknown baseline accepted")
	}
}

func TestResolveGoldKeepsStoredGold(t *testing.T) {
	tk := Task{GoldFiles: []string{"a.go"}, GoldFunctions: []string{}}
	if err := ResolveGold(&tk, retrieve.MapSnapshot{}, GoldOptions{}); err != nil || len(tk.GoldFiles) != 1 || tk.GoldFunctions == nil {
		t.Fatalf("stored gold changed: %+v %v", tk, err)
	}
}
