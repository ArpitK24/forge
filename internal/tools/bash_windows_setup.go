//go:build windows

package tools

import (
	"os/exec"
	"syscall"
)

// applyProcessGroupSetup configures the *exec.Cmd for
// Windows process-group signaling. CREATE_NEW_PROCESS_GROUP
// (0x00000200) makes cmd.exe the root of a new console process
// group; when we signal cancellation below,
// GenerateConsoleCtrlEvent with CTRL_BREAK_EVENT can target
// the whole group — which is how child processes spawned by
// cmd.exe (ping, timeout, anything started with start /wait)
// finally get killed. Without this, os.Process.Kill only
// kills cmd.exe, leaving the grandchild orphaned.
//
// The Cancel func sends Ctrl+Break first via
// sendCtrlBreakToProcessGroup and then os/exec's WaitDelay
// escalates to Kill after bashTimeoutGrace.
func applyProcessGroupSetup(cmd *exec.Cmd) {
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
}
