package mcp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"

	"github.com/ArpitK24/forge/internal/core"
)

// writeFrame serializes a JSON-RPC body using the LSP-derived
// Content-Length framing. The MCP spec (2025-06-18) adopts this
// framing for the stdio transport, so we use the same shape:
//
//	Content-Length: N\r\n
//	\r\n
//	<body bytes>
//
// Returns the number of bytes written. Errors are propagated
// from the underlying io.Writer.
func writeFrame(w io.Writer, body []byte) (int, error) {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	hN, err := io.WriteString(w, header)
	if err != nil {
		return hN, core.Wrap(core.KindIO, err, "write frame header")
	}
	bN, err := w.Write(body)
	if err != nil {
		return hN + bN, core.Wrap(core.KindIO, err, "write frame body")
	}
	return hN + bN, nil
}

// readFrame reads one Content-Length-framed message from r and
// returns the body. Blocks until the full message is available
// or an error occurs.
//
// Error classification:
//
//   - EOF before any header bytes: *core.Error{Kind:KindIO,
//     Message:"read frame header: unexpected EOF"}. This is
//     treated as a transport-level EOF (the peer disconnected
//     cleanly before sending anything).
//   - EOF mid-header or mid-body: *core.Error{Kind:KindIO} with
//     "short read" framing. The peer hung up partway through a
//     message — a protocol error, not a clean shutdown.
//   - Header parse failure (malformed Content-Length, missing
//     colon, missing \r\n\r\n terminator): *core.Error{Kind:
//     KindMCP, Message:"invalid frame header"}. A wire-level
//     bug, distinguishable from a transport failure.
//   - Body length exceeds an internal sanity cap: *core.Error{
//     Kind:KindMCP, Message:"frame too large"}. Defends against
//     a malicious or buggy peer sending a 4 GiB Content-Length
//     that would otherwise exhaust memory.
//
// The 1 MiB cap is generous: a real MCP message is rarely more
// than a few hundred KiB (a tools/call response with a large
// image block is the worst realistic case). 1 MiB leaves headroom
// while still capping a hostile peer's framing bug.
const maxFrameBytes = 1 << 20 // 1 MiB

func readFrame(r *bufio.Reader) ([]byte, error) {
	// Read until we hit the \r\n\r\n terminator. We tolerate
	// any number of header lines; we only consume Content-Length.
	// Other header lines (per LSP, e.g. Content-Type) are ignored.
	var headerBuf bytes.Buffer
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if err == io.EOF && headerBuf.Len() == 0 {
				return nil, core.New(core.KindIO, "read frame header: unexpected EOF")
			}
			return nil, core.Wrap(core.KindIO, err, "read frame header")
		}
		// Strip trailing \r\n or \n.
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			// Empty line terminates the header block.
			if headerBuf.Len() == 0 {
				// Two \r\n\r\n in a row — treat as protocol error.
				return nil, core.New(core.KindMCP, "empty header line before Content-Length")
			}
			break
		}
		headerBuf.Write(line)
		headerBuf.WriteByte('\n')
		if headerBuf.Len() > 4096 {
			// Headers should never exceed a few hundred bytes.
			return nil, core.Newf(core.KindMCP, "frame header exceeds 4 KiB sanity cap")
		}
	}

	// Parse the header buffer for Content-Length. We accept the
	// single field the MCP spec requires; other LSP-derived headers
	// are silently ignored.
	headerText := headerBuf.String()
	var contentLength int
	for _, hl := range bytes.Split([]byte(headerText), []byte{'\n'}) {
		hl = bytes.TrimRight(hl, "\r")
		if len(hl) == 0 {
			continue
		}
		colon := bytes.IndexByte(hl, ':')
		if colon < 0 {
			return nil, core.Newf(core.KindMCP, "malformed header line: %q", string(hl))
		}
		field := string(bytes.TrimSpace(hl[:colon]))
		value := string(bytes.TrimSpace(hl[colon+1:]))
		if field != "Content-Length" {
			continue
		}
		// Parse a non-negative integer.
		n := 0
		for _, c := range value {
			if c < '0' || c > '9' {
				return nil, core.Newf(core.KindMCP, "invalid Content-Length: %q", value)
			}
			n = n*10 + int(c-'0')
		}
		contentLength = n
		break
	}
	if contentLength < 0 {
		return nil, core.Newf(core.KindMCP, "negative Content-Length: %d", contentLength)
	}
	if contentLength > maxFrameBytes {
		return nil, core.Newf(core.KindMCP, "frame too large: Content-Length=%d (max %d)", contentLength, maxFrameBytes)
	}

	// Read exactly contentLength bytes. bufio.Reader's Read
	// handles the case where the body spans multiple underlying
	// reads; we loop until we've collected contentLength bytes.
	body := make([]byte, contentLength)
	if contentLength == 0 {
		return body, nil
	}
	total := 0
	for total < contentLength {
		n, err := r.Read(body[total:])
		if err != nil {
			if err == io.EOF {
				return nil, core.Newf(core.KindIO, "short body: got %d, want %d", total, contentLength)
			}
			return nil, core.Wrap(core.KindIO, err, "read frame body")
		}
		total += n
	}
	return body, nil
}
