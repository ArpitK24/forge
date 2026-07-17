package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteCreatesNewFile writes to a non-existent path and
// verifies the file is created with the expected content.
func TestWriteCreatesNewFile(t *testing.T) {
	tc, dir := readTC(t)
	w := &WriteTool{}
	out := w.Execute(context.Background(),
		decodeReadInput(t, `{"file_path":"out/hello.txt","content":"hi\nthere\n"}`),
		tc)
	if out.IsError {
		t.Fatalf("Write failed: %+v", out)
	}
	// Parent dir was created automatically.
	path := filepath.Join(dir, "out", "hello.txt")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "hi\nthere\n" {
		t.Errorf("file content = %q, want %q", got, "hi\nthere\n")
	}
	if bytes, _ := out.Metadata["bytes_written"].(int); bytes != len("hi\nthere\n") {
		t.Errorf("bytes_written = %d, want %d", bytes, len("hi\nthere\n"))
	}
	if lines, _ := out.Metadata["lines_written"].(int); lines != 2 {
		t.Errorf("lines_written = %d, want 2", lines)
	}
}

// TestWriteOverwritesExisting verifies that Write replaces
// the existing file rather than appending.
func TestWriteOverwritesExisting(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "replace.txt")
	if err := os.WriteFile(path, []byte("OLD CONTENT"), 0644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w := &WriteTool{}
	out := w.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"content":"NEW"}`, path)),
		tc)
	if out.IsError {
		t.Fatalf("Write failed: %+v", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "NEW" {
		t.Errorf("file content = %q, want %q", got, "NEW")
	}
}

// TestWriteEmptyFile writes zero bytes. Line count should be 0.
func TestWriteEmptyFile(t *testing.T) {
	tc, dir := readTC(t)
	w := &WriteTool{}
	out := w.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"content":""}`, filepath.Join(dir, "empty.txt"))),
		tc)
	if out.IsError {
		t.Fatalf("Write failed: %+v", out)
	}
	if lines, _ := out.Metadata["lines_written"].(int); lines != 0 {
		t.Errorf("lines_written = %d, want 0", lines)
	}
	if bytes, _ := out.Metadata["bytes_written"].(int); bytes != 0 {
		t.Errorf("bytes_written = %d, want 0", bytes)
	}
}

// TestWriteNestedDirs creates a deeply nested path and
// verifies all missing directories are created.
func TestWriteNestedDirs(t *testing.T) {
	tc, dir := readTC(t)
	w := &WriteTool{}
	deepPath := filepath.Join(dir, "a", "b", "c", "d", "leaf.txt")
	out := w.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"content":"leaf"}`, deepPath)),
		tc)
	if out.IsError {
		t.Fatalf("Write to nested path failed: %+v", out)
	}
	if _, err := os.Stat(deepPath); err != nil {
		t.Errorf("file not created at nested path: %v", err)
	}
}

// TestWriteInvalidInput covers the JSON-decode path and the
// missing-file_path path.
func TestWriteInvalidInput(t *testing.T) {
	tc, _ := readTC(t)
	w := &WriteTool{}
	out := w.Execute(context.Background(), json.RawMessage(`{not json`), tc)
	if !out.IsError {
		t.Errorf("invalid JSON should be IsError=true; got %+v", out)
	}
	out = w.Execute(context.Background(), json.RawMessage(`{"content":"x"}`), tc)
	if !out.IsError {
		t.Errorf("missing file_path should be IsError=true; got %+v", out)
	}
}

// TestWriteLineCountEdgeCases covers the countLines
// function via the public API. We use a fresh temp dir per
// case so file paths don't collide.
func TestWriteLineCountEdgeCases(t *testing.T) {
	tc, dir := readTC(t)
	w := &WriteTool{}
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"no trailing newline", "a\nb", 2},
		{"trailing newline", "a\nb\n", 2},
		{"single line no nl", "hello", 1},
		{"single line with nl", "hello\n", 1},
		{"just newlines", "\n\n\n", 3},
		{"blank", "", 0},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("case-%d.txt", i))
			out := w.Execute(context.Background(),
				decodeReadInput(t, fmt.Sprintf(`{"file_path":%q,"content":%q}`, path, c.content)),
				tc)
			if out.IsError {
				t.Fatalf("Write failed: %+v", out)
			}
			if got, _ := out.Metadata["lines_written"].(int); got != c.want {
				t.Errorf("lines_written = %d, want %d (content=%q)", got, c.want, c.content)
			}
		})
	}
}
