package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// SSE state — the parser keeps one sseState per call so it can
// track the current event name across line-by-line reads. SSE
// events are framed by blank lines: each event is a sequence of
// "field: value" lines, then a blank line. We accumulate the
// fields of the current event and dispatch on flush when we hit
// a blank line or EOF.
type sseState struct {
	currentEvent string
	currentData  string
	// toolInputBuf accumulates partial_json fragments for each
	// tool_use content block until the matching content_block_stop.
	// Keyed by block index. The query loop's canonical delta stream
	// doesn't need the assembled JSON (it streams each fragment as
	// a Delta event), but the parser uses this internally to handle
	// input_json_delta ordering quirks if they ever arise.
	toolInputBuf map[int]string
}

func newSSEState() *sseState {
	return &sseState{toolInputBuf: map[int]string{}}
}

// parseSSE reads the Anthropic SSE response body and emits
// canonical StreamEvents. Anthropic's SSE differs from OpenAI's:
//
//   - Each event carries both an `event:` name AND a `data:`
//     payload. We switch on the event name and unmarshal the data
//     into the appropriate event-specific shape.
//
//   - Order is fixed: message_start → one-or-more
//     (content_block_start, content_block_delta*, content_block_stop)
//     → message_delta → message_stop. Pings can appear anywhere.
//     Errors can appear anywhere and terminate the stream.
//
//   - The token counts in `usage` on `message_delta` are cumulative.
//     We forward them through to the canonical EventMessageDelta's
//     Usage field for downstream accounting.
//
// Reference:
// https://platform.claude.com/docs/en/build-with-claude/streaming
func parseSSE(ctx context.Context, body io.Reader, events chan<- api.StreamEvent, errs chan<- error, model string) {
	reader := bufio.NewReaderSize(body, 64*1024)
	state := newSSEState()

	for {
		select {
		case <-ctx.Done():
			emitError(events, errs, core.Wrap(core.KindCancelled, ctx.Err(), "stream cancelled"))
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				// A healthy stream ends with a blank line
				// followed by EOF after message_stop. If we
				// have a pending event, flush it before
				// returning.
				state.flush(events)
				return
			}
			if ctx.Err() != nil {
				emitError(events, errs, core.Wrap(core.KindCancelled, ctx.Err(), "read cancelled"))
				return
			}
			emitError(events, errs, core.Wrap(core.KindHTTP, err, "read sse"))
			return
		}

		// Trim trailing \r\n or \n.
		line = bytes.TrimRight(line, "\r\n")

		// Blank line = end of current event. Dispatch.
		if len(line) == 0 {
			state.dispatch(events, errs, model)
			continue
		}

		// Comment line — ignore per SSE spec.
		if line[0] == ':' {
			continue
		}

		// Field lines: "field: value" or "field:value".
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			// Unknown line — skip.
			continue
		}
		field := string(line[:colon])
		value := string(line[colon+1:])
		// Strip a single leading space (per SSE spec).
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}

		switch field {
		case "event":
			state.currentEvent = value
		case "data":
			state.currentData = value
		case "id", "retry":
			// Ignored.
		default:
			// Unknown field — skip.
		}
	}
}

// flush handles a trailing event when the stream ends without a
// terminating blank line. Anthropic's stream is always
// well-formed (every event ends with a blank line), but the
// parser is robust to sloppy servers that omit the terminator.
func (s *sseState) flush(events chan<- api.StreamEvent) {
	if s.currentEvent == "" && s.currentData == "" {
		return
	}
	// No err channel here because the SSE-parser invocation in
	// parseSSE is at end-of-stream — there's no further caller
	// to receive. Errors here would indicate a malformed event
	// from a healthy stream.
	noopErrs := make(chan<- error, 1)
	s.dispatch(events, noopErrs, "")
}

// dispatch emits the canonical event for the accumulated SSE
// event, then resets the state. Called when we hit a blank line
// (end of one event) or at end-of-stream.
func (s *sseState) dispatch(events chan<- api.StreamEvent, errs chan<- error, model string) {
	if s.currentEvent == "" && s.currentData == "" {
		return
	}
	ev := s.currentEvent
	data := s.currentData
	s.currentEvent = ""
	s.currentData = ""

	switch ev {
	case "message_start":
		s.handleMessageStart(data, events)
	case "content_block_start":
		s.handleContentBlockStart(data, events)
	case "content_block_delta":
		s.handleContentBlockDelta(data, events)
	case "content_block_stop":
		s.handleContentBlockStop(data, events)
	case "message_delta":
		s.handleMessageDelta(data, events, errs)
	case "message_stop":
		events <- api.EventOfMessageStop()
	case "ping":
		events <- api.EventOfPing()
	case "error":
		s.handleStreamError(data, events, errs, model)
	default:
		// Unknown event type — ignored per Anthropic's
		// versioning policy ("your code should handle
		// unknown event types gracefully").
	}
}

// handleMessageStart parses the message_start event and emits a
// canonical EventMessageStart. The wire payload is:
//
//	{
//	  "type": "message_start",
//	  "message": {
//	    "id": "msg_...",
//	    "type": "message",
//	    "role": "assistant",
//	    "model": "...",
//	    "stop_reason": null,
//	    "usage": {"input_tokens": N, "output_tokens": N, ...}
//	  }
//	}
//
// We only consume `model` (forwarded to EventMessageStart.Model)
// and `usage` (forwarded as *core.UsageInfo). Other message
// fields are not surfaced today.
func (s *sseState) handleMessageStart(data string, events chan<- api.StreamEvent) {
	var msg struct {
		Message struct {
			Model string          `json:"model"`
			Usage *core.UsageInfo `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(data), &msg); err != nil {
		return
	}
	events <- api.EventOfMessageStart(msg.Message.Model, msg.Message.Usage)
}

// handleContentBlockStart parses the content_block_start event
// and emits a canonical EventContentBlockStart. Three block types
// arrive at start time:
//
//   - "text"     → empty text block
//   - "thinking" → empty thinking block (we carry through the
//     signature if Anthropic included one in the start frame, but
//     it almost always arrives via signature_delta just before
//     content_block_stop instead)
//   - "tool_use" → {id, name, input: {}} — the partial JSON
//     input accumulates via input_json_delta events
//
// We ignore other block types (server_tool_use, web_search_tool_result,
// etc.) by emitting a text-shaped placeholder; downstream code
// doesn't currently understand them, but the stream proceeds.
func (s *sseState) handleContentBlockStart(data string, events chan<- api.StreamEvent) {
	var ev struct {
		Index        int             `json:"index"`
		ContentBlock json.RawMessage `json:"content_block"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return
	}

	// Inspect the content_block's `type` to know which canonical
	// block to emit. We do this by re-parsing just the type
	// field — cheaper than decoding into a discriminated union.
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(ev.ContentBlock, &probe); err != nil {
		return
	}

	switch probe.Type {
	case "text":
		var tb struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.ContentBlock, &tb)
		events <- api.EventOfBlockStart(ev.Index, core.ContentBlock{
			Kind: core.BlockText,
			Text: tb.Text,
		})
	case "thinking":
		var tb struct {
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}
		_ = json.Unmarshal(ev.ContentBlock, &tb)
		events <- api.EventOfBlockStart(ev.Index, core.ContentBlock{
			Kind: core.BlockThinking,
			Thinking: &core.Thinking{
				Text:      tb.Thinking,
				Signature: tb.Signature,
			},
		})
	case "tool_use":
		var tb struct {
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		_ = json.Unmarshal(ev.ContentBlock, &tb)
		// We default Input to {} when Anthropic sends an empty
		// object (which it always does at start time). The
		// actual arguments arrive via subsequent input_json_delta
		// events which the canonical Delta stream consumes.
		events <- api.EventOfBlockStart(ev.Index, core.ContentBlock{
			Kind: core.BlockToolUse,
			ToolUse: &core.ToolUse{
				ID:    tb.ID,
				Name:  tb.Name,
				Input: tb.Input,
			},
		})
	default:
		// Unknown block type (server_tool_use, web_search_tool_result,
		// future types). Emit a text block with the raw type so
		// the consumer can see something; downstream code ignores
		// text blocks whose content doesn't match a known kind.
		events <- api.EventOfBlockStart(ev.Index, core.ContentBlock{
			Kind: core.BlockText,
			Text: fmt.Sprintf("[unsupported block type: %s]", probe.Type),
		})
	}

	// Reset the input buffer for this index.
	delete(s.toolInputBuf, ev.Index)
}

// handleContentBlockDelta parses a content_block_delta event and
// emits a canonical EventContentBlockDelta. Four delta types are
// recognized:
//
//   - text_delta       → text fragment
//   - input_json_delta → partial JSON for a tool_use's input
//   - thinking_delta   → reasoning fragment
//   - signature_delta  → opaque signature blob for a thinking block
func (s *sseState) handleContentBlockDelta(data string, events chan<- api.StreamEvent) {
	var ev struct {
		Index int `json:"index"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
		} `json:"delta"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return
	}
	switch ev.Delta.Type {
	case "text_delta":
		events <- api.EventOfBlockDelta(ev.Index, api.TextDelta(ev.Delta.Text))
	case "input_json_delta":
		// Accumulate (debug aid; not required downstream).
		s.toolInputBuf[ev.Index] += ev.Delta.PartialJSON
		events <- api.EventOfBlockDelta(ev.Index, api.ToolInputJSONDelta(ev.Delta.PartialJSON))
	case "thinking_delta":
		events <- api.EventOfBlockDelta(ev.Index, api.ThinkingDelta(ev.Delta.Thinking))
	case "signature_delta":
		// Anthropic emits a signature_delta on the thinking
		// block just before content_block_stop. It's an opaque
		// blob that MUST be passed back unchanged on the next
		// request to preserve the reasoning thread — never
		// user-visible. Route it as a SignatureDelta so the
		// query loop's accumulator writes it into the
		// Thinking.Signature field rather than the visible
		// text stream.
		events <- api.EventOfBlockDelta(ev.Index, api.SignatureDelta(ev.Delta.Signature))
	default:
		// Unknown delta type — ignored per Anthropic's
		// versioning policy.
	}
}

// handleContentBlockStop parses content_block_stop. The wire
// payload is just {type, index}; the canonical event carries no
// additional fields.
func (s *sseState) handleContentBlockStop(data string, events chan<- api.StreamEvent) {
	var ev struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return
	}
	events <- api.EventOfBlockStop(ev.Index)
	// Drop the input buffer for this index — the next tool_use
	// at a different index gets a fresh one.
	delete(s.toolInputBuf, ev.Index)
}

// handleMessageDelta parses the message_delta event and emits a
// canonical EventMessageDelta. The payload is:
//
//	{
//	  "type": "message_delta",
//	  "delta": {"stop_reason": "...", "stop_sequence": null},
//	  "usage": {"output_tokens": N, ...}
//	}
//
// The token counts are cumulative for the message (output_tokens
// grows as content blocks stream); we forward them as-is to
// the canonical Usage.
//
// stop_reason values from Anthropic and their canonical mapping:
//
//	"end_turn"                     → StopEndTurn
//	"tool_use"                     → StopToolUse
//	"max_tokens"                   → StopMaxTokens
//	"stop_sequence"                → StopStopSequence
//	"refusal"                      → StopEndTurn (clean stop)
//	"model_context_window_exceeded"→ StopContextLimit
//	"pause_turn"                   → StopEndTurn (long-running,
//	                                   we treat pause as a normal end —
//	                                   resume is a future feature)
//	"" (empty / null)              → "" (don't override)
func (s *sseState) handleMessageDelta(data string, events chan<- api.StreamEvent, errs chan<- error) {
	var ev struct {
		Delta struct {
			StopReason   string `json:"stop_reason"`
			StopSequence string `json:"stop_sequence"`
		} `json:"delta"`
		Usage *core.UsageInfo `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return
	}
	stop := mapStopReason(ev.Delta.StopReason)
	events <- api.EventOfMessageDelta(stop, ev.Usage)
}

// handleStreamError parses an in-stream error event and emits a
// canonical EventError. The wire payload is:
//
//	{
//	  "type": "error",
//	  "error": {"type": "overloaded_error", "message": "..."}
//	}
//
// The error "type" is informational; we use it only to disambiguate
// overloaded_error (529-equivalent). The message is surfaced as
// the canonical Error.Message.
func (s *sseState) handleStreamError(data string, events chan<- api.StreamEvent, errs chan<- error, model string) {
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		emitError(events, errs, core.Newf(core.KindAPI, "anthropic stream error (unparseable): %s", data))
		return
	}
	msg := env.Error.Message
	if msg == "" {
		msg = fmt.Sprintf("anthropic stream error: %s", env.Error.Type)
	}
	kind := core.KindAPI
	switch env.Error.Type {
	case "overloaded_error":
		kind = core.KindHTTPStatus
	case "rate_limit_error":
		kind = core.KindRateLimit
	case "authentication_error", "permission_error":
		kind = core.KindAuth
	case "invalid_request_error", "not_found_error":
		kind = core.KindAPI
	}
	e := core.Newf(kind, "%s", msg).WithDetail("anthropic_error_type", env.Error.Type)
	emitError(events, errs, e)
}

// mapStopReason normalizes Anthropic's stop_reason vocabulary into
// the canonical set used by query. Unknown values pass through
// (the query loop's switch will treat them as "unknown" and surface
// a generic end_turn — preserving the data for debugging).
func mapStopReason(s string) string {
	switch s {
	case "end_turn":
		return api.StopEndTurn
	case "tool_use":
		return api.StopToolUse
	case "max_tokens":
		return api.StopMaxTokens
	case "stop_sequence":
		return api.StopStopSequence
	case "refusal":
		return api.StopEndTurn
	case "model_context_window_exceeded":
		return api.StopContextLimit
	case "pause_turn":
		return api.StopEndTurn
	case "":
		return ""
	default:
		return s
	}
}
