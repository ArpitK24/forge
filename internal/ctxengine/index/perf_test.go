package index_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/ctxengine/index"
	"github.com/ArpitK24/forge/internal/ctxengine/retrieve"
)

// repoRoot is this checkout's root (three levels above the package).
func repoRoot(t testing.TB) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Skip("not running inside the forge checkout")
	}
	return root
}

func coldIndex(t testing.TB, root string) (*index.Store, *index.DirSource, time.Duration, index.SyncStats) {
	t.Helper()
	s, err := index.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	src := index.NewDirSource(root, retrieve.Indexable)
	start := time.Now()
	st, err := s.Sync(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	return s, src, time.Since(start), st
}

// TestIndexPerf measures the Milestone 1 exit criteria on this repo:
// cold index time and single-file reindex p95. It only reports unless
// FORGE_INDEX_PERF=1, which also enforces the targets (5 s and 50 ms);
// shared CI runners are too noisy to gate every PR on wall time.
func TestIndexPerf(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := repoRoot(t)
	s, _, cold, st := coldIndex(t, root)
	defer s.Close()
	stats, _ := s.Stats(context.Background())

	// Reindex the largest Go files in turn, each after a real edit
	// (append a comment, so the hash changes and the file is reparsed).
	paths, _ := s.Paths(context.Background())
	type sized struct {
		p string
		n int64
	}
	var goFiles []sized
	for _, p := range paths {
		if filepath.Ext(p) == ".go" {
			fi, err := os.Stat(filepath.Join(root, p))
			if err == nil {
				goFiles = append(goFiles, sized{p, fi.Size()})
			}
		}
	}
	sort.Slice(goFiles, func(i, j int) bool { return goFiles[i].n > goFiles[j].n })
	goFiles = goFiles[:min(len(goFiles), 40)]

	// Edit in-memory copies so the checkout is never modified.
	cp := index.MapSource{}
	for _, f := range goFiles {
		b, err := os.ReadFile(filepath.Join(root, f.p))
		if err != nil {
			t.Fatal(err)
		}
		cp[f.p] = b
	}
	var lat []time.Duration
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		for _, f := range goFiles {
			cp[f.p] = append(cp[f.p], []byte(fmt.Sprintf("\n// edit %d\n", round))...)
			start := time.Now()
			r, err := s.Refresh(ctx, cp, []string{f.p})
			if err != nil {
				t.Fatal(err)
			}
			lat = append(lat, time.Since(start))
			if r.Updated != 1 {
				t.Fatalf("refresh %s: %+v", f.p, r)
			}
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	p50 := lat[len(lat)/2]
	p95 := lat[(len(lat)*95+99)/100-1]
	t.Logf("cold index: %v for %d files / %d chunks (%+v), GOMAXPROCS=%d",
		cold.Round(time.Millisecond), stats.Files, stats.Chunks, st, runtime.GOMAXPROCS(0))
	t.Logf("single-file reindex over %d edits of the %d largest Go files: p50 %v, p95 %v, max %v",
		len(lat), len(goFiles), p50.Round(10*time.Microsecond), p95.Round(10*time.Microsecond), lat[len(lat)-1].Round(10*time.Microsecond))
	if os.Getenv("FORGE_INDEX_PERF") == "1" {
		if cold > 5*time.Second {
			t.Errorf("cold index %v exceeds 5 s", cold)
		}
		if p95 > 50*time.Millisecond {
			t.Errorf("reindex p95 %v exceeds 50 ms", p95)
		}
	}
}

// BenchmarkColdIndex indexes this repository from scratch.
func BenchmarkColdIndex(b *testing.B) {
	root := repoRoot(b)
	for b.Loop() {
		s, _, _, _ := coldIndex(b, root)
		s.Close()
	}
}

// BenchmarkReindexFile edits and reindexes one large Go file.
func BenchmarkReindexFile(b *testing.B) {
	root := repoRoot(b)
	s, _, _, _ := coldIndex(b, root)
	defer s.Close()
	const p = "internal/query/loop.go"
	content, err := os.ReadFile(filepath.Join(root, p))
	if err != nil {
		b.Skip(err)
	}
	src := index.MapSource{p: content}
	ctx := context.Background()
	i := 0
	for b.Loop() {
		i++
		src[p] = append(content, []byte(fmt.Sprintf("\n// %d\n", i))...)
		if _, err := s.Refresh(ctx, src, []string{p}); err != nil {
			b.Fatal(err)
		}
	}
}
