package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/core"
)

// saveStubSession writes a session with id + title + a known
// UpdatedAt so tests can verify ordering. (withTempHome is
// provided by session_save_test.go.)
func saveStubSession(t *testing.T, id, title string, at time.Time) {
	t.Helper()
	s := &core.ConversationSession{
		ID:        id,
		CreatedAt: at,
		UpdatedAt: at,
		Model:     core.DefaultModel,
		Title:     title,
		Messages:  []core.Message{core.NewUserText("hi from " + id)},
	}
	if err := core.SaveSession(s); err != nil {
		t.Fatalf("SaveSession(%q): %v", id, err)
	}
}

// openPicker is a one-liner for tests: builds a picker from
// the current on-disk sessions and mounts it on the Model.
func openPicker(t *testing.T, m *Model) {
	t.Helper()
	m.SessionPicker = openResumePicker()
	m.computeLayout()
}

// ---------------------------------------------------------------------------
// openResumePicker
// ---------------------------------------------------------------------------

// TestOpenPicker_PopulatesFromDisk saves three sessions and
// confirms the picker holds all of them, in the same order
// ListSessions returns (most-recent first).
func TestOpenPicker_PopulatesFromDisk(t *testing.T) {
	withTempHome(t)
	base := time.Now().Add(-1 * time.Hour)
	saveStubSession(t, "a", "alpha", base)
	saveStubSession(t, "b", "beta", base.Add(1*time.Second))
	saveStubSession(t, "c", "gamma", base.Add(2*time.Second))

	p := openResumePicker()
	if p == nil {
		t.Fatal("openResumePicker returned nil")
	}
	if len(p.Entries) != 3 {
		t.Fatalf("len(Entries) = %d, want 3", len(p.Entries))
	}
	want := []string{"c", "b", "a"} // most-recent first
	for i, id := range want {
		if p.Entries[i].ID != id {
			t.Errorf("Entries[%d].ID = %q, want %q", i, p.Entries[i].ID, id)
		}
	}
}

// TestOpenPicker_EmptyDir returns a picker with no entries
// when no sessions exist.
func TestOpenPicker_EmptyDir(t *testing.T) {
	withTempHome(t)
	p := openResumePicker()
	if p == nil {
		t.Fatal("openResumePicker returned nil")
	}
	if len(p.Entries) != 0 {
		t.Errorf("len(Entries) = %d, want 0", len(p.Entries))
	}
	if p.FilterInput.Placeholder == "" {
		t.Error("FilterInput.Placeholder should be set so the user can see the prompt")
	}
}

// ---------------------------------------------------------------------------
// Esc closes; Enter dispatches /resume <id>
// ---------------------------------------------------------------------------

// TestPicker_EscCloses verifies the modal contract: Esc exits
// the modal regardless of selection state. Matches H2.
func TestPicker_EscCloses(t *testing.T) {
	withTempHome(t)
	saveStubSession(t, "abc", "alpha", time.Now())

	m := newDialogModel()
	openPicker(t, m)
	if m.SessionPicker == nil {
		t.Fatal("setup: picker should be open")
	}
	updated, _ := m.handlePickerKey(tea.KeyMsg{Type: tea.KeyEsc})
	*m = updated.(Model)
	if m.SessionPicker != nil {
		t.Errorf("Esc should close picker, SessionPicker = %+v", m.SessionPicker)
	}
}

// TestPicker_EnterDispatchesResumeEnter verifies that pressing
// Enter on the highlighted session loads its history into
// m.shared.messages via the ResultSetMessages handler. We
// compare the assistant-rendered text on the model's
// Messages slice.
func TestPicker_EnterDispatchesResume(t *testing.T) {
	withTempHome(t)
	saveStubSession(t, "abc", "alpha", time.Now())

	m := newDialogModel()
	openPicker(t, m)

	updated, _ := m.handlePickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	*m = updated.(Model)

	if m.SessionPicker != nil {
		t.Errorf("Enter should close picker, SessionPicker = %+v", m.SessionPicker)
	}
	// The picker loaded session "abc" which has one user
	// message "hi from abc". After ResultSetMessages, that
	// becomes one rendered message with the same text.
	found := false
	for _, rm := range m.Messages {
		if rm.Text == "hi from abc" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("after picker Enter, Messages should include loaded user text; got: %+v", m.Messages)
	}
}

// TestPicker_EnterOnEmptyClosesOnly verifies that pressing
// Enter on an empty picker closes the picker and does NOT
// crash or dispatch anything.
func TestPicker_EnterOnEmptyClosesOnly(t *testing.T) {
	withTempHome(t)
	m := newDialogModel()
	openPicker(t, m)

	updated, _ := m.handlePickerKey(tea.KeyMsg{Type: tea.KeyEnter})
	*m = updated.(Model)
	if m.SessionPicker != nil {
		t.Error("Enter on empty picker should close it")
	}
}

// ---------------------------------------------------------------------------
// Filter narrows the visible list
// ---------------------------------------------------------------------------

// TestPicker_FilterNarrowsList types into the filter input
// and asserts the underlying list shrinks. We don't depend on
// j/k navigation — we just want the entries count to drop
// after the filter narrows.
func TestPicker_FilterNarrowsList(t *testing.T) {
	withTempHome(t)
	saveStubSession(t, "abc", "alpha", time.Now())
	saveStubSession(t, "def", "beta", time.Now().Add(1*time.Second))
	saveStubSession(t, "ghi", "gamma", time.Now().Add(2*time.Second))

	m := newDialogModel()
	openPicker(t, m)
	if got := len(m.SessionPicker.Entries); got != 3 {
		t.Fatalf("setup: Entries = %d, want 3", got)
	}

	// Type "alp" — should narrow to the alpha entry only.
	for _, r := range "alp" {
		updated, _ := m.handlePickerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		*m = updated.(Model)
	}
	if got := len(m.SessionPicker.List.Items()); got != 1 {
		t.Errorf("after filter 'alp', Items = %d, want 1", got)
	}
	// The first item must be "alpha".
	if it := m.SessionPicker.List.SelectedItem(); it != nil {
		if entry, ok := it.(resumePickerEntry); ok {
			if entry.session.ID != "abc" {
				t.Errorf("selected session = %q, want %q", entry.session.ID, "abc")
			}
		} else {
			t.Errorf("selected item is not resumePickerEntry: %T", it)
		}
	}
}

// TestPicker_FilterEmptyShowsAll clears the filter input and
// verifies the list returns to its full size.
func TestPicker_FilterEmptyShowsAll(t *testing.T) {
	withTempHome(t)
	saveStubSession(t, "abc", "alpha", time.Now())
	saveStubSession(t, "def", "beta", time.Now().Add(1*time.Second))

	m := newDialogModel()
	openPicker(t, m)

	// Type 'z' first to narrow, then backspace to clear.
	for _, r := range "z" {
		updated, _ := m.handlePickerKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		*m = updated.(Model)
	}
	if got := len(m.SessionPicker.List.Items()); got != 0 {
		t.Errorf("after filter 'z', Items = %d, want 0", got)
	}
	updated, _ := m.handlePickerKey(tea.KeyMsg{Type: tea.KeyBackspace})
	*m = updated.(Model)
	if got := len(m.SessionPicker.List.Items()); got != 2 {
		t.Errorf("after clearing filter, Items = %d, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// renderResumePicker
// ---------------------------------------------------------------------------

// TestRenderPicker_Empty covers the "no saved sessions yet"
// branch of the renderer. The overlay must still produce a
// non-empty string (otherwise the user sees a blank modal).
func TestRenderPicker_Empty(t *testing.T) {
	withTempHome(t)
	m := newDialogModel()
	openPicker(t, m)
	out := m.renderResumePicker()
	if out == "" {
		t.Error("renderResumePicker should return non-empty even with no sessions")
	}
	if !strings.Contains(out, "Resume a session") {
		t.Errorf("rendered picker should include the title, got: %q", out)
	}
	if !strings.Contains(out, "no saved sessions") {
		t.Errorf("rendered picker should mention 'no saved sessions' for empty list, got: %q", out)
	}
}

// TestRenderPicker_NonEmpty renders with sessions present.
// Asserts the title shows up and at least one session title
// appears in the output.
func TestRenderPicker_NonEmpty(t *testing.T) {
	withTempHome(t)
	saveStubSession(t, "abc", "alpha chat", time.Now())

	m := newDialogModel()
	openPicker(t, m)
	out := m.renderResumePicker()
	if !strings.Contains(out, "Resume a session") {
		t.Errorf("rendered picker missing title, got: %q", out)
	}
	if !strings.Contains(out, "alpha chat") {
		t.Errorf("rendered picker missing the saved-session title, got: %q", out)
	}
}

// TestRenderPicker_NilSafe: when SessionPicker is nil, the
// renderer returns "" instead of panicking. Update()'s View()
// branch checks this before calling.
func TestRenderPicker_NilSafe(t *testing.T) {
	m := newDialogModel()
	if got := m.renderResumePicker(); got != "" {
		t.Errorf("renderResumePicker with nil SessionPicker = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// recomputePickerSize
// ---------------------------------------------------------------------------

// TestRecomputePickerSize_NilSafe verifies recomputePickerSize
// is a no-op when the picker is closed.
func TestRecomputePickerSize_NilSafe(t *testing.T) {
	m := newDialogModel()
	m.Width = 80
	m.Height = 24
	// Should not panic.
	m.recomputePickerSize()
}

// TestRecomputePickerSize_AppliesToList verifies that after
// recomputePickerSize, the bubbles list has non-zero
// dimensions that match the requested cap.
func TestRecomputePickerSize_AppliesToList(t *testing.T) {
	withTempHome(t)
	m := newDialogModel()
	openPicker(t, m)
	m.Width = 120
	m.Height = 40
	m.recomputePickerSize()
	w := m.SessionPicker.List.Width()
	h := m.SessionPicker.List.Height()
	if w <= 0 || h <= 0 {
		t.Errorf("recomputePickerSize left list at %dx%d, want > 0", w, h)
	}
	if h > 21 {
		// The cap is 20; allow a small margin for off-by-one.
		t.Errorf("recomputePickerSize produced h=%d, want <= 20", h)
	}
}

// ---------------------------------------------------------------------------
// filterEntries (pure function — small surface, broad assertions)
// ---------------------------------------------------------------------------

func TestFilterEntries_SubstringCaseInsensitive(t *testing.T) {
	entries := []core.ConversationSession{
		{ID: "abc-1", Title: "Hello world"},
		{ID: "def-2", Title: "goodbye world"},
		{ID: "ghi-3", Title: "farewell"},
	}
	got := filterEntries(entries, "WORLD")
	if len(got) != 2 {
		t.Errorf("filter WORLD: len = %d, want 2", len(got))
	}
}

func TestFilterEntries_EmptyReturnsAll(t *testing.T) {
	entries := []core.ConversationSession{
		{ID: "a", Title: "alpha"},
		{ID: "b", Title: "beta"},
	}
	got := filterEntries(entries, "   ")
	if len(got) != 2 {
		t.Errorf("whitespace filter: len = %d, want 2", len(got))
	}
}

// ---------------------------------------------------------------------------
// pickerSelectedID
// ---------------------------------------------------------------------------

func TestPickerSelectedID_Empty(t *testing.T) {
	p := &SessionPickerState{}
	if got := pickerSelectedID(p); got != "" {
		t.Errorf("pickerSelectedID(empty) = %q, want \"\"", got)
	}
}

func TestPickerSelectedID_NilPicker(t *testing.T) {
	if got := pickerSelectedID(nil); got != "" {
		t.Errorf("pickerSelectedID(nil) = %q, want \"\"", got)
	}
}
