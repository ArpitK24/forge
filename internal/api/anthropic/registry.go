package anthropic

import "github.com/ArpitK24/forge/internal/api"

// ContextWindowForModel is a thin re-export so callers using
// the anthropic adapter can stay inside this package when they
// need the window size. The canonical implementation is in
// internal/api; the known-models table in that package holds the
// authoritative Claude context windows and capability flags
// (200k for Claude 4 family, prompt caching + extended thinking
// both supported).
func ContextWindowForModel(modelID string) int {
	return api.ContextWindowForModel(modelID)
}

// LookupModel is a thin re-export, same rationale as
// ContextWindowForModel. Returns the ModelInfo (context window,
// capability flags) for the given model id, or a synthesized
// conservative entry for unknown ids.
func LookupModel(modelID string) api.ModelInfo {
	return api.LookupModel(modelID)
}
