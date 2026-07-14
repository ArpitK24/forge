package openai

import (
	"bufio"
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

// Client is an OpenAI-compatible chat completions client.
// It implements api.Provider by translating the canonical
// Request into an OpenAI Chat Completions POST and forwarding
// the SSE response as canonical StreamEvents.
//
// The client is a single value: an http.Client, an auth key, an
// API base URL, and the static ModelInfo for the model id. It
// is safe to use from a single goroutine (the query loop runs
// serially within a turn). The Stream method spawns a goroutine
// per call to drive the SSE reader; that goroutine is
// short-lived and ends with the call.
type Client struct {
	// http is the underlying HTTP client. Tests substitute this
	// with a client whose Transport is a httptest.Server; the
	// default uses the standard library's default transport.
	http *http.Client
	// apiKey is the bearer token sent in the Authorization
	// header. For NIM this is the value of NVIDIA_API_KEY.
	apiKey string
	// apiBase is the OpenAI-compatible base URL with no trailing
	// slash, e.g. "https://integrate.api.nvidia.com/v1". The
	// /chat/completions path is appended to it.
	apiBase string
	// info is the static ModelInfo for this client. It is
	// returned by Info() and used for downstream sizing.
	info api.ModelInfo
}

// New constructs an OpenAI-compatible client.
//
// apiBase is the base URL with no trailing slash, e.g.
// "https://integrate.api.nvidia.com/v1". modelID is the model
// id sent in every request. apiKey is the bearer token.
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

// Stream implements api.Provider. It POSTs a Chat Completions
// request and forwards the SSE response as canonical events.
// The two returned channels follow the api.Provider contract:
// events is closed after EventMessageStop or EventError; errs
// is closed after the goroutine that produced events has
// returned.
//
// ctx cancellation aborts the in-flight HTTP request via the
// request's context, and the reader goroutine watches for
// ctx.Done() while reading the body.
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

		url := c.apiBase + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			emitError(events, errs, core.Newf(core.KindHTTP, "new request: %v", err))
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.http.Do(httpReq)
		if err != nil {
			// Classify the transport error. ctx cancellation
			// surfaces as KindCancelled; everything else is
			// KindHTTP.
			if ctx.Err() != nil {
				emitError(events, errs, core.Wrap(core.KindCancelled, ctx.Err(), "request cancelled"))
				return
			}
			emitError(events, errs, core.Wrap(core.KindHTTP, err, "http do"))
			return
		}

		// Map non-2xx responses to typed errors. Read the body
		// (limited) so the error message can include the
		// provider's error text.
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

// buildRequestBody serializes the canonical Request to the
// OpenAI Chat Completions JSON wire format.
func buildRequestBody(req api.Request) ([]byte, error) {
	wire := wireRequest{
		Model:    req.Model,
		Stream:   true,
		Messages: convertMessages(req.Messages),
	}
	if req.System.IsString && req.System.String != "" {
		// Prepend a system message. The OpenAI Chat Completions
		// API keeps the system role as a top-level message,
		// not a separate field.
		wire.Messages = append([]wireMessage{{Role: "system", Content: req.System.String}}, wire.Messages...)
	}
	if req.MaxTokens > 0 {
		wire.MaxTokens = req.MaxTokens
	}
	if req.Temperature != nil {
		wire.Temperature = req.Temperature
	}
	if req.TopP != nil {
		wire.TopP = req.TopP
	}
	if len(req.StopSequences) > 0 {
		wire.Stop = req.StopSequences
	}
	if len(req.Tools) > 0 {
		wire.Tools = convertTools(req.Tools)
		wire.ToolChoice = "auto"
	}
	// Note: thinking is not honored by NIM in Phase 2; the
	// OpenAI-compatible wire format doesn't have a stable
	// surface for it. Phase 4's Anthropic adapter will
	// honor req.Thinking.

	return json.Marshal(wire)
}

// wireRequest is the OpenAI Chat Completions wire shape. The
// fields we omit are nil/empty on the wire.
type wireRequest struct {
	Model       string             `json:"model"`
	Messages    []wireMessage      `json:"messages"`
	Stream      bool               `json:"stream"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stop        []string           `json:"stop,omitempty"`
	Tools       []wireTool         `json:"tools,omitempty"`
	ToolChoice  any                `json:"tool_choice,omitempty"`
}

// wireMessage is the per-message wire shape. The Content field
// is a string for plain text and a list of parts for multimodal;
// Phase 2 sends string only.
type wireMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCalls  []wireToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// wireTool is the OpenAI tool-definition shape. We pass
// through the JSON Schema from core.ToolDefinition.
type wireTool struct {
	Type     string             `json:"type"`
	Function wireToolFunction   `json:"function"`
}

// wireToolFunction is the function-calling part of a tool def.
type wireToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// wireToolCall is the assistant's tool-use request on the wire.
// Index is the position in the tool_calls list (used by
// streaming chunks to identify which call a delta belongs to).
type wireToolCall struct {
	Index    int          `json:"index"`
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function wireCallFunc `json:"function"`
}

// wireCallFunc is the function-call portion of a tool call.
type wireCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// convertMessages walks the canonical Message list and
// produces the wire shape. Tool-result messages become
// "role: tool" entries with the matching tool_call_id.
func convertMessages(in []core.Message) []wireMessage {
	out := make([]wireMessage, 0, len(in))
	for _, m := range in {
		role := m.Role.String()
		// Special-case the assistant's tool-calls.
		if m.Role == core.RoleAssistant && m.HasToolUse() {
			wm := wireMessage{Role: "assistant"}
			text := m.GetFirstText()
			if text != "" {
				wm.Content = text
			}
			for i, tu := range m.ToolUses() {
				wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
					Index: i,
					ID:    tu.ID,
					Type:  "function",
					Function: wireCallFunc{
						Name:      tu.Name,
						Arguments: string(tu.Input),
					},
				})
			}
			out = append(out, wm)
			continue
		}
		// User-side tool results arrive as a list of tool_result
		// blocks in a single user message. Emit one wireMessage
		// per tool result with role=tool and the matching id.
		if m.Role == core.RoleUser && len(m.Content.Blocks) > 0 {
			anyToolResult := false
			for _, b := range m.Content.Blocks {
				if b.Kind == core.BlockToolResult && b.ToolResult != nil {
					anyToolResult = true
					out = append(out, wireMessage{
						Role:       "tool",
						Content:    string(b.ToolResult.Content),
						ToolCallID: b.ToolResult.ToolUseID,
					})
				}
			}
			if anyToolResult {
				continue
			}
		}
		// Plain text or content-blocks-without-tool-results.
		out = append(out, wireMessage{
			Role:    role,
			Content: m.GetFirstText(),
		})
	}
	return out
}

// convertTools walks the canonical ToolDefinition list and
// produces the OpenAI tool shape. The schema (Parameters) is
// passed through as raw JSON.
func convertTools(in []core.ToolDefinition) []wireTool {
	out := make([]wireTool, 0, len(in))
	for _, t := range in {
		out = append(out, wireTool{
			Type: "function",
			Function: wireToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
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
func classifyHTTPError(status int, retryAfterHeader, body string) *core.Error {
	details := map[string]any{"status": status}
	if body != "" {
		details["body"] = body
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return core.Newf(core.KindAuth, "authentication failed (%d): %s", status, body).WithDetail("status", status)
	case status == http.StatusTooManyRequests:
		e := core.Newf(core.KindRateLimit, "rate limited (%d): %s", status, body)
		if ra, err := parseRetryAfter(retryAfterHeader); err == nil {
			e.RetryAfter = ra
		}
		return e
	case status == 529:
		return core.Newf(core.KindHTTPStatus, "overloaded (529): %s", body).WithDetail("status", status)
	case status == http.StatusBadRequest,
		status == http.StatusRequestEntityTooLarge,
		status == http.StatusUnprocessableEntity,
		status >= 500:
		return core.Newf(core.KindHTTPStatus, "http %d: %s", status, body).WithDetail("status", status)
	default:
		return core.Newf(core.KindAPI, "http %d: %s", status, body)
	}
}

// parseRetryAfter parses an HTTP Retry-After header. It
// accepts either a number of seconds ("30") or an HTTP-date.
// Anything we can't parse is reported as an error so the
// caller can decide whether to default.
func parseRetryAfter(h string) (int, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, fmt.Errorf("empty")
	}
	// Try numeric form first.
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
// the errors channel. The query loop drains both, and we want
// both so downstream code that only watches one still sees the
// failure.
func emitError(events chan<- api.StreamEvent, errs chan<- error, e *core.Error) {
	select {
	case events <- api.EventOfError(e):
	default:
		// Buffer full: drop the event; the errs channel is
		// still authoritative.
	}
	errs <- e
}

// parseSSE reads the SSE response body and emits canonical
// StreamEvents. It is split out from Stream so tests can
// exercise the parser directly with an io.Reader.
//
// The expected format:
//
//	data: {json}\n
//	\n
//	data: {json}\n
//	\n
//	...
//	data: [DONE]\n
//	\n
//
// The body may also include event:, id:, retry:, and comment
// (":") lines per the SSE spec; we ignore all of those except
// `data:`. Empty lines and unknown lines are skipped.
func parseSSE(ctx context.Context, body io.Reader, events chan<- api.StreamEvent, errs chan<- error, model string) {
	_ = model // currently unused; reserved for future error context
	reader := bufio.NewReaderSize(body, 64*1024)
	state := newSSEState()

	for {
		select {
		case <-ctx.Done():
			emitError(events, errs, core.Wrap(core.KindCancelled, ctx.Err(), "stream cancelled"))
			return
		default:
		}

		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				// Treat premature EOF as an error: a healthy
				// stream ends with `data: [DONE]`. If we
				// never saw DONE, the connection was cut.
				if !state.sawDone {
					emitError(events, errs, core.Newf(core.KindAPI, "stream ended without [DONE]"))
				}
				return
			}
			// ctx cancellation surfaces here too.
			if ctx.Err() != nil {
				emitError(events, errs, core.Wrap(core.KindCancelled, ctx.Err(), "read cancelled"))
				return
			}
			emitError(events, errs, core.Wrap(core.KindHTTP, err, "read sse"))
			return
		}

		// Trim trailing \r\n or \n.
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			continue
		}
		// Comments (":...") are ignored per SSE spec.
		if line[0] == ':' {
			continue
		}

		// Field lines: "field: value" or "field:value".
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			// Unknown line type — skip.
			continue
		}
		field := string(line[:colon])
		value := string(line[colon+1:])
		// Strip a single leading space (per SSE spec).
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}

		switch field {
		case "data":
			if value == "[DONE]" {
				state.sawDone = true
				// Flush any pending tool-call partial JSON
				// as a final block stop, then emit the
				// terminal message_stop. This is the natural
				// end of a successful stream.
				state.flush(events)
				events <- api.EventOfMessageStop()
				return
			}
			state.handleData(value, events)
		case "event", "id", "retry":
			// Ignored. OpenAI sends "event: ..." but the
			// payload is on the data: line, so we don't need
			// to switch on event.
		default:
			// Unknown field: skip.
		}
	}
}
