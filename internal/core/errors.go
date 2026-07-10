// Package core holds the shared foundation types for Forge: errors,
// messages, content blocks, config, permissions, and constants.
//
// Every other internal package depends on core. Core depends on nothing
// (besides the Go standard library and a few small third-party packages
// added later, like uuid).
package core

import (
	"fmt"
	"net/http"
)

// ErrorKind is a closed set of failure categories. Every Forge error
// carries exactly one of these so callers can branch on it without
// resorting to string matching.
type ErrorKind int

const (
	// KindUnknown is the zero value; treat it as "I forgot to classify this"
	// and fix the call site. New code should always set a real kind.
	KindUnknown ErrorKind = iota

	// KindAPI is a vendor-neutral API error with no HTTP status attached
	// (e.g. malformed request, schema mismatch).
	KindAPI

	// KindHTTPStatus is an API error carrying an HTTP status code
	// (e.g. 400, 500 from a provider gateway).
	KindHTTPStatus

	// KindAuth is an authentication failure: missing key, invalid token,
	// expired OAuth credential, device-code flow not completed.
	KindAuth

	// KindPermission is a permission denial from the local permission system
	// (not a vendor-side 403). The user (or the TUI permission handler)
	// decided not to allow a tool call.
	KindPermission

	// KindTool is a tool execution failure. The tool was allowed to run
	// and produced an error result; the process did not panic.
	KindTool

	// KindIO is a filesystem or process I/O failure (read, write, stat,
	// spawn, kill, pipe).
	KindIO

	// KindJSON is a JSON parse/serialize failure.
	KindJSON

	// KindHTTP is a transport-level HTTP failure (connection refused,
	// TLS handshake, DNS, timeout at the network layer — distinct from
	// a response that came back with a bad status).
	KindHTTP

	// KindRateLimit is a vendor rate-limit response. When present, RetryAfter
	// is populated from the Retry-After header or vendor equivalent.
	KindRateLimit

	// KindContextLimit is the model's context window being exceeded by
	// the current request. Triggers auto-compaction (query package).
	KindContextLimit

	// KindMaxTokens is the response hitting the max_tokens cap before
	// the model produced a stop reason. Distinct from context window
	// exhaustion: this is the OUTPUT cap, not the INPUT cap.
	KindMaxTokens

	// KindCancelled is a user-initiated cancellation (Ctrl+C, /cancel,
	// context cancellation). Not a real error — usually surfaced as
	// a normal exit.
	KindCancelled

	// KindConfig is a configuration load/parse/validate failure
	// (settings.json malformed, MCP config wrong, etc.).
	KindConfig

	// KindMCP is a Model Context Protocol protocol-level error
	// (JSON-RPC error response, transport failure to an MCP server).
	KindMCP
)

// String returns a stable, lowercase, kebab-style name for the kind.
// Used in logs and in StreamJson output mode.
func (k ErrorKind) String() string {
	switch k {
	case KindUnknown:
		return "unknown"
	case KindAPI:
		return "api"
	case KindHTTPStatus:
		return "http-status"
	case KindAuth:
		return "auth"
	case KindPermission:
		return "permission"
	case KindTool:
		return "tool"
	case KindIO:
		return "io"
	case KindJSON:
		return "json"
	case KindHTTP:
		return "http"
	case KindRateLimit:
		return "rate-limit"
	case KindContextLimit:
		return "context-limit"
	case KindMaxTokens:
		return "max-tokens"
	case KindCancelled:
		return "cancelled"
	case KindConfig:
		return "config"
	case KindMCP:
		return "mcp"
	default:
		return fmt.Sprintf("kind-%d", int(k))
	}
}

// Error is Forge's single tagged error type. Every fallible operation
// in the codebase returns one of these (or wraps one). It is the only
// error type that crosses module boundaries; modules are free to use
// their own internal error types but must convert at the boundary.
//
// Wrap with fmt.Errorf("...: %w", err) to add context while preserving
// the Kind. Use errors.As to recover the *Error at the call site.
type Error struct {
	Kind       ErrorKind
	Message    string         // human-readable description
	Cause      error          // underlying error, if any (may be nil)
	StatusCode int            // HTTP status, populated only for KindHTTPStatus
	RetryAfter int            // seconds, populated only for KindRateLimit (0 = not provided)
	Details    map[string]any // optional structured context (tool name, file path, etc.)
}

// New constructs an Error with the given kind and message.
func New(kind ErrorKind, msg string) *Error {
	return &Error{Kind: kind, Message: msg}
}

// Newf is New with a formatted message.
func Newf(kind ErrorKind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// Wrap returns an Error that adds a message layer over cause. The
// classification rule:
//
//   - If cause is nil, the returned error uses kind as its kind.
//   - If cause is a non-*Error (e.g. a stdlib error), the returned
//     error uses kind as its kind and preserves cause as the wrapped
//     cause for errors.Is / errors.As.
//   - If cause is already an *Error, the inner (root-cause) kind
//     wins. This is because call sites branch on Kind to decide
//     retry / compact / permission logic, and the inner kind is the
//     actual failure mode — the outer "I was inside a tool" context
//     is captured in the message string and the Details map, not in
//     the kind. The kind argument is ignored in this case.
//
// In short: kind classifies the cause when cause is unclassified,
// and the message always layers on top of whatever message the cause
// already carried.
func Wrap(kind ErrorKind, cause error, msg string) *Error {
	if e, ok := cause.(*Error); ok && e != nil {
		return &Error{
			Kind:       e.Kind, // root cause always wins
			Message:    msg + ": " + e.Message,
			Cause:      e.Cause,
			StatusCode: e.StatusCode,
			RetryAfter: e.RetryAfter,
			Details:    e.Details,
		}
	}
	return &Error{Kind: kind, Message: msg, Cause: cause}
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Kind, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// IsRetryable reports whether a caller should retry the operation.
// Per spec §2.1: true for rate-limit and for HTTP 529 "overloaded".
func (e *Error) IsRetryable() bool {
	if e == nil {
		return false
	}
	if e.Kind == KindRateLimit {
		return true
	}
	if e.Kind == KindHTTPStatus && e.StatusCode == http.StatusTooManyRequests {
		// 429 is also rate-limit, even if the provider didn't set Retry-After.
		return true
	}
	if e.Kind == KindHTTPStatus && e.StatusCode == 529 {
		// 529 is "Site is overloaded" (used by some providers, including
		// Anthropic's overloaded signal). Treat as retryable.
		return true
	}
	return false
}

// IsContextLimit reports whether this error means the model cannot
// accept the current request size. Used by the query package to decide
// when to auto-compact (spec §5.2).
func (e *Error) IsContextLimit() bool {
	if e == nil {
		return false
	}
	return e.Kind == KindContextLimit || e.Kind == KindMaxTokens
}

// WithDetail adds a structured detail to the error. Returns the same
// *Error so it can be chained: core.New(...).WithDetail("tool", "Bash").
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any, 1)
	}
	e.Details[key] = value
	return e
}
