package query

import (
	"testing"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// TestAccumulatorSignatureDeltaRoutesToThinkingSignature verifies
// that a DeltaSignature on a thinking block's index lands in the
// block's Thinking.Signature field, never in the visible text
// stream. This is the end-to-end check for FIX 1: the canonical
// event from the Anthropic adapter gets folded into the right
// field on core.Thinking by the query loop's accumulator.
func TestAccumulatorSignatureDeltaRoutesToThinkingSignature(t *testing.T) {
	a := newAccumulator()

	// Apply a minimal thinking block sequence:
	//   message_start
	//   content_block_start (thinking) at index 0
	//   thinking_delta
	//   signature_delta
	//   content_block_stop
	//   message_delta (end_turn)
	//   message_stop
	events := []api.StreamEvent{
		api.EventOfMessageStart("claude-sonnet-4-5", &core.UsageInfo{InputTokens: 10, OutputTokens: 1}),
		api.EventOfBlockStart(0, core.ContentBlock{
			Kind:     core.BlockThinking,
			Thinking: &core.Thinking{Text: ""},
		}),
		api.EventOfBlockDelta(0, api.ThinkingDelta("reasoning...")),
		api.EventOfBlockDelta(0, api.SignatureDelta("sig-blob-xyz")),
		api.EventOfBlockStop(0),
		api.EventOfMessageDelta(api.StopEndTurn, &core.UsageInfo{OutputTokens: 2}),
		api.EventOfMessageStop(),
	}
	for _, e := range events {
		a.apply(e)
	}

	// The accumulated message must have a Thinking block whose
	// Signature is the wire value, AND the Thinking block's
	// Text must contain the reasoning we streamed.
	tbs := a.message.Content.ThinkingBlocks()
	if len(tbs) != 1 {
		t.Fatalf("ThinkingBlocks() = %d blocks, want 1; message=%+v", len(tbs), a.message)
	}
	if tbs[0].Text != "reasoning..." {
		t.Errorf("Thinking.Text = %q, want %q", tbs[0].Text, "reasoning...")
	}
	if tbs[0].Signature != "sig-blob-xyz" {
		t.Errorf("Thinking.Signature = %q, want %q", tbs[0].Signature, "sig-blob-xyz")
	}

	// No text block should exist, and no block should carry
	// the literal "[signature]" or any other text-delta residue.
	if text := a.message.Content.AllText(); text != "" {
		t.Errorf("visible text = %q, want empty (signature must not leak to text)", text)
	}
	if blocks := a.message.Content.Blocks; len(blocks) != 1 || blocks[0].Kind != core.BlockThinking {
		t.Errorf("blocks = %+v, want single BlockThinking", blocks)
	}
}

// TestAccumulatorSignatureDeltaLazyCreateBlock ensures the
// accumulator handles the (defensive) case where a signature_delta
// arrives before any thinking_delta — it should create a Thinking
// block rather than crashing or routing the signature into a
// text/tool-use block.
func TestAccumulatorSignatureDeltaLazyCreateBlock(t *testing.T) {
	a := newAccumulator()

	events := []api.StreamEvent{
		api.EventOfMessageStart("m", nil),
		api.EventOfBlockStart(0, core.ContentBlock{
			Kind:     core.BlockThinking,
			Thinking: &core.Thinking{},
		}),
		// signature arrives first (unusual but possible if a
		// future provider sends things in a different order).
		api.EventOfBlockDelta(0, api.SignatureDelta("sig-only")),
		api.EventOfBlockStop(0),
		api.EventOfMessageStop(),
	}
	for _, e := range events {
		a.apply(e)
	}

	tbs := a.message.Content.ThinkingBlocks()
	if len(tbs) != 1 {
		t.Fatalf("ThinkingBlocks = %d, want 1; message=%+v", len(tbs), a.message)
	}
	if tbs[0].Signature != "sig-only" {
		t.Errorf("Signature = %q, want %q", tbs[0].Signature, "sig-only")
	}
	if tbs[0].Text != "" {
		t.Errorf("Text = %q, want empty (no thinking_delta was applied)", tbs[0].Text)
	}
}
