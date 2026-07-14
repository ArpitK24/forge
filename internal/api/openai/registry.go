package openai

import "github.com/ArpitK24/forge/internal/api"

// ContextWindowForModel is a thin re-export so callers using
// the openai adapter can stay inside this package when they
// need the window size. The canonical implementation is in
// internal/api.
//
// Phase 2: NIM and the OpenAI-compatible endpoint don't
// distinguish themselves from any other OpenAI-compatible
// endpoint on the wire, so the registry is shared. Phase 4's
// Anthropic adapter will have its own registry.
func ContextWindowForModel(modelID string) int {
	return api.ContextWindowForModel(modelID)
}

// LookupModel is a thin re-export, same rationale as
// ContextWindowForModel.
func LookupModel(modelID string) api.ModelInfo {
	return api.LookupModel(modelID)
}
