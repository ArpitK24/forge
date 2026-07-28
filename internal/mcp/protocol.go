// Package mcp implements a hand-rolled Model Context Protocol
// (MCP) client. It connects to one or more MCP servers (stdio
// or streamable-HTTP transports), exposes their tools to the
// rest of Forge under the canonical mcp__<server>__<tool>
// namespace, and propagates multi-block tool results faithfully
// to the model via tools.ToolResult.Blocks.
//
// This package deliberately does NOT use the official
// github.com/modelcontextprotocol/go-sdk — the protocol is
// small enough to implement directly (~550 LOC for stdio + the
// minimum-viable HTTP stub) and hand-rolling keeps Forge's
// binary budget unchanged. If a future step needs additional
// MCP capabilities (sampling, server-sent notifications,
// roots, etc.), this package is the place to add them; the
// JSON-RPC 2.0 envelope defined in protocol.go is the seam.
//
// Transport scope (Phase 4 step 5):
//   - StdioTransport: production-ready, used for every server
//     today.
//   - HTTPTransport: stub only. Returns a KindMCP error so a
//     user who configures server_type: "http" learns why it
//     doesn't work rather than getting a confusing connect
//     failure. Streamable-HTTP is a Phase 4.1 follow-on.
package mcp

import "encoding/json"

// JSONRPCVersion is the protocol version string every envelope
// carries. JSON-RPC 2.0 hardcodes "2.0" — anything else is a
// protocol error.
const JSONRPCVersion = "2.0"

// Canonical MCP method names. These are the strings exchanged
// on the wire; the constants are surfaced (not opaque) so the
// smoke test can assert lifecycle ordering.
const (
	// MethodInitialize is the first request the client sends.
	// The server's response carries its protocolVersion and
	// capabilities. The MCP spec pins the version today
	// (2025-06-18); we hardcode and warn on mismatch.
	MethodInitialize = "initialize"
	// MethodInitialized is a notification (no id) sent by the
	// client after receiving the initialize response. It
	// signals the client is ready for normal traffic.
	// Notification-vs-request framing is the most common MCP
	// client bug; the smoke test exercises this.
	MethodInitialized = "notifications/initialized"
	// MethodShutdown is a notification sent before Close to
	// give the server a chance to flush cleanly. The server
	// is not required to respond.
	MethodShutdown = "shutdown"
	// MethodToolsList returns the array of tool descriptors
	// the server exposes. Called once after initialize.
	MethodToolsList = "tools/list"
	// MethodToolsCall invokes a tool. The result carries a
	// content[] array (possibly multi-block: text + image +
	// embedded_resource).
	MethodToolsCall = "tools/call"
	// MethodResourcesList returns the server's resources.
	// Not used in Phase 4 step 5; reserved for a follow-on.
	MethodResourcesList = "resources/list"
	// MethodResourcesRead fetches a resource by URI. Not used
	// in step 5; reserved for a follow-on.
	MethodResourcesRead = "resources/read"
)

// Request is a JSON-RPC 2.0 request envelope. ID is int64
// because Forge's id generator (atomic.Int64 in client.go)
// produces int64 values, and JSON-RPC 2.0 requires string,
// number, or null. We use number exclusively; the protocol
// test pins the wire shape.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response envelope. Exactly one of
// Result and Error is non-nil per spec; we don't enforce that
// at decode time (the wire may carry both as a server bug, in
// which case Result wins because Error is optional in our
// decode).
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification is a JSON-RPC 2.0 notification: a request
// without an id field. The server does not respond to
// notifications. Used for "initialized", "shutdown", and
// server-initiated messages like progress.
type Notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// RPCError is the standard JSON-RPC 2.0 error object. Code is
// an integer per spec; predefined codes are -32768 (parse),
// -32600 (invalid request), -32601 (method not found), etc.
// Implementation-defined codes occupy [-32000, -32099] and
// are where MCP servers report their own errors.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}
