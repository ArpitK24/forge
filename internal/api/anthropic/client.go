package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
)

// DefaultAPIBase is the canonical Anthropic Messages endpoint host.
// The /v1/messages path is appended to it in Stream().
const DefaultAPIBase = "https://api.anthropic.com"

// APIVersion is the Anthropic API version header value. Pinned
// per spec §5.7: Anthropic versions are stable; 2023-06-01 is the
// version every Forge release targets.
const APIVersion = "2023-06-01"

// Client is an Anthropic Messages API client. It implements
// api.Provider by translating the canonical Request into
// Anthropic's wire format and forwarding the SSE response as
// canonical StreamEvents.
//
// One Client per (apiBase, model, apiKey) triple. Safe to use
// from a single goroutine (the query loop runs serially within
// a turn). Stream spawns a short-lived goroutine per call that
// drives the SSE reader and ends with the call.
type Client struct {
	// http is the underlying HTTP client. Tests inject one whose
	// Transport is an httptest.Server; production uses the
	// standard library's default transport.
	http *http.Client
	// apiKey is the value sent in the x-api-key header. For
	// Anthropic this is the user's Anthropic API key (or an
	// OAuth-derived token, but Phase 4 Step 3 ships static-key
	// auth only — device-code / OAuth lands in Step 10).
	apiKey string
	// apiBase is the Anthropic Messages endpoint base URL with
	// no trailing slash, e.g. "https://api.anthropic.com".
	// The /v1/messages path is appended in Stream().
	apiBase string
	// info is the static ModelInfo for this client. Returned
	// by Info() and used by downstream sizing / auto-compaction.
	info api.ModelInfo
}

// New constructs an Anthropic client.
//
// apiBase is the base URL with no trailing slash (e.g.
// "https://api.anthropic.com"). modelID is the Anthropic model id
// (e.g. "claude-sonnet-4-5"). apiKey is the x-api-key bearer.
func New(apiBase, apiKey, modelID string) *Client {
	return &Client{
		http:    &http.Client{Timeout: 0}, // no overall timeout; ctx controls the call
		apiKey:  apiKey,
		apiBase: strings.TrimRight(apiBase, "/"),
		info:    api.LookupModel(modelID),
	}
}

// NewWithHTTP is like New but allows injecting a custom
// *http.Client. Used by tests with httptest.Server.
func NewWithHTTP(httpClient *http.Client, apiBase, apiKey, modelID string) *Client {
	c := New(apiBase, apiKey, modelID)
	if httpClient != nil {
		c.http = httpClient
	}
	return c
}

// Info implements api.Provider.
func (c *Client) Info() api.ModelInfo { return c.info }

// ListModels implements api.ModelLister. It GETs Anthropic's
// /v1/models endpoint with the same x-api-key + anthropic-version
// headers Stream uses, parses the JSON {data: [{id, display_name,
// ...}]} list, and returns each entry as a ModelInfo with ID and
// Provider populated. Capability flags and ContextWindow are
// intentionally left zero — the canonical MergeWithKnown in
// internal/api/provider.go fills those from the bundled
// knownModels overlay when the caller asks for an enriched view.
//
// Non-2xx responses are classified with classifyHTTPError so
// 401/403/429/500 surface the same typed errors callers see
// from Stream. ctx cancellation is respected via the request
// context.
func (c *Client) ListModels(ctx context.Context) ([]api.ModelInfo, error) {
	url := c.apiBase + "/v1/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, core.Newf(core.KindHTTP, "new request: %v", err)
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", APIVersion)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, core.Wrap(core.KindCancelled, ctx.Err(), "request cancelled")
		}
		return nil, core.Wrap(core.KindHTTP, err, "http do")
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		return nil, classifyHTTPError(resp.StatusCode, resp.Header.Get("Retry-After"), string(bodyBytes))
	}

	var env struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, core.Wrap(core.KindAPI, err, "decode /v1/models")
	}
	out := make([]api.ModelInfo, 0, len(env.Data))
	for _, m := range env.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, api.ModelInfo{
			ID:       m.ID,
			Provider: core.ProviderAnthropic,
		})
	}
	return out, nil
}

// Stream implements api.Provider. POSTs a /v1/messages request
// and forwards the SSE response as canonical events. Channel
// contract matches the api.Provider documentation: events is
// closed after EventMessageStop or EventError; errs is closed
// after the goroutine that produced events has returned.
//
// ctx cancellation aborts the in-flight HTTP request via the
// request's context, and the SSE reader goroutine watches for
// ctx.Done() between reads.
func (c *Client) Stream(ctx context.Context, req api.Request) (<-chan api.StreamEvent, <-chan error) {
	events := make(chan api.StreamEvent, 32)
	errs := make(chan error, 2)

	go func() {
		defer close(events)
		defer close(errs)

		body, err := buildRequestBody(req)
		if err != nil {
			emitError(events, errs, core.Newf(core.KindAPI, "build request: %v", err))
			return
		}

		url := c.apiBase + "/v1/messages"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			emitError(events, errs, core.Newf(core.KindHTTP, "new request: %v", err))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("x-api-key", c.apiKey)
		httpReq.Header.Set("anthropic-version", APIVersion)

		resp, err := c.http.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				emitError(events, errs, core.Wrap(core.KindCancelled, ctx.Err(), "request cancelled"))
				return
			}
			emitError(events, errs, core.Wrap(core.KindHTTP, err, "http do"))
			return
		}

		// Map non-2xx responses to typed errors. Read the body
		// (limited) so the message can include the provider's
		// structured error text.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
			resp.Body.Close()
			emitError(events, errs, classifyHTTPError(resp.StatusCode, resp.Header.Get("Retry-After"), string(bodyBytes)))
			return
		}

		// 2xx — read the SSE stream.
		parseSSE(ctx, resp.Body, events, errs, req.Model)
	}()

	return events, errs
}

// buildRequestBody serializes the canonical Request into the
// Anthropic Messages wire format.
//
// Key translations:
//
//   - System prompt: top-level field, string-or-list-of-blocks.
//   - max_tokens: Anthropic requires it. If the caller didn't set
//     it, default to 4096. When thinking is enabled, max_tokens
//     must be strictly greater than budget_tokens — bump if needed.
//   - messages: alternating user/assistant. The query loop emits
//     tool-result blocks as user-message content blocks, which is
//     already the Anthropic shape; convertMessages adapts the
//     canonical MessageContent into Anthropic's content list.
//   - tools: flatter shape (name/description/input_schema at top
//     level, no function-wrapping).
//   - temperature / top_p: omitted when thinking is enabled
//     (Anthropic rejects them).
//   - thinking: emitted as {type:"enabled", budget_tokens:N} only
//     when the canonical ThinkingConfig.Enabled is true and
//     BudgetTokens > 0. The "adaptive" / "disabled" forms are
//     not currently emitted by Forge — they imply model-default
//     behavior that we don't yet know how to map.
func buildRequestBody(req api.Request) ([]byte, error) {
	wire := wireRequest{
		Model:     req.Model,
		Stream:    true,
		MaxTokens: resolveMaxTokens(req),
		Messages:  convertMessages(req.Messages),
	}

	// System prompt: top-level field.
	wire.System = convertSystem(req.System)

	// Sampling parameters — but skip when thinking is on
	// (Anthropic explicitly rejects temperature/top_p alongside
	// an enabled thinking config).
	thinkingEnabled := req.Thinking != nil && req.Thinking.Enabled && req.Thinking.BudgetTokens > 0
	if !thinkingEnabled {
		if req.Temperature != nil {
			wire.Temperature = req.Temperature
		}
		if req.TopP != nil {
			wire.TopP = req.TopP
		}
	}

	if len(req.StopSequences) > 0 {
		wire.StopSequences = req.StopSequences
	}

	if len(req.Tools) > 0 {
		wire.Tools = convertTools(req.Tools)
		wire.ToolChoice = wireToolChoice{Type: "auto"}
	}

	if thinkingEnabled {
		wire.Thinking = &wireThinking{
			Type:         "enabled",
			BudgetTokens: req.Thinking.BudgetTokens,
		}
	}

	return json.Marshal(wire)
}

// resolveMaxTokens returns the per-call max_tokens for the wire
// request. Anthropic requires this field. We default to 4096 when
// the caller didn't set it, and bump to budget+1024 when thinking
// is enabled and the caller's max_tokens would not strictly exceed
// the budget (Anthropic rejects budget_tokens >= max_tokens).
func resolveMaxTokens(req api.Request) int {
	max := req.MaxTokens
	if max <= 0 {
		max = 4096
	}
	if req.Thinking != nil && req.Thinking.Enabled && req.Thinking.BudgetTokens > 0 {
		if max <= req.Thinking.BudgetTokens {
			max = req.Thinking.BudgetTokens + 1024
		}
	}
	return max
}

// convertSystem maps the canonical SystemPrompt to Anthropic's
// `system` field shape. The string form is sent as a plain string;
// the list-of-blocks form is sent as a list with optional
// cache_control markers.
//
// Note: this adapter currently translates but does not itself
// emit cache_control markers. The phase 4 summarizer uses the
// string form; a future step that wants Anthropic prompt caching
// to kick in would compose SystemBlocks(...) on the request
// path, and the wire-translation here would carry the
// cache_control through unchanged.
func convertSystem(in api.SystemPrompt) any {
	if in.IsString {
		if in.String == "" {
			return nil
		}
		return in.String
	}
	if len(in.Blocks) == 0 {
		return nil
	}
	out := make([]wireSystemBlock, 0, len(in.Blocks))
	for _, b := range in.Blocks {
		wsb := wireSystemBlock{Type: "text", Text: b.Text}
		if b.CacheControl != nil && b.CacheControl.Type != "" {
			wsb.CacheControl = &wireCacheControl{Type: b.CacheControl.Type}
		}
		out = append(out, wsb)
	}
	return out
}

// convertMessages walks the canonical Message list and produces
// the Anthropic messages wire shape. The two interesting cases:
//
//  1. Assistant tool_use: Anthropic's assistant message content is
//     a list of {type:"text", text:"..."} and {type:"tool_use",
//     id, name, input} blocks. We emit one wireContentBlock per
//     text block + tool_use block.
//
//  2. User-side tool results: Anthropic has no separate tool
//     role. Tool results are nested inside the next user message
//     as {type:"tool_result", tool_use_id, content} blocks. The
//     query loop already groups tool-result blocks under a single
//     user message — we just translate each block.
func convertMessages(in []core.Message) []wireMessage {
	out := make([]wireMessage, 0, len(in))
	for _, m := range in {
		role := m.Role.String()
		if m.Role == core.RoleUser {
			role = "user"
		} else {
			role = "assistant"
		}

		if m.Role == core.RoleAssistant && m.HasToolUse() {
			wm := wireMessage{Role: "assistant"}
			var blocks []wireContentBlock
			for _, b := range m.Content.Blocks {
				wcb := wireContentBlock{}
				if b.CacheControl != nil && b.CacheControl.Type != "" {
					wcb.CacheControl = &wireCacheControl{Type: b.CacheControl.Type}
				}
				switch b.Kind {
				case core.BlockText:
					if b.Text != "" {
						wcb.Type = "text"
						wcb.Text = b.Text
						blocks = append(blocks, wcb)
					}
				case core.BlockToolUse:
					if b.ToolUse != nil {
						wcb.Type = "tool_use"
						wcb.ID = b.ToolUse.ID
						wcb.Name = b.ToolUse.Name
						wcb.Input = b.ToolUse.Input
						blocks = append(blocks, wcb)
					}
				case core.BlockThinking:
					if b.Thinking != nil {
						wcb.Type = "thinking"
						wcb.Thinking = b.Thinking.Text
						wcb.Signature = b.Thinking.Signature
						blocks = append(blocks, wcb)
					}
				}
			}
			wm.Content = blocks
			out = append(out, wm)
			continue
		}

		// User side. Build a content list that mixes text blocks
		// and tool_result blocks (Anthropic's native user-message
		// shape).
		if m.Role == core.RoleUser {
			var blocks []wireContentBlock
			if m.Content.IsString {
				if m.Content.String != "" {
					blocks = append(blocks, wireContentBlock{Type: "text", Text: m.Content.String})
				}
			} else {
				for _, b := range m.Content.Blocks {
					switch b.Kind {
					case core.BlockText:
						if b.Text != "" {
							blocks = append(blocks, wireContentBlock{Type: "text", Text: b.Text})
						}
					case core.BlockToolResult:
						if b.ToolResult != nil {
							blocks = append(blocks, wireContentBlock{
								Type:      "tool_result",
								ToolUseID: b.ToolResult.ToolUseID,
								Content:   b.ToolResult.Content,
								IsError:   b.ToolResult.IsError,
							})
						}
					}
				}
			}
			if len(blocks) > 0 {
				out = append(out, wireMessage{Role: "user", Content: blocks})
			}
			continue
		}

		// Plain text fallback (no special blocks).
		out = append(out, wireMessage{Role: role, Content: m.Content.GetFirstText()})
	}
	return out
}

// convertTools walks the canonical ToolDefinition list and produces
// Anthropic's flatter tool shape. The schema (input_schema) is
// passed through as raw JSON, and the canonical ToolDefinition's
// CacheControl marker is forwarded as Anthropic's nested
// `cache_control: {type: "ephemeral"}`.
func convertTools(in []core.ToolDefinition) []wireTool {
	out := make([]wireTool, 0, len(in))
	for _, t := range in {
		wt := wireTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
		if t.CacheControl != nil && t.CacheControl.Type != "" {
			wt.CacheControl = &wireCacheControl{Type: t.CacheControl.Type}
		}
		out = append(out, wt)
	}
	return out
}

// classifyHTTPError maps a non-2xx HTTP response to a typed
// core.Error. The rules:
//
//   - 401 / 403 → KindAuth
//   - 429 → KindRateLimit, with Retry-After parsed if present
//   - 529 → KindHTTPStatus, retryable via IsRetryable
//   - 400 / 413 / 422 / 500+ → KindHTTPStatus
//   - everything else → KindAPI
//
// Anthropic returns a structured error body
// ({type:"error", error:{type, message}}); we surface the inner
// message when present, else the raw body.
func classifyHTTPError(status int, retryAfterHeader, body string) *core.Error {
	msg := extractAnthropicErrorMessage(body, fmt.Sprintf("http %d: %s", status, body))
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return core.Newf(core.KindAuth, "authentication failed (%d): %s", status, msg).WithDetail("status", status)
	case status == http.StatusTooManyRequests:
		e := core.Newf(core.KindRateLimit, "rate limited (%d): %s", status, msg)
		if ra, err := parseRetryAfter(retryAfterHeader); err == nil {
			e.RetryAfter = ra
		}
		return e
	case status == 529:
		return core.Newf(core.KindHTTPStatus, "overloaded (529): %s", msg).WithDetail("status", status)
	case status == http.StatusBadRequest,
		status == http.StatusRequestEntityTooLarge,
		status == http.StatusUnprocessableEntity,
		status >= 500:
		return core.Newf(core.KindHTTPStatus, "http %d: %s", status, msg).WithDetail("status", status)
	default:
		return core.Newf(core.KindAPI, "http %d: %s", status, msg)
	}
}

// extractAnthropicErrorMessage pulls the human-readable message
// out of Anthropic's structured error envelope. If the body
// doesn't match the shape (older formats, plain text, etc.),
// it returns the fallback string verbatim.
func extractAnthropicErrorMessage(body, fallback string) string {
	if body == "" {
		return fallback
	}
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	return body
}

// parseRetryAfter parses an HTTP Retry-After header. Accepts
// either a number of seconds ("30") or an HTTP-date. Anything we
// can't parse returns an error so the caller can decide whether
// to default.
func parseRetryAfter(h string) (int, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, c := range h {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not numeric: %q", h)
		}
		n = n*10 + int(c-'0')
	}
	if n > 0 {
		return n, nil
	}
	return 0, fmt.Errorf("zero or unparseable: %q", h)
}

// emitError writes an EventError event AND sends the error on
// the errors channel. Mirrors openai/emitError — the query loop
// drains both channels, so we want both downstream consumers
// (event-watcher vs err-channel-watcher) to see the failure.
func emitError(events chan<- api.StreamEvent, errs chan<- error, e *core.Error) {
	select {
	case events <- api.EventOfError(e):
	default:
		// Buffer full: drop the event; the errs channel is
		// still authoritative.
	}
	errs <- e
}

// ---------------------------------------------------------------------------
// Wire types
// ---------------------------------------------------------------------------

// wireRequest is the Anthropic Messages wire shape.
type wireRequest struct {
	Model         string         `json:"model"`
	Stream        bool           `json:"stream"`
	MaxTokens     int            `json:"max_tokens"`
	System        any            `json:"system,omitempty"`
	Messages      []wireMessage  `json:"messages"`
	Tools         []wireTool     `json:"tools,omitempty"`
	ToolChoice    wireToolChoice `json:"tool_choice,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	StopSequences []string       `json:"stop_sequences,omitempty"`
	Thinking      *wireThinking  `json:"thinking,omitempty"`
}

// wireMessage is the per-message wire shape. Content is
// `any` so we can emit either a plain string (simple text-only
// user turn) or a list of blocks (mixed text + tool_use /
// tool_result).
type wireMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// wireContentBlock is one entry in a multi-block content list.
// The wire format tags each block with a `type` discriminator;
// additional fields depend on the type.
type wireContentBlock struct {
	// Type is one of "text", "tool_use", "tool_result", "thinking".
	Type string `json:"type"`
	// Text is the body of a "text" block.
	Text string `json:"text,omitempty"`
	// ID + Name + Input belong to a "tool_use" block.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// ToolUseID + Content + IsError belong to a "tool_result" block.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// Thinking + Signature belong to a "thinking" block.
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// CacheControl is emitted only on blocks that Anthropic
	// accepts cache markers on (assistant-side text and tool_use;
	// not on tool_result or user-side blocks). When the canonical
	// ContentBlock carries a non-nil CacheControl, we forward it.
	CacheControl *wireCacheControl `json:"cache_control,omitempty"`
}

// wireSystemBlock is one entry in a list-of-blocks system prompt.
// Anthropic's system blocks are typed ("text") and may carry a
// cache_control marker.
type wireSystemBlock struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	CacheControl *wireCacheControl `json:"cache_control,omitempty"`
}

// wireCacheControl is the cache-control marker. Anthropic's only
// supported type today is "ephemeral"; we keep the field open.
type wireCacheControl struct {
	Type string `json:"type"`
	// TTL may be "5m" (default) or "1h". Forwarded as-is when
	// the canonical CacheControl carries a TTL hint (it doesn't
	// today — kept as a forward-compat field).
	TTL string `json:"ttl,omitempty"`
}

// wireTool is the Anthropic tool shape: flat
// {name, description, input_schema}. When the canonical
// ToolDefinition carries a CacheControl marker we forward it
// as Anthropic's nested `cache_control: {type: "ephemeral"}`
// — Anthropic accepts cache markers on tool definitions to
// keep the tool catalog warm across turns.
type wireTool struct {
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	InputSchema  json.RawMessage   `json:"input_schema,omitempty"`
	CacheControl *wireCacheControl `json:"cache_control,omitempty"`
}

// wireToolChoice is Anthropic's tool_choice envelope. We always
// send "auto" today — the model decides whether to call a tool.
type wireToolChoice struct {
	Type string `json:"type"`
}

// wireThinking is the extended-thinking config. Phase 4 Step 3
// emits only the "enabled" form with a budget; "adaptive" and
// "disabled" forms are not currently produced by Forge because
// there's no canonical flag for them yet.
type wireThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}
