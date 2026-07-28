//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// applyProcessGroupSetup mirrors internal/tools/bash_unix.go:21-42.
// On Unix we put the subprocess in its own process group (so SIGTERM
// can be sent to the group, not just the immediate child) and
// install a Cancel that does the SIGTERM-on-cancel handshake that
// os/exec expects. WaitDelay escalates to SIGKILL after shutdownGrace.
func applyProcessGroupSetup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID = process group. SIGTERM lets every
		// descendant clean up; os/exec's WaitDelay escalates
		// to SIGKILL if the process hasn't exited by then.
		pgid, err := syscall.Getpgid(cmd.Process.Pid)
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		} else {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}
	cmd.WaitDelay = shutdownGrace
}
