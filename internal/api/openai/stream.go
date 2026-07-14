package openai

import (
	"encoding/json"
	"strings"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// sseState is the per-stream state of the SSE parser. It
// tracks in-progress tool calls (whose argument JSON is being
// accumulated across multiple chunks) and the final stop
// reason / usage as they arrive.
type sseState struct {
	// sawDone is true after the parser sees `data: [DONE]`.
	sawDone bool

	// toolCalls is the list of in-progress tool calls keyed by
	// their index in the OpenAI tool_calls array. The Arguments
	// string is accumulated from partial deltas.
	toolCalls map[int]*sseToolCall

	// emittedMessageStart is true after the parser has emitted
	// EventMessageStart. We emit it on the first content chunk
	// (or first tool-call chunk if there is no content).
	emittedMessageStart bool

	// emittedBlocks is the set of block indices we've already
	// emitted a ContentBlockStart for. We need this so the
	// accumulator in the query loop can pair starts with stops.
	emittedBlocks map[int]bool

	// currentBlockIndex is the index of the block currently
	// being emitted. The first content chunk opens block 0; a
	// subsequent tool call opens block 1; etc.
	currentBlockIndex int

	// textBlockIndex is the index of the assistant text block
	// (if any). -1 if the stream has not produced any text yet
	// and we haven't decided whether to open a text block.
	textBlockIndex int

	// finalStopReason is the stop reason from the last
	// finish_reason field we saw. Emitted as the MessageDelta's
	// StopReason.
	finalStopReason string

	// finalUsage is the usage chunk we may have received at the
	// end. Emitted as the MessageDelta's Usage.
	finalUsage *core.UsageInfo
}

// sseToolCall is one in-progress tool call being assembled
// from streaming chunks.
type sseToolCall struct {
	ID        string
	Name      string
	Arguments strings.Builder
	// blockIndex is the canonical content-block index we
	// assigned to this tool call in the EventContentBlockStart
	// (i.e. the position the accumulator will see). Set on
	// the first chunk that carries an id or name for this call.
	blockIndex int
	// started is true once we've emitted the
	// EventContentBlockStart for this call. We use it to
	// decide between emitting a fresh start on the first
	// chunk (which carries id+name) and just a delta on
	// later chunks.
	started bool
}

const sentinelNoTextBlock = -1

func newSSEState() *sseState {
	return &sseState{
		toolCalls:      make(map[int]*sseToolCall),
		emittedBlocks:  make(map[int]bool),
		textBlockIndex: sentinelNoTextBlock,
	}
}

// chunk is the OpenAI Chat Completions streaming chunk shape.
type chunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []chunkChoice  `json:"choices"`
	Usage   *chunkUsage    `json:"usage,omitempty"`
	Error   *chunkError    `json:"error,omitempty"`
}

// chunkChoice is one entry in a chunk's `choices` array.
type chunkChoice struct {
	Index        int         `json:"index"`
	Delta        chunkDelta  `json:"delta"`
	FinishReason string      `json:"finish_reason"`
}

// chunkDelta is the per-chunk delta payload.
type chunkDelta struct {
	Role      string        `json:"role,omitempty"`
	Content   string        `json:"content,omitempty"`
	ToolCalls []chunkTCDelta `json:"tool_calls,omitempty"`
}

// chunkTCDelta is one tool-call delta within a chunk.
type chunkTCDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function *struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

// chunkUsage is the final usage payload, if the provider
// includes it.
type chunkUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// chunkError is the error payload, if the provider sent one
// in-band (rare; usually it goes on the HTTP status).
type chunkError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// handleData processes one `data: ...` payload. It updates
// state and emits the right canonical events.
//
// The mapping rules:
//
//   - First chunk (role="assistant"): emit EventMessageStart.
//   - Content delta: emit a text block start (lazy, on first
//     content delta) and a text_delta.
//   - Tool call delta: emit a tool_use block start (lazy, on
//     first chunk that carries id+name) and an input_json_delta.
//   - Finish reason: emit a MessageDelta with the stop reason
//     and any final usage, then a MessageStop.
//
// Block indices are assigned in arrival order: text first
// (index 0) if any text arrives, then tool calls at index 1,
// 2, ... in the order they first appear.
func (s *sseState) handleData(payload string, events chan<- api.StreamEvent) {
	var c chunk
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		// Malformed chunk. We log nothing here (the caller
		// can wrap the StreamEvent in a future error event if
		// it wants); we just drop the chunk.
		return
	}

	// If the provider attached an error payload, surface it.
	if c.Error != nil {
		// We don't have an HTTP status to classify with;
		// treat as KindAPI.
		emitErrorInline(events, core.Newf(core.KindAPI, "provider error: %s", c.Error.Message))
		return
	}

	if len(c.Choices) == 0 {
		// Some providers send a usage-only final chunk with
		// no choices. Capture the usage if present.
		if c.Usage != nil {
			s.finalUsage = convertUsage(c.Usage)
		}
		return
	}

	choice := c.Choices[0]

	// Emit message start on the very first non-empty chunk.
	if !s.emittedMessageStart {
		s.emittedMessageStart = true
		// Some providers include prompt tokens in the first
		// chunk's usage. If we have any, attach them.
		events <- api.EventOfMessageStart(c.Model, s.finalUsage)
		s.finalUsage = nil // consumed
	}

	// Capture finish reason and usage from this chunk.
	if choice.FinishReason != "" {
		s.finalStopReason = mapFinishReason(choice.FinishReason)
	}
	if c.Usage != nil {
		s.finalUsage = convertUsage(c.Usage)
	}

	delta := choice.Delta

	// Text content delta.
	if delta.Content != "" {
		if s.textBlockIndex == sentinelNoTextBlock {
			// Open the text block.
			s.textBlockIndex = s.currentBlockIndex
			s.emittedBlocks[s.currentBlockIndex] = true
			s.currentBlockIndex++
			events <- api.EventOfBlockStart(s.textBlockIndex, core.TextBlock(""))
		}
		events <- api.EventOfBlockDelta(s.textBlockIndex, api.TextDelta(delta.Content))
	}

	// Tool call deltas.
	for _, tcd := range delta.ToolCalls {
		tc, ok := s.toolCalls[tcd.Index]
		if !ok {
			tc = &sseToolCall{}
			s.toolCalls[tcd.Index] = tc
		}
		// First chunk for this tool call carries id+name; emit
		// the content_block_start here.
		if !tc.started {
			if tcd.ID != "" {
				tc.ID = tcd.ID
			}
			if tcd.Function != nil && tcd.Function.Name != "" {
				tc.Name = tcd.Function.Name
			}
			tc.blockIndex = s.currentBlockIndex
			s.emittedBlocks[s.currentBlockIndex] = true
			s.currentBlockIndex++
			events <- api.EventOfBlockStart(tc.blockIndex, core.ContentBlock{
				Kind: core.BlockToolUse,
				ToolUse: &core.ToolUse{
					ID:    tc.ID,
					Name:  tc.Name,
					Input: []byte(`{}`),
				},
			})
			tc.started = true
		}
		// Accumulate the argument JSON and emit a delta.
		if tcd.Function != nil && tcd.Function.Arguments != "" {
			tc.Arguments.WriteString(tcd.Function.Arguments)
			events <- api.EventOfBlockDelta(tc.blockIndex, api.ToolInputJSONDelta(tcd.Function.Arguments))
		}
	}
}

// flush is called after `data: [DONE]` to close any open
// blocks (text + tool calls). The query loop's accumulator
// expects a ContentBlockStop before the MessageStop.
func (s *sseState) flush(events chan<- api.StreamEvent) {
	// Close any in-progress tool calls.
	for _, tc := range s.toolCalls {
		if !tc.started {
			continue
		}
		// Materialize the tool input. The accumulator in
		// query rebuilds the input from the partial deltas,
		// so we just emit a stop here.
		events <- api.EventOfBlockStop(tc.blockIndex)
	}
	// Close the text block if it was opened.
	if s.textBlockIndex != sentinelNoTextBlock {
		events <- api.EventOfBlockStop(s.textBlockIndex)
	}
	// Final message delta carries stop reason + usage.
	events <- api.EventOfMessageDelta(s.finalStopReason, s.finalUsage)
}

// mapFinishReason translates the OpenAI finish_reason into
// the canonical StopReason set used by the query loop.
func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return api.StopEndTurn
	case "tool_calls":
		return api.StopToolUse
	case "length":
		return api.StopMaxTokens
	case "content_filter":
		return api.StopError
	case "function_call":
		// Some older models used this; treat as tool_use.
		return api.StopToolUse
	default:
		return reason
	}
}

// convertUsage maps the OpenAI usage shape to core.UsageInfo.
func convertUsage(u *chunkUsage) *core.UsageInfo {
	if u == nil {
		return nil
	}
	return &core.UsageInfo{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
	}
}

// emitErrorInline emits a StreamEvent error and returns. It
// does NOT push to the errs channel — that channel is the
// Stream goroutine's responsibility, and the parser is called
// from inside that goroutine.
func emitErrorInline(events chan<- api.StreamEvent, e *core.Error) {
	select {
	case events <- api.EventOfError(e):
	default:
		// Buffer full: drop.
	}
}
