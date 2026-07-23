package tui

import (
	"testing"
)

// newHistoryModel returns a Model populated for history-
// only tests; nothing else in the Model matters.
func newHistoryModel() *Model {
	return &Model{InputHistory: []string{}, HistoryIndex: -1}
}

// TestHistoryAppendBasic verifies a fresh Model can
// accumulate entries in submission order.
func TestHistoryAppendBasic(t *testing.T) {
	m := newHistoryModel()
	m.historyAppend("first")
	m.historyAppend("second")
	m.historyAppend("third")
	if got := len(m.InputHistory); got != 3 {
		t.Fatalf("InputHistory len = %d, want 3", got)
	}
	want := []string{"first", "second", "third"}
	for i, w := range want {
		if m.InputHistory[i] != w {
			t.Errorf("InputHistory[%d] = %q, want %q", i, m.InputHistory[i], w)
		}
	}
	// After append, HistoryIndex resets to -1.
	if m.HistoryIndex != -1 {
		t.Errorf("HistoryIndex = %d after append, want -1", m.HistoryIndex)
	}
}

// TestHistoryAppendTrimsWhitespace covers the leading/
// trailing whitespace trim.
func TestHistoryAppendTrimsWhitespace(t *testing.T) {
	m := newHistoryModel()
	m.historyAppend("  hello  ")
	if len(m.InputHistory) != 1 || m.InputHistory[0] != "hello" {
		t.Errorf("expected trimmed 'hello', got %v", m.InputHistory)
	}
}

// TestHistoryAppendSkipsEmpty covers the "no-op for blank
// lines" rule. Pressing Enter on an empty input should not
// pollute history.
func TestHistoryAppendSkipsEmpty(t *testing.T) {
	m := newHistoryModel()
	m.historyAppend("")
	m.historyAppend("   ")
	m.historyAppend("\t\n")
	if len(m.InputHistory) != 0 {
		t.Errorf("expected empty history, got %v", m.InputHistory)
	}
}

// TestHistoryAppendSkipsConsecutiveDuplicates covers the
// "pressing Up after submitting doesn't re-show the same
// line" rule.
func TestHistoryAppendSkipsConsecutiveDuplicates(t *testing.T) {
	m := newHistoryModel()
	m.historyAppend("hello")
	m.historyAppend("hello")
	m.historyAppend("world")
	m.historyAppend("hello")
	if got := m.InputHistory; len(got) != 3 ||
		got[0] != "hello" || got[1] != "world" || got[2] != "hello" {
		t.Errorf("expected [hello, world, hello], got %v", got)
	}
}

// TestHistoryAppendCapsAtMax covers the bounded-growth rule.
// We don't import the constant to keep this test robust to
// tuning; instead we hard-code the cap number we expect.
// (Update this if the cap is changed.)
func TestHistoryAppendCapsAtMax(t *testing.T) {
	m := newHistoryModel()
	const cap = 500
	for i := 0; i < cap+50; i++ {
		m.historyAppend(string(rune('a' + (i % 26))))
	}
	if len(m.InputHistory) != cap {
		t.Errorf("InputHistory len = %d, want %d", len(m.InputHistory), cap)
	}
}

// TestHistoryPrevFromEmpty covers the no-history case.
// historyPrev on an empty history should return the
// current value unchanged.
func TestHistoryPrevFromEmpty(t *testing.T) {
	m := newHistoryModel()
	got := m.historyPrev("draft")
	if got != "draft" {
		t.Errorf("historyPrev on empty = %q, want 'draft'", got)
	}
	if m.HistoryIndex != -1 {
		t.Errorf("HistoryIndex = %d, want -1 (no history)", m.HistoryIndex)
	}
}

// TestHistoryPrevWalksBack verifies Up-arrow recall: the
// first Up returns the most-recent entry, the second
// returns the one before that, etc.
func TestHistoryPrevWalksBack(t *testing.T) {
	m := newHistoryModel()
	m.historyAppend("one")
	m.historyAppend("two")
	m.historyAppend("three")

	// First Up: most recent.
	if got := m.historyPrev("draft"); got != "three" {
		t.Errorf("first historyPrev = %q, want 'three'", got)
	}
	// Second Up: previous.
	if got := m.historyPrev("three"); got != "two" {
		t.Errorf("second historyPrev = %q, want 'two'", got)
	}
	// Third Up: oldest.
	if got := m.historyPrev("two"); got != "one" {
		t.Errorf("third historyPrev = %q, want 'one'", got)
	}
	// Fourth Up: at the beginning, no further change.
	if got := m.historyPrev("one"); got != "one" {
		t.Errorf("historyPrev at start = %q, want 'one'", got)
	}
}

// TestHistoryNextReturnsToDraft covers Down-arrow: after
// walking back, Down returns the next entry; pressing
// Down past the most-recent entry restores the user's
// pre-navigation draft (what was in the input before the
// first Up-arrow) and resets HistoryIndex to -1.
//
// The helper itself tracks the draft: historyPrev stores
// the current value as the draft on the first navigation
// step, and historyNext restores it on the step past the
// end. This matches readline / bash behavior so the user
// never loses their unsent text.
func TestHistoryNextReturnsToDraft(t *testing.T) {
	m := newHistoryModel()
	m.historyAppend("a")
	m.historyAppend("b")
	m.historyAppend("c")

	// First Up: "draft" is captured as the navigation
	// origin; we get back the most-recent entry.
	const draft = "draft"
	if got := m.historyPrev(draft); got != "c" {
		t.Fatalf("first historyPrev = %q, want 'c'", got)
	}
	if m.HistoryDraft != draft {
		t.Errorf("HistoryDraft = %q, want %q (saved on first Up)", m.HistoryDraft, draft)
	}
	m.historyPrev("c") // 'b'
	m.historyPrev("b") // 'a'

	// Walk forward.
	if got := m.historyNext("a"); got != "b" {
		t.Errorf("first historyNext = %q, want 'b'", got)
	}
	if got := m.historyNext("b"); got != "c" {
		t.Errorf("second historyNext = %q, want 'c'", got)
	}
	// Past the end: returns the saved draft and resets
	// the index.
	if got := m.historyNext("c"); got != draft {
		t.Errorf("historyNext past end = %q, want %q (the saved draft)", got, draft)
	}
	if m.HistoryIndex != -1 {
		t.Errorf("HistoryIndex after past-end = %d, want -1", m.HistoryIndex)
	}
	if m.HistoryDraft != "" {
		t.Errorf("HistoryDraft after past-end = %q, want '' (cleared)", m.HistoryDraft)
	}
}

// TestHistoryNextFromIdle covers the case where Down is
// pressed without first pressing Up: no-op, return
// current value, index stays at -1.
func TestHistoryNextFromIdle(t *testing.T) {
	m := newHistoryModel()
	m.historyAppend("a")
	got := m.historyNext("draft")
	if got != "draft" {
		t.Errorf("historyNext from idle = %q, want 'draft'", got)
	}
	if m.HistoryIndex != -1 {
		t.Errorf("HistoryIndex = %d, want -1", m.HistoryIndex)
	}
}

// TestHistoryAppendResetsIndex covers the rule that
// HistoryIndex resets to -1 after every successful append
// (so the next Up starts fresh from the most recent).
func TestHistoryAppendResetsIndex(t *testing.T) {
	m := newHistoryModel()
	m.historyAppend("first")
	m.historyPrev("draft") // index -> 0
	if m.HistoryIndex != 0 {
		t.Fatalf("setup: HistoryIndex = %d, want 0", m.HistoryIndex)
	}
	m.historyAppend("second")
	if m.HistoryIndex != -1 {
		t.Errorf("HistoryIndex after append = %d, want -1", m.HistoryIndex)
	}
}
