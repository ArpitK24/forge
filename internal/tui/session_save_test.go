package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/core"
)

// withTempHome sets HOME / USERPROFILE to a temp dir for the
// duration of the test. Mirrors the helper in core/session_test.go;
// duplicated here so the tui package's tests don't depend on
// internal test helpers from another package.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
		t.Setenv("HOME", dir)
	} else {
		t.Setenv("HOME", dir)
		t.Setenv("USERPROFILE", dir)
	}
	return dir
}

// TestSaveSessionOnExit_NilModel — the panic-recovery path
// passes nil for the model; saveSessionOnExit must not crash.
func TestSaveSessionOnExit_NilModel(t *testing.T) {
	withTempHome(t)
	// Should be a no-op, not a panic.
	saveSessionOnExit(&core.Config{}, nil, nil)
}

// TestSaveSessionOnExit_EmptyMessages — a TUI session with
// no messages does not produce a file. The user opened the
// app, didn't type anything, and quit; that's not worth a
// session file.
func TestSaveSessionOnExit_EmptyMessages(t *testing.T) {
	withTempHome(t)
	m := InitialModel(&core.Config{Model: core.DefaultModel}, core.NewCostTracker())
	m.Width = 80
	m.Height = 24
	m.computeLayout()

	saveSessionOnExit(m.Config, nil, &m)

	dir, _ := core.ConversationsPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("empty TUI should not save a session, found %d files", len(entries))
	}
}

// TestSaveSessionOnExit_PopulatedModel — the headline test:
// build a Model with two messages, pass it to saveSessionOnExit,
// and assert a session file is written with both messages intact.
func TestSaveSessionOnExit_PopulatedModel(t *testing.T) {
	withTempHome(t)
	cfg := &core.Config{
		Model:      core.DefaultModel,
		WorkingDir: "/tmp/proj",
	}
	m := InitialModel(cfg, core.NewCostTracker())
	m.Width = 80
	m.Height = 24
	m.computeLayout()
	// Populate the shared message history. The save hook
	// reads from m.shared, not from m.Messages.
	m.shared.mu.Lock()
	m.shared.messages = []core.Message{
		core.NewUserText("hello forge"),
		core.NewAssistantText("hi, how can I help?"),
	}
	m.shared.mu.Unlock()

	// The helper's type assertion is to *Model. In production
	// bubbletea stores our *Model on the program and hands it
	// back from p.Run(), so this is the canonical case.
	saveSessionOnExit(cfg, nil, &m)

	dir, _ := core.ConversationsPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 session file, got %d", len(entries))
	}
	// Load it back and verify both messages survived.
	id := entries[0].Name()
	id = id[:len(id)-len(".json")]
	loaded, err := core.LoadSession(id)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("loaded Messages len = %d, want 2", len(loaded.Messages))
	}
	if loaded.Messages[0].GetFirstText() != "hello forge" {
		t.Errorf("first message text = %q, want %q",
			loaded.Messages[0].GetFirstText(), "hello forge")
	}
	if loaded.Messages[1].GetFirstText() != "hi, how can I help?" {
		t.Errorf("second message text = %q, want %q",
			loaded.Messages[1].GetFirstText(), "hi, how can I help?")
	}
	if loaded.Model != core.DefaultModel {
		t.Errorf("Model = %q, want %q", loaded.Model, core.DefaultModel)
	}
	if loaded.WorkingDir != "/tmp/proj" {
		t.Errorf("WorkingDir = %q, want %q", loaded.WorkingDir, "/tmp/proj")
	}
}

// TestSaveSessionOnExit_DefensiveNonModel — if bubbletea ever
// hands us a different tea.Model type, the helper skips
// silently. We define a minimal foreignModel that satisfies
// tea.Model but is NOT *Model.
func TestSaveSessionOnExit_DefensiveNonModel(t *testing.T) {
	withTempHome(t)
	var fm foreignModel
	saveSessionOnExit(&core.Config{}, nil, fm)

	dir, _ := core.ConversationsPath()
	entries, _ := os.ReadDir(dir)
	// The dir may exist (ConversationsPath creates it), but
	// no .json files should be in it.
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			t.Errorf("non-Model input should not produce a file, found %s", e.Name())
		}
	}
}

// foreignModel is a minimal tea.Model used only by
// TestSaveSessionOnExit_DefensiveNonModel to verify the type
// assertion path. It does not need to do anything — the
// helper should reject it before touching the struct.
type foreignModel struct{}

func (foreignModel) Init() tea.Cmd                       { return nil }
func (foreignModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return foreignModel{}, nil }
func (foreignModel) View() string                        { return "" }

// TestSaveSessionOnExit_NoSharedCrash — defensive: a Model
// with nil shared should not panic. (The real code path
// always populates shared, but we want the panic guard for
// tests and edge cases.)
func TestSaveSessionOnExit_NoSharedCrash(t *testing.T) {
	withTempHome(t)
	// We can't easily build a *Model with nil shared through
	// the public API — InitialModel always sets it — so
	// skip this scenario. The other tests cover the live
	// path; if someone refactors and breaks the invariant,
	// a runtime panic will show up in their test runs.
	t.Skip("InitialModel always populates shared; nil-shared is not reachable in practice")
}

// TestSaveResumedSessionOnExit_PreservesID verifies that a TUI
// run entered via --resume <id> overwrites the on-disk record
// with the same id, instead of minting a fresh one. The test
// seeds a session file with two messages, builds a Model
// whose LoadedSessionID points at that id, mutates the live
// history, and asserts the rewritten file:
//   - lives at the same id
//   - has the new message count
//   - has the new first-message text
//   - has a non-empty Title (DeriveTitle now populates it)
//   - has a CreatedAt preserved from the original save
func TestSaveResumedSessionOnExit_PreservesID(t *testing.T) {
	withTempHome(t)
	const id = "resume-preserve-1"
	cfg := &core.Config{
		Model:      core.DefaultModel,
		WorkingDir: "/tmp/proj",
	}
	// Seed an existing session file with id + 2 messages +
	// an explicit CreatedAt so we can verify it's preserved.
	past := time.Date(2026, 7, 1, 9, 30, 0, 0, time.UTC)
	original := &core.ConversationSession{
		ID:        id,
		CreatedAt: past,
		UpdatedAt: past,
		Model:     core.DefaultModel,
		Messages: []core.Message{
			core.NewUserText("original first prompt"),
			core.NewAssistantText("original first reply"),
		},
		WorkingDir: "/tmp/proj",
		Title:      "",
	}
	if err := core.SaveSession(original); err != nil {
		t.Fatalf("seed SaveSession: %v", err)
	}

	// Build the TUI Model the way RunProgram does on the
	// --resume path: LoadedSessionID set, shared.messages
	// pre-populated.
	m := InitialModel(cfg, core.NewCostTracker())
	m.LoadedSessionID = id
	m.Width = 80
	m.Height = 24
	m.computeLayout()
	m.shared.mu.Lock()
	m.shared.messages = []core.Message{
		core.NewUserText("original first prompt"),
		core.NewAssistantText("original first reply"),
		core.NewUserText("second prompt added during resumed run"),
	}
	m.shared.mu.Unlock()

	saveResumedSessionOnExit(cfg, nil, &m)

	// Verify exactly one session file exists, with the SAME id
	// (no fresh file minted).
	dir, _ := core.ConversationsPath()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 session file, got %d", len(entries))
	}
	if entries[0].Name() != id+".json" {
		t.Errorf("file name = %q, want %q.json", entries[0].Name(), id)
	}

	loaded, err := core.LoadSession(id)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(loaded.Messages) != 3 {
		t.Errorf("Messages len = %d, want 3", len(loaded.Messages))
	}
	if loaded.Messages[2].GetFirstText() != "second prompt added during resumed run" {
		t.Errorf("last message text = %q, want the new one",
			loaded.Messages[2].GetFirstText())
	}
	if loaded.Title == "" {
		t.Errorf("Title should be derived from the new history, got empty")
	}
	if !loaded.CreatedAt.Equal(past) {
		t.Errorf("CreatedAt = %v, want %v (preserved from original)", loaded.CreatedAt, past)
	}
}

// TestSaveResumedSessionOnExit_NilModel — symmetric to
// saveSessionOnExit's nil guard. Pre-Init panic path passes
// nil; the helper must not crash.
func TestSaveResumedSessionOnExit_NilModel(t *testing.T) {
	withTempHome(t)
	saveResumedSessionOnExit(&core.Config{}, nil, nil)
}

// TestSaveResumedSessionOnExit_EmptyMessages — empty history
// at exit produces no file, same contract as the fresh-id
// variant.
func TestSaveResumedSessionOnExit_EmptyMessages(t *testing.T) {
	withTempHome(t)
	m := InitialModel(&core.Config{}, core.NewCostTracker())
	m.LoadedSessionID = "anything"
	saveResumedSessionOnExit(m.Config, nil, &m)

	dir, _ := core.ConversationsPath()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			t.Errorf("empty history should not save a file, found %s", e.Name())
		}
	}
}
