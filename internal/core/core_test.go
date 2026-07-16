package core

import (
	"errors"
	"net/http"
	"testing"
)

func TestErrorIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want bool
	}{
		{"nil error", nil, false},
		{"plain API error", New(KindAPI, "boom"), false},
		{"rate limit with retry-after", &Error{Kind: KindRateLimit, RetryAfter: 30}, true},
		{"rate limit without retry-after", New(KindRateLimit, "slow down"), true},
		{"HTTP 429", &Error{Kind: KindHTTPStatus, StatusCode: http.StatusTooManyRequests}, true},
		{"HTTP 529 (overloaded)", &Error{Kind: KindHTTPStatus, StatusCode: 529}, true},
		{"HTTP 500", &Error{Kind: KindHTTPStatus, StatusCode: 500}, false},
		{"HTTP 401", &Error{Kind: KindHTTPStatus, StatusCode: 401}, false},
		{"context limit", New(KindContextLimit, "too big"), false},
		{"auth", New(KindAuth, "no key"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.IsRetryable(); got != tc.want {
				t.Errorf("IsRetryable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestErrorIsContextLimit(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want bool
	}{
		{"nil", nil, false},
		{"context limit", New(KindContextLimit, "x"), true},
		{"max tokens", New(KindMaxTokens, "x"), true},
		{"rate limit", New(KindRateLimit, "x"), false},
		{"auth", New(KindAuth, "x"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.IsContextLimit(); got != tc.want {
				t.Errorf("IsContextLimit() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestErrorWrapPreservesKind(t *testing.T) {
	// Wrapping an *Error: the inner (root-cause) kind wins.
	original := New(KindAuth, "no api key")
	wrapped := Wrap(KindTool, original, "Bash tool failed")

	if wrapped.Kind != KindAuth {
		t.Errorf("Wrap overwrote the root-cause kind: got %v, want %v", wrapped.Kind, KindAuth)
	}
	if !contains(wrapped.Message, "Bash tool failed") {
		t.Errorf("wrapped message lost the wrapping context: %q", wrapped.Message)
	}
	// Unwrap should still return the original cause (nil here).
	if errors.Unwrap(wrapped) != nil {
		t.Errorf("Unwrap should return the original cause, got %v", errors.Unwrap(wrapped))
	}

	// Wrapping a plain (non-*Error) error: the kind argument is used.
	stdErr := errors.New("disk full")
	wrapped2 := Wrap(KindIO, stdErr, "Write tool")
	if wrapped2.Kind != KindIO {
		t.Errorf("Wrap with non-*Error should use the kind argument: got %v, want %v",
			wrapped2.Kind, KindIO)
	}
	if !errors.Is(wrapped2, stdErr) {
		t.Errorf("errors.Is should find the underlying cause")
	}
}

func TestErrorUnwrapChain(t *testing.T) {
	cause := errors.New("disk full")
	wrapped := Wrap(KindIO, cause, "Write tool")
	if !errors.Is(wrapped, cause) {
		t.Errorf("errors.Is should find the underlying cause through *Error wrap")
	}
}

func TestAutoPermissionHandlerMatrix(t *testing.T) {
	tests := []struct {
		name    string
		mode    PermissionMode
		readOnly bool
		want    PermissionDecision
	}{
		{"bypass everything", PermissionBypassPermissions, false, DecisionAllow},
		{"bypass read-only", PermissionBypassPermissions, true, DecisionAllow},
		{"accept-edits writes", PermissionAcceptEdits, false, DecisionAllow},
		{"accept-edits read-only", PermissionAcceptEdits, true, DecisionAllow},
		{"plan denies writes", PermissionPlan, false, DecisionDeny},
		{"plan allows reads", PermissionPlan, true, DecisionAllow},
		{"default denies writes", PermissionDefault, false, DecisionDeny},
		{"default allows reads", PermissionDefault, true, DecisionAllow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &AutoPermissionHandler{Mode: tc.mode}
			got := h.RequestPermission(PermissionRequest{
				ToolName:   "Bash",
				IsReadOnly: tc.readOnly,
			})
			if got != tc.want {
				t.Errorf("RequestPermission(mode=%v, readOnly=%v) = %v, want %v",
					tc.mode, tc.readOnly, got, tc.want)
			}
		})
	}
}

func TestMessageAccessors(t *testing.T) {
	// Plain string message.
	m := NewUserText("hello")
	if m.GetFirstText() != "hello" {
		t.Errorf("GetFirstText = %q, want %q", m.GetFirstText(), "hello")
	}
	if m.AllText() != "hello" {
		t.Errorf("AllText = %q, want %q", m.AllText(), "hello")
	}
	if m.HasToolUse() {
		t.Errorf("plain text message should not have tool use")
	}
	if len(m.ToolUses()) != 0 {
		t.Errorf("plain text message should have zero tool uses")
	}

	// Multi-block assistant message with text + thinking + tool use.
	m = NewAssistantBlocks(
		ThinkingBlock("let me think...", "sig-abc"),
		TextBlock("I'll use the read tool."),
		ContentBlock{
			Kind:    BlockToolUse,
			ToolUse: &ToolUse{ID: "tu_1", Name: "Read", Input: []byte(`{"path":"x"}`)},
		},
	)
	if m.GetFirstText() != "I'll use the read tool." {
		t.Errorf("GetFirstText on multi-block = %q, want %q", m.GetFirstText(), "I'll use the read tool.")
	}
	if !m.HasToolUse() {
		t.Errorf("multi-block with tool use should report HasToolUse")
	}
	tus := m.ToolUses()
	if len(tus) != 1 || tus[0].Name != "Read" {
		t.Errorf("ToolUses = %+v, want one Read tool use", tus)
	}
	think := m.ThinkingBlocks()
	if len(think) != 1 || think[0].Text != "let me think..." {
		t.Errorf("ThinkingBlocks = %+v, want one thinking block", think)
	}
}

func TestUsageMath(t *testing.T) {
	u := UsageInfo{
		InputTokens:              100,
		OutputTokens:             50,
		CacheCreationInputTokens: 20,
		CacheReadInputTokens:     30,
	}
	if got := u.TotalInput(); got != 150 {
		t.Errorf("TotalInput = %d, want 150", got)
	}
	if got := u.Total(); got != 200 {
		t.Errorf("Total = %d, want 200", got)
	}
}

func TestConfigEffectiveDefaults(t *testing.T) {
	// Empty config falls back to the defaults.
	c := &Config{}
	if got := c.EffectiveModel(); got != DefaultModel {
		t.Errorf("EffectiveModel = %q, want %q", got, DefaultModel)
	}
	if got := c.EffectiveMaxTokens(); got != DefaultMaxTokens {
		t.Errorf("EffectiveMaxTokens = %d, want %d", got, DefaultMaxTokens)
	}
	if got := c.EffectiveMaxTurns(); got != DefaultMaxTurns {
		t.Errorf("EffectiveMaxTurns = %d, want %d", got, DefaultMaxTurns)
	}

	// Explicit values are respected.
	c = &Config{Model: "claude-opus-4-7", MaxTokens: 16384, MaxTurns: 25}
	if got := c.EffectiveModel(); got != "claude-opus-4-7" {
		t.Errorf("EffectiveModel = %q, want %q", got, "claude-opus-4-7")
	}
	if got := c.EffectiveMaxTokens(); got != 16384 {
		t.Errorf("EffectiveMaxTokens = %d, want 16384", got)
	}
	if got := c.EffectiveMaxTurns(); got != 25 {
		t.Errorf("EffectiveMaxTurns = %d, want 25", got)
	}

	// Runaway values are clamped to the hard ceiling.
	c = &Config{MaxTokens: 1_000_000}
	if got := c.EffectiveMaxTokens(); got != MaxTokensHardCeiling {
		t.Errorf("EffectiveMaxTokens = %d, want %d (hard ceiling)", got, MaxTokensHardCeiling)
	}
}

func TestParsePermissionMode(t *testing.T) {
	cases := map[string]PermissionMode{
		"default":             PermissionDefault,
		"accept-edits":        PermissionAcceptEdits,
		"bypass-permissions":  PermissionBypassPermissions,
		"plan":                PermissionPlan,
	}
	for s, want := range cases {
		got, ok := ParsePermissionMode(s)
		if !ok {
			t.Errorf("ParsePermissionMode(%q) = !ok", s)
			continue
		}
		if got != want {
			t.Errorf("ParsePermissionMode(%q) = %v, want %v", s, got, want)
		}
	}
	if _, ok := ParsePermissionMode("nonsense"); ok {
		t.Errorf("ParsePermissionMode(\"nonsense\") should fail")
	}
}

func TestParseOutputFormat(t *testing.T) {
	cases := map[string]OutputFormat{
		"text":        OutputText,
		"json":        OutputJson,
		"stream-json": OutputStreamJson,
	}
	for s, want := range cases {
		got, ok := ParseOutputFormat(s)
		if !ok {
			t.Errorf("ParseOutputFormat(%q) = !ok", s)
			continue
		}
		if got != want {
			t.Errorf("ParseOutputFormat(%q) = %v, want %v", s, got, want)
		}
	}
	if _, ok := ParseOutputFormat("yaml"); ok {
		t.Errorf("ParseOutputFormat(\"yaml\") should fail")
	}
}

// contains is a tiny helper to keep the test file stdlib-only.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
