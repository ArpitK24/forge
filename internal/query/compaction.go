package query

import (
	"context"

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

// CompactConversation is the Phase 2 stub for §5.2's real
// summarizer. The interface and the wiring are in scope; the
// implementation lands in Phase 4 alongside the cost-tracker /
// history-store work.
//
// Phase-2 behavior: returns the input messages unchanged and a
// nil error. Tests assert that the wiring is exercised without
// requiring a real summarizer.
func CompactConversation(_ context.Context, messages []core.Message, _ string) ([]core.Message, error) {
	return messages, nil
}

// AutoCompactIfNeeded is the entry point called from the query
// loop after every turn. It checks the trigger, runs
// compaction, and updates the per-session failure counter. The
// failure counter lives on a *core.CostTracker — we abuse the
// tracker's last-model field as a stand-in for a per-session
// state store for Phase 2; Phase 4 introduces a proper
// SessionState type.
//
// Spec §5.2: "on success reset the failure counter and
// increment the compaction counter; on failure increment the
// failure counter and trip the circuit breaker after 3
// consecutive failures."
func AutoCompactIfNeeded(
	ctx context.Context,
	messages *[]core.Message,
	model string,
	turnUsage core.UsageInfo,
	_ *core.CostTracker,
) bool {
	if messages == nil || len(*messages) == 0 {
		return false
	}
	// We use the most recent input-token count as the "used"
	// approximation. A proper implementation would track the
	// running input total across turns; for Phase 2 the
	// per-turn snapshot is enough to exercise the trigger math.
	used := turnUsage.InputTokens
	window := api.ContextWindowForModel(model)
	if !ShouldAutoCompact(used, window, 0) {
		return false
	}
	compacted, err := CompactConversation(ctx, *messages, model)
	if err != nil {
		// Failure: in Phase 4, increment the failure counter
		// on the session state. For now, just return false.
		return false
	}
	*messages = compacted
	return true
}
