package plugins

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestFraming_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	msgs := [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`),
		[]byte(`{}`),
		[]byte(`{"text":"héllo — ünïcode"}`),
	}
	for _, m := range msgs {
		n, err := writeFrame(&buf, m)
		if err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
		if n <= len(m) {
			t.Fatalf("writeFrame returned %d, want > body length %d", n, len(m))
		}
	}
	r := bufio.NewReader(&buf)
	for i, want := range msgs {
		got, err := readFrame(r)
		if err != nil {
			t.Fatalf("readFrame #%d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("readFrame #%d = %q, want %q", i, got, want)
		}
	}
	if _, err := readFrame(r); err == nil || !isCleanEOF(err) {
		t.Fatalf("after last frame: err = %v, want clean EOF", err)
	}
}

func TestFraming_IgnoresExtraHeaders(t *testing.T) {
	in := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\nContent-Length: 2\r\n\r\n{}"
	got, err := readFrame(bufio.NewReader(strings.NewReader(in)))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("body = %q, want {}", got)
	}
}

func TestFraming_AcceptsBareLF(t *testing.T) {
	got, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: 2\n\n{}")))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(got) != "{}" {
		t.Fatalf("body = %q", got)
	}
}

func TestFraming_ZeroLength(t *testing.T) {
	got, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: 0\r\n\r\n")))
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("body = %q, want empty", got)
	}
}

func TestFraming_Errors(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantMsg string
	}{
		{"clean EOF", "", "unexpected EOF"},
		{"EOF mid header", "Content-Len", "read frame header"},
		{"blank line first", "\r\n", "empty header line"},
		{"no colon", "garbage\r\n\r\n", "malformed header line"},
		{"non numeric length", "Content-Length: abc\r\n\r\n{}", "invalid Content-Length"},
		{"negative length", "Content-Length: -5\r\n\r\n", "invalid Content-Length"},
		{"too large", "Content-Length: 99999999\r\n\r\n", "frame too large"},
		{"short body", "Content-Length: 10\r\n\r\n{}", "short body"},
		{"huge header", strings.Repeat("X-Pad: aaaaaaaaaaaaaaaa\r\n", 300), "4 KiB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readFrame(bufio.NewReader(strings.NewReader(tc.in)))
			if err == nil {
				t.Fatalf("readFrame(%q) = nil error, want %q", tc.in, tc.wantMsg)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("err = %q, want it to contain %q", err, tc.wantMsg)
			}
		})
	}
}

func TestIsCleanEOF_OnlyForEmptyStream(t *testing.T) {
	_, err := readFrame(bufio.NewReader(strings.NewReader("")))
	if !isCleanEOF(err) {
		t.Fatalf("empty stream: isCleanEOF(%v) = false", err)
	}
	_, err = readFrame(bufio.NewReader(strings.NewReader("Content-Length: 10\r\n\r\n{}")))
	if isCleanEOF(err) {
		t.Fatalf("short body must not count as a clean EOF: %v", err)
	}
}
