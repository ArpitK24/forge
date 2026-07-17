package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/ArpitK24/forge/internal/core"
)

// BashInput is the JSON-decoded input shape for the Bash tool.
// The model fills in Command and (optionally) TimeoutSeconds.
type BashInput struct {
	// Command is the shell command line to run.
	Command string `json:"command"`
	// TimeoutSeconds is the per-call timeout in seconds. 0 means
	// "use the default" (core.DefaultBashTimeoutSeconds). Values
	// larger than core.MaxBashTimeoutSeconds are clamped down.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Description is a short human-readable description the
	// model fills in. Optional; surfaced in the TUI but not
	// in the headless text output.
	Description string `json:"description,omitempty"`
}

// bashInputSchema is the JSON Schema sent to the model for the
// Bash tool's input shape. Hand-written (small, stable) so we
// can iterate on the model's view of the tool without bumping
// the Go types.
var bashInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {
      "type": "string",
      "description": "The shell command line to run. On Windows this is passed to cmd /c; on Unix to bash -c."
    },
    "timeout_seconds": {
      "type": "integer",
      "description": "Optional per-call timeout in seconds. Defaults to 120, max 600.",
      "minimum": 0,
      "maximum": 600
    },
    "description": {
      "type": "string",
      "description": "A short human-readable description of what this command does."
    }
  },
  "required": ["command"],
  "additionalProperties": false
}`)

// bashTimeoutGrace is how long we wait between the first kill
// signal (SIGTERM on Unix, Ctrl+Break on Windows) and a
// hard-kill follow-up. 2 seconds is enough for well-behaved
// processes to flush and exit, short enough that the tool
// call doesn't hang visibly to the user.
const bashTimeoutGrace = 2 * time.Second

// BashTool is the implementation of the Bash tool. Spec §3.2.
type BashTool struct{}

// Name implements Tool.
func (b *BashTool) Name() string { return core.ToolBash }

// Description implements Tool. Kept concise; the model's view
// of what Bash can do is also informed by the system prompt's
// "Bash is for..." guidance.
func (b *BashTool) Description() string {
	return "Run a shell command. Returns combined stdout+stderr, the exit code, " +
		"and the wall-clock duration. Times out at the configured limit " +
		"(default 120s, max 600s). Output is truncated past ~100k characters."
}

// PermissionLevel implements Tool.
func (b *BashTool) PermissionLevel() core.PermissionLevel { return core.PermExecute }

// InputSchema implements Tool.
func (b *BashTool) InputSchema() json.RawMessage { return bashInputSchema }

// Execute runs the command. The behavior:
//
//   - Decode the JSON input into BashInput.
//   - Resolve TimeoutSeconds: 0 → default; clamp to MaxBashTimeoutSeconds.
//   - Spawn via os/exec with context.WithTimeout. Use `bash -c` on Unix,
//     `cmd /c` on Windows. On Windows, the cmd.exe is started with
//     CREATE_NEW_PROCESS_GROUP so we can deliver a Ctrl+Break event
//     to the whole group on timeout (this is the Phase 3 hardening
//     that makes kill actually propagate to grandchildren like
//     `ping` and `timeout`).
//   - Run in the ToolContext's WorkingDir (or "." if empty).
//   - Collect combined stdout+stderr into a buffer.
//   - On success: ToolResult{Text, Metadata: {"exit_code": 0, "duration_ms": ...}}.
//   - On non-zero exit: ToolResult{IsError: true, Text, Metadata: {"exit_code": N, "duration_ms": ...}}.
//   - On timeout: ToolResult{IsError: true, Text: "command timed out after Ns"}.
//   - On spawn error: ToolResult{IsError: true, Text: "..."}.
//   - On output overflow: truncate to core.MaxBashOutputBytes and append a
//     "[truncated: N bytes omitted]" notice.
func (b *BashTool) Execute(ctx context.Context, input json.RawMessage, tc *ToolContext) ToolResult {
	var in BashInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Bash: invalid input JSON: %v", err),
			IsError: true,
		}
	}
	if in.Command == "" {
		return ToolResult{
			Text:    "Bash: command is required",
			IsError: true,
		}
	}

	// Resolve timeout: 0 → default, clamp to max.
	timeoutSec := in.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = core.DefaultBashTimeoutSeconds
	}
	if timeoutSec > core.MaxBashTimeoutSeconds {
		timeoutSec = core.MaxBashTimeoutSeconds
	}
	timeout := time.Duration(timeoutSec) * time.Second

	// Build the per-call context with a timeout. We don't want
	// to cancel the parent (which might be a long-lived session
	// context); the timeout is local to this tool call.
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Pick the shell. Unix: bash -c "command". Windows: cmd /c "command".
	// Spec §3.2 also lists PowerShell as a separate tool; Phase 3
	// only ships Bash, and on Windows it uses cmd /c. PowerShell
	// will land as a separate tool in Phase 3.1.
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(runCtx, "cmd", "/c", in.Command)
		// CREATE_NEW_PROCESS_GROUP (0x00000200) makes cmd.exe
		// the root of a new console process group. When we
		// signal cancellation below, GenerateConsoleCtrlEvent
		// with CTRL_BREAK_EVENT can target the whole group
		// — which is how child processes spawned by cmd.exe
		// (ping, timeout, anything started with start /wait)
		// finally get killed. Without this, os.Process.Kill
		// only kills cmd.exe, leaving the grandchild
		// orphaned.
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
		}
		// Cancel is called by os/exec when the context is
		// cancelled. On Windows, it must be set to a non-nil
		// func to opt out of the default Kill (which would
		// only kill cmd.exe). We send Ctrl+Break first and
		// then escalate to Kill after bashTimeoutGrace.
		cmd.Cancel = func() error {
			return sendCtrlBreakToProcessGroup(cmd.Process)
		}
		cmd.WaitDelay = bashTimeoutGrace
	} else {
		cmd = exec.CommandContext(runCtx, "bash", "-c", in.Command)
		// Unix: put the child in its own process group and
		// install a Cancel that sends SIGTERM to the group.
		// os/exec's WaitDelay then escalates to SIGKILL if
		// the group hasn't exited.
		applyProcessGroupSetup(cmd)
	}
	if tc != nil && tc.WorkingDir != "" {
		cmd.Dir = tc.WorkingDir
	}
	// Capture combined stdout+stderr. We use a single buffer
	// because the spec's contract is "streams/collects stdout+stderr"
	// — keeping them in one stream matches that.
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	// Truncate if necessary. Truncation keeps the head and adds
	// a notice at the tail. The model can still see the exit
	// code and a reasonable slice of output.
	output := outBuf.String()
	truncated, omitted := truncateOutput(output, core.MaxBashOutputBytes)

	exitCode := 0
	var runErr string
	if err != nil {
		// On both Unix and Windows, our Cancel function
		// (SIGTERM on Unix, Ctrl+Break on Windows) lets
		// the child exit gracefully. The child then returns
		// a non-zero exit code (e.g. 143 on Unix from
		// SIGTERM=15+128, 1 on Windows from ping's handling
		// of Ctrl+Break). We need to distinguish "child
		// exited because we asked it to" from "child exited
		// with an error of its own". The way to do that is
		// to check runCtx.Err() first: if the context was
		// cancelled (deadline or parent cancel), the non-
		// zero exit is *our* doing, not the child's.
		switch {
		case runCtx.Err() == context.DeadlineExceeded:
			return ToolResult{
				Text: fmt.Sprintf("command timed out after %ds\n%s", timeoutSec, truncated),
				IsError: true,
				Metadata: map[string]any{
					"exit_code":   -1,
					"duration_ms": duration.Milliseconds(),
					"timed_out":   true,
				},
			}
		case runCtx.Err() == context.Canceled:
			// Parent context was cancelled (e.g. user
			// hit Ctrl+C in the TUI, or the loop is
			// shutting down). Surface as a cancelled
			// result, not as the child's exit code.
			return ToolResult{
				Text:    "command cancelled",
				IsError: true,
				Metadata: map[string]any{
					"exit_code":   -1,
					"duration_ms": duration.Milliseconds(),
					"cancelled":   true,
				},
			}
		}
		// Otherwise the child exited on its own with a
		// non-zero code (or a different error). exec.ExitError
		// carries the exit code; other errors (e.g. binary
		// not found) are surfaced as the spawn-error case.
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			runErr = err.Error()
		}
	}

	// Build the result text. On non-zero exit, prefix with a
	// short header so the model sees "this is an error" before
	// the output body.
	var text string
	if runErr != "" {
		text = fmt.Sprintf("spawn error: %s\n%s", runErr, truncated)
	} else if exitCode != 0 {
		text = fmt.Sprintf("exit code %d\n%s", exitCode, truncated)
	} else {
		text = truncated
	}
	if omitted > 0 {
		text += fmt.Sprintf("\n[truncated: %d bytes omitted]", omitted)
	}

	return ToolResult{
		Text:    text,
		IsError: runErr != "" || exitCode != 0,
		Metadata: map[string]any{
			"exit_code":   exitCode,
			"duration_ms": duration.Milliseconds(),
		},
	}
}

// truncateOutput returns the output trimmed to at most max bytes,
// plus the number of bytes that were omitted. The cut happens
// at a byte boundary but we trim back to the last newline within
// the kept range so the model's view doesn't end on a partial
// line.
func truncateOutput(s string, max int) (string, int) {
	if len(s) <= max {
		return s, 0
	}
	head := s[:max]
	// Try to trim back to a newline so the truncated output
	// doesn't end on a half-line.
	if nl := bytes.LastIndexByte([]byte(head), '\n'); nl > max/2 {
		head = head[:nl]
	}
	omitted := len(s) - len(head)
	return head, omitted
}
