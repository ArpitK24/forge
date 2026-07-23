package tui

import "strings"

// Input history persistence (readline-style up/down recall).
// Per-session in-memory in Phase 3; disk persistence in Phase 3.1.
//
// The history walker tracks the user's "draft" — what was
// in the input box before the first Up-arrow. When the user
// walks forward past the most-recent entry, we restore the
// draft. This matches every other readline-style UI (bash,
// zsh, fish, Claude Code, etc.) so the user never loses
// what they were typing.

// historyAppend adds a non-empty entry to the input history.
// Duplicates of the most recent entry are suppressed (so
// pressing Up after submitting doesn't re-show the same line).
func (m *Model) historyAppend(line string) {
	line = trimWhitespace(line)
	if line == "" {
		return
	}
	// Suppress consecutive duplicates.
	if len(m.InputHistory) > 0 && m.InputHistory[len(m.InputHistory)-1] == line {
		return
	}
	m.InputHistory = append(m.InputHistory, line)
	// Cap history size to avoid unbounded growth.
	const maxHistory = 500
	if len(m.InputHistory) > maxHistory {
		m.InputHistory = m.InputHistory[len(m.InputHistory)-maxHistory:]
	}
	m.HistoryIndex = -1 // reset cursor after append
	m.HistoryDraft = "" // any pre-existing draft is now stale
}

// historyPrev recalls the previous entry in the input history.
// Returns the text to place in the input box (or empty if at
// the beginning of history).
//
// On the FIRST call of a navigation session (HistoryIndex
// == -1), we save the current value as the "draft" so it
// can be restored when the user walks back past the most-
// recent entry. This matches readline / bash behavior.
func (m *Model) historyPrev(current string) string {
	// If we're not browsing history yet, save the current
	// input and start browsing from the end.
	if m.HistoryIndex == -1 {
		if len(m.InputHistory) == 0 {
			return current
		}
		m.HistoryDraft = current
		m.HistoryIndex = len(m.InputHistory) - 1
		return m.InputHistory[m.HistoryIndex]
	}
	if m.HistoryIndex <= 0 {
		return current
	}
	m.HistoryIndex--
	return m.InputHistory[m.HistoryIndex]
}

// historyNext recalls the next entry (toward the present).
// When the user walks past the most-recent entry, restores
// the saved draft (what was in the input before any history
// navigation began) and resets the index.
func (m *Model) historyNext(current string) string {
	if m.HistoryIndex == -1 {
		return current
	}
	if m.HistoryIndex >= len(m.InputHistory)-1 {
		m.HistoryIndex = -1
		// Return the saved draft. We've been returning
		// "current" in older versions, which is wrong:
		// after walking back to the most-recent entry,
		// "current" is that entry's text, not the user's
		// original draft. The draft is what we want.
		draft := m.HistoryDraft
		m.HistoryDraft = ""
		return draft
	}
	m.HistoryIndex++
	return m.InputHistory[m.HistoryIndex]
}

// trimWhitespace strips leading and trailing whitespace from s.
func trimWhitespace(s string) string {
	return strings.TrimSpace(s)
}
