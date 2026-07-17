// Package commands implements Forge's slash-command framework:
// the `/help`, `/clear`, `/compact`, `/cost`, `/model`, ... commands
// the user types into the TUI's input box.
//
// The shape of this package is governed by spec §6.1 (framework)
// and §6.2 (baseline command set). Every command implements the
// Command interface; the registry (AllCommands / FindCommand /
// ExecuteCommand) parses a leading `/`, looks up the command by
// name or alias, and returns its CommandResult — or returns nil
// for "not a recognized command, pass the input through to the
// query loop as a normal prompt."
//
// # Dependency direction
//
// Per spec §1, `commands` depends on `core` (for Config, CostTracker,
// Message) and reads `query` (it calls query.CompactConversation for
// `/compact`). It does NOT depend on `tools`, `api`, or `tui`. The
// TUI is the caller of this package, never the reverse:
//
//	cli → query → tools → core
//	  ↓       ↓
//	commands core
//	  ↓
//	 tui  →  core
//
// # What lands in Phase 3 vs Phase 4
//
// Phase 3 ships the subset of §6.2's baseline that is meaningful
// without Phase 4 features (no MCP, no plugins, no sessions browser,
// no goal loop, no multi-provider). The commands that need those
// features are either omitted entirely or registered as hidden
// stubs that print "not implemented in this phase." See baseline.go
// for the per-command breakdown.
//
// # CommandResult as a tagged union
//
// Spec §6.1 enumerates eight result kinds: Message, UserMessage,
// ConfigChange, ClearConversation, SetMessages, Exit, Silent,
// Error. We model this as a struct with a Kind discriminant plus
// payload fields (Text, Config, Messages) and a set of constructor
// helpers (MessageResult, ErrorResult, ExitResult, ...). This
// matches how the rest of the codebase models tagged unions
// (api.StreamEvent, query loop's Outcome).
package commands
