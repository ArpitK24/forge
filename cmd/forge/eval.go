package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/ArpitK24/forge/internal/ctxengine/eval"
)

// runEval dispatches the hidden `forge eval` subcommand (context-engine
// evaluation harness, see eval/README.md). It is intercepted before the
// normal flag parser and is deliberately absent from --help.
func runEval(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if code := eval.Main(ctx, args, os.Stdout, os.Stderr); code != 0 {
		return exitError(code)
	}
	return nil
}
