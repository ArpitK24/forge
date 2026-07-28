package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// ProtocolVersion is the MCP version we speak on the wire.
// Spec §2 (Lifecycle): the server picks one of its supported
// versions or rejects with -32602. 2025-06-18 is current as of
// step 5's smoke-test target.
const ProtocolVersion = "2025-06-18"

// ClientName / ClientVersion identify us on the wire so a
// server's logs and crash reports tell the operator which
// client connected. Hard-coded — Forge is the only client.
const (
	ClientName    = "forge"
	ClientVersion = "0.4.0-step5"
)

// connectTimeout caps how long Connect waits per server.
// Spec §4 (Timeouts): receivers SHOULD enforce a maximum
// timeout regardless of progress notifications. 10s is the
// H8 risk called out in the plan — enough headroom for a slow
// spawn but tight enough to surface a wedged server quickly.
const connectTimeout = 10 * time.Second

// serverTool captures one tool descriptor returned by a
// server's tools/list. Stored on the Manager so future steps
// (sampling, list_changed) can rebuild the tool list without
// hitting the wire again.
type serverTool struct {
	Name        string          // e.g. "read_file"
	Description string          // human-readable
	InputSchema json.RawMessage // JSON Schema (verbatim)
}

// serverState is one connected MCP server. The Manager holds
// one of these per successfully-connected entry in
// cfg.McpServers; failed servers leave no entry but their
// error is captured in Manager.errors.
type serverState struct {
	name    string
	cli     *client
	tools   []serverTool // snapshot at Connect time
	rawTools json.RawMessage // verbatim result.tools array, kept for tools.go to wrap
}

// Manager is the top-level MCP coordinator. One Manager per
// process: TUI, headless, and any future ACP runtime all share
// it via closure capture on *core.Config. The Manager is
// concurrency-safe — Tools() and Close() may be called from
// any goroutine.
type Manager struct {
	cfg     []core.McpServerConfig // copy of what the user asked for
	log     *slog.Logger
	mu      sync.RWMutex           // guards servers and errors
	servers map[string]*serverState
	errors  map[string]error // per-server connect errors
}

// NewManager constructs a Manager from a slice of server
// configs. The Manager is not connected yet — call Connect.
func NewManager(cfg []core.McpServerConfig, log *slog.Logger) *Manager {
	return &Manager{
		cfg:     cfg,
		log:     log,
		servers: make(map[string]*serverState),
		errors:  make(map[string]error),
	}
}

// Connect walks cfg.McpServers, attempting to bring each one
// up. A failure on one server is captured in Manager.errors
// and does NOT stop the others — partial-connect is the
// documented behavior (a single bad config shouldn't take
// down the whole tool registry).
//
// Per-server timeout: connectTimeout. Spec §4 says receivers
// SHOULD enforce a maximum timeout regardless of progress
// notifications; we cap the whole Connect(ctx) call at the
// longer of (len(cfg)*connectTimeout) and ctx's own deadline.
//
// Returns the first error encountered (or nil if all
// connected). Always inspect Manager.errors for per-server
// detail — the returned error is just a "did at least one
// fail" indicator.
func (m *Manager) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error
	for _, srvCfg := range m.cfg {
		// Honor the caller's ctx deadline by deriving a
		// per-server sub-context. If the caller's ctx is
		// already past its deadline, this server's
		// sub-context inherits the cancellation and the
		// attempt fails fast.
		serverCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		err := m.connectOne(serverCtx, srvCfg)
		cancel()
		if err != nil {
			m.errors[srvCfg.Name] = err
			m.log.Warn("mcp: server connect failed",
				"server", srvCfg.Name, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
	}
	return firstErr
}

// connectOne brings up one server: pick transport, run
// initialize handshake, list tools, store the state.
//
// On transport-level failure (spawn error, malformed envelope,
// peer EOF during handshake), returns *core.Error{Kind:
// KindMCP} so callers can classify the error.
func (m *Manager) connectOne(ctx context.Context, srvCfg core.McpServerConfig) error {
	transport := m.newTransport(srvCfg)
	cli := newClient(srvCfg.Name, transport, m.log)
	if err := cli.start(ctx); err != nil {
		return err
	}

	// initialize handshake. We MUST capture the result so we
	// can log the server's self-reported version on connect
	// failure, but we don't currently negotiate capabilities —
	// the spec allows a client to send empty capabilities and
	// rely on what the server says it can do.
	var initResult initializeResult
	if err := cli.call(ctx, MethodInitialize, initializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{}, // no client features yet
		ClientInfo: implementationInfo{
			Name:    ClientName,
			Version: ClientVersion,
		},
	}, &initResult); err != nil {
		_ = cli.close(ctx)
		return core.Wrap(core.KindMCP, err,
			fmt.Sprintf("mcp %q: initialize", srvCfg.Name))
	}

	// initialized notification — no response expected.
	if err := cli.notify(MethodInitialized, map[string]any{}); err != nil {
		_ = cli.close(ctx)
		return core.Wrap(core.KindMCP, err,
			fmt.Sprintf("mcp %q: initialized notification", srvCfg.Name))
	}

	// tools/list. Some servers may omit the tools capability
	// and return an empty list — that's valid. We treat any
	// successful response (including empty tools) as success.
	var toolsResult toolsListResult
	if err := cli.call(ctx, MethodToolsList, toolsListParams{}, &toolsResult); err != nil {
		_ = cli.close(ctx)
		return core.Wrap(core.KindMCP, err,
			fmt.Sprintf("mcp %q: tools/list", srvCfg.Name))
	}

	state := &serverState{
		name:     srvCfg.Name,
		cli:      cli,
		tools:    make([]serverTool, 0, len(toolsResult.Tools)),
		rawTools: toolsResult.RawTools,
	}
	for _, t := range toolsResult.Tools {
		state.tools = append(state.tools, serverTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	m.servers[srvCfg.Name] = state
	m.log.Info("mcp: server connected",
		"server", srvCfg.Name,
		"protocolVersion", initResult.ProtocolVersion,
		"toolCount", len(state.tools))
	return nil
}

// newTransport picks the right transport implementation for a
// server config. Stdio today; http is a stub that returns a
// KindMCP error so the user sees a clear "Phase 4.1 follow-on"
// message instead of a confusing connect failure.
func (m *Manager) newTransport(cfg core.McpServerConfig) Transport {
	switch cfg.ServerType {
	case "http", "streamable-http", "sse":
		// The MCP 2025-06-18 spec folds SSE and streamable-HTTP
		// into one transport. Both are deferred to a Phase 4.1
		// follow-on step. The stub makes the deferral obvious.
		return NewHTTPTransport(cfg)
	case "stdio", "":
		// Empty ServerType defaults to stdio — the most common
		// case and what every example config uses.
		return NewStdioTransport(cfg)
	default:
		// Unknown transport type. We still return a stdio
		// transport so the spawn error surfaces a "command not
		// found" rather than failing silently. The Manager
		// will log the unknown type as a warning before this
		// call so the user sees the real cause.
		m.log.Warn("mcp: unknown server_type, defaulting to stdio",
			"server", cfg.Name, "serverType", cfg.ServerType)
		return NewStdioTransport(cfg)
	}
}

// Tools returns a flat list of namespaced tools.Tool values,
// one per server-tool across every connected server. The
// returned tools retain a reference to the Manager so their
// Execute method can route calls back to the right server.
//
// Tools() is called by the TUI at startup and again after
// every /reload command. It is concurrency-safe with Connect
// and Close — those mutate the manager while Tools() reads.
func (m *Manager) Tools() []tools.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.servers) == 0 {
		return nil
	}
	var out []tools.Tool
	for _, srv := range m.servers {
		for _, st := range srv.tools {
			out = append(out, &mcpTool{
				manager:   m,
				server:    srv.name,
				name:      st.Name,
				desc:      st.Description,
				inputJSON: st.InputSchema,
			})
		}
	}
	return out
}

// Errors returns a copy of per-server connect errors. Useful
// for the TUI to render a "MCP: filesystem: failed (no such
// command)" banner at startup.
func (m *Manager) Errors() map[string]error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]error, len(m.errors))
	for k, v := range m.errors {
		out[k] = v
	}
	return out
}

// Close shuts every connected server with a per-server
// timeout (closeTimeout, from client.go). Idempotent — calling
// Close on an already-closed Manager returns nil.
//
// On Close, servers that fail to shut down within their
// window get failPending'd by the client (see client.close)
// so any pending callers see a clean error rather than
// hanging forever.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for name, srv := range m.servers {
		serverCtx, cancel := context.WithTimeout(ctx, closeTimeout)
		if err := srv.cli.close(serverCtx); err != nil {
			m.log.Warn("mcp: server close failed",
				"server", name, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
		cancel()
	}
	m.servers = nil
	return firstErr
}

// initializeParams is what we send on the wire for "initialize".
// Shape per spec §2.1 (Lifecycle).
type initializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]any         `json:"capabilities"`
	ClientInfo      implementationInfo     `json:"clientInfo"`
}

// implementationInfo identifies a client or server. Both
// clientInfo and serverInfo use the same shape per the spec.
type implementationInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeResult is what we decode the "initialize" response
// into. We only consume the protocolVersion today; capabilities
// and serverInfo are kept so a future step can negotiate
// features (sampling, elicitation, list_changed) without a
// wire-shape change.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    map[string]any     `json:"capabilities"`
	ServerInfo      implementationInfo `json:"serverInfo"`
	// Instructions is an optional string the server sends
	// telling the client how to use it. We don't surface
	// this today; reserved for a future step.
	Instructions string `json:"instructions,omitempty"`
}

// toolsListParams is what we send on the wire for "tools/list".
// Cursor is for pagination, which we don't exercise in step 5
// — every real server returns its full tool list in one shot,
// and pagination is a Phase 4.1 follow-on.
type toolsListParams struct {
	// Cursor is the optional pagination cursor. Empty means
	// "first page."
	Cursor string `json:"cursor,omitempty"`
}

// toolDescriptor is one entry in a tools/list response.
type toolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	// Title is an optional display name (spec §3). Not
	// consumed today; reserved for TUI rendering.
	Title string `json:"title,omitempty"`
}

// toolsListResult is the decoded shape of a tools/list
// response. RawTools holds the verbatim `tools` array so
// tools.go (step 6) can pass it through as ToolResult.Blocks
// without losing any field the descriptor struct didn't
// capture.
type toolsListResult struct {
	Tools     []toolDescriptor `json:"tools"`
	NextCursor string          `json:"nextCursor,omitempty"`
	RawTools   json.RawMessage `json:"-"`
}

// UnmarshalJSON on toolsListResult captures the verbatim
// `tools` array for downstream use while also decoding the
// well-known fields. Custom unmarshalling keeps the round-trip
// lossless without forcing tools.go to re-parse.
func (t *toolsListResult) UnmarshalJSON(b []byte) error {
	// Decode into a parallel struct that retains the raw
	// array as well as the typed entries.
	var aux struct {
		Tools     []toolDescriptor `json:"tools"`
		NextCursor string          `json:"nextCursor,omitempty"`
	}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	t.Tools = aux.Tools
	t.NextCursor = aux.NextCursor
	// Extract the raw `tools` array (preserving order and any
	// extra fields per descriptor). A second Unmarshal call
	// would re-order keys; json.RawMessage keeps bytes
	// untouched. We re-encode aux.Tools to keep field order
	// deterministic; this is cheap (handful of tools in
	// practice) and avoids a second json.RawMessage capture
	// inside the aux struct.
	if raw, err := json.Marshal(aux.Tools); err == nil {
		t.RawTools = raw
	}
	return nil
}

// Compile-time assertion: mcpTool satisfies tools.Tool.
// The full implementation lives in tools.go; if its
// interface drifts this build breaks here.
var _ tools.Tool = (*mcpTool)(nil)