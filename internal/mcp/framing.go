package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/ArpitK24/forge/internal/core"
)

// MCP stdio framing.
//
// The MCP stdio transport (spec 2025-06-18, basic/transports#stdio)
// is newline-delimited JSON-RPC:
//
//   - each message is one UTF-8 JSON-RPC request, notification or
//     response, terminated by "\n";
//   - a message MUST NOT contain embedded newlines;
//   - nothing else may be written to the peer's stdin/stdout.
//
// There are no Content-Length headers. (LSP uses Content-Length
// framing, and so does Forge's own plugin sidecar protocol in
// internal/plugins, but MCP over stdio does not.)
//
// https://modelcontextprotocol.io/specification/2025-06-18/basic/transports#stdio

// maxFrameBytes caps one inbound message. Real MCP messages are
// usually small, but a tools/call result carrying an image or a
// large file can run to several MiB once base64-encoded, so the
// cap is generous. It still bounds what a buggy or hostile peer
// can make us buffer for a single line.
const maxFrameBytes = 32 << 20 // 32 MiB

// writeFrame writes one JSON-RPC message followed by "\n".
//
// The body must be a single JSON value. encoding/json never emits
// raw newlines, but a caller-supplied body might be indented; in
// that case it is compacted first (JSON escapes newlines inside
// strings, so compaction removes every raw newline). A body that
// still cannot be put on one line is rejected rather than sent,
// because a stray newline would desynchronize the peer's reader.
//
// The message and its terminator go out in a single Write so a
// concurrent writer sharing the pipe can never split them.
// Returns the number of bytes written.
func writeFrame(w io.Writer, body []byte) (int, error) {
	if bytes.ContainsAny(body, "\r\n") {
		var buf bytes.Buffer
		if err := json.Compact(&buf, body); err != nil {
			return 0, core.Wrap(core.KindMCP, err, "write frame: body contains a newline and is not valid JSON")
		}
		body = buf.Bytes()
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return 0, core.New(core.KindMCP, "write frame: empty message")
	}
	msg := make([]byte, 0, len(body)+1)
	msg = append(msg, body...)
	msg = append(msg, '\n')
	n, err := w.Write(msg)
	if err != nil {
		return n, core.Wrap(core.KindIO, err, "write frame")
	}
	return n, nil
}

// readFrame reads the next newline-delimited JSON-RPC message from
// r and returns it without its line terminator. Blocks until a full
// line is available or an error occurs.
//
// Tolerances: a trailing "\r" (CRLF line endings, e.g. a server on
// Windows writing in text mode) is stripped, and blank or
// whitespace-only lines between messages are skipped.
//
// Error classification:
//
//   - EOF before any message bytes: *core.Error{Kind:KindIO,
//     Message:"read frame: unexpected EOF"}. The peer disconnected
//     cleanly between messages.
//   - EOF in the middle of a line (no terminating "\n"):
//     *core.Error{Kind:KindIO}. The peer hung up partway through a
//     message.
//   - A line longer than maxFrameBytes: *core.Error{Kind:KindMCP,
//     Message:"frame too large"}. The rest of that line is not
//     buffered.
//
// readFrame does not validate the JSON itself; the client's read
// loop decodes the envelope and reports malformed bodies.
func readFrame(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := readLine(r)
		if err != nil {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue // blank line between messages
		}
		return line, nil
	}
}

// readLine reads up to and including the next "\n" and returns the
// line without it. It accumulates bufio.Reader fragments itself so
// the size cap is enforced before the line is fully buffered.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(line)+len(frag) > maxFrameBytes+2 { // +2 for "\r\n"
			return nil, core.Newf(core.KindMCP, "frame too large: message exceeds %d bytes", maxFrameBytes)
		}
		switch {
		case err == nil:
			if line == nil {
				// Common case: the whole line fit in the
				// reader's buffer. Copy it, because ReadSlice's
				// result is only valid until the next read.
				line = append(make([]byte, 0, len(frag)), frag...)
			} else {
				line = append(line, frag...)
			}
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			line = append(line, frag...)
			continue
		case errors.Is(err, io.EOF):
			line = append(line, frag...)
			if len(bytes.TrimSpace(line)) == 0 {
				return nil, core.New(core.KindIO, "read frame: unexpected EOF")
			}
			return nil, core.Newf(core.KindIO, "read frame: EOF after %d bytes of an unterminated message", len(line))
		default:
			return nil, core.Wrap(core.KindIO, err, "read frame")
		}
	}
}
