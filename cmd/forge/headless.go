package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/api/openai"
	"github.com/ArpitK24/forge/internal/cli"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/query"
	"github.com/ArpitK24/forge/internal/tools"
)

// runHeadless is the real headless one-shot mode. It
// assembles the system prompt, resolves the API key, builds
// the provider + tool context, drives the query loop, and
// drains events into the configured output format (text /
// json / stream-json). Ctrl+C cancels the in-flight stream
// via a signal-driven context.
//
// The function is the Phase 2 replacement for the Phase 1
// runHeadlessPlaceholder.
func runHeadless(a *cli.Args, cfg *core.Config, logger *slog.Logger) error {
	// 1. Resolve the API key. Order:
	//      a) --api-key flag
	//      b) FORGE_API_KEY env
	//      c) NVIDIA_API_KEY env (NIM)
	//      d) OPENAI_API_KEY env (any OpenAI-compatible endpoint)
	// The key is NEVER logged.
	apiKey, keySrc := resolveAPIKey(a.APIKey)
	if apiKey == "" {
		fmt.Fprintln(os.Stderr,
			"forge: no API key found. Set one of:\n"+
				"  --api-key <key>\n"+
				"  FORGE_API_KEY=<key>\n"+
				"  NVIDIA_API_KEY=<key>     (NIM)\n"+
				"  OPENAI_API_KEY=<key>     (any OpenAI-compatible endpoint)\n"+
				"Then run again.")
		return exitError(2)
	}
	logger.Debug("api key resolved", "source", keySrc)

	// 2. Resolve the API base. Order:
	//      a) --api-base flag (added in Phase 2)
	//      b) FORGE_API_BASE env
	//      c) core.DefaultAPIBase (NIM)
	apiBase := resolveAPIBase(a, cfg)

	// 3. Build the system prompt.
	cwd := resolveCwd(cfg.WorkingDir)
	systemPrompt := core.BuildSystemPrompt(cfg, cwd)

	// 4. Build the provider.
	model := cfg.EffectiveModel()
	provider := openai.New(apiBase, apiKey, model)

	// 5. Build the tool list.
	toolsList := tools.AllTools()

	// 6. Build the per-call ToolContext.
	perm := &core.AutoPermissionHandler{Mode: cfg.PermissionMode}
	tc := &tools.ToolContext{
		WorkingDir: cwd,
		Permission: perm,
		Cfg:        cfg,
		NonInteractive: true,
		CheckPermission: func(name, desc string, ro bool) core.PermissionDecision {
			return perm.RequestPermission(core.PermissionRequest{
				ToolName: name, Description: desc, IsReadOnly: ro,
			})
		},
	}

	// 7. Build the cancelable context.
	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 8. Resolve the first user message from the prompt
	// argument or stdin (already read into parsed.PositionalPrompt
	// by main()).
	prompt := strings.TrimSpace(a.PositionalPrompt)
	if prompt == "" {
		return fmt.Errorf("no prompt provided; pass one as a positional argument or pipe via stdin")
	}

	// 9. Build the message history and start the loop.
	messages := []core.Message{core.NewUserText(prompt)}
	cost := core.NewCostTracker()
	events := make(chan query.Event, 256)

	// Drain events in a goroutine based on the output format.
	// The cost tracker is threaded into the json and
	// stream-json renderers so they can emit the cost
	// summary as part of the final document — no stray
	// stderr line that would pollute a programmatic
	// consumer's stdout.
	out := os.Stdout
	renderer, err := newOutputRenderer(cfg.OutputFormat, out, cost, logger)
	if err != nil {
		return err
	}
	rendererDone := make(chan struct{})
	go func() {
		defer close(rendererDone)
		renderer.run(events)
	}()

	// 10. Run the loop with retry on transient errors.
	//
	// RunQueryLoop returns OutcomeError on provider failures
	// (network errors, 5xx, 429, etc.). For headless mode we
	// retry the whole loop on retryable errors — honoring
	// the provider's Retry-After header when present, with
	// exponential backoff and jitter as a floor. Non-retryable
	// errors (auth, context-limit, 4xx other than 429) return
	// immediately.
	outcome := runWithRetry(ctx, provider, messages, toolsList, tc, query.Config{
		Model:              model,
		MaxTokens:          cfg.EffectiveMaxTokens(),
		SystemPrompt:       systemPrompt,
		AppendSystemPrompt: cfg.AppendSystemPrompt,
		MaxTurns:           cfg.EffectiveMaxTurns(),
	}, cost, events, logger)

	// 11. Close the events channel so the renderer drains and
	// returns, then wait for it.
	close(events)
	<-rendererDone

	// 12. Print the cost summary on stderr ONLY in text
	// mode. The json and stream-json renderers already
	// emitted the cost as part of their final document;
	// printing it again on stderr would corrupt those
	// consumers' parsing.
	if cfg.OutputFormat == core.OutputText {
		fmt.Fprintf(os.Stderr, "\n%s\n", cost.Summary())
	}

	// 13. Map outcome to exit code.
	switch outcome.Kind {
	case query.OutcomeEndTurn, query.OutcomeMaxTokens:
		return nil
	case query.OutcomeCancelled:
		// Cancelled is not an error — exit 0 (the user pressed
		// Ctrl+C; that is a normal end of session).
		return nil
	case query.OutcomeError:
		if outcome.Err != nil {
			fmt.Fprintln(os.Stderr, "forge error:", outcome.Err)
		}
		return exitError(1)
	}
	return nil
}

// resolveAPIKey returns the first non-empty key from the
// precedence list, and a string describing the source (for
// debug logging; the key itself is never logged).
func resolveAPIKey(flag string) (string, string) {
	if flag != "" {
		return flag, "flag"
	}
	if v := os.Getenv("FORGE_API_KEY"); v != "" {
		return v, "FORGE_API_KEY"
	}
	if v := os.Getenv("NVIDIA_API_KEY"); v != "" {
		return v, "NVIDIA_API_KEY"
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		return v, "OPENAI_API_KEY"
	}
	return "", ""
}

// resolveAPIBase returns the first non-empty base URL from the
// precedence list, defaulting to NIM.
func resolveAPIBase(a *cli.Args, cfg *core.Config) string {
	if a != nil {
		if v := a.APIBase; v != "" {
			return v
		}
	}
	if v := os.Getenv("FORGE_API_BASE"); v != "" {
		return v
	}
	if cfg != nil && cfg.APIBase != "" {
		return cfg.APIBase
	}
	return core.DefaultAPIBase
}

// outputRenderer drains query.Event into one of three output
// formats: text (default), json, stream-json. Created by
// newOutputRenderer; the renderer's lifetime is one
// RunQueryLoop call.
type outputRenderer interface {
	// run consumes the events channel until it closes, then
	// returns. The caller closes the channel after the loop
	// terminates; run is the drain side.
	run(events <-chan query.Event)
}

// newOutputRenderer constructs the right renderer for the
// requested output format. Phase 2 supports text, json, and
// stream-json.
//
// The cost tracker is passed to the json and stream-json
// renderers so they can include the cost summary in their
// final document; the text renderer does not need it (it
// prints to stderr from the caller).
func newOutputRenderer(format core.OutputFormat, out io.Writer, cost *core.CostTracker, logger *slog.Logger) (outputRenderer, error) {
	switch format {
	case core.OutputText:
		return &textRenderer{w: out, logger: logger}, nil
	case core.OutputJson:
		return &jsonRenderer{w: out, cost: cost}, nil
	case core.OutputStreamJson:
		return &streamJSONRenderer{w: out, cost: cost}, nil
	default:
		// Unknown / zero value: default to text.
		return &textRenderer{w: out, logger: logger}, nil
	}
}

// textRenderer writes the final assistant text to stdout and
// prints a one-line "tool call: NAME" annotation for each
// ToolStartEvent so the user has some feedback that work is
// happening.
type textRenderer struct {
	w      io.Writer
	logger *slog.Logger
}

func (r *textRenderer) run(events <-chan query.Event) {
	var buf strings.Builder
	for ev := range events {
		switch e := ev.(type) {
		case query.ToolStartEvent:
			fmt.Fprintf(r.w, "[tool call: %s]\n", e.Name)
		case query.ToolEndEvent:
			if e.IsError {
				fmt.Fprintf(r.w, "[tool error: %s]\n", e.Name)
			}
		case query.StatusEvent:
			fmt.Fprintf(r.w, "[%s]\n", e.Message)
		case query.StreamEventForward:
			// We only render the text deltas; ignore the
			// other event kinds.
			if e.Event.Kind == api.EventContentBlockDelta &&
				e.Event.Delta.Kind == api.DeltaText {
				buf.WriteString(e.Event.Delta.Text)
			}
		case query.ErrorEvent:
			r.logger.Error("loop error event", "err", e.Err)
		case query.TurnCompleteEvent:
			// No-op for text mode.
		}
	}
	// Flush the buffered text once at the end. The deltas
	// have arrived in real time but we buffer so the line
	// ordering is stable and we don't write a half-line if
	// the loop is cancelled.
	io.WriteString(r.w, buf.String())
}

// jsonRenderer buffers the entire conversation and prints one
// final JSON object with the assistant text, the tool calls,
// and the usage. Useful for programmatic consumers that
// prefer a single document to NDJSON.
type jsonRenderer struct {
	w    io.Writer
	cost *core.CostTracker
}

type jsonOutput struct {
	Text     string                  `json:"text"`
	ToolUses []jsonToolUse           `json:"tool_uses,omitempty"`
	Usage    *core.UsageInfo         `json:"usage,omitempty"`
	Turns    int                     `json:"turns"`
	Outcome  string                  `json:"outcome"`
	Error    string                  `json:"error,omitempty"`
	Cost     string                  `json:"cost,omitempty"`
}

type jsonToolUse struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Result string `json:"result,omitempty"`
	Error  bool   `json:"error,omitempty"`
}

func (r *jsonRenderer) run(events <-chan query.Event) {
	var (
		text        strings.Builder
		toolUses    []jsonToolUse
		// outcome is filled in when the loop emits its
		// terminal OutcomeEvent. Until then, we report a
		// generic end_turn so the document is well-formed
		// even if the stream is closed before the event
		// arrives (which can happen on a malformed provider
		// that closes the channel without a final event).
		outcomeKind = "end_turn"
		outcomeErr  string
		usage       core.UsageInfo
		turns       int
	)
	for ev := range events {
		switch e := ev.(type) {
		case query.ToolStartEvent:
			toolUses = append(toolUses, jsonToolUse{Name: e.Name, ID: e.ID})
		case query.ToolEndEvent:
			// Update the last matching tool use.
			for i := len(toolUses) - 1; i >= 0; i-- {
				if toolUses[i].ID == e.ID {
					toolUses[i].Result = e.Result
					toolUses[i].Error = e.IsError
					break
				}
			}
		case query.StreamEventForward:
			if e.Event.Kind == api.EventContentBlockDelta &&
				e.Event.Delta.Kind == api.DeltaText {
				text.WriteString(e.Event.Delta.Text)
			}
		case query.OutcomeEvent:
			outcomeKind = e.Kind.String()
			usage = e.Usage
			turns = e.Turns
			if e.Err != nil {
				outcomeErr = e.Err.Error()
			}
		}
	}
	out := jsonOutput{
		Text:     text.String(),
		ToolUses: toolUses,
		Usage:    &usage,
		Turns:    turns,
		Outcome:  outcomeKind,
		Error:    outcomeErr,
	}
	if r.cost != nil {
		// CostTracker.Summary() returns a human-readable
		// line ("input: 100, output: 50, $0.0012"). For
		// programmatic consumers, that's enough — they
		// can also use Usage to compute their own
		// estimate.
		out.Cost = r.cost.Summary()
	}
	_ = json.NewEncoder(r.w).Encode(out)
}

// streamJSONRenderer prints every StreamEventForward as one
// NDJSON line. The query loop already shapes the events;
// this renderer just encodes them. Consumers parse one JSON
// object per line.
type streamJSONRenderer struct {
	w    io.Writer
	cost *core.CostTracker
}

func (r *streamJSONRenderer) run(events <-chan query.Event) {
	enc := json.NewEncoder(r.w)
	for ev := range events {
		switch e := ev.(type) {
		case query.StreamEventForward:
			_ = enc.Encode(struct {
				Type string `json:"type"`
				Event api.StreamEvent `json:"event"`
			}{
				Type: "stream_event",
				Event: e.Event,
			})
		case query.ToolStartEvent:
			_ = enc.Encode(struct {
				Type string `json:"type"`
				query.ToolStartEvent
			}{Type: "tool_start", ToolStartEvent: e})
		case query.ToolEndEvent:
			_ = enc.Encode(struct {
				Type string `json:"type"`
				query.ToolEndEvent
			}{Type: "tool_end", ToolEndEvent: e})
		case query.TurnCompleteEvent:
			_ = enc.Encode(struct {
				Type string `json:"type"`
				query.TurnCompleteEvent
			}{Type: "turn_complete", TurnCompleteEvent: e})
		case query.StatusEvent:
			_ = enc.Encode(struct {
				Type string `json:"type"`
				query.StatusEvent
			}{Type: "status", StatusEvent: e})
		case query.ErrorEvent:
			_ = enc.Encode(struct {
				Type string `json:"type"`
				Err  *core.Error `json:"error"`
			}{Type: "error", Err: e.Err})
		case query.OutcomeEvent:
			_ = enc.Encode(struct {
				Type string `json:"type"`
				Kind string `json:"kind"`
				Usage core.UsageInfo `json:"usage"`
				Turns int `json:"turns"`
				Err  *core.Error `json:"error,omitempty"`
			}{
				Type:  "outcome",
				Kind:  e.Kind.String(),
				Usage: e.Usage,
				Turns: e.Turns,
				Err:   e.Err,
			})
		}
	}
	// Emit a final `cost` event with the session's cost
	// summary so programmatic consumers don't have to
	// scrape stderr to see the totals.
	if r.cost != nil {
		_ = enc.Encode(struct {
			Type string `json:"type"`
			Cost string `json:"cost"`
		}{Type: "cost", Cost: r.cost.Summary()})
	}
	// Signal end of stream so consumers can know we're done.
	_ = enc.Encode(struct {
		Type string `json:"type"`
		Time time.Time `json:"time"`
	}{Type: "end", Time: time.Now()})
}

// --- retry on transient errors (Phase 2 hardening) ---

// headlessRetryMax is the cap on consecutive retries. After
// this many transient failures we surface OutcomeError so
// the caller can exit non-zero rather than spinning.
const headlessRetryMax = 4

// runWithRetry wraps query.RunQueryLoop with a small retry
// loop. On OutcomeError, it inspects the error: if it's
// retryable (KindRateLimit, HTTP 429, HTTP 529) the loop is
// re-run from the same starting messages; otherwise the
// error is returned immediately.
//
// The retry honors the provider's Retry-After header (in
// seconds) when present, falling back to exponential
// backoff with a small jitter floor.
//
// The events channel is shared across attempts — the loop
// emits ToolStart/End, TurnComplete, and the terminal
// OutcomeEvent on it. The renderer's drain runs once
// after the final attempt; for retried attempts the
// renderer's pre-existing drain goroutine may have already
// observed an OutcomeEvent, but the renderer is OK with
// multiple OutcomeEvents (it overwrites with the last one).
func runWithRetry(
	ctx context.Context,
	provider api.Provider,
	messages []core.Message,
	toolsList []tools.Tool,
	toolCtx *tools.ToolContext,
	cfg query.Config,
	cost *core.CostTracker,
	events chan<- query.Event,
	logger *slog.Logger,
) query.Outcome {
	var last query.Outcome
	for attempt := 0; attempt < headlessRetryMax; attempt++ {
		last = query.RunQueryLoop(ctx, provider, messages, toolsList, toolCtx, cfg, cost, events)
		if last.Kind != query.OutcomeError {
			return last
		}
		// Inspect the error. Outcome.Err is already
		// *core.Error; nil-receiver methods (e.g.
		// IsRetryable) are safe to call.
		if last.Err == nil || !last.Err.IsRetryable() {
			return last
		}
		// Compute the delay. If the provider told us
		// Retry-After, honor it (in seconds). Otherwise
		// exponential backoff: 1s, 2s, 4s, 8s …
		delay := time.Duration(0)
		if last.Err.RetryAfter > 0 {
			delay = time.Duration(last.Err.RetryAfter) * time.Second
		} else {
			// 1<<attempt seconds + 0–250ms jitter
			base := time.Duration(1<<attempt) * time.Second
			jitter := time.Duration(time.Now().UnixNano() % 250) * time.Millisecond
			delay = base + jitter
		}
		logger.Warn("transient error, retrying",
			"attempt", attempt+1,
			"max", headlessRetryMax,
			"delay", delay,
			"err_kind", last.Err.Kind,
		)
		// Reset the cost tracker for the next attempt so
		// the final summary reflects only the successful
		// attempt. Reset is exposed as a public method on
		// CostTracker; the loop's own AddUsage calls are
		// what we want to clear.
		if cost != nil {
			cost.Reset()
		}
		select {
		case <-ctx.Done():
			last.Kind = query.OutcomeCancelled
			return last
		case <-time.After(delay):
			// continue the retry loop
		}
	}
	// Exhausted retries: return the last OutcomeError.
	return last
}
