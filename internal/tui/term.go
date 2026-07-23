package tui

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// setupRawMode puts the terminal into raw mode and enters the
// alternate screen buffer. The returned restore function MUST
// be called on every exit path (including panic) to return the
// terminal to its original state.
//
// The function is idempotent — calling restore more than once is
// safe (the second call is a no-op). This is important for the
// panic-recovery path: defer restore; defer recover(…restore).
func setupRawMode() (restore func(), err error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return func() {}, fmt.Errorf("stdin is not a terminal")
	}

	// Save the original state so we can restore it.
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}, fmt.Errorf("failed to enter raw mode: %w", err)
	}

	restored := false
	restore = func() {
		if restored {
			return
		}
		restored = true
		_ = term.Restore(fd, oldState)
		// Exit the alternate screen buffer.
		fmt.Fprintf(os.Stdout, "\x1b[?1049l")
		fmt.Fprint(os.Stdout, "\x1b[?25h") // show cursor
		fmt.Fprint(os.Stdout, "\x1b[0m")   // reset attributes
	}

	// Enter the alternate screen buffer + hide the cursor.
	fmt.Fprint(os.Stdout, "\x1b[?1049h")
	fmt.Fprint(os.Stdout, "\x1b[?25l")

	// Register a SIGINT/SIGTERM handler that restores the
	// terminal before exiting. Without this, Ctrl+C while the
	// program is blocked leaves the terminal in raw mode — an
	// unusable state for the user.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		restore()
		os.Exit(130) // 128 + SIGINT (standard convention)
	}()

	return restore, nil
}

// withPanicRestore wraps a function call so that if it panics, the
// terminal is restored before the panic propagates. Usage:
//
//	tui.withPanicRestore(restore, func() {
//	    p := tea.NewProgram(...)
//	    _, err := p.Run()
//	    ...
//	})
func withPanicRestore(restore func(), f func()) {
	defer func() {
		if r := recover(); r != nil {
			restore()
			// Re-panic so the caller sees the original stack.
			panic(r)
		}
	}()
	f()
}
