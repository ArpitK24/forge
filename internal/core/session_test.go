package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// withTempHome sets HOME / USERPROFILE to a temp dir for the
// duration of the test, returning the dir so tests can poke at
// the layout. os.UserHomeDir honors USERPROFILE on Windows and
// HOME on Unix; tests use runtime.GOOS to pick the right one
// (and set the other to a sane value too, so the same test
// passes cross-platform).
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

// makeSampleSession returns a ConversationSession with a known
// id and two messages. Useful for round-trip tests.
func makeSampleSession(id string) *ConversationSession {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	return &ConversationSession{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		Model:     DefaultModel,
		Messages: []Message{
			NewUserText("hello"),
			NewAssistantText("hi there"),
		},
		WorkingDir: "/tmp/proj",
		Title:      "greeting",
	}
}

// ---------------------------------------------------------------------------
// NewSessionID
// ---------------------------------------------------------------------------

// TestNewSessionID_Format asserts the string matches the
// canonical UUID v4 layout: 8-4-4-4-12 lowercase hex with the
// version (4) and variant (8/9/a/b) nibbles in the right slots.
func TestNewSessionID_Format(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(id) {
		t.Errorf("NewSessionID = %q, does not match UUID v4 regex", id)
	}
}

// TestNewSessionID_Unique sanity-checks the random source: 1000
// ids should be all distinct. (Collisions are possible in
// principle but vanishingly unlikely with 122 bits.)
func TestNewSessionID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id, err := NewSessionID()
		if err != nil {
			t.Fatalf("NewSessionID #%d: %v", i, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewSessionID returned duplicate %q on iteration %d", id, i)
		}
		seen[id] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// ConversationsPath
// ---------------------------------------------------------------------------

func TestConversationsPath_CreatesDir(t *testing.T) {
	home := withTempHome(t)
	path, err := ConversationsPath()
	if err != nil {
		t.Fatalf("ConversationsPath: %v", err)
	}
	want := filepath.Join(home, ConfigDirName, ConversationsDir)
	if path != want {
		t.Errorf("ConversationsPath = %q, want %q", path, want)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created dir: %v", err)
	}
	if !stat.IsDir() {
		t.Errorf("%s is not a directory", path)
	}
}

// ---------------------------------------------------------------------------
// SaveSession + LoadSession round-trip
// ---------------------------------------------------------------------------

func TestSession_RoundTrip(t *testing.T) {
	withTempHome(t)
	// Use a past timestamp so Save's UpdateAt = time.Now()
	// produces a strictly different value — this makes the
	// SaveSession mutation visible and proves we are
	// comparing the loaded record, not the mutated input.
	past := time.Now().Add(-1 * time.Hour).UTC()
	original := &ConversationSession{
		ID:         "test-roundtrip-1",
		CreatedAt:  past,
		UpdatedAt:  past,
		Model:      DefaultModel,
		Messages:   []Message{NewUserText("hello"), NewAssistantText("hi there")},
		WorkingDir: "/tmp/proj",
		Title:      "greeting",
	}
	if err := SaveSession(original); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	// After Save, original.UpdatedAt has been bumped to
	// roughly time.Now(). The on-disk record matches that
	// mutated state.
	loaded, err := LoadSession("test-roundtrip-1")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	// JSON round-trips wall time only — strip the monotonic
	// clock suffix on the in-memory UpdatedAt before comparing.
	// Also normalize CreatedAt: time.Time has an unexported
	// *Location pointer that DeepEqual compares by identity.
	// After a JSON round-trip the decoded time may carry a
	// different *Location instance even though the visible
	// wall+offset is identical. Compare RFC 3339 strings of
	// the wall time so location-pointer identity doesn't
	// matter; leave UpdatedAt on time.Time (mutated in place
	// by SaveSession) and compare via .Equal().
	got := *loaded
	got.UpdatedAt = loaded.UpdatedAt.Round(0)
	want := *original
	want.UpdatedAt = original.UpdatedAt.Round(0)
	if !reflect.DeepEqual(want.Messages, got.Messages) {
		t.Errorf("messages mismatch:\n  want: %+v\n  got:  %+v", want.Messages, got.Messages)
	}
	if want.ID != got.ID || want.Model != got.Model ||
		want.WorkingDir != got.WorkingDir || want.Title != got.Title {
		t.Errorf("scalar fields mismatch:\n  want: %+v\n  got:  %+v", &want, &got)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("created_at mismatch: want %v, got %v", want.CreatedAt, got.CreatedAt)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("updated_at mismatch: want %v, got %v", want.UpdatedAt, got.UpdatedAt)
	}
	// And the mutation is what we expected: the in-memory
	// UpdatedAt is now later than what we passed in.
	if !original.UpdatedAt.After(past) {
		t.Errorf("SaveSession did not advance UpdatedAt: still %v", original.UpdatedAt)
	}
}

// TestSession_SaveUpdatesUpdatedAt verifies that calling
// SaveSession twice on the same logical session bumps
// UpdatedAt but preserves CreatedAt.
func TestSession_SaveUpdatesUpdatedAt(t *testing.T) {
	withTempHome(t)
	// Use a past timestamp so the first save's UpdatedAt is
	// strictly later than CreatedAt (not equal, which would
	// make the After() check trivially true on a zero
	// CreatedAt).
	past := time.Now().Add(-1 * time.Hour).UTC()
	s := &ConversationSession{
		ID:        "test-resave",
		CreatedAt: past,
		UpdatedAt: past,
		Model:     DefaultModel,
		Messages:  []Message{NewUserText("hi")},
	}
	if err := SaveSession(s); err != nil {
		t.Fatalf("first SaveSession: %v", err)
	}
	firstUpdated := s.UpdatedAt
	// Sleep just enough that the second UpdatedAt is strictly later.
	time.Sleep(2 * time.Millisecond)
	if err := SaveSession(s); err != nil {
		t.Fatalf("second SaveSession: %v", err)
	}
	if !s.UpdatedAt.After(firstUpdated) {
		t.Errorf("UpdatedAt did not advance: first=%v, second=%v", firstUpdated, s.UpdatedAt)
	}
	if !s.CreatedAt.Equal(past) {
		t.Errorf("CreatedAt changed: was %v, now %v", past, s.CreatedAt)
	}
}

// TestSession_SaveRejectsMissingID rejects empty/blank IDs.
func TestSession_SaveRejectsMissingID(t *testing.T) {
	withTempHome(t)
	for _, id := range []string{"", " ", "\t\n"} {
		s := makeSampleSession(id)
		if err := SaveSession(s); err == nil {
			t.Errorf("SaveSession with id=%q: expected error, got nil", id)
		}
	}
}

// TestSession_LoadMissing returns a wrapped os.ErrNotExist for
// an unknown id (not a generic "not found" string).
func TestSession_LoadMissing(t *testing.T) {
	withTempHome(t)
	_, err := LoadSession("nonexistent")
	if err == nil {
		t.Fatal("LoadSession for missing id: expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("LoadSession error should wrap os.ErrNotExist, got: %v", err)
	}
}

// TestSession_LoadMissingID rejects empty input without
// touching the filesystem.
func TestSession_LoadMissingID(t *testing.T) {
	withTempHome(t)
	if _, err := LoadSession(""); err == nil {
		t.Error("LoadSession with empty id: expected error, got nil")
	}
}

// TestSession_FileModeIs600 verifies the on-disk file is
// 0600. (Conversation history can be sensitive — keystrokes
// the user typed, plus model responses that may include
// private code.)
func TestSession_FileModeIs600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not enforced on Windows")
	}
	withTempHome(t)
	s := makeSampleSession("test-mode")
	if err := SaveSession(s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	dir, _ := ConversationsPath()
	path := filepath.Join(dir, "test-mode.json")
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	mode := stat.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("session file mode = %o, want 0600", mode)
	}
}

// TestSession_LoadCorruptReturnsError writes a malformed JSON
// file and expects LoadSession to return a parse error.
func TestSession_LoadCorruptReturnsError(t *testing.T) {
	withTempHome(t)
	dir, _ := ConversationsPath()
	if err := os.WriteFile(filepath.Join(dir, "corrupt.json"),
		[]byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	_, err := LoadSession("corrupt")
	if err == nil {
		t.Fatal("LoadSession on corrupt file: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("LoadSession error should mention parse, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListSessions
// ---------------------------------------------------------------------------

// TestListSessions_SortedByUpdatedAtDesc saves three sessions
// with staggered timestamps, then asserts the list comes back
// in reverse-creation order.
func TestListSessions_SortedByUpdatedAtDesc(t *testing.T) {
	withTempHome(t)
	a := makeSampleSession("a-oldest")
	b := makeSampleSession("b-middle")
	c := makeSampleSession("c-newest")

	if err := SaveSession(a); err != nil {
		t.Fatalf("save a: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := SaveSession(b); err != nil {
		t.Fatalf("save b: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := SaveSession(c); err != nil {
		t.Fatalf("save c: %v", err)
	}

	list, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	if list[0].ID != "c-newest" || list[1].ID != "b-middle" || list[2].ID != "a-oldest" {
		t.Errorf("order = [%s, %s, %s], want [c-newest, b-middle, a-oldest]",
			list[0].ID, list[1].ID, list[2].ID)
	}
}

// TestListSessions_EmptyDir returns (nil, nil) for a fresh
// install. The directory is created on first call to
// ConversationsPath; with no files, the list is empty.
func TestListSessions_EmptyDir(t *testing.T) {
	withTempHome(t)
	list, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("len = %d, want 0", len(list))
	}
}

// TestListSessions_SkipsCorruptFiles confirms ListSessions
// does not bail on a single bad file. One corrupt file
// alongside two good ones → list of two, no error.
func TestListSessions_SkipsCorruptFiles(t *testing.T) {
	withTempHome(t)
	if err := SaveSession(makeSampleSession("good-1")); err != nil {
		t.Fatalf("save good-1: %v", err)
	}
	dir, _ := ConversationsPath()
	if err := os.WriteFile(filepath.Join(dir, "broken.json"),
		[]byte("not json at all"), 0o600); err != nil {
		t.Fatalf("write broken: %v", err)
	}
	if err := SaveSession(makeSampleSession("good-2")); err != nil {
		t.Fatalf("save good-2: %v", err)
	}
	list, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("len = %d, want 2 (corrupt file should be skipped)", len(list))
	}
}

// ---------------------------------------------------------------------------
// DeleteSession
// ---------------------------------------------------------------------------

// TestDeleteSession_RemovesFile covers the happy path.
func TestDeleteSession_RemovesFile(t *testing.T) {
	withTempHome(t)
	if err := SaveSession(makeSampleSession("to-delete")); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := DeleteSession("to-delete"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	dir, _ := ConversationsPath()
	if _, err := os.Stat(filepath.Join(dir, "to-delete.json")); !os.IsNotExist(err) {
		t.Errorf("file should be gone, stat err = %v", err)
	}
}

// TestDeleteSession_Idempotent — calling delete twice is OK.
func TestDeleteSession_Idempotent(t *testing.T) {
	withTempHome(t)
	if err := DeleteSession("never-existed"); err != nil {
		t.Errorf("first delete: %v", err)
	}
	if err := DeleteSession("never-existed"); err != nil {
		t.Errorf("second delete should be a no-op, got: %v", err)
	}
}

// TestDeleteSession_EmptyID rejects blank input.
func TestDeleteSession_EmptyID(t *testing.T) {
	withTempHome(t)
	if err := DeleteSession(""); err == nil {
		t.Error("DeleteSession with empty id: expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// JSON shape
// ---------------------------------------------------------------------------

// TestSession_JSONShape pins the on-disk JSON field names. A
// future /resume command will read these; if they change,
// that command breaks. This is the only "schema test" we
// have.
func TestSession_JSONShape(t *testing.T) {
	withTempHome(t)
	s := makeSampleSession("schema-check")
	if err := SaveSession(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	dir, _ := ConversationsPath()
	data, err := os.ReadFile(filepath.Join(dir, "schema-check.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "created_at", "updated_at", "messages", "model", "working_dir", "title"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("JSON missing required key %q", key)
		}
	}
}

// ---------------------------------------------------------------------------
// DeriveTitle / truncateTitle
// ---------------------------------------------------------------------------

// TestDeriveTitle_Short: a short first user message is
// returned unchanged (after whitespace collapse).
func TestDeriveTitle_Short(t *testing.T) {
	got := DeriveTitle([]Message{
		NewUserText("hello"),
		NewAssistantText("hi there"),
	})
	if got != "hello" {
		t.Errorf("DeriveTitle = %q, want %q", got, "hello")
	}
}

// TestDeriveTitle_WhitespaceCollapse: tabs and newlines and
// runs of spaces collapse into single spaces.
func TestDeriveTitle_WhitespaceCollapse(t *testing.T) {
	got := DeriveTitle([]Message{
		NewUserText("hello\n\n   world\t\tfoo  bar"),
	})
	if got != "hello world foo bar" {
		t.Errorf("DeriveTitle = %q, want %q", got, "hello world foo bar")
	}
}

// TestDeriveTitle_TruncatesAt60: a long first user message
// truncates to 59 runes + "…".
func TestDeriveTitle_TruncatesAt60(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := DeriveTitle([]Message{NewUserText(long)})
	if want := strings.Repeat("a", 59) + "…"; got != want {
		t.Errorf("len(DeriveTitle) = %d, want %d runes; got %q", len([]rune(got)), 60, got)
	}
}

// TestDeriveTitle_Exactly60: at the cap, no ellipsis.
func TestDeriveTitle_Exactly60(t *testing.T) {
	exact := strings.Repeat("b", 60)
	got := DeriveTitle([]Message{NewUserText(exact)})
	if got != exact {
		t.Errorf("DeriveTitle at cap = %q, want %q (no ellipsis)", got, exact)
	}
}

// TestDeriveTitle_NoUserMessages: an assistant-only session
// has no title.
func TestDeriveTitle_NoUserMessages(t *testing.T) {
	got := DeriveTitle([]Message{
		NewAssistantText("hello"),
	})
	if got != "" {
		t.Errorf("DeriveTitle = %q, want \"\"", got)
	}
}

// TestDeriveTitle_OnlyWhitespaceFirstUserMsg: a whitespace-
// only first user message returns "".
func TestDeriveTitle_OnlyWhitespaceFirstUserMsg(t *testing.T) {
	got := DeriveTitle([]Message{
		NewUserText("   \n\t  "),
	})
	if got != "" {
		t.Errorf("DeriveTitle = %q, want \"\"", got)
	}
}

// TestDeriveTitle_FirstEmptyUserThenNonEmpty: if the first
// user message is empty/whitespace, the next non-empty one is
// used.
func TestDeriveTitle_FirstEmptyUserThenNonEmpty(t *testing.T) {
	got := DeriveTitle([]Message{
		NewUserText(""),
		NewUserText("   "),
		NewUserText("real prompt"),
	})
	if got != "real prompt" {
		t.Errorf("DeriveTitle = %q, want %q", got, "real prompt")
	}
}

// TestDeriveTitle_MultiByteUTF8: rune-counted; emoji and
// CJK characters count as 1, no broken sequences.
func TestDeriveTitle_MultiByteUTF8(t *testing.T) {
	// 61 runes total. emoji "🎉" is one rune (4 bytes).
	emojis := strings.Repeat("🎉", 61)
	got := truncateTitle(emojis, 60)
	want := strings.Repeat("🎉", 59) + "…"
	if got != want {
		t.Errorf("truncateTitle multi-byte: got %q, want %q", got, want)
	}
	// Verify the result really is <= 60 runes.
	if n := len([]rune(got)); n != 60 {
		t.Errorf("truncateTitle rune count = %d, want 60", n)
	}
}

// TestTruncateTitle_Boundaries: under-cap unchanged, at-cap
// unchanged, over-cap truncated with ellipsis.
func TestTruncateTitle_Boundaries(t *testing.T) {
	if got := truncateTitle("hello", 60); got != "hello" {
		t.Errorf("under-cap: got %q, want %q", got, "hello")
	}
	if got := truncateTitle(strings.Repeat("x", 60), 60); got != strings.Repeat("x", 60) {
		t.Errorf("at-cap: should be unchanged")
	}
	if got := truncateTitle(strings.Repeat("x", 100), 60); got != strings.Repeat("x", 59)+"…" {
		t.Errorf("over-cap: should be 59 runes + ellipsis, got %q", got)
	}
	// runeCap=1 returns just the ellipsis; we don't truncate
	// to zero runes (would be a useless empty string).
	if got := truncateTitle("anything", 1); got != "…" {
		t.Errorf("runeCap=1: got %q, want \"…\"", got)
	}
	// runeCap<=0 returns empty. Defensive; shouldn't happen
	// in practice because DeriveTitle uses 60.
	if got := truncateTitle("anything", 0); got != "" {
		t.Errorf("runeCap<=0: got %q, want \"\"", got)
	}
}

// TestListSessions_BackfillsEmptyTitle: a session saved with
// Title == "" gets a derived title when ListSessions runs.
// Disk file is NOT rewritten until the next save.
func TestListSessions_BackfillsEmptyTitle(t *testing.T) {
	withTempHome(t)
	s := &ConversationSession{
		ID:    "backfill-test",
		Title: "", // intentionally empty
		Model: DefaultModel,
		Messages: []Message{
			NewUserText("derive me"),
		},
	}
	if err := SaveSession(s); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Sanity: disk file does NOT contain a "title" key (the
	// field uses `omitempty` so an empty Title is omitted from
	// the JSON — not encoded as "title": "").
	dir, _ := ConversationsPath()
	data, _ := os.ReadFile(filepath.Join(dir, "backfill-test.json"))
	if strings.Contains(string(data), `"title"`) {
		t.Fatalf("disk should not contain \"title\" key (omitempty); got: %s", data)
	}

	list, err := ListSessions()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list len = %d, want 1", len(list))
	}
	if list[0].Title != "derive me" {
		t.Errorf("backfilled title = %q, want %q", list[0].Title, "derive me")
	}

	// Disk file still has no "title" key — backfill is virtual.
	data2, _ := os.ReadFile(filepath.Join(dir, "backfill-test.json"))
	if strings.Contains(string(data2), `"title"`) {
		t.Errorf("disk should still have no title key (no rewrite), got: %s", data2)
	}
}
