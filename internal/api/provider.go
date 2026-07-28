package api

import (
	"context"

	"github.com/ArpitK24/forge/internal/core"
)

// Provider is the canonical model-backend interface. Per spec
// §4.3, every adapter (Anthropic native, OpenAI-compatible,
// Google, Azure, etc.) implements this; downstream code (query
// loop, TUI, ACP, bridge) works only against the interface and
// never against a vendor-specific type.
//
// The contract is: submit a Request, get back two channels —
// one of canonical StreamEvents, one of errors. The event
// channel is closed by the provider after EventMessageStop
// or EventError. The error channel is closed once the provider
// has no more errors to report. Either channel may produce
// values while the other is empty (events and errors can
// interleave in pathological cases).
type Provider interface {
	// Info returns static metadata about the provider: id,
	// model, context window, capability flags. Used by the
	// query loop to size the auto-compaction trigger and to
	// decide whether prompt caching is worth attempting.
	Info() ModelInfo
	// Stream opens a streaming completion against the model.
	// The returned channels are read by the query loop. The
	// implementation MUST close the events channel on
	// EventMessageStop or EventError, and MUST close the err
	// channel after the goroutine that produced events has
	// returned. ctx cancellation MUST interrupt the underlying
	// HTTP request.
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, <-chan error)
}

// ModelLister is an optional capability a Provider MAY
// implement. Adapters that wrap a provider with a live model
// catalog endpoint (Anthropic /v1/models, OpenAI /v1/models,
// OpenRouter /api/v1/models, Ollama /api/tags) satisfy this
// interface; adapters that don't (some private proxies, some
// curated gateways) don't.
//
// Keeping this off the base Provider interface is intentional:
// not every backend has a discoverable list, and forcing every
// implementation to lie with a hardcoded cursor would be a
// worse default than letting callers type-assert:
//
//	if lister, ok := provider.(api.ModelLister); ok {
//	    models, err := lister.ListModels(ctx)
//	}
//
// The list returned by ListModels is the provider's catalog as
// advertised by the API. Capability flags returned by the
// listing endpoint (when present) may be merged with the
// bundled knownModels overlay via MergeWithKnown to enrich
// the canonical ModelInfo. See provider.go's MergeWithKnown.
type ModelLister interface {
	// ListModels returns the set of models the provider will
	// accept requests for, as advertised by the provider's
	// listing endpoint. The returned slice's elements have
	// only ID and Provider populated by the adapter; the
	// capability flags and context window are filled in by
	// MergeWithKnown.
	//
	// Implementations MUST respect ctx cancellation and
	// return a *core.Error (or a wrapped error) on non-2xx so
	// the caller can surface auth/transport problems cleanly.
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ModelInfo is static metadata about a specific model on a
// specific provider. Spec §4.3: "a lookup table mapping model
// id → provider, context-window size, pricing tier, and
// capability flags (supports extended thinking, supports
// vision/image input, supports prompt caching)."
type ModelInfo struct {
	// ID is the model id, e.g. "meta/llama-3.3-70b-instruct".
	ID string
	// Provider is the provider id (e.g. ProviderOpenAI, ProviderAnthropic).
	// The provider id is informational here — the Provider
	// interface itself is what the code dispatches on.
	Provider string
	// ContextWindow is the model's input + output context window
	// in tokens. Used by query's auto-compaction trigger math.
	ContextWindow int
	// SupportsToolUse is true if the model natively supports
	// tool/function calling. Phase 2: every model we ship
	// supports it; this is here for the future case where a
	// chat-only model is added.
	SupportsToolUse bool
	// SupportsPromptCaching is true if the provider honors the
	// canonical cache_control field on the Request. NIM does
	// not; the OpenAI-compatible wire format doesn't have a
	// stable cache-control surface. The Anthropic adapter in
	// Phase 4 will set this true.
	SupportsPromptCaching bool
	// SupportsExtendedThinking is true if the provider supports
	// the canonical ThinkingConfig. NIM does not (the 70B
	// model has no separate thinking toggle). Anthropic does
	// (Phase 4 will set this true for Claude models).
	SupportsExtendedThinking bool
}

// Provider-id values used on ModelInfo.Provider. The constants
// live in internal/core (core.ProviderOpenAI,
// core.ProviderAnthropic, core.ProviderNIM) so config parsing,
// the selector, and the listing path agree on the same set of
// strings. The wire-surface vs backend-identity distinction
// documented on ModelLister's ListModels return applies here:
// live listings tag by the API surface they expose
// (OpenAI-compatible vs Anthropic-native), curated entries in
// knownModels tag by the backend that actually serves the
// model. Today both shapes are ProviderOpenAI for NIM-hosted
// Llama — when a real direct-OpenAI adapter lands, listing
// will tag itself ProviderOpenAI while curated entries from
// NIM stay ProviderNIM.

// ModelRegistry is the model-id → ModelInfo lookup table.
// Phase 2 ships a hand-picked handful of NIM-hosted models
// plus a 128k-token safe fallback for unknown current-gen
// ids (spec §5.2: "default to the larger of the two common
// context sizes for unrecognized-but-current-generation
// models, and a conservative smaller size otherwise").
//
// Per-model context windows are conservative — the actual
// NIM window can vary by deployment, so we use the public
// published number.
var knownModels = []ModelInfo{
	{
		ID:                       "meta/llama-3.3-70b-instruct",
		Provider:                 core.ProviderNIM, // served by NIM today; OpenAI-compatible surface
		ContextWindow:            128_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    false, // NIM doesn't honor cache_control
		SupportsExtendedThinking: false,
	},
	{
		ID:                       "meta/llama-3.1-70b-instruct",
		Provider:                 core.ProviderNIM,
		ContextWindow:            128_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    false,
		SupportsExtendedThinking: false,
	},
	{
		ID:                       "meta/llama-3.1-8b-instruct",
		Provider:                 core.ProviderNIM,
		ContextWindow:            128_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    false,
		SupportsExtendedThinking: false,
	},
	{
		ID:                       "openai/gpt-oss-120b",
		Provider:                 core.ProviderNIM,
		ContextWindow:            128_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    false,
		SupportsExtendedThinking: false,
	},
	// Anthropic Claude 4 family. All ship with 200k context,
	// native prompt caching (cache_control: ephemeral markers),
	// and extended thinking. Spec §5.7: "Anthropic adapter in
	// Phase 4 will set this true" — this is that step.
	//
	// Model ids are Anthropic's published names; substring
	// matching in ContextWindowForModel means partial ids
	// (e.g. "claude-sonnet-4-5-20251001") resolve correctly.
	{
		ID:                       "claude-opus-4-1",
		Provider:                 core.ProviderAnthropic,
		ContextWindow:            200_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    true,
		SupportsExtendedThinking: true,
	},
	{
		ID:                       "claude-opus-4",
		Provider:                 core.ProviderAnthropic,
		ContextWindow:            200_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    true,
		SupportsExtendedThinking: true,
	},
	{
		ID:                       "claude-sonnet-4-5",
		Provider:                 core.ProviderAnthropic,
		ContextWindow:            200_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    true,
		SupportsExtendedThinking: true,
	},
	{
		ID:                       "claude-sonnet-4",
		Provider:                 core.ProviderAnthropic,
		ContextWindow:            200_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    true,
		SupportsExtendedThinking: true,
	},
	{
		ID:                       "claude-haiku-4",
		Provider:                 core.ProviderAnthropic,
		ContextWindow:            200_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    true,
		SupportsExtendedThinking: true,
	},
	{
		ID:                       "claude-3-7-sonnet-latest",
		Provider:                 core.ProviderAnthropic,
		ContextWindow:            200_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    true,
		SupportsExtendedThinking: true,
	},
	{
		ID:                       "claude-3-5-sonnet-latest",
		Provider:                 core.ProviderAnthropic,
		ContextWindow:            200_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    true,
		SupportsExtendedThinking: false,
	},
	{
		ID:                       "claude-3-5-haiku-latest",
		Provider:                 core.ProviderAnthropic,
		ContextWindow:            200_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    true,
		SupportsExtendedThinking: false,
	},
	// Future: more providers (Google Gemini, GitHub Copilot) will
	// add their own entries here as we ship their adapters.
}

// DefaultContextWindowFallback is the context-window size we
// assume for unknown current-generation models. Spec §5.2:
// "default to the larger of the two common context sizes for
// unrecognized-but-current-generation models." 128k is the
// industry-standard "big enough for anything" current-gen
// window. Older / smaller models fall back to 8k.
const (
	DefaultContextWindowFallback      = 128_000
	ConservativeContextWindowFallback = 8_000
)

// OldGenerationSubstrings are model-id fragments that signal
// a clearly older generation. An unknown model whose id contains
// any of these gets the conservative 8k fallback rather than
// the 128k default.
var oldGenerationSubstrings = []string{
	"gpt-3.5", "gpt-4-32k", "claude-2", "claude-instant",
	"llama-2", "mistral-7b", "mixtral-8x7b",
}

// ContextWindowForModel returns the context-window size for a
// model id, with safe fallbacks per spec §5.2.
//
// Lookup is substring match (case-insensitive) against the
// knownModels table. Unknown models get 128k by default unless
// their id contains an old-generation marker, in which case
// they get 8k.
func ContextWindowForModel(modelID string) int {
	id := lower(modelID)
	for _, m := range knownModels {
		if id == m.ID || contains(id, lower(m.ID)) || contains(lower(m.ID), id) {
			return m.ContextWindow
		}
	}
	for _, s := range oldGenerationSubstrings {
		if contains(id, s) {
			return ConservativeContextWindowFallback
		}
	}
	return DefaultContextWindowFallback
}

// LookupModel returns the ModelInfo for a model id, or a
// synthesized info with the default flags if unknown.
//
// This is the synchronous, no-network fallback used by
// api.Provider constructors that need to populate Info()
// before any request has been made. Callers that have the
// chance to call ListModels once at startup (or lazily,
// cached in-process per session) should prefer the result of
// MergeWithKnown over LookupModel — MergeWithKnown trusts
// only knownModels or the live listing, never a substring
// heuristic on the id (see MergeWithKnown for details).
//
// Unknown Anthropic-shaped ids (substring "claude") get the
// Anthropic flags — prompt caching + extended thinking on by
// assumption, since every Claude model Anthropic ships today
// supports both. Unknown OpenAI/NIM-shaped ids get the original
// conservative defaults.
func LookupModel(modelID string) ModelInfo {
	// Curated hit: delegate to the same matcher MergeWithKnown
	// uses so the two functions cannot drift on what counts as
	// a known id. The test TestLookupAndMergeAgreeOnMatchedCuratedEntry
	// pins this agreement.
	if known, ok := findKnownModel(modelID); ok {
		return known
	}
	// Synthesize conservative defaults for the unknown id.
	// Pick the provider family from the id namespace.
	id := lower(modelID)
	provider := core.ProviderOpenAI
	supportsCache := false
	supportsThinking := false
	if contains(id, "claude") {
		provider = core.ProviderAnthropic
		supportsCache = true
		supportsThinking = true
	}
	return ModelInfo{
		ID:                       modelID,
		Provider:                 provider,
		ContextWindow:            ContextWindowForModel(modelID),
		SupportsToolUse:          true, // assume yes — every modern chat model can call tools
		SupportsPromptCaching:    supportsCache,
		SupportsExtendedThinking: supportsThinking,
	}
}

// MergeWithKnown takes the live listing produced by a
// ModelLister and enriches each entry's capability flags and
// ContextWindow from the bundled knownModels overlay when an
// id matches the same substring rule LookupModel uses.
//
// The merge rule:
//
//   - ID matches a knownModels entry → copy
//     SupportsPromptCaching, SupportsExtendedThinking, and
//     ContextWindow from the known entry onto the listed
//     entry. ID, Provider, and SupportsToolUse stay as the
//     adapter produced them.
//
//   - ID does not match any knownModels entry → the listed
//     entry is returned as-is, with whatever capability flags
//     the live listing supplied (usually zero — most listing
//     endpoints don't include the canonical flag set).
//     ContextWindow is filled from ContextWindowForModel only
//     when the listed entry's own ContextWindow is zero.
//
// Unlike LookupModel's synchronous fallback, an unmatched
// listed model here keeps zero-value capability flags rather
// than guessing from its id — MergeWithKnown only trusts
// knownModels or an actual API response, never a substring
// heuristic. LookupModel still does that heuristic, but only
// because it has no other option; MergeWithKnown always
// has the live listing to defer to.
//
// Callers that can issue a ListModels request (Anthropic
// /v1/models, OpenAI /v1/models, OpenRouter /api/v1/models,
// Ollama /api/tags) should prefer this function over
// LookupModel for any capability decision the query loop
// makes — e.g. whether to attempt prompt caching or offer
// /thinking. The overlay is an enrichment, not a gate: an
// unlisted but live model still works; it just gets zero
// flags until Forge ships a curated entry.
func MergeWithKnown(listed []ModelInfo) []ModelInfo {
	out := make([]ModelInfo, len(listed))
	copy(out, listed)
	for i := range out {
		if entry, ok := findKnownModel(out[i].ID); ok {
			// Copy capability flags + context window from
			// the curated entry. ID, Provider, and
			// SupportsToolUse stay as the adapter produced
			// them — the live listing is authoritative on
			// what the provider actually exposes today,
			// and knownModels only fills what the listing
			// can't tell us.
			out[i].SupportsPromptCaching = entry.SupportsPromptCaching
			out[i].SupportsExtendedThinking = entry.SupportsExtendedThinking
			out[i].ContextWindow = entry.ContextWindow
			continue
		}
		// No curated entry. If the live listing didn't tell
		// us a context window, fall back to the same
		// heuristic ContextWindowForModel applies — but
		// DO NOT guess capability flags. A model we don't
		// recognize should be assumed to NOT support
		// prompt caching or extended thinking until
		// proven otherwise. That's the whole point of
		// moving away from the id-substring heuristic.
		if out[i].ContextWindow == 0 {
			out[i].ContextWindow = ContextWindowForModel(out[i].ID)
		}
	}
	return out
}

// findKnownModel is the substring-match lookup used by
// MergeWithKnown. It mirrors the same rule LookupModel applies
// internally so the two functions agree on which id belongs to
// which curated entry.
//
// Returns the matched known entry and true, or the zero
// ModelInfo and false.
func findKnownModel(modelID string) (ModelInfo, bool) {
	id := lower(modelID)
	for _, m := range knownModels {
		if id == m.ID || contains(id, lower(m.ID)) || contains(lower(m.ID), id) {
			return m, true
		}
	}
	return ModelInfo{}, false
}

// lower is a tiny ASCII-only lowercase to avoid the strings
// import dependency in a hot lookup.
func lower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
