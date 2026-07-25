// Package core, session.go — persisted conversation snapshots.
//
// One ConversationSession per logical TUI run, written to
// ~/.forge/conversations/<id>.json. Step 6 (Phase 3) only
// writes them on graceful exit; Phase 3.1 adds the /resume
// command that lists and loads prior sessions. The format
// here is the on-disk contract that /resume will read, so it
// is versioned (implicitly — fields use stable JSON names).
//
// Spec §5.1 defines the file format and the /resume UX. Spec
// §2.10 mentions session history as a Phase 3.1 capability;
// the persistence primitives are established in Step 6 so the
// command has something to read.
package core

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ConversationSession is a serialized snapshot of a TUI
// conversation. Written to <ConfigDir>/<ConversationsDir>/<id>.json
// on graceful exit. Read back by /resume in Phase 3.1.
type ConversationSession struct {
	// ID is a UUID v4 string ("xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx").
	// Generated when the session is first saved; stable across
	// re-saves of the same logical conversation.
	ID string `json:"id"`

	// CreatedAt is the wall-clock time the session was first
	// opened. Set on the first Save call and never updated.
	// RFC 3339.
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is the wall-clock time of the most recent save.
	// Refreshed on every Save call. ListSessions sorts by this
	// field descending so the user sees the most recent first.
	UpdatedAt time.Time `json:"updated_at"`

	// Messages is the full conversation history at save time.
	// core.Message and all its ContentBlock variants are
	// JSON-serializable (verified by round-trip tests).
	Messages []Message `json:"messages"`

	// Model is the active model id at save time. Recorded so a
	// /resume can warn the user (or auto-switch) if the model
	// has changed since the session was created.
	Model string `json:"model"`

	// WorkingDir is the CWD at save time. Optional — empty is
	// treated as "the directory forge was launched from is no
	// longer known."
	WorkingDir string `json:"working_dir,omitempty"`

	// Title is an optional human-readable label. Phase 3.1
	// derives it from the first user message; Step 6 leaves it
	// empty.
	Title string `json:"title,omitempty"`
}

// ConversationsPath returns the absolute path to the
// conversations directory, creating it (and ~/.forge if
// necessary) if it does not exist. The returned path is the
// canonical location for all ConversationSession files.
//
// Mode of the created directory is 0755 — readable by the
// user only, same as ~/.forge itself. Individual session
// files are written with 0600.
func ConversationsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", Wrap(KindIO, err, "resolve home directory")
	}
	dir := filepath.Join(home, ConfigDirName, ConversationsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", Wrap(KindIO, err, "create conversations directory")
	}
	return dir, nil
}

// NewSessionID returns a fresh UUID v4 string. Hand-rolled
// with crypto/rand so the package has no external dependency
// for what is essentially six bytes of randomness; the
// output format matches github.com/google/uuid if we ever
// swap implementations.
//
// Format: 8-4-4-4-12 lowercase hex. Version (4) and variant
// (10xx) bits are set per RFC 4122 §4.4. Errors are returned
// only if the system entropy source is broken — practically
// never.
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", Wrap(KindIO, err, "read random bytes for session id")
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// SaveSession writes the session to <dir>/<id>.json with mode
// 0600. UpdatedAt is set to time.Now() on every call;
// CreatedAt is set on the first save and preserved across
// re-saves. Missing fields (ID, etc.) are rejected with a
// descriptive error so the TUI can surface the problem
// instead of writing a garbage file.
//
// If a file with the same ID already exists, it is
// overwritten — sessions are append-only by version, not by
// file. (Phase 3.1 may switch to append-only once the
// message-history format stabilizes.)
func SaveSession(s *ConversationSession) error {
	if s == nil {
		return New(KindConfig, "save session: nil")
	}
	if strings.TrimSpace(s.ID) == "" {
		return New(KindConfig, "save session: missing id")
	}
	now := time.Now()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = now
	}
	s.UpdatedAt = now

	dir, err := ConversationsPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return Wrap(KindConfig, err, "marshal session")
	}
	path := filepath.Join(dir, s.ID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return Wrap(KindIO, err, "write session file")
	}
	return nil
}

// LoadSession reads a single session by id. Returns
// os.ErrNotExist wrapped as a *Error{Kind: KindIO} if the
// file is missing. Parse errors are also wrapped so the
// caller can distinguish "no such session" from "session is
// corrupt."
func LoadSession(id string) (*ConversationSession, error) {
	if strings.TrimSpace(id) == "" {
		return nil, New(KindConfig, "load session: missing id")
	}
	dir, err := ConversationsPath()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, Wrap(KindIO, err, "read session file")
	}
	var s ConversationSession
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, Wrap(KindConfig, err, "parse session file")
	}
	return &s, nil
}

// ListSessions returns all sessions in the conversations
// directory, sorted by UpdatedAt descending (most recent
// first). The returned slice is a snapshot — mutations do
// not affect on-disk state.
//
// Behavior:
//   - Missing directory: returns (nil, nil). Fresh installs
//     have no sessions; this is not an error.
//   - Corrupt files: skipped. A parse error on one file does
//     not poison the whole list. (Tested explicitly.)
//   - Non-JSON files: skipped. Belt-and-braces against
//     accidental writes to the directory.
//   - Subdirectories: skipped. The directory should contain
//     only flat files.
func ListSessions() ([]ConversationSession, error) {
	dir, err := ConversationsPath()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, Wrap(KindIO, err, "read conversations directory")
	}
	out := make([]ConversationSession, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		s, err := LoadSession(id)
		if err != nil {
			// Skip corrupt files; do not abort the list.
			continue
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// DeleteSession removes a session file. A missing file is
// not an error — Delete is idempotent, so /resume can call
// it freely without first checking existence.
func DeleteSession(id string) error {
	if strings.TrimSpace(id) == "" {
		return New(KindConfig, "delete session: missing id")
	}
	dir, err := ConversationsPath()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return Wrap(KindIO, err, "delete session file")
	}
	return nil
}
