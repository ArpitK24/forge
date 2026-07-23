package tui

import "testing"

// newLayoutModel returns a Model populated for layout
// tests: just enough to let computeLayout() operate
// without nil-dereferencing on the embedded textarea or
// viewport. WorkingDir is unused; the layout never touches
// the command context.
func newLayoutModel() *Model {
	m := InitialModel(nil, nil)
	return &m
}

// TestComputeLayoutMinimumSize covers the smallest legal
// terminal: 1 row, 1 column. The message pane gets 1 row
// and the input is clipped (the formula clamps).
func TestComputeLayoutMinimumSize(t *testing.T) {
	m := newLayoutModel()
	m.Width = 1
	m.Height = 1
	m.computeLayout()
	if m.Viewport.Width < 1 {
		t.Errorf("Viewport.Width = %d, want >= 1", m.Viewport.Width)
	}
	if m.Viewport.Height < 1 {
		t.Errorf("Viewport.Height = %d, want >= 1", m.Viewport.Height)
	}
}

// TestComputeLayoutSplitsRowsCorrectly verifies the row
// accounting: messages + input (with border) + status
// must equal the terminal height (or less, with at least
// 1 row for messages).
func TestComputeLayoutSplitsRowsCorrectly(t *testing.T) {
	cases := []struct{ w, h int }{
		{80, 24},  // standard terminal
		{120, 40}, // wide, tall
		{40, 10},  // small
		{200, 50}, // very wide
	}
	for _, c := range cases {
		m := newLayoutModel()
		m.Width = c.w
		m.Height = c.h
		m.computeLayout()
		// The layout formula is:
		//   maxInputH = clamp(h/5, minInputHeight, 10)
		//   inputH    = maxInputH
		//   messageH  = h - inputH - borderHeight - statusHeight
		//              (clamped to >= 1)
		maxInputH := c.h / 5
		if maxInputH > 10 {
			maxInputH = 10
		}
		if maxInputH < minInputHeight {
			maxInputH = minInputHeight
		}
		expectedMsg := c.h - maxInputH - borderHeight - statusHeight
		if expectedMsg < 1 {
			expectedMsg = 1
		}
		if m.Viewport.Height != expectedMsg {
			t.Errorf("(%d x %d): Viewport.Height = %d, want %d",
				c.w, c.h, m.Viewport.Height, expectedMsg)
		}
	}
}

// TestComputeLayoutInputWidth verifies the input box
// width is narrower than the terminal (border padding).
// The exact delta is dictated by bubbles/textarea
// (rounded border + inner padding + cursor column),
// so we don't pin a magic number; we just assert that
// (a) it's strictly less than the terminal width and
// (b) it's positive even on a 1-column terminal.
func TestComputeLayoutInputWidth(t *testing.T) {
	m := newLayoutModel()
	m.Width = 80
	m.Height = 24
	m.computeLayout()
	if got := m.Input.Width(); got >= 80 {
		t.Errorf("Input.Width = %d, want < 80 (should account for border padding)", got)
	}
	if got := m.Input.Width(); got < 1 {
		t.Errorf("Input.Width = %d, want >= 1", got)
	}
}

// TestComputeLayoutRespectsMinInputHeight covers the
// very-short terminal case: the input box should be at
// least minInputHeight even when the terminal is shorter
// than 5x that.
func TestComputeLayoutRespectsMinInputHeight(t *testing.T) {
	// 5 rows tall. The layout formula gives maxInputH = 5/5 = 1,
	// which is below minInputHeight (3). The implementation
	// should clamp up to minInputHeight.
	m := newLayoutModel()
	m.Width = 80
	m.Height = 5
	m.computeLayout()
	if got := m.Input.Height(); got < minInputHeight {
		t.Errorf("Input.Height = %d, want >= %d (minInputHeight)",
			got, minInputHeight)
	}
}
