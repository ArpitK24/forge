package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// These tests pin the MCP stdio wire format: newline-delimited
// JSON-RPC, one message per line, no embedded newlines, no
// Content-Length headers.
// https://modelcontextprotocol.io/specification/2025-06-18/basic/transports#stdio

// TestWriteFrame_SpecWireFormat checks the exact bytes on the wire:
// the JSON body followed by a single "\n", and nothing else.
func TestWriteFrame_SpecWireFormat(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	var buf bytes.Buffer
	n, err := writeFrame(&buf, body)
	if err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	want := string(body) + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("wire bytes = %q, want %q", got, want)
	}
	if n != len(want) {
		t.Errorf("n = %d, want %d", n, len(want))
	}
	if strings.Contains(buf.String(), "Content-Length") {
		t.Errorf("wire bytes contain a Content-Length header: %q", buf.String())
	}
}

// TestWriteFrame_CompactsEmbeddedNewlines: a pretty-printed body
// must still go out as exactly one line, with its string contents
// (including escaped newlines) intact.
func TestWriteFrame_CompactsEmbeddedNewlines(t *testing.T) {
	body := []byte("{\n  \"jsonrpc\": \"2.0\",\r\n  \"method\": \"x\",\n  \"params\": {\"text\": \"a\\nb\"}\n}")
	var buf bytes.Buffer
	if _, err := writeFrame(&buf, body); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	wire := buf.String()
	if strings.Count(wire, "\n") != 1 || !strings.HasSuffix(wire, "\n") {
		t.Fatalf("wire = %q, want exactly one trailing newline", wire)
	}
	if strings.Contains(wire, "\r") {
		t.Fatalf("wire = %q, contains a carriage return", wire)
	}
	var got struct {
		Params struct {
			Text string `json:"text"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(wire), &got); err != nil {
		t.Fatalf("wire is not valid JSON: %v", err)
	}
	if got.Params.Text != "a\nb" {
		t.Errorf("text = %q, want %q", got.Params.Text, "a\nb")
	}
}

// TestWriteFrame_RejectsUnframeableBody: a body with a raw newline
// that is not valid JSON cannot be put on one line, so it must not
// be written at all.
func TestWriteFrame_RejectsUnframeableBody(t *testing.T) {
	for _, body := range []string{"not json\n", "", "  "} {
		var buf bytes.Buffer
		_, err := writeFrame(&buf, []byte(body))
		if err == nil {
			t.Errorf("writeFrame(%q) succeeded; want error", body)
			continue
		}
		var ce *core.Error
		if !errors.As(err, &ce) || ce.Kind != core.KindMCP {
			t.Errorf("writeFrame(%q) err = %v, want KindMCP", body, err)
		}
		if buf.Len() != 0 {
			t.Errorf("writeFrame(%q) wrote %q; want nothing", body, buf.String())
		}
	}
}

// TestWriteFrame_RejectsOversizeBody: outbound messages share the
// inbound cap.
func TestWriteFrame_RejectsOversizeBody(t *testing.T) {
	body := []byte(`{"s":"` + strings.Repeat("x", maxFrameBytes) + `"}`)
	var buf bytes.Buffer
	_, err := writeFrame(&buf, body)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindMCP {
		t.Fatalf("err = %v, want KindMCP", err)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %d bytes; want nothing", buf.Len())
	}
}

// TestWriteFrame_DoesNotMutateCallerBuffer: appending the newline
// must not scribble into spare capacity of the caller's slice.
func TestWriteFrame_DoesNotMutateCallerBuffer(t *testing.T) {
	backing := []byte(`{"a":1}XXXX`)
	body := backing[:7]
	var buf bytes.Buffer
	if _, err := writeFrame(&buf, body); err != nil {
		t.Fatalf("writeFrame: %v", err)
	}
	if string(backing) != `{"a":1}XXXX` {
		t.Errorf("caller buffer modified: %q", backing)
	}
	if buf.String() != "{\"a\":1}\n" {
		t.Errorf("wire = %q", buf.String())
	}
}

// TestWriteReadRoundTrip: a body written via writeFrame is
// recovered exactly by readFrame, across sizes that cross the
// bufio buffer boundary (64 KiB in StdioTransport) several times.
func TestWriteReadRoundTrip(t *testing.T) {
	sizes := []int{1, 1024, 65535, 65536, 65537, 1 << 20, 8 << 20}
	for _, n := range sizes {
		t.Run(itoa(n), func(t *testing.T) {
			body := []byte(`{"s":"` + strings.Repeat("x", n) + `"}`)
			var buf bytes.Buffer
			if _, err := writeFrame(&buf, body); err != nil {
				t.Fatalf("writeFrame: %v", err)
			}
			got, err := readFrame(bufio.NewReaderSize(&buf, 64*1024))
			if err != nil {
				t.Fatalf("readFrame: %v", err)
			}
			if !bytes.Equal(got, body) {
				t.Fatalf("body mismatch: got %d bytes, want %d", len(got), len(body))
			}
		})
	}
}

// TestReadFrame_SpecPeerStream reads what a spec-conformant server
// writes: several messages back to back, one per line, with UTF-8
// payloads, CRLF endings and blank lines tolerated.
func TestReadFrame_SpecPeerStream(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}` + "\n" +
		"\n" +
		`{"jsonrpc":"2.0","method":"notifications/message","params":{"data":"héllo 世界 🚀"}}` + "\r\n" +
		"   \r\n" +
		`{"jsonrpc":"2.0","id":2,"result":{}}` + "\n"
	r := bufio.NewReader(strings.NewReader(raw))
	want := []string{
		`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/message","params":{"data":"héllo 世界 🚀"}}`,
		`{"jsonrpc":"2.0","id":2,"result":{}}`,
	}
	for i, w := range want {
		got, err := readFrame(r)
		if err != nil {
			t.Fatalf("message %d: readFrame: %v", i, err)
		}
		if string(got) != w {
			t.Fatalf("message %d = %q, want %q", i, got, w)
		}
	}
	// Stream is exhausted: a clean EOF between messages.
	_, err := readFrame(r)
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindIO {
		t.Fatalf("after last message err = %v, want KindIO EOF", err)
	}
}

// TestReadFrame_EOFBeforeMessage covers the "peer disconnected
// cleanly before sending anything" case. Should classify as
// KindIO (transport-level EOF), not KindMCP (protocol bug).
func TestReadFrame_EOFBeforeMessage(t *testing.T) {
	for _, raw := range []string{"", "\n\n", "\r\n  "} {
		_, err := readFrame(bufio.NewReader(strings.NewReader(raw)))
		if err == nil {
			t.Fatalf("readFrame(%q): expected EOF error", raw)
		}
		var ce *core.Error
		if !errors.As(err, &ce) {
			t.Fatalf("err type = %T, want *core.Error", err)
		}
		if ce.Kind != core.KindIO {
			t.Errorf("readFrame(%q) kind = %v, want KindIO", raw, ce.Kind)
		}
	}
}

// TestReadFrame_UnterminatedMessage covers a peer that hangs up
// partway through a message (no trailing newline). KindIO.
func TestReadFrame_UnterminatedMessage(t *testing.T) {
	_, err := readFrame(bufio.NewReader(strings.NewReader(`{"jsonrpc":"2.0","id":1,"res`)))
	if err == nil {
		t.Fatalf("expected error for unterminated message")
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindIO {
		t.Fatalf("err = %v, want KindIO", err)
	}
}

// TestReadFrame_OversizeFrame: a line longer than maxFrameBytes is
// rejected with KindMCP instead of being buffered without bound.
func TestReadFrame_OversizeFrame(t *testing.T) {
	r := io.MultiReader(
		strings.NewReader(`{"s":"`),
		io.LimitReader(repeatReader('x'), maxFrameBytes+1024),
		strings.NewReader("\"}\n"),
	)
	_, err := readFrame(bufio.NewReaderSize(r, 64*1024))
	if err == nil {
		t.Fatalf("expected frame-too-large error")
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Kind != core.KindMCP {
		t.Fatalf("err = %v, want KindMCP", err)
	}
	if !strings.Contains(ce.Error(), "too large") {
		t.Errorf("err = %q, want it to mention 'too large'", ce.Error())
	}
}

// TestReadFrame_MessageSpansMultipleReads: a message that arrives
// one byte at a time (a slow pipe) is reassembled into one line.
func TestReadFrame_MessageSpansMultipleReads(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":7,"result":{"ok":true}}`
	got, err := readFrame(bufio.NewReader(&oneByteReader{r: strings.NewReader(body + "\n")}))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}

// repeatReader yields an endless stream of b.
type repeatReader byte

func (b repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}

// oneByteReader returns at most one byte per Read call.
type oneByteReader struct{ r io.Reader }

func (o *oneByteReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
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
