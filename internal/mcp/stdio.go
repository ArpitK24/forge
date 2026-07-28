package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ArpitK24/forge/internal/core"
)

// shutdownGrace is how long we wait for the subprocess to
// exit cleanly after closing stdin (signals a graceful EOF
// for well-behaved MCP servers). After this, the process is
// hard-killed.
const shutdownGrace = 2 * time.Second

// StdioTransport spawns a subprocess and communicates with
// it over its stdio using Content-Length-framed JSON-RPC
// (the LSP/MCP convention). It is the only production
// transport in Phase 4 step 5.
//
// Concurrency: Send is mutex-protected (one write per frame
// must be atomic at the OS level for framing correctness).
// Recv is single-goroutine (only the per-server client's
// read goroutine calls it). Close is idempotent.
type StdioTransport struct {
	cfg    core.McpServerConfig
	mu     sync.Mutex // serializes Send
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr io.ReadCloser
	closed bool
}

// NewStdioTransport constructs a transport but does NOT
// start the subprocess. Call Start(ctx) to spawn the
// process and prepare stdio pipes.
func NewStdioTransport(cfg core.McpServerConfig) *StdioTransport {
	return &StdioTransport{cfg: cfg}
}

// Start spawns the subprocess. After Start returns nil,
// Send and Recv are valid until Close is called.
//
// The subprocess's env is cfg.Env merged on top of
// os.Environ() — matches the existing convention in
// internal/tools/bash.go. A user expecting "extra env only"
// (PATH scrubbed) will be surprised; that's a documented
// risk in the plan (H4).
func (t *StdioTransport) Start(ctx context.Context) error {
	if t.cmd != nil {
		return core.Newf(core.KindMCP, "stdio transport already started for %q", t.cfg.Name)
	}
	if t.cfg.Command == "" {
		return core.Newf(core.KindConfig, "mcp server %q: missing command", t.cfg.Name)
	}
	cmd := exec.CommandContext(ctx, t.cfg.Command, t.cfg.Args...)
	env := os.Environ()
	for k, v := range t.cfg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return core.Wrap(core.KindIO, err, fmt.Sprintf("mcp %q: stdin pipe", t.cfg.Name))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return core.Wrap(core.KindIO, err, fmt.Sprintf("mcp %q: stdout pipe", t.cfg.Name))
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return core.Wrap(core.KindIO, err, fmt.Sprintf("mcp %q: stderr pipe", t.cfg.Name))
	}

	// Per-platform process-group setup so a stuck server can
	// be hard-killed. Mirrors internal/tools/bash_unix.go
	// and bash_windows_setup.go patterns.
	applyProcessGroupSetup(cmd)

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return core.Wrap(core.KindIO, err, fmt.Sprintf("mcp %q: spawn", t.cfg.Name))
	}

	t.cmd = cmd
	t.stdin = stdin
	t.stdout = bufio.NewReaderSize(stdout, 64*1024)
	t.stderr = stderr
	return nil
}

// Send writes one framed message. Mutex-protected so
// concurrent callers (the per-server client serializes
// call() and notify() at the transport boundary) get one
// atomic write per frame.
func (t *StdioTransport) Send(body []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil || t.closed {
		return core.Newf(core.KindMCP, "mcp %q: send on closed transport", t.cfg.Name)
	}
	if _, err := writeFrame(t.stdin, body); err != nil {
		return core.Wrap(core.KindMCP, err, fmt.Sprintf("mcp %q: send", t.cfg.Name))
	}
	return nil
}

// Recv blocks until one framed message arrives or ctx is
// cancelled. The framing is synchronous on a single bufio
// reader; a peer that sends nothing will block the goroutine
// until ctx is cancelled or the process dies (which closes
// stdout, surfacing an EOF).
func (t *StdioTransport) Recv(ctx context.Context) ([]byte, error) {
	if t.stdout == nil {
		return nil, core.Newf(core.KindMCP, "mcp %q: recv before start", t.cfg.Name)
	}
	// We can't truly "select" on a blocking Read from
	// bufio.Reader. Instead, run readFrame in a goroutine
	// and race it against ctx.Done(). The goroutine is
	// short-lived — if ctx wins, the read eventually returns
	// and we discard its result.
	type result struct {
		body []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		body, err := readFrame(t.stdout)
		ch <- result{body, err}
	}()
	select {
	case <-ctx.Done():
		// Don't leak the goroutine — but we can't cancel a
		// in-flight readFrame on a bufio.Reader. The
		// subprocess death (via exec.CommandContext) will
		// close stdout, which trips an EOF in the goroutine.
		return nil, core.Wrap(core.KindCancelled, ctx.Err(), fmt.Sprintf("mcp %q: recv cancelled", t.cfg.Name))
	case r := <-ch:
		if r.err != nil {
			// readFrame's error is always *core.Error (verified
			// by framing_test.go). If somehow it isn't, fall
			// back to KindMCP — the peer's framing was malformed
			// enough to make our classifier not run.
			var kind core.ErrorKind = core.KindMCP
			var ce *core.Error
			if errors.As(r.err, &ce) {
				kind = ce.Kind
			}
			return nil, core.Wrap(kind, r.err, fmt.Sprintf("mcp %q: recv", t.cfg.Name))
		}
		return r.body, nil
	}
}

// Close shuts the subprocess. Order:
//
//  1. Close stdin — signals a graceful EOF to well-behaved
//     MCP servers, which then run their shutdown handler.
//  2. Wait up to shutdownGrace for the process to exit.
//  3. If still alive, hard-kill.
//
// Idempotent: a second Close returns nil immediately.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.cmd == nil {
		t.closed = true
		return nil
	}
	t.closed = true

	// Close stdin first; ignore error (already closed? fine).
	_ = t.stdin.Close()

	// Wait up to shutdownGrace, then hard-kill if needed.
	done := make(chan struct{})
	go func() {
		_ = t.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(shutdownGrace):
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
			<-done // reaped
		}
		return nil
	}
}

// Stderr is exposed for callers that want to surface
// subprocess stderr (e.g. an MCP server logging to stderr).
// Returns nil before Start.
func (t *StdioTransport) Stderr() io.ReadCloser { return t.stderr }
