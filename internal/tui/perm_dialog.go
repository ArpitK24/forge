package tui

import (
	"fmt"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
)

// matchPermissionRule scans rules for the first entry whose Tool
// matches name and (if non-empty) ArgPattern is a substring of
// desc. Returns the rule and true on hit, zero-value and false
// otherwise. Used by the CheckPermission closure in program.go
// to short-circuit the dialog when a session-scoped "always"
// decision applies.
//
// Substring (not glob) matching is intentional for Phase 3 — the
// spec defers arg-pattern globs to Phase 3.1. The ArgPattern
// field is a literal substring today.
func matchPermissionRule(rules []core.PermissionRule, name, desc string) (core.PermissionRule, bool) {
	for _, r := range rules {
		if !strings.EqualFold(r.Tool, name) {
			continue
		}
		if r.ArgPattern != "" && !strings.Contains(desc, r.ArgPattern) {
			continue
		}
		return r, true
	}
	return core.PermissionRule{}, false
}

// permissionButtonLabels is the order of the four buttons in the
// dialog. The dialog's Focused field is the index into this slice.
// Order matches spec §9.5: Allow first (safest default), Allow
// always, Deny, Deny always. Esc always denies, regardless of
// the focused button.
var permissionButtonLabels = []string{
	"Allow",
	"Allow always",
	"Deny",
	"Deny always",
}

// permissionButtonDecisions maps the focused-button index to the
// PermissionDecision that activates it. Mirrors permissionButtonLabels.
var permissionButtonDecisions = []core.PermissionDecision{
	core.DecisionAllow,
	core.DecisionAllowPermanently,
	core.DecisionDeny,
	core.DecisionDenyPermanently,
}

// renderPermissionDialog returns the dialog body (not yet centered).
// View() wraps this in lipgloss.Place to overlay it on the base
// layout. The renderer is pure: same Model state always produces
// the same string.
func (m Model) renderPermissionDialog() string {
	d := m.PermissionDialog
	if d == nil {
		return ""
	}

	var body strings.Builder
	fmt.Fprintf(&body, "%s\n\n", dialogTitleStyle.Render("Permission needed"))
	fmt.Fprintf(&body, "Tool: %s\n", dialogToolNameStyle.Render(d.ToolName))
	if d.Description != "" {
		fmt.Fprintf(&body, "Action: %s\n", d.Description)
	}
	// For Bash, the loop's CheckPermission closure passes the
	// command line as the description; the dialog surfaces it in
	// its own block so it's visually distinct from the action
	// summary. We keep both so the user can read the verbatim
	// command before deciding.
	if d.Details != nil {
		if cmd, ok := d.Details["command"].(string); ok && cmd != "" {
			fmt.Fprintf(&body, "\nCommand:\n  %s\n", dialogCommandStyle.Render(cmd))
		}
	}

	body.WriteString("\n")
	body.WriteString(dialogBodyStyle.Render("Choose how to handle this call:"))
	body.WriteString("\n\n")

	// Button row. Each button is a fixed-width cell so the row
	// aligns regardless of focus. We render them with a small
	// separator between cells.
	for i, label := range permissionButtonLabels {
		style := dialogBtnUnfocused
		if i == d.Focused {
			style = dialogBtnFocused
		}
		body.WriteString(style.Render(" " + label + " "))
		if i < len(permissionButtonLabels)-1 {
			body.WriteString("  ")
		}
	}
	body.WriteString("\n\n")
	body.WriteString(dialogHintStyle.Render(
		"tab / shift+tab: move  •  enter: activate  •  esc: deny"))
	return dialogOverlayStyle.Render(body.String())
}
