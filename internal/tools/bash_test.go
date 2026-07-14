package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/core"
)

// bashTC returns a minimal ToolContext suitable for unit tests.
// Tests that don't care about permission handling use this.
func bashTC() *ToolContext {
	return &ToolContext{
		WorkingDir: ".",
		Permission: &core.AutoPermissionHandler{Mode: core.PermissionBypassPermissions},
	}
}

func decodeBashInput(t *testing.T, s string) json.RawMessage {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("test setup: invalid input JSON %q", s)
	}
	return json.RawMessage(s)
}

func TestBashEchoSuccess(t *testing.T) {
	b := &BashTool{}
	tc := bashTC()
	out := b.Execute(context.Background(), decodeBashInput(t, `{"command":"echo hello-from-bash"}`), tc)
	if out.IsError {
		t.Errorf("Bash succeeded unexpectedly: Text=%q Metadata=%+v", out.Text, out.Metadata)
	}
	if !strings.Contains(out.Text, "hello-from-bash") {
		t.Errorf("output missing 'hello-from-bash': %q", out.Text)
	}
	if code, _ := out.Metadata["exit_code"].(int); code != 0 {
		t.Errorf("exit_code = %d, want 0", code)
	}
}

func TestBashNonZeroExitIsError(t *testing.T) {
	b := &BashTool{}
	tc := bashTC()
	out := b.Execute(context.Background(), decodeBashInput(t, `{"command":"exit 7"}`), tc)
	if !out.IsError {
		t.Errorf("Bash with non-zero exit should be IsError=true; got %+v", out)
	}
	if code, _ := out.Metadata["exit_code"].(int); code != 7 {
		t.Errorf("exit_code = %d, want 7", code)
	}
	if !strings.Contains(out.Text, "exit code 7") {
		t.Errorf("error text missing 'exit code 7': %q", out.Text)
	}
}

func TestBashTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in -short mode")
	}
	if runtime.GOOS == "windows" {
		// On Windows, `cmd /c ping` doesn't always exit promptly
		// when the parent context is cancelled, because cmd
		// doesn't propagate the kill to the child ping process.
		// The proper fix is to set CREATE_NEW_PROCESS_GROUP and
		// kill the group, which is a Phase-3 hardening item.
		// For now, this test only runs on POSIX.
		t.Skip("Bash timeout test: skipping on Windows (see comment)")
	}
	b := &BashTool{}
	tc := bashTC()
	cmd := "sleep 999"
	in := decodeBashInput(t, fmt.Sprintf(`{"command":%q,"timeout_seconds":1}`, cmd))

	start := time.Now()
	out := b.Execute(context.Background(), in, tc)
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Errorf("Bash timeout took %v, want <10s", elapsed)
	}
	if !out.IsError {
		t.Errorf("Bash timeout should be IsError=true; got %+v", out)
	}
	if timedOut, _ := out.Metadata["timed_out"].(bool); !timedOut {
		t.Errorf("Metadata.timed_out should be true; got %+v", out.Metadata)
	}
	if !strings.Contains(out.Text, "timed out") {
		t.Errorf("timeout text missing 'timed out': %q", out.Text)
	}
}

func TestBashTruncation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping truncation test in -short mode")
	}
	b := &BashTool{}
	tc := bashTC()
	// Generate ~150k bytes of output. We avoid Python/Perl
	// (might not be installed) and use shell built-ins.
	// Strategy: write a big file, then `cat` it.
	big := strings.Repeat("a", 150_000)
	tmpFile, err := os.CreateTemp(t.TempDir(), "forge-bash-test-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := tmpFile.WriteString(big); err != nil {
		t.Fatalf("write big: %v", err)
	}
	tmpFile.Close()

	// `cat` on Unix reads from the file; on Windows we'd need
	// `type` or PowerShell. We branch for portability.
	var cmd string
	if runtime.GOOS == "windows" {
		// `cmd /c type <file>` works on every Windows.
		cmd = fmt.Sprintf("type %s", tmpFile.Name())
	} else {
		cmd = fmt.Sprintf("cat %s", tmpFile.Name())
	}
	out := b.Execute(context.Background(), decodeBashInput(t, fmt.Sprintf(`{"command":%q}`, cmd)), tc)
	if !strings.Contains(out.Text, "[truncated:") {
		t.Errorf("expected truncation notice; got %d-byte output, head=%q",
			len(out.Text), headBytes(out.Text, 80))
	}
}

func TestBashMissingCommandIsError(t *testing.T) {
	b := &BashTool{}
	out := b.Execute(context.Background(), decodeBashInput(t, `{"command":""}`), bashTC())
	if !out.IsError {
		t.Errorf("empty command should be IsError=true; got %+v", out)
	}
}

func TestBashInvalidInputJSONIsError(t *testing.T) {
	b := &BashTool{}
	out := b.Execute(context.Background(), json.RawMessage(`{not json`), bashTC())
	if !out.IsError {
		t.Errorf("invalid JSON input should be IsError=true; got %+v", out)
	}
}

func TestBashRespectsWorkingDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-specific test (uses cat)")
	}
	b := &BashTool{}
	dir := t.TempDir()
	sentinel := dir + string(os.PathSeparator) + "sentinel.txt"
	if err := os.WriteFile(sentinel, []byte("found-it"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tc := bashTC()
	tc.WorkingDir = dir
	out := b.Execute(context.Background(), decodeBashInput(t, `{"command":"cat sentinel.txt"}`), tc)
	if out.IsError {
		t.Errorf("cat should succeed in tc.WorkingDir; got %+v", out)
	}
	if !strings.Contains(out.Text, "found-it") {
		t.Errorf("output missing 'found-it': %q", out.Text)
	}
}

func TestBashOutputCombinesStdoutAndStderr(t *testing.T) {
	// Spec §3.2: "streams/collects stdout+stderr". The simplest
	// way to verify is to write to stderr and check it appears
	// in the combined output.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX-specific test (uses 2>&1)")
	}
	b := &BashTool{}
	out := b.Execute(context.Background(),
		decodeBashInput(t, `{"command":"echo to-stdout; echo to-stderr 1>&2"}`),
		bashTC())
	if !strings.Contains(out.Text, "to-stdout") {
		t.Errorf("missing stdout: %q", out.Text)
	}
	if !strings.Contains(out.Text, "to-stderr") {
		t.Errorf("missing stderr (Bash must combine): %q", out.Text)
	}
}

func TestBashRespectsParentContextCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	if runtime.GOOS == "windows" {
		// See TestBashTimeout for the same skip reason.
		t.Skip("Bash cancel test: skipping on Windows")
	}
	b := &BashTool{}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after 100ms; the command sleeps for 5s. The
	// command should be killed promptly.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	out := b.Execute(ctx, decodeBashInput(t, `{"command":"sleep 5"}`), bashTC())
	elapsed := time.Since(start)
	// Allow some leeway: a cancelled process takes a moment
	// to be reaped. But it should be nowhere near 5 seconds.
	if elapsed > 2*time.Second {
		t.Errorf("cancelled Bash took %v, want <2s", elapsed)
	}
	if !out.IsError {
		t.Errorf("cancelled Bash should be IsError=true; got %+v", out)
	}
}

func TestBashMaxTimeoutIsClamped(t *testing.T) {
	// Request a timeout larger than the max; the tool should
	// clamp it (and then return immediately on a trivial
	// command). We can't directly assert the clamp happened
	// without timing, but the call should succeed without
	// hanging the test.
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	b := &BashTool{}
	out := b.Execute(context.Background(),
		decodeBashInput(t, `{"command":"echo ok","timeout_seconds":9999999}`),
		bashTC())
	if out.IsError {
		t.Errorf("Bash with clamped timeout: %+v", out)
	}
}

func headBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
