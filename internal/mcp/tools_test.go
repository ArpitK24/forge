package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// toolCallWire is a small helper to construct a JSON-RPC
// response for a tools/call invocation. Returns the bytes
// the fake should hand back to the read goroutine.
func toolCallWire(t *testing.T, id int64, blocks []map[string]any, isErr bool) []byte {
	t.Helper()
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result: json.RawMessage(mustMarshal(map[string]any{
			"content": blocks,
			"isError": isErr,
		})),
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// setupMcpTool wires up an mcpTool backed by a fakeTransport
// so tests can drive Execute() end-to-end. The returned
// channel signals "registration complete" (the goroutine
// inside Manager.lookup has populated serverState).
func setupMcpTool(t *testing.T, respBytes []byte) (*mcpTool, *fakeTransport) {
	t.Helper()
	tr := newFakeTransport()
	tr.queue(respBytes)
	mgr := NewManager(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.servers["test"] = &serverState{
		name: "test",
		cli:  newClient("test", tr, slog.New(slog.NewTextHandler(io.Discard, nil))),
		tools: []serverTool{
			{
				Name:        "read_file",
				Description: "Read a file",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
	}
	// We don't call cli.start because the fake's Recv
	// would block on the gate. Instead, return the
	// already-queued response. But the readLoop is what
	// demuxes — we need it running. Start it directly
	// here.
	mgr.servers["test"].cli.start(context.Background())
	mt := &mcpTool{
		manager:   mgr,
		server:    "test",
		name:      "read_file",
		desc:      "Read a file",
		inputJSON: json.RawMessage(`{"type":"object"}`),
	}
	return mt, tr
}

// TestMcpTool_Name verifies the namespacing: server=test,
// tool=read_file → "mcp__test__read_file".
func TestMcpTool_Name(t *testing.T) {
	mt := &mcpTool{server: "filesystem", name: "read_file"}
	want := "mcp__filesystem__read_file"
	if got := mt.Name(); got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

// TestMcpTool_PermissionLevel verifies the safety
// classification: PermExecute, since MCP tools can do
// anything the server can.
func TestMcpTool_PermissionLevel(t *testing.T) {
	mt := &mcpTool{}
	if got := mt.PermissionLevel(); got != core.PermExecute {
		t.Errorf("PermissionLevel() = %v, want PermExecute", got)
	}
}

// TestMcpTool_InputSchema verifies the schema is passed
// through verbatim — the MCP server is the source of truth.
func TestMcpTool_InputSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	mt := &mcpTool{inputJSON: schema}
	got := mt.InputSchema()
	if string(got) != string(schema) {
		t.Errorf("InputSchema() = %s, want %s", got, schema)
	}
}

// TestMcpTool_Execute_TextBlock verifies the text-only
// success path: a tools/call response with one text block
// becomes a ToolResult with that text in Text and
// IsError=false.
func TestMcpTool_Execute_TextBlock(t *testing.T) {
	resp := toolCallWire(t, 1, []map[string]any{
		{"type": "text", "text": "hello"},
	}, false)
	mt, tr := setupMcpTool(t, resp)
	// We don't call release here; the readLoop is
	// already running (via cli.start). The fake's hold
	// mode is off by default, so Recv will return the
	// queued response immediately.
	_ = tr
	r := mt.Execute(context.Background(), json.RawMessage(`{"path":"/tmp/x"}`), nil)
	if r.IsError {
		t.Errorf("IsError = true, want false; Text=%q", r.Text)
	}
	if r.Text != "hello" {
		t.Errorf("Text = %q, want %q", r.Text, "hello")
	}
}

// TestMcpTool_Execute_MultipleTextBlocks verifies that
// multiple text blocks are joined with newlines (the
// step-4 stub behavior — step 6 will preserve blocks
// verbatim via Blocks).
func TestMcpTool_Execute_MultipleTextBlocks(t *testing.T) {
	resp := toolCallWire(t, 1, []map[string]any{
		{"type": "text", "text": "first"},
		{"type": "text", "text": "second"},
	}, false)
	mt, _ := setupMcpTool(t, resp)
	r := mt.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if r.IsError {
		t.Errorf("IsError = true, want false")
	}
	if r.Text != "first\nsecond" {
		t.Errorf("Text = %q, want %q", r.Text, "first\nsecond")
	}
}

// TestMcpTool_Execute_IsError verifies that a server-side
// tool-level failure (isError: true with text) becomes a
// ToolResult with IsError=true.
func TestMcpTool_Execute_IsError(t *testing.T) {
	resp := toolCallWire(t, 1, []map[string]any{
		{"type": "text", "text": "ENOENT: no such file"},
	}, true)
	mt, _ := setupMcpTool(t, resp)
	r := mt.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if !r.IsError {
		t.Errorf("IsError = false, want true")
	}
	if r.Text != "ENOENT: no such file" {
		t.Errorf("Text = %q, want %q", r.Text, "ENOENT: no such file")
	}
}

// TestMcpTool_Execute_IsErrorNoText verifies that an
// isError response with no text blocks still gets a
// non-empty Text field (so the model sees something).
func TestMcpTool_Execute_IsErrorNoText(t *testing.T) {
	resp := toolCallWire(t, 1, []map[string]any{}, true)
	mt, _ := setupMcpTool(t, resp)
	r := mt.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if !r.IsError {
		t.Errorf("IsError = false, want true")
	}
	if r.Text == "" {
		t.Errorf("Text is empty; want a placeholder so the model sees something")
	}
}

// TestMcpTool_Execute_TransportFailure verifies that a
// transport-level failure (no response queued) becomes a
// tool-level error, not a panic. The safeExecute wrapper
// in query/loop.go catches panics, but a defensive recover
// is cheap insurance.
func TestMcpTool_Execute_TransportFailure(t *testing.T) {
	tr := newFakeTransport()
	tr.recvErr = errors.New("simulated transport failure")
	mgr := NewManager(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mgr.servers["x"] = &serverState{
		name: "x",
		cli:  newClient("x", tr, slog.New(slog.NewTextHandler(io.Discard, nil))),
		tools: []serverTool{
			{Name: "t", Description: "t", InputSchema: json.RawMessage(`{}`)},
		},
	}
	mgr.servers["x"].cli.start(context.Background())
	mt := &mcpTool{manager: mgr, server: "x", name: "t"}

	r := mt.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if !r.IsError {
		t.Errorf("IsError = false, want true on transport failure")
	}
	if r.Text == "" {
		t.Errorf("Text empty on transport failure")
	}
}

// TestMcpTool_Execute_ServerNotConnected verifies that
// Execute on a tool whose server has been Close'd returns
// a clear error rather than panicking on nil deref.
func TestMcpTool_Execute_ServerNotConnected(t *testing.T) {
	mgr := NewManager(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// No servers registered.
	mt := &mcpTool{manager: mgr, server: "gone", name: "t"}
	r := mt.Execute(context.Background(), json.RawMessage(`{}`), nil)
	if !r.IsError {
		t.Errorf("IsError = false, want true when server not connected")
	}
}

// TestMcpTool_InterfaceCompliance is a compile-time guard:
// if mcpTool drifts from the tools.Tool interface this build
// breaks. The run-time assertion below is redundant but
// documents the contract.
func TestMcpTool_InterfaceCompliance(t *testing.T) {
	var _ tools.Tool = (*mcpTool)(nil)
}

// waitForReady polls the fake until pending queue is empty,
// proving the read goroutine drained the queued bytes. Used
// when a test wants to know the demux finished.
func waitForReady(t *testing.T, f *fakeTransport) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.pending)
		f.mu.Unlock()
		if got == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitForReady: pending queue still has bytes after 2s")
}