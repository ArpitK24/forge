package retrieve

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/ctxengine/index"
	"github.com/ArpitK24/forge/internal/ctxengine/symbols"
	"github.com/ArpitK24/forge/internal/ctxengine/tokens"
)

// M1Options are the knobs of the Milestone 1 retriever. Defaults were
// chosen on the internal set and the Verified dev split only (see
// eval/README.md); never tune them on held-out tasks.
type M1Options struct {
	// Search weighs the FTS5 columns (path, symbol name, body).
	Search index.SearchOptions
	// FileDecay aggregates a file's chunk scores: the best chunk
	// counts fully, the next ones are weighted FileDecay, FileDecay²,
	// ... up to FileTopChunks chunks.
	FileDecay     float64
	FileTopChunks int
	// SymbolFileWeight mixes the file score into each symbol's score
	// (as a fraction of the best file score), so symbols in strong
	// files outrank lone lexical matches elsewhere.
	SymbolFileWeight float64
	// ActivityWeight scales the activity boost (files the session read
	// or edited) relative to the best lexical file score.
	ActivityWeight float64
	// ActivityDecay is the per-turn decay of an activity hit.
	ActivityDecay float64
	// Packer.
	PackMaxFiles     int     // files considered for the pack
	PackMinScore     float64 // drop chunks below this fraction of the best
	PackMaxChunkFrac float64 // a body larger than this budget fraction is shown as a signature
	PackSigsPerFile  int     // signature lines per file for unexpanded matches
}

// DefaultM1Options are the committed Milestone 1 settings.
var DefaultM1Options = M1Options{
	Search:           index.SearchOptions{Limit: 2000, PathWeight: 1, NameWeight: 2, BodyWeight: 1},
	FileDecay:        0.5,
	FileTopChunks:    3,
	SymbolFileWeight: 0,
	ActivityWeight:   1,
	ActivityDecay:    0.7,
	PackMaxFiles:     10,
	PackMinScore:     0.6,
	PackMaxChunkFrac: 0.25,
	PackSigsPerFile:  5,
}

// M1 is the Milestone 1 retriever ("m1"): BM25 over symbol-level
// chunks in the SQLite/FTS5 index, aggregated to files, boosted by
// session activity, and packed as symbol bodies and signatures instead
// of whole files.
type M1 struct {
	Opts M1Options
}

// NewM1 returns M1 with the default options.
func NewM1() M1 { return M1{Opts: DefaultM1Options} }

// Name implements Retriever.
func (M1) Name() string { return "m1" }

// Retrieve implements Retriever by building an in-memory index of the
// corpus (the same code path the engine uses on disk) and querying it.
func (m M1) Retrieve(ctx context.Context, c *Corpus, q Query, budget int) (Result, error) {
	s, err := index.OpenMemory()
	if err != nil {
		return Result{}, err
	}
	defer s.Close()
	if _, err := s.Sync(ctx, corpusSource{c}); err != nil {
		return Result{}, err
	}
	return m.RetrieveIndex(ctx, s, q, budget, func(p string) (string, bool) {
		if !c.Has(p) {
			return "", false
		}
		return c.Content(p), true
	})
}

// corpusSource adapts a Corpus to index.Source.
type corpusSource struct{ c *Corpus }

func (s corpusSource) List(context.Context) ([]index.FileStat, error) {
	out := make([]index.FileStat, 0, len(s.c.Files()))
	for _, p := range s.c.Files() {
		out = append(out, index.FileStat{Path: p, Size: int64(len(s.c.Content(p)))})
	}
	return out, nil
}

func (s corpusSource) Stat(p string) (index.FileStat, bool, error) {
	if !s.c.Has(p) {
		return index.FileStat{}, false, nil
	}
	return index.FileStat{Path: p, Size: int64(len(s.c.Content(p)))}, true, nil
}

func (s corpusSource) Read(p string) ([]byte, error) {
	if !s.c.Has(p) {
		return nil, fmt.Errorf("%s: not in corpus", p)
	}
	return []byte(s.c.Content(p)), nil
}

// RetrieveIndex ranks and packs against an existing index. read returns
// a file's current content for packing.
func (m M1) RetrieveIndex(ctx context.Context, s *index.Store, q Query, budget int, read func(string) (string, bool)) (Result, error) {
	o := m.Opts
	known := func(p string) bool { _, ok := read(p); return ok }
	act := ParseActivity(q.Trajectory, known, o.ActivityDecay)
	terms := append(symbols.Terms(q.ProblemStatement), act.Terms...)
	hits, err := s.Search(ctx, terms, o.Search)
	if err != nil {
		return Result{}, err
	}
	for i := range hits {
		hits[i].Score = quantize(hits[i].Score)
	}

	// Files: decayed sum of each file's best chunk scores.
	perFile := map[string][]float64{}
	for _, h := range hits {
		if len(perFile[h.Path]) < max(o.FileTopChunks, 1) {
			perFile[h.Path] = append(perFile[h.Path], h.Score) // hits are best-first
		}
	}
	fileScore := map[string]float64{}
	best := 0.0
	for p, ss := range perFile {
		v, w := 0.0, 1.0
		for _, x := range ss {
			v += w * x
			w *= o.FileDecay
		}
		fileScore[p] = v
		best = math.Max(best, v)
	}
	if best == 0 {
		best = 1
	}
	for p, a := range act.Files {
		fileScore[p] += o.ActivityWeight * a * best
	}
	items := make([]scored, 0, len(fileScore))
	for p, v := range fileScore {
		items = append(items, scored{key: p, score: v})
	}
	files := rank(items)

	// Symbols: chunk score plus a share of the file score.
	var syms []scored
	for _, h := range hits {
		if h.Name == "" {
			continue
		}
		syms = append(syms, scored{key: h.SymbolID(), score: h.Score + o.SymbolFileWeight*fileScore[h.Path]/best*hits[0].Score})
	}
	res := Result{Files: files, Symbols: rank(syms)}
	res.Pack = m.pack(hits, act.Files, files, budget, read)
	return res, nil
}

// pack is the Milestone 1 packer, a simplified §10 allocator: chunk
// bodies (L1) of the best chunks in the top files, in score order,
// while they fit and score at least PackMinScore of the best; then
// signature lines (L2) for further matches in those files. Output is
// grouped by file in rank order, chunks in line order.
func (m M1) pack(hits []index.Hit, activity map[string]float64, files []string, budget int, read func(string) (string, bool)) Pack {
	o := m.Opts
	// Chunks in files the session touched get the same activity
	// boost their file got, so an edited file's matches are packed
	// even when other files match the query better.
	if len(hits) > 0 && len(activity) > 0 {
		top := hits[0].Score
		boosted := append([]index.Hit(nil), hits...)
		for i := range boosted {
			boosted[i].Score = quantize(boosted[i].Score + o.ActivityWeight*activity[boosted[i].Path]*top)
		}
		sort.SliceStable(boosted, func(i, j int) bool { return boosted[i].Score > boosted[j].Score })
		hits = boosted
	}
	top := map[string]int{}
	for i, f := range files {
		if i >= o.PackMaxFiles {
			break
		}
		top[f] = i
	}
	type item struct {
		h    index.Hit
		text string // body, or "" for a signature line
	}
	sel := map[string][]item{}
	used := map[int64]bool{}
	chunkCache := map[string][]index.Chunk{}
	chunkText := func(h index.Hit) (string, bool) {
		cs, ok := chunkCache[h.Path]
		if !ok {
			content, ok := read(h.Path)
			if !ok {
				return "", false
			}
			cs = index.Chunks(h.Path, []byte(content))
			chunkCache[h.Path] = cs
		}
		for _, c := range cs {
			if c.Start == h.Start && c.Name == h.Name && c.Kind == h.Kind {
				return c.Text, true
			}
		}
		return "", false
	}
	headerBytes := func(f string) int { return len("=== " + f + " ===\n") }
	bodyHead := func(h index.Hit) string {
		label := h.Name
		if label == "" {
			label = h.Kind
		}
		return fmt.Sprintf("-- %s L%d-%d\n", label, h.Start, h.End)
	}
	sigLine := func(h index.Hit) string {
		return fmt.Sprintf("L%d: %s\n", h.Start, h.Signature)
	}
	bytesUsed := 0
	fits := func(n int) bool { return tokens.EstimateBytes(bytesUsed+n) <= budget }
	maxChunk := int(float64(budget) * o.PackMaxChunkFrac)
	minScore := 0.0
	if len(hits) > 0 {
		minScore = hits[0].Score * o.PackMinScore
	}

	// L1: bodies.
	for _, h := range hits {
		if _, ok := top[h.Path]; !ok || h.Score < minScore {
			continue
		}
		if h.Tokens > maxChunk {
			continue
		}
		text, ok := chunkText(h)
		if !ok {
			continue
		}
		n := len(bodyHead(h)) + len(text)
		if len(sel[h.Path]) == 0 {
			n += headerBytes(h.Path)
		}
		if !fits(n) {
			continue
		}
		bytesUsed += n
		used[h.ChunkID] = true
		sel[h.Path] = append(sel[h.Path], item{h: h, text: text})
	}
	// L2: signatures of further matches in the top files.
	sigs := map[string]int{}
	for _, h := range hits {
		if _, ok := top[h.Path]; !ok || used[h.ChunkID] || h.Signature == "" || h.Score < minScore {
			continue
		}
		if sigs[h.Path] >= o.PackSigsPerFile {
			continue
		}
		n := len(sigLine(h))
		if len(sel[h.Path]) == 0 {
			n += headerBytes(h.Path)
		}
		if !fits(n) {
			continue
		}
		bytesUsed += n
		sigs[h.Path]++
		used[h.ChunkID] = true
		sel[h.Path] = append(sel[h.Path], item{h: h})
	}

	var b strings.Builder
	var p Pack
	for _, f := range files {
		its := sel[f]
		if len(its) == 0 {
			continue
		}
		sort.Slice(its, func(i, j int) bool {
			if its[i].h.Start != its[j].h.Start {
				return its[i].h.Start < its[j].h.Start
			}
			return its[i].h.ChunkID < its[j].h.ChunkID
		})
		b.WriteString("=== " + f + " ===\n")
		hasBody := false
		for _, it := range its {
			if it.text != "" {
				hasBody = true
				b.WriteString(bodyHead(it.h))
				b.WriteString(it.text)
			} else {
				b.WriteString(sigLine(it.h))
			}
		}
		// gold_in_pack counts a file only when real code from it is
		// packed; a signature line alone does not count.
		if hasBody {
			p.Files = append(p.Files, f)
		}
	}
	p.Text = b.String()
	p.Tokens = tokens.Estimate(p.Text)
	return p
}

// Activity is what the session's own tool calls say about relevance.
type Activity struct {
	// Files maps a path to its decayed activity weight (an edit counts
	// 2, a read or a file:line in tool output 1, decayed per turn).
	Files map[string]float64
	// Terms are extra lexical terms (Grep patterns).
	Terms []string
}

var fileLineRE = regexp.MustCompile(`([A-Za-z0-9_./\\-]+\.[A-Za-z0-9]+):(\d+)`)

// ParseActivity reads the turn history (oldest first). known reports
// whether a slash-separated relative path is indexed; absolute or
// partial paths in tool calls are matched to indexed paths by suffix.
// The most recent turn has weight 1, earlier ones decay by decay^age.
func ParseActivity(turns []Turn, known func(string) bool, decay float64) Activity {
	a := Activity{Files: map[string]float64{}}
	resolve := func(p string) string {
		p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
		p = strings.TrimPrefix(p, "./")
		if p == "" {
			return ""
		}
		// Try the path and every suffix after a slash: an absolute
		// path ends with the repo-relative one.
		for {
			if known(p) {
				return p
			}
			i := strings.IndexByte(p, '/')
			if i < 0 {
				return ""
			}
			p = p[i+1:]
		}
	}
	add := func(p string, w float64) {
		if r := resolve(p); r != "" {
			a.Files[r] += w
		}
	}
	for i, t := range turns {
		w := math.Pow(decay, float64(len(turns)-1-i))
		var in struct {
			FilePath string `json:"file_path"`
			Path     string `json:"path"`
			Pattern  string `json:"pattern"`
		}
		_ = json.Unmarshal([]byte(t.Input), &in)
		switch t.Tool {
		case "Read":
			add(in.FilePath, w)
		case "Edit", "Write", "MultiEdit":
			add(in.FilePath, 2*w)
		case "Grep":
			a.Terms = append(a.Terms, symbols.Terms(in.Pattern)...)
			if in.Path != "" && path.Ext(in.Path) != "" {
				add(in.Path, w)
			}
		}
		// file:line references in any tool output (test failures,
		// panics, compiler errors, grep -n results).
		seen := map[string]bool{}
		for _, mm := range fileLineRE.FindAllStringSubmatch(t.Output, 200) {
			if r := resolve(mm[1]); r != "" && !seen[r] {
				seen[r] = true
				a.Files[r] += w
			}
		}
	}
	return a
}
