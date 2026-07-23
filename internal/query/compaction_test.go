package query

import (
	"context"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

func TestCalculateTokenWarningState(t *testing.T) {
	cases := []struct {
		name   string
		used   int
		window int
		want   TokenWarningState
	}{
		{"ok at 50% used (large window)", 50_000, 100_000, TokenWarningOk},
		{"ok at 80% used", 80_000, 100_000, TokenWarningOk},
		{"warning at 85% used", 85_000, 100_000, TokenWarningWarning},
		{"ok just under warning threshold (remaining 20001)", 79_999, 100_000, TokenWarningOk},
		{"warning just over warning threshold (remaining 19999)", 80_001, 100_000, TokenWarningWarning},
		{"critical at 95% used", 95_000, 100_000, TokenWarningCritical},
		{"warning at exactly reserve boundary (remaining 13000)", 87_000, 100_000, TokenWarningWarning},
		{"critical just past reserve boundary (remaining 12999)", 87_001, 100_000, TokenWarningCritical},
		{"ok at 0 used", 0, 100_000, TokenWarningOk},
		{"ok at boundary - 1 from warning", 80_000, 100_000, TokenWarningOk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CalculateTokenWarningState(tc.used, tc.window); got != tc.want {
				t.Errorf("CalculateTokenWarningState(%d, %d) = %v, want %v",
					tc.used, tc.window, got, tc.want)
			}
		})
	}
}

func TestShouldAutoCompactTriggerFraction(t *testing.T) {
	cases := []struct {
		name     string
		used     int
		window   int
		failures int
		want     bool
	}{
		// Trigger fraction is 0.90, so on a 100k window, 90k+
		// used should fire; <90k should not.
		{"below trigger", 80_000, 100_000, 0, false},
		{"at trigger boundary - 1", 89_999, 100_000, 0, false},
		{"at trigger boundary", 90_000, 100_000, 0, true},
		{"above trigger", 95_000, 100_000, 0, true},
		{"at full", 100_000, 100_000, 0, true},
		{"zero window", 1, 0, 0, false},
		{"negative used", -1, 100_000, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldAutoCompact(tc.used, tc.window, tc.failures); got != tc.want {
				t.Errorf("ShouldAutoCompact(%d, %d, %d) = %v, want %v",
					tc.used, tc.window, tc.failures, got, tc.want)
			}
		})
	}
}

func TestShouldAutoCompactCircuitBreaker(t *testing.T) {
	// 3 consecutive failures → circuit breaker trips. The
	// spec says "tripped" means we never try again until the
	// user (or a successful compaction) resets the counter.
	const maxFailures = core.AutoCompactMaxConsecutiveFailures
	for n := 0; n < maxFailures; n++ {
		if !ShouldAutoCompact(95_000, 100_000, n) {
			t.Errorf("ShouldAutoCompact(failures=%d) = false, want true (under cap)", n)
		}
	}
	// At the cap, we should not trigger.
	if ShouldAutoCompact(95_000, 100_000, maxFailures) {
		t.Errorf("ShouldAutoCompact(failures=%d) = true, want false (circuit breaker)", maxFailures)
	}
	// And past the cap, still no.
	if ShouldAutoCompact(99_999, 100_000, maxFailures+5) {
		t.Errorf("ShouldAutoCompact(failures=%d) = true, want false (well past cap)", maxFailures+5)
	}
}

func TestCompactConversationStub(t *testing.T) {
	// Phase 2's CompactConversation is a no-op; it returns the
	// input messages unchanged with a nil error. We assert that
	// here so a future Phase 4 rewrite of the body doesn't
	// accidentally change this contract without breaking
	// the test.
	in := []core.Message{
		core.NewUserText("hello"),
		core.NewAssistantText("hi"),
	}
	out, err := CompactConversation(context.Background(), in, "fake-model")
	if err != nil {
		t.Errorf("CompactConversation err = %v, want nil", err)
	}
	if len(out) != len(in) {
		t.Errorf("CompactConversation length = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if in[i].GetFirstText() != out[i].GetFirstText() {
			t.Errorf("message %d changed: in=%q out=%q", i,
				in[i].GetFirstText(), out[i].GetFirstText())
		}
	}
}

func TestAutoCompactIfNeededNoMessages(t *testing.T) {
	// With no messages, the function should be a no-op and
	// return false.
	var msgs []core.Message
	got := AutoCompactIfNeeded(context.Background(), &msgs, "fake-model",
		core.UsageInfo{InputTokens: 999_999}, nil)
	if got {
		t.Errorf("AutoCompactIfNeeded with no messages = true, want false")
	}
}

func TestAutoCompactIfNeededBelowTriggerIsNoOp(t *testing.T) {
	// A small turn-usage below the trigger fraction → no
	// compaction.
	msgs := []core.Message{core.NewUserText("hi"), core.NewAssistantText("hello")}
	original := len(msgs)
	got := AutoCompactIfNeeded(context.Background(), &msgs, "fake-model",
		core.UsageInfo{InputTokens: 100}, nil)
	if got {
		t.Errorf("AutoCompactIfNeeded below trigger = true, want false")
	}
	if len(msgs) != original {
		t.Errorf("messages changed when no compaction should have fired: %d", len(msgs))
	}
}

func TestAutoCompactIfNeededAboveTrigger(t *testing.T) {
	// A very large turn-usage, way over the trigger. Phase 2's
	// CompactConversation is a no-op, so the function should
	// return true (we "did" compact) and the messages should
	// come back unchanged.
	msgs := []core.Message{core.NewUserText("hi"), core.NewAssistantText("hello")}
	got := AutoCompactIfNeeded(context.Background(), &msgs, "fake-model",
		core.UsageInfo{InputTokens: 200_000}, nil)
	if !got {
		t.Errorf("AutoCompactIfNeeded above trigger = false, want true (Phase-2 stub fires)")
	}
}
