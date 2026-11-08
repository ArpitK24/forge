package query

import (
	"context"
	"fmt"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// ContextProvider is the query loop's hook for the context engine
// (design doc §4, "Hook points"). It is deliberately small and uses
// only core types, so the engine (internal/ctxengine) never imports
// this package or internal/tools.
//
// Config.Context is nil by default, and a nil provider leaves the loop
// exactly as it was: the system prompt is sent unchanged and nothing
// is observed.
type ContextProvider interface {
	// Assemble returns the context block for the next request,
	// built from the conversation so far (the user's request plus
	// every tool_use and tool_result). "" adds nothing. An error is
	// reported as a status event and the turn proceeds without
	// context; the engine can never fail a turn.
	Assemble(ctx context.Context, messages []core.Message) (string, error)
	// Observe is called after every executed tool call with the
	// call and its result text. The engine uses it to reindex
	// files the agent edited before the next turn.
	Observe(ctx context.Context, call core.ToolUse, result string, isError bool)
}

// assembleContext appends the provider's block to the system text.
// Panics and errors in the provider degrade to "no context".
func assembleContext(ctx context.Context, p ContextProvider, messages []core.Message, system string, eventCh chan<- Event) (out string) {
	out = system
	defer func() {
		if r := recover(); r != nil {
			out = system
			sendEvent(eventCh, StatusEvent{Message: fmt.Sprintf("context engine: panic: %v", r)})
		}
	}()
	block, err := p.Assemble(ctx, messages)
	if err != nil {
		sendEvent(eventCh, StatusEvent{Message: "context engine: " + err.Error()})
		return system
	}
	if block == "" {
		return system
	}
	if system == "" {
		return block
	}
	return system + "\n\n" + block
}

func observeTool(ctx context.Context, p ContextProvider, tu core.ToolUse, res tools.ToolResult) {
	defer func() { _ = recover() }()
	p.Observe(ctx, tu, res.Text, res.IsError)
}
