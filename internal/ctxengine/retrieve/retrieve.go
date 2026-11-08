// Package retrieve defines the context engine's retrieval contract and
// the Milestone 0 baselines measured against it:
//
//   - B0 ("b0"): no engine. Today's Forge: nothing ranked, empty pack.
//   - B1 ("b1-1k", "b1-4k"): an Aider-style repo map. Definition and
//     reference tags, a file graph weighted by shared identifiers,
//     PageRank personalized by files and identifiers the issue mentions,
//     and a map of top definitions rendered to a 1k or 4k token budget.
//   - B2 ("b2"): full-file grep. BM25 over identifier-split query terms,
//     whole files packed greedily up to the budget.
//
// Everything here is deterministic: inputs are sorted, scores are
// quantized before ranking and ties break on the path or symbol id.
package retrieve

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Turn is one replayed agent step (a tool call and its result). The
// baselines ignore it; later milestones seed the graph from it. It is
// plain data so ctxengine never depends on internal/tools.
type Turn struct {
	Tool   string `json:"tool"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

// Query is what a retriever is asked.
type Query struct {
	ProblemStatement string
	Trajectory       []Turn
}

// Pack is the context a retriever would put in front of the model.
type Pack struct {
	Text   string
	Tokens int
	// Files lists every file whose content (B2) or map entry (B1)
	// is in Text, in pack order.
	Files []string
}

// Result is a retriever's answer for one task.
type Result struct {
	Files   []string // ranked, best first; only files with signal
	Symbols []string // ranked symbol ids ("path::Name")
	Pack    Pack
}

// Retriever ranks a repository snapshot against a query and packs the
// best of it under a token budget.
type Retriever interface {
	Name() string
	Retrieve(ctx context.Context, c *Corpus, q Query, budget int) (Result, error)
}

// Names lists the baselines New accepts, in canonical order.
var Names = []string{"b0", "b1-1k", "b1-4k", "b2"}

// New returns the named baseline. "b1" is not a retriever by itself; use
// ExpandNames to turn it into b1-1k and b1-4k.
func New(name string) (Retriever, error) {
	switch name {
	case "b0":
		return B0{}, nil
	case "b1-1k":
		return B1{MapTokens: 1024}, nil
	case "b1-4k":
		return B1{MapTokens: 4096}, nil
	case "b2":
		return B2{}, nil
	}
	return nil, fmt.Errorf("unknown baseline %q (want one of %s, or b1)", name, strings.Join(Names, ", "))
}

// ExpandNames parses a comma-separated baseline list, expands "b1" to
// both map sizes, drops duplicates and returns canonical order.
func ExpandNames(list string) ([]string, error) {
	want := map[string]bool{}
	for _, n := range strings.Split(list, ",") {
		n = strings.ToLower(strings.TrimSpace(n))
		switch n {
		case "":
			continue
		case "b1":
			want["b1-1k"], want["b1-4k"] = true, true
			continue
		}
		if _, err := New(n); err != nil {
			return nil, err
		}
		want[n] = true
	}
	var out []string
	for _, n := range Names {
		if want[n] {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no baselines selected")
	}
	return out, nil
}

// B0 is "no context engine": it retrieves nothing.
type B0 struct{}

// Name implements Retriever.
func (B0) Name() string { return "b0" }

// Retrieve implements Retriever.
func (B0) Retrieve(context.Context, *Corpus, Query, int) (Result, error) { return Result{}, nil }

// quantize rounds a score to 1e-9 so that last-bit float differences
// (for example fused multiply-add on arm64 vs amd64) cannot reorder
// results across machines.
func quantize(x float64) float64 { return math.Round(x*1e9) / 1e9 }

type scored struct {
	key   string
	score float64
}

// rank sorts by quantized score descending, then key ascending, and
// drops non-positive scores.
func rank(items []scored) []string {
	for i := range items {
		items[i].score = quantize(items[i].score)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].key < items[j].key
	})
	out := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if it.score > 0 && !seen[it.key] {
			seen[it.key] = true
			out = append(out, it.key)
		}
	}
	return out
}
