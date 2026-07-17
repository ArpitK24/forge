//go:build windows

package tools

import "os/exec"

// applyProcessGroupSetup is a no-op on Windows — the
// Windows-specific SysProcAttr setup (CREATE_NEW_PROCESS_GROUP
// + the Cancel function that calls GenerateConsoleCtrlEvent)
// is inlined in bash.go because it uses Windows-only
// constants and API calls that aren't available here.
//
// This stub exists so the call site in bash.go can be
// a single function call on every platform.
func applyProcessGroupSetup(cmd *exec.Cmd) {}
