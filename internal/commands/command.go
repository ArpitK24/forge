package commands

import (
	"context"

	"github.com/ArpitK24/forge/internal/core"
)

// Command is the interface every slash command implements. Spec §6.1:
// name, aliases, description, help text, hidden flag, async execute.
//
// Implementations are stateless — all per-invocation state is passed
// in via CommandContext. The same Command instance is reused across
// every invocation in a session.
type Command interface {
	// Name is the canonical invocation, without the leading slash.
	// Must be lowercase ASCII (commands are matched case-insensitively,
	// but the canonical form is lowercase for display consistency).
	Name() string
	// Aliases are alternate invocations. `/exit`'s alias is `quit`.
	// May be nil. Aliases are matched case-insensitively.
	Aliases() []string
	// Description is the one-line summary shown in /help. Should fit
	// on one terminal line (~60 chars) and start with a lowercase
	// verb, e.g. "clear the visible transcript".
	Description() string
	// Help is the longer usage text shown for `/help <name>`. Multi-
	// line strings are fine; the renderer handles wrapping. Empty
	// means "no extra help; Description is enough."
	Help() string
	// Hidden is true for internal/debug commands that don't appear in
	// the default /help listing (e.g. dump-config). They still execute
	// if invoked directly.
	Hidden() bool
	// Execute runs the command. `args` is the raw text after the
	// command name (already trimmed of leading whitespace; may be
	// empty). Implementations MUST NOT panic — any error condition
	// is returned as a CommandResult with Kind == ResultError.
	// ctx is cancelled if the user aborts (Ctrl+C in the TUI).
	Execute(ctx context.Context, args string, cctx *CommandContext) CommandResult
}

// CommandContext carries the per-invocation state a command needs.
// Spec §6.1: active config, cost tracker, message history, working
// directory. The TUI constructs one CommandContext per session and
// passes the same pointer to every Execute call, so commands that
// mutate (e.g. /clear, /compact) can update the shared history in
// place.
type CommandContext struct {
	// Config is the active, resolved runtime configuration. Commands
	// that report on config (/model, /config, /status) read from it.
	// /config is read-only in Phase 3; mutations land in Phase 3.1.
	Config *core.Config
	// Cost is the shared session cost tracker. /cost and /status
	// read from it. May be nil in early tests; commands tolerate nil.
	Cost *core.CostTracker
	// Messages is the current conversation history. Shared with the
	// TUI and the query loop. Commands that rewrite history
	// (/clear, /compact) replace this slice via SetMessages or
	// ClearConversation results — they do NOT mutate the slice in
	// place, because the query loop goroutine may be reading it.
	Messages []core.Message
	// WorkingDir is the directory the TUI is running in. Used by
	// /init to scaffold a FORGE.md and by /status for display.
	WorkingDir string
}

// ResultKind discriminates the CommandResult variants. Spec §6.1.
type ResultKind int

const (
	// ResultMessage is the default: show Text to the user as an
	// informational/system message (not part of the conversation
	// history the model sees). Use for /help, /version, /cost output.
	ResultMessage ResultKind = iota
	// ResultUserMessage injects Text into the conversation as if the
	// user had typed it. Used by commands that want to seed the model
	// with a synthesized prompt.
	ResultUserMessage
	// ResultConfigChange applies a new Config. The TUI swaps the
	// active config; the next query-loop turn uses it. Phase 3.1
	// is where mutations like /model <name> start using this; Phase
	// 3's /model is read-only (ResultMessage).
	ResultConfigChange
	// ResultClearConversation asks the TUI to wipe the visible
	// transcript and reset Messages to empty. The model loses all
	// prior context. Used by /clear.
	ResultClearConversation
	// ResultSetMessages replaces the conversation history with the
	// supplied slice. Used by /compact (which produces a summarized
	// replacement) and /resume (Phase 3.1).
	ResultSetMessages
	// ResultExit asks the TUI to tear down cleanly. Used by /exit
	// and /quit.
	ResultExit
	// ResultSilent means "the command did something but wants no
	// visible output." Used by commands whose effect is purely a
	// side effect (e.g. a future /permissions grant).
	ResultSilent
	// ResultError is a recoverable error shown to the user. The
	// session continues; the user can retype. Distinct from a panic,
	// which never happens.
	ResultError
)

func (k ResultKind) String() string {
	switch k {
	case ResultMessage:
		return "message"
	case ResultUserMessage:
		return "user-message"
	case ResultConfigChange:
		return "config-change"
	case ResultClearConversation:
		return "clear-conversation"
	case ResultSetMessages:
		return "set-messages"
	case ResultExit:
		return "exit"
	case ResultSilent:
		return "silent"
	case ResultError:
		return "error"
	default:
		return "unknown"
	}
}

// CommandResult is what Execute returns. Spec §6.1's tagged union.
// Only the fields relevant to the active Kind are populated; the
// constructor helpers below make that explicit so callers don't
// have to remember which fields go with which Kind.
type CommandResult struct {
	Kind ResultKind
	// Text is the payload for ResultMessage, ResultUserMessage, and
	// ResultError. For ResultMessage it's rendered as a system line;
	// for ResultUserMessage it's pushed into the conversation; for
	// ResultError it's shown with error styling.
	Text string
	// Config is the payload for ResultConfigChange.
	Config *core.Config
	// Messages is the payload for ResultSetMessages.
	Messages []core.Message
}

// --- Constructors ---------------------------------------------------

// MessageResult returns a ResultMessage showing Text to the user.
func MessageResult(text string) CommandResult {
	return CommandResult{Kind: ResultMessage, Text: text}
}

// UserMessageResult returns a ResultUserMessage that injects Text
// into the conversation as a user prompt.
func UserMessageResult(text string) CommandResult {
	return CommandResult{Kind: ResultUserMessage, Text: text}
}

// ConfigChangeResult returns a ResultConfigChange that swaps the
// active config to c.
func ConfigChangeResult(c *core.Config) CommandResult {
	return CommandResult{Kind: ResultConfigChange, Config: c}
}

// ClearConversationResult returns a ResultClearConversation.
func ClearConversationResult() CommandResult {
	return CommandResult{Kind: ResultClearConversation}
}

// SetMessagesResult returns a ResultSetMessages that replaces the
// conversation history with msgs.
func SetMessagesResult(msgs []core.Message) CommandResult {
	return CommandResult{Kind: ResultSetMessages, Messages: msgs}
}

// ExitResult returns a ResultExit.
func ExitResult() CommandResult {
	return CommandResult{Kind: ResultExit}
}

// SilentResult returns a ResultSilent.
func SilentResult() CommandResult {
	return CommandResult{Kind: ResultSilent}
}

// ErrorResult returns a ResultError with the given message.
func ErrorResult(text string) CommandResult {
	return CommandResult{Kind: ResultError, Text: text}
}
