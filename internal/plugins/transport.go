package plugins

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
// for well-behaved plugins). After this, the process is
// hard-killed.
const shutdownGrace = 2 * time.Second

// Transport is the interface a plugin connection uses to
// communicate with its subprocess. Mirrors the MCP Transport
// interface for consistency.
type Transport interface {
	Start(ctx context.Context) error
	Send(body []byte) error
	Recv(ctx context.Context) ([]byte, error)
	Close() error
}

// StdioTransport spawns a plugin subprocess and communicates
// with it over its stdio using Content-Length-framed JSON-RPC
// (the LSP/MCP convention).
//
// Concurrency: Send is mutex-protected (one write per frame
// must be atomic at the OS level for framing correctness).
// Recv is single-goroutine (only the per-plugin client's
// read goroutine calls it). Close is idempotent.
type StdioTransport struct {
	cfg       PluginConfig
	mu        sync.Mutex // serializes Send
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	stderr    io.ReadCloser
	closed    bool
	stderrDone chan struct{}
}

// PluginConfig holds the configuration for a single plugin.
type PluginConfig struct {
	Name      string
	Path      string // absolute path to the plugin binary
	Args      []string
	Env       map[string]string
}

// NewStdioTransport constructs a transport but does NOT
// start the subprocess. Call Start(ctx) to spawn the
// process and prepare stdio pipes.
func NewStdioTransport(cfg PluginConfig) *StdioTransport {
	return &StdioTransport{cfg: cfg}
}

// Start spawns the subprocess. After Start returns nil,
// Send and Recv are valid until Close is called.
func (t *StdioTransport) Start(ctx context.Context) error {
	_ = ctx // unused; see comment above
	if t.cmd != nil {
		return core.Newf(core.KindMCP, "plugin transport already started for %q", t.cfg.Name)
	}
	if t.cfg.Path == "" {
		return core.Newf(core.KindConfig, "plugin %q: missing path", t.cfg.Name)
	}

	cmd := exec.CommandContext(context.Background(), t.cfg.Path, t.cfg.Args...)
	env := os.Environ()
	for k, v := range t.cfg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return core.Wrap(core.KindIO, err, fmt.Sprintf("plugin %q: stdin pipe", t.cfg.Name))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return core.Wrap(core.KindIO, err, fmt.Sprintf("plugin %q: stdout pipe", t.cfg.Name))
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return core.Wrap(core.KindIO, err, fmt.Sprintf("plugin %q: stderr pipe", t.cfg.Name))
	}

	// Per-platform process-group setup so a stuck plugin can
	// be hard-killed. Mirrors internal/tools/bash_unix.go
	// and bash_windows_setup.go patterns.
	applyProcessGroupSetup(cmd)

	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return core.Wrap(core.KindIO, err, fmt.Sprintf("plugin %q: spawn", t.cfg.Name))
	}

	t.cmd = cmd
	t.stdin = stdin
	t.stdout = bufio.NewReaderSize(stdout, 64*1024)
	t.stderr = stderr
	t.stderrDone = make(chan struct{})

	// Drain stderr so the subprocess cannot block on a full
	// pipe buffer. A plugin that writes ~64KB+ of stderr will
	// block on write() otherwise, and our Recv goroutine will
	// never see EOF — the plugin hangs forever.
	go func() {
		defer close(t.stderrDone)
		_, _ = io.Copy(io.Discard, stderr)
	}()
	return nil
}

// Send writes one framed message. Mutex-protected so
// concurrent callers get one atomic write per frame.
func (t *StdioTransport) Send(body []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cmd == nil || t.closed {
		return core.Newf(core.KindMCP, "plugin %q: send on closed transport", t.cfg.Name)
	}
	if _, err := writeFrame(t.stdin, body); err != nil {
		return core.Wrap(core.KindMCP, err, fmt.Sprintf("plugin %q: send", t.cfg.Name))
	}
	return nil
}

// Recv blocks until one framed message arrives or ctx is
// cancelled.
func (t *StdioTransport) Recv(ctx context.Context) ([]byte, error) {
	if t.stdout == nil {
		return nil, core.Newf(core.KindMCP, "plugin %q: recv before start", t.cfg.Name)
	}
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
		return nil, core.Wrap(core.KindCancelled, ctx.Err(), fmt.Sprintf("plugin %q: recv cancelled", t.cfg.Name))
	case r := <-ch:
		if r.err != nil {
			var kind core.ErrorKind = core.KindMCP
			var ce *core.Error
			if errors.As(r.err, &ce) {
				kind = ce.Kind
			}
			return nil, core.Wrap(kind, r.err, fmt.Sprintf("plugin %q: recv", t.cfg.Name))
		}
		return r.body, nil
	}
}

// Close shuts the subprocess. Order:
//
//  1. Close stdin — signals a graceful EOF to well-behaved
//     plugins, which then run their shutdown handler.
//  2. On Windows, also signal cmd.Cancel (Ctrl+Break to the
//     process group) so plugins with subprocess children get
//     a clean shutdown. cmd.WaitDelay (shutdownGrace) means
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

	// Wait for graceful exit.
	select {
	case <-time.After(shutdownGrace):
		// Hard kill.
		_ = t.cmd.Process.Kill()
	case <-func() chan struct{} {
		done := make(chan struct{}, 1)
		go func() {
			_ = t.cmd.Wait()
			done <- struct{}{}
		}()
		return done
	}():
	}

	// Wait for stderr drain to finish (short grace).
	select {
	case <-t.stderrDone:
	case <-time.After(500 * time.Millisecond):
	}

	t.cmd = nil
	t.stdin = nil
	t.stdout = nil
	t.stderr = nil
	return nil
}