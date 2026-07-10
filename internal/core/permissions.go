package core

// PermissionDecision is the verdict of a permission check.
// Spec §2.6: Allow | AllowPermanently | Deny | DenyPermanently.
//
// "Permanent" decisions get persisted as PermissionRules in Settings
// and apply for the rest of the session (and across sessions if the
// user marks them as durable).
type PermissionDecision int

const (
	// DecisionAllow permits the tool call exactly once.
	DecisionAllow PermissionDecision = iota
	// DecisionAllowPermanently permits and remembers the decision
	// for the rest of the session (and persisted per Tool / ArgPattern).
	DecisionAllowPermanently
	// DecisionDeny denies the tool call exactly once.
	DecisionDeny
	// DecisionDenyPermanently denies and remembers the decision.
	DecisionDenyPermanently
)

func (d PermissionDecision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionAllowPermanently:
		return "allow-permanently"
	case DecisionDeny:
		return "deny"
	case DecisionDenyPermanently:
		return "deny-permanently"
	default:
		return "unknown"
	}
}

// PermissionRequest is what the caller passes to the permission handler
// when it needs a decision. Spec §2.6.
type PermissionRequest struct {
	// ToolName is the tool asking for permission (e.g. "Bash", "Edit").
	ToolName string
	// Description is a short human-readable summary of the action
	// (e.g. "run `npm install` in /home/user/proj"). The TUI
	// permission dialog renders this as the prompt.
	Description string
	// Details is optional structured context (the command line, the
	// file path, the URL, etc.) that the TUI can render alongside
	// the description.
	Details map[string]any
	// IsReadOnly is a hint the tool gives about its own safety: true
	// means the tool has no side effects, false means it does. The
	// permission handler may use this to short-circuit in Default mode.
	IsReadOnly bool
}

// PermissionHandler decides whether a tool call is allowed.
// Spec §2.6: two methods, both must be safe to call from any goroutine.
type PermissionHandler interface {
	// CheckPermission is a fast, non-blocking check. It returns the
	// final decision for this call (no prompting). Use it for tools
	// the handler has already decided about (cached rules, mode
	// shortcuts, etc.).
	CheckPermission(toolName string) PermissionDecision
	// RequestPermission is the slow path: it may prompt the user
	// (TUI) or block (auto-handler in headless mode). Returns the
	// final decision.
	RequestPermission(req PermissionRequest) PermissionDecision
}

// PermissionLevel is a coarse safety classification of a tool.
// Spec §3.1: every Tool has one of these.
type PermissionLevel int

const (
	// PermNone is for tools that have no side effects at all
	// (TodoWrite, TaskCreate, Sleep, GoalComplete, etc.). They never
	// require permission, even in Default mode.
	PermNone PermissionLevel = iota
	// PermReadOnly is for tools that read but don't mutate
	// (Read, Glob, Grep, WebFetch, WebSearch, etc.). Auto-allowed
	// in Default mode.
	PermReadOnly
	// PermWrite is for tools that mutate the filesystem
	// (Edit, Write, NotebookEdit, etc.). Requires permission in
	// Default mode, auto-allowed in AcceptEdits and BypassPermissions.
	PermWrite
	// PermExecute is for tools that spawn processes or talk to the
	// network (Bash, PowerShell, PtyBash, etc.). Requires permission
	// in Default and AcceptEdits modes, auto-allowed only in
	// BypassPermissions.
	PermExecute
)

func (l PermissionLevel) String() string {
	switch l {
	case PermNone:
		return "none"
	case PermReadOnly:
		return "read-only"
	case PermWrite:
		return "write"
	case PermExecute:
		return "execute"
	default:
		return "unknown"
	}
}

// AutoPermissionHandler is the non-interactive permission handler
// used in headless mode (CLI -p, StreamJson output, ACP). Spec §2.6.
type AutoPermissionHandler struct {
	// Mode is the active permission mode.
	Mode PermissionMode
}

// CheckPermission implements PermissionHandler.
func (h *AutoPermissionHandler) CheckPermission(toolName string) PermissionDecision {
	// In headless mode there are no cached rules — every check either
	// falls through to a mode-based default or defers to RequestPermission.
	// We return Deny here with a "no cached decision" semantic by
	// returning Deny for anything not in the auto-allow list. Callers
	// that need an actual decision should use RequestPermission.
	//
	// In practice, the query loop always calls RequestPermission, not
	// CheckPermission, so this is just a defensive default.
	return DecisionDeny
}

// RequestPermission implements PermissionHandler. Per spec §2.6:
//   - BypassPermissions: allow everything
//   - AcceptEdits: allow everything (broader than the TUI's AcceptEdits,
//     which only auto-allows writes — headless is more permissive because
//     there's no human to ask)
//   - Plan: allow only IsReadOnly, otherwise deny
//   - Default: allow only IsReadOnly, otherwise deny (i.e. headless
//     runs never silently mutate unless the user opted into a permissive
//     mode via a CLI flag)
func (h *AutoPermissionHandler) RequestPermission(req PermissionRequest) PermissionDecision {
	switch h.Mode {
	case PermissionBypassPermissions, PermissionAcceptEdits:
		return DecisionAllow
	case PermissionPlan, PermissionDefault:
		if req.IsReadOnly {
			return DecisionAllow
		}
		return DecisionDeny
	default:
		return DecisionDeny
	}
}
