package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
// content array. The typed fields (Type, Text) capture the
// shape we actively consume (text concatenation, error
// detection). The Raw field holds the original bytes for
// every block — preserved verbatim so multi-block results
// (text + image + audio + embedded resource) pass through
// without losing any field the typed struct doesn't capture.
type toolResultBlock struct {
	Type string          `json:"type"`
	Text string          `json:"text,omitempty"`
	Data string          `json:"data,omitempty"`       // base64-encoded media bytes (image / audio)
	MIMEType string      `json:"mimeType,omitempty"`   // MIME type for image / audio
	URI  string          `json:"uri,omitempty"`        // resource_link URI
	Name string          `json:"name,omitempty"`       // resource_link / embedded resource name
	Resource json.RawMessage `json:"resource,omitempty"` // embedded resource object
	// Raw holds the verbatim block bytes — set by UnmarshalJSON
	// from the input. Used by convertCallResult to populate
	// ToolResult.Blocks losslessly (preserves any extra fields
	// the typed struct above doesn't carry).
	Raw json.RawMessage `json:"-"`
}

// toolsCallResult is what the server returns for a tools/call.
// Spec §3 (Tools): a result has content[] (multi-block) and
// optional isError. RawContent holds the verbatim content[]
// array bytes — passed through to ToolResult.Blocks so the
// model sees the full multi-block structure on the wire.
type toolsCallResult struct {
	Content    []toolResultBlock `json:"content"`
	IsError    bool              `json:"isError"`
	RawContent json.RawMessage   `json:"-"`
}

// UnmarshalJSON captures the verbatim content[] array for
// downstream use while decoding the typed entries. Mirrors
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
	// Capture the verbatim content[] array. We have to
	// re-encode aux.Content (a []toolResultBlock) so the
	// model sees the typed fields in a stable shape; the
	// alternative would be to slice b directly with the
	// index of the "content" key, which is brittle across
	// whitespace differences.
	if raw, err := json.Marshal(aux.Content); err == nil {
		r.RawContent = raw
	}
	return nil
}

// UnmarshalJSON on toolResultBlock preserves the verbatim
// block bytes. Without this, Raw would be empty and any
// fields the typed struct above doesn't capture (e.g. a
// future "annotations" field) would be lost on the way
// to convertCallResult.
func (b *toolResultBlock) UnmarshalJSON(data []byte) error {
	type alias toolResultBlock // avoid recursion on Raw's omitempty
	if err := json.Unmarshal(data, (*alias)(b)); err != nil {
		return err
	}
	b.Raw = append([]byte(nil), data...)
	return nil
}

// convertCallResult turns an MCP tools/call response into
// a tools.ToolResult. The contract:
//
//   - Text: concatenation of every text block's text, joined
//     with "\n". If the server returned only non-text blocks
//     (image / audio / embedded resource), Text is a short
//     human-readable summary so the model still gets
//     feedback — empty Text + non-empty Blocks would force
//     the loop to substitute "(no output)".
//   - Blocks: the verbatim content[] array, when at least
//     one block was returned. The query loop's serializer
//     uses Blocks INSTEAD of Text when set — so the model
//     sees the full multi-block structure (text + image +
//     audio + embedded resource) without lossy re-encoding.
//   - Metadata: {"block_count": N, "non_text_blocks":
//     ["image", "audio", ...]}. These are diagnostic
//     fields the model can read to understand the shape of
//     what it just received — useful when Blocks is
//     non-empty and the tool summary in the TUI needs a
//     quick count.
//   - IsError: propagated from the server's isError field.
//     Per spec §3, tool-level failures (vs protocol-level
//     failures) are signaled via isError:true in a normal
//     JSON-RPC response.
//
// Edge cases handled:
//
//   - Zero blocks: Blocks stays nil (loop falls back to
//     Text-only serialization). A placeholder Text prevents
//     "(no output)" from appearing when the server
//     explicitly returned isError:true with no content.
//   - Non-text-only result: Text becomes "mcp tool X
//     returned N block(s): [block-types]" so the model
//     knows the structured content is in Blocks.
func convertCallResult(r toolsCallResult, toolName string) tools.ToolResult {
	var textParts []string
	nonText := []string{}
	for _, b := range r.Content {
		if b.Type == "text" {
			textParts = append(textParts, b.Text)
		} else if b.Type != "" {
			nonText = append(nonText, b.Type)
		}
	}
	text := strings.Join(textParts, "\n")

	// Build the metadata. block_count is the total; non_text_blocks
	// lists every non-text type that appeared. We don't bother
	// building this when there are no blocks (nil Blocks →
	// loop's Text-only branch — no metadata needed).
	var metadata map[string]any
	if len(r.Content) > 0 {
		metadata = map[string]any{
			"block_count":     len(r.Content),
			"non_text_blocks": nonText,
		}
	}

	// Populate Blocks only when the server actually
	// returned content. A response with isError:true and
	// no blocks still gets Text set below — it doesn't
	// need Blocks for the loop's serializer to work.
	// Gate on len(Content) rather than len(RawContent):
	// RawContent for an empty content array is the bytes
	// "[]" (non-zero length), but the loop's serializer
	// treats non-empty Blocks as "verbatim content[]" —
	// which would serialize as the literal "[]" instead
	// of letting the Text branch fire.
	var blocks json.RawMessage
	if len(r.Content) > 0 && len(r.RawContent) > 0 {
		blocks = r.RawContent
	}

	// IsError-with-no-text fallback: the model needs to
	// see SOMETHING. "(mcp tool X returned isError)" makes
	// it clear the tool errored without inventing details.
	if r.IsError && text == "" {
		if len(r.Content) > 0 {
			text = fmt.Sprintf("mcp tool %s returned %d block(s) with isError=true",
				toolName, len(r.Content))
		} else {
			text = "mcp tool " + toolName + " returned isError"
		}
	}

	// Non-text-only success fallback: when the server
	// returned only non-text blocks (e.g. just an image
	// with no caption), Text is empty. Without a fallback,
	// the TUI's ToolEnd event would surface "(no output)"
	// and the model would have no textual hint that the
	// tool actually returned something — it'd have to
	// notice via Blocks alone. Surface a one-line summary
	// so the user / model sees the tool succeeded.
	if !r.IsError && text == "" && len(r.Content) > 0 {
		text = fmt.Sprintf("mcp tool %s returned %d block(s): [%s]",
			toolName, len(r.Content), strings.Join(nonText, ", "))
	}

	return tools.ToolResult{
		Text:     text,
		IsError:  r.IsError,
		Metadata: metadata,
		Blocks:   blocks,
	}
}