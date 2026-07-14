package api

import "context"

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
		Provider:                 "openai", // NIM is OpenAI-compatible
		ContextWindow:            128_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    false, // NIM doesn't honor cache_control
		SupportsExtendedThinking: false,
	},
	{
		ID:                       "meta/llama-3.1-70b-instruct",
		Provider:                 "openai",
		ContextWindow:            128_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    false,
		SupportsExtendedThinking: false,
	},
	{
		ID:                       "meta/llama-3.1-8b-instruct",
		Provider:                 "openai",
		ContextWindow:            128_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    false,
		SupportsExtendedThinking: false,
	},
	{
		ID:                       "openai/gpt-oss-120b",
		Provider:                 "openai",
		ContextWindow:            128_000,
		SupportsToolUse:          true,
		SupportsPromptCaching:    false,
		SupportsExtendedThinking: false,
	},
	// Future: Anthropic models (Phase 4) will have SupportsPromptCaching=true.
}

// DefaultContextWindowFallback is the context-window size we
// assume for unknown current-generation models. Spec §5.2:
// "default to the larger of the two common context sizes for
// unrecognized-but-current-generation models." 128k is the
// industry-standard "big enough for anything" current-gen
// window. Older / smaller models fall back to 8k.
const (
	DefaultContextWindowFallback  = 128_000
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
func LookupModel(modelID string) ModelInfo {
	id := lower(modelID)
	for _, m := range knownModels {
		if id == m.ID || contains(id, lower(m.ID)) || contains(lower(m.ID), id) {
			return m
		}
	}
	// Synthesize a conservative ModelInfo for the unknown id.
	return ModelInfo{
		ID:                       modelID,
		Provider:                 "openai",
		ContextWindow:            ContextWindowForModel(modelID),
		SupportsToolUse:          true, // assume yes — model is on a tool-call-capable endpoint
		SupportsPromptCaching:    false,
		SupportsExtendedThinking: false,
	}
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
