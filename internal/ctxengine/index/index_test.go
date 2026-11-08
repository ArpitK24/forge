package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const goSrc = `package demo

import "strings"

// MaxRetries caps retries.
const MaxRetries = 3

// Server serves requests.
type Server struct {
	name string
}

// ParseHTTPRequest parses
// a raw request.
func ParseHTTPRequest(raw string,
	strict bool) (string, error) {
	return strings.TrimSpace(raw), nil
}

func (s *Server) closeAll() {}
`

const pySrc = `import os

class Model:
    """A model."""
    name = "m"

    def save(self, force=False):
        return os.path.join("a", "b")

    @property
    def pk(self):
        return 1

def helper_fn(x):
    return x
`

func TestChunksGo(t *testing.T) {
	cs := Chunks("demo/server.go", []byte(goSrc))
	var got []string
	for _, c := range cs {
		got = append(got, c.Kind+":"+c.Name+":"+c.Signature)
	}
	want := []string{
		"header::package demo",
		"type:Server:type Server struct {",
		"func:ParseHTTPRequest:func ParseHTTPRequest(raw string, strict bool) (string, error)",
		"method:Server.closeAll:func (s *Server) closeAll()",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	h := cs[0]
	if !strings.Contains(h.Text, "MaxRetries = 3") || strings.Contains(h.Text, "closeAll") {
		t.Fatalf("header text should hold package-level code only:\n%s", h.Text)
	}
	if cs[2].SymbolID("demo/server.go") != "demo/server.go::ParseHTTPRequest" {
		t.Fatalf("SymbolID = %q", cs[2].SymbolID("demo/server.go"))
	}
	if cs[0].SymbolID("x") != "" || cs[0].IsSymbol() {
		t.Fatal("header must not be a symbol")
	}
}

func TestChunksPythonClassElidesMethods(t *testing.T) {
	cs := Chunks("m.py", []byte(pySrc))
	byName := map[string]Chunk{}
	for _, c := range cs {
		byName[c.Kind+":"+c.Name] = c
	}
	cls, ok := byName["class:Model"]
	if !ok {
		t.Fatalf("no class chunk in %+v", cs)
	}
	if strings.Contains(cls.Text, "os.path.join") || !strings.Contains(cls.Text, `name = "m"`) || !strings.Contains(cls.Text, "    ...") {
		t.Fatalf("class text should elide its methods:\n%s", cls.Text)
	}
	if m := byName["method:Model.pk"]; m.Signature != "def pk(self):" {
		t.Fatalf("decorated method signature = %q", m.Signature)
	}
	if _, ok := byName["func:helper_fn"]; !ok {
		t.Fatalf("missing helper_fn: %+v", byName)
	}
	if h := byName["header:"]; h.Text != "import os\n\n...\n\n...\n" {
		t.Fatalf("header text = %q", h.Text)
	}
}

func TestChunksWindows(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 130; i++ {
		b.WriteString("line\n")
	}
	cs := Chunks("README.md", []byte(b.String()))
	if len(cs) != 3 || cs[1].Start != 61 || cs[1].End != 120 || cs[2].End != 130 || cs[0].Kind != KindWindow {
		t.Fatalf("windows = %+v", cs)
	}
	if Chunks("empty.md", nil) != nil {
		t.Fatal("empty file should have no chunks")
	}
}

func mustOpen(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func names(hs []Hit) []string {
	var out []string
	for _, h := range hs {
		if h.Name != "" {
			out = append(out, h.SymbolID())
		}
	}
	return out
}

func TestSearchSplitsIdentifiers(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)
	src := MapSource{
		"demo/server.go": []byte(goSrc),
		"m.py":           []byte(pySrc),
		"logo.png":       []byte("\x89PNG\x00\x00"),
	}
	st, err := s.Sync(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != 3 || st.Skipped != 1 {
		t.Fatalf("stats = %+v", st)
	}
	hits, err := s.Search(ctx, []string{"http", "request"}, DefaultSearchOptions)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(hits); len(got) == 0 || got[0] != "demo/server.go::ParseHTTPRequest" {
		t.Fatalf("hits = %v", got)
	}
	// Compound terms are indexed too ("helper_fn" -> "helper_fn").
	hits, err = s.Search(ctx, []string{"helper_fn"}, DefaultSearchOptions)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(hits); !reflect.DeepEqual(got, []string{"m.py::helper_fn"}) {
		t.Fatalf("hits = %v", got)
	}
	// FTS5 operators in the query are quoted, never parsed.
	if _, err := s.Search(ctx, []string{"and", "not", "near", `x"y`}, DefaultSearchOptions); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search(ctx, nil, DefaultSearchOptions); hits != nil {
		t.Fatal("empty query should return nil")
	}
	st2, _ := s.Stats(ctx)
	if st2.Files != 2 {
		t.Fatalf("Stats = %+v", st2)
	}
	ps, _ := s.Paths(ctx)
	if !reflect.DeepEqual(ps, []string{"demo/server.go", "m.py"}) {
		t.Fatalf("Paths = %v", ps)
	}
}

func write(t *testing.T, root, rel, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestSyncIsIncremental(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t0 := time.Unix(1_700_000_000, 0)
	write(t, root, "a/a.go", "package a\n\nfunc Alpha() {}\n", t0)
	write(t, root, "b/b.go", "package b\n\nfunc Beta() {}\n", t0)
	write(t, root, "node_modules/x.js", "function x() {}\n", t0)
	write(t, root, "gen/skip.go", "package gen\n\nfunc Generated() {}\n", t0)
	write(t, root, ".forgeignore", "gen/\n", t0)
	src := NewDirSource(root, func(p string, _ int64) bool { return strings.HasSuffix(p, ".go") })

	dbPath := filepath.Join(root, DefaultPath)
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Sync(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != 2 || st.Listed != 2 {
		t.Fatalf("cold sync = %+v", st)
	}
	e1, _ := s.Epoch(ctx)

	st, _ = s.Sync(ctx, src)
	if st.Unchanged != 2 || st.Changed() {
		t.Fatalf("warm sync = %+v", st)
	}
	if e, _ := s.Epoch(ctx); e != e1 {
		t.Fatalf("epoch moved on a no-op sync: %d -> %d", e1, e)
	}

	// Same content, new mtime: hashed but not reparsed.
	write(t, root, "a/a.go", "package a\n\nfunc Alpha() {}\n", t0.Add(time.Second))
	st, _ = s.Sync(ctx, src)
	if st.Touched != 1 || st.Changed() {
		t.Fatalf("touch sync = %+v", st)
	}

	// An edit seen through Refresh is searchable at once.
	write(t, root, "a/a.go", "package a\n\nfunc Alpha() {}\n\nfunc Gamma() {}\n", t0.Add(2*time.Second))
	st, err = s.Refresh(ctx, src, []string{"a/a.go", "a/a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Updated != 1 {
		t.Fatalf("refresh = %+v", st)
	}
	hits, _ := s.Search(ctx, []string{"gamma"}, DefaultSearchOptions)
	if got := names(hits); !reflect.DeepEqual(got, []string{"a/a.go::Gamma"}) {
		t.Fatalf("after refresh: %v", got)
	}
	if e, _ := s.Epoch(ctx); e <= e1 {
		t.Fatal("epoch should grow on change")
	}

	// Deletion through Refresh removes rows and FTS entries.
	if err := os.Remove(filepath.Join(root, "b", "b.go")); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Refresh(ctx, src, []string{"b/b.go", "never/existed.go"})
	if st.Removed != 1 {
		t.Fatalf("delete refresh = %+v", st)
	}
	if hits, _ := s.Search(ctx, []string{"beta"}, DefaultSearchOptions); len(hits) != 0 {
		t.Fatalf("deleted file still matches: %v", hits)
	}
	// Full sync notices deletions too.
	write(t, root, "c.go", "package c\n", t0)
	_ = os.Remove(filepath.Join(root, "a", "a.go"))
	st, _ = s.Sync(ctx, src)
	if st.Removed != 1 || st.Added != 1 {
		t.Fatalf("sync after delete = %+v", st)
	}
	s.Close()

	// Reopen: the index persists and stays warm.
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	st, _ = s2.Sync(ctx, src)
	if st.Unchanged != 1 || st.Changed() {
		t.Fatalf("reopened sync = %+v", st)
	}
}

func TestSchemaVersionRebuilds(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "x", "index.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, MapSource{"a.go": []byte("package a\nfunc A() {}\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE meta SET value='0' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if st, _ := s.Stats(ctx); st.Files != 0 {
		t.Fatalf("stale index should be rebuilt empty, got %+v", st)
	}
}

func TestMapSourceDetectsSameSizeEdits(t *testing.T) {
	ctx := context.Background()
	s := mustOpen(t)
	src := MapSource{"a.go": []byte("package a\nfunc Aaaa() {}\n")}
	if _, err := s.Sync(ctx, src); err != nil {
		t.Fatal(err)
	}
	src["a.go"] = []byte("package a\nfunc Bbbb() {}\n")
	st, err := s.Sync(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if st.Updated != 1 {
		t.Fatalf("same-size edit missed: %+v", st)
	}
	chunks, _ := s.FileChunks(ctx, "a.go")
	if len(chunks) != 2 || chunks[1].Name != "Bbbb" {
		t.Fatalf("FileChunks = %+v", chunks)
	}
}

func TestDirSourceHonoursGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	now := time.Now()
	write(t, root, ".gitignore", "build/\n*.log\n", now)
	write(t, root, "main.go", "package main\n", now)
	write(t, root, "build/out.go", "package build\n", now)
	write(t, root, "debug.log", "x\n", now)
	write(t, root, "new/untracked.go", "package n\n", now)
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	src := NewDirSource(root, nil)
	list, err := src.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range list {
		got = append(got, f.Path)
	}
	want := []string{".gitignore", "main.go", "new/untracked.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
}

func TestDirSourceWalkWithoutGit(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	write(t, root, "main.go", "package main\n", now)
	write(t, root, ".hidden/x.go", "package x\n", now)
	write(t, root, "vendor/v.go", "package v\n", now)
	list, err := NewDirSource(root, nil).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Path != "main.go" {
		t.Fatalf("List = %+v", list)
	}
}
