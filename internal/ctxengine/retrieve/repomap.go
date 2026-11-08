package retrieve

import (
	"context"
	"math"
	"path"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/ctxengine/symbols"
	"github.com/ArpitK24/forge/internal/ctxengine/tokens"
)

// B1 is an Aider-style repository map (aider/repomap.py, re-implemented
// in pure Go):
//
//  1. Tags. Definitions come from symbols.Extract (go/ast for Go, the
//     indentation scanner for Python), keyed by their short name.
//     References are every identifier occurrence in the file.
//  2. Graph. For each identifier defined in file D and referenced in
//     file R there is an edge R -> D (self-edges included, as in Aider;
//     a file's own definition sites do not count as references) with
//     weight
//     mul * sqrt(refs), where mul is x10 for identifiers the issue
//     mentions, x10 for long compound names, x0.1 for _private names
//     and x0.1 for names defined in more than five files.
//  3. Rank. PageRank (damping 0.85) personalized toward files whose
//     path or stem the issue mentions, or that define a mentioned
//     identifier; uniform when the issue mentions nothing.
//  4. Map. Each edge passes its source's rank to the definition it
//     points at; definitions are rendered best first as
//     "path:" headers with their first source line until MapTokens is
//     spent (capped by the caller's budget).
//
// Only Go and Python files take part, since those are the languages we
// extract tags for.
type B1 struct {
	MapTokens int
}

// Name implements Retriever.
func (b B1) Name() string {
	if b.MapTokens == 1024 {
		return "b1-1k"
	}
	if b.MapTokens == 4096 {
		return "b1-4k"
	}
	return "b1"
}

type defKey struct {
	file  int
	ident string
}

// Retrieve implements Retriever.
func (b B1) Retrieve(ctx context.Context, c *Corpus, q Query, budget int) (Result, error) {
	var files []string
	for _, p := range c.Files() {
		if symbols.Language(p) != "" {
			files = append(files, p)
		}
	}
	if len(files) == 0 {
		return Result{}, nil
	}

	// 1. Tags.
	defines := map[string][]int{}                  // ident -> files defining it (sorted, unique)
	refs := make([]map[string]int, len(files))     // file -> ident -> count
	defCount := make([]map[string]int, len(files)) // file -> ident -> definitions
	for i, p := range files {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		dc := map[string]int{}
		for _, s := range c.Symbols(p) {
			id := shortName(s.Name)
			if dc[id] == 0 {
				defines[id] = append(defines[id], i)
			}
			dc[id]++
		}
		defCount[i] = dc
		r := map[string]int{}
		for _, id := range symbols.Identifiers(c.Content(p)) {
			r[id]++
		}
		refs[i] = r
	}

	// Mentions from the issue.
	mentionedIdents := map[string]bool{}
	for _, id := range symbols.Identifiers(q.ProblemStatement) {
		if _, ok := defines[id]; ok {
			mentionedIdents[id] = true
		}
	}
	personal := make([]float64, len(files))
	issue := q.ProblemStatement
	anyPersonal := false
	for i, p := range files {
		stem := strings.TrimSuffix(path.Base(p), path.Ext(p))
		if strings.Contains(issue, p) || containsWord(issue, path.Base(p)) ||
			(len(stem) >= 4 && mentionedIdents[stem]) {
			personal[i] = 1
			anyPersonal = true
		}
	}
	for id := range mentionedIdents {
		for _, f := range defines[id] {
			if personal[f] == 0 {
				personal[f] = 0.5
				anyPersonal = true
			}
		}
	}
	if !anyPersonal {
		for i := range personal {
			personal[i] = 1
		}
	}

	// 2. Graph, built in sorted identifier order.
	idents := make([]string, 0, len(defines))
	for id := range defines {
		idents = append(idents, id)
	}
	sort.Strings(idents)
	type edge struct {
		src, dst int
		w        float64
		ident    string
	}
	mul := map[string]float64{}
	for _, id := range idents {
		m := 1.0
		if mentionedIdents[id] {
			m *= 10
		}
		if len(id) >= 8 && isCompound(id) {
			m *= 10
		}
		if strings.HasPrefix(id, "_") {
			m *= 0.1
		}
		if len(defines[id]) > 5 {
			m *= 0.1
		}
		mul[id] = m
	}
	var edges []edge
	for r := range files {
		used := make([]string, 0, len(refs[r]))
		for id := range refs[r] {
			if _, ok := defines[id]; ok {
				used = append(used, id)
			}
		}
		sort.Strings(used)
		for _, id := range used {
			for _, d := range defines[id] {
				n := refs[r][id]
				if d == r {
					n -= defCount[r][id] // the definition sites themselves
				}
				if n > 0 {
					edges = append(edges, edge{src: r, dst: d, w: mul[id] * math.Sqrt(float64(n)), ident: id})
				}
			}
		}
	}

	// 3. PageRank.
	adj := make([][]wEdge, len(files))
	for _, e := range edges {
		adj[e.src] = append(adj[e.src], wEdge{to: e.dst, w: e.w})
	}
	pr := pageRank(adj, personal, 0.85, 100, 1e-12)

	fileItems := make([]scored, len(files))
	for i, p := range files {
		fileItems[i] = scored{key: p, score: pr[i]}
	}
	res := Result{Files: rank(fileItems)}

	// 4. Definition ranks.
	outW := make([]float64, len(files))
	for _, e := range edges {
		outW[e.src] += e.w
	}
	defRank := map[defKey]float64{}
	for _, e := range edges {
		defRank[defKey{e.dst, e.ident}] += pr[e.src] * e.w / outW[e.src]
	}
	var symItems []scored
	symFirstLine := map[string]string{}
	symFile := map[string]string{}
	fileLines := map[int][]string{}
	for k, v := range defRank {
		p := files[k.file]
		lines, ok := fileLines[k.file]
		if !ok {
			lines = strings.Split(c.Content(p), "\n")
			fileLines[k.file] = lines
		}
		for _, s := range c.Symbols(p) {
			if shortName(s.Name) != k.ident {
				continue
			}
			id := symbols.ID(p, s.Name)
			symItems = append(symItems, scored{key: id, score: v})
			line := ""
			for l := s.Start; l <= s.End && l <= len(lines); l++ {
				t := strings.TrimSpace(lines[l-1])
				if t != "" && !strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "@") {
					line = strings.TrimRight(lines[l-1], " \t\r")
					break
				}
			}
			if _, ok := symFile[id]; !ok {
				symFirstLine[id] = line
				symFile[id] = p
			}
		}
	}
	res.Symbols = rank(symItems)

	limit := min(b.MapTokens, budget)
	if b.MapTokens <= 0 {
		limit = budget
	}
	res.Pack = renderMap(res.Symbols, symFile, symFirstLine, limit)
	return res, nil
}

// renderMap writes definitions in rank order, grouped under their file
// in order of first appearance, stopping at the first one that would
// overflow the budget.
func renderMap(ranked []string, file, first map[string]string, budget int) Pack {
	order := []string{}
	byFile := map[string][]string{}
	size := 0
	for _, id := range ranked {
		f := file[id]
		add := len("│") + len(first[id]) + 1 // "│" + line + "\n"
		if _, ok := byFile[f]; !ok {
			add += len(f) + 2 // "path:\n"
		}
		if tokens.EstimateBytes(size+add) > budget {
			break
		}
		if _, ok := byFile[f]; !ok {
			order = append(order, f)
		}
		byFile[f] = append(byFile[f], first[id])
		size += add
	}
	var b strings.Builder
	for _, f := range order {
		b.WriteString(f + ":\n")
		for _, l := range byFile[f] {
			b.WriteString("│" + l + "\n")
		}
	}
	return Pack{Text: b.String(), Tokens: tokens.Estimate(b.String()), Files: order}
}

type wEdge struct {
	to int
	w  float64
}

// pageRank runs personalized PageRank by power iteration. Dangling nodes
// and teleports both follow the personalization vector. Iteration order
// is fixed so the result is reproducible.
func pageRank(adj [][]wEdge, personal []float64, d float64, iters int, tol float64) []float64 {
	n := len(adj)
	p := normalize(personal)
	outW := make([]float64, n)
	for i, es := range adj {
		for _, e := range es {
			outW[i] += e.w
		}
	}
	r := append([]float64(nil), p...)
	next := make([]float64, n)
	for it := 0; it < iters; it++ {
		dangling := 0.0
		for i := range r {
			if outW[i] == 0 {
				dangling += r[i]
			}
		}
		for i := range next {
			next[i] = (1-d)*p[i] + d*dangling*p[i]
		}
		for i, es := range adj {
			if outW[i] == 0 {
				continue
			}
			share := d * r[i] / outW[i]
			for _, e := range es {
				next[e.to] += share * e.w
			}
		}
		diff := 0.0
		for i := range r {
			diff += math.Abs(next[i] - r[i])
		}
		r, next = next, r
		if diff < tol {
			break
		}
	}
	return r
}

func normalize(v []float64) []float64 {
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	out := make([]float64, len(v))
	if sum == 0 {
		return out
	}
	for i, x := range v {
		out[i] = x / sum
	}
	return out
}

func shortName(qualified string) string {
	if i := strings.LastIndexByte(qualified, '.'); i >= 0 {
		return qualified[i+1:]
	}
	return qualified
}

func isCompound(id string) bool {
	return strings.Contains(strings.Trim(id, "_"), "_") || len(symbols.SplitIdent(id)) > 1
}

// containsWord reports whether word occurs in s bounded by non
// identifier characters (so "loop.go" matches "see loop.go" but not
// "myloop.go").
func containsWord(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !isIdentByte(s[j-1])
		after := j+len(word) == len(s) || !isIdentByte(s[j+len(word)])
		if before && after {
			return true
		}
		i = j + 1
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '/' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
