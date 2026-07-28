//go:build windows

package mcp

import (
	"os/exec"
	"syscall"
)

// applyProcessGroupSetup mirrors internal/tools/bash_windows_setup.go's
// pattern. CREATE_NEW_PROCESS_GROUP (0x00000200) makes the subprocess
// the root of a new console process group; Cancel sends a Ctrl+Break
// to the group so child processes spawned by the MCP server are also
// signaled. WaitDelay escalates to a hard Kill after shutdownGrace.
//
// The Ctrl+Break delivery uses kernel32!GenerateConsoleCtrlEvent
// directly rather than going through internal/tools so this package
// stays import-graph-clean (mcp doesn't depend on tools). The body is
// lifted from internal/tools/bash_windows.go:40-60 with no behavior
// change.
func applyProcessGroupSetup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		const CTRL_BREAK_EVENT = 1
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		proc := kernel32.NewProc("GenerateConsoleCtrlEvent")
		// cmd.Process.Pid is the process group ID because we
		// used CREATE_NEW_PROCESS_GROUP. dwProcessGroupId=0
		// would target the Go parent — wrong.
		r1, _, _ := proc.Call(uintptr(CTRL_BREAK_EVENT), uintptr(cmd.Process.Pid))
		if r1 == 0 {
			// Fall back to hard kill so the caller still has
			// a way to stop the runaway subprocess.
			_ = cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = shutdownGrace
}
