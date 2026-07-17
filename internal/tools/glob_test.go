package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGlobBasicMatch creates 3 files, runs "*.txt", and
// verifies both .txt files come back and the .md is
// excluded.
func TestGlobBasicMatch(t *testing.T) {
	tc, dir := readTC(t)
	for name, content := range map[string]string{
		"a.txt": "a",
		"b.txt": "b",
		"c.md":  "c",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	g := &GlobTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"*.txt"}`),
		tc)
	if out.IsError {
		t.Fatalf("Glob failed: %+v", out)
	}
	if c, _ := out.Metadata["count"].(int); c != 2 {
		t.Errorf("count = %d, want 2", c)
	}
	lines := strings.Split(strings.TrimSpace(out.Text), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), out.Text)
	}
	found := map[string]bool{}
	for _, l := range lines {
		found[l] = true
	}
	if !found["a.txt"] || !found["b.txt"] {
		t.Errorf("expected a.txt and b.txt, got %v", found)
	}
	if found["c.md"] {
		t.Errorf("c.md should not match *.txt; got %v", found)
	}
}

// TestGlobDoubleStar walks subdirectories.
func TestGlobDoubleStar(t *testing.T) {
	tc, dir := readTC(t)
	for _, p := range []string{
		filepath.Join(dir, "x", "a.go"),
		filepath.Join(dir, "x", "y", "b.go"),
		filepath.Join(dir, "c.go"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("package x"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	g := &GlobTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"**/*.go"}`),
		tc)
	if out.IsError {
		t.Fatalf("Glob failed: %+v", out)
	}
	if c, _ := out.Metadata["count"].(int); c != 3 {
		t.Errorf("count = %d, want 3 (output:\n%s)", c, out.Text)
	}
	if !strings.Contains(out.Text, "x/a.go") {
		t.Errorf("missing x/a.go in:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "x/y/b.go") {
		t.Errorf("missing x/y/b.go in:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "c.go") {
		t.Errorf("missing c.go in:\n%s", out.Text)
	}
}

// TestGlobSortedByMtime verifies the order: the most
// recently modified file is listed first. We touch one
// file into the future to make the order deterministic.
func TestGlobSortedByMtime(t *testing.T) {
	tc, dir := readTC(t)
	old := filepath.Join(dir, "old.txt")
	mid := filepath.Join(dir, "mid.txt")
	newer := filepath.Join(dir, "new.txt")
	for _, p := range []string{old, mid, newer} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(mid, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	g := &GlobTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"*.txt"}`),
		tc)
	if out.IsError {
		t.Fatalf("Glob failed: %+v", out)
	}
	lines := strings.Split(strings.TrimSpace(out.Text), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), out.Text)
	}
	if lines[0] != "mid.txt" {
		t.Errorf("first line = %q, want mid.txt (most recent)", lines[0])
	}
}

// TestGlobNoMatches covers the "no files matched" path.
// Should be IsError=true (so the model knows nothing was
// found; a silent empty success would be ambiguous).
func TestGlobNoMatches(t *testing.T) {
	tc, _ := readTC(t)
	g := &GlobTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"*.nonexistent"}`),
		tc)
	if !out.IsError {
		t.Errorf("Glob with no matches should be IsError=true; got %+v", out)
	}
	if !strings.Contains(strings.ToLower(out.Text), "no files") {
		t.Errorf("error should mention 'no files'; got %q", out.Text)
	}
}

// TestGlobBasePathExplicit covers the case where the model
// passes a base_path different from the working dir.
func TestGlobBasePathExplicit(t *testing.T) {
	tc, _ := readTC(t)
	sub := t.TempDir()
	if err := os.WriteFile(filepath.Join(sub, "z.txt"), []byte("z"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := &GlobTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"pattern":"*.txt","base_path":%q}`, sub)),
		tc)
	if out.IsError {
		t.Fatalf("Glob failed: %+v", out)
	}
	if !strings.Contains(out.Text, "z.txt") {
		t.Errorf("expected z.txt in output: %q", out.Text)
	}
}

// TestGlobBasePathDoesNotExist covers the missing-dir error.
func TestGlobBasePathDoesNotExist(t *testing.T) {
	tc, dir := readTC(t)
	g := &GlobTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"pattern":"*.txt","base_path":%q}`, filepath.Join(dir, "nope"))),
		tc)
	if !out.IsError {
		t.Errorf("Glob with missing base_path should be IsError=true; got %+v", out)
	}
}

// TestGlobInvalidInput covers JSON-decode and missing-field
// paths.
func TestGlobInvalidInput(t *testing.T) {
	tc, _ := readTC(t)
	g := &GlobTool{}
	out := g.Execute(context.Background(), []byte(`{not json`), tc)
	if !out.IsError {
		t.Errorf("invalid JSON should be IsError=true; got %+v", out)
	}
	out = g.Execute(context.Background(), []byte(`{}`), tc)
	if !out.IsError {
		t.Errorf("missing pattern should be IsError=true; got %+v", out)
	}
}
