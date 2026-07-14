// Package query is Forge's agentic turn-execution loop. Spec §5.
//
// The loop's job: turn a list of messages + a set of tools +
// a tool context into a finished assistant turn, possibly
// executing many tool calls along the way, returning a
// `Outcome` to the caller.
//
// The implementation here is the canonical Phase-2 loop body
// (turn counter, cancellation, request building, event drain,
// accumulation, stop-reason branching, tool execution, panic
// recovery, auto-compaction) — and it is the SINGLE agentic
// loop implementation in the whole codebase. The TUI REPL
// (Phase 3) and the ACP server (Phase 4) both drive this same
// function, not a duplicate one for each surface.
package query
