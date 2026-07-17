//go:build !windows

package tools

import "os"

// sendCtrlBreakToProcessGroup is a no-op on non-Windows
// platforms. The Unix path in bash.go uses cmd.Cancel
// (SIGTERM to the process group) directly and never
// calls this function; it exists only so the symbol
// is available to the Windows-only build of bash.go.
func sendCtrlBreakToProcessGroup(p *os.Process) error {
	return nil
}
