package commands

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/core"
)

// filepathSeparator keeps the corrupt-file path cross-platform
// without importing both path/filepath and runtime just to test
// what goes through writeFile.
var filepathSeparator = string(filepath.Separator)

// withTempHome sets USERPROFILE (Windows) and HOME (Unix) to a
// fresh temp dir for the duration of the test, returning the
// dir. The session persistence helpers consult os.UserHomeDir,
// which honors USERPROFILE on Windows and HOME on Unix; tests
// set both so they pass cross-platform.
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

// saveStub writes a session with the given id and two known
// messages so tests can assert the loaded conversation. The
// first-message text incorporates `id` so tests can verify they
// loaded the right session even when multiple stubs share the
// same shape.
func saveStub(t *testing.T, id string) {
	t.Helper()
	now := time.Now().Add(-1 * time.Hour).UTC()
	s := &core.ConversationSession{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		Model:     core.DefaultModel,
		Messages: []core.Message{
			core.NewUserText("user-" + id),
			core.NewAssistantText("assistant-" + id),
		},
	}
	if err := core.SaveSession(s); err != nil {
		t.Fatalf("SaveSession(%q): %v", id, err)
	}
}

// ---------------------------------------------------------------------------
// /resume (no arg) → OpenPicker
// ---------------------------------------------------------------------------

// TestResume_NoArgsOpensPicker verifies that `/resume` with no
// argument asks the TUI to open the sessions picker. The TUI
// owns the rest of the flow (filter → enter → re-dispatch
// `/resume <id>` via dispatchCommand), so the command layer's
// only job here is to carry the discriminator.
func TestResume_NoArgsOpensPicker(t *testing.T) {
	withTempHome(t)
	res := ExecuteCommand(context.Background(), "/resume", testCtx())
	if res == nil {
		t.Fatal("/resume with no args returned nil")
	}
	if res.Kind != ResultOpenPicker {
		t.Fatalf("Kind = %v, want ResultOpenPicker", res.Kind)
	}
	if res.PickerKind != "session-resume" {
		t.Errorf("PickerKind = %q, want %q", res.PickerKind, "session-resume")
	}
}

// ---------------------------------------------------------------------------
// /resume <id>
// ---------------------------------------------------------------------------

// TestResume_ByID_Success loads a saved session and swaps the
// current Messages slice via ResultSetMessages. The TUI's
// ResultSetMessages handler applies the swap atomically; this
// test verifies the command produces the right payload.
func TestResume_ByID_Success(t *testing.T) {
	withTempHome(t)
	const id = "by-id-success"
	saveStub(t, id)
	res := ExecuteCommand(context.Background(), "/resume "+id, testCtx())
	if res == nil {
		t.Fatal("/resume <id> returned nil")
	}
	if res.Kind != ResultSetMessages {
		t.Fatalf("Kind = %v, want ResultSetMessages", res.Kind)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(res.Messages))
	}
	if res.Messages[0].Role != core.RoleUser ||
		res.Messages[0].GetFirstText() != "user-"+id {
		t.Errorf("Messages[0] = %+v, want user text %q", res.Messages[0], "user-"+id)
	}
	if res.Messages[1].Role != core.RoleAssistant ||
		res.Messages[1].GetFirstText() != "assistant-"+id {
		t.Errorf("Messages[1] = %+v, want assistant text %q", res.Messages[1], "assistant-"+id)
	}
}

// TestResume_ByID_NotFound returns a friendly error when the
// id does not match any saved session. The current session is
// unaffected because errors flow through ResultError. We only
// assert "load" appears in the message — Windows error strings
// embed the full absolute path which makes an id substring
// check fragile across platforms.
func TestResume_ByID_NotFound(t *testing.T) {
	withTempHome(t)
	res := ExecuteCommand(context.Background(), "/resume nonexistent-id", testCtx())
	if res == nil || res.Kind != ResultError {
		t.Fatalf("Kind = %v, want ResultError", res.Kind)
	}
	if !strings.Contains(res.Text, "load") {
		t.Errorf("error should mention 'load', got: %s", res.Text)
	}
}

// TestResume_ByID_CorruptReturnsError writes malformed JSON
// to a session file; LoadSession surfaces a parse error and
// resume converts it into ResultError. exercises the file-
// exists-but-unreadable path of loadAndReturn.
func TestResume_ByID_CorruptReturnsError(t *testing.T) {
	withTempHome(t)
	dir, err := core.ConversationsPath()
	if err != nil {
		t.Fatalf("ConversationsPath: %v", err)
	}
	path := dir + string(filepathSeparator) + "corrupt.json"
	if err := writeFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	res := ExecuteCommand(context.Background(), "/resume corrupt", testCtx())
	if res == nil || res.Kind != ResultError {
		t.Fatalf("Kind = %v, want ResultError", res.Kind)
	}
	if !strings.Contains(res.Text, "parse") && !strings.Contains(res.Text, "resume") {
		t.Errorf("error should mention parse or resume, got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /resume <n> (1-based index)
// ---------------------------------------------------------------------------

// TestResume_ByIndex_Success resolves a 1-based index against
// ListSessions and swaps in the loaded conversation. The save
// order (with staggered UpdatedAt) is what makes "1" point at
// "alpha": ListSessions sorts by UpdatedAt descending.
func TestResume_ByIndex_Success(t *testing.T) {
	withTempHome(t)
	saveStub(t, "alpha")
	time.Sleep(2 * time.Millisecond)
	saveStub(t, "beta")
	// beta is the most recent → index 1.
	res := ExecuteCommand(context.Background(), "/resume 1", testCtx())
	if res == nil || res.Kind != ResultSetMessages {
		t.Fatalf("/resume 1: kind = %v, want ResultSetMessages", res.Kind)
	}
	// The picker and the index lookup must agree on order.
	// We verify that by loading index 1 and 2 and ensuring the
	// message slices come from distinct sessions.
	first := res.Messages
	// Tiny gap so the next SaveSession's UpdatedAt is strictly
	// later — without it, fast clocks can produce equal
	// timestamps and ListSessions' sort is unstable.
	time.Sleep(20 * time.Millisecond)
	res = ExecuteCommand(context.Background(), "/resume 2", testCtx())
	if res == nil || res.Kind != ResultSetMessages {
		t.Fatalf("/resume 2: kind = %v, want ResultSetMessages", res.Kind)
	}
	second := res.Messages
	if reflect.DeepEqual(first, second) {
		t.Errorf("resume 1 and resume 2 returned the same messages; index lookup is order-broken")
	}
}

// TestResume_ByIndex_OutOfRange rejects an n that exceeds the
// list length, with a friendly error message.
func TestResume_ByIndex_OutOfRange(t *testing.T) {
	withTempHome(t)
	saveStub(t, "only-one")
	res := ExecuteCommand(context.Background(), "/resume 5", testCtx())
	if res == nil || res.Kind != ResultError {
		t.Fatalf("Kind = %v, want ResultError", res.Kind)
	}
	if !strings.Contains(res.Text, "out of range") {
		t.Errorf("error should say 'out of range', got: %s", res.Text)
	}
}

// TestResume_ByIndex_EmptyList rejects n=1 with a clean error
// when no sessions exist, instead of an obscure file-open
// failure.
func TestResume_ByIndex_EmptyList(t *testing.T) {
	withTempHome(t)
	res := ExecuteCommand(context.Background(), "/resume 1", testCtx())
	if res == nil || res.Kind != ResultError {
		t.Fatalf("Kind = %v, want ResultError", res.Kind)
	}
	// Either an "out of range" or "load" error is acceptable
	// — what we really want is a ResultError, not a panic or a
	// silent nil.
	if !strings.Contains(res.Text, "range") && !strings.Contains(res.Text, "load") {
		t.Errorf("error should mention range or load, got: %s", res.Text)
	}
}

// TestResume_ByIndex_NegativeOrZero is rejected — the index
// input is treated as a literal id only when it's non-empty
// AND non-numeric; "0", "-1", "+1" all reach the index branch
// because isAllDigits allows '-'/'+'.
func TestResume_ByIndex_NegativeRejected(t *testing.T) {
	withTempHome(t)
	saveStub(t, "alpha")
	for _, bad := range []string{"0", "-1", "+1"} {
		res := ExecuteCommand(context.Background(), "/resume "+bad, testCtx())
		if res == nil || res.Kind != ResultError {
			t.Errorf("/resume %s: Kind = %v, want ResultError", bad, res.Kind)
		}
	}
}

// ---------------------------------------------------------------------------
// /help listing covers the new command
// ---------------------------------------------------------------------------

func TestResume_InHelpListing(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/help", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/help should return a message")
	}
	if !strings.Contains(res.Text, "/resume") {
		t.Errorf("/help listing should mention /resume, got:\n%s", res.Text)
	}
}
