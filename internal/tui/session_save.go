package tui

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/core"
)

// asModel recovers the TUI Model from a tea.Model. bubbletea's
// p.Run() returns the Model VALUE that Update produced; tests and
// other callers may pass a *Model. Both are accepted.
func asModel(tm tea.Model) (*Model, bool) {
	switch v := tm.(type) {
	case Model:
		return &v, true
	case *Model:
		return v, v != nil
	}
	return nil, false
}

// saveFinalModel saves the session for the model p.Run()
// returned: a --resume run overwrites its loaded record in place,
// anything else mints a fresh id.
func saveFinalModel(cfg *core.Config, logger *slog.Logger, final tea.Model) {
	if fm, ok := asModel(final); ok && fm.LoadedSessionID != "" {
		saveResumedSessionOnExit(cfg, logger, final)
		return
	}
	saveSessionOnExit(cfg, logger, final)
}

// saveSessionOnExit writes the conversation to disk on a
// graceful TUI exit. Called by RunProgram after p.Run() returns
// and before the terminal is restored, so any error message we
// print is still legible.
//
// Behavior:
//   - If the final model is nil (panic path before Init), skip.
//   - If the model is neither a Model nor a *Model, skip.
//   - If the message history is empty, skip. A blank TUI
//     session is not worth a file.
//   - On any save error, log to stderr. The TUI is gone; the
//     user has no other channel. We do NOT fail the exit.
//
// The save is synchronous. Phase 3.1 may move it off the exit
// path if slow disks become a complaint; for now the spec asks
// for "save on graceful Exit or quit command" and synchronous
// is the simplest interpretation.
//
// Title is derived from the first user message via
// core.DeriveTitle so the sessions browser shows a short
// human-readable label. Mid-session /resume and fresh sessions
// both go through this path; --resume callers use
// saveResumedSessionOnExit instead so the on-disk id is
// preserved.
func saveSessionOnExit(cfg *core.Config, logger *slog.Logger, m tea.Model) {
	if m == nil {
		return
	}
	mm, ok := asModel(m)
	if !ok {
		return
	}
	// Snapshot the live history (a copy, so later mutations
	// don't affect the on-disk file).
	snapshot := mm.historySnapshot()
	if len(snapshot) == 0 {
		return
	}

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
		Title:      core.DeriveTitle(snapshot),
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

// saveResumedSessionOnExit is the --resume variant of
// saveSessionOnExit. Instead of minting a fresh UUID, it
// overwrites the on-disk record that was loaded into this
// TUI run, preserving the id so the user can `--resume
// <same-id>` again later to keep iterating.
//
// Behavior:
//   - Same nil/empty guards as saveSessionOnExit.
//   - Reuses mm.LoadedSessionID for the on-disk id. If the
//     value is empty for any reason (defensive — RunProgram
//     should never call this path in that case), falls back
//     to minting a fresh id rather than writing to "" (which
//     SaveSession rejects).
//   - Title is re-derived from the live history, so a
//     resumed session whose first user message changed gets
//     an updated label. SaveSession preserves CreatedAt
//     because the load-then-save cycle reads the original
//     CreatedAt off disk and SaveSession only sets it on a
//     zero value — see core.SaveSession for the contract.
func saveResumedSessionOnExit(cfg *core.Config, logger *slog.Logger, m tea.Model) {
	if m == nil {
		return
	}
	mm, ok := asModel(m)
	if !ok {
		return
	}
	snapshot := mm.historySnapshot()
	if len(snapshot) == 0 {
		return
	}

	id := mm.LoadedSessionID
	if id == "" {
		// Defensive: shouldn't happen — RunProgram routes
		// only via LoadedSessionID != "". Fall back to a
		// fresh id so we don't refuse to save.
		var err error
		id, err = core.NewSessionID()
		if err != nil {
			fmt.Fprintln(os.Stderr, "forge: session id:", err)
			if logger != nil {
				logger.Error("session id", "err", err)
			}
			return
		}
	}

	// Pre-load the on-disk record so CreatedAt survives the
	// rewrite. SaveSession's "only set CreatedAt when zero"
	// guard would otherwise reset it because the in-memory
	// struct's CreatedAt is the zero value here.
	// Missing or corrupt file: start fresh. The user will see
	// the new entry with a "now" CreatedAt rather than the
	// original; that's an acceptable fallback when the source
	// is gone. (LoadSession returns nil on error.)
	var createdAt time.Time
	if loaded, err := core.LoadSession(id); err == nil && loaded != nil {
		createdAt = loaded.CreatedAt
	}

	s := &core.ConversationSession{
		ID:         id,
		CreatedAt:  createdAt,
		Messages:   snapshot,
		Model:      cfg.EffectiveModel(),
		WorkingDir: cfg.WorkingDir,
		Title:      core.DeriveTitle(snapshot),
	}
	if err := core.SaveSession(s); err != nil {
		fmt.Fprintln(os.Stderr, "forge: save session:", err)
		if logger != nil {
			logger.Error("save session", "err", err)
		}
		return
	}
	if logger != nil {
		logger.Info("session saved (resumed)", "id", id, "messages", len(snapshot))
	}
}
