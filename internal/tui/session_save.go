package tui

import (
	"fmt"
	"log/slog"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/core"
)

// saveSessionOnExit writes the conversation to disk on a
// graceful TUI exit. Called by RunProgram after p.Run() returns
// and before the terminal is restored, so any error message we
// print is still legible.
//
// Behavior:
//   - If the final model is nil (panic path before Init), skip.
//   - If the model is not a *Model (defensive — bubbletea may
//     wrap it for some Cmd paths), skip. In practice it always
//     is, because NewProgram was given the Model directly.
//   - If the message history is empty, skip. A blank TUI
//     session is not worth a file.
//   - On any save error, log to stderr. The TUI is gone; the
//     user has no other channel. We do NOT fail the exit.
//
// The save is synchronous. Phase 3.1 may move it off the exit
// path if slow disks become a complaint; for now the spec asks
// for "save on graceful Exit or quit command" and synchronous
// is the simplest interpretation.
func saveSessionOnExit(cfg *core.Config, logger *slog.Logger, m tea.Model) {
	if m == nil {
		return
	}
	mm, ok := m.(*Model)
	if !ok || mm == nil {
		return
	}
	// Snapshot the shared messages under the mutex. The
	// pointer to shared is stable across Model copies
	// (bubbletea copies Model on every Update, but the
	// *sharedState field points to the same struct), so this
	// lock sees the latest state.
	msgs, unlock := mm.shared.lockMessages()
	defer unlock()
	if len(*msgs) == 0 {
		return
	}
	// Copy the slice so future mutations to mm.shared.messages
	// (e.g. /clear) don't affect the on-disk file. We use
	// append-to-nil to allocate a new backing array.
	snapshot := append([]core.Message(nil), *msgs...)

	id, err := core.NewSessionID()
	if err != nil {
		fmt.Fprintln(os.Stderr, "forge: session id:", err)
		if logger != nil {
			logger.Error("session id", "err", err)
		}
		return
	}
	s := &core.ConversationSession{
		ID:         id,
		Messages:   snapshot,
		Model:      cfg.EffectiveModel(),
		WorkingDir: cfg.WorkingDir,
	}
	if err := core.SaveSession(s); err != nil {
		fmt.Fprintln(os.Stderr, "forge: save session:", err)
		if logger != nil {
			logger.Error("save session", "err", err)
		}
		return
	}
	if logger != nil {
		logger.Info("session saved", "id", id, "messages", len(snapshot))
	}
}
