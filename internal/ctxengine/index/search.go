package index

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Hit is one chunk matched by Search.
type Hit struct {
	ChunkID   int64
	Path      string
	Kind      string
	Name      string
	Start     int
	End       int
	Signature string
	Tokens    int
	// Score is FTS5's bm25() negated, so higher is better.
	Score float64
}

// SymbolID mirrors Chunk.SymbolID.
func (h Hit) SymbolID() string {
	if h.Name == "" {
		return ""
	}
	return h.Path + "::" + h.Name
}

// SearchOptions weighs the FTS5 columns and caps the result.
type SearchOptions struct {
	Limit      int     // default 2000
	PathWeight float64 // bm25 weight of the path column
	NameWeight float64 // ... of the symbol name column
	BodyWeight float64 // ... of the body column
}

// DefaultSearchOptions are the Milestone 1 column weights.
var DefaultSearchOptions = SearchOptions{Limit: 2000, PathWeight: 1, NameWeight: 2, BodyWeight: 1}

// Search runs a BM25 OR-query of terms (already code-tokenized, see
// symbols.Terms) over the chunk index. Hits come back best first with
// ties broken by chunk id, which follows path order on a cold index.
func (s *Store) Search(ctx context.Context, terms []string, opt SearchOptions) ([]Hit, error) {
	q := matchExpr(terms)
	if q == "" {
		return nil, nil
	}
	if opt.Limit <= 0 {
		opt.Limit = DefaultSearchOptions.Limit
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, f.path, c.kind, c.name, c.start_line, c.end_line, c.signature, c.tokens,
		       bm25(chunks_fts, ?, ?, ?) AS s
		FROM chunks_fts
		JOIN chunks c ON c.id = chunks_fts.rowid
		JOIN files f ON f.id = c.file_id
		WHERE chunks_fts MATCH ?
		ORDER BY s, c.id
		LIMIT ?`, opt.PathWeight, opt.NameWeight, opt.BodyWeight, q, opt.Limit)
	if err != nil {
		return nil, fmt.Errorf("index: search: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var s float64
		if err := rows.Scan(&h.ChunkID, &h.Path, &h.Kind, &h.Name, &h.Start, &h.End, &h.Signature, &h.Tokens, &s); err != nil {
			return nil, fmt.Errorf("index: search: %w", err)
		}
		h.Score = -s
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("index: search: %w", err)
	}
	return out, nil
}

// matchExpr builds `"a" OR "b" ...` from unique, sorted terms. Terms
// are quoted so FTS5 never reads one as an operator (AND, NOT, NEAR).
func matchExpr(terms []string) string {
	seen := map[string]bool{}
	var u []string
	for _, t := range terms {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] || strings.ContainsAny(t, "\"\x00") {
			continue
		}
		seen[t] = true
		u = append(u, t)
	}
	sort.Strings(u)
	for i, t := range u {
		u[i] = `"` + t + `"`
	}
	return strings.Join(u, " OR ")
}

// FileChunks returns a file's chunks in line order (no text: callers
// that need text re-chunk the file's current content with Chunks).
func (s *Store) FileChunks(ctx context.Context, path string) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, f.path, c.kind, c.name, c.start_line, c.end_line, c.signature, c.tokens
		FROM chunks c JOIN files f ON f.id = c.file_id
		WHERE f.path = ?
		ORDER BY c.start_line, c.id`, path)
	if err != nil {
		return nil, fmt.Errorf("index: file chunks: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.Path, &h.Kind, &h.Name, &h.Start, &h.End, &h.Signature, &h.Tokens); err != nil {
			return nil, fmt.Errorf("index: file chunks: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Paths returns every indexed (non-binary) path, sorted.
func (s *Store) Paths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM files WHERE skipped=0 ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("index: paths: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("index: paths: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
