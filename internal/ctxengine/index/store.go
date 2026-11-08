// Package index is the context engine's persistent code index
// (design doc §5): a SQLite database, opened through the pure-Go
// modernc.org/sqlite driver so Forge still builds with CGO_ENABLED=0,
// holding one row per file, one row per chunk (a symbol, a file's
// top-level code, or a window of a file without symbols) and an FTS5
// table over the chunks' code-aware terms.
//
// The index is incremental: Sync compares each file's mtime and size
// with the stored row, hashes only the files that differ, and reparses
// only the files whose hash changed, replacing their rows in one
// transaction. Refresh does the same for an explicit list of paths and
// is what the engine calls after the agent edits a file, so the next
// turn always sees the agent's own edits.
//
// Milestone 1 stores files and chunks only. References, edges, git
// co-change and feedback (the rest of §5) arrive with later milestones;
// a schema version bump rebuilds the index, which is only a cache.
package index

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // pure-Go SQLite with FTS5
)

// SchemaVersion is bumped whenever the schema or the chunking changes.
// An index with a different version is dropped and rebuilt.
const SchemaVersion = 1

// DefaultPath is where the engine keeps a project's index, relative to
// the project root.
const DefaultPath = ".forge/index/index.db"

var schema = []string{
	`CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS files(
		id INTEGER PRIMARY KEY,
		path TEXT NOT NULL UNIQUE,
		lang TEXT NOT NULL,
		size INTEGER NOT NULL,
		mtime_ns INTEGER NOT NULL,
		hash TEXT NOT NULL,
		skipped INTEGER NOT NULL DEFAULT 0,
		epoch INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS chunks(
		id INTEGER PRIMARY KEY,
		file_id INTEGER NOT NULL,
		kind TEXT NOT NULL,
		name TEXT NOT NULL,
		start_line INTEGER NOT NULL,
		end_line INTEGER NOT NULL,
		signature TEXT NOT NULL,
		tokens INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS chunks_file ON chunks(file_id)`,
	// Contentless FTS5: the terms are only needed for matching and
	// bm25(); chunk text is read back from the file itself.
	`CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
		path, name, body,
		content='', contentless_delete=1,
		tokenize='unicode61 remove_diacritics 0')`,
}

// Store is an open index database. It is safe for concurrent use; all
// access goes through one connection, so writers never see SQLITE_BUSY
// from themselves.
type Store struct {
	db *sql.DB
	mu sync.Mutex // serializes write transactions
}

// Open opens (creating if needed) the index database at path. A stale
// schema version is dropped and recreated.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)"
	return open(dsn)
}

// OpenMemory opens a private in-memory index (eval and tests).
func OpenMemory() (*Store, error) {
	return open("file::memory:")
}

func open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("index: %w", err)
	}
	// One connection: an in-memory database exists per connection, and
	// a single writer keeps WAL semantics simple.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
	if err == nil && v == strconv.Itoa(SchemaVersion) {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !isNoTable(err) {
		return fmt.Errorf("index: read schema version: %w", err)
	}
	// Missing or stale: the index is a cache, rebuild it.
	for _, t := range []string{"chunks_fts", "chunks", "files", "meta"} {
		if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS `+t); err != nil {
			return fmt.Errorf("index: drop %s: %w", t, err)
		}
	}
	for _, q := range schema {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("index: create schema: %w", err)
		}
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO meta(key, value) VALUES('schema_version', ?), ('epoch', '0')`,
		strconv.Itoa(SchemaVersion))
	if err != nil {
		return fmt.Errorf("index: write schema version: %w", err)
	}
	return nil
}

func isNoTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

// Epoch returns the index's write epoch; every write batch bumps it.
func (s *Store) Epoch(ctx context.Context) (int64, error) {
	var v string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='epoch'`).Scan(&v); err != nil {
		return 0, fmt.Errorf("index: epoch: %w", err)
	}
	return strconv.ParseInt(v, 10, 64)
}

// Stats counts the index's rows.
type Stats struct {
	Files  int
	Chunks int
}

// Stats returns row counts (indexed files exclude skipped binaries).
func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM files WHERE skipped=0),
		(SELECT count(*) FROM chunks)`).Scan(&st.Files, &st.Chunks)
	if err != nil {
		return st, fmt.Errorf("index: stats: %w", err)
	}
	return st, nil
}
