package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// readTC is a minimal ToolContext for Read/Write/Edit tests.
// All five tools share this helper so test setup stays short.
func readTC(t *testing.T) (*ToolContext, string) {
	t.Helper()
	dir := t.TempDir()
	return &ToolContext{
		WorkingDir: dir,
		Permission: &core.AutoPermissionHandler{Mode: core.PermissionBypassPermissions},
	}, dir
}

func decodeReadInput(t *testing.T, s string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("invalid JSON in test: %q", s)
	}
	return json.RawMessage(s)
}

// TestReadBasicFile writes a small file and reads it back.
// Verifies line numbering, tab separator, and that the
// returned text contains every input line.
func TestReadBasicFile(t *testing.T) {
	tc, dir := readTC(t)
	content := "first line\nsecond line\nthird line\n"
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	r := &ReadTool{}
	out := r.Execute(context.Background(), decodeReadInput(t, `{"file_path":"hello.txt"}`), tc)
	if out.IsError {
		t.Fatalf("Read failed: %+v", out)
	}
	if !strings.Contains(out.Text, "1\tfirst line") {
		t.Errorf("missing 1\tfirst line in:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "2\tsecond line") {
		t.Errorf("missing 2\tsecond line in:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "3\tthird line") {
		t.Errorf("missing 3\tthird line in:\n%s", out.Text)
	}
	if total, _ := out.Metadata["total_lines"].(int); total != 3 {
		t.Errorf("total_lines = %d, want 3", total)
	}
	if truncated, _ := out.Metadata["truncated"].(bool); truncated {
		t.Errorf("truncated = true, want false")
	}
}

// TestReadOffsetAndLimit exercises offset + limit together.
// Per spec §3.2, offset is 1-based. The fixture has 10
// lines; we ask for offset=3 limit=4 and verify we get
// lines 4..7 (1-based), truncated=true.
//
// (Why offset=3, not 4: offset=3 means "start AT line 3",
// so the first line returned is 3, then 4, 5, 6 — only 3
// lines because limit=4 truncates? No: lines 3,4,5,6 is 4
// lines, with 6 as the last. We assert lines 3..6.)
func TestReadOffsetAndLimit(t *testing.T) {
	tc, dir := readTC(t)
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		b.WriteString("line\n")
	}
	path := filepath.Join(dir, "ten.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	r := &ReadTool{}
	out := r.Execute(context.Background(), decodeReadInput(t, `{"file_path":"ten.txt","offset":3,"limit":4}`), tc)
	if out.IsError {
		t.Fatalf("Read failed: %+v", out)
	}
	if !strings.Contains(out.Text, "3\tline") {
		t.Errorf("expected line 3 in output:\n%s", out.Text)
	}
	if !strings.Contains(out.Text, "6\tline") {
		t.Errorf("expected line 6 in output:\n%s", out.Text)
	}
	// Should NOT contain lines before 3 or after 6.
	if strings.Contains(out.Text, "2\tline") {
		t.Errorf("output should not include line 2 (offset=3 1-based starts at 3):\n%s", out.Text)
	}
	if strings.Contains(out.Text, "7\tline") {
		t.Errorf("output should not include line 7 (limit=4 caps at 6):\n%s", out.Text)
	}
	if truncated, _ := out.Metadata["truncated"].(bool); !truncated {
		t.Errorf("truncated = false, want true")
	}
	if returned, _ := out.Metadata["returned_lines"].(int); returned != 4 {
		t.Errorf("returned_lines = %d, want 4", returned)
	}
}

// TestReadFileNotFound verifies the error path for a missing
// file. Should be IsError=true with a message that mentions
// "not found" so the model can self-diagnose.
func TestReadFileNotFound(t *testing.T) {
	tc, dir := readTC(t)
	r := &ReadTool{}
	missing := filepath.Join(dir, "nope.txt")
	out := r.Execute(context.Background(),
		decodeReadInput(t, fmt.Sprintf(`{"file_path":%q}`, missing)),
		tc)
	if !out.IsError {
		t.Errorf("Read on missing file should be IsError=true; got %+v", out)
	}
	if !strings.Contains(strings.ToLower(out.Text), "not found") {
		t.Errorf("error text should mention 'not found'; got %q", out.Text)
	}
}

// TestReadDirectoryIsError verifies the directory guard.
// Reading a directory should error cleanly (model should
// use Glob/Grep instead).
func TestReadDirectoryIsError(t *testing.T) {
	tc, _ := readTC(t)
	r := &ReadTool{}
	out := r.Execute(context.Background(), decodeReadInput(t, `{"file_path":"."}`), tc)
	if !out.IsError {
		t.Errorf("Read on directory should be IsError=true; got %+v", out)
	}
	if !strings.Contains(strings.ToLower(out.Text), "directory") {
		t.Errorf("error text should mention 'directory'; got %q", out.Text)
	}
}

// TestReadBinaryFileIsError writes a file with a NUL byte
// and verifies Read rejects it as binary. This is the safety
// guard that prevents the model from corrupting a binary
// blob with an Edit call.
func TestReadBinaryFileIsError(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "binary.bin")
	// 4 KiB of NUL bytes; the sniff window is 8 KiB so we
	// need at least 1 NUL inside the first 8 KiB.
	blob := make([]byte, 4096)
	if err := os.WriteFile(path, blob, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	r := &ReadTool{}
	out := r.Execute(context.Background(), decodeReadInput(t, `{"file_path":"binary.bin"}`), tc)
	if !out.IsError {
		t.Errorf("Read on binary file should be IsError=true; got %+v", out)
	}
	if !strings.Contains(strings.ToLower(out.Text), "binary") {
		t.Errorf("error text should mention 'binary'; got %q", out.Text)
	}
}

// TestReadEmptyFile verifies the edge case of a zero-byte
// file. Should succeed with 0 lines.
func TestReadEmptyFile(t *testing.T) {
	tc, dir := readTC(t)
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	r := &ReadTool{}
	out := r.Execute(context.Background(), decodeReadInput(t, `{"file_path":"empty.txt"}`), tc)
	if out.IsError {
		t.Fatalf("Read on empty file failed: %+v", out)
	}
	if total, _ := out.Metadata["total_lines"].(int); total != 0 {
		t.Errorf("total_lines = %d, want 0", total)
	}
}

// TestReadInvalidInput verifies the JSON-decode path and
// the missing-file_path path.
func TestReadInvalidInput(t *testing.T) {
	tc, _ := readTC(t)
	r := &ReadTool{}
	out := r.Execute(context.Background(), json.RawMessage(`{not json`), tc)
	if !out.IsError {
		t.Errorf("invalid JSON should be IsError=true; got %+v", out)
	}
	out = r.Execute(context.Background(), json.RawMessage(`{}`), tc)
	if !out.IsError {
		t.Errorf("missing file_path should be IsError=true; got %+v", out)
	}
}

// TestReadImagePDFStub verifies the spec §3.2 mandate: "for
// images/PDFs, return a stub message rather than raw bytes".
// We write a tiny PNG header (8 bytes of valid PNG magic
// + IEND chunk) to make sure the file is technically
// readable, and verify the Read tool returns a clean stub
// with IsError=false (this is a "not text" outcome, not a
// tool failure).
func TestReadImagePDFStub(t *testing.T) {
	tc, dir := readTC(t)
	// Minimal valid PNG header: 8-byte signature + 25-byte
	// IHDR + 12-byte IEND.
	pngHeader := []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0, 0, 0, 0x0D, 'I', 'H', 'D', 'R',
	}
	for len(pngHeader) < 64 {
		pngHeader = append(pngHeader, 0)
	}
	pngPath := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(pngPath, pngHeader, 0644); err != nil {
		t.Fatalf("write png: %v", err)
	}
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\n%fake pdf content\n"), 0644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	r := &ReadTool{}
	for _, p := range []string{pngPath, pdfPath} {
		out := r.Execute(context.Background(),
			decodeReadInput(t, fmt.Sprintf(`{"file_path":%q}`, p)),
			tc)
		if out.IsError {
			t.Errorf("Read on %s: should NOT be IsError (it's a stub, not a failure): %+v", p, out)
		}
		if !strings.Contains(out.Text, "does not return image or PDF content") {
			t.Errorf("Read on %s: stub text missing; got %q", p, out.Text)
		}
		if kind, _ := out.Metadata["kind"].(string); kind != "image_or_pdf" {
			t.Errorf("Read on %s: Metadata.kind = %q, want 'image_or_pdf'", p, kind)
		}
	}
}
