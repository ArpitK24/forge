package query

import (
	"context"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// --- Trigger math (unchanged from Phase 2) ------------------------

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

// --- Real summarizer (Phase 4 Step 1) -----------------------------

// fakeSummaryScript returns a FakeProvider whose Stream emits
// a one-turn canned summary. The summary text is what the
// "model" produces.
func fakeSummaryScript(summary string) *api.FakeProvider {
	return api.NewFakeProvider(api.ScriptTextResponse(summary))
}

func TestCompactConversationShorterThanTailIsNoOp(t *testing.T) {
	// A conversation shorter than AutoCompactTailKeep can't
	// be compacted (there's nothing older than the tail).
	// CompactConversation must return the input unchanged.
	msgs := []core.Message{
		core.NewUserText("hi"),
		core.NewAssistantText("hello"),
		core.NewUserText("how are you?"),
	}
	out, err := CompactConversation(context.Background(),
		fakeSummaryScript("ignored — we won't get here"),
		msgs, "fake-model", core.FastModel)
	if err != nil {
		t.Fatalf("CompactConversation err = %v, want nil", err)
	}
	if len(out) != len(msgs) {
		t.Errorf("len(out) = %d, want %d", len(out), len(msgs))
	}
	for i := range msgs {
		if msgs[i].GetFirstText() != out[i].GetFirstText() {
			t.Errorf("msg %d changed: in=%q out=%q", i,
				msgs[i].GetFirstText(), out[i].GetFirstText())
		}
	}
}

func TestCompactConversationSummarizesHeadAndKeepsTail(t *testing.T) {
	// Build a 15-message conversation. AutoCompactTailKeep is
	// 10, so the head is 5 messages and the tail is the last
	// 10. The summarizer's canned response is the summary
	// text; the compacted slice must contain 1 user-role
	// summary message + the original 10 tail messages, in
	// that order, with the head messages GONE.
	const headLen = 5
	const tailLen = core.AutoCompactTailKeep
	totalLen := headLen + tailLen
	msgs := make([]core.Message, 0, totalLen)
	for i := 0; i < headLen; i++ {
		msgs = append(msgs, core.NewUserText("head-"+string(rune('A'+i))))
	}
	for i := 0; i < tailLen; i++ {
		msgs = append(msgs, core.NewUserText("tail-"+string(rune('A'+i))))
	}

	const summaryText = "Five older messages summarized."
	out, err := CompactConversation(context.Background(),
		fakeSummaryScript(summaryText),
		msgs, "fake-model", "fake-summarizer")
	if err != nil {
		t.Fatalf("CompactConversation err = %v, want nil", err)
	}
	if len(out) != 1+tailLen {
		t.Fatalf("len(out) = %d, want %d (summary + tail)", len(out), 1+tailLen)
	}
	// First message is the summary — user role, marked.
	summaryMsg := out[0]
	if summaryMsg.Role != core.RoleUser {
		t.Errorf("summary role = %v, want RoleUser", summaryMsg.Role)
	}
	if !strings.Contains(summaryMsg.GetFirstText(), SummaryMarkerOpen) {
		t.Errorf("summary body missing opening marker: %q", summaryMsg.GetFirstText())
	}
	if !strings.Contains(summaryMsg.GetFirstText(), SummaryMarkerClose) {
		t.Errorf("summary body missing closing marker: %q", summaryMsg.GetFirstText())
	}
	if !strings.Contains(summaryMsg.GetFirstText(), summaryText) {
		t.Errorf("summary body missing summary text %q: %q", summaryText, summaryMsg.GetFirstText())
	}
	if !strings.Contains(summaryMsg.GetFirstText(), `turns-summarized="5"`) {
		t.Errorf("summary body missing turns-summarized attribute: %q", summaryMsg.GetFirstText())
	}
	// Tail messages are preserved verbatim, in order.
	for i := 0; i < tailLen; i++ {
		want := "tail-" + string(rune('A'+i))
		if got := out[1+i].GetFirstText(); got != want {
			t.Errorf("tail[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestCompactConversationSummaryFailureReturnsInput(t *testing.T) {
	// A provider that errors on Stream must cause
	// CompactConversation to return the input slice
	// unchanged, so the caller can decide whether to retry
	// (and increment the failure counter).
	p := api.NewFakeProvider(nil) // nil script → Stream errors
	msgs := make([]core.Message, 0, 20)
	for i := 0; i < 20; i++ {
		msgs = append(msgs, core.NewUserText("msg-"+string(rune('A'+i))))
	}
	out, err := CompactConversation(context.Background(),
		p, msgs, "fake-model", "fake-summarizer")
	if err == nil {
		t.Errorf("CompactConversation err = nil, want non-nil on provider error")
	}
	if len(out) != len(msgs) {
		t.Errorf("len(out) = %d, want %d (input unchanged on failure)",
			len(out), len(msgs))
	}
}

// --- AutoCompactIfNeeded wiring -----------------------------------

func TestAutoCompactIfNeededNoMessages(t *testing.T) {
	var msgs []core.Message
	state := &CompactState{}
	got := AutoCompactIfNeeded(context.Background(),
		fakeSummaryScript("ignored"), &msgs, "fake-model", "fake-summarizer",
		core.UsageInfo{InputTokens: 999_999}, state, nil)
	if got {
		t.Errorf("AutoCompactIfNeeded with no messages = true, want false")
	}
	if state.CompactionCount != 0 {
		t.Errorf("CompactionCount = %d, want 0", state.CompactionCount)
	}
}

func TestAutoCompactIfNeededBelowTriggerIsNoOp(t *testing.T) {
	msgs := []core.Message{core.NewUserText("hi"), core.NewAssistantText("hello")}
	original := len(msgs)
	state := &CompactState{}
	got := AutoCompactIfNeeded(context.Background(),
		fakeSummaryScript("ignored"), &msgs, "fake-model", "fake-summarizer",
		core.UsageInfo{InputTokens: 100}, state, nil)
	if got {
		t.Errorf("AutoCompactIfNeeded below trigger = true, want false")
	}
	if len(msgs) != original {
		t.Errorf("messages changed when no compaction should have fired: %d", len(msgs))
	}
	if state.CompactionCount != 0 {
		t.Errorf("CompactionCount = %d, want 0", state.CompactionCount)
	}
}

func TestAutoCompactIfNeededAboveTriggerCompactsAndCounts(t *testing.T) {
	// Above the trigger with a FakeProvider that returns a
	// canned summary. AutoCompactIfNeeded should compact the
	// slice, increment CompactionCount, and emit a StatusEvent
	// on the event channel.
	msgs := make([]core.Message, 0, 20)
	for i := 0; i < 20; i++ {
		msgs = append(msgs, core.NewUserText("m-"+string(rune('A'+i))))
	}
	state := &CompactState{}
	events := make(chan Event, 16)

	got := AutoCompactIfNeeded(context.Background(),
		fakeSummaryScript("short summary"),
		&msgs, "fake-model", "fake-summarizer",
		core.UsageInfo{InputTokens: 200_000}, state, events)

	if !got {
		t.Fatalf("AutoCompactIfNeeded above trigger = false, want true")
	}
	// Slice must shrink: 20 messages → 1 summary + 10 tail.
	if len(msgs) != 1+core.AutoCompactTailKeep {
		t.Errorf("len(msgs) = %d, want %d", len(msgs), 1+core.AutoCompactTailKeep)
	}
	// First message is the summary marker.
	if !strings.Contains(msgs[0].GetFirstText(), SummaryMarkerOpen) {
		t.Errorf("first message is not the summary marker: %q", msgs[0].GetFirstText())
	}
	// State updated.
	if state.CompactionCount != 1 {
		t.Errorf("CompactionCount = %d, want 1", state.CompactionCount)
	}
	if state.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0 on success", state.ConsecutiveFailures)
	}
	// StatusEvent emitted.
	select {
	case ev := <-events:
		se, ok := ev.(StatusEvent)
		if !ok {
			t.Errorf("event = %T, want StatusEvent", ev)
		} else if !strings.Contains(se.Message, "compacted conversation") {
			t.Errorf("status message = %q, want contains 'compacted conversation'", se.Message)
		}
	default:
		t.Errorf("no StatusEvent was emitted on the event channel")
	}
}

func TestAutoCompactIfNeededFailureIncrementsCounter(t *testing.T) {
	// A failing provider (nil script → error). AutoCompactIfNeeded
	// must increment the failure counter and NOT change the
	// message slice. After AutoCompactMaxConsecutiveFailures
	// failures in a row the Disabled flag trips.
	msgs := make([]core.Message, 0, 20)
	for i := 0; i < 20; i++ {
		msgs = append(msgs, core.NewUserText("m-"+string(rune('A'+i))))
	}
	state := &CompactState{}
	failingProvider := api.NewFakeProvider(nil)

	for i := 0; i < core.AutoCompactMaxConsecutiveFailures; i++ {
		got := AutoCompactIfNeeded(context.Background(),
			failingProvider, &msgs, "fake-model", "fake-summarizer",
			core.UsageInfo{InputTokens: 200_000}, state, nil)
		if got {
			t.Errorf("attempt %d: AutoCompactIfNeeded = true, want false (provider errors)", i)
		}
		if state.ConsecutiveFailures != i+1 {
			t.Errorf("attempt %d: ConsecutiveFailures = %d, want %d",
				i, state.ConsecutiveFailures, i+1)
		}
	}
	// After the cap, the breaker must trip.
	if !state.Disabled {
		t.Errorf("Disabled = false, want true after %d failures",
			core.AutoCompactMaxConsecutiveFailures)
	}
	// And the slice is untouched.
	if len(msgs) != 20 {
		t.Errorf("len(msgs) = %d, want 20 (unchanged on failure)", len(msgs))
	}
}

func TestAutoCompactIfNeededResetsFailureCounterOnSuccess(t *testing.T) {
	// One failure then a success: counter must reset to 0.
	msgs := make([]core.Message, 0, 20)
	for i := 0; i < 20; i++ {
		msgs = append(msgs, core.NewUserText("m-"+string(rune('A'+i))))
	}
	// Pre-load with 1 prior failure so this test still
	// exercises "not yet at the breaker cap."
	state := &CompactState{ConsecutiveFailures: 1}

	// First a failure (counter goes 1 → 2; breaker not yet tripped).
	failing := api.NewFakeProvider(nil)
	_ = AutoCompactIfNeeded(context.Background(),
		failing, &msgs, "fake-model", "fake-summarizer",
		core.UsageInfo{InputTokens: 200_000}, state, nil)
	if state.ConsecutiveFailures != 2 {
		t.Fatalf("after first failure: ConsecutiveFailures = %d, want 2",
			state.ConsecutiveFailures)
	}
	// Now a success.
	ok := AutoCompactIfNeeded(context.Background(),
		fakeSummaryScript("ok"),
		&msgs, "fake-model", "fake-summarizer",
		core.UsageInfo{InputTokens: 200_000}, state, nil)
	if !ok {
		t.Fatalf("second attempt should have succeeded")
	}
	if state.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0 after success", state.ConsecutiveFailures)
	}
}

func TestAutoCompactIfNeededNilStateTolerated(t *testing.T) {
	// A nil CompactState must not panic. The function still
	// compacts (when the trigger fires) but doesn't update any
	// counters — useful for one-shot test scenarios.
	msgs := make([]core.Message, 0, 20)
	for i := 0; i < 20; i++ {
		msgs = append(msgs, core.NewUserText("m-"+string(rune('A'+i))))
	}
	got := AutoCompactIfNeeded(context.Background(),
		fakeSummaryScript("ok"), &msgs, "fake-model", "fake-summarizer",
		core.UsageInfo{InputTokens: 200_000}, nil, nil)
	if !got {
		t.Errorf("AutoCompactIfNeeded with nil state = false, want true")
	}
}
