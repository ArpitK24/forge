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
//
// Stderr handling: the subprocess's stderr is captured and
// drained in a background goroutine so a chatty server
// cannot deadlock on a full pipe buffer (~64KB on Linux).
// Stderr is not surfaced to the user today — the manager
// logs at debug level only. Future step: structured
// severity propagation.
type StdioTransport struct {
	cfg    core.McpServerConfig
	mu     sync.Mutex // serializes Send
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr io.ReadCloser
	closed bool

	// stderrDone is closed when the stderr drain goroutine
	// exits. Close waits on it (with a short grace) so the
	// goroutine doesn't outlive the transport.
	stderrDone chan struct{}
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
// The ctx parameter is accepted for interface symmetry with
// future transports (the HTTP stub takes one) but is unused
// here — the subprocess is registered with a context.Background
// so its lifetime is governed by StdioTransport.Close, not by
// the caller's Connect sub-context.
//
// The subprocess's env is cfg.Env merged on top of
// os.Environ() — matches the existing convention in
// internal/tools/bash.go. A user expecting "extra env only"
// (PATH scrubbed) will be surprised; that's a documented
// risk in the plan (H4).
func (t *StdioTransport) Start(ctx context.Context) error {
	_ = ctx // unused; see comment above
	if t.cmd != nil {
		return core.Newf(core.KindMCP, "stdio transport already started for %q", t.cfg.Name)
	}
	if t.cfg.Command == "" {
		return core.Newf(core.KindConfig, "mcp server %q: missing command", t.cfg.Name)
	}
	// We must use CommandContext (not plain Command) because
	// applyProcessGroupSetup sets cmd.Cancel — Go's exec package
	// requires that any cmd with a Cancel was built via
	// CommandContext. The context we pass here is the transport's
	// lifetime context, which is closed by StdioTransport.Close
	// (not by the caller's ctx). Passing the caller's ctx would
	// tie the subprocess to Connect's per-server sub-ctx — which
	// is cancelled when connectOne returns — and SIGKILL the
	// server before any tools/call can be made.
	cmd := exec.CommandContext(context.Background(), t.cfg.Command, t.cfg.Args...)
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
	t.stderrDone = make(chan struct{})
	// Drain stderr so the subprocess cannot block on a full
	// pipe buffer. A server that writes ~64KB+ of stderr will
	// block on write() otherwise, and our Recv goroutine will
	// never see EOF — the server hangs forever. We don't
	// surface stderr today; future step will plumb it back
	// to the user through the manager's error channel.
	go func() {
		defer close(t.stderrDone)
		_, _ = io.Copy(io.Discard, stderr)
	}()
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
//
// Note: in the current client.go, the read loop calls
// Recv with context.Background(), so the ctx.Done() branch
// is unreachable. The dual-goroutine is kept defensively
// in case a future caller (e.g. a per-call timeout) wants
// to cancel a single Recv without tearing down the transport.
func (t *StdioTransport) Recv(ctx context.Context) ([]byte, error) {
	if t.stdout == nil {
		return nil, core.Newf(core.KindMCP, "mcp %q: recv before start", t.cfg.Name)
	}
	// We can't truly "select" on a blocking Read from a
	// bufio.Reader. Instead, run readFrame in a goroutine
	// and race it against ctx.Done(). If ctx wins, the
	// read goroutine stays blocked until the subprocess
	// dies (closing stdout → EOF) and is then GC'd. That's
	// acceptable because the read goroutine is short-lived
	// per Recv call and the subprocess is owned by us.
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
//  2. On Windows, also signal cmd.Cancel (Ctrl+Break to the
//     process group) so servers with subprocess children get
//     a clean shutdown. cmd.WaitDelay (`shutdownGrace`) means
//     a hard kill follows automatically if Cancel doesn't
//     unblock the process. On Unix, stdin-close is enough;
//     subprocesses inherit the parent's death via process
//     group teardown.
//  3. Wait up to shutdownGrace for the process to exit.
//  4. If still alive, hard-kill.
//  5. Wait for the stderr drain goroutine to exit (shortly
//     after the pipe closes when the process dies).
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

	// On Windows, cmd.Cancel sends Ctrl+Break to the process
	// group (cmd.WaitDelay then escalates to a hard kill). On
	// Unix, cmd.Cancel is nil and this is a no-op — the
	// process-group teardown is already handled by Setpgid +
	// the trailing Kill below.
	if t.cmd.Cancel != nil {
		_ = t.cmd.Cancel()
	}

	// Wait up to shutdownGrace, then hard-kill if needed.
	done := make(chan struct{})
	go func() {
		_ = t.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
			<-done // reaped
		}
	}

	// Drain stderr goroutine. It exits when EOF arrives on
	// the stderr pipe, which happens when the subprocess
	// dies. Bounded by a short grace so a wedged server
	// can't block Close indefinitely.
	if t.stderrDone != nil {
		select {
		case <-t.stderrDone:
		case <-time.After(shutdownGrace):
		}
	}
	return nil
}

// Stderr is exposed for callers that want to surface
// subprocess stderr (e.g. an MCP server logging to stderr).
// Returns nil before Start.
func (t *StdioTransport) Stderr() io.ReadCloser { return t.stderr }
