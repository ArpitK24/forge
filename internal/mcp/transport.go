package mcp

import (
	"context"
	"io"
)

// Transport is the seam between a per-server client and the
// physical MCP connection. Two implementations exist:
//
//   - StdioTransport: subprocess + Content-Length-framed
//     stdio (the bedrock, used for every real server today).
//   - HTTPTransport: stub. Streamable-HTTP deferred to a
//     follow-on step. Start returns a KindMCP error so a
//     user who configures server_type: "http" learns why
//     it doesn't work.
//
// Concurrency contract: Transport is safe for concurrent
// Recv calls (returns distinct messages). Send is NOT
// concurrent-safe — the per-server client (client.go)
// serializes writes through a mutex at the StdioTransport
// boundary because one frame written atomically to the
// underlying writer is required for framing correctness.
type Transport interface {
	// Start brings the transport up. ctx cancellation
	// aborts Start. After Start returns nil, Send and Recv
	// are valid. Start may be called again only after Close.
	Start(ctx context.Context) error
	// Send writes one framed message. Implementations MUST
	// flush before returning so the peer sees the bytes
	// promptly. Send is single-goroutine — the per-server
	// client enforces serialization.
	Send(body []byte) error
	// Recv blocks until one framed message arrives, ctx is
	// cancelled, or the underlying transport errors.
	// On ctx cancel, returns the ctx error wrapped as
	// *core.Error{Kind:KindCancelled}.
	Recv(ctx context.Context) ([]byte, error)
	// Close shuts the transport. Idempotent. Should drain
	// any pending send or return an error explaining why
	// the drain failed.
	Close() error
}

// Compile-time interface compliance. A non-implementation
// would fail to build rather than fail at runtime.
var (
	_ Transport = (*StdioTransport)(nil)
	_ Transport = (*HTTPTransport)(nil)
)

// nopCloser is used by transports that don't expose a
// stream to Close separately — saves an import cycle.
type nopCloser struct {
	io.Reader
}

func (nopCloser) Close() error { return nil }
