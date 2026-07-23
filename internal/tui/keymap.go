package tui

import (
	"github.com/charmbracelet/bubbles/key"
)

// KeyMap holds the TUI's keybindings, resolved through named
// actions. Per spec §9.3: every binding is looked up by name,
// never hardcoded inside a widget. Users can remap bindings in
// a future phase by changing the key in one place here.
type KeyMap struct {
	// Submit the current input.
	Enter key.Binding
	// Context-dependent: cancel streaming turn OR clear input
	// OR quit (disambiguated by state, not by separate keys).
	Escape key.Binding
	// Quit when input is empty (dedicated, no ambiguity).
	CtrlD key.Binding
	// Delete character before cursor.
	Backspace key.Binding
	// Recall previous/next input from history (at input edges).
	Up   key.Binding
	Down key.Binding
	// Scroll the message pane.
	PageUp   key.Binding
	PageDown key.Binding
	// Toggle the help overlay.
	CtrlQuestion key.Binding
	// Toggle extended thinking flag (in-memory only; Phase 3.1
	// wires it to the provider).
	CtrlT key.Binding
}

// DefaultKeyMap returns the standard keybindings. Per spec §9.3.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "submit"),
		),
		Escape: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel/clear/quit"),
		),
		CtrlD: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "quit if empty"),
		),
		Backspace: key.NewBinding(
			key.WithKeys("backspace"),
			key.WithHelp("backspace", "delete"),
		),
		Up: key.NewBinding(
			key.WithKeys("up"),
			key.WithHelp("↑", "history prev"),
		),
		Down: key.NewBinding(
			key.WithKeys("down"),
			key.WithHelp("↓", "history next"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup"),
			key.WithHelp("pgup", "scroll up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown"),
			key.WithHelp("pgdown", "scroll down"),
		),
		CtrlQuestion: key.NewBinding(
			key.WithKeys("ctrl+?"),
			key.WithHelp("ctrl+?", "help"),
		),
		CtrlT: key.NewBinding(
			key.WithKeys("ctrl+t"),
			key.WithHelp("ctrl+t", "toggle thinking"),
		),
	}
}

// ShortHelp returns the keybindings shown in the help overlay's
// short view (the single-line status bar hint). Only the most
// important bindings.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Enter, k.Escape, k.CtrlD, k.CtrlQuestion}
}

// FullHelp returns all keybindings for the /help overlay.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Enter, k.Escape, k.CtrlD, k.Backspace},
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.CtrlQuestion, k.CtrlT},
	}
}
