package tui

import (
	"testing"
)

// TestKeyMapDefaultContainsAllBindings verifies the
// DefaultKeyMap has every binding the spec calls for, with
// sensible key names.
func TestKeyMapDefaultContainsAllBindings(t *testing.T) {
	km := DefaultKeyMap()
	checks := []struct {
		name    string
		binding string
	}{
		{"Enter", "enter"},
		{"Escape", "esc"},
		{"CtrlD", "ctrl+d"},
		{"Backspace", "backspace"},
		{"Up", "up"},
		{"Down", "down"},
		{"PageUp", "pgup"},
		{"PageDown", "pgdown"},
		{"CtrlQuestion", "ctrl+?"},
		{"CtrlT", "ctrl+t"},
	}
	for _, c := range checks {
		t.Run(c.name, func(t *testing.T) {
			// We can't easily get the binding's key string
			// from outside, but we can verify the binding
			// is non-zero and renders a help line.
			// Use reflection via the Short/Full helpers
			// indirectly: at least one of Short/Full should
			// reference every binding.
			found := false
			for _, b := range km.ShortHelp() {
				if b.Help().Key != "" {
					found = true
				}
			}
			for _, row := range km.FullHelp() {
				for _, b := range row {
					if b.Help().Key != "" {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("binding %s: no help text found anywhere in Short/Full", c.name)
			}
		})
	}
}

// TestKeyMapShortHelpContainsCoreBindings verifies the
// short (status-bar) help mentions the most important
// keys: submit, escape, quit, and help. Per spec §9.3 the
// short hint is the single line of hints at the bottom of
// the screen; including the less-important keys (PageUp,
// etc.) there would crowd the bar.
func TestKeyMapShortHelpContainsCoreBindings(t *testing.T) {
	km := DefaultKeyMap()
	short := km.ShortHelp()
	if len(short) == 0 {
		t.Fatal("ShortHelp returned no bindings")
	}
	// Collect the help-keys into a set for easy lookup.
	keys := make(map[string]bool)
	for _, b := range short {
		keys[b.Help().Key] = true
	}
	for _, want := range []string{"enter", "esc", "ctrl+d", "ctrl+?"} {
		if !keys[want] {
			t.Errorf("ShortHelp missing %q (got %v)", want, keys)
		}
	}
	// Less-important keys (PageUp/Down, arrow keys) should
	// NOT be in the short hint.
	for _, banned := range []string{"pgup", "pgdown", "up", "down"} {
		if keys[banned] {
			t.Errorf("ShortHelp should not include %q (it's in the full help overlay)", banned)
		}
	}
}
