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
	out := os.Stdout
	renderer, err := newOutputRenderer(cfg.OutputFormat, out, logger)
	if err != nil {
		return err
	}
	rendererDone := make(chan struct{})
	go func() {
		defer close(rendererDone)
		renderer.run(events)
	}()

	// 10. Run the loop.
	outcome := query.RunQueryLoop(ctx, provider, messages, toolsList, tc, query.Config{
		Model:              model,
		MaxTokens:          cfg.EffectiveMaxTokens(),
		SystemPrompt:       systemPrompt,
		AppendSystemPrompt: cfg.AppendSystemPrompt,
		MaxTurns:           effectiveMaxTurns(cfg),
	}, cost, events)

	// 11. Close the events channel so the renderer drains and
	// returns, then wait for it.
	close(events)
	<-rendererDone

	// 12. Print the cost summary on stderr.
	fmt.Fprintf(os.Stderr, "\n%s\n", cost.Summary())

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
func newOutputRenderer(format core.OutputFormat, out io.Writer, logger *slog.Logger) (outputRenderer, error) {
	switch format {
	case core.OutputText:
		return &textRenderer{w: out, logger: logger}, nil
	case core.OutputJson:
		return &jsonRenderer{w: out}, nil
	case core.OutputStreamJson:
		return &streamJSONRenderer{w: out}, nil
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
	w io.Writer
}

type jsonOutput struct {
	Text     string                  `json:"text"`
	ToolUses []jsonToolUse           `json:"tool_uses,omitempty"`
	Usage    *core.UsageInfo         `json:"usage,omitempty"`
	Turns    int                     `json:"turns"`
	Outcome  string                  `json:"outcome"`
}

type jsonToolUse struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Result string `json:"result,omitempty"`
	Error  bool   `json:"error,omitempty"`
}

func (r *jsonRenderer) run(events <-chan query.Event) {
	var (
		text     strings.Builder
		toolUses []jsonToolUse
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
		}
	}
	out := jsonOutput{Text: text.String(), ToolUses: toolUses, Outcome: "end_turn"}
	// We don't have Outcome/Usage here — they live on the
	// Outcome value, but the renderer doesn't see it. Phase
	// 4 will hoist these into the event stream so the json
	// renderer can report them.
	_ = json.NewEncoder(r.w).Encode(out)
}

// streamJSONRenderer prints every StreamEventForward as one
// NDJSON line. The query loop already shapes the events;
// this renderer just encodes them. Consumers parse one JSON
// object per line.
type streamJSONRenderer struct {
	w io.Writer
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
		}
	}
	// Signal end of stream so consumers can know we're done.
	_ = enc.Encode(struct {
		Type string `json:"type"`
		Time time.Time `json:"time"`
	}{Type: "end", Time: time.Now()})
}
