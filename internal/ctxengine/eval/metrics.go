package eval

import (
	"math"
	"sort"

	"github.com/ArpitK24/forge/internal/ctxengine/retrieve"
)

// Metric names, in report order.
const (
	MFileR1     = "file_recall@1"
	MFileR3     = "file_recall@3"
	MFileR5     = "file_recall@5"
	MFileR10    = "file_recall@10"
	MFuncR5     = "func_recall@5"
	MFuncR10    = "func_recall@10"
	MMRR        = "mrr"
	MGoldInPack = "gold_in_pack"
	MPackTokens = "pack_tokens"
)

// MetricNames lists every metric in report order.
var MetricNames = []string{MFileR1, MFileR3, MFileR5, MFileR10, MFuncR5, MFuncR10, MMRR, MGoldInPack, MPackTokens}

// TaskResult is one baseline's outcome on one task.
type TaskResult struct {
	ID string `json:"id"`
	// Metrics holds every metric that is defined for the task. Function
	// recall is absent when the task has no gold functions.
	Metrics    map[string]float64 `json:"metrics"`
	TopFiles   []string           `json:"top_files"`
	TopSymbols []string           `json:"top_symbols"`
	PackFiles  int                `json:"pack_files"`
}

// Score computes the per-task metrics for one retrieval result.
//
//   - file_recall@k: |gold files in the top k| / |gold files|.
//   - func_recall@k: the same over gold symbol ids and the ranked
//     symbol list (only defined when the task has gold functions).
//   - mrr: 1 / rank of the first gold file in the ranking, 0 if none.
//   - gold_in_pack: |gold files present in the pack| / |gold files|.
//   - pack_tokens: the pack's estimated token count.
func Score(t Task, r retrieve.Result) TaskResult {
	m := map[string]float64{
		MFileR1:     recallAt(r.Files, t.GoldFiles, 1),
		MFileR3:     recallAt(r.Files, t.GoldFiles, 3),
		MFileR5:     recallAt(r.Files, t.GoldFiles, 5),
		MFileR10:    recallAt(r.Files, t.GoldFiles, 10),
		MMRR:        reciprocalRank(r.Files, t.GoldFiles),
		MGoldInPack: recallAt(r.Pack.Files, t.GoldFiles, len(r.Pack.Files)),
		MPackTokens: float64(r.Pack.Tokens),
	}
	if len(t.GoldFunctions) > 0 {
		m[MFuncR5] = recallAt(r.Symbols, t.GoldFunctions, 5)
		m[MFuncR10] = recallAt(r.Symbols, t.GoldFunctions, 10)
	}
	return TaskResult{
		ID:         t.ID,
		Metrics:    m,
		TopFiles:   head(r.Files, 10),
		TopSymbols: head(r.Symbols, 10),
		PackFiles:  len(r.Pack.Files),
	}
}

func recallAt(ranked, gold []string, k int) float64 {
	if len(gold) == 0 {
		return 0
	}
	g := map[string]bool{}
	for _, x := range gold {
		g[x] = true
	}
	hit := 0
	for i, x := range ranked {
		if i >= k {
			break
		}
		if g[x] {
			hit++
			delete(g, x)
		}
	}
	return float64(hit) / float64(len(gold))
}

func reciprocalRank(ranked, gold []string) float64 {
	g := map[string]bool{}
	for _, x := range gold {
		g[x] = true
	}
	for i, x := range ranked {
		if g[x] {
			return 1 / float64(i+1)
		}
	}
	return 0
}

func head(xs []string, n int) []string {
	out := make([]string, 0, min(n, len(xs)))
	return append(out, xs[:min(n, len(xs))]...)
}

// Stat is an aggregate with a percentile bootstrap confidence interval.
type Stat struct {
	Mean float64 `json:"mean"`
	Lo   float64 `json:"ci_lo"`
	Hi   float64 `json:"ci_hi"`
	N    int     `json:"n"`
}

// Paired is the bootstrap distribution of B - A over the same tasks.
type Paired struct {
	A        string  `json:"a"`
	B        string  `json:"b"`
	Metric   string  `json:"metric"`
	MeanDiff float64 `json:"mean_diff"`
	Lo       float64 `json:"ci_lo"`
	Hi       float64 `json:"ci_hi"`
	N        int     `json:"n"`
}

// Bootstrap holds a fixed resample matrix so every baseline and metric
// is resampled with exactly the same task indices (paired bootstrap).
type Bootstrap struct {
	Resamples int
	Seed      uint64
	Level     float64
	idx       [][]int
}

// NewBootstrap draws resamples x n indices from a SplitMix64 stream
// seeded with seed. SplitMix64 is spelled out here (not math/rand) so
// the stream can never change under a Go upgrade.
func NewBootstrap(n, resamples int, seed uint64, level float64) *Bootstrap {
	b := &Bootstrap{Resamples: resamples, Seed: seed, Level: level}
	if n == 0 {
		return b
	}
	rng := splitMix(seed)
	for r := 0; r < resamples; r++ {
		row := make([]int, n)
		for i := range row {
			row[i] = int(rng() % uint64(n))
		}
		b.idx = append(b.idx, row)
	}
	return b
}

// Mean aggregates vals (NaN = undefined for that task).
func (b *Bootstrap) Mean(vals []float64) Stat {
	mean, n := meanDefined(vals, nil)
	st := Stat{Mean: round6(mean), N: n}
	if n == 0 {
		return st
	}
	var dist []float64
	for _, row := range b.idx {
		if m, k := meanDefined(vals, row); k > 0 {
			dist = append(dist, m)
		}
	}
	st.Lo, st.Hi = b.interval(dist)
	return st
}

// Diff aggregates b - a, paired by task.
func (b *Bootstrap) Diff(a, c []float64) (mean, lo, hi float64, n int) {
	d := make([]float64, len(a))
	for i := range a {
		d[i] = c[i] - a[i] // NaN propagates: undefined in either is undefined
	}
	st := b.Mean(d)
	return st.Mean, st.Lo, st.Hi, st.N
}

func (b *Bootstrap) interval(dist []float64) (float64, float64) {
	if len(dist) == 0 {
		return 0, 0
	}
	sort.Float64s(dist)
	alpha := (1 - b.Level) / 2
	lo := int(math.Floor(alpha * float64(len(dist))))
	hi := int(math.Ceil((1-alpha)*float64(len(dist)))) - 1
	lo = min(max(lo, 0), len(dist)-1)
	hi = min(max(hi, 0), len(dist)-1)
	return round6(dist[lo]), round6(dist[hi])
}

// meanDefined averages the non-NaN values, over idx when given.
func meanDefined(vals []float64, idx []int) (float64, int) {
	sum, n := 0.0, 0
	if idx == nil {
		for _, v := range vals {
			if !math.IsNaN(v) {
				sum += v
				n++
			}
		}
	} else {
		for _, i := range idx {
			if v := vals[i]; !math.IsNaN(v) {
				sum += v
				n++
			}
		}
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}

// round6 rounds to 6 decimals so reports are stable and readable.
func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }

// splitMix returns a SplitMix64 stream.
func splitMix(seed uint64) func() uint64 {
	s := seed
	return func() uint64 {
		s += 0x9e3779b97f4a7c15
		z := s
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		return z ^ (z >> 31)
	}
}
