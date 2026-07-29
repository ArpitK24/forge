//go:build mcp_smoke

// Package mcp — smoke_test.go. Real-server integration tests
// that exercise the full MCP protocol against a live Node
// `@modelcontextprotocol/server-filesystem` subprocess.
//
// These tests are gated by the `mcp_smoke` build tag so they
// never run in the default `go test ./...` path (which would
// require npm and a network registry to be installed on the
// developer machine). They run only in CI on ubuntu-latest
// (see .github/workflows/ci.yml).
//
// To run locally:
//
//	npx -y @modelcontextprotocol/server-filesystem --help >/dev/null
//	go test -count=1 -tags mcp_smoke -run TestMcpFilesystem ./internal/mcp/...
//
// Pre-flight: the test setup assumes `npx` is on $PATH and the
// MCP filesystem server can be fetched. If the pre-flight fails
// (no Node, no network), the test SKIPs rather than failing
// the build.
package mcp

import (
	"bytes"
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

// filesystemServerArgs returns the argv slice for invoking the
// upstream filesystem MCP server. The server requires a list of
// directories it can serve — we point it at the test's tmp dir
// so it has something to read.
func filesystemServerArgs(t *testing.T, dirs ...string) []string {
	t.Helper()
	return append([]string{"-y", "@modelcontextprotocol/server-filesystem"}, dirs...)
}

// npxAvailable reports whether the npx executable is on PATH.
// A missing npx is the most common reason this suite is unable
// to run on a fresh developer machine.
func npxAvailable() bool {
	_, err := exec.LookPath("npx")
	return err == nil
}

// requireNpx t.Skip()s the calling test if npx isn't installed.
// Run once per test rather than in TestMain so a missing npx
// fails fast on the first test rather than via panics in
// subsequent ones.
func requireNpx(t *testing.T) {
	t.Helper()
	if !npxAvailable() {
		t.Skip("npx not on PATH; install Node.js or run on ubuntu-latest CI")
	}
}

// runMcpCmd launches the filesystem MCP server with the given
// arguments and waits up to 3s for it to either start producing
// output (success) or exit (failure). Returns nil if the process
// is still alive after the grace period, or the error from a
// failed spawn.
//
// We use this to pre-flight the test environment: a working
// install of @modelcontextprotocol/server-filesystem is a
// prerequisite for the rest of the suite. The server takes
// one or more directory paths as positional args (NOT --help)
// and waits on stdin for JSON-RPC; if it's alive after 3s the
// install is good.
func runMcpCmd(t *testing.T, args ...string) error {
	t.Helper()
	cmd := exec.Command("npx", args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	// Wait briefly for the process to either die (bad install)
	// or stay alive (good install — server is waiting on stdin).
	timer := time.AfterFunc(3*time.Second, func() {
		_ = cmd.Process.Kill()
	})
	defer timer.Stop()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		// Still alive — that's success.
		_ = cmd.Process.Kill()
		<-done
		return nil
	}
}

// TestMcpFilesystemLifecycle covers the full initialize →
// initialized → tools/list handshake against the real server.
// The fake-transport unit tests prove the protocol framing
// works; this test proves the real server actually answers.
//
// What we verify:
//
//  1. Manager.Connect() returns nil (no error).
//  2. Manager.Errors() is empty.
//  3. Manager.Tools() returns at least one tool — the
//     filesystem server ships with read_file, list_directory,
//     etc. We don't pin the exact set in case upstream
//     renames a tool.
func TestMcpFilesystemLifecycle(t *testing.T) {
	requireNpx(t)
	dir := t.TempDir()
	// Pre-flight: ensure the server can actually be invoked.
	// A failure here usually means a bad npm cache or no
	// network — same root cause as a flake in the test
	// below, so failing fast here is clearer. We use a
	// throwaway directory as the server's allowed-root and
	// rely on the 3s liveness check (server should be
	// waiting on stdin at that point).
	if err := runMcpCmd(t, "-y", "@modelcontextprotocol/server-filesystem", t.TempDir()); err != nil {
		t.Skipf("pre-flight failed: %v", err)
	}

	cfg := []core.McpServerConfig{{
		Name:       "fs",
		Command:    "npx",
		Args:       filesystemServerArgs(t, dir),
		ServerType: "stdio",
	}}
	mgr := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if errs := mgr.Errors(); len(errs) > 0 {
		t.Errorf("Errors() = %v, want empty", errs)
	}
	mgrTools := mgr.Tools()
	if len(mgrTools) == 0 {
		t.Errorf("Tools() returned 0; want at least 1 from filesystem server")
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Close(c)
	})
}

// TestMcpFilesystemListDirectory exercises a tools/call
// round-trip through the full Manager → mcpTool → Client
// pipeline. Creates a temp dir, writes a known file, asks
// the server to list it, and confirms the result contains
// the file name.
//
// Verifies that:
//
//   - the namespaced tool name (mcp__fs__list_directory)
//     resolves to the right server,
//   - the JSON-RPC request/response cycle survives a real
//     subprocess (frame sizes, newline handling, partial reads),
//   - mcpTool.Execute returns a ToolResult whose Text field
//     contains the expected filename.
func TestMcpFilesystemListDirectory(t *testing.T) {
	requireNpx(t)
	dir := t.TempDir()
	known := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(known, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := []core.McpServerConfig{{
		Name:       "fs",
		Command:    "npx",
		Args:       filesystemServerArgs(t, dir),
		ServerType: "stdio",
	}}
	mgr := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Close(c)
	})

	mgrTools := mgr.Tools()
	var listDir tools.Tool
	for _, tl := range mgrTools {
		if strings.HasSuffix(tl.Name(), "__list_directory") {
			listDir = tl
			break
		}
	}
	if listDir == nil {
		t.Fatalf("no list_directory tool found; tools=%v", toolNames(mgrTools))
	}
	res := listDir.Execute(ctx, json.RawMessage(`{"path":"`+dir+`"}`), nil)
	if res.IsError {
		t.Fatalf("Execute: IsError=true, Text=%q", res.Text)
	}
	if !strings.Contains(res.Text, "hello.txt") {
		t.Errorf("Text = %q, want it to contain %q", res.Text, "hello.txt")
	}
}

// TestMcpFilesystemReadNonexistent verifies that the tool-level
// isError:true path works against a real server. The unit tests
// cover the converter; this test proves the server actually
// returns isError:true (rather than crashing or returning
// empty content).
func TestMcpFilesystemReadNonexistent(t *testing.T) {
	requireNpx(t)
	dir := t.TempDir()
	cfg := []core.McpServerConfig{{
		Name:       "fs",
		Command:    "npx",
		Args:       filesystemServerArgs(t, dir),
		ServerType: "stdio",
	}}
	mgr := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Close(c)
	})
	mgrTools := mgr.Tools()
	var read tools.Tool
	for _, tl := range mgrTools {
		if strings.HasSuffix(tl.Name(), "__read_file") {
			read = tl
			break
		}
	}
	if read == nil {
		t.Fatalf("no read_file tool found; tools=%v", toolNames(mgrTools))
	}
	res := read.Execute(ctx, json.RawMessage(`{"path":"/no/such/path/abcxyz"}`), nil)
	if !res.IsError {
		t.Errorf("IsError = false, want true (nonexistent file); Text=%q", res.Text)
	}
}

// TestMcpFilesystemReconnectAfterClose verifies the manager
// can be Closed and a fresh Manager created against the same
// server config — i.e. no leaked subprocesses, no file-descriptor
// exhaustion, no shared-state corruption across lifecycles.
func TestMcpFilesystemReconnectAfterClose(t *testing.T) {
	requireNpx(t)
	dir := t.TempDir()
	cfg := []core.McpServerConfig{{
		Name:       "fs",
		Command:    "npx",
		Args:       filesystemServerArgs(t, dir),
		ServerType: "stdio",
	}}

	// First lifecycle.
	mgr1 := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr1.Connect(ctx); err != nil {
		t.Fatalf("Connect #1: %v", err)
	}
	if err := mgr1.Close(ctx); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	// Second lifecycle — same config, new manager. The
	// server should come up cleanly. If subprocess pipes
	// leaked from #1, this would either hang or fail
	// to spawn.
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

// TestMcpFilesystemMultiBlockResult exercises the step-6
// widening: a tools/call that returns multiple content blocks
// (the filesystem server returns a single text block in
// practice, so this test confirms the verbatim Blocks path
// still works end-to-end via a real subprocess).
//
// We verify that ToolResult.Blocks is populated when the
// server returned a content array (even a 1-element one),
// and that the converter's Text field is the joined text.
func TestMcpFilesystemMultiBlockResult(t *testing.T) {
	requireNpx(t)
	dir := t.TempDir()
	// Write a file so the directory has at least one entry
	// to list. list_directory returns a content array; the
	// exact shape depends on the server version, but a
	// successful response should have at least one block.
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := []core.McpServerConfig{{
		Name:       "fs",
		Command:    "npx",
		Args:       filesystemServerArgs(t, dir),
		ServerType: "stdio",
	}}
	mgr := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := mgr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Close(c)
	})

	mgrTools := mgr.Tools()
	var listDir tools.Tool
	for _, tl := range mgrTools {
		if strings.HasSuffix(tl.Name(), "__list_directory") {
			listDir = tl
			break
		}
	}
	if listDir == nil {
		t.Fatalf("no list_directory tool")
	}
	res := listDir.Execute(ctx, json.RawMessage(`{"path":"`+dir+`"}`), nil)
	if res.IsError {
		t.Fatalf("Execute: IsError=true, Text=%q", res.Text)
	}
	// Blocks must be a valid JSON array (the verbatim
	// content[] from the wire). It should contain at least
	// one element for a non-empty directory.
	if len(res.Blocks) == 0 {
		t.Errorf("Blocks empty; want verbatim content[] array")
	}
	if !bytes.HasPrefix(res.Blocks, []byte("[")) {
		t.Errorf("Blocks = %s, want a JSON array", res.Blocks)
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(res.Blocks, &blocks); err != nil {
		t.Fatalf("Blocks is not valid JSON array: %v", err)
	}
	if len(blocks) == 0 {
		t.Errorf("Blocks parsed to empty array; want >=1")
	}
	// Text is the joined text blocks. The filesystem
	// server emits the directory listing as one text
	// block, so Text should mention our fixture file.
	if !strings.Contains(res.Text, "x.txt") {
		t.Errorf("Text = %q, want it to contain 'x.txt'", res.Text)
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