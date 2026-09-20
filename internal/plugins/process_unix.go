//go:build !windows

package plugins

import (
	"os/exec"
	"syscall"
)

// applyProcessGroupSetup configures the *exec.Cmd for
// cross-platform process-group signaling. On Unix we
// put the child in its own process group (so SIGTERM
// can be sent to the group, not just the plugin) and install
// a Cancel function that does the SIGTERM-on-cancel
// handshake that os/exec expects.
func applyProcessGroupSetup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID = process group. Sending SIGTERM
		// to the group gives every descendant a chance
		// to clean up; os/exec's WaitDelay then escalates
		// to SIGKILL if the process hasn't exited.
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