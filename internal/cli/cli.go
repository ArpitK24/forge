// Package cli implements the Forge command-line interface: argument
// parsing, settings loading, and the wiring that turns parsed flags
// into a *core.Config the rest of the system can consume.
//
// This is a Phase-1 implementation: the parser and the flag-to-config
// mapping are complete, but settings loading and the per-provider
// defaults are stubs that will be filled in during Phase 2.
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
)

// Args is the parsed command-line input, post-parse and validated.
// All fields are exported so cmd/forge/main.go can wire them into a
// *core.Config without ceremony.
type Args struct {
	// PositionalPrompt is every non-flag argument joined with single
	// spaces (flags may appear before or after it). It is
	// the "headless one-shot prompt" mode from spec §7.3: when present
	// (or piped on stdin) and the binary is not in interactive TUI
	// mode, this becomes the first user message.
	PositionalPrompt string

	// FromStdin is true when no positional prompt was given AND stdin
	// is not a terminal (i.e. the prompt is being piped in). The main
	// entry point reads stdin in that case.
	FromStdin bool

	// Print forces non-interactive/headless mode. Equivalent to passing
	// -p on the CLI, but settable programmatically for tests.
	Print bool

	// Model is the --model override.
	Model string

	// PermissionMode is the --permission-mode override, parsed.
	PermissionMode core.PermissionMode

	// PermissionModeSet is true when the user explicitly passed
	// --permission-mode. main.go uses this to know whether to
	// overwrite the settings-file default.
	PermissionModeSet bool

	// Resume is the --resume session id.
	Resume string

	// MaxTurns is the --max-turns override, or 0 for "use default."
	MaxTurns int

	// SystemPrompt is the --system-prompt override (replaces default).
	SystemPrompt string

	// AppendSystemPrompt is the --append-system-prompt override.
	AppendSystemPrompt string

	// NoProjectMemory disables FORGE.md discovery.
	NoProjectMemory bool

	// OutputFormat is the --output-format override, parsed.
	OutputFormat core.OutputFormat

	// OutputFormatSet is true when the user explicitly passed
	// --output-format.
	OutputFormatSet bool

	// Verbose enables debug logging.
	Verbose bool

	// APIKey is the --api-key override.
	APIKey string

	// APIBase is the --api-base override. Empty = use the
	// provider's default base (NIM for the openai adapter).
	APIBase string

	// MaxTokens is the --max-tokens override, or 0 for "use default."
	MaxTokens int

	// Cwd is the --cwd override. Empty means "use the process's
	// actual working directory."
	Cwd string

	// DangerouslySkipPermissions is the --dangerously-skip-permissions
	// shortcut for PermissionBypassPermissions.
	DangerouslySkipPermissions bool

	// DumpSystemPrompt prints the assembled system prompt and exits
	// without making any API call. Used to debug prompt assembly.
	DumpSystemPrompt bool

	// MCPConfig is the path to an MCP config JSON file.
	MCPConfig string

	// PluginDirs is the list of --plugin-dir directories.
	PluginDirs []string

	// NoAutoCompact disables §5.2's auto-compaction.
	NoAutoCompact bool

	// ShowVersion prints the version and exits.
	ShowVersion bool

	// ShowHelp prints the usage banner and exits.
	ShowHelp bool
}

// Parser holds the flag set and the raw positional args. Construct
// with NewParser, then call Parse.
type Parser struct {
	fs   *flag.FlagSet
	args []string
}

// NewParser constructs a Parser bound to the given program name (for
// help output). The args slice is the post-flag-declaration arguments,
// usually os.Args[1:].
func NewParser(program string, args []string) *Parser {
	fs := flag.NewFlagSet(program, flag.ContinueOnError)
	// Send flag-set's own errors to a buffer so we control the output
	// format. We still want to surface parse errors though.
	fs.SetOutput(io.Discard)
	return &Parser{fs: fs, args: args}
}

// Parse runs the flag parser and returns a fully-populated Args or
// an error. The error is suitable for printing to stderr; on error
// the returned Args is partially populated but should not be used.
func (p *Parser) Parse() (*Args, error) {
	a := &Args{}

	// Bool flags. Use the flag package's idiomatic naming: long form
	// with single-dash aliases where the spec calls them out.
	p.fs.BoolVar(&a.Print, "p", false, "force non-interactive/headless mode")
	p.fs.BoolVar(&a.Print, "print", false, "force non-interactive/headless mode")
	p.fs.BoolVar(&a.Verbose, "v", false, "enable debug-level logging")
	p.fs.BoolVar(&a.Verbose, "verbose", false, "enable debug-level logging")
	p.fs.BoolVar(&a.NoProjectMemory, "no-project-memory", false, "skip FORGE.md discovery")
	p.fs.BoolVar(&a.DangerouslySkipPermissions, "dangerously-skip-permissions", false,
		"shortcut for --permission-mode=bypass-permissions")
	p.fs.BoolVar(&a.DumpSystemPrompt, "dump-system-prompt", false,
		"print the assembled system prompt and exit, no API call")
	p.fs.BoolVar(&a.NoAutoCompact, "no-auto-compact", false,
		"disable automatic context compaction")
	p.fs.BoolVar(&a.ShowVersion, "version", false, "print version and exit")
	p.fs.BoolVar(&a.ShowHelp, "h", false, "print help and exit")
	p.fs.BoolVar(&a.ShowHelp, "help", false, "print help and exit")

	// String flags.
	p.fs.StringVar(&a.Model, "m", "", "override the model id")
	p.fs.StringVar(&a.Model, "model", "", "override the model id")
	p.fs.StringVar(&a.Resume, "resume", "", "resume a session by id")
	p.fs.StringVar(&a.SystemPrompt, "s", "", "full system-prompt override (replaces default)")
	p.fs.StringVar(&a.SystemPrompt, "system-prompt", "", "full system-prompt override (replaces default)")
	p.fs.StringVar(&a.AppendSystemPrompt, "append-system-prompt", "",
		"appended to the default system prompt")
	p.fs.StringVar(&a.APIKey, "api-key", "", "explicit API key override")
	p.fs.StringVar(&a.APIBase, "api-base", "", "API base URL override (default: NIM)")
	p.fs.StringVar(&a.Cwd, "cwd", "", "working directory override")
	p.fs.StringVar(&a.MCPConfig, "mcp-config", "",
		"path to a JSON file of MCP server configs to connect at startup")
	p.fs.Func("plugin-dir", "directory to scan for plugins (repeatable)",
		func(s string) error {
			a.PluginDirs = append(a.PluginDirs, s)
			return nil
		})

	// Int flags. We use a string intermediate so we can detect
	// "user passed a value" vs "user didn't pass anything" and only
	// overwrite the default in the former case.
	var maxTurnsStr, maxTokensStr, permModeStr, outputFormatStr string
	p.fs.StringVar(&maxTurnsStr, "max-turns", "", "cap on agentic turns (default 10)")
	p.fs.StringVar(&maxTokensStr, "max-tokens", "", "override the per-response output token cap")
	p.fs.StringVar(&permModeStr, "permission-mode", "",
		"one of: default, accept-edits, bypass-permissions, plan")
	p.fs.StringVar(&outputFormatStr, "output-format", "",
		"one of: text, json, stream-json")

	// The stdlib flag package stops at the first positional argument,
	// so `forge -p "fix tests" --model x` would leave --model unparsed.
	// Move every flag (and its value) ahead of the positionals first,
	// keeping their relative order, then parse once.
	flags, positionals := splitArgs(p.fs, p.args)
	if err := p.fs.Parse(flags); err != nil {
		return nil, err
	}
	// splitArgs only routes recognised flag shapes to flags, so
	// anything Parse leaves behind is a positional too.
	positionals = slices.Concat(p.fs.Args(), positionals)

	// Positional: every remaining word, joined with single spaces, is
	// the prompt, so `forge fix the tests` and `forge "fix the tests"`
	// behave the same.
	a.PositionalPrompt = strings.Join(positionals, " ")

	// Stdin-piped prompt: if there's no positional prompt and stdin
	// is not a terminal, we'll read the prompt from stdin in main.go.
	if a.PositionalPrompt == "" {
		if fi, err := os.Stdin.Stat(); err == nil {
			a.FromStdin = (fi.Mode() & os.ModeCharDevice) == 0
		}
	}

	// Parse the optional int flags.
	if maxTurnsStr != "" {
		n, err := strconv.Atoi(maxTurnsStr)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid --max-turns %q: must be a positive integer", maxTurnsStr)
		}
		a.MaxTurns = n
	}
	if maxTokensStr != "" {
		n, err := strconv.Atoi(maxTokensStr)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid --max-tokens %q: must be a positive integer", maxTokensStr)
		}
		a.MaxTokens = n
	}

	// Parse --permission-mode.
	if permModeStr != "" {
		mode, ok := core.ParsePermissionMode(permModeStr)
		if !ok {
			return nil, fmt.Errorf("invalid --permission-mode %q: must be one of "+
				"default, accept-edits, bypass-permissions, plan", permModeStr)
		}
		a.PermissionMode = mode
		a.PermissionModeSet = true
	}
	if a.DangerouslySkipPermissions {
		a.PermissionMode = core.PermissionBypassPermissions
		a.PermissionModeSet = true
	}

	// Parse --output-format.
	if outputFormatStr != "" {
		of, ok := core.ParseOutputFormat(outputFormatStr)
		if !ok {
			return nil, fmt.Errorf("invalid --output-format %q: must be one of "+
				"text, json, stream-json", outputFormatStr)
		}
		a.OutputFormat = of
		a.OutputFormatSet = true
	}

	return a, nil
}

// splitArgs separates args into flag tokens (each flag with its value,
// in their original order) and positional words, so flags may appear
// anywhere on the command line, before or after the prompt.
//
// Rules, matching the flag package's own syntax:
//   - "--" ends flag parsing; every later argument is positional.
//   - "-" on its own is positional.
//   - "-name=value" / "--name=value" is one flag token.
//   - "-name" / "--name" for a boolean flag is one token; for any other
//     flag it takes the next argument as its value (even if that value
//     looks like a flag or is "--").
//   - Unknown flags are kept as flags so Parse reports them, rather
//     than having them silently become part of the prompt.
func splitArgs(fs *flag.FlagSet, args []string) (flags, positionals []string) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positionals = append(positionals, arg)
			continue
		}
		flags = append(flags, arg)
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil || isBoolFlag(f) {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, positionals
}

// isBoolFlag reports whether f is a boolean flag, i.e. one the flag
// package accepts without a value.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// loadMCPConfig reads the JSON file at a.MCPConfig and returns the
// list of MCP server configs it describes. A missing a.MCPConfig is
// a no-op (returns nil, nil); a malformed file or unreadable file
// surfaces as an error so main.go can exit with status 2.
//
// The file shape is the same as core.Settings.McpServers:
// a top-level object with an "mcp_servers" array. We accept either
// that shape or a bare array — the bare-array form is convenient for
// ad-hoc configs that don't want to wrap a single list in an object.
func (a *Args) loadMCPConfig() ([]core.McpServerConfig, error) {
	if a.MCPConfig == "" {
		return nil, nil
	}
	b, err := os.ReadFile(a.MCPConfig)
	if err != nil {
		return nil, fmt.Errorf("read --mcp-config %q: %w", a.MCPConfig, err)
	}
	// Try the object form first (matches core.Settings). We use a
	// struct with a *[]McpServerConfig pointer so we can distinguish
	// "key absent" from "key present with empty array" — the former
	// means we should fall back to bare-array parsing, the latter
	// means the user explicitly wrote `"mcp_servers": []`.
	var obj struct {
		McpServers *[]core.McpServerConfig `json:"mcp_servers"`
	}
	if err := json.Unmarshal(b, &obj); err == nil && obj.McpServers != nil {
		return *obj.McpServers, nil
	}
	// Fall back to a bare array.
	var arr []core.McpServerConfig
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, fmt.Errorf("parse --mcp-config %q: %w", a.MCPConfig, err)
	}
	return arr, nil
}

// ToConfig converts parsed Args into a *core.Config. It does NOT load
// settings.json (that's a later phase) — it just maps the parsed flags
// into the Config fields. Callers should layer settings on top of
// this result before handing it to the query loop.
//
// Returns an error if --mcp-config was passed and the file failed to
// read or parse. A missing path is not an error.
func (a *Args) ToConfig() (*core.Config, error) {
	mcpServers, err := a.loadMCPConfig()
	if err != nil {
		return nil, err
	}
	c := &core.Config{
		Provider:              "", // empty = "use default" (Anthropic in Phase 2)
		APIKey:                a.APIKey,
		APIBase:               a.APIBase,
		Model:                 a.Model,
		MaxTokens:             a.MaxTokens,
		MaxTurns:              a.MaxTurns,
		SystemPrompt:          a.SystemPrompt,
		AppendSystemPrompt:    a.AppendSystemPrompt,
		SkipProjectMemoryFile: a.NoProjectMemory,
		Verbose:               a.Verbose,
		WorkingDir:            a.Cwd,
		AutoCompact:           !a.NoAutoCompact,
		McpServers:            mcpServers,
		ResumeID:              a.Resume,
		PluginDirs:            a.PluginDirs,
	}
	if a.PermissionModeSet {
		c.PermissionMode = a.PermissionMode
	} else {
		c.PermissionMode = core.PermissionDefault
	}
	if a.OutputFormatSet {
		c.OutputFormat = a.OutputFormat
	} else {
		c.OutputFormat = core.OutputText
	}
	return c, nil
}

// Usage returns the help text shown for --help. Exposed as a function
// (not a method) so it can be printed from main.go on parse errors.
func Usage(program string) string {
	var b strings.Builder
	b.WriteString(program + " — " + core.AppTagline + "\n\n")
	b.WriteString("Usage:\n")
	b.WriteString("  " + program + " [flags] [prompt]\n")
	b.WriteString("  " + program + " [flags] -p [prompt]      # headless one-shot\n")
	b.WriteString("  " + program + " [flags] acp            # ACP server mode (Phase 4)\n\n")
	b.WriteString("Flags:\n")
	b.WriteString("  -p, --print                      force non-interactive mode\n")
	b.WriteString("  -m, --model <id>                 override the model id\n")
	b.WriteString("      --permission-mode <mode>     default | accept-edits | bypass-permissions | plan\n")
	b.WriteString("      --resume <session-id>        resume a saved session\n")
	b.WriteString("      --max-turns <n>              cap on agentic turns (default 10)\n")
	b.WriteString("  -s, --system-prompt <text>       replace the default system prompt\n")
	b.WriteString("      --append-system-prompt <t>   append to the default system prompt\n")
	b.WriteString("      --no-project-memory          skip FORGE.md discovery\n")
	b.WriteString("      --output-format <fmt>        text | json | stream-json\n")
	b.WriteString("  -v, --verbose                    enable debug-level logging\n")
	b.WriteString("      --api-key <key>              explicit API key override\n")
	b.WriteString("      --api-base <url>             API base URL override (default: NIM)\n")
	b.WriteString("      --max-tokens <n>             override the per-response output token cap\n")
	b.WriteString("      --cwd <path>                 working directory override\n")
	b.WriteString("      --dangerously-skip-permissions\n")
	b.WriteString("                                   shortcut for --permission-mode=bypass-permissions\n")
	b.WriteString("      --dump-system-prompt         print the assembled system prompt and exit\n")
	b.WriteString("      --mcp-config <path>          MCP server config JSON file\n")
	b.WriteString("      --plugin-dir <path>          directory to scan for plugins (repeatable)\n")
	b.WriteString("      --no-auto-compact            disable automatic context compaction\n")
	b.WriteString("  -h, --help                       print this help and exit\n")
	b.WriteString("      --version                    print version and exit\n")
	b.WriteString("\nEnvironment:\n")
	b.WriteString("  FORGE_API_KEY                    default API key (overridden by --api-key)\n")
	b.WriteString("  ANTHROPIC_API_KEY                Anthropic-specific default\n")
	b.WriteString("  FORGE_API_BASE                   API base URL override\n")
	b.WriteString("  FORGE_DEBUG                      any non-empty value enables debug logging\n")
	return b.String()
}

// ConfigDir returns the per-user config directory for this OS, e.g.
//   - Windows: %USERPROFILE%\.forge
//   - Unix:    $HOME/.forge
//
// Created lazily by main.go, not by this function. Centralized here
// so settings loading, conversation storage, and the memory directory
// all agree on the same path.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, core.ConfigDirName), nil
}
