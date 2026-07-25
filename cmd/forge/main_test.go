package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/cli"
	"github.com/ArpitK24/forge/internal/core"
)

// captureStdout captures everything written to os.Stdout for
// the duration of f, restoring the original writer before
// returning. Used by the --help and --version tests below.
func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	f()
	_ = w.Close()
	return <-done
}

// captureStderr mirrors captureStdout for stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	f()
	_ = w.Close()
	return <-done
}

// TestRun_HelpShortCircuits confirms that `forge --help`
// prints the usage banner and returns nil WITHOUT entering
// the TUI or headless paths. This is the guard for the
// "user asked for help, don't open a terminal" case.
func TestRun_HelpShortCircuits(t *testing.T) {
	out := captureStdout(t, func() {
		err := run("forge", []string{"--help"})
		if err != nil {
			t.Errorf("run(--help): err = %v, want nil", err)
		}
	})
	if !strings.Contains(out, "Usage:") && !strings.Contains(out, "forge") {
		t.Errorf("--help output should contain usage, got: %s", out)
	}
}

// TestRun_VersionShortCircuits confirms that `forge --version`
// prints the app name + version and returns nil.
func TestRun_VersionShortCircuits(t *testing.T) {
	out := captureStdout(t, func() {
		err := run("forge", []string{"--version"})
		if err != nil {
			t.Errorf("run(--version): err = %v, want nil", err)
		}
	})
	if !strings.Contains(out, core.AppName) {
		t.Errorf("--version output should contain %q, got: %s", core.AppName, out)
	}
	if !strings.Contains(out, core.AppVersion) {
		t.Errorf("--version output should contain %q, got: %s", core.AppVersion, out)
	}
}

// TestRun_InvalidFlag returns exitError(2) for unknown flags.
// The placeholder is removed (Step 7) — this test guards the
// "usage error → exit 2" path.
func TestRun_InvalidFlag(t *testing.T) {
	_ = captureStderr(t, func() {
		err := run("forge", []string{"--this-flag-does-not-exist"})
		if err == nil {
			t.Fatal("run with unknown flag: expected error, got nil")
		}
		var ee exitError
		if !errors.As(err, &ee) {
			t.Errorf("expected exitError, got %T: %v", err, err)
		} else if int(ee) != 2 {
			t.Errorf("exit code = %d, want 2", int(ee))
		}
	})
}

// TestRun_Parse_PrintFlag — confirms the parser still wires
// the -p flag through. This is the gate that decides between
// headless and TUI in run(); if the flag stops being
// recognized, the TUI would launch on a `forge -p "hi"`
// invocation.
func TestRun_Parse_PrintFlag(t *testing.T) {
	parser := cli.NewParser("forge", []string{"-p", "hi"})
	parsed, err := parser.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !parsed.Print {
		t.Error("parsed.Print = false, want true for -p")
	}
	if parsed.PositionalPrompt != "hi" {
		t.Errorf("PositionalPrompt = %q, want %q", parsed.PositionalPrompt, "hi")
	}
}

// TestRun_Parse_NoFlags — running forge with no args should
// produce an Args struct where neither Print nor
// PositionalPrompt is set. This is the case that lands in
// the TUI (Step 7 dispatch).
func TestRun_Parse_NoFlags(t *testing.T) {
	parser := cli.NewParser("forge", []string{})
	parsed, err := parser.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed.Print {
		t.Error("parsed.Print = true with no flags, want false")
	}
	if parsed.PositionalPrompt != "" {
		t.Errorf("PositionalPrompt = %q with no flags, want empty",
			parsed.PositionalPrompt)
	}
}
