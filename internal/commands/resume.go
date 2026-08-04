package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
)

// resumeCmd loads a previously-saved session's history into the
// current TUI run. Three argument shapes:
//
//   - No argument:    ask the TUI to open the sessions picker.
//     The picker's selection comes back as
//     `/resume <id>` and re-enters this command.
//   - Pure digits:    1-based index into the same sorted
//     ListSessions list the picker shows. So
//     `/resume 1` matches the topmost entry.
//   - Anything else:  treat as a session id and LoadSession
//     directly. UUIDs are the only realistic
//     id format, but we don't validate the
//     shape — LoadSession's file-not-found
//     error tells the user what went wrong.
//
// On success the loaded conversation replaces the current
// Messages slice (ResultSetMessages). On failure the result
// surfaces a friendly error; the session is unchanged so the
// user can retry.
//
// The picked/loaded session is NOT marked as the active
// session id — that's only set by --resume at the CLI layer.
// Mid-session /resume behaves as a "fork": the next save mints
// a fresh id.
type resumeCmd struct{}

func (resumeCmd) Name() string        { return "resume" }
func (resumeCmd) Aliases() []string   { return nil }
func (resumeCmd) Description() string { return "load a saved session (or open the picker)" }
func (resumeCmd) Hidden() bool        { return false }
func (resumeCmd) Help() string {
	return "/resume                  open the sessions picker\n" +
		"/resume <id>            load the session with this id directly\n" +
		"/resume <n>             load the nth session (1-based, most-recent first)"
}
func (resumeCmd) Execute(_ context.Context, args string, _ *CommandContext) CommandResult {
	arg := strings.TrimSpace(args)

	// No argument → open the picker. The picker dispatches the
	// canonical /resume <id> when the user picks one.
	if arg == "" {
		return OpenPickerResult("session-resume")
	}

	// Pure-digits argument → index lookup against ListSessions.
	// Matches the order the picker shows: most-recent first.
	if isAllDigits(arg) {
		return resumeByIndex(arg)
	}

	// Otherwise treat as a literal session id.
	return resumeByID(arg)
}

// resumeByIndex resolves a 1-based index into ListSessions and
// returns the loaded conversation. ListSessions itself sorts by
// UpdatedAt descending; the picker renders in the same order so
// `/resume 1` matches the topmost picker entry.
func resumeByIndex(arg string) CommandResult {
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 {
		return ErrorResult(fmt.Sprintf("resume: invalid index %q (must be a positive integer)", arg))
	}
	list, err := core.ListSessions()
	if err != nil {
		return ErrorResult(fmt.Sprintf("resume: list sessions: %v", err))
	}
	if n > len(list) {
		return ErrorResult(fmt.Sprintf("resume: index %d out of range (only %d saved sessions)", n, len(list)))
	}
	picked := list[n-1]
	return loadAndReturn(picked.ID)
}

// resumeByID loads the session with the given id and returns its
// conversation. Empty / whitespace-only ids are rejected up front
// so we don't pay the file-open cost.
func resumeByID(id string) CommandResult {
	if strings.TrimSpace(id) == "" {
		return ErrorResult("resume: empty session id")
	}
	return loadAndReturn(id)
}

// loadAndReturn is the shared "load + return SetMessages" path
// used by both the index and id branches. A missing or corrupt
// session surfaces as a friendly error; the current session is
// untouched.
func loadAndReturn(id string) CommandResult {
	s, err := core.LoadSession(id)
	if err != nil {
		return ErrorResult(fmt.Sprintf("resume: load %q: %v", id, err))
	}
	return SetMessagesResult(s.Messages)
}

// isAllDigits reports whether s consists entirely of ASCII
// digits. We use this — not strconv.Atoi — because the picker
// index lookup and the "load by id" branches must agree on
// what counts as a numeric argument. Atoi accepts signs and
// leading whitespace; we don't want /resume +1 to behave as
// "load the first saved session".
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
