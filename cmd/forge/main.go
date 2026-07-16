// Command forge is the entry point for the Forge terminal coding agent.
//
// In Phase 1, the binary parses arguments, assembles a *core.Config,
// prints a startup banner, and exits. The actual agent loop lands in
// Phase 2 (single-provider headless) and Phase 3 (TUI REPL).
//
// The binary is built with CGO_ENABLED=0 to produce a fully static
// binary that does not require a Go runtime on the target machine.
// This is the distribution model required by spec §0 / §15.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ArpitK24/forge/internal/cli"
	"github.com/ArpitK24/forge/internal/core"
)

func main() {
	if err := run(os.Args[0], os.Args[1:]); err != nil {
		// For typed exit errors (e.g. usage errors), the error message
		// has already been printed inside run(). Just exit with the
		// right code, no additional "forge: exit 2" suffix.
		var ee exitError
		if errors.As(err, &ee) {
			os.Exit(int(ee))
		}
		fmt.Fprintln(os.Stderr, "forge:", err)
		os.Exit(1)
	}
}

// run is the testable entry point: same as main() but takes args
// explicitly and returns an error rather than calling os.Exit.
// This makes the binary's startup logic unit-testable from a future
// phase without spawning a subprocess.
func run(program string, args []string) error {
	parser := cli.NewParser(program, args)
	parsed, err := parser.Parse()
	if err != nil {
		// On a parse error, print usage to stderr and exit 2 (the
		// conventional "usage error" code).
		fmt.Fprint(os.Stderr, err.Error()+"\n\n")
		fmt.Fprint(os.Stderr, cli.Usage(program))
		return exitError(2)
	}

	// --help / --version short-circuit before any setup work.
	if parsed.ShowHelp {
		fmt.Print(cli.Usage(program))
		return nil
	}
	if parsed.ShowVersion {
		fmt.Printf("%s %s\n", core.AppName, core.AppVersion)
		fmt.Println(core.AppTagline)
		return nil
	}

	// Configure logging. Per spec §16: "structured logging, off by
	// default at a quiet level, with a verbose flag that raises the
	// level." We use slog's text handler on stderr so it never touches
	// stdout (which the ACP server mode in Phase 4 will use as a
	// JSON-RPC transport).
	logger := setupLogging(parsed.Verbose)
	logger.Debug("forge starting",
		"version", core.AppVersion,
		"verbose", parsed.Verbose,
		"print_mode", parsed.Print,
		"has_prompt", parsed.PositionalPrompt != "" || parsed.FromStdin,
	)

	// Phase 2: load settings and apply them under the CLI
	// overrides. CLI flags still win; settings.json is the
	// fallback. Failures here are non-fatal: we warn on stderr
	// and continue with the CLI-only config.
	cfg := parsed.ToConfig()
	settings, err := cli.LoadLayeredSettings(resolveCwd(parsed.Cwd))
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not load settings:", err)
		settings = core.Settings{}
	}
	cfg = cli.ApplySettings(cfg, settings)

	// --dump-system-prompt: prints the real assembled prompt.
	if parsed.DumpSystemPrompt {
		return dumpSystemPrompt(cfg)
	}

	// Read piped prompt if there is one.
	if parsed.PositionalPrompt == "" && parsed.FromStdin {
		prompt, err := readPromptFromStdin()
		if err != nil {
			return fmt.Errorf("read prompt from stdin: %w", err)
		}
		parsed.PositionalPrompt = prompt
	}

	// Headless vs. interactive: spec §7.5.
	//   - "give me a prompt and I'll print the answer" → runHeadless
	//   - "no prompt, no -p, no stdin" → start the TUI (Phase 3)
	if parsed.Print || parsed.PositionalPrompt != "" {
		return runHeadless(parsed, cfg, logger)
	}
	return runInteractivePlaceholder(parsed, cfg, logger)
}

// setupLogging constructs the slog.Logger. Default level is WARN;
// --verbose (or FORGE_DEBUG=1) raises it to DEBUG. Output goes to
// stderr so it never interferes with stdout (which ACP mode uses
// for JSON-RPC traffic).
func setupLogging(verbose bool) *slog.Logger {
	level := slog.LevelWarn
	if verbose || os.Getenv("FORGE_DEBUG") != "" {
		level = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})
	return slog.New(handler)
}

// dumpSystemPrompt is the --dump-system-prompt handler. It
// prints the resolved configuration and the fully-assembled
// system prompt the model will see on the next turn.
func dumpSystemPrompt(cfg *core.Config) error {
	cwd := resolveCwd(cfg.WorkingDir)
	fmt.Println("# Resolved configuration")
	fmt.Printf("model:           %s\n", cfg.EffectiveModel())
	fmt.Printf("max_tokens:      %d\n", cfg.EffectiveMaxTokens())
	fmt.Printf("permission_mode: %s\n", cfg.PermissionMode)
	fmt.Printf("output_format:   %s\n", cfg.OutputFormat)
	fmt.Printf("working_dir:     %s\n", cfg.WorkingDir)
	if cwd != "" && cfg.WorkingDir == "" {
		fmt.Printf("process_cwd:     %s\n", cwd)
	}
	fmt.Printf("auto_compact:    %v\n", cfg.AutoCompact)
	fmt.Println()
	fmt.Println("# Assembled system prompt")
	fmt.Println(core.BuildSystemPrompt(cfg, cwd))
	return nil
}

// readPromptFromStdin reads a single prompt from stdin. We use a
// Scanner with a generous buffer so multi-line pastes work; the
// "prompt" is everything up to EOF.
func readPromptFromStdin() (string, error) {
	if _, err := os.Stdin.Seek(0, io.SeekStart); err != nil {
		// stdin may be a pipe (not seekable); ignore that case.
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var b strings.Builder
	for scanner.Scan() {
		b.WriteString(scanner.Text())
		b.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// runHeadlessPlaceholder was the Phase-1 headless stub. The
// real implementation now lives in headless.go as runHeadless.

// runInteractivePlaceholder is the Phase-1 TUI stub. It prints a
// message telling the user to come back in Phase 3 and exits 0.
func runInteractivePlaceholder(a *cli.Args, cfg *core.Config, logger *slog.Logger) error {
	logger.Info("interactive mode (Phase 1 placeholder)")
	fmt.Printf("forge %s — interactive mode (Phase 1 placeholder)\n", core.AppVersion)
	fmt.Println()
	fmt.Println("The TUI lands in Phase 3. For now:")
	fmt.Println("  - run `forge -p \"hello\"` for a headless one-shot")
	fmt.Println("  - run `forge --help` for the full flag list")
	fmt.Println("  - run `forge --version` for the version")
	return nil
}

// exitError is a sentinel error type that run() returns to signal
// a specific exit code. main() inspects the error with errors.As
// to decide the code. Using a typed error (rather than os.Exit at
// the call site) keeps the call graph testable.
type exitError int

func (e exitError) Error() string { return "" }

// resolveCwd is a small helper used by later phases. Centralized so
// the precedence order is one place: --cwd flag, then $FORGE_CWD
// env, then process cwd. Phase 1 doesn't call it, but it's
// documented here for the Phase 2 wiring.
func resolveCwd(flagValue string) string {
	if flagValue != "" {
		abs, err := filepath.Abs(flagValue)
		if err == nil {
			return abs
		}
		return flagValue
	}
	if env := os.Getenv("FORGE_CWD"); env != "" {
		return env
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
