package api

import (
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// TestModelTiersHaveCuratedEntries pins the three model tiers in
// internal/core to real knownModels rows. A tier that falls out of
// the table silently gets the 128k heuristic window and default
// capability flags, which is how an end-of-life default model can
// go unnoticed until a user hits HTTP 410 on their first request.
func TestModelTiersHaveCuratedEntries(t *testing.T) {
	tiers := map[string]string{
		"DefaultModel": core.DefaultModel,
		"FastModel":    core.FastModel,
		"SmartModel":   core.SmartModel,
	}
	for name, id := range tiers {
		t.Run(name, func(t *testing.T) {
			var found *ModelInfo
			for i := range knownModels {
				if knownModels[i].ID == id {
					found = &knownModels[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("core.%s = %q has no exact knownModels entry", name, id)
			}
			if !found.SupportsToolUse {
				t.Errorf("core.%s = %q: SupportsToolUse = false; the agent loop needs tool calls", name, id)
			}
			if found.ContextWindow <= 0 {
				t.Errorf("core.%s = %q: ContextWindow = %d, want > 0", name, id, found.ContextWindow)
			}
		})
	}
}

// TestDefaultModelIsNotEndOfLife guards against re-introducing
// a NIM model that has been retired. meta/llama-3.x and
// openai/gpt-oss-120b return HTTP 410 Gone on
// integrate.api.nvidia.com since 2026-08-26.
func TestDefaultModelIsNotEndOfLife(t *testing.T) {
	retired := []string{
		"meta/llama-3.3-70b-instruct",
		"meta/llama-3.1-70b-instruct",
		"meta/llama-3.1-8b-instruct",
		"openai/gpt-oss-120b",
	}
	for _, id := range []string{core.DefaultModel, core.FastModel, core.SmartModel} {
		for _, r := range retired {
			if strings.EqualFold(id, r) {
				t.Errorf("model tier %q is retired on NIM (HTTP 410)", id)
			}
		}
	}
	for _, m := range knownModels {
		for _, r := range retired {
			if strings.EqualFold(m.ID, r) {
				t.Errorf("knownModels still lists retired NIM model %q", m.ID)
			}
		}
	}
}

// TestDefaultModelContextWindow checks the curated window for
// the default model reaches ContextWindowForModel, which drives
// the auto-compaction trigger.
func TestDefaultModelContextWindow(t *testing.T) {
	if got := ContextWindowForModel(core.DefaultModel); got != 262_144 {
		t.Errorf("ContextWindowForModel(%q) = %d, want 262144", core.DefaultModel, got)
	}
	if got := LookupModel(core.DefaultModel); got.Provider != core.ProviderNIM {
		t.Errorf("LookupModel(%q).Provider = %q, want %q", core.DefaultModel, got.Provider, core.ProviderNIM)
	}
}
