package query

import (
	"encoding/json"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// accumulator rebuilds the canonical core.Message from the
// stream of canonical StreamEvents. It also tracks per-stream
// usage and the final stop reason.
//
// The accumulator is a small stateful object used only inside
// RunQueryLoop. It's split out of loop.go so that file's flow
// stays readable.
type accumulator struct {
	// message is the in-progress assistant message. It's empty
	// until the first EventMessageStart is applied.
	message core.Message
	// blocks holds the content blocks keyed by their index in
	// the message. We use a map rather than a slice because
	// some providers emit block deltas out of order (rare,
	// but seen on Anthropic), and a map tolerates that.
	blocks map[int]core.ContentBlock
	// toolInputs is the partial-JSON accumulator for each
	// tool-use block. When the model emits a tool call, the
	// input arrives as a stream of partial-JSON strings that
	// we concatenate here. The block is materialized into a
	// ToolUse only at the end (EventContentBlockStop).
	toolInputs map[int][]byte
	// usage is the final per-stream usage (set on EventMessageDelta).
	usage core.UsageInfo
	// stopReason is the final stop reason (set on EventMessageDelta).
	stopReason string
}

func newAccumulator() *accumulator {
	return &accumulator{
		blocks:     make(map[int]core.ContentBlock),
		toolInputs: make(map[int][]byte),
	}
}

// apply processes a single StreamEvent, updating the
// accumulator's state. The events are expected in the order
// a real provider emits them, but apply is tolerant of
// out-of-order deltas.
func (a *accumulator) apply(ev api.StreamEvent) {
	switch ev.Kind {
	case api.EventMessageStart:
		a.message.Role = core.RoleAssistant
		if ev.Usage != nil {
			// Some providers (Anthropic) include input usage
			// in message_start; we save it.
			usage := *ev.Usage
			a.usage.InputTokens += usage.InputTokens
			a.usage.CacheCreationInputTokens += usage.CacheCreationInputTokens
			a.usage.CacheReadInputTokens += usage.CacheReadInputTokens
		}

	case api.EventContentBlockStart:
		// Initialize the block at this index. For text/thinking
		// blocks, we start with empty content. For tool-use
		// blocks, the block carries id+name+empty input.
		block := ev.Block
		if block.Kind == 0 {
			// Some adapters (NIM) don't emit a content_block_start
			// at all; the first delta opens the block. In that
			// case the first delta handler creates the block.
			return
		}
		a.blocks[ev.Index] = block

	case api.EventContentBlockDelta:
		a.applyDelta(ev.Index, ev.Delta)

	case api.EventContentBlockStop:
		// If this is a tool-use block, finalize its input
		// from the accumulated partial JSON.
		if block, ok := a.blocks[ev.Index]; ok {
			if block.Kind == core.BlockToolUse && block.ToolUse != nil {
				if buf, ok := a.toolInputs[ev.Index]; ok {
					// Validate that the partial JSON is a
					// complete object; if not, fall back to
					// empty input (the model produced malformed
					// JSON — let the tool report the error).
					if json.Valid(buf) {
						block.ToolUse.Input = json.RawMessage(append([]byte(nil), buf...))
					} else {
						block.ToolUse.Input = json.RawMessage(`{}`)
					}
				}
				a.blocks[ev.Index] = block
			}
		}

	case api.EventMessageDelta:
		if ev.StopReason != "" {
			a.stopReason = ev.StopReason
		}
		if ev.Usage != nil {
			usage := *ev.Usage
			a.usage.OutputTokens += usage.OutputTokens
		}

	case api.EventError, api.EventPing, api.EventMessageStop:
		// No state to update.
	}

	// Materialize blocks into the message at the end of every
	// event, in index order, so partial progress is visible if
	// the loop reads a.message mid-stream.
	a.rebuild()
}

func (a *accumulator) applyDelta(idx int, d api.Delta) {
	switch d.Kind {
	case api.DeltaText:
		// Get-or-create the text block at this index.
		block, ok := a.blocks[idx]
		if !ok || block.Kind != core.BlockText {
			block = core.TextBlock("")
		}
		block.Text += d.Text
		a.blocks[idx] = block

	case api.DeltaThinking:
		block, ok := a.blocks[idx]
		if !ok || block.Kind != core.BlockThinking {
			block = core.ThinkingBlock("", "")
		}
		if block.Thinking == nil {
			block.Thinking = &core.Thinking{}
		}
		block.Thinking.Text += d.Thinking
		a.blocks[idx] = block

	case api.DeltaToolInputJSON:
		// Append to the per-index partial-JSON buffer.
		buf, ok := a.toolInputs[idx]
		if !ok {
			// First delta for this block: ensure the block
			// exists as a tool-use stub. (The
			// EventContentBlockStart for tool-use should have
			// set this, but be defensive.)
			if block, exists := a.blocks[idx]; !exists || block.Kind != core.BlockToolUse {
				a.blocks[idx] = core.ContentBlock{
					Kind: core.BlockToolUse,
					ToolUse: &core.ToolUse{
						ID:    "",
						Name:  "",
						Input: json.RawMessage(`{}`),
					},
				}
			}
		}
		buf = append(buf, d.PartialJSON...)
		a.toolInputs[idx] = buf
	}
}

// rebuild copies the blocks map into the message's content
// in index order. Called after every event; cheap (a few
// blocks at most per turn).
func (a *accumulator) rebuild() {
	if len(a.blocks) == 0 {
		a.message.Content = core.StringContent("")
		return
	}
	// Find the max index so we know the slice size.
	maxIdx := 0
	for idx := range a.blocks {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	blocks := make([]core.ContentBlock, 0, maxIdx+1)
	for i := 0; i <= maxIdx; i++ {
		if b, ok := a.blocks[i]; ok {
			blocks = append(blocks, b)
		}
	}
	a.message.Content = core.BlocksContent(blocks...)
}
