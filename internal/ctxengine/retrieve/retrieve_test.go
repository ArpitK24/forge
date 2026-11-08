package retrieve

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/ctxengine/tokens"
)

var repo = MapSnapshot{
	"internal/query/loop.go": `package query

// RunQueryLoop drives the agent loop and compaction.
func RunQueryLoop() { executeToolCalls(); AutoCompactIfNeeded() }

func executeToolCalls() {}
`,
	"internal/query/compaction.go": `package query

// AutoCompactIfNeeded compacts the conversation when the window fills.
func AutoCompactIfNeeded() { CompactConversation() }

func CompactConversation() {}
`,
	"internal/tui/program.go": `package tui

func RunProgram() { buildProvider() }

func buildProvider() {}
`,
	"README.md":   "Forge is a terminal coding agent.\n",
	"logo.png":    "\x89PNG\x00\x00binary",
	"big.go":      strings.Repeat("// x\n", MaxFileBytes/5+10),
	"vendor/x.go": "package x\n",
}

func corpus(t *testing.T) *Corpus {
	t.Helper()
	c, err := NewCorpus(repo)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCorpusFiltersFiles(t *testing.T) {
	want := []string{"README.md", "internal/query/compaction.go", "internal/query/loop.go", "internal/tui/program.go"}
	if got := corpus(t).Files(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Files = %v, want %v", got, want)
	}
}

func TestBM25(t *testing.T) {
	ix := NewBM25([]string{"a", "b", "c"}, [][]string{
		{"compact", "conversation", "window"},
		{"provider", "program"},
		{"compact", "compact", "compact", "noise", "noise", "noise", "noise", "noise"},
	})
	got := ix.Rank([]string{"compact", "window", "missing"})
	if !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Fatalf("Rank = %v, want [a c]", got)
	}
	if ix.IDF("compact") >= ix.IDF("window") {
		t.Error("rarer term should have higher idf")
	}
	if ix.IDF("missing") <= 0 {
		t.Error("idf must stay positive")
	}
	if s := ix.Score([]string{"program"}); s[0] != 0 || s[1] <= 0 {
		t.Errorf("Score = %v", s)
	}
}

func TestB2(t *testing.T) {
	c := corpus(t)
	res, err := B2{}.Retrieve(context.Background(), c,
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
	if !strings.Contains(res.Pack.Text, "=== internal/query/compaction.go ===") || res.Pack.Tokens != tokens.Estimate(res.Pack.Text) {
		t.Fatalf("pack = %q (%d tokens)", res.Pack.Text, res.Pack.Tokens)
	}
}

func TestB2RespectsBudget(t *testing.T) {
	c := corpus(t)
	q := Query{ProblemStatement: "RunQueryLoop compaction conversation provider program"}
	for _, budget := range []int{1, 30, 60, 200} {
		res, _ := B2{}.Retrieve(context.Background(), c, q, budget)
		if res.Pack.Tokens > budget {
			t.Errorf("budget %d: pack has %d tokens", budget, res.Pack.Tokens)
		}
		if budget == 1 && len(res.Pack.Files) != 0 {
			t.Errorf("budget 1 packed %v", res.Pack.Files)
		}
	}
}

func TestB1PersonalizesMentions(t *testing.T) {
	c := corpus(t)
	res, err := B1{MapTokens: 1024}.Retrieve(context.Background(), c,
		Query{ProblemStatement: "buildProvider ignores the API base"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files[0] != "internal/tui/program.go" {
		t.Fatalf("Files = %v, want program.go first (defines a mentioned ident)", res.Files)
	}
	if res.Pack.Tokens > 1024 || res.Pack.Tokens == 0 {
		t.Fatalf("map tokens = %d", res.Pack.Tokens)
	}
	// The query-loop file references compaction, so compaction.go's
	// definitions get rank from it.
	res, _ = B1{MapTokens: 4096}.Retrieve(context.Background(), c, Query{ProblemStatement: "loop.go misbehaves"}, 8192)
	// loop.go is personalized (named in the issue) and passes its rank
	// on to compaction.go, whose definitions it calls; program.go is
	// unreachable from the mention and gets nothing.
	if !reflect.DeepEqual(res.Files, []string{"internal/query/compaction.go", "internal/query/loop.go"}) {
		t.Fatalf("Files = %v, want compaction.go and loop.go only", res.Files)
	}
	if !strings.Contains(res.Pack.Text, "internal/query/compaction.go:\n│func AutoCompactIfNeeded()") {
		t.Fatalf("map = %q", res.Pack.Text)
	}
	if res.Symbols[0] != "internal/query/compaction.go::AutoCompactIfNeeded" && res.Symbols[0] != "internal/query/compaction.go::CompactConversation" {
		t.Fatalf("Symbols = %v", res.Symbols)
	}
}

func TestB1MapBudgetCappedByCaller(t *testing.T) {
	c := corpus(t)
	res, _ := B1{MapTokens: 4096}.Retrieve(context.Background(), c, Query{}, 20)
	if res.Pack.Tokens > 20 {
		t.Fatalf("map tokens = %d, budget 20", res.Pack.Tokens)
	}
}

func TestPageRank(t *testing.T) {
	adj := [][]wEdge{{{to: 1, w: 1}}, {{to: 2, w: 1}}, {}}
	pr := pageRank(adj, []float64{1, 1, 1}, 0.85, 100, 1e-12)
	sum := 0.0
	for _, x := range pr {
		sum += x
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("ranks sum to %v", sum)
	}
	if !(pr[2] > pr[1] && pr[1] > pr[0]) {
		t.Fatalf("pr = %v, want chain sink highest", pr)
	}
	// Personalization moves mass toward the personalized node.
	pp := pageRank(adj, []float64{1, 0, 0}, 0.85, 100, 1e-12)
	if pp[0] <= pr[0] {
		t.Fatalf("personalized pr[0] = %v, uniform %v", pp[0], pr[0])
	}
}

func TestB0(t *testing.T) {
	res, err := B0{}.Retrieve(context.Background(), corpus(t), Query{ProblemStatement: "anything"}, 100)
	if err != nil || len(res.Files) != 0 || res.Pack.Tokens != 0 {
		t.Fatalf("B0 = %+v, %v", res, err)
	}
}

func TestExpandNames(t *testing.T) {
	got, err := ExpandNames("b2, b1,b0,b2")
	if err != nil || !reflect.DeepEqual(got, []string{"b0", "b1-1k", "b1-4k", "b2"}) {
		t.Fatalf("ExpandNames = %v, %v", got, err)
	}
	if _, err := ExpandNames("b9"); err == nil {
		t.Fatal("unknown baseline accepted")
	}
	if _, err := ExpandNames(" , "); err == nil {
		t.Fatal("empty list accepted")
	}
}

func TestRankQuantizesAndBreaksTies(t *testing.T) {
	got := rank([]scored{{"b", 1.0000000000001}, {"a", 1}, {"c", 0}, {"a", 1}})
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("rank = %v, want [a b]", got)
	}
}

func TestContainsWord(t *testing.T) {
	if !containsWord("see loop.go now", "loop.go") || containsWord("myloop.go", "loop.go") || containsWord("x/loop.go", "loop.go") {
		t.Fatal("containsWord boundaries wrong")
	}
}

func TestDirSnapshot(t *testing.T) {
	dir := t.TempDir()
	snap := DirSnapshot(dir)
	files, err := snap.Files()
	if err != nil || len(files) != 0 {
		t.Fatalf("empty dir: %v %v", files, err)
	}
}
