package mcp

import (
	"context"

	"github.com/ArpitK24/forge/internal/core"
)

// HTTPTransport is the streamable-HTTP transport per the MCP
// 2025-06-18 spec. It is a STUB in Phase 4 step 5 — the
// streamable-HTTP spec is brand-new (Anthropic shipped it in
// June 2025) and under-specified around SSE-pushback semantics,
// so a real implementation is deferred to a Phase 4.1 follow-on
// step.
//
// The stub satisfies the Transport interface and returns a clear
// error from Start so a user who configures server_type: "http"
// learns why it doesn't work, rather than getting a confusing
// connect failure midway through Manager.Connect.
type HTTPTransport struct {
	cfg core.McpServerConfig
}

// NewHTTPTransport constructs an HTTP transport. The transport
// is not started; the caller must invoke Start.
func NewHTTPTransport(cfg core.McpServerConfig) *HTTPTransport {
	return &HTTPTransport{cfg: cfg}
}

// Start returns a KindMCP error explaining that the HTTP
// transport is deferred. No goroutines are spawned; no network
// I/O happens; this is a deliberate hard-fail until the
// follow-on step lands.
func (t *HTTPTransport) Start(ctx context.Context) error {
	return core.Newf(core.KindMCP,
		"mcp server %q: http transport is Phase 4.1 follow-on (use server_type: \"stdio\" today)",
		t.cfg.Name)
}

// Send is intentionally unimplemented because Start always
// fails. Calling Send without a successful Start is a bug at
// the call site; the assertion clarifies the contract.
func (t *HTTPTransport) Send(body []byte) error {
	return core.Newf(core.KindMCP, "mcp server %q: send on unstarted http transport", t.cfg.Name)
}

// Recv is intentionally unimplemented (see Send).
func (t *HTTPTransport) Recv(ctx context.Context) ([]byte, error) {
	return nil, core.Newf(core.KindMCP, "mcp server %q: recv on unstarted http transport", t.cfg.Name)
}

// Close is a no-op for the stub — there's nothing to clean up.
func (t *HTTPTransport) Close() error { return nil }
