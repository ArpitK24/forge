package mcp

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// TestWriteReadRoundTrip is the basic sanity check: a body of N
// bytes written via writeFrame is recovered exactly by readFrame.
// Tests four sizes spanning the small/medium/large range so the
// body-read loop is exercised at multiple lengths.
func TestWriteReadRoundTrip(t *testing.T) {
	sizes := []int{1, 1024, 65536, 1 << 20} // 1B, 1KiB, 64KiB, 1MiB
	for _, n := range sizes {
		t.Run("", func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), n)
			var buf bytes.Buffer
			if _, err := writeFrame(&buf, body); err != nil {
				t.Fatalf("writeFrame: %v", err)
			}
			got, err := readFrame(bufio.NewReader(&buf))
			if err != nil {
				t.Fatalf("readFrame: %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("body mismatch: got %d bytes, want %d", len(got), n)
			}
		})
	}
}

// TestReadFrame_EOFBeforeHeader covers the "peer disconnected
// cleanly before sending anything" case. Should classify as
// KindIO (transport-level EOF), not KindMCP (protocol bug).
func TestReadFrame_EOFBeforeHeader(t *testing.T) {
	_, err := readFrame(bufio.NewReader(bytes.NewReader(nil)))
	if err == nil {
		t.Fatalf("expected EOF error")
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err type = %T, want *core.Error", err)
	}
	if ce.Kind != core.KindIO {
		t.Errorf("kind = %v, want KindIO", ce.Kind)
	}
}

// TestReadFrame_ShortBody covers the "peer sent header but hung
// up partway through body" case. Should classify as KindIO
// (transport failure mid-message).
func TestReadFrame_ShortBody(t *testing.T) {
	// Header says 100 bytes; we only send 10.
	raw := "Content-Length: 100\r\n\r\nxxxxxxxxxx"
	_, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
	if err == nil {
		t.Fatalf("expected short-body error")
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err type = %T, want *core.Error", err)
	}
	if ce.Kind != core.KindIO {
		t.Errorf("kind = %v, want KindIO", ce.Kind)
	}
}

// TestReadFrame_BodyLengthMismatch covers the "peer claimed 50
// bytes but only sent 30" case via a manufactured body that's
// truncated. readFrame's loop bails with KindIO.
func TestReadFrame_BodyLengthMismatch(t *testing.T) {
	raw := "Content-Length: 50\r\n\r\n0123456789"
	_, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
	if err == nil {
		t.Fatalf("expected mismatch error")
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err type = %T, want *core.Error", err)
	}
	if ce.Kind != core.KindIO {
		t.Errorf("kind = %v, want KindIO", ce.Kind)
	}
}

// TestReadFrame_InvalidContentLength covers a malformed
// Content-Length value (letters instead of digits). Should
// classify as KindMCP.
func TestReadFrame_InvalidContentLength(t *testing.T) {
	raw := "Content-Length: abc\r\n\r\n"
	_, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
	if err == nil {
		t.Fatalf("expected parse error")
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err type = %T, want *core.Error", err)
	}
	if ce.Kind != core.KindMCP {
		t.Errorf("kind = %v, want KindMCP", ce.Kind)
	}
}

// TestReadFrame_OversizeFrame covers a Content-Length above the
// 1 MiB cap. Defends against a buggy or hostile peer sending
// a giant Content-Length that would otherwise exhaust memory.
func TestReadFrame_OversizeFrame(t *testing.T) {
	raw := "Content-Length: 99999999\r\n\r\n"
	_, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
	if err == nil {
		t.Fatalf("expected oversize error")
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err type = %T, want *core.Error", err)
	}
	if ce.Kind != core.KindMCP {
		t.Errorf("kind = %v, want KindMCP", ce.Kind)
	}
}

// TestReadFrame_IgnoresUnknownHeaders covers the LSP-derived
// behavior: Content-Type and other non-Content-Length header
// lines are tolerated and ignored. The MCP stdio framing only
// requires Content-Length.
func TestReadFrame_IgnoresUnknownHeaders(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0"}`)
	var buf bytes.Buffer
	buf.WriteString("Content-Type: application/vscode-jsonrpc; charset=utf-8\r\n")
	buf.WriteString("Content-Length: ")
	buf.WriteString(itoa(len(body)))
	buf.WriteString("\r\n\r\n")
	buf.Write(body)

	got, err := readFrame(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body = %q, want %q", got, body)
	}
}

// TestReadFrame_EmptyBody covers the legitimate "zero-length
// body" case (e.g. a notification with empty params). Should
// round-trip cleanly with no error and an empty []byte.
func TestReadFrame_EmptyBody(t *testing.T) {
	raw := "Content-Length: 0\r\n\r\n"
	got, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("body = %q, want empty", got)
	}
}

// TestReadFrame_BodySpansMultipleReads covers the case where
// the body arrives in chunks smaller than the bufio.Reader's
// internal buffer. The body-read loop must keep collecting
// bytes until it has the full Content-Length.
func TestReadFrame_BodySpansMultipleReads(t *testing.T) {
	body := []byte("abcdefghijklmnopqrstuvwxyz")
	var buf bytes.Buffer
	buf.WriteString("Content-Length: 26\r\n\r\n")
	// Write one byte at a time — extremely small chunks.
	for _, b := range body {
		buf.WriteByte(b)
	}
	got, err := readFrame(bufio.NewReader(&buf))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body = %q, want %q", got, body)
	}
}

// itoa is a tiny helper so the test file doesn't need to import
// strconv just for one conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
