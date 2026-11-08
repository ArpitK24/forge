package retrieve

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/ctxengine/symbols"
	"github.com/ArpitK24/forge/internal/ctxengine/tokens"
)

// BM25 parameters (Robertson/Spärck Jones defaults).
const (
	bm25K1 = 1.2
	bm25B  = 0.75
)

// BM25 is an Okapi BM25 index over a fixed set of documents.
type BM25 struct {
	keys  []string
	tf    []map[string]int
	dl    []int
	df    map[string]int
	avgdl float64
}

// NewBM25 indexes docs, where docs[i] is the term list of keys[i].
func NewBM25(keys []string, docs [][]string) *BM25 {
	ix := &BM25{keys: keys, df: map[string]int{}}
	total := 0
	for _, d := range docs {
		tf := map[string]int{}
		for _, t := range d {
			tf[t]++
		}
		for t := range tf {
			ix.df[t]++
		}
		ix.tf = append(ix.tf, tf)
		ix.dl = append(ix.dl, len(d))
		total += len(d)
	}
	if len(docs) > 0 {
		ix.avgdl = float64(total) / float64(len(docs))
	}
	return ix
}

// IDF is the BM25+ style non-negative idf: ln(1 + (N-df+0.5)/(df+0.5)).
func (ix *BM25) IDF(term string) float64 {
	n := float64(len(ix.tf))
	df := float64(ix.df[term])
	return math.Log(1 + (n-df+0.5)/(df+0.5))
}

// Score returns each document's BM25 score for the unique query terms.
// Terms are visited in sorted order so the float sum is reproducible.
func (ix *BM25) Score(query []string) []float64 {
	uniq := uniqueSorted(query)
	scores := make([]float64, len(ix.tf))
	if ix.avgdl == 0 {
		return scores
	}
	for _, t := range uniq {
		if ix.df[t] == 0 {
			continue
		}
		idf := ix.IDF(t)
		for i, tf := range ix.tf {
			f := float64(tf[t])
			if f == 0 {
				continue
			}
			norm := bm25K1 * (1 - bm25B + bm25B*float64(ix.dl[i])/ix.avgdl)
			scores[i] += idf * (f * (bm25K1 + 1)) / (f + norm)
		}
	}
	return scores
}

// Rank returns keys with a positive score, best first.
func (ix *BM25) Rank(query []string) []string {
	s := ix.Score(query)
	items := make([]scored, len(s))
	for i := range s {
		items[i] = scored{key: ix.keys[i], score: s[i]}
	}
	return rank(items)
}

func uniqueSorted(ts []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range ts {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// B2 is full-file grep: BM25 over files (path terms + content terms) for
// the file ranking, BM25 over symbol bodies for the symbol ranking, and
// whole files packed greedily in rank order until the budget is spent.
type B2 struct{}

// Name implements Retriever.
func (B2) Name() string { return "b2" }

// Retrieve implements Retriever.
func (B2) Retrieve(ctx context.Context, c *Corpus, q Query, budget int) (Result, error) {
	query := symbols.Terms(q.ProblemStatement)
	files := c.Files()
	docs := make([][]string, len(files))
	var symKeys []string
	var symDocs [][]string
	for i, p := range files {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		content := c.Content(p)
		docs[i] = append(symbols.Terms(p), symbols.Terms(content)...)
		syms := c.Symbols(p)
		if len(syms) == 0 {
			continue
		}
		lines := strings.Split(content, "\n")
		for _, s := range syms {
			body := strings.Join(lines[max(s.Start-1, 0):min(s.End, len(lines))], "\n")
			symKeys = append(symKeys, symbols.ID(p, s.Name))
			symDocs = append(symDocs, append(symbols.Terms(s.Name), symbols.Terms(body)...))
		}
	}
	res := Result{
		Files:   NewBM25(files, docs).Rank(query),
		Symbols: NewBM25(symKeys, symDocs).Rank(query),
	}
	res.Pack = packWholeFiles(c, res.Files, budget)
	return res, nil
}

// packWholeFiles adds files in order while they fit, skipping any that
// would overflow, so a large early file does not starve the rest.
func packWholeFiles(c *Corpus, ranked []string, budget int) Pack {
	var b strings.Builder
	var p Pack
	for _, f := range ranked {
		chunk := "=== " + f + " ===\n" + c.Content(f)
		if !strings.HasSuffix(chunk, "\n") {
			chunk += "\n"
		}
		if tokens.EstimateBytes(b.Len()+len(chunk)) > budget {
			continue
		}
		b.WriteString(chunk)
		p.Files = append(p.Files, f)
	}
	p.Text = b.String()
	p.Tokens = tokens.Estimate(p.Text)
	return p
}
