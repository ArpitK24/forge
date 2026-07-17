//go:build windows

package tools

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestBashTimeoutWindows verifies the Phase 3 hardening:
// `cmd /c <long-running-cmd>` is now spawned in its own
// process group (CREATE_NEW_PROCESS_GROUP), and the Cancel
// function sends a CTRL_BREAK_EVENT to that group via
// GenerateConsoleCtrlEvent. os/exec's WaitDelay then
// escalates to Kill if the group hasn't exited.
//
// The test is gated to runtime.GOOS == "windows" because
// the underlying call only makes sense on that platform.
// On Unix, TestBashTimeout covers the same path using
// `sleep` and SIGTERM.
func TestBashTimeoutWindows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}

	// We use `ping -n 999 127.0.0.1` as the long-running
	// command. `ping` is built into every Windows install,
	// takes a `-n` count, and does not exit on stdin close.
	// That last property is what makes this a faithful
	// test of the process-group kill: a normal short
	// command would exit on its own when stdin is closed,
	// masking whether our Cancel function actually
	// delivered Ctrl+Break to the group.
	//
	// We give the command a 1-second timeout and verify
	// that Execute returns within a few seconds with
	// IsError=true and Metadata.timed_out=true.
	const longCmd = `ping -n 999 127.0.0.1`
	b := &BashTool{}
	tc := bashTC()
	in := decodeBashInput(t, fmt.Sprintf(`{"command":%q,"timeout_seconds":1}`, longCmd))

	start := time.Now()
	out := b.Execute(context.Background(), in, tc)
	elapsed := time.Since(start)

	// Should be back in well under 10s. The exact bound
	// is loose because Windows process cleanup isn't
	// instant, but bashTimeoutGrace is 2s and the
	// kernel-level cleanup is quick.
	if elapsed > 10*time.Second {
		t.Errorf("Bash timeout on Windows took %v, want <10s", elapsed)
	}
	if !out.IsError {
		t.Errorf("Bash timeout on Windows should be IsError=true; got %+v", out)
	}
	if timedOut, _ := out.Metadata["timed_out"].(bool); !timedOut {
		t.Errorf("Metadata.timed_out should be true; got %+v", out.Metadata)
	}
	if !strings.Contains(out.Text, "timed out") {
		t.Errorf("timeout text missing 'timed out': %q", out.Text)
	}
}

// TestBashCancelWindows exercises the parent-context
// cancel path (not the timeout path): we cancel the
// context while the child is running and verify the
// process group is killed promptly.
func TestBashCancelWindows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}

	const longCmd = `ping -n 999 127.0.0.1`
	b := &BashTool{}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	out := b.Execute(ctx, decodeBashInput(t, fmt.Sprintf(`{"command":%q}`, longCmd)), bashTC())
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Errorf("cancelled Bash on Windows took %v, want <10s", elapsed)
	}
	if !out.IsError {
		t.Errorf("cancelled Bash on Windows should be IsError=true; got %+v", out)
	}
}
