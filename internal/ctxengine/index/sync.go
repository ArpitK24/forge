package index

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/ArpitK24/forge/internal/ctxengine/symbols"
)

// SyncStats reports what a Sync or Refresh did.
type SyncStats struct {
	Listed    int // files the source offered
	Added     int // new files parsed and inserted
	Updated   int // files whose content changed and were reparsed
	Touched   int // mtime/size changed but the hash did not
	Unchanged int // mtime and size matched, not read
	Removed   int // rows deleted for files that are gone
	Skipped   int // binary files recorded without chunks
}

// Changed reports whether the sync wrote any chunk.
func (s SyncStats) Changed() bool { return s.Added+s.Updated+s.Removed > 0 }

type fileRow struct {
	id      int64
	size    int64
	mtimeNS int64
	hash    string
}

// parsed is one file read, hashed and (when new) chunked.
type parsed struct {
	stat    FileStat
	hash    string
	binary  bool
	chunks  []Chunk
	prev    *fileRow
	touched bool // only mtime/size changed
	err     error
}

// Sync mirrors the whole source into the index: new and changed files
// are (re)parsed, vanished files are removed. Files whose mtime and
// size match the stored row are not read at all.
func (s *Store) Sync(ctx context.Context, src Source) (SyncStats, error) {
	list, err := src.List(ctx)
	if err != nil {
		return SyncStats{}, fmt.Errorf("index: list: %w", err)
	}
	rows, err := s.fileRows(ctx, nil)
	if err != nil {
		return SyncStats{}, err
	}
	listed := make(map[string]bool, len(list))
	for _, st := range list {
		listed[st.Path] = true
	}
	var gone []string
	for p := range rows {
		if !listed[p] {
			gone = append(gone, p)
		}
	}
	sort.Strings(gone)
	return s.apply(ctx, src, list, rows, gone)
}

// Refresh re-checks only the given paths (slash-separated, relative to
// the source root): changed files are reparsed and deleted ones
// removed. The engine calls it synchronously after Edit and Write so
// the next turn sees the agent's own edits.
func (s *Store) Refresh(ctx context.Context, src Source, paths []string) (SyncStats, error) {
	want := uniq(paths)
	rows, err := s.fileRows(ctx, want)
	if err != nil {
		return SyncStats{}, err
	}
	var list []FileStat
	var gone []string
	for _, p := range want {
		st, ok, err := src.Stat(p)
		if err != nil {
			return SyncStats{}, fmt.Errorf("index: stat %s: %w", p, err)
		}
		if ok {
			list = append(list, st)
		} else if _, had := rows[p]; had {
			gone = append(gone, p)
		}
	}
	return s.apply(ctx, src, list, rows, gone)
}

func uniq(ps []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range ps {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) fileRows(ctx context.Context, only []string) (map[string]*fileRow, error) {
	out := map[string]*fileRow{}
	scan := func(rows *sql.Rows) error {
		defer rows.Close()
		for rows.Next() {
			var p string
			r := &fileRow{}
			if err := rows.Scan(&p, &r.id, &r.size, &r.mtimeNS, &r.hash); err != nil {
				return err
			}
			out[p] = r
		}
		return rows.Err()
	}
	const q = `SELECT path, id, size, mtime_ns, hash FROM files`
	if only == nil {
		rows, err := s.db.QueryContext(ctx, q)
		if err != nil {
			return nil, fmt.Errorf("index: read files: %w", err)
		}
		if err := scan(rows); err != nil {
			return nil, fmt.Errorf("index: read files: %w", err)
		}
		return out, nil
	}
	for _, p := range only {
		rows, err := s.db.QueryContext(ctx, q+` WHERE path = ?`, p)
		if err != nil {
			return nil, fmt.Errorf("index: read files: %w", err)
		}
		if err := scan(rows); err != nil {
			return nil, fmt.Errorf("index: read files: %w", err)
		}
	}
	return out, nil
}

func (s *Store) apply(ctx context.Context, src Source, list []FileStat, rows map[string]*fileRow, gone []string) (SyncStats, error) {
	st := SyncStats{Listed: len(list)}
	var todo []FileStat
	for _, f := range list {
		r := rows[f.Path]
		// mtime 0 means "unknown" (in-memory sources): always hash.
		if r != nil && f.MtimeNS != 0 && r.mtimeNS == f.MtimeNS && r.size == f.Size {
			st.Unchanged++
			continue
		}
		todo = append(todo, f)
	}
	results := parseAll(ctx, src, todo, rows)
	if err := ctx.Err(); err != nil {
		return st, err
	}
	for _, r := range results {
		if r.err != nil {
			return st, r.err
		}
	}
	if len(results) == 0 && len(gone) == 0 {
		return st, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return st, fmt.Errorf("index: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	w, err := newWriter(ctx, tx)
	if err != nil {
		return st, err
	}
	defer w.close()
	// Bump the epoch once, and only when chunks change.
	epoch := int64(-1)
	needEpoch := func() error {
		if epoch >= 0 {
			return nil
		}
		var err error
		epoch, err = w.bumpEpoch(ctx)
		return err
	}
	for _, p := range gone {
		if err := needEpoch(); err != nil {
			return st, err
		}
		if err := w.deleteFile(ctx, rows[p].id); err != nil {
			return st, err
		}
		st.Removed++
	}
	for _, r := range results {
		switch {
		case r.touched:
			if _, err := w.touch.ExecContext(ctx, r.stat.Size, r.stat.MtimeNS, r.prev.id); err != nil {
				return st, fmt.Errorf("index: touch %s: %w", r.stat.Path, err)
			}
			if r.stat.MtimeNS != 0 {
				st.Touched++
			} else {
				st.Unchanged++
			}
			continue
		case r.prev != nil:
			st.Updated++
		default:
			st.Added++
		}
		if r.binary {
			st.Skipped++
		}
		if err := needEpoch(); err != nil {
			return st, err
		}
		if err := w.putFile(ctx, r, epoch); err != nil {
			return st, err
		}
	}
	if err := tx.Commit(); err != nil {
		return st, fmt.Errorf("index: commit: %w", err)
	}
	return st, nil
}

// parseAll reads, hashes and chunks files on all CPUs. Results come
// back in input order so inserts (and rowids) are deterministic.
func parseAll(ctx context.Context, src Source, todo []FileStat, rows map[string]*fileRow) []parsed {
	out := make([]parsed, len(todo))
	workers := min(runtime.GOMAXPROCS(0), max(len(todo), 1))
	next := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				out[i] = parseOne(src, todo[i], rows[todo[i].Path])
			}
		}()
	}
	for i := range todo {
		if ctx.Err() != nil {
			break
		}
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}

func parseOne(src Source, st FileStat, prev *fileRow) parsed {
	p := parsed{stat: st, prev: prev}
	b, err := src.Read(st.Path)
	if err != nil {
		p.err = fmt.Errorf("index: read %s: %w", st.Path, err)
		return p
	}
	p.stat.Size = int64(len(b))
	sum := sha256.Sum256(b)
	p.hash = hex.EncodeToString(sum[:16])
	if prev != nil && prev.hash == p.hash {
		p.touched = true
		return p
	}
	if bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		p.binary = true
		return p
	}
	p.chunks = Chunks(st.Path, b)
	return p
}

// writer holds the prepared statements of one write transaction.
type writer struct {
	tx                                       *sql.Tx
	touch, insFile, updFile, insChunk, insFT *sql.Stmt
	delFT, delChunks, delFile                *sql.Stmt
}

func newWriter(ctx context.Context, tx *sql.Tx) (*writer, error) {
	w := &writer{tx: tx}
	prep := []struct {
		dst **sql.Stmt
		q   string
	}{
		{&w.touch, `UPDATE files SET size=?, mtime_ns=? WHERE id=?`},
		{&w.insFile, `INSERT INTO files(path, lang, size, mtime_ns, hash, skipped, epoch) VALUES(?,?,?,?,?,?,?)`},
		{&w.updFile, `UPDATE files SET lang=?, size=?, mtime_ns=?, hash=?, skipped=?, epoch=? WHERE id=?`},
		{&w.insChunk, `INSERT INTO chunks(file_id, kind, name, start_line, end_line, signature, tokens) VALUES(?,?,?,?,?,?,?)`},
		{&w.insFT, `INSERT INTO chunks_fts(rowid, path, name, body) VALUES(?,?,?,?)`},
		{&w.delFT, `DELETE FROM chunks_fts WHERE rowid IN (SELECT id FROM chunks WHERE file_id=?)`},
		{&w.delChunks, `DELETE FROM chunks WHERE file_id=?`},
		{&w.delFile, `DELETE FROM files WHERE id=?`},
	}
	for _, p := range prep {
		st, err := tx.PrepareContext(ctx, p.q)
		if err != nil {
			w.close()
			return nil, fmt.Errorf("index: prepare: %w", err)
		}
		*p.dst = st
	}
	return w, nil
}

func (w *writer) close() {
	for _, st := range []*sql.Stmt{w.touch, w.insFile, w.updFile, w.insChunk, w.insFT, w.delFT, w.delChunks, w.delFile} {
		if st != nil {
			st.Close()
		}
	}
}

func (w *writer) bumpEpoch(ctx context.Context) (int64, error) {
	var e int64
	err := w.tx.QueryRowContext(ctx,
		`UPDATE meta SET value = CAST(value AS INTEGER) + 1 WHERE key='epoch' RETURNING CAST(value AS INTEGER)`).Scan(&e)
	if err != nil {
		return 0, fmt.Errorf("index: bump epoch: %w", err)
	}
	return e, nil
}

func (w *writer) clearChunks(ctx context.Context, fileID int64) error {
	if _, err := w.delFT.ExecContext(ctx, fileID); err != nil {
		return fmt.Errorf("index: delete fts rows: %w", err)
	}
	if _, err := w.delChunks.ExecContext(ctx, fileID); err != nil {
		return fmt.Errorf("index: delete chunks: %w", err)
	}
	return nil
}

func (w *writer) deleteFile(ctx context.Context, fileID int64) error {
	if err := w.clearChunks(ctx, fileID); err != nil {
		return err
	}
	if _, err := w.delFile.ExecContext(ctx, fileID); err != nil {
		return fmt.Errorf("index: delete file: %w", err)
	}
	return nil
}

func (w *writer) putFile(ctx context.Context, r parsed, epoch int64) error {
	lang := symbols.Language(r.stat.Path)
	if r.binary {
		lang = "binary"
	}
	skipped := 0
	if r.binary {
		skipped = 1
	}
	var id int64
	if r.prev != nil {
		id = r.prev.id
		if err := w.clearChunks(ctx, id); err != nil {
			return err
		}
		if _, err := w.updFile.ExecContext(ctx, lang, r.stat.Size, r.stat.MtimeNS, r.hash, skipped, epoch, id); err != nil {
			return fmt.Errorf("index: update %s: %w", r.stat.Path, err)
		}
	} else {
		res, err := w.insFile.ExecContext(ctx, r.stat.Path, lang, r.stat.Size, r.stat.MtimeNS, r.hash, skipped, epoch)
		if err != nil {
			return fmt.Errorf("index: insert %s: %w", r.stat.Path, err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("index: insert %s: %w", r.stat.Path, err)
		}
	}
	pathTerms := strings.Join(symbols.Terms(r.stat.Path), " ")
	for _, c := range r.chunks {
		res, err := w.insChunk.ExecContext(ctx, id, c.Kind, c.Name, c.Start, c.End, c.Signature, c.Tokens)
		if err != nil {
			return fmt.Errorf("index: insert chunk %s: %w", r.stat.Path, err)
		}
		cid, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("index: insert chunk %s: %w", r.stat.Path, err)
		}
		name := strings.Join(symbols.Terms(c.Name), " ")
		body := strings.Join(symbols.Terms(c.Text), " ")
		if _, err := w.insFT.ExecContext(ctx, cid, pathTerms, name, body); err != nil {
			return fmt.Errorf("index: insert fts %s: %w", r.stat.Path, err)
		}
	}
	return nil
}
