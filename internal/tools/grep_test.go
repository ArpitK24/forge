package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepFilesWithMatches is the default-mode happy path.
// Creates a small tree, runs a regex, and verifies the
// files (not content) come back.
func TestGrepFilesWithMatches(t *testing.T) {
	tc, dir := readTC(t)
	for name, content := range map[string]string{
		"a.txt": "alpha\nbeta\ngamma\n",
		"b.txt": "delta\nepsilon\n",
		"c.md":  "alpha in markdown\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"alpha","output_mode":"files_with_matches"}`),
		tc)
	if out.IsError {
		t.Fatalf("Grep failed: %+v", out)
	}
	if !strings.Contains(out.Text, "a.txt") {
		t.Errorf("expected a.txt in output: %q", out.Text)
	}
	if !strings.Contains(out.Text, "c.md") {
		t.Errorf("expected c.md in output: %q", out.Text)
	}
	if strings.Contains(out.Text, "b.txt") {
		t.Errorf("b.txt should not be in output: %q", out.Text)
	}
}

// TestGrepContentMode checks the `{path}:{line}:{content}`
// output. Also verifies case_insensitive.
func TestGrepContentMode(t *testing.T) {
	tc, dir := readTC(t)
	for name, content := range map[string]string{
		"a.txt": "Alpha\nbeta\nGAMMA\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"alpha","output_mode":"content","case_insensitive":true}`),
		tc)
	if out.IsError {
		t.Fatalf("Grep failed: %+v", out)
	}
	// Should find both "Alpha" (line 1) and "GAMMA" does
	// not match; "alpha" is the pattern, "Alpha" matches
	// case-insensitively. Only one match expected.
	if c, _ := out.Metadata["count"].(int); c != 1 {
		t.Errorf("count = %d, want 1", c)
	}
	if !strings.Contains(out.Text, "a.txt:1:Alpha") {
		t.Errorf("expected 'a.txt:1:Alpha' in: %q", out.Text)
	}
}

// TestGrepCountMode verifies the per-file counts.
func TestGrepCountMode(t *testing.T) {
	tc, dir := readTC(t)
	if err := os.WriteFile(filepath.Join(dir, "x.txt"),
		[]byte("hit\nmiss\nhit\nhit\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "y.txt"),
		[]byte("hit\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"hit","output_mode":"count"}`),
		tc)
	if out.IsError {
		t.Fatalf("Grep failed: %+v", out)
	}
	// Expected output (sorted by count desc, then name):
	//   x.txt\t3
	//   y.txt\t1
	if !strings.Contains(out.Text, "x.txt\t3") {
		t.Errorf("expected 'x.txt\\t3' in: %q", out.Text)
	}
	if !strings.Contains(out.Text, "y.txt\t1") {
		t.Errorf("expected 'y.txt\\t1' in: %q", out.Text)
	}
}

// TestGrepSkipsHiddenAndIgnoredDirs verifies the
// directory-skip rules: `.git`, `node_modules`, hidden
// directories (starting with `.`) should all be skipped.
func TestGrepSkipsHiddenAndIgnoredDirs(t *testing.T) {
	tc, dir := readTC(t)
	// Files we should NOT see in results.
	for _, p := range []string{
		filepath.Join(dir, ".git", "config"),
		filepath.Join(dir, "node_modules", "lib", "x.js"),
		filepath.Join(dir, ".hidden", "secret.txt"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("MATCHED"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// One file we DO want to see.
	if err := os.WriteFile(filepath.Join(dir, "visible.txt"),
		[]byte("MATCHED"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"MATCHED","output_mode":"files_with_matches"}`),
		tc)
	if out.IsError {
		t.Fatalf("Grep failed: %+v", out)
	}
	if !strings.Contains(out.Text, "visible.txt") {
		t.Errorf("expected visible.txt in: %q", out.Text)
	}
	for _, banned := range []string{".git", "node_modules", ".hidden"} {
		if strings.Contains(out.Text, banned) {
			t.Errorf("output should not mention %s: %q", banned, out.Text)
		}
	}
}

// TestGrepHeadLimitAndOffset verifies pagination.
func TestGrepHeadLimitAndOffset(t *testing.T) {
	tc, dir := readTC(t)
	var b strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "match line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(b.String()), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"match","output_mode":"content","head_limit":3,"offset":2}`),
		tc)
	if out.IsError {
		t.Fatalf("Grep failed: %+v", out)
	}
	// Should have 3 matches starting at index 2 (lines
	// 3,4,5 → "match line 2", "match line 3", "match
	// line 4").
	if c, _ := out.Metadata["count"].(int); c != 3 {
		t.Errorf("count = %d, want 3", c)
	}
	if !strings.Contains(out.Text, "match line 2") {
		t.Errorf("expected 'match line 2' in: %q", out.Text)
	}
	if !strings.Contains(out.Text, "match line 4") {
		t.Errorf("expected 'match line 4' in: %q", out.Text)
	}
	if strings.Contains(out.Text, "match line 1") {
		t.Errorf("'match line 1' should be skipped (offset=2): %q", out.Text)
	}
	if trunc, _ := out.Metadata["truncated"].(bool); !trunc {
		t.Errorf("truncated = false, want true")
	}
}

// TestGrepTypeFilter verifies the `type` shortcut.
func TestGrepTypeFilter(t *testing.T) {
	tc, dir := readTC(t)
	for name, content := range map[string]string{
		"a.go":         "package main",
		"b.txt":        "package main", // same content, wrong ext
		"c.md":         "package main",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"package","output_mode":"files_with_matches","type":"go"}`),
		tc)
	if out.IsError {
		t.Fatalf("Grep failed: %+v", out)
	}
	if !strings.Contains(out.Text, "a.go") {
		t.Errorf("expected a.go in: %q", out.Text)
	}
	for _, banned := range []string{"b.txt", "c.md"} {
		if strings.Contains(out.Text, banned) {
			t.Errorf("%s should be filtered out by type=go: %q", banned, out.Text)
		}
	}
}

// TestGrepUnknownType covers the error for an unknown type.
func TestGrepUnknownType(t *testing.T) {
	tc, _ := readTC(t)
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"x","type":"pascal"}`),
		tc)
	if !out.IsError {
		t.Errorf("Grep with unknown type should be IsError=true; got %+v", out)
	}
}

// TestGrepInvalidRegex covers the bad-pattern path.
func TestGrepInvalidRegex(t *testing.T) {
	tc, _ := readTC(t)
	g := &GrepTool{}
	// Unbalanced paren is the canonical "always invalid"
	// RE2 pattern.
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"("}`),
		tc)
	if !out.IsError {
		t.Errorf("Grep with invalid regex should be IsError=true; got %+v", out)
	}
}

// TestGrepNoMatches covers the empty-result path.
func TestGrepNoMatches(t *testing.T) {
	tc, dir := readTC(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"),
		[]byte("nothing here\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"absolutely-not-here"}`),
		tc)
	if !out.IsError {
		t.Errorf("Grep with no matches should be IsError=true; got %+v", out)
	}
}

// TestGrepPathDoesNotExist covers the missing-path error.
func TestGrepPathDoesNotExist(t *testing.T) {
	tc, dir := readTC(t)
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"pattern":"x","path":%q}`, filepath.Join(dir, "nope"))),
		tc)
	if !out.IsError {
		t.Errorf("Grep with missing path should be IsError=true; got %+v", out)
	}
}

// TestGrepSkipsBinaryFile verifies that a NUL byte in the
// first 8KiB causes the file to be skipped.
func TestGrepSkipsBinaryFile(t *testing.T) {
	tc, dir := readTC(t)
	// 4 KiB of NUL; first 1k has "MATCHED" embedded but
	// the NUL byte trumps it.
	blob := make([]byte, 4096)
	copy(blob, []byte("MATCHED"))
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), blob, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"),
		[]byte("MATCHED here\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	g := &GrepTool{}
	out := g.Execute(context.Background(),
		decodeReadInput(t, `{"pattern":"MATCHED","output_mode":"files_with_matches"}`),
		tc)
	if out.IsError {
		t.Fatalf("Grep failed: %+v", out)
	}
	if !strings.Contains(out.Text, "ok.txt") {
		t.Errorf("expected ok.txt in: %q", out.Text)
	}
	if strings.Contains(out.Text, "blob.bin") {
		t.Errorf("blob.bin should be skipped (binary): %q", out.Text)
	}
}

// TestGrepInvalidInput covers JSON-decode and missing-field
// paths.
func TestGrepInvalidInput(t *testing.T) {
	tc, _ := readTC(t)
	g := &GrepTool{}
	out := g.Execute(context.Background(), []byte(`{not json`), tc)
	if !out.IsError {
		t.Errorf("invalid JSON should be IsError=true; got %+v", out)
	}
	out = g.Execute(context.Background(), []byte(`{}`), tc)
	if !out.IsError {
		t.Errorf("missing pattern should be IsError=true; got %+v", out)
	}
}
