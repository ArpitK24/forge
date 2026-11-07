package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// This file is the stdio interop test. It runs the real
// Manager → client → StdioTransport stack against a separate
// process that speaks the MCP stdio transport exactly as the
// spec defines it, independent of framing.go:
//
//	https://modelcontextprotocol.io/specification/2025-06-18/basic/transports#stdio
//	"Messages are delimited by newlines, and MUST NOT contain
//	 embedded newlines."
//
// The peer is this test binary re-executed with
// FORGE_MCP_SPEC_PEER=1, so it needs no Node, Python or network
// and runs on every CI leg, Windows included. It is strict: any
// stdin line that is not a single JSON-RPC message (for example
// an LSP-style "Content-Length: N" header) makes it log the line
// to stderr and exit 3, the way a real server's parser would
// reject it.

const specPeerEnv = "FORGE_MCP_SPEC_PEER"

// TestSpecPeerHelper is not a test. When the test binary is
// re-executed with FORGE_MCP_SPEC_PEER=1 it becomes the peer
// server; otherwise it returns immediately.
func TestSpecPeerHelper(t *testing.T) {
	if os.Getenv(specPeerEnv) != "1" {
		t.Skip("helper process for the stdio spec interop tests")
	}
	os.Exit(runSpecPeer(os.Stdin, os.Stdout, os.Stderr))
}

// runSpecPeer is a minimal MCP server over newline-delimited
// JSON-RPC. It reads with its own bufio.Reader and writes with
// json.Marshal + "\n", deliberately sharing no code with
// framing.go.
func runSpecPeer(in io.Reader, out io.Writer, errw io.Writer) int {
	fmt.Fprintln(errw, "spec-peer ready")
	r := bufio.NewReaderSize(in, 1<<20)
	initialized := false
	write := func(msg map[string]any) {
		b, err := json.Marshal(msg)
		if err != nil {
			fmt.Fprintf(errw, "spec-peer: marshal: %v\n", err)
			os.Exit(4)
		}
		line := append(b, '\n')
		// Split each message across two writes so the client
		// has to reassemble a line that arrives in pieces.
		half := len(line) / 2
		_, _ = out.Write(line[:half])
		time.Sleep(time.Millisecond)
		_, _ = out.Write(line[half:])
	}
	for {
		raw, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && raw == "" {
				return 0 // client closed stdin: clean shutdown
			}
			fmt.Fprintf(errw, "spec-peer: read: %v (partial %q)\n", err, raw)
			return 3
		}
		line := strings.TrimSuffix(raw, "\n")
		if strings.ContainsAny(line, "\r\n") || strings.TrimSpace(line) == "" {
			fmt.Fprintf(errw, "spec-peer: not one message per line: %q\n", raw)
			return 3
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id,omitempty"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil || req.JSONRPC != "2.0" {
			fmt.Fprintf(errw, "spec-peer: stdin line is not a JSON-RPC message: %q\n", line)
			return 3
		}
		isRequest := len(req.ID) > 0
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			write(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"protocolVersion": p.ProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "spec-peer", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
			initialized = true
		case "tools/list":
			if !initialized {
				fmt.Fprintln(errw, "spec-peer: tools/list before notifications/initialized")
				return 3
			}
			write(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []any{map[string]any{
					"name":        "echo",
					"description": "Echo the text argument",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				}}},
			})
		case "tools/call":
			var p struct {
				Name      string `json:"name"`
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			// An unrelated notification first, so the client
			// must handle interleaved traffic on the stream.
			write(map[string]any{
				"jsonrpc": "2.0", "method": "notifications/message",
				"params": map[string]any{"level": "info", "data": "calling " + p.Name},
			})
			write(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "echo: " + p.Arguments.Text}},
					"isError": false,
				},
			})
		default:
			if isRequest {
				write(map[string]any{
					"jsonrpc": "2.0", "id": req.ID,
					"error": map[string]any{"code": -32601, "message": "Method not found: " + req.Method},
				})
			}
		}
	}
}

// specPeerConfig returns a server config that re-executes this
// test binary as the spec peer.
func specPeerConfig(t *testing.T) core.McpServerConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate test binary: %v", err)
	}
	return core.McpServerConfig{
		Name:       "spec",
		Command:    exe,
		Args:       []string{"-test.run=^TestSpecPeerHelper$"},
		Env:        map[string]string{specPeerEnv: "1"},
		ServerType: "stdio",
	}
}

// TestStdioSpecPeer_Interop is the regression test for the
// framing bug: with LSP Content-Length framing the peer rejects
// the very first line and initialize fails. With spec framing
// the handshake, tools/list and a tools/call (multi-line, UTF-8
// text; interleaved notification; response split across writes)
// all succeed.
func TestStdioSpecPeer_Interop(t *testing.T) {
	mgr := NewManager([]core.McpServerConfig{specPeerConfig(t)},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Close(c)
	})
	if err := mgr.Connect(ctx); err != nil {
		t.Fatalf("Connect to spec-conformant stdio server: %v", err)
	}
	var echo tools.Tool
	for _, tl := range mgr.Tools() {
		if strings.HasSuffix(tl.Name(), "__echo") {
			echo = tl
		}
	}
	if echo == nil {
		t.Fatalf("echo tool not listed; tools=%d", len(mgr.Tools()))
	}
	text := "line one\nline two — héllo 世界 🚀"
	input, _ := json.Marshal(map[string]string{"text": text})
	for i := 0; i < 3; i++ {
		res := echo.Execute(ctx, input, nil)
		if res.IsError {
			t.Fatalf("call %d: IsError=true, Text=%q", i, res.Text)
		}
		if !strings.Contains(res.Text, "echo: "+text) {
			t.Fatalf("call %d: Text = %q, want it to contain %q", i, res.Text, "echo: "+text)
		}
	}
}

// TestStdioSpecPeer_TransportWire drives StdioTransport directly
// against the peer: what Send writes must be accepted as one
// message, and what the peer writes must come back from Recv as
// exactly one JSON object.
func TestStdioSpecPeer_TransportWire(t *testing.T) {
	tr := NewStdioTransport(specPeerConfig(t))
	if err := tr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer tr.Close()

	if err := tr.Send([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	body, err := tr.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv initialize response: %v", err)
	}
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("Recv returned %q, not one JSON message: %v", body, err)
	}
	if resp.ID != 1 || resp.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("response = %s, want id 1 with protocolVersion 2025-06-18", body)
	}
}
