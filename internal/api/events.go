package api

import (
	"encoding/json"
	"fmt"

	"github.com/ArpitK24/forge/internal/core"
)

// StreamEvent is the canonical, provider-agnostic streaming event
// the query loop and the TUI consume. Per spec §4.2, the set
// covers message start, content-block start/delta/stop, message
// delta (stop reason + usage), message stop, and error.
//
// We model this as a tagged union: a single struct with a Kind
// discriminator and kind-specific fields, like the ContentBlock
// shape in core. The String() method renders a one-line JSON
// representation suitable for the --output-format=stream-json
// NDJSON output.
type StreamEvent struct {
	// Kind is the event-type discriminator. Always set.
	Kind EventKind

	// MessageStart fields (Kind == EventMessageStart).
	Model string
	Usage *core.UsageInfo

	// ContentBlockStart fields (Kind == EventContentBlockStart).
	// Index is the block's position in the assistant message.
	// Block is the first-frame content (Text, Thinking, or
	// ToolUse-with-id+name; the tool-use *input* is delivered
	// via subsequent deltas, not the start event).
	Index int
	Block core.ContentBlock

	// ContentBlockDelta fields (Kind == EventContentBlockDelta).
	// Delta is a TextDelta, ThinkingDelta, or ToolInputJSONDelta.
	Delta Delta

	// ContentBlockStop fields (Kind == EventContentBlockStop).
	// Index is the position of the closing block.

	// MessageDelta fields (Kind == EventMessageDelta).
	// StopReason is the canonical stop reason — "end_turn",
	// "tool_use", "max_tokens", "stop_sequence", "context_limit",
	// "cancelled", or "error". Adapters normalize vendor-specific
	// finish_reasons into this small set.
	StopReason string

	// ErrorEvent fields (Kind == EventError).
	Err *core.Error
}

// EventKind enumerates the StreamEvent subtypes.
type EventKind int

const (
	// EventMessageStart is the first event of every stream.
	// Carries the model id and (when known) the input-token count.
	EventMessageStart EventKind = iota
	// EventContentBlockStart signals a new content block is
	// beginning at Index. The block's id/name (for tool_use)
	// or first text fragment is on the event.
	EventContentBlockStart
	// EventContentBlockDelta is a chunk of content for the
	// block at Index. Delta is a TextDelta, ThinkingDelta,
	// or ToolInputJSONDelta.
	EventContentBlockDelta
	// EventContentBlockStop closes the block at Index.
	EventContentBlockStop
	// EventMessageDelta is the per-message delta: stop reason
	// and final usage. Sent exactly once per stream, just
	// before EventMessageStop.
	EventMessageDelta
	// EventMessageStop is the last event of every successful
	// stream. After this, the provider closes the channel.
	EventMessageStop
	// EventPing is a keep-alive some providers send. We surface
	// it but most consumers ignore it.
	EventPing
	// EventError terminates the stream with a failure. The
	// provider MUST close both channels after an error event.
	EventError
)

// String returns a stable wire-format name. Used in NDJSON output.
func (k EventKind) String() string {
	switch k {
	case EventMessageStart:
		return "message_start"
	case EventContentBlockStart:
		return "content_block_start"
	case EventContentBlockDelta:
		return "content_block_delta"
	case EventContentBlockStop:
		return "content_block_stop"
	case EventMessageDelta:
		return "message_delta"
	case EventMessageStop:
		return "message_stop"
	case EventPing:
		return "ping"
	case EventError:
		return "error"
	default:
		return fmt.Sprintf("event-%d", int(k))
	}
}

// Delta is the sub-type of a content-block delta event. The Kind
// discriminator tells you which of Text / Thinking / ToolInputJSON
// to read; the other fields are zero.
type Delta struct {
	// Kind is the delta-type discriminator.
	Kind DeltaKind

	// Text is populated when Kind == DeltaText.
	Text string
	// Thinking is populated when Kind == DeltaThinking.
	Thinking string
	// PartialJSON is populated when Kind == DeltaToolInputJSON.
	// It's a JSON fragment that the consumer should append to
	// the in-progress tool-input string for the same block.
	PartialJSON string
}

// DeltaKind enumerates delta sub-types.
type DeltaKind int

const (
	// DeltaText is a chunk of text content.
	DeltaText DeltaKind = iota
	// DeltaThinking is a chunk of extended-reasoning text.
	DeltaThinking
	// DeltaToolInputJSON is a partial JSON fragment of a
	// tool_use's input. NIM streams these.
	DeltaToolInputJSON
)

// String returns the stable wire-format name.
func (k DeltaKind) String() string {
	switch k {
	case DeltaText:
		return "text_delta"
	case DeltaThinking:
		return "thinking_delta"
	case DeltaToolInputJSON:
		return "input_json_delta"
	default:
		return fmt.Sprintf("delta-%d", int(k))
	}
}

// Constructors. These exist so callers (adapters, FakeProvider)
// don't have to remember the struct literal shape; the field
// groupings are easy to get wrong by hand.

func EventOfMessageStart(model string, usage *core.UsageInfo) StreamEvent {
	return StreamEvent{Kind: EventMessageStart, Model: model, Usage: usage}
}

func EventOfBlockStart(index int, block core.ContentBlock) StreamEvent {
	return StreamEvent{Kind: EventContentBlockStart, Index: index, Block: block}
}

func EventOfBlockDelta(index int, delta Delta) StreamEvent {
	return StreamEvent{Kind: EventContentBlockDelta, Index: index, Delta: delta}
}

func EventOfBlockStop(index int) StreamEvent {
	return StreamEvent{Kind: EventContentBlockStop, Index: index}
}

func EventOfMessageDelta(stopReason string, usage *core.UsageInfo) StreamEvent {
	return StreamEvent{Kind: EventMessageDelta, StopReason: stopReason, Usage: usage}
}

func EventOfMessageStop() StreamEvent {
	return StreamEvent{Kind: EventMessageStop}
}

func EventOfPing() StreamEvent {
	return StreamEvent{Kind: EventPing}
}

func EventOfError(err *core.Error) StreamEvent {
	return StreamEvent{Kind: EventError, Err: err}
}

// TextDelta constructs a TextDelta Delta.
func TextDelta(text string) Delta {
	return Delta{Kind: DeltaText, Text: text}
}

// ThinkingDelta constructs a ThinkingDelta Delta.
func ThinkingDelta(text string) Delta {
	return Delta{Kind: DeltaThinking, Thinking: text}
}

// ToolInputJSONDelta constructs a ToolInputJSONDelta Delta.
func ToolInputJSONDelta(partial string) Delta {
	return Delta{Kind: DeltaToolInputJSON, PartialJSON: partial}
}

// streamEventJSON is the NDJSON shape for StreamJson output mode
// (spec §7.3). Each event renders as one line of JSON with the
// "type" field set to the event's kind name and the kind-specific
// fields flattened into the top level. This is the shape the
// stream-json output format prints — external consumers that
// want to drive Forge as a subprocess parse this.
//
// We use a custom struct shape (not just marshal StreamEvent)
// because the public StreamEvent uses a non-JSON-friendly
// union-with-pointer style, and the canonical NDJSON shape is
// the public, stable API for downstream parsers.
type streamEventJSON struct {
	Type        string          `json:"type"`
	Model       string          `json:"model,omitempty"`
	Index       int             `json:"index,omitempty"`
	StopReason  string          `json:"stop_reason,omitempty"`
	Text        string          `json:"text,omitempty"`
	Thinking    string          `json:"thinking,omitempty"`
	PartialJSON string          `json:"partial_json,omitempty"`
	Block       *core.ContentBlock `json:"block,omitempty"`
	Usage       *core.UsageInfo `json:"usage,omitempty"`
	Error       *core.Error     `json:"error,omitempty"`
}

// JSON returns the event as one NDJSON-friendly JSON object. The
// caller is responsible for appending a trailing newline.
//
// This is the shape --output-format=stream-json emits.
func (e StreamEvent) JSON() ([]byte, error) {
	out := streamEventJSON{
		Type:        e.Kind.String(),
		Model:       e.Model,
		Index:       e.Index,
		StopReason:  e.StopReason,
		Text:        e.Delta.Text,
		Thinking:    e.Delta.Thinking,
		PartialJSON: e.Delta.PartialJSON,
	}
	if e.Block.Kind != 0 { // only populated on a content_block_start
		b := e.Block
		out.Block = &b
	}
	if e.Usage != nil {
		u := *e.Usage
		out.Usage = &u
	}
	if e.Err != nil {
		errVal := *e.Err
		out.Error = &errVal
	}
	return json.Marshal(out)
}

// String returns a compact one-line rendering of the event. The
// stream-json output mode uses JSON() instead, but String() is
// useful for debug logs.
func (e StreamEvent) String() string {
	data, err := e.JSON()
	if err != nil {
		return fmt.Sprintf("%s: <marshal error: %v>", e.Kind, err)
	}
	return string(data)
}

// Stop-reason constants. The set is the union of what every
// supported provider can emit, normalized here so the query
// loop can branch on a single small set.
const (
	// StopEndTurn means the model finished naturally.
	StopEndTurn = "end_turn"
	// StopToolUse means the model wants to call one or more
	// tools before continuing.
	StopToolUse = "tool_use"
	// StopMaxTokens means the response hit the max_tokens cap.
	StopMaxTokens = "max_tokens"
	// StopStopSequence means the model hit a stop_sequence.
	StopStopSequence = "stop_sequence"
	// StopContextLimit means the model's context window is full.
	StopContextLimit = "context_limit"
	// StopCancelled means the user cancelled mid-stream.
	StopCancelled = "cancelled"
	// StopError is the catch-all stop reason for a stream
	// terminated by an error event.
	StopError = "error"
)
