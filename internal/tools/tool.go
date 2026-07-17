package tools

import (
	"context"
	"encoding/json"

	"github.com/ArpitK24/forge/internal/core"
)

// Tool is the interface every built-in tool implements. Spec
// §3.1. Tools are stateless — all per-call state lives in
// the ToolContext.
type Tool interface {
	// Name returns the tool's canonical name. Must match a
	// constant in core (e.g. core.ToolBash).
	Name() string
	// Description is the human-readable description the model
	// sees. One to three sentences — long enough to teach the
	// model when to use the tool, short enough not to crowd
	// out other tool descriptions in the context.
	Description() string
	// PermissionLevel classifies the tool's safety posture.
	// The permission handler in ToolContext consults this.
	PermissionLevel() core.PermissionLevel
	// InputSchema returns the JSON Schema describing the
	// tool's input shape. Returned as a raw JSON message
	// (encoding/json) so the tool can hand-author the schema
	// without depending on a JSON Schema library.
	InputSchema() json.RawMessage
	// Execute runs the tool. Implementations MUST NOT panic:
	// any error condition is returned as ToolResult{IsError: true}.
	// ctx cancellation MUST interrupt long-running operations
	// (e.g. Bash's subprocess).
	Execute(ctx context.Context, input json.RawMessage, tc *ToolContext) ToolResult
}

// ToolResult is what every tool returns. Spec §3.1: success
// (Text + optional Metadata) or error (Text with IsError=true).
// Errors are first-class data — the model reads IsError and
// adjusts its next turn accordingly.
type ToolResult struct {
	// Text is the result body the model will read. For success
	// cases this is plain text; for errors it's a human-readable
	// description of what went wrong.
	Text string
	// IsError marks this result as a tool-execution failure
	// rather than a successful response.
	IsError bool
	// Metadata is optional structured context the tool wants
	// to surface to callers (and, where useful, to the model).
	// The Bash tool sets {"exit_code": N, "duration_ms": M};
	// other tools can set their own.
	Metadata map[string]any
}

// ToolContext is the per-call context passed to Execute. It
// carries the working directory, permission handler, cost
// tracker, and other shared state. The query loop constructs
// one ToolContext per top-level call and reuses it across
// every tool call in a turn (and across turns within a
// session).
type ToolContext struct {
	// WorkingDir is the directory the tool runs in. Most tools
	// resolve relative paths against this; Bash spawns in it.
	WorkingDir string
	// Permission is the permission handler. The tool MAY call
	// tc.Permission.RequestPermission(...) to ask for an
	// explicit decision, or rely on the loop's pre-check.
	// Spec §3.1.
	Permission core.PermissionHandler
	// CostTracker accumulates token usage. Phase 2's tools
	// don't add to it; the provider adapter does. The field
	// is here for tools that need to record their own cost
	// (e.g. WebSearch's per-call pricing).
	CostTracker *core.CostTracker
	// SessionID is the active session id. Empty in headless
	// mode where there's no persisted session.
	SessionID string
	// NonInteractive is true in headless / StreamJson / ACP
	// modes. Tools that normally prompt the user (e.g. an
	// "ask the user a question" tool) should auto-resolve
	// when this is true.
	NonInteractive bool
	// Cfg is the active Config. Tools that need to consult
	// it (timeout caps, model-aware behavior) read from here.
	Cfg *core.Config
	// ResolvePath resolves a (possibly relative) path against
	// WorkingDir. Returned paths are absolute. Provided so
	// each tool doesn't reimplement the logic.
	ResolvePath func(p string) (string, error)
	// CheckPermission is a convenience wrapper around
	// Permission.RequestPermission. The tool passes its name
	// and a short description; the handler returns the
	// decision. Tools that need a richer call (with details)
	// use tc.Permission directly.
	CheckPermission func(name, description string, isReadOnly bool) core.PermissionDecision
}

// AllTools returns every built-in tool, in the order the model
// should see them in its system prompt. Spec §3.2. Order is
// deliberate: the read/search tools first (the model reaches
// for these to understand context), then the write/edit tools
// (mutating, gated by PermWrite), then execution last (gated
// by PermExecute — the most privileged level).
//
// Phase 3 adds the five file/search tools (Read, Write, Edit,
// Glob, Grep). The remaining tools in the spec (WebFetch,
// Task, NotebookEdit, ApplyPatch, etc.) land in Phase 4.
func AllTools() []Tool {
	return []Tool{
		// Read/search (PermReadOnly — auto-allowed in Default).
		&ReadTool{},
		&GlobTool{},
		&GrepTool{},
		// File mutation (PermWrite).
		&WriteTool{},
		&EditTool{},
		// Process execution (PermExecute).
		&BashTool{},
	}
}

// FindTool looks up a tool by name (case-insensitive match
// against Tool.Name()). Returns the tool and true if found,
// or nil and false if not.
func FindTool(name string) (Tool, bool) {
	for _, t := range AllTools() {
		if eqFoldASCII(t.Name(), name) {
			return t, true
		}
	}
	return nil, false
}

// eqFoldASCII is a small ASCII-only case-insensitive equality
// check, avoiding the strings import for a one-call site.
func eqFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
