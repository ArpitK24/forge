package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View renders the current model state as a string of terminal
// output. This is bubbletea's pure render function — given the
// model, produce the visible output. The runtime diffs this
// against the previous frame.
//
// Layout (spec §9.4): vertically split into scrollable message
// pane → bordered multi-line input → one-line status bar.
//
// Step 5: when a permission dialog is open, it overlays the
// base layout. The user confirmed the freeze-everything design,
// so the dialog is the only visible interaction surface while
// it's up. The base layout is still rendered (so the message
// pane doesn't appear to vanish), but lipgloss.Place draws the
// dialog on top of it.
func (m Model) View() string {
	if m.Quitting {
		return ""
	}

	// 1. Render the message pane.
	var content strings.Builder
	for _, msg := range m.Messages {
		content.WriteString(renderMessage(msg))
		content.WriteByte('\n')
	}
	// If streaming, append the in-flight buffer with a cursor.
	if m.Streaming && m.StreamBuffer != "" {
		content.WriteString(m.StreamBuffer)
		content.WriteString(streamingCursor)
		content.WriteByte('\n')
	} else if m.Streaming {
		content.WriteString(streamingCursor)
		content.WriteByte('\n')
	}

	m.Viewport.SetContent(content.String())

	// 2. Render the input box.
	input := inputBorderStyle.Render(m.Input.View())

	// 3. Render the status line.
	status := m.renderStatus()

	// 4. Assemble vertically.
	var b strings.Builder
	b.WriteString(m.Viewport.View())
	b.WriteByte('\n')
	b.WriteString(input)
	b.WriteByte('\n')
	b.WriteString(status)

	base := b.String()

	// 5. Overlay the help or permission dialog if visible. The
	// permission dialog takes priority: if both are up (which
	// shouldn't happen in practice, but is possible if a help
	// overlay was open when a tool call needed permission), the
	// dialog wins because it's the time-sensitive surface.
	if m.PermissionDialog != nil {
		return lipgloss.Place(m.Width, m.Height,
			lipgloss.Center, lipgloss.Center,
			m.renderPermissionDialog())
	}
	if m.HelpVisible {
		return lipgloss.Place(m.Width, m.Height,
			lipgloss.Center, lipgloss.Center,
			m.renderHelpOverlay())
	}
	return base
}

// renderMessage formats a single message for display in the
// message pane.
func renderMessage(msg renderedMessage) string {
	switch msg.Role {
	case "user":
		return userMsgStyle.Render("▸ You: ") + msg.Text
	case "assistant":
		text := msg.Text
		if msg.IsStreaming {
			text = text + streamingCursor
		}
		return assistantMsgStyle.Render(text)
	case "system":
		return systemMsgStyle.Render("  " + msg.Text)
	case "error":
		return errorMsgStyle.Render("  ⚠ " + msg.Text)
	case "tool":
		return toolMsgStyle.Render("  ◆ " + msg.Text)
	default:
		return msg.Text
	}
}

// renderStatus builds the one-line status bar showing the active
// model and a running cost summary. Per spec §9.4.
func (m Model) renderStatus() string {
	left := fmt.Sprintf(" %s ", m.effectiveModel())
	right := ""
	if m.Cost != nil {
		s := m.Cost.Summary()
		if s != "" {
			right = " " + s + " "
		}
	}
	if m.Status != "" {
		right = m.Status + "   " + right
	}
	if right == "" {
		return statusStyle.Render(left)
	}

	// Pad left side to fill the width.
	totalLen := lipgloss.Width(left) + lipgloss.Width(right)
	if totalLen < m.Width {
		gap := m.Width - totalLen
		left = left + strings.Repeat(" ", gap)
	}
	return statusStyle.Render(left + right)
}

// renderHelpOverlay draws the help keybinding overlay centered
// on the screen.
func (m Model) renderHelpOverlay() string {
	km := DefaultKeyMap()
	content := "Forge — Keybindings\n\n"
	for _, group := range km.FullHelp() {
		for _, b := range group {
			content += fmt.Sprintf("  %-16s %s\n", b.Keys(), b.Help())
		}
		content += "\n"
	}
	content += "Press Esc or ctrl+? to close.\n"

	overlay := helpOverlayStyle.Render(content)
	return lipgloss.Place(m.Width, m.Height, lipgloss.Center, lipgloss.Center, overlay)
}
