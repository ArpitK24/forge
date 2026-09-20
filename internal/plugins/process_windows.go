//go:build windows

package plugins

import (
	"os"
	"os/exec"
	"syscall"
)

// applyProcessGroupSetup configures the *exec.Cmd for
// Windows process-group signaling. CREATE_NEW_PROCESS_GROUP
// (0x00000200) makes cmd.exe the root of a new console process
// group; when we signal cancellation below,
// GenerateConsoleCtrlEvent with CTRL_BREAK_EVENT can target
// the whole group — which is how child processes spawned by
// cmd.exe finally get killed. Without this, os.Process.Kill only
// kills cmd.exe, leaving the grandchild orphaned.
func applyProcessGroupSetup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
	}
	// Cancel is called by os/exec when the context is
	// cancelled. On Windows, it must be set to a non-nil
	// func to opt out of the default Kill (which would
	// only kill cmd.exe). We send Ctrl+Break first and
	// then escalate to Kill after shutdownGrace.
	cmd.Cancel = func() error {
		return sendCtrlBreakToProcessGroup(cmd.Process)
	}
	cmd.WaitDelay = shutdownGrace
}

// sendCtrlBreakToProcessGroup sends a CTRL_BREAK_EVENT to the
// process group of the given process. This is the Windows
// equivalent of killing a Unix process group with SIGTERM.
func sendCtrlBreakToProcessGroup(proc *os.Process) error {
	if proc == nil {
		return nil
	}
	// We need to call GenerateConsoleCtrlEvent from kernel32.dll.
	// This is done via syscall.Syscall. The event is CTRL_BREAK_EVENT (1).
	// The process group ID is the PID of the root process (cmd.exe).
	// See: https://learn.microsoft.com/en-us/windows/console/generateconsolectrlevent
	dll := syscall.NewLazyDLL("kernel32.dll")
	procGenCtrl := dll.NewProc("GenerateConsoleCtrlEvent")
	r, _, err := procGenCtrl.Call(
		1,          // CTRL_BREAK_EVENT
		uintptr(proc.Pid), // dwProcessGroupId
	)
	if r == 0 {
		return err
	}
	return nil
}