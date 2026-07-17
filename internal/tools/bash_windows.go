//go:build windows

package tools

import (
	"fmt"
	"os"
	"syscall"
)

// sendCtrlBreakToProcessGroup delivers a CTRL_BREAK_EVENT to
// the process group rooted at p. This is the Windows
// analogue of sending SIGTERM to a Unix process group: it
// gives the cmd.exe and its children a chance to shut down
// gracefully before we escalate to a hard kill.
//
// We use the documented Windows API:
//
//	BOOL GenerateConsoleCtrlEvent(
//	    DWORD dwCtrlEvent,    // CTRL_BREAK_EVENT = 1
//	    DWORD dwProcessGroup // process group ID (== PID
//	                         //   when CREATE_NEW_PROCESS_GROUP
//	                         //   was used on the child)
//	)
//
// Because we set CREATE_NEW_PROCESS_GROUP on cmd.exe, the
// child PID is also the process group ID. Passing p.Pid as
// the group ID targets only that group — the Go parent
// (which lives in its own group / has no group on Windows)
// does NOT receive the event, so the parent's test runner
// or main loop is unaffected.
//
// If the process has no console (the common case when
// running as a subprocess of something that already
// redirected stdio — e.g. our own forge.exe running with
// piped stdio) GenerateConsoleCtrlEvent returns FALSE and
// GetLastError reports "The handle is invalid." In that
// case we fall back to os.Process.Kill so the caller
// still has a way to stop the runaway.
func sendCtrlBreakToProcessGroup(p *os.Process) error {
	if p == nil {
		return nil
	}
	const CTRL_BREAK_EVENT = 1
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GenerateConsoleCtrlEvent")
	// p.Pid is the process group ID because we used
	// CREATE_NEW_PROCESS_GROUP. dwProcessGroupId=0 would
	// mean "the group attached to the calling process's
	// console" — i.e. the Go parent itself — which is
	// wrong. Always pass the child's PID.
	r1, _, _ := proc.Call(uintptr(CTRL_BREAK_EVENT), uintptr(p.Pid))
	if r1 == 0 {
		// Fall back to hard kill.
		if err := p.Kill(); err != nil {
			return fmt.Errorf("ctrl-break failed and Kill failed: %w", err)
		}
		return nil
	}
	return nil
}
