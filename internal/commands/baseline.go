package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/query"
)

// baselineCommands returns the command set Phase 3 ships, wired
// against the spec §6.2 baseline. Commands that need Phase 4
// features (MCP, plugins, sessions browser, goal loop, multi-
// provider, OAuth) are either omitted entirely or registered as
// hidden stubs that say so. See each command's Help() for the
// per-command rationale.
//
// Order here is the order /help lists non-hidden commands in. Group
// related commands together (session, config, etc.) so the listing
// reads cleanly.
func baselineCommands() []Command {
	return []Command{
		// --- Session / transcript ---
		&helpCmd{},
		&clearCmd{},
		&compactCmd{},
		&exitCmd{}, // alias: quit
		&statusCmd{},

		// --- Config / model ---
		&modelCmd{},
		&configCmd{},
		&versionCmd{},
		&costCmd{},
		&thinkingCmd{},
		&permissionsCmd{},

		// --- Project ---
		&initCmd{},
		&diffCmd{},

		// --- Hidden / dev ---
		&dumpConfigCmd{},
		&dumpHistoryCmd{},
	}
}

// ---------------------------------------------------------------------------
// Session / transcript commands
// ---------------------------------------------------------------------------

// helpCmd lists every non-hidden command, or shows extended help
// for `/help <name>`.
type helpCmd struct{}

func (helpCmd) Name() string        { return "help" }
func (helpCmd) Aliases() []string   { return []string{"?"} }
func (helpCmd) Description() string { return "list commands, or show help for one" }
func (helpCmd) Hidden() bool        { return false }
func (helpCmd) Help() string        { return "" }
func (helpCmd) Execute(_ context.Context, args string, _ *CommandContext) CommandResult {
	// `/help <name>` — show that command's help.
	if name := strings.TrimSpace(args); name != "" {
		c, ok := FindCommand(name)
		if !ok {
			return ErrorResult(fmt.Sprintf("no such command: /%s", name))
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "/%s", c.Name())
		if len(c.Aliases()) > 0 {
			fmt.Fprintf(&sb, " (aliases: /%s)", strings.Join(c.Aliases(), ", /"))
		}
		sb.WriteString("\n")
		sb.WriteString(c.Description())
		if h := c.Help(); h != "" {
			sb.WriteString("\n\n")
			sb.WriteString(h)
		}
		return MessageResult(sb.String())
	}

	// `/help` — list every non-hidden command.
	visible := make([]Command, 0, 16)
	for _, c := range AllCommands() {
		if c.Hidden() {
			continue
		}
		visible = append(visible, c)
	}
	sort.Slice(visible, func(i, j int) bool {
		return visible[i].Name() < visible[j].Name()
	})
	var sb strings.Builder
	sb.WriteString("Available commands:\n")
	for _, c := range visible {
		line := fmt.Sprintf("  /%-14s %s", c.Name(), c.Description())
		if aliases := c.Aliases(); len(aliases) > 0 {
			line += fmt.Sprintf("  (/%s)", strings.Join(aliases, ", /"))
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	sb.WriteString("\nType /help <command> for more on a specific command.")
	return MessageResult(sb.String())
}

// clearCmd wipes the visible transcript and resets the conversation.
type clearCmd struct{}

func (clearCmd) Name() string        { return "clear" }
func (clearCmd) Aliases() []string   { return nil }
func (clearCmd) Description() string { return "clear the conversation history" }
func (clearCmd) Hidden() bool        { return false }
func (clearCmd) Help() string {
	return "Removes every message from the current session. The model " +
		"loses all prior context. The session cost totals are NOT reset " +
		"(use /cost to see them). This is irreversible."
}
func (clearCmd) Execute(_ context.Context, _ string, _ *CommandContext) CommandResult {
	return ClearConversationResult()
}

// compactCmd runs the manual compaction trigger. Calls
// query.CompactConversation on the current history and, if it
// produced a shorter slice, swaps it in.
type compactCmd struct{}

func (compactCmd) Name() string        { return "compact" }
func (compactCmd) Aliases() []string   { return nil }
func (compactCmd) Description() string { return "summarize the conversation to reclaim context" }
func (compactCmd) Hidden() bool        { return false }
func (compactCmd) Help() string {
	return "Replaces the conversation history with a shorter summary. " +
		"The most recent few turns are kept verbatim. Phase 3's " +
		"summarizer is a stub (the trigger math and the hook are real; " +
		"the actual summarization model call lands in Phase 4), so for " +
		"now this is effectively a no-op that reports the current length."
}
func (compactCmd) Execute(ctx context.Context, _ string, cctx *CommandContext) CommandResult {
	if cctx == nil {
		return ErrorResult("compact: no command context")
	}
	before := len(cctx.Messages)
	model := ""
	if cctx.Config != nil {
		model = cctx.Config.EffectiveModel()
	}
	compacted, err := query.CompactConversation(ctx, cctx.Messages, model)
	if err != nil {
		return ErrorResult(fmt.Sprintf("compact failed: %v", err))
	}
	after := len(compacted)
	if after == before {
		return MessageResult(fmt.Sprintf("Nothing to compact — the summarizer returned the history unchanged (%d messages).", before))
	}
	return SetMessagesResult(compacted)
}

// exitCmd tears the TUI down cleanly. Aliased as `quit`.
type exitCmd struct{}

func (exitCmd) Name() string        { return "exit" }
func (exitCmd) Aliases() []string   { return []string{"quit"} }
func (exitCmd) Description() string { return "exit forge" }
func (exitCmd) Hidden() bool        { return false }
func (exitCmd) Help() string {
	return "Saves the current session (if session persistence is on) and quits."
}
func (exitCmd) Execute(_ context.Context, _ string, _ *CommandContext) CommandResult {
	return ExitResult()
}

// statusCmd prints a one-screen session status.
type statusCmd struct{}

func (statusCmd) Name() string        { return "status" }
func (statusCmd) Aliases() []string   { return nil }
func (statusCmd) Description() string { return "show session status (model, turns, cost)" }
func (statusCmd) Hidden() bool        { return false }
func (statusCmd) Help() string        { return "" }
func (statusCmd) Execute(_ context.Context, _ string, cctx *CommandContext) CommandResult {
	if cctx == nil {
		return ErrorResult("status: no command context")
	}
	model := "(default)"
	if cctx.Config != nil {
		model = cctx.Config.EffectiveModel()
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Model:    %s\n", model)
	fmt.Fprintf(&sb, "Messages: %d\n", len(cctx.Messages))
	if cctx.Config != nil {
		fmt.Fprintf(&sb, "Mode:     %s\n", cctx.Config.PermissionMode)
		fmt.Fprintf(&sb, "MaxTurns: %d\n", cctx.Config.EffectiveMaxTurns())
	}
	if cctx.WorkingDir != "" {
		fmt.Fprintf(&sb, "Cwd:      %s\n", cctx.WorkingDir)
	}
	if cctx.Cost != nil {
		if s := cctx.Cost.Summary(); s != "" {
			fmt.Fprintf(&sb, "Cost:     %s\n", s)
		}
	}
	return MessageResult(strings.TrimRight(sb.String(), "\n"))
}

// ---------------------------------------------------------------------------
// Config / model commands
// ---------------------------------------------------------------------------

// modelCmd shows the active model. Phase 3 is read-only; switching
// the model live (`/model <name>`) lands in Phase 3.1 via
// ResultConfigChange.
type modelCmd struct{}

func (modelCmd) Name() string        { return "model" }
func (modelCmd) Aliases() []string   { return nil }
func (modelCmd) Description() string { return "show the active model" }
func (modelCmd) Hidden() bool        { return false }
func (modelCmd) Help() string {
	return "With no argument, prints the active model id. Switching " +
		"the model for the running session (/model <name>) lands in a " +
		"later phase; for now pass --model on the command line."
}
func (modelCmd) Execute(_ context.Context, args string, cctx *CommandContext) CommandResult {
	if cctx == nil || cctx.Config == nil {
		return ErrorResult("model: no config in context")
	}
	if name := strings.TrimSpace(args); name != "" {
		return ErrorResult(fmt.Sprintf(
			"switching the model mid-session is not implemented in this phase; "+
				"the active model is %s. Restart with --model %s to change it.",
			cctx.Config.EffectiveModel(), name))
	}
	return MessageResult(fmt.Sprintf("Active model: %s", cctx.Config.EffectiveModel()))
}

// configCmd prints the resolved Config as JSON.
type configCmd struct{}

func (configCmd) Name() string        { return "config" }
func (configCmd) Aliases() []string   { return nil }
func (configCmd) Description() string { return "print the resolved configuration" }
func (configCmd) Hidden() bool        { return false }
func (configCmd) Help() string {
	return "Dumps the fully-resolved runtime config (after defaults, " +
		"settings files, and CLI flags are layered). Read-only in this " +
		"phase; mutations land later."
}
func (configCmd) Execute(_ context.Context, _ string, cctx *CommandContext) CommandResult {
	if cctx == nil || cctx.Config == nil {
		return ErrorResult("config: no config in context")
	}
	out, err := jsonMarshalIndent(cctx.Config)
	if err != nil {
		return ErrorResult(fmt.Sprintf("config: marshal: %v", err))
	}
	return MessageResult(string(out))
}

// versionCmd prints the app version.
type versionCmd struct{}

func (versionCmd) Name() string        { return "version" }
func (versionCmd) Aliases() []string   { return []string{"v"} }
func (versionCmd) Description() string { return "print the forge version" }
func (versionCmd) Hidden() bool        { return false }
func (versionCmd) Help() string        { return "" }
func (versionCmd) Execute(_ context.Context, _ string, _ *CommandContext) CommandResult {
	return MessageResult(fmt.Sprintf("%s %s — %s", core.AppName, core.AppVersion, core.AppTagline))
}

// costCmd prints the session cost summary.
type costCmd struct{}

func (costCmd) Name() string        { return "cost" }
func (costCmd) Aliases() []string   { return nil }
func (costCmd) Description() string { return "show session token usage and cost" }
func (costCmd) Hidden() bool        { return false }
func (costCmd) Help() string        { return "" }
func (costCmd) Execute(_ context.Context, _ string, cctx *CommandContext) CommandResult {
	if cctx == nil || cctx.Cost == nil {
		return MessageResult("No usage recorded yet for this session.")
	}
	s := cctx.Cost.Summary()
	if s == "" {
		return MessageResult("No usage recorded yet for this session.")
	}
	return MessageResult(s)
}

// thinkingCmd shows the thinking configuration. Read-only in Phase 3;
// the actual toggle lands in Phase 3.1 with a keybinding.
type thinkingCmd struct{}

func (thinkingCmd) Name() string        { return "thinking" }
func (thinkingCmd) Aliases() []string   { return nil }
func (thinkingCmd) Description() string { return "show extended-thinking configuration" }
func (thinkingCmd) Hidden() bool        { return false }
func (thinkingCmd) Help() string {
	return "Phase 3 is read-only: it reports whether extended thinking is " +
		"on and the token budget. Toggling it live (Ctrl+T in the TUI, or " +
		"/thinking on|off) lands in a later phase."
}
func (thinkingCmd) Execute(_ context.Context, _ string, cctx *CommandContext) CommandResult {
	if cctx == nil || cctx.Config == nil {
		return ErrorResult("thinking: no config in context")
	}
	budget := cctx.Config.ThinkingBudget
	if budget <= 0 {
		return MessageResult("Extended thinking: off (budget = 0).")
	}
	return MessageResult(fmt.Sprintf("Extended thinking: on, budget = %d tokens.", budget))
}

// permissionsCmd lists the current permission posture and any
// persisted rules, and (with an argument) switches the active
// mode for the rest of the session. The argument is one of
// the four PermissionMode values: default, accept-edits,
// bypass-permissions, plan. Without an argument, the command
// is read-only.
//
// "Allow always" / "Deny always" decisions made via the TUI
// dialog are appended to Config.PermissionRules in-memory;
// disk persistence is Phase 3.1.
type permissionsCmd struct{}

func (permissionsCmd) Name() string      { return "permissions" }
func (permissionsCmd) Aliases() []string { return []string{"perm"} }
func (permissionsCmd) Description() string {
	return "show the current permission mode and rules (or set with /permissions <mode>)"
}
func (permissionsCmd) Hidden() bool { return false }
func (permissionsCmd) Help() string {
	return "/permissions                         show the current mode and rules\n" +
		"/permissions <mode>                  set the active mode (default, accept-edits, bypass-permissions, plan)"
}
func (permissionsCmd) Execute(_ context.Context, args string, cctx *CommandContext) CommandResult {
	if cctx == nil || cctx.Config == nil {
		return ErrorResult("permissions: no config in context")
	}
	modeArg := strings.TrimSpace(args)
	if modeArg == "" {
		// Read-only display.
		var sb strings.Builder
		fmt.Fprintf(&sb, "Active mode: %s\n", cctx.Config.PermissionMode)
		sb.WriteString("Persisted rules: (none — disk persistence lands in a later phase)")
		return MessageResult(sb.String())
	}
	// Argument path: switch the active mode for the rest of
	// the session. core.ParsePermissionMode handles unknown
	// values by returning ok=false.
	newMode, ok := core.ParsePermissionMode(modeArg)
	if !ok {
		return ErrorResult(fmt.Sprintf(
			"permissions: unknown mode %q (try: default, accept-edits, bypass-permissions, plan)",
			modeArg))
	}
	if newMode == cctx.Config.PermissionMode {
		return MessageResult(fmt.Sprintf("Permission mode is already %s.", newMode))
	}
	// Build a shallow copy of the config with the new mode.
	// The in-memory rule list is shared (same underlying array)
	// so "always" decisions survive the mode switch.
	newCfg := *cctx.Config
	newCfg.PermissionMode = newMode
	return ConfigChangeResult(&newCfg)
}

// ---------------------------------------------------------------------------
// Project commands
// ---------------------------------------------------------------------------

// initCmd scaffolds a FORGE.md memory file in the working dir, only
// if one is missing.
type initCmd struct{}

func (initCmd) Name() string        { return "init" }
func (initCmd) Aliases() []string   { return nil }
func (initCmd) Description() string { return "scaffold a FORGE.md project memory file" }
func (initCmd) Hidden() bool        { return false }
func (initCmd) Help() string {
	return "Creates a stub FORGE.md in the working directory (if none " +
		"exists) with a short template. FORGE.md is walked upward and " +
		"injected into the system prompt, so notes you put there are " +
		"seen by the model on every turn."
}
func (initCmd) Execute(_ context.Context, _ string, cctx *CommandContext) CommandResult {
	dir := "."
	if cctx != nil && cctx.WorkingDir != "" {
		dir = cctx.WorkingDir
	}
	path := filepath.Join(dir, core.MemoryFileName)
	if exists, err := fileExists(path); err != nil {
		return ErrorResult(fmt.Sprintf("init: %v", err))
	} else if exists {
		return MessageResult(fmt.Sprintf("FORGE.md already exists at %s — leaving it untouched.", path))
	}
	const tmpl = `# Project memory

> Notes here are injected into the model's system prompt on every
> turn. Keep it short and high-signal: build commands, conventions,
> gotchas, the layout of the repo.

## Build / test
- (e.g. ` + "`go build ./... && go test ./...`" + `)

## Conventions
-
`
	if err := writeFile(path, []byte(tmpl), 0o644); err != nil {
		return ErrorResult(fmt.Sprintf("init: write %s: %v", path, err))
	}
	return MessageResult(fmt.Sprintf("Created %s — edit it to capture project context.", path))
}

// diffCmd prints the session's accumulated diff. Stub in Phase 3 —
// there's no Edit/Write tracking yet; the full implementation lands
// in Phase 3.1.
type diffCmd struct{}

func (diffCmd) Name() string        { return "diff" }
func (diffCmd) Aliases() []string   { return nil }
func (diffCmd) Description() string { return "show the session's accumulated file changes" }
func (diffCmd) Hidden() bool        { return false }
func (diffCmd) Help() string {
	return "Phase 3 stub: no per-session change tracking yet. Use `git " +
		"diff` via the Bash tool in the meantime. The full implementation " +
		"(tracking every Write/Edit since session start) lands in a " +
		"later phase."
}
func (diffCmd) Execute(_ context.Context, _ string, _ *CommandContext) CommandResult {
	return MessageResult("No changes tracked for this session yet (diff viewer is a stub in this phase).")
}

// ---------------------------------------------------------------------------
// Hidden / dev commands
// ---------------------------------------------------------------------------

// dumpConfigCmd is the unfiltered config dump (always shows every
// field, including zero values that /config may omit). Hidden so it
// doesn't clutter /help.
type dumpConfigCmd struct{}

func (dumpConfigCmd) Name() string        { return "dump-config" }
func (dumpConfigCmd) Aliases() []string   { return nil }
func (dumpConfigCmd) Description() string { return "[dev] dump the full resolved config as JSON" }
func (dumpConfigCmd) Hidden() bool        { return true }
func (dumpConfigCmd) Help() string        { return "" }
func (dumpConfigCmd) Execute(_ context.Context, _ string, cctx *CommandContext) CommandResult {
	if cctx == nil || cctx.Config == nil {
		return ErrorResult("dump-config: no config in context")
	}
	out, err := jsonMarshalIndent(cctx.Config)
	if err != nil {
		return ErrorResult(fmt.Sprintf("dump-config: marshal: %v", err))
	}
	return MessageResult(string(out))
}

// dumpHistoryCmd dumps the conversation history as JSON. Hidden.
type dumpHistoryCmd struct{}

func (dumpHistoryCmd) Name() string        { return "dump-history" }
func (dumpHistoryCmd) Aliases() []string   { return nil }
func (dumpHistoryCmd) Description() string { return "[dev] dump the conversation history as JSON" }
func (dumpHistoryCmd) Hidden() bool        { return true }
func (dumpHistoryCmd) Help() string        { return "" }
func (dumpHistoryCmd) Execute(_ context.Context, _ string, cctx *CommandContext) CommandResult {
	if cctx == nil {
		return ErrorResult("dump-history: no command context")
	}
	if len(cctx.Messages) == 0 {
		return MessageResult("[]")
	}
	out, err := jsonMarshalIndent(cctx.Messages)
	if err != nil {
		return ErrorResult(fmt.Sprintf("dump-history: marshal: %v", err))
	}
	return MessageResult(string(out))
}
