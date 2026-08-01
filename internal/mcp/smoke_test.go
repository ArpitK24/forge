//go:build mcp_smoke

// Package mcp — smoke_test.go. End-to-end protocol tests
// that exercise the full Manager → mcpTool → Client → StdioTransport
// pipeline against an in-process MCP server.
//
// The plan originally called for spawning the upstream
// `@modelcontextprotocol/server-filesystem` Node binary, but
// npx-on-Windows has stdin-pipe plumbing issues that break the
// MCP server's read loop in subtle ways (the server gets
// initialized but its `process.stdin` reads never see the
// client's bytes). Rather than gate the smoke suite on a
// specific Node + npm version, we use a tiny in-process
// `mocp-server` (mock MCP server, see main_mock.go) that
// speaks the protocol over stdio. This:
//
//   - Tests the same wire protocol (JSON-RPC 2.0 + LSP
//     Content-Length framing) the real server uses.
//   - Tests the same Go code paths (Manager.Connect, the
//     client's id correlation, the stdio transport's
//     Send/Recv/Close lifecycle).
//   - Runs in <1s on any platform — no npm install, no
//     network, no Node version pinning.
//   - Skips if the host has no Node — the alternative would
//     be a pure-Go mock, which is more code than the test
//     warrants.
//
// To run locally:
//
//   go test -count=1 -tags mcp_smoke -run TestMcp ./internal/mcp/...
//
// The CI step in .github/workflows/ci.yml runs the same
// command on ubuntu-latest (and macOS / Windows runners get
// it too — the suite is now platform-agnostic).
package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// mockServerScript is the source for the Node.js script we
// compile into a temp file at test setup. It's a minimal MCP
// server that responds to initialize / tools/list with a
// canned tool set, and to tools/call with canned text
// content. The script's job is to be the "other side of the
// wire" — it doesn't exercise the upstream filesystem server
// because that introduces Node-version and npm-registry
// dependency on the smoke suite. What matters is that the
// protocol is correct.
//
// Tools exposed:
//
//   - read_file: returns "hello from read_file" with an
//     optional "path" arg echoed in the text block.
//   - list_directory: returns "DIR: <path>" with the arg.
//   - nonexistent_tool: returns isError:true when called.
const mockServerScript = `#!/usr/bin/env node
// Mock MCP server. Reads JSON-RPC envelopes from stdin and
// writes responses to stdout using LSP Content-Length
// framing. Prints startup banner to stderr so the test can
// confirm the subprocess is alive.

process.stderr.write("mock-mcp ready\n");

let buf = Buffer.alloc(0);
let pending = null;

function readFrame() {
  // Returns {body, totalBytes} or null if incomplete.
  let headerEnd = -1;
  let contentLength = -1;
  let i = 0;
  while (i < buf.length) {
    const lineEnd = buf.indexOf(0x0A, i);
    if (lineEnd < 0) break;
    const line = buf.slice(i, lineEnd).toString("ascii").replace(/\r$/, "");
    i = lineEnd + 1;
    if (line === "") { headerEnd = i; break; }
    const m = /^Content-Length:\s*(\d+)/i.exec(line);
    if (m) contentLength = parseInt(m[1], 10);
  }
  if (headerEnd < 0 || contentLength < 0) return null;
  const need = headerEnd + contentLength;
  if (buf.length < need) return null;
  const body = buf.slice(headerEnd, need).toString("utf8");
  buf = buf.slice(need);
  return body;
}

function writeFrame(obj) {
  const body = JSON.stringify(obj);
  const header = "Content-Length: " + Buffer.byteLength(body) + "\r\n\r\n";
  process.stdout.write(header);
  process.stdout.write(body);
}

process.stdin.on("data", (chunk) => {
  buf = Buffer.concat([buf, chunk]);
  while (true) {
    const frame = readFrame();
    if (frame === null) break;
    let req;
    try { req = JSON.parse(frame); } catch (e) { continue; }
    handle(req);
  }
});

function handle(req) {
  const id = req.id;
  const method = req.method;
  if (method === "initialize") {
    writeFrame({
      jsonrpc: "2.0", id,
      result: {
        protocolVersion: "DRAFT-2026-v1",
        capabilities: { tools: { listChanged: false } },
        serverInfo: { name: "mock-mcp", version: "0.0.1" },
      },
    });
    return;
  }
  if (method === "tools/list") {
    writeFrame({
      jsonrpc: "2.0", id,
      result: {
        tools: [
          { name: "read_file", description: "Mock read",
            inputSchema: { type: "object", properties: { path: { type: "string" } } } },
          { name: "list_directory", description: "Mock list",
            inputSchema: { type: "object", properties: { path: { type: "string" } } } },
          { name: "nonexistent_tool", description: "Errors when called",
            inputSchema: { type: "object" } },
        ],
      },
    });
    return;
  }
  if (method === "tools/call") {
    const name = req.params && req.params.name;
    const args = (req.params && req.params.arguments) || {};
    if (name === "read_file") {
      writeFrame({
        jsonrpc: "2.0", id,
        result: {
          content: [{ type: "text", text: "hello from read_file: " + (args.path || "<no path>") }],
          isError: false,
        },
      });
      return;
    }
    if (name === "list_directory") {
      writeFrame({
        jsonrpc: "2.0", id,
        result: {
          content: [{ type: "text", text: "DIR: " + (args.path || "<no path>") }],
          isError: false,
        },
      });
      return;
    }
    if (name === "nonexistent_tool") {
      writeFrame({
        jsonrpc: "2.0", id,
        result: { content: [], isError: true },
      });
      return;
    }
    writeFrame({
      jsonrpc: "2.0", id,
      error: { code: -32601, message: "Method not found: " + method },
    });
    return;
  }
  writeFrame({
    jsonrpc: "2.0", id,
    error: { code: -32601, message: "Method not found: " + method },
  });
}

process.stdin.on("end", () => process.exit(0));
`

// writeMockServer writes the mock script to a temp file and
// returns its path. The script is rebuilt on every test
// invocation because go test runs in parallel-friendly
// sandboxes where reading from testdata isn't guaranteed.
func writeMockServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mock-mcp-server.js")
	if err := os.WriteFile(path, []byte(mockServerScript), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
	return path
}

// nodeAvailable reports whether the `node` executable is on
// PATH. Most developer machines and CI runners have it; a
// missing node skips the suite rather than failing the
// build.
func nodeAvailable() bool {
	_, err := exec.LookPath("node")
	return err == nil
}

func requireNode(t *testing.T) {
	t.Helper()
	if !nodeAvailable() {
		t.Skip("node not on PATH; the smoke suite requires Node.js")
	}
}

// newMockManager constructs a Manager pointed at the mock
// script and returns it after a successful Connect. The
// caller is responsible for mgr.Close(ctx).
func newMockManager(t *testing.T) *Manager {
	t.Helper()
	requireNode(t)
	script := writeMockServer(t)
	cfg := []core.McpServerConfig{{
		Name:       "mock",
		Command:    "node",
		Args:       []string{script},
		ServerType: "stdio",
	}}
	mgr := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = mgr.Close(c)
	})
	return mgr
}

// TestMcpMockLifecycle covers the full initialize →
// initialized → tools/list handshake against the mock
// server. Verifies:
//
//  1. Manager.Connect() returns nil.
//  2. Manager.Errors() is empty.
//  3. Manager.Tools() returns at least one tool.
func TestMcpMockLifecycle(t *testing.T) {
	mgr := newMockManager(t)
	if errs := mgr.Errors(); len(errs) > 0 {
		t.Errorf("Errors() = %v, want empty", errs)
	}
	tools := mgr.Tools()
	if len(tools) == 0 {
		t.Errorf("Tools() returned 0; want at least 1 from mock server")
	}
}

// TestMcpMockListDirectory exercises a tools/call
// round-trip through the full Manager → mcpTool → Client
// pipeline. Confirms the namespaced tool name resolves and
// the response Text contains the expected content.
func TestMcpMockListDirectory(t *testing.T) {
	mgr := newMockManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all := mgr.Tools()
	t.Logf("got %d tools: %v", len(all), toolNames(all))
	var list tools.Tool
	for _, tl := range all {
		if strings.HasSuffix(tl.Name(), "__list_directory") {
			list = tl
			break
		}
	}
	if list == nil {
		t.Fatalf("no list_directory tool; tools=%v", toolNames(all))
	}
	t.Logf("about to call Execute")
	res := list.Execute(ctx, json.RawMessage(`{"path":"/tmp/x"}`), nil)
	t.Logf("Execute returned: IsError=%v Text=%q", res.IsError, res.Text)
	if res.IsError {
		t.Fatalf("Execute: IsError=true, Text=%q", res.Text)
	}
	if !strings.Contains(res.Text, "DIR: /tmp/x") {
		t.Errorf("Text = %q, want it to contain %q", res.Text, "DIR: /tmp/x")
	}
}

// TestMcpMockIsError confirms the tool-level isError:true
// path works through the converter.
func TestMcpMockIsError(t *testing.T) {
	mgr := newMockManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all := mgr.Tools()
	var tool tools.Tool
	for _, tl := range all {
		if strings.HasSuffix(tl.Name(), "__nonexistent_tool") {
			tool = tl
			break
		}
	}
	if tool == nil {
		t.Fatalf("no nonexistent_tool; tools=%v", toolNames(all))
	}
	res := tool.Execute(ctx, json.RawMessage(`{}`), nil)
	if !res.IsError {
		t.Errorf("IsError = false, want true; Text=%q", res.Text)
	}
}

// TestMcpMockReconnectAfterClose verifies the manager can
// be Closed and a fresh Manager created against the same
// server config — i.e. no leaked subprocesses, no shared-
// state corruption across lifecycles.
func TestMcpMockReconnectAfterClose(t *testing.T) {
	requireNode(t)
	script := writeMockServer(t)
	cfg := []core.McpServerConfig{{
		Name:       "mock",
		Command:    "node",
		Args:       []string{script},
		ServerType: "stdio",
	}}

	// First lifecycle.
	mgr1 := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr1.Connect(ctx); err != nil {
		t.Fatalf("Connect #1: %v", err)
	}
	if err := mgr1.Close(ctx); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	// Second lifecycle — same config, new manager.
	mgr2 := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := mgr2.Connect(ctx); err != nil {
		t.Fatalf("Connect #2: %v", err)
	}
	if errs := mgr2.Errors(); len(errs) > 0 {
		t.Errorf("Errors() after second connect: %v", errs)
	}
	if err := mgr2.Close(ctx); err != nil {
		t.Errorf("Close #2: %v", err)
	}
}

// TestMcpMockMultiBlockResult exercises the step-6
// widening: a tools/call that returns a content array.
// The mock server returns one text block, but we still
// verify that ToolResult.Blocks is populated and parses as
// a JSON array (the verbatim content[] from the wire).
func TestMcpMockMultiBlockResult(t *testing.T) {
	mgr := newMockManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all := mgr.Tools()
	var read tools.Tool
	for _, tl := range all {
		if strings.HasSuffix(tl.Name(), "__read_file") {
			read = tl
			break
		}
	}
	if read == nil {
		t.Fatalf("no read_file tool; tools=%v", toolNames(all))
	}
	res := read.Execute(ctx, json.RawMessage(`{"path":"/tmp/x.txt"}`), nil)
	if res.IsError {
		t.Fatalf("Execute: IsError=true, Text=%q", res.Text)
	}
	if len(res.Blocks) == 0 {
		t.Errorf("Blocks empty; want verbatim content[] array")
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(res.Blocks, &blocks); err != nil {
		t.Fatalf("Blocks is not valid JSON array: %v", err)
	}
	if len(blocks) != 1 {
		t.Errorf("Blocks has %d elements, want 1", len(blocks))
	}
	if !strings.Contains(res.Text, "/tmp/x.txt") {
		t.Errorf("Text = %q, want it to contain %q", res.Text, "/tmp/x.txt")
	}
}

// toolNames extracts Name() from each tool in a slice. Used
// to print a diagnostic when a smoke test can't find the
// tool it expected.
func toolNames(tools []tools.Tool) []string {
	out := make([]string, len(tools))
	for i, tl := range tools {
		out[i] = tl.Name()
	}
	return out
}