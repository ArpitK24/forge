// Package tools defines the tool framework and ships the Phase-2
// tool set (just Bash in this phase). Spec §3.1–§3.2 govern.
//
// The shape:
//
//   - Tool is the interface every tool implements. Execute
//     takes a context, a JSON input (the model-supplied args),
//     and a ToolContext (working dir, permission handler, cost
//     tracker, etc.), and returns a ToolResult.
//
//   - AllTools() returns every built-in tool (Bash in Phase 2,
//     plus Read/Edit/Write/Glob/Grep/etc. landing in Phase 2.1
//     and Phase 3). FindTool(name) looks one up by name.
//
//   - ToolResult is { Text, IsError, Metadata }. Spec §3.1:
//     "errors must never panic the process — every tool call is
//     caught and converted into a ToolResult::error." The query
//     loop adds a recover() on top of that to defend against
//     panics in tool code.
//
// Dependency direction: tools depends on core. Nothing else in
// this layer depends on tools. The query loop is the only
// caller.
package tools
