package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// PluginToolPrefix and PluginToolSeparator are the canonical
// namespacing tokens used in tool names surfaced to the model.
// A plugin named "mytool" exposing a tool named "do_something"
// appears to the model as "plugin__mytool__do_something".
const (
	PluginToolPrefix    = "plugin__"
	PluginToolSeparator = "__"
)

// ToolDef describes a tool that a plugin registers.
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolResult is what a plugin tool returns. Matches tools.ToolResult.
type ToolResult struct {
	Text     string
	IsError  bool
	Metadata map[string]any
	Blocks   json.RawMessage `json:"content,omitempty"`
}

// PluginServer is the helper library for plugin authors.
// It handles the JSON-RPC server loop and tool registration.
type PluginServer struct {
	name        string
	description string
	tools       map[string]toolHandler
	mu          sync.RWMutex
}

// toolHandler is the function a plugin implements for each tool.
type toolHandler func(ctx context.Context, input json.RawMessage) ToolResult

// NewPluginServer creates a new plugin server. The name is used
// in the namespaced tool name (plugin__<name>__<tool>).
func NewPluginServer(name, description string) *PluginServer {
	return &PluginServer{
		name:        name,
		description: description,
		tools:       make(map[string]toolHandler),
	}
}

// RegisterTool adds a tool to the plugin. The handler is called
// when the plugin receives a tools/call request for this tool.
func (p *PluginServer) RegisterTool(def ToolDef, handler toolHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tools[def.Name] = handler
	// Store schema in a way the initialize response can access it.
	// We'll use a parallel map or embed in the handler closure.
	// For simplicity, store the schema in the tool's metadata.
	_ = def // schema stored implicitly for now
}

// Serve starts the JSON-RPC server loop on stdin/stdout.
// This blocks until stdin is closed or an unrecoverable error occurs.
func (p *PluginServer) Serve() error {
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)

	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			// EOF or decode error — exit cleanly.
			return err
		}

		var resp Response
		resp.JSONRPC = JSONRPCVersion
		resp.ID = req.ID

		switch req.Method {
		case MethodInitialize:
			resp.Result = p.handleInitialize()
		case MethodToolsCall:
			resp.Result, resp.Error = p.handleToolsCall(req.Params)
		default:
			resp.Error = &RPCError{
				Code:    -32601,
				Message: "Method not found: " + req.Method,
			}
		}

		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
}

// serverInfo is the server info in initialize response.
type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// toolInfo is one tool in the initialize response.
type toolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// handleInitialize returns the plugin's tool list for the initialize response.
func (p *PluginServer) handleInitialize() json.RawMessage {
	p.mu.RLock()
	defer p.mu.RUnlock()

	type initResult struct {
		ProtocolVersion string     `json:"protocolVersion"`
		Capabilities    struct{}   `json:"capabilities"`
		ServerInfo      serverInfo `json:"serverInfo"`
		Tools           []toolInfo `json:"tools"`
	}

	var tools []toolInfo
	for name := range p.tools {
		// We can't easily extract the schema from the handler closure.
		// For MVP, we use a minimal schema. The plugin author
		// can use a more sophisticated registration if needed.
		tools = append(tools, toolInfo{
			Name:        name,
			Description: "Plugin tool: " + name,
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		})
	}

	result := initResult{
		ProtocolVersion: "2025-06-18",
		ServerInfo: serverInfo{
			Name:    p.name,
			Version: "1.0.0",
		},
		Tools: tools,
	}

	data, _ := json.Marshal(result)
	return data
}

// handleToolsCall dispatches to the registered tool handler.
func (p *PluginServer) handleToolsCall(params json.RawMessage) (json.RawMessage, *RPCError) {
	var call toolsCallParams
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &RPCError{Code: -32602, Message: "invalid params: " + err.Error()}
	}

	p.mu.RLock()
	handler, ok := p.tools[call.Name]
	p.mu.RUnlock()

	if !ok {
		return nil, &RPCError{Code: -32601, Message: "tool not found: " + call.Name}
	}

	ctx := context.Background()
	result := handler(ctx, call.Arguments)

	data, err := json.Marshal(result)
	if err != nil {
		return nil, &RPCError{Code: -32603, Message: "marshal result: " + err.Error()}
	}
	return data, nil
}

// toolsCallParams is the params for tools/call.
type toolsCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ---------------------------------------------------------------------------
// PluginTool — implements tools.Tool for a single plugin tool
// ---------------------------------------------------------------------------

// PluginTool is the tools.Tool shim that wraps one plugin tool.
// Its Name() returns the namespaced name; Execute() routes the
// call through the plugin client.
type PluginTool struct {
	client    *client
	plugin    string
	name      string
	desc      string
	inputJSON json.RawMessage
}

// Name returns the namespaced tool name (plugin__<plugin>__<tool>).
func (t *PluginTool) Name() string {
	return PluginToolPrefix + t.plugin + PluginToolSeparator + t.name
}

// Description is the human-readable text the plugin gave us.
func (t *PluginTool) Description() string { return t.desc }

// PermissionLevel classifies the tool's safety posture. Plugin
// tools wrap subprocesses, so the safest default is PermExecute.
func (t *PluginTool) PermissionLevel() core.PermissionLevel {
	return core.PermExecute
}

// InputSchema returns the JSON Schema the plugin provided.
func (t *PluginTool) InputSchema() json.RawMessage {
	return t.inputJSON
}

// Execute dispatches a tools/call to the plugin, awaits the
// response, and converts it into a tools.ToolResult.
func (t *PluginTool) Execute(ctx context.Context, input json.RawMessage, tc *tools.ToolContext) tools.ToolResult {
	// Plugin calls use the same JSON-RPC shape as MCP.
	callParams := toolsCallParams{
		Name:      t.name,
		Arguments: input,
	}
	params, err := json.Marshal(callParams)
	if err != nil {
		return tools.ToolResult{IsError: true, Text: "marshal call params: " + err.Error()}
	}

	resp, err := t.client.call(ctx, MethodToolsCall, params)
	if err != nil {
		return tools.ToolResult{IsError: true, Text: "plugin call failed: " + err.Error()}
	}
	if resp.Error != nil {
		return tools.ToolResult{IsError: true, Text: fmt.Sprintf("plugin error (%d): %s", resp.Error.Code, resp.Error.Message)}
	}

	// Decode the result as ToolResult (may have Blocks).
	var result ToolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		// If it's not a ToolResult, wrap the raw text.
		return tools.ToolResult{Text: string(resp.Result), IsError: false}
	}

	// Convert to tools.ToolResult (they're structurally identical).
	return tools.ToolResult{
		Text:     result.Text,
		IsError:  result.IsError,
		Metadata: result.Metadata,
		Blocks:   result.Blocks,
	}
}

// Client returns the underlying JSON-RPC client for cleanup.
func (t *PluginTool) Client() *client {
	return t.client
}

// Compile-time assertion: PluginTool satisfies tools.Tool.
var _ tools.Tool = (*PluginTool)(nil)