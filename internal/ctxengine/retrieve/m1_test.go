package retrieve

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/ctxengine/tokens"
)

func TestM1RanksSymbolsAndPacksBodies(t *testing.T) {
	c := corpus(t)
	res, err := NewM1().Retrieve(context.Background(), c,
		Query{ProblemStatement: "AutoCompactIfNeeded never compacts the conversation"}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 || res.Files[0] != "internal/query/compaction.go" {
		t.Fatalf("Files = %v, want compaction.go first", res.Files)
	}
	if res.Symbols[0] != "internal/query/compaction.go::AutoCompactIfNeeded" {
		t.Fatalf("Symbols = %v", res.Symbols)
	}
	p := res.Pack
	if !strings.Contains(p.Text, "=== internal/query/compaction.go ===\n") ||
		!strings.Contains(p.Text, "-- AutoCompactIfNeeded L") ||
		p.Tokens != tokens.Estimate(p.Text) || p.Files[0] != "internal/query/compaction.go" {
		t.Fatalf("pack = %q (%d tokens, files %v)", p.Text, p.Tokens, p.Files)
	}
	if strings.Contains(p.Text, "README") {
		t.Fatalf("unrelated file packed: %q", p.Text)
	}
}

func TestM1RespectsBudget(t *testing.T) {
	c := corpus(t)
	q := Query{ProblemStatement: "RunQueryLoop compaction conversation provider program"}
	for _, budget := range []int{1, 20, 60, 200, 8192} {
		res, err := NewM1().Retrieve(context.Background(), c, q, budget)
		if err != nil {
			t.Fatal(err)
		}
		if res.Pack.Tokens > budget {
			t.Errorf("budget %d: pack has %d tokens", budget, res.Pack.Tokens)
		}
		if budget == 1 && res.Pack.Text != "" {
			t.Errorf("budget 1 packed %q", res.Pack.Text)
		}
	}
}

func TestM1SignatureOnlyFilesAreNotGoldInPack(t *testing.T) {
	c := corpus(t)
	m := NewM1()
	m.Opts.PackMaxChunkFrac = 0 // every body is "too large": signatures only
	res, err := m.Retrieve(context.Background(), c, Query{ProblemStatement: "AutoCompactIfNeeded compaction"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Pack.Files) != 0 || !strings.Contains(res.Pack.Text, "func AutoCompactIfNeeded()") {
		t.Fatalf("pack files %v text %q", res.Pack.Files, res.Pack.Text)
	}
}

func TestM1ActivityBoostsTouchedFiles(t *testing.T) {
	c := corpus(t)
	q := Query{ProblemStatement: "AutoCompactIfNeeded compaction"}
	base, _ := NewM1().Retrieve(context.Background(), c, q, 8192)
	q.Trajectory = []Turn{
		{Tool: "Read", Input: `{"file_path":"/home/me/forge/internal/tui/program.go"}`},
		{Tool: "Edit", Input: `{"file_path":"/home/me/forge/internal/tui/program.go","old_string":"a"}`},
	}
	boosted, _ := NewM1().Retrieve(context.Background(), c, q, 8192)
	if base.Files[0] == "internal/tui/program.go" || boosted.Files[0] != "internal/tui/program.go" {
		t.Fatalf("base %v, boosted %v", base.Files, boosted.Files)
	}
}

func TestParseActivity(t *testing.T) {
	known := func(p string) bool {
		return p == "internal/query/loop.go" || p == "internal/tui/program.go" || p == "pkg/models.py"
	}
	turns := []Turn{
		{Tool: "Read", Input: `{"file_path":"/abs/root/internal/tui/program.go"}`},
		{Tool: "Grep", Input: `{"pattern":"parseHTTPRequest","path":"internal/query/loop.go"}`},
		{Tool: "Bash", Input: `{"command":"go test"}`, Output: "--- FAIL: TestX\n\tinternal/query/loop.go:42: boom\n  File \"pkg/models.py\", line 3\nvendor/x.go:1: nope"},
		{Tool: "Write", Input: `not json`},
	}
	a := ParseActivity(turns, known, 0.5)
	want := map[string]float64{
		"internal/tui/program.go": 0.125,      // read 3 turns ago
		"internal/query/loop.go":  0.25 + 0.5, // grep path 2 turns ago + bash file:line 1 turn ago
	}
	if !reflect.DeepEqual(a.Files, want) {
		t.Fatalf("Files = %v, want %v", a.Files, want)
	}
	if !reflect.DeepEqual(a.Terms, []string{"parse", "http", "request", "parsehttprequest"}) {
		t.Fatalf("Terms = %v", a.Terms)
	}
}

func TestM1Deterministic(t *testing.T) {
	q := Query{ProblemStatement: "RunQueryLoop compaction conversation provider program buildProvider"}
	first, _ := NewM1().Retrieve(context.Background(), corpus(t), q, 8192)
	for i := 0; i < 3; i++ {
		again, _ := NewM1().Retrieve(context.Background(), corpus(t), q, 8192)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, first, again)
		}
	}
}
