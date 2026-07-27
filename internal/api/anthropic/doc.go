// Package anthropic is the Anthropic Messages API adapter for Forge.
// It implements api.Provider by translating the canonical Request
// into Anthropic's wire format and forwarding the SSE response as
// canonical StreamEvents.
//
// Why a dedicated package (instead of routing Anthropic through the
// existing internal/api/openai/ adapter):
//
//   - Different endpoint: POST /v1/messages, not /chat/completions.
//   - Different auth: x-api-key header + anthropic-version: 2023-06-01
//     instead of Authorization: Bearer.
//   - System prompt is a top-level field, not a message.
//   - Tool results live inside the next user message as content
//     blocks (no separate tool role).
//   - SSE carries an `event:` name AND a `data:` payload — the parser
//     switches on event names (message_start, content_block_start,
//     content_block_delta, content_block_stop, message_delta,
//     message_stop, ping, error), not just on data payloads.
//   - Stop reason vocabulary is different
//     (end_turn / tool_use / max_tokens / stop_sequence / refusal /
//     model_context_window_exceeded / pause_turn).
//   - Prompt caching is a first-class concept
//     (cache_control: {type: "ephemeral"} markers on system blocks,
//     tools, message content blocks).
//   - Thinking is a first-class concept
//     (thinking: {type: "enabled", budget_tokens} with the
//     constraint that budget_tokens < max_tokens and temperature
//     must be unset when thinking is on).
//
// Public surface is intentionally minimal:
//
//   - Client implements api.Provider.
//   - New / NewWithHTTP construct one.
//   - ContextWindowForModel / LookupModel mirror the openai adapter's
//     helpers.
//
// Wire format references:
//
//   - POST /v1/messages:  https://platform.claude.com/docs/en/api/messages
//   - Streaming events:   https://platform.claude.com/docs/en/build-with-claude/streaming
//   - Thinking:           https://platform.claude.com/docs/en/build-with-claude/thinking
//
// Spec references (Forge):
//
//   - §4.3 Provider interface
//   - §5.2 Context windows and per-model metadata
//   - §5.7 Multi-provider (this package is the Anthropic half)
package anthropic
