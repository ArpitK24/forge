package api

import (
	"github.com/ArpitK24/forge/internal/core"
)

// Request is the canonical, provider-agnostic message-creation
// request. Per spec §4.1: model id, max tokens, message list,
// optional system prompt (plain text or a list of cacheable
// system blocks), optional tool definitions, sampling params,
// stop sequences, optional thinking config, and an internally-
// forced stream=true.
//
// Adapter implementations translate this into their vendor's
// wire format. Downstream code (query loop, TUI, ACP) builds
// this once and feeds it to whatever provider is active.
type Request struct {
	// Model is the model id, e.g. "meta/llama-3.3-70b-instruct".
	Model string
	// MaxTokens is the per-response output cap.
	MaxTokens int
	// Messages is the full conversation history up to and including
	// the current user turn. The query loop owns appending the
	// model's prior turn to this list before the next iteration.
	Messages []core.Message
	// System is the system prompt. Phase 2 sends a single string;
	// a future Anthropic adapter (Phase 4) will exercise the
	// list-of-blocks shape with cache_control markers.
	System SystemPrompt
	// Tools is the set of tools the model may invoke this turn.
	Tools []core.ToolDefinition
	// Temperature is the sampling temperature. nil = "use the
	// provider's default" (which is usually 1.0 for NIM/OpenAI
	// and 1.0 for Anthropic, so we set 0.2 explicitly when the
	// agent is doing tool-calling work and want stability).
	Temperature *float64
	// TopP is nucleus sampling. nil = "use the provider's default."
	TopP *float64
	// StopSequences, if non-empty, are strings at which the
	// model must stop generating further tokens.
	StopSequences []string
	// Thinking is the extended-reasoning budget config, if the
	// provider supports it. Phase 2's NIM path sets it to nil
	// (NIM doesn't have a separate thinking toggle for the
	// 3.3-70B model). Phase 4's Anthropic adapter will use it.
	Thinking *ThinkingConfig
	// Stream is forced true by the provider adapters — the
	// Provider.Stream contract always returns a stream. We
	// keep the field anyway so future non-streaming adapters
	// (a /no-stream CLI flag) can flip it.
	Stream bool
}

// SystemPrompt is the "either a plain string or a list of
// cacheable blocks" shape from spec §4.1. Phase 2 sends the
// string form; the list form (which Anthropic supports for
// per-block cache_control) is wired up but not emitted by any
// current adapter. Keeping both shapes lets the next Anthropic
// adapter light up cache_control without touching call sites.
type SystemPrompt struct {
	// IsString is true when this is plain text.
	IsString bool
	// String is the text when IsString.
	String string
	// Blocks are the cacheable text blocks when !IsString.
	Blocks []SystemBlock
}

// SystemBlock is a single system-prompt block with optional
// cache-control marker. The Anthropic adapter maps this to
// its `system: [{type, text, cache_control}]` shape.
type SystemBlock struct {
	Text          string
	CacheControl  *CacheControl
}

// CacheControl is the spec-agnostic cacheability marker.
// Providers that don't understand it (NIM, raw OpenAI) ignore
// it; Anthropic maps it to its `ephemeral` cache control type.
type CacheControl struct {
	// Type is the cache-control kind. Most providers support
	// only "ephemeral" today; we keep the field open for future
	// types ("persistent", "long-lived", etc.).
	Type string
}

// SystemString constructs a plain-text SystemPrompt.
func SystemString(s string) SystemPrompt {
	return SystemPrompt{IsString: true, String: s}
}

// SystemBlocks constructs a list-of-blocks SystemPrompt.
func SystemBlocks(blocks ...SystemBlock) SystemPrompt {
	return SystemPrompt{IsString: false, Blocks: blocks}
}

// ThinkingConfig is an alias kept here for backwards
// compatibility with early callers; the canonical type lives
// in core. Spec §4.5: an "enabled" flag plus a token budget.
// Providers that don't support variable effort (NIM in Phase 2)
// silently ignore this.
type ThinkingConfig = core.ThinkingConfig

// Request is the message-level request data passed to the model.
// (This is just a doc-comment alias for the top-level Request —
// keeping the spec language visible at the package level.)
//
// The wire format of any individual provider adapter lives in
// that adapter's package; the canonical shape here is what every
// adapter translates *to* and *from*.
