package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// testCtx returns a minimal CommandContext for tests. Nil-safe —
// commands must tolerate nil fields gracefully.
func testCtx(overrides ...func(*CommandContext)) *CommandContext {
	c := &CommandContext{
		Config: &core.Config{
			Model:          core.DefaultModel,
			PermissionMode: core.PermissionDefault,
			MaxTurns:       core.DefaultMaxTurns,
			AutoCompact:    true,
			ThinkingBudget: 0,
		},
		Cost:       core.NewCostTracker(),
		Messages:   nil,
		WorkingDir: ".",
	}
	for _, fn := range overrides {
		fn(c)
	}
	return c
}

// ---------------------------------------------------------------------------
// ExecuteCommand: pass-through cases
// ---------------------------------------------------------------------------

func TestExecuteCommand_NonSlashIsPassthrough(t *testing.T) {
	res := ExecuteCommand(context.Background(), "hello world", testCtx())
	if res != nil {
		t.Fatalf("expected nil (passthrough) for non-slash input, got %+v", res)
	}
}

func TestExecuteCommand_EmptyInputIsPassthrough(t *testing.T) {
	res := ExecuteCommand(context.Background(), "", testCtx())
	if res != nil {
		t.Fatalf("expected nil (passthrough) for empty input, got %+v", res)
	}
}

func TestExecuteCommand_BareSlashIsPassthrough(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/", testCtx())
	if res != nil {
		t.Fatalf("expected nil (passthrough) for bare slash, got %+v", res)
	}
}

func TestExecuteCommand_UnknownCommandIsPassthrough(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/nonexistent", testCtx())
	if res != nil {
		t.Fatalf("expected nil (passthrough) for unknown command, got %+v", res)
	}
}

func TestExecuteCommand_UnknownCommandWithArgsIsPassthrough(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/foobar some args", testCtx())
	if res != nil {
		t.Fatalf("expected nil (passthrough) for unknown command with args, got %+v", res)
	}
}

func TestExecuteCommand_NilContextStillDispatches(t *testing.T) {
	// Commands that don't need context (e.g. /version) should work
	// even with a nil CommandContext.
	res := ExecuteCommand(context.Background(), "/version", nil)
	if res == nil {
		t.Fatal("expected non-nil result for /version with nil context")
	}
	if res.Kind != ResultMessage {
		t.Fatalf("expected ResultMessage, got %v", res.Kind)
	}
	if !strings.Contains(res.Text, core.AppName) {
		t.Errorf("version output should contain app name, got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /help
// ---------------------------------------------------------------------------

func TestExecuteCommand_HelpListsCommands(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/help", testCtx())
	if res == nil {
		t.Fatal("expected non-nil for /help")
	}
	if res.Kind != ResultMessage {
		t.Fatalf("expected ResultMessage, got %v", res.Kind)
	}
	text := res.Text
	// The help listing should mention the mandatory baseline commands.
	for _, cmd := range []string{"help", "clear", "compact", "exit", "model", "version", "status"} {
		if !strings.Contains(text, "/"+cmd) {
			t.Errorf("/help listing should contain /%s, got:\n%s", cmd, text)
		}
	}
	// Hidden commands should NOT appear in the default listing.
	for _, hidden := range []string{"dump-config", "dump-history"} {
		if strings.Contains(text, "/"+hidden) {
			t.Errorf("/help listing should NOT contain hidden /%s", hidden)
		}
	}
}

func TestExecuteCommand_HelpSpecificCommand(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/help clear", testCtx())
	if res == nil {
		t.Fatal("expected non-nil for /help clear")
	}
	if res.Kind != ResultMessage {
		t.Fatalf("expected ResultMessage, got %v", res.Kind)
	}
	if !strings.Contains(res.Text, "/clear") {
		t.Errorf("help for /clear should contain /clear, got: %s", res.Text)
	}
}

func TestExecuteCommand_HelpUnknownCommand(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/help doesnotexist", testCtx())
	if res == nil {
		t.Fatal("expected non-nil for /help doesnotexist")
	}
	if res.Kind != ResultError {
		t.Fatalf("expected ResultError, got %v", res.Kind)
	}
}

// ---------------------------------------------------------------------------
// /clear
// ---------------------------------------------------------------------------

func TestExecuteCommand_ClearReturnsClearConversation(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/clear", testCtx())
	if res == nil {
		t.Fatal("expected non-nil for /clear")
	}
	if res.Kind != ResultClearConversation {
		t.Fatalf("expected ResultClearConversation, got %v", res.Kind)
	}
}

// ---------------------------------------------------------------------------
// /exit and /quit (alias)
// ---------------------------------------------------------------------------

func TestExecuteCommand_ExitReturnsExit(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/exit", testCtx())
	if res == nil {
		t.Fatal("expected non-nil for /exit")
	}
	if res.Kind != ResultExit {
		t.Fatalf("expected ResultExit, got %v", res.Kind)
	}
}

func TestExecuteCommand_QuitAliasWorks(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/quit", testCtx())
	if res == nil {
		t.Fatal("expected non-nil for /quit")
	}
	if res.Kind != ResultExit {
		t.Fatalf("expected ResultExit (quit is alias of exit), got %v", res.Kind)
	}
}

// ---------------------------------------------------------------------------
// /version
// ---------------------------------------------------------------------------

func TestExecuteCommand_VersionOutput(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/version", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/version should return a message")
	}
	if !strings.Contains(res.Text, core.AppName) {
		t.Errorf("version should contain app name %q, got: %s", core.AppName, res.Text)
	}
	if !strings.Contains(res.Text, core.AppVersion) {
		t.Errorf("version should contain version %q, got: %s", core.AppVersion, res.Text)
	}
}

func TestExecuteCommand_VersionAliasV(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/v", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/v should be an alias for /version and return a message")
	}
	if !strings.Contains(res.Text, core.AppName) {
		t.Errorf("/v output should contain app name, got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /model
// ---------------------------------------------------------------------------

func TestExecuteCommand_ModelShowsActive(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/model", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/model should return a message")
	}
	if !strings.Contains(res.Text, core.DefaultModel) {
		t.Errorf("/model should show default model %q, got: %s", core.DefaultModel, res.Text)
	}
}

func TestExecuteCommand_ModelSwitchIsError(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/model some-other-model", testCtx())
	if res == nil || res.Kind != ResultError {
		t.Fatal("/model <name> should return an error in Phase 3")
	}
	if !strings.Contains(res.Text, "not implemented") {
		t.Errorf("error should mention 'not implemented', got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /status
// ---------------------------------------------------------------------------

func TestExecuteCommand_StatusOutput(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/status", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/status should return a message")
	}
	text := res.Text
	for _, want := range []string{"Model:", "Messages:"} {
		if !strings.Contains(text, want) {
			t.Errorf("status should contain %q, got: %s", want, text)
		}
	}
}

// ---------------------------------------------------------------------------
// /cost
// ---------------------------------------------------------------------------

func TestExecuteCommand_CostWithNoUsage(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/cost", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/cost should return a message")
	}
	// Fresh cost tracker has no calls; Summary returns "0 turns".
	if !strings.Contains(res.Text, "0 turns") {
		t.Errorf("fresh /cost should say '0 turns', got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /config
// ---------------------------------------------------------------------------

func TestExecuteCommand_ConfigDumpsJSON(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/config", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/config should return a message")
	}
	if !strings.Contains(res.Text, `"model"`) {
		t.Errorf("config dump should contain 'model' key, got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /compact
// ---------------------------------------------------------------------------

func TestExecuteCommand_CompactStub(t *testing.T) {
	// With no messages, compact should report "nothing to compact".
	res := ExecuteCommand(context.Background(), "/compact", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/compact should return a message")
	}
	if !strings.Contains(res.Text, "Nothing to compact") {
		t.Errorf("compact with no messages should say nothing to compact, got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /thinking
// ---------------------------------------------------------------------------

func TestExecuteCommand_ThinkingOffByDefault(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/thinking", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/thinking should return a message")
	}
	if !strings.Contains(res.Text, "off") {
		t.Errorf("default thinking should be 'off', got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /permissions
// ---------------------------------------------------------------------------

func TestExecuteCommand_PermissionsOutput(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/permissions", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/permissions should return a message")
	}
	if !strings.Contains(res.Text, "default") {
		t.Errorf("permissions should show 'default' mode, got: %s", res.Text)
	}
}

// TestExecuteCommand_PermissionsSetMode verifies that
// /permissions <mode> flips the active mode and returns a
// ResultConfigChange so the TUI can swap the new config in.
func TestExecuteCommand_PermissionsSetMode(t *testing.T) {
	ctx := testCtx()
	if ctx.Config == nil {
		t.Fatal("testCtx returned nil Config")
	}
	if ctx.Config.PermissionMode != core.PermissionDefault {
		t.Fatalf("test setup: PermissionMode = %v, want Default",
			ctx.Config.PermissionMode)
	}

	res := ExecuteCommand(context.Background(), "/permissions accept-edits", ctx)
	if res == nil {
		t.Fatal("/permissions accept-edits returned nil")
	}
	if res.Kind != ResultConfigChange {
		t.Errorf("Kind = %v, want ResultConfigChange", res.Kind)
	}
	if res.Config == nil {
		t.Fatal("Config payload is nil")
	}
	if res.Config.PermissionMode != core.PermissionAcceptEdits {
		t.Errorf("PermissionMode = %v, want AcceptEdits",
			res.Config.PermissionMode)
	}
}

// TestExecuteCommand_PermissionsSetModeUnknown rejects bad input.
func TestExecuteCommand_PermissionsSetModeUnknown(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/permissions banana", testCtx())
	if res == nil || res.Kind != ResultError {
		t.Fatalf("Kind = %v, want ResultError", res.Kind)
	}
	if !strings.Contains(res.Text, "unknown mode") {
		t.Errorf("error should mention 'unknown mode', got: %s", res.Text)
	}
}

// TestExecuteCommand_PermissionsNoopOnSameMode returns a plain
// message (not ConfigChange) when the requested mode is already
// the active one. Avoids a needless Model swap.
func TestExecuteCommand_PermissionsNoopOnSameMode(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/permissions default", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatalf("Kind = %v, want ResultMessage (noop)", res.Kind)
	}
}

// ---------------------------------------------------------------------------
// /diff (stub)
// ---------------------------------------------------------------------------

func TestExecuteCommand_DiffStub(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/diff", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/diff should return a message")
	}
	if !strings.Contains(res.Text, "stub") {
		t.Errorf("diff should mention it's a stub, got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// /init
// ---------------------------------------------------------------------------

func TestExecuteCommand_InitCreatesForgeMD(t *testing.T) {
	dir := t.TempDir()
	res := ExecuteCommand(context.Background(), "/init", testCtx(func(c *CommandContext) {
		c.WorkingDir = dir
	}))
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/init should return a message")
	}
	if !strings.Contains(res.Text, "Created") {
		t.Errorf("/init should say 'Created', got: %s", res.Text)
	}
	// Verify the file actually exists.
	exists, err := fileExists(dir + string([]byte{'\\'}) + core.MemoryFileName)
	if err != nil {
		t.Fatalf("fileExists check: %v", err)
	}
	if !exists {
		t.Errorf("FORGE.md should exist in %s", dir)
	}
}

func TestExecuteCommand_InitIdempotent(t *testing.T) {
	dir := t.TempDir()
	// Create the file first.
	writeFile(dir+string([]byte{'\\'})+core.MemoryFileName, []byte("# existing"), 0o644)
	res := ExecuteCommand(context.Background(), "/init", testCtx(func(c *CommandContext) {
		c.WorkingDir = dir
	}))
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/init should return a message")
	}
	if !strings.Contains(res.Text, "already exists") {
		t.Errorf("/init on existing file should say 'already exists', got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// Hidden commands execute if invoked directly
// ---------------------------------------------------------------------------

func TestExecuteCommand_HiddenDumpConfig(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/dump-config", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/dump-config should return a message")
	}
	if !strings.Contains(res.Text, `"model"`) {
		t.Errorf("dump-config should contain JSON, got: %s", res.Text)
	}
}

func TestExecuteCommand_HiddenDumpHistory(t *testing.T) {
	res := ExecuteCommand(context.Background(), "/dump-history", testCtx())
	if res == nil || res.Kind != ResultMessage {
		t.Fatal("/dump-history should return a message")
	}
	// With no messages, should output "[]".
	if !strings.Contains(res.Text, "[]") {
		t.Errorf("dump-history with no messages should be [], got: %s", res.Text)
	}
}

// ---------------------------------------------------------------------------
// FindCommand
// ---------------------------------------------------------------------------

func TestFindCommand_ByName(t *testing.T) {
	c, ok := FindCommand("help")
	if !ok || c.Name() != "help" {
		t.Fatal("FindCommand should find 'help'")
	}
}

func TestFindCommand_CaseInsensitive(t *testing.T) {
	c, ok := FindCommand("HELP")
	if !ok || c.Name() != "help" {
		t.Fatal("FindCommand should find 'HELP' case-insensitively")
	}
}

func TestFindCommand_ByAlias(t *testing.T) {
	c, ok := FindCommand("quit")
	if !ok || c.Name() != "exit" {
		t.Fatal("FindCommand should resolve alias 'quit' to 'exit'")
	}
}

func TestFindCommand_UnknownReturnsFalse(t *testing.T) {
	_, ok := FindCommand("doesnotexist")
	if ok {
		t.Fatal("FindCommand should return false for unknown command")
	}
}

func TestFindCommand_EmptyReturnsFalse(t *testing.T) {
	_, ok := FindCommand("")
	if ok {
		t.Fatal("FindCommand should return false for empty string")
	}
}

// ---------------------------------------------------------------------------
// AllCommands
// ---------------------------------------------------------------------------

func TestAllCommands_IncludesBaseline(t *testing.T) {
	cmds := AllCommands()
	if len(cmds) == 0 {
		t.Fatal("AllCommands should return at least one command")
	}
	names := make(map[string]bool)
	for _, c := range cmds {
		names[c.Name()] = true
	}
	for _, want := range []string{"help", "clear", "exit", "version", "model", "status", "compact", "cost", "config"} {
		if !names[want] {
			t.Errorf("AllCommands should include %q", want)
		}
	}
}

// ---------------------------------------------------------------------------
// CommandResult constructors
// ---------------------------------------------------------------------------

func TestResultConstructors(t *testing.T) {
	tests := []struct {
		name string
		got  CommandResult
		want ResultKind
		text string // empty means don't check
	}{
		{"MessageResult", MessageResult("hi"), ResultMessage, "hi"},
		{"UserMessageResult", UserMessageResult("hi"), ResultUserMessage, "hi"},
		{"ErrorResult", ErrorResult("oops"), ResultError, "oops"},
		{"ExitResult", ExitResult(), ResultExit, ""},
		{"SilentResult", SilentResult(), ResultSilent, ""},
		{"ClearConversationResult", ClearConversationResult(), ResultClearConversation, ""},
	}
	for _, tc := range tests {
		if tc.got.Kind != tc.want {
			t.Errorf("%s: expected Kind %v, got %v", tc.name, tc.want, tc.got.Kind)
		}
		if tc.text != "" && tc.got.Text != tc.text {
			t.Errorf("%s: expected Text %q, got %q", tc.name, tc.text, tc.got.Text)
		}
	}
}

// ---------------------------------------------------------------------------
// ResultKind.String
// ---------------------------------------------------------------------------

func TestResultKindString(t *testing.T) {
	tests := []struct {
		kind ResultKind
		want string
	}{
		{ResultMessage, "message"},
		{ResultUserMessage, "user-message"},
		{ResultConfigChange, "config-change"},
		{ResultClearConversation, "clear-conversation"},
		{ResultSetMessages, "set-messages"},
		{ResultExit, "exit"},
		{ResultSilent, "silent"},
		{ResultError, "error"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("ResultKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
