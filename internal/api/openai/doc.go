// Package openai implements the OpenAI-compatible HTTP/SSE
// adapter that Forge uses against NVIDIA NIM (and any other
// OpenAI-compatible endpoint).
//
// NIM exposes the OpenAI Chat Completions API at
// `https://integrate.api.nvidia.com/v1/chat/completions`, with
// SSE streaming. The wire format is the standard OpenAI shape:
//
//	POST /v1/chat/completions
//	Authorization: Bearer <key>
//	Content-Type: application/json
//	Accept: text/event-stream
//	{
//	  "model": "meta/llama-3.3-70b-instruct",
//	  "messages": [...],
//	  "stream": true,
//	  "tools": [...]
//	}
//
// Response is a sequence of `data: {...}` SSE frames, each
// carrying a Chat Completions chunk. We translate each chunk
// into one or more canonical api.StreamEvents on the way out.
// The final frame is `data: [DONE]`, which we map to
// EventMessageStop.
//
// Phase 2's adapter covers NIM end-to-end. Phase 4's Anthropic
// adapter ships a separate, vendor-specific wire format in its
// own package; the bridge between the two adapters and Forge's
// canonical event format is the `api.Provider` interface.
//
// This package deliberately has zero dependencies on the rest
// of Forge. It only imports internal/api and internal/core.
package openai
