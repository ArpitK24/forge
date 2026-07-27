package query

import (
	"context"
	"fmt"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// TokenWarningState is the human-visible state of the
// context-window warning indicator. Spec §5.2: Ok / Warning /
// Critical, based on remaining-headroom thresholds.
type TokenWarningState int

const (
	// TokenWarningOk means plenty of room left.
	TokenWarningOk TokenWarningState = iota
	// TokenWarningWarning means the user should be told
	// compaction is approaching.
	TokenWarningWarning
	// TokenWarningCritical means compaction should fire on
	// the next turn.
	TokenWarningCritical
)

// String returns the stable name.
func (s TokenWarningState) String() string {
	switch s {
	case TokenWarningOk:
		return "ok"
	case TokenWarningWarning:
		return "warning"
	case TokenWarningCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// CalculateTokenWarningState returns Ok/Warning/Critical based
// on the remaining headroom. Spec §5.2: Warning when remaining
// is below AutoCompactWarningTokens; Critical when below
// AutoCompactReserveTokens.
func CalculateTokenWarningState(used, contextWindow int) TokenWarningState {
	remaining := contextWindow - used
	if remaining < core.AutoCompactReserveTokens {
		return TokenWarningCritical
	}
	if remaining < core.AutoCompactWarningTokens {
		return TokenWarningWarning
	}
	return TokenWarningOk
}

// ShouldAutoCompact reports whether auto-compaction should fire.
// Returns false if the circuit breaker is tripped (too many
// consecutive failures); otherwise returns true when used has
// crossed the trigger fraction of the context window.
//
// Spec §5.2: "false if the circuit breaker is tripped;
// otherwise true once the trigger fraction is crossed."
func ShouldAutoCompact(used, contextWindow int, consecutiveFailures int) bool {
	if consecutiveFailures >= core.AutoCompactMaxConsecutiveFailures {
		return false
	}
	if contextWindow <= 0 {
		return false
	}
	trigger := int(float64(contextWindow) * core.AutoCompactTriggerFraction)
	return used >= trigger
}

// CompactState is the per-session auto-compaction state.
// Spec §5.2 requires tracking "compaction count, consecutive-
// failure count, and a disabled flag (a circuit breaker that
// trips after 3 consecutive compaction failures)."
//
// The query loop's RunQueryLoop constructs one of these per
// session and passes a pointer to AutoCompactIfNeeded; the
// function mutates it in place. Callers that don't care about
// the counters can pass nil — the function tolerates a nil
// state pointer for read-only callers (tests, one-shot
// explorations) but mutating paths must hand it a real one.
//
// Phase 2 abused CostTracker.LastModel as a stand-in for this
// state; Phase 4 lifts that hack into a proper type.
type CompactState struct {
	// ConsecutiveFailures is the count of back-to-back
	// compaction errors. ShouldAutoCompact short-circuits when
	// it reaches AutoCompactMaxConsecutiveFailures (the
	// circuit-breaker trip).
	ConsecutiveFailures int
	// CompactionCount is the total number of successful
	// compactions in this session. Surfaced to renderers and
	// observers; not used by the trigger math itself.
	CompactionCount int
	// Disabled is the trip flag. Once true, no further
	// compaction attempts fire until the caller resets it
	// (typically on session restart or a successful manual
	// /compact). Spec §5.2: "trip the circuit breaker after
	// 3 consecutive failures."
	Disabled bool
}

// SummaryMarkerOpen / SummaryMarkerClose delimit a compact
// summary in the conversation. They make the summary block
// easy for a future /inspect view to identify (spec §5.2:
// "clearly delimited compact-summary marker") and easy for
// humans to grep for in saved sessions.
const (
	// SummaryMarkerOpen opens the compact-summary block.
	// The opening tag carries two attributes the model emits
	// verbatim into the summary header: the model that
	// produced the summary (so consumers can audit which
	// model summarized what) and the count of summarized
	// turns. Both are advisory metadata.
	SummaryMarkerOpen = "<forge-compact-summary"
	// SummaryMarkerClose closes the compact-summary block.
	SummaryMarkerClose = "</forge-compact-summary>"
)

// CompactConversation replaces an older slice of the
// conversation with a model-generated summary. Spec §5.2's
// "compact_conversation": split into head (older) and tail
// (most recent AutoCompactTailKeep messages), summarize the
// head, return [summary-as-a-user-message] + tail.
//
// Parameters:
//   - ctx: cancellation. Propagated to SummarizeHead.
//   - client: the Provider. CompactConversation issues one
//     non-streaming API call (via SummarizeHead) to produce
//     the summary; passing the loop's regular provider is
//     correct because the summary model is selected via
//     summaryModel.
//   - messages: the full conversation. Read-only — the
//     returned slice is freshly allocated.
//   - model: the active loop model id. Currently unused by
//     the body; carried in the signature so a future
//     provider-aware variant (Anthropic adapter's cache
//     marker on the summary message) can read it without a
//     second signature change.
//   - summaryModel: the model id to invoke for the summary
//     call. In Step 1 this is hardcoded to core.FastModel.
//
// Returns the compacted slice (summary message + tail). When
// the conversation is shorter than AutoCompactTailKeep+1,
// the input is returned unchanged — there's nothing older
// than the tail to summarize. On a summary-call failure,
// returns the input slice unchanged and a non-nil error so
// the caller can increment its failure counter.
//
// The summary is wrapped in a user-role message whose body
// begins with SummaryMarkerOpen (carrying the summary model
// and the count of summarized turns as XML-style attributes)
// and ends with SummaryMarkerClose. The model's summary text
// sits between the markers verbatim.
func CompactConversation(
	ctx context.Context,
	client api.Provider,
	messages []core.Message,
	model string,
	summaryModel string,
) ([]core.Message, error) {
	_ = model // reserved for future per-provider decoration.
	if len(messages) <= core.AutoCompactTailKeep {
		// Not enough head to summarize. Spec §5.2's tail-keep
		// is the floor: everything is tail. Return unchanged.
		return messages, nil
	}
	cutoff := len(messages) - core.AutoCompactTailKeep
	head := make([]core.Message, cutoff)
	copy(head, messages[:cutoff])
	tail := messages[cutoff:]

	summaryText, err := SummarizeHead(ctx, client, summaryModel, head)
	if err != nil {
		// On failure, return the input slice so the caller
		// can decide whether to retry. We don't synthesize a
		// "compaction failed" message because the model needs
		// to see the conversation, not a status line.
		return messages, err
	}

	summaryMsg := buildSummaryMessage(summaryText, summaryModel, len(head))
	out := make([]core.Message, 0, 1+len(tail))
	out = append(out, summaryMsg)
	out = append(out, tail...)
	return out, nil
}

// buildSummaryMessage wraps the summary text in the
// compact-summary marker. The marker attributes are advisory
// metadata: a human reading a saved session file can see at
// a glance which model produced the summary and how many
// older turns were collapsed. The model itself only sees
// the body (between markers).
func buildSummaryMessage(text, model string, turnsSummarized int) core.Message {
	header := fmt.Sprintf("%s model=%q turns-summarized=\"%d\">\n",
		SummaryMarkerOpen, model, turnsSummarized)
	body := header + text + "\n" + SummaryMarkerClose
	return core.NewUserText(body)
}

// AutoCompactIfNeeded is the entry point called from the query
// loop after every turn. It checks the trigger, runs
// compaction, and updates the per-session state in *state.
// Spec §5.2: "on success reset the failure counter and
// increment the compaction counter; on failure increment the
// failure counter and trip the circuit breaker after 3
// consecutive failures."
//
// Parameters:
//   - ctx: cancellation. Propagated to CompactConversation.
//   - client: the Provider. Same one the loop is using.
//   - messages: the conversation history. Mutated in place:
//     the slice header is reassigned to the compacted slice
//     on success. Callers that share the slice with other
//     goroutines (the TUI does, via sharedState) must hold
//     their own lock around the call.
//   - model: the active loop model id. Forwarded to
//     CompactConversation (currently unused inside).
//   - summaryModel: the model id to use for the summary
//     call. Hardcoded to core.FastModel by the call sites in
//     Phase 4 Step 1.
//   - turnUsage: the most recent turn's token usage. Used as
//     the "used" approximation in the trigger math; a proper
//     implementation would track the running input total
//     across turns, but a per-turn snapshot is enough to
//     exercise the trigger math (and is what the loop
//     already has on hand).
//   - state: the per-session compaction state. May be nil
//     for callers that don't track counters (tests). When
//     nil, failure / success bookkeeping is skipped — the
//     function still compacts but doesn't update any
//     counters.
//
// Returns true when compaction fired AND succeeded (the
// caller can use this to know whether to emit any
// downstream event). Returns false on no-op (below trigger,
// empty history, disabled, or summary-call failure).
//
// On success a StatusEvent describing the compaction is sent
// on eventCh when eventCh is non-nil. The event is best-
// effort — the loop's sendEvent helper drops events when
// the buffer is full, so backpressure never blocks the loop.
func AutoCompactIfNeeded(
	ctx context.Context,
	client api.Provider,
	messages *[]core.Message,
	model string,
	summaryModel string,
	turnUsage core.UsageInfo,
	state *CompactState,
	eventCh chan<- Event,
) bool {
	if messages == nil || len(*messages) == 0 {
		return false
	}
	// Track the circuit-breaker state. nil state means "I'm a
	// one-shot test; don't bother with the counter."
	failures := 0
	if state != nil {
		failures = state.ConsecutiveFailures
	}
	used := turnUsage.InputTokens
	window := api.ContextWindowForModel(model)
	if !ShouldAutoCompact(used, window, failures) {
		return false
	}

	before := len(*messages)
	compacted, err := CompactConversation(ctx, client, *messages, model, summaryModel)
	if err != nil {
		// Failure: increment counter, trip the breaker.
		if state != nil {
			state.ConsecutiveFailures++
			if state.ConsecutiveFailures >= core.AutoCompactMaxConsecutiveFailures {
				state.Disabled = true
			}
		}
		return false
	}
	*messages = compacted

	// Success: reset failures, increment compaction count.
	if state != nil {
		state.ConsecutiveFailures = 0
		state.CompactionCount++
	}

	// Best-effort StatusEvent. Renderers and observers can
	// surface it (TUI toast, headless stderr) without forcing
	// a render path here.
	sendEvent(eventCh, StatusEvent{
		Message: fmt.Sprintf("compacted conversation: %d → %d messages",
			before, len(compacted)),
	})
	return true
}
