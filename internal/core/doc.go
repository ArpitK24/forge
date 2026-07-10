// Package core is the shared foundation of Forge. Every other internal
// package depends on this one; this one depends on nothing (besides
// the Go standard library and a small set of third-party packages
// added in later phases: github.com/google/uuid for session IDs,
// modernc.org/sqlite for the conversation store, etc.).
//
// The package is organized by concern:
//
//   - errors.go     — the single tagged *core.Error type (§2.1)
//   - messages.go   — Role, ContentBlock, Message, UsageInfo, ToolDefinition (§2.2)
//   - config.go     — Config, Settings, PermissionMode, OutputFormat, HookEvent (§2.3)
//   - permissions.go— PermissionDecision, PermissionHandler, AutoPermissionHandler (§2.6)
//   - constants.go  — every magic string/number lives here (§2.4)
//
// Other files added in later phases:
//   - session.go    — ConversationSession + persistence (§2.7)
//   - cost.go       — ModelPricing + CostTracker (§2.8)
//   - hooks.go      — run_hooks + HookOutcome (§2.9)
//   - context.go    — build_system_context / build_user_context (§2.5)
//   - keybindings.go— the central keybinding registry (§9.5)
//
// The strict one-directional dependency rule (§1) means: core depends
// on nothing. If you find yourself wanting to import an internal
// package from core, the answer is to move the type up here, not to
// break the dependency direction.
package core
