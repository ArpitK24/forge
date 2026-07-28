package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRequestJSONMarshal verifies the canonical wire shape:
// jsonrpc, id (int64 not float), method, params (omitempty).
// Pinning the wire bytes is important — a regression that
// re-typed ID as float would silently break every JSON-RPC
// peer.
func TestRequestJSONMarshal(t *testing.T) {
	req := Request{
		JSONRPC: JSONRPCVersion,
		ID:      42,
		Method:  MethodInitialize,
		Params:  json.RawMessage(`{"protocolVersion":"2025-06-18"}`),
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	want := `{"jsonrpc":"2.0","id":42,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	if got != want {
		t.Errorf("Request JSON = %s, want %s", got, want)
	}
	// Specifically check id is rendered as integer 42, not float 42.0.
	if !strings.Contains(got, `"id":42`) {
		t.Errorf("id field = %q, want integer 42", got)
	}
	// Detect a float-rendered id. `42,` (followed by next field)
	// is the correct plain-int form; `42.0` or `42e0` would be float.
	if strings.Contains(got, `"id":42.0`) || strings.Contains(got, `"id":42e`) {
		t.Errorf("id field is float/exponent; want plain int: %s", got)
	}
}

// TestResponseJSONRoundTrip checks that a successful response
// (Result populated, Error nil) marshals and unmarshals cleanly.
// Pin: when Error is nil, the json output must OMIT the error
// key (json:"error,omitempty").
func TestResponseJSONRoundTrip(t *testing.T) {
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      7,
		Result:  json.RawMessage(`{"protocolVersion":"2025-06-18"}`),
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"error"`) {
		t.Errorf("Response JSON should omit error key when nil: %s", b)
	}
	var got Response
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != 7 {
		t.Errorf("ID = %d, want 7", got.ID)
	}
	if string(got.Result) != `{"protocolVersion":"2025-06-18"}` {
		t.Errorf("Result = %s, want preserved", got.Result)
	}
	if got.Error != nil {
		t.Errorf("Error = %+v, want nil", got.Error)
	}
}

// TestResponseJSONWithError covers the error-response case.
// The wire shape: `error` is present, `result` is absent.
func TestResponseJSONWithError(t *testing.T) {
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      99,
		Error: &RPCError{
			Code:    -32601,
			Message: "Method not found",
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"error":{"code":-32601`) {
		t.Errorf("error field malformed: %s", b)
	}
	if strings.Contains(string(b), `"result"`) {
		t.Errorf("Response JSON should omit result key when nil: %s", b)
	}
	var got Response
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error == nil || got.Error.Code != -32601 {
		t.Errorf("Error.Code = %v, want -32601", got.Error)
	}
}

// TestNotificationJSONMarshal pins the wire shape of a
// notification: no `id` field at all.
func TestNotificationJSONMarshal(t *testing.T) {
	n := Notification{
		JSONRPC: JSONRPCVersion,
		Method:  MethodInitialized,
		Params:  json.RawMessage(`{}`),
	}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	want := `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`
	if got != want {
		t.Errorf("Notification JSON = %s, want %s", got, want)
	}
	if strings.Contains(got, `"id"`) {
		t.Errorf("Notification must not have an id field: %s", got)
	}
}

// TestMethodConstants pins the exact string values of the
// canonical MCP method names. Any drift would silently break
// compatibility with real servers.
func TestMethodConstants(t *testing.T) {
	cases := map[string]string{
		"MethodInitialize":    MethodInitialize,
		"MethodInitialized":   MethodInitialized,
		"MethodShutdown":      MethodShutdown,
		"MethodToolsList":     MethodToolsList,
		"MethodToolsCall":     MethodToolsCall,
		"MethodResourcesList": MethodResourcesList,
		"MethodResourcesRead": MethodResourcesRead,
		"JSONRPCVersion":      JSONRPCVersion,
	}
	want := map[string]string{
		"MethodInitialize":    "initialize",
		"MethodInitialized":   "notifications/initialized",
		"MethodShutdown":      "shutdown",
		"MethodToolsList":     "tools/list",
		"MethodToolsCall":     "tools/call",
		"MethodResourcesList": "resources/list",
		"MethodResourcesRead": "resources/read",
		"JSONRPCVersion":      "2.0",
	}
	for k, v := range cases {
		if v != want[k] {
			t.Errorf("%s = %q, want %q", k, v, want[k])
		}
	}
}
