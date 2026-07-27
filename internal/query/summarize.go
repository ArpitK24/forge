package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// CompactSummaryPrompt is the system prompt used when asking
// the model to summarize a slice of the conversation. Spec §5.2
// says the summarizer is a "dedicated, non-tool-using API call";
// this is the prompt that call gets. The wording is stable:
// forge-1 ships with this exact phrasing, so any later prompt
// change is a deliberate, reviewable edit.
//
// Keep it short. The summary itself does the work; the prompt
// only sets the role and lists the categories worth preserving.
const CompactSummaryPrompt = "You are a context-compaction assistant. Summarize the conversation " +
	"below for a fresh agent that has not seen any of it. Preserve: " +
	"decisions made and their rationale, file paths touched, exact " +
	"command output that's still relevant (errors, version strings, " +
	"file listings), and any open questions. Drop: greetings, " +
	"repeated clarifications, and verbose tool output that's no " +
	"longer relevant. Write in compact prose, no preamble."

// SummarizeHead issues a single, non-streaming API call asking
// the model to summarize an older slice of the conversation.
// Spec §5.2's "summarise_head".
//
// Parameters:
//   - ctx: cancellation. A cancelled ctx aborts the underlying
//     HTTP request via the provider contract.
//   - client: the Provider to call. Same one the loop uses.
//   - summaryModel: the model id to invoke. In Step 1 this is
//     hardcoded to core.FastModel; a future --summary-model flag
//     will plumb it from config (out of scope here).
//   - head: the older slice of the conversation to summarize.
//     Empty head returns an empty string with no error (no API
//     call) — the caller can short-circuit.
//
// Returns the model's summary text, or an error wrapping the
// provider's failure mode. The summary is concatenated from the
// stream's text deltas; thinking / tool-use deltas are ignored
// because the summary call passes no tools and the model is
// instructed to write prose only.
//
// The call is single-shot and non-streaming from the consumer's
// point of view, but it does use the provider's Stream()
// contract (Phase 2's Provider surface is stream-only). The
// function drains the stream synchronously and returns when the
// provider closes its event channel.
func SummarizeHead(
	ctx context.Context,
	client api.Provider,
	summaryModel string,
	head []core.Message,
) (string, error) {
	if len(head) == 0 {
		return "", nil
	}
	req := api.Request{
		Model:    summaryModel,
		Messages: head,
		System:   api.SystemString(CompactSummaryPrompt),
		// No Tools — the summarizer must not call out to anything.
		// Per spec §5.2 this is a "dedicated, non-tool-using API
		// call".
		MaxTokens: 2048,
		Stream:    true,
	}
	events, errs := client.Stream(ctx, req)

	var sb strings.Builder
	var lastErr error
	for ev := range events {
		if ev.Kind == api.EventContentBlockDelta &&
			ev.Delta.Kind == api.DeltaText {
			sb.WriteString(ev.Delta.Text)
		}
		if ev.Kind == api.EventError && ev.Err != nil {
			lastErr = ev.Err
		}
	}
	// Drain any post-stream errors. A provider that closes the
	// events channel cleanly will have already closed errs.
	for err := range errs {
		if err != nil && lastErr == nil {
			lastErr = core.Wrap(core.KindAPI, err, "summarize provider")
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	if sb.Len() == 0 {
		return "", fmt.Errorf("summarize: empty response from model %q", summaryModel)
	}
	return sb.String(), nil
}
