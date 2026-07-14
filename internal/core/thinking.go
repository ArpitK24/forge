package core

// ThinkingConfig is the extended-reasoning / chain-of-thought
// budget. Spec §2.10: "effort/reasoning-level enum" and §4.5:
// "an 'enabled' flag plus a token budget." Providers that
// don't support variable reasoning effort silently ignore
// this — NIM in Phase 2 does; Anthropic in Phase 4 will.
//
// The fields are an int (BudgetTokens) and a bool (Enabled).
// A zero-value ThinkingConfig is the "off" state, which is
// what every caller gets by default if they don't construct
// one explicitly.
type ThinkingConfig struct {
	// Enabled is the master switch. When false, the model
	// runs without extended reasoning even if a budget is set.
	Enabled bool
	// BudgetTokens is the maximum number of reasoning tokens
	// the model may spend before the visible response.
	BudgetTokens int
}
