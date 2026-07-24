package tui

import "github.com/charmbracelet/lipgloss"

// Layout dimensions and style constants. These control the
// vertical split of the terminal: message pane → input → status.
//
// The layout is computed dynamically from the terminal width and
// height on every resize. The constants below are minimums.
const (
	// minInputHeight is the minimum rows for the input box.
	minInputHeight = 3
	// statusHeight is the fixed height (rows) for the status bar.
	statusHeight = 1
	// borderHeight accounts for the top+bottom border of the
	// input box.
	borderHeight = 2
)

// Styles are the lipgloss styles used across the TUI. Declared
// at package level so they're allocated once, not on every frame.
var (
	// statusStyle is the one-line status bar at the bottom.
	statusStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7c7c7c"))

	// inputBorderStyle is the border around the multi-line input box.
	inputBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#555555"))

	// userMsgStyle is for user messages in the message pane.
	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#5afdff")).
			Bold(true)

	// assistantMsgStyle is for assistant messages.
	assistantMsgStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#ffffff"))

	// systemMsgStyle is for system/status messages (/help, /cost, etc.).
	systemMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			Italic(true)

	// errorMsgStyle is for error messages.
	errorMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ff5555"))

	// toolMsgStyle is for tool-start/tool-end messages.
	toolMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d7a000"))

	// streamingCursor is the blinking cursor shown while the
	// assistant is streaming.
	streamingCursor = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			Render("▊")
)

// helpOverlayStyle is the full-screen help overlay.
var helpOverlayStyle = lipgloss.NewStyle().
	Border(lipgloss.DoubleBorder()).
	BorderForeground(lipgloss.Color("#666666")).
	Padding(1, 2)

// --- Permission dialog styles (Step 5) ---

// dialogOverlayStyle wraps the modal body. The border is amber
// (matches toolMsgStyle) so the dialog reads as a system-driven
// prompt rather than a regular message. Width is fixed so the
// dialog doesn't reflow as the button row grows.
var dialogOverlayStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("#d7a000")).
	Padding(1, 3).
	Width(64)

// dialogTitleStyle is the headline of the dialog ("Permission
// needed"). Bold + amber to match the border.
var dialogTitleStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#d7a000"))

// dialogToolNameStyle is the "Tool: Bash" line. The tool name
// gets a brighter foreground so the user knows at a glance which
// tool is asking.
var dialogToolNameStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#5afdff"))

// dialogBodyStyle is the "Choose how to handle this call:" line
// and other secondary text.
var dialogBodyStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#cccccc"))

// dialogCommandStyle is the verbatim Bash command block. Italic
// + dim to make it visually distinct from the prose description
// above it.
var dialogCommandStyle = lipgloss.NewStyle().
	Italic(true).
	Foreground(lipgloss.Color("#aaaaaa"))

// dialogHintStyle is the keyboard hint footer.
var dialogHintStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#888888")).
	Italic(true)

// dialogBtnFocused is the focused button: black on amber, bold.
// Matches the border so the user sees a single amber "selection"
// in the dialog.
var dialogBtnFocused = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("#000000")).
	Background(lipgloss.Color("#d7a000")).
	Padding(0, 1)

// dialogBtnUnfocused is the unfocused button: grey foreground,
// no background. Padded the same as focused so the row doesn't
// shift when focus moves.
var dialogBtnUnfocused = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#888888")).
	Padding(0, 1)

// computeLayout updates the model's viewport and textarea
// dimensions based on the current terminal size. Called on every
// tea.WindowSizeMsg.
func (m *Model) computeLayout() {
	// Input height: at least minInputHeight, up to 40% of the
	// terminal height.
	inputH := minInputHeight
	maxInputH := m.Height / 5
	if maxInputH > 10 {
		maxInputH = 10
	}
	if maxInputH > minInputHeight {
		inputH = maxInputH
	}
	// Status bar + input (with borders) + message pane.
	messageH := m.Height - inputH - borderHeight - statusHeight
	if messageH < 1 {
		messageH = 1
	}

	m.Viewport.Width = m.Width
	m.Viewport.Height = messageH
	m.Input.SetWidth(m.Width - 4) // inside the border
	m.Input.SetHeight(inputH)
}
