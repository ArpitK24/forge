package mcp

import (
	"context"
	"encoding/json"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// McpToolPrefix and McpToolSeparator are the canonical
// namespacing tokens used in tool names surfaced to the
// model. A server named "filesystem" exposing a tool named
// "read_file" appears to the model as
// "mcp__filesystem__read_file".
//
// Exported (not just package-internal) because step 8 wires
// them into TUI display strings and step 7's MCP config
// loader validates names against this convention.
const (
	McpToolPrefix    = "mcp__"
	McpToolSeparator = "__"
)

// mcpTool is the tools.Tool shim that wraps one server-side
// tool. Its Name() returns the namespaced name; Execute()
// routes the call back through the Manager to the right
// server, decodes the response, and converts the MCP
// `content[]` array into a tools.ToolResult.
//
// Step 4 lands a working Execute that decodes a text-only
// response (the common case); step 6 (tools.go work) widens
// it to handle image / audio / embedded-resource blocks and
// populates ToolResult.Blocks. The two phases share this
// file — phase 4's stubs in Execute are explicitly marked so
// a reviewer can tell what's complete vs deferred.
type mcpTool struct {
	manager   *Manager
	server    string
	name      string
	desc      string
	inputJSON json.RawMessage
}

// Name returns the namespaced tool name
// (mcp__<server>__<tool>). This is the string the model sees
// in its tool list and the string it passes back when
// invoking.
func (t *mcpTool) Name() string {
	return McpToolPrefix + t.server + McpToolSeparator + t.name
}

// Description is the human-readable text the server gave us
// for this tool. MCP servers are required to provide one;
// an empty string is rare but not impossible.
func (t *mcpTool) Description() string { return t.desc }

// PermissionLevel classifies the tool's safety posture. MCP
// tools wrap subprocesses / network calls, so the safest
// default is PermExecute — the permission handler treats
// them like Bash. Future steps may let servers self-declare
// annotations like `readOnlyHint: true`, which would let us
// downgrade to PermReadOnly for trusted servers.
func (t *mcpTool) PermissionLevel() core.PermissionLevel {
	return core.PermExecute
}

// InputSchema returns the JSON Schema the server provided.
// We pass it through verbatim — the MCP spec is JSON Schema
// 2020-12, which is what the Anthropic / OpenAI / Ollama
// provider adapters already expect.
func (t *mcpTool) InputSchema() json.RawMessage {
	return t.inputJSON
}

// Execute dispatches a tools/call to the server, awaits the
// response, and converts the MCP `content[]` array into a
// tools.ToolResult.
//
// Errors:
//
//   - Server not in the Manager (e.g. Close() raced): a
//     ToolResult{IsError: true, Text:"..."}. The query loop
//     treats this the same as any other tool error.
//   - Transport-level failure (server died, ctx cancelled):
//     propagated from client.call as a *core.Error wrapped
//     in a ToolResult with IsError: true. We never panic —
//     the safeExecute wrapper in query/loop.go would catch
//     it but a defensive recover is cheap insurance.
//   - Tool-level failure (server returned isError: true):
//     ToolResult{IsError: true, Text: <first text block> or
//     empty string}. This is a normal success response from
//     JSON-RPC's perspective — the failure lives in the
//     payload, not the transport.
//
// Step 4 implements the text-only path. Step 6 adds:
//   - Multi-block handling (image / audio / embedded
//     resource blocks surfaced via ToolResult.Blocks).
//   - Metadata fields: block_count, non_text_blocks list.
//   - zero-text-blocks fallback (image-only result still
//     carries a non-empty Text summary).
func (t *mcpTool) Execute(ctx context.Context, input json.RawMessage, tc *tools.ToolContext) tools.ToolResult {
	srv := t.manager.lookup(t.server)
	if srv == nil {
		return tools.ToolResult{
			IsError: true,
			Text:    "mcp: server not connected: " + t.server,
		}
	}
	var result toolsCallResult
	callParams := toolsCallParams{
		Name:      t.name,
		Arguments: json.RawMessage(input),
	}
	if err := srv.cli.call(ctx, MethodToolsCall, callParams, &result); err != nil {
		// Transport-level failure. Wrap as a tool error so
		// the model sees the cause instead of the loop
		// crashing.
		return tools.ToolResult{
			IsError: true,
			Text:    "mcp: tools/call failed: " + err.Error(),
		}
	}
	return convertCallResult(result, t.name)
}

// lookup returns the serverState for a given name, or nil
// if the server isn't in the Manager. Used by Execute() to
// resolve the back-reference after Tools() returned.
func (m *Manager) lookup(name string) *serverState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.servers[name]
}

// toolsCallParams is the wire shape for "tools/call" requests.
// Arguments is the raw JSON the model produced — wrapped in
// json.RawMessage so an Arguments of `null` (some servers
// accept tools with no arguments) passes through unchanged.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// toolResultBlock is one entry in an MCP tools/call result's
// content array. Only the fields we consume today are typed;
// extra fields per block are dropped (image, audio, resource,
// etc. land in step 6 via the Raw field).
type toolResultBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Raw is the verbatim block JSON. Captured for step 6 so
	// ToolResult.Blocks can preserve image/audio data without
	// us having to type every block variant today.
	Raw json.RawMessage `json:"-"`
}

// toolsCallResult is what the server returns for a tools/call.
// Spec §3 (Tools): a result has content[] (multi-block) and
// optional isError. We capture RawContent so step 6 can pass
// it through as ToolResult.Blocks without losing anything.
type toolsCallResult struct {
	Content    []toolResultBlock `json:"content"`
	IsError    bool              `json:"isError"`
	RawContent json.RawMessage   `json:"-"`
}

// UnmarshalJSON keeps a verbatim copy of the content[] array
// for downstream use while decoding the typed entries. Mirrors
// toolsListResult.UnmarshalJSON above.
func (r *toolsCallResult) UnmarshalJSON(b []byte) error {
	var aux struct {
		Content []toolResultBlock `json:"content"`
		IsError bool              `json:"isError"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	r.Content = aux.Content
	r.IsError = aux.IsError
	if raw, err := json.Marshal(aux.Content); err == nil {
		r.RawContent = raw
	}
	return nil
}

// convertCallResult is the text-only conversion path. Step 6
// replaces this with a multi-block aware version that
// populates ToolResult.Blocks.
//
// Current behavior:
//
//   - result.IsError: ToolResult.IsError = true; Text =
//     concatenation of all text blocks (or empty).
//   - Otherwise: ToolResult.IsError = false; Text =
//     concatenation of all text blocks (or empty).
//
// This intentionally drops non-text blocks. A real-world
// server returning [text("ok"), image(png)] would lose the
// PNG until step 6 lands — but no end-to-end smoke test
// exercises the multi-block path until then, so the silent
// drop is acceptable in this intermediate state.
func convertCallResult(r toolsCallResult, toolName string) tools.ToolResult {
	var text string
	for _, b := range r.Content {
		if b.Type == "text" {
			if text != "" {
				text += "\n"
			}
			text += b.Text
		}
	}
	if r.IsError && text == "" {
		text = "mcp tool " + toolName + " returned isError"
	}
	return tools.ToolResult{
		Text:    text,
		IsError: r.IsError,
	}
}