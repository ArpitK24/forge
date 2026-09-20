package main

import (
	"context"
	"encoding/json"

	"github.com/ArpitK24/forge/internal/plugins"
)

// EchoInput is the input schema for the echo tool.
type EchoInput struct {
	Text string `json:"text"`
}

func main() {
	server := plugins.NewPluginServer("echo", "Echo tool plugin")
	server.RegisterTool(plugins.ToolDef{
		Name:        "echo",
		Description: "Echo back the input text",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}, func(ctx context.Context, input json.RawMessage) plugins.ToolResult {
		var in EchoInput
		if err := json.Unmarshal(input, &in); err != nil {
			return plugins.ToolResult{
				Text:    "invalid input: " + err.Error(),
				IsError: true,
			}
		}
		return plugins.ToolResult{
			Text:    in.Text,
			IsError: false,
		}
	})
	if err := server.Serve(); err != nil {
		panic(err)
	}
}