package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/core"
)

// resumePickerEntry is what bubbles/list actually displays. We
// carry the full session record alongside so the dispatcher
// can read the id without re-list-mapping; the bubbles list
// accepts any fmt.Stringer.
type resumePickerEntry struct {
	session core.ConversationSession
}

func (e resumePickerEntry) Title() string {
	if e.session.Title != "" {
		return e.session.Title
	}
	// Sessions written before DeriveTitle landed have an empty
	// Title on disk. The list view runs against ListSessions
	// which backfills the title on read, so this branch is
	// defensive — kept here in case someone constructs an
	// entry directly.
	return "(untitled)"
}

func (e resumePickerEntry) Description() string {
	ts := e.session.UpdatedAt.Local().Format("2006-01-02 15:04")
	if e.session.WorkingDir != "" {
		return fmt.Sprintf("%s  •  %s", ts, e.session.WorkingDir)
	}
	return ts
}

func (e resumePickerEntry) FilterValue() string {
	// Fuzzy-match against title + id so the user can filter by
	// either. Whitespace is included verbatim.
	return e.session.Title + " " + e.session.ID
}

// openResumePicker builds a fresh SessionPickerState from the
// current on-disk session list. Called when a command returns
// ResultOpenPicker("session-resume"); the picker is then
// mounted on the Model by Update.
//
// On error (e.g. the conversations directory cannot be read),
// an empty picker is returned — the user just sees "no saved
// sessions" and Esc closes it. We don't surface the error in
// the picker overlay because the modal contract is "use this
// to pick a session"; failures belong on the status line.
func openResumePicker() *SessionPickerState {
	listEntries, err := core.ListSessions()
	if err != nil {
		listEntries = nil
	}
	items := make([]list.Item, len(listEntries))
	for i, s := range listEntries {
		items[i] = resumePickerEntry{session: s}
	}

	// Default list dimensions; recomputePickerSize() updates
	// these on resize.
	const defaultPickerHeight = 12
	const defaultPickerWidth = 60
	l := list.New(items, resumePickerDelegate{}, defaultPickerWidth, defaultPickerHeight)
	l.Title = "Resume a session"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false) // we own the filter
	l.SetShowHelp(false)

	ti := textinput.New()
	ti.Placeholder = "filter…"
	ti.CharLimit = 64
	ti.Prompt = "> "
	ti.Focus() // textinput.Update is a no-op when not focused;
	// without this, typed runes silently pass through the
	// picker without ever landing in the filter value.

	return &SessionPickerState{
		Entries:     listEntries,
		FilterInput: ti,
		List:        l,
	}
}

// resumePickerDelegate renders each row in the session list.
// Minimal: title in bold-ish color, description below in
// dimmer text. We don't import lipgloss here — the project
// keeps styling local to layout.go's overlay code, so plain
// strings are fine.
type resumePickerDelegate struct{}

func (resumePickerDelegate) Height() int                             { return 2 }
func (resumePickerDelegate) Spacing() int                            { return 1 }
func (resumePickerDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }
func (resumePickerDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	e, ok := item.(resumePickerEntry)
	if !ok {
		return
	}
	// Cursor: prefix with the marker; render the rest plainly.
	if index == m.Index() {
		fmt.Fprint(w, "> ")
	} else {
		fmt.Fprint(w, "  ")
	}
	fmt.Fprintf(w, "%s\n", e.Title())
	fmt.Fprintf(w, "    %s\n", e.Description())
}

// handlePickerKey routes one key press while the picker is
// open. The picker owns all input while m.SessionPicker !=
// nil; this function is invoked from Update's top short-
// circuit.
//
// Behavior:
//   - Esc closes the picker without dispatching anything.
//   - Enter dispatches `/resume <id>` for the selected entry
//     through the normal dispatchCommand path. The picker
//     closes regardless of the dispatch result.
//   - All other keys are forwarded to the filter input
//     first (so typing focuses the filter) and, if not
//     consumed, fall through to the list (for j/k/arrows).
func (m Model) handlePickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.SessionPicker == nil {
		return m, nil
	}
	sp := m.SessionPicker

	// Esc closes unconditionally — matches the spec "Esc
	// leaves the modal" decision documented in H2.
	if k.Type == tea.KeyEsc {
		m.SessionPicker = nil
		return m, nil
	}

	// Enter dispatches /resume <id> for the selected entry
	// and closes the picker. Empty list → Enter is a no-op
	// (closing the picker is also fine, but be explicit).
	if k.Type == tea.KeyEnter {
		id := pickerSelectedID(sp)
		m.SessionPicker = nil
		if id == "" {
			return m, nil
		}
		// Reuse the existing dispatchCommand path: the
		// input box uses /resume <id> via this exact call,
		// so the picker's selection gets the same
		// ResultSetMessages handling and the same loop
		// re-subscribe.
		return m.dispatchCommand("/resume " + id)
	}

	// First, route the key into the filter input. textinput
	// consumes runes, backspace, and a few control keys; we
	// want typeahead to take priority over list navigation
	// so the user can filter-then-narrow without pressing
	// Tab.
	var tiCmd tea.Cmd
	sp.FilterInput, tiCmd = sp.FilterInput.Update(k)

	// Re-filter the list against the current filter text.
	// list.SetItems is the documented way to apply a filter
	// when SetFilteringEnabled is false (we own the filter).
	filterText := sp.FilterInput.Value()
	filtered := filterEntries(sp.Entries, filterText)
	items := make([]list.Item, len(filtered))
	for i, s := range filtered {
		items[i] = resumePickerEntry{session: s}
	}
	sp.List.SetItems(items)
	// Reset selection to the top after a filter change —
	// otherwise the cursor can land past the end of a
	// shorter list.
	sp.List.ResetSelected()

	// Also forward the key to the list so j/k/arrows still
	// work when the filter input doesn't claim them. We
	// always pass it through (the list's internal logic
	// ignores keys it doesn't care about).
	var listCmd tea.Cmd
	sp.List, listCmd = sp.List.Update(k)

	// Combine both Cmds; bubbletea runs them sequentially.
	if tiCmd != nil && listCmd != nil {
		return m, tea.Batch(tiCmd, listCmd)
	}
	if tiCmd != nil {
		return m, tiCmd
	}
	return m, listCmd
}

// pickerSelectedID returns the session id of the currently
// highlighted entry, or "" if the list is empty.
func pickerSelectedID(sp *SessionPickerState) string {
	if sp == nil {
		return ""
	}
	it := sp.List.SelectedItem()
	if it == nil {
		return ""
	}
	e, ok := it.(resumePickerEntry)
	if !ok {
		return ""
	}
	return e.session.ID
}

// filterEntries narrows the saved-session list by case-
// insensitive substring against title + id. Empty filter →
// all entries.
func filterEntries(entries []core.ConversationSession, q string) []core.ConversationSession {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return entries
	}
	out := make([]core.ConversationSession, 0, len(entries))
	for _, s := range entries {
		hay := strings.ToLower(s.Title + " " + s.ID)
		if strings.Contains(hay, q) {
			out = append(out, s)
		}
	}
	return out
}

// recomputePickerSize updates the bubbles list dimensions on
// resize. Called from computeLayout. We cap the height at
// roughly half the terminal so the picker doesn't crowd the
// chat when the user shrinks the window.
func (m *Model) recomputePickerSize() {
	if m.SessionPicker == nil || m.Width == 0 || m.Height == 0 {
		return
	}
	w := m.Width - 4
	if w < 20 {
		w = 20
	}
	h := m.Height / 2
	if h < 6 {
		h = 6
	}
	if h > 20 {
		h = 20
	}
	m.SessionPicker.List.SetSize(w, h)
	m.SessionPicker.FilterInput.Width = w
}

// renderResumePicker produces the picker overlay string.
// Pure: same Model state always yields the same output.
// Centered via the same dialogOverlayStyle path the permission
// dialog uses.
func (m Model) renderResumePicker() string {
	if m.SessionPicker == nil {
		return ""
	}
	sp := m.SessionPicker

	var body strings.Builder
	body.WriteString(dialogTitleStyle.Render("Resume a session"))
	body.WriteByte('\n')
	body.WriteString(dialogHintStyle.Render(
		"type to filter  •  enter: load  •  esc: cancel"))
	body.WriteByte('\n')
	body.WriteByte('\n')
	body.WriteString(sp.FilterInput.View())
	body.WriteByte('\n')
	body.WriteByte('\n')
	if len(sp.Entries) == 0 {
		body.WriteString(dialogBodyStyle.Render(
			"(no saved sessions yet — exit once to create one)"))
	} else {
		body.WriteString(sp.List.View())
	}
	return dialogOverlayStyle.Render(body.String())
}
