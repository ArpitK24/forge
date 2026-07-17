package commands

import (
	"context"
	"strings"
)

// AllCommands returns every registered command, in the order /help
// lists them. Spec §6.1. Hidden commands are included here (so they
// execute if invoked directly); the /help renderer filters them.
func AllCommands() []Command {
	return baselineCommands()
}

// FindCommand looks up a command by exact name or alias, case-
// insensitively. Returns the command and true on a match, or nil
// and false if nothing matches (in which case the caller treats the
// input as a normal prompt and passes it to the query loop).
//
// Spec §6.1: the lookup matches on name OR any alias. `/quit` and
// `/exit` resolve to the same command.
func FindCommand(nameOrAlias string) (Command, bool) {
	if nameOrAlias == "" {
		return nil, false
	}
	needle := strings.ToLower(strings.TrimSpace(nameOrAlias))
	for _, c := range AllCommands() {
		if strings.EqualFold(c.Name(), needle) {
			return c, true
		}
		for _, a := range c.Aliases() {
			if strings.EqualFold(a, needle) {
				return c, true
			}
		}
	}
	return nil, false
}

// ExecuteCommand parses raw input and dispatches to the matching
// command. Spec §6.1.
//
// Behavior:
//
//   - If raw does not start with `/`, returns nil (pass-through —
//     the input is a normal prompt for the query loop).
//   - If the token after `/` is not a known command, returns nil
//     (pass-through) rather than an error. This matches the spec's
//     "returns None if nothing matches" semantics; a `/typo` is
//     treated as a prompt, not a fatal error. (If the TUI wants to
//     flag unknown commands, it can call FindCommand directly.)
//   - Otherwise, invokes the command with the remaining text (after
//     the command name) as args, and returns the CommandResult.
//
// The args string is everything after the first whitespace-separated
// token. Leading/trailing whitespace is trimmed.
func ExecuteCommand(ctx context.Context, raw string, cctx *CommandContext) *CommandResult {
	raw = strings.TrimRight(raw, "\r\n")
	if !strings.HasPrefix(raw, "/") {
		return nil // not a command
	}
	body := strings.TrimSpace(raw[1:])
	if body == "" {
		return nil // bare "/" — treat as pass-through
	}

	// Split into "name args…" on the first run of whitespace.
	name := body
	args := ""
	if i := strings.IndexAny(body, " \t"); i >= 0 {
		name = body[:i]
		args = strings.TrimSpace(body[i+1:])
	}

	cmd, ok := FindCommand(name)
	if !ok {
		return nil // unknown command — pass-through
	}
	res := cmd.Execute(ctx, args, cctx)
	return &res
}

// parseSubcommand splits args into a leading sub-word and the rest.
// Used by commands like `/goal pause` and `/mcp list`. Returns the
// sub-command (lowercased) and the remainder (untrimmed leading
// whitespace already stripped). Empty input → ("", "").
func parseSubcommand(args string) (sub, rest string) {
	args = strings.TrimSpace(args)
	if args == "" {
		return "", ""
	}
	if i := strings.IndexAny(args, " \t"); i >= 0 {
		return strings.ToLower(args[:i]), strings.TrimSpace(args[i+1:])
	}
	return strings.ToLower(args), ""
}
