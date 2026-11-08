package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/cli"
)

// TestRun_EvalIsHiddenSubcommand checks that `forge eval` reaches the
// eval harness (usage error without a subcommand) and that --help does
// not advertise it.
func TestRun_EvalIsHiddenSubcommand(t *testing.T) {
	var err error
	stderr := captureStderr(t, func() { err = run("forge", []string{"eval"}) })
	var ee exitError
	if !errors.As(err, &ee) || ee != 2 {
		t.Fatalf("run(eval) err = %v, want exitError(2)", err)
	}
	if !strings.Contains(stderr, "forge eval retrieval") {
		t.Fatalf("eval usage not printed: %q", stderr)
	}
	if strings.Contains(cli.Usage("forge"), "eval") {
		t.Fatal("--help must not list the hidden eval command")
	}
}
