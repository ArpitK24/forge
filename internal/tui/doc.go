// Package tui implements the interactive terminal UI for
// Forge: a full-screen REPL with streaming model output,
// slash commands, an in-line input box, and an interactive
// permission dialog for non-read-only tool calls.
//
// # Architecture
//
// The TUI is built on the bubbletea Elm-style architecture
// (github.com/charmbracelet/bubbletea). Every visible widget
// is a pure function of the program's Model state; the
// bubbletea runtime diffs the rendered output against the
// previous frame and emits only the minimal escape-sequence
// delta. This matches spec §9.1's "immediate-mode rendering
// approach" — we never emit more raw terminal output than
// the visible delta requires.
//
// The Model owns:
//
//   - the rendered message list (assistant + user + tool calls),
//   - the multi-line input buffer (bubbles/textarea),
//   - input history with up/down recall,
//   - a scroll offset for the message pane,
//   - a streaming flag + in-flight text buffer,
//   - the active Config and the shared CostTracker,
//   - permission-dialog state when a tool needs approval,
//   - a help-overlay-visible flag.
//
// # Dependency choice
//
// Phase 3 standardizes on the charmbracelet stack:
//
//   - bubbletea  — the runtime + model/view/update loop.
//   - bubbles    — textinput/textarea/viewport/list/spinner/help.
//   - lipgloss   — declarative styling.
//
// Rationale: bubbletea is the de-facto standard Go TUI
// framework, has first-class Windows support (uses
// golang.org/x/term under the hood for raw mode), and the
// Elm architecture maps 1:1 onto the spec's "App state"
// requirements in §9.2. It is pure Go, so the CGO_ENABLED=0
// static build (§0 / §15) still produces a single 6-7 MB
// binary. We do NOT pull in tcell, gocui, or termui —
// bubbletea is enough.
//
// We deliberately do NOT pull in:
//
//   - A web/browser-based UI dependency — Phase 3 is
//     terminal-only.
//   - A syntax-highlighting dependency — the diff viewer
//     (Phase 3.1) will need one and will be added then.
//
// # Module dependency graph
//
// After Phase 3 the dependency graph is strictly
// one-directional (spec §1):
//
//	cli → query → tools → core
//	  ↓       ↓
//	commands core
//	  ↓
//	 tui  →  core
//
// `tui` and `commands` both depend on `core` (for types like
// Config, CostTracker, Message) and on `query` (read-only —
// we drive the loop, we don't duplicate it). Neither tui
// nor commands depends on the other. Neither depends on
// `tools` directly — the TUI's permission dialog receives
// the tool's name and description via the event channel.
package tui

// This file is the package's documentation anchor. The
// implementation lives in:
//
//	model.go         — the bubbletea Model struct + state
//	keymap.go        — central keybinding registry (spec §9.3)
//	layout.go        — vertical split (messages / input / status)
//	render.go        — pure View() function
//	update.go        — bubbletea Update() method
//	program.go       — RunProgram() entry point + loop driver
//	term.go          — raw-mode + alt-screen setup/teardown
//	history.go       — input-history persistence
//	perm_dialog.go   — the interactive permission dialog
//	perm_dialog_test.go
