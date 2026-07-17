package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEditReplacesUniqueMatch covers the happy path:
// old_string appears exactly once and is replaced.
func TestEditReplacesUniqueMatch(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world\nsecond line\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &EditTool{}
	out := e.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"old_string":"hello world","new_string":"goodbye"}`, path)),
		tc)
	if out.IsError {
		t.Fatalf("Edit failed: %+v", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "goodbye\nsecond line\n" {
		t.Errorf("file content = %q, want %q", got, "goodbye\nsecond line\n")
	}
	if r, _ := out.Metadata["replacements"].(int); r != 1 {
		t.Errorf("replacements = %d, want 1", r)
	}
}

// TestEditAmbiguousWithoutReplaceAll covers the
// disambiguation error: old_string appears 3 times, no
// replace_all → error. The model should then either pass
// more context or set replace_all=true.
func TestEditAmbiguousWithoutReplaceAll(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("aaa aaa aaa\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &EditTool{}
	out := e.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"old_string":"aaa","new_string":"b"}`, path)),
		tc)
	if !out.IsError {
		t.Errorf("ambiguous Edit should be IsError=true; got %+v", out)
	}
	if !strings.Contains(strings.ToLower(out.Text), "ambiguous") {
		t.Errorf("error should mention 'ambiguous'; got %q", out.Text)
	}
	// File must not have been modified.
	got, _ := os.ReadFile(path)
	if string(got) != "aaa aaa aaa\n" {
		t.Errorf("file was modified despite error: %q", got)
	}
}

// TestEditReplaceAll handles the multi-occurrence case
// when the model explicitly opts in.
func TestEditReplaceAll(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("aaa aaa aaa\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &EditTool{}
	out := e.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"old_string":"aaa","new_string":"b","replace_all":true}`, path)),
		tc)
	if out.IsError {
		t.Fatalf("Edit failed: %+v", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "b b b\n" {
		t.Errorf("file content = %q, want %q", got, "b b b\n")
	}
	if r, _ := out.Metadata["replacements"].(int); r != 3 {
		t.Errorf("replacements = %d, want 3", r)
	}
}

// TestEditNotFound covers the "old_string not in file" path.
func TestEditNotFound(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &EditTool{}
	out := e.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"old_string":"goodbye","new_string":"x"}`, path)),
		tc)
	if !out.IsError {
		t.Errorf("Edit with no match should be IsError=true; got %+v", out)
	}
	if !strings.Contains(strings.ToLower(out.Text), "not found") {
		t.Errorf("error should mention 'not found'; got %q", out.Text)
	}
}

// TestEditSameStringIsError covers the no-op guard:
// old == new is rejected because the model shouldn't be
// making that call (and if it is, the error tells it so
// without modifying the file).
func TestEditSameStringIsError(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &EditTool{}
	out := e.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"old_string":"hello","new_string":"hello"}`, path)),
		tc)
	if !out.IsError {
		t.Errorf("Edit with old==new should be IsError=true; got %+v", out)
	}
	// File must not have been modified.
	got, _ := os.ReadFile(path)
	if string(got) != "hello\n" {
		t.Errorf("file was modified despite no-op: %q", got)
	}
}

// TestEditEmptyOldStringIsError covers the "empty needle
// matches every position" guard. An empty old_string is
// undefined behavior in the spec — we reject it explicitly
// rather than running with a degenerate count.
func TestEditEmptyOldStringIsError(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("anything"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &EditTool{}
	out := e.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"old_string":"","new_string":"x"}`, path)),
		tc)
	if !out.IsError {
		t.Errorf("Edit with empty old_string should be IsError=true; got %+v", out)
	}
}

// TestEditMultilineReplace covers the multi-line case:
// old_string spans multiple lines, and so does new_string.
func TestEditMultilineReplace(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("function foo() {\n  return 1\n}\n"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	e := &EditTool{}
	out := e.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(
			`{"file_path":%q,"old_string":"function foo() {\n  return 1\n}","new_string":"function bar() {\n  return 2\n}"}`, path)),
		tc)
	if out.IsError {
		t.Fatalf("Edit failed: %+v", out)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "function bar() {\n  return 2\n}\n" {
		t.Errorf("file content = %q", got)
	}
}

// TestEditFileNotFound covers the missing-file case. Should
// be a clean error, not a panic.
func TestEditFileNotFound(t *testing.T) {
	tc, dir := readTC(t)
	e := &EditTool{}
	out := e.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"old_string":"x","new_string":"y"}`,
			filepath.Join(dir, "missing.txt"))),
		tc)
	if !out.IsError {
		t.Errorf("Edit on missing file should be IsError=true; got %+v", out)
	}
}

// TestEditInvalidInput covers JSON-decode and missing-field
// paths.
func TestEditInvalidInput(t *testing.T) {
	tc, _ := readTC(t)
	e := &EditTool{}
	out := e.Execute(context.Background(), json.RawMessage(`{not json`), tc)
	if !out.IsError {
		t.Errorf("invalid JSON should be IsError=true; got %+v", out)
	}
	out = e.Execute(context.Background(), json.RawMessage(`{"old_string":"a","new_string":"b"}`), tc)
	if !out.IsError {
		t.Errorf("missing file_path should be IsError=true; got %+v", out)
	}
}
