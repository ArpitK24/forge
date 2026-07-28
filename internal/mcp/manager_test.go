package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// TestManager_EmptyConfigIsNoOp verifies that connecting a
// Manager with no servers returns nil and yields no tools.
// A user who hasn't configured MCP shouldn't see an error
// at startup or be locked out of the rest of the tool
// registry.
func TestManager_EmptyConfigIsNoOp(t *testing.T) {
	m := NewManager(nil, silentLogger())
	if err := m.Connect(context.Background()); err != nil {
		t.Errorf("Connect(empty): %v, want nil", err)
	}
	if tools := m.Tools(); len(tools) != 0 {
		t.Errorf("Tools() len = %d, want 0", len(tools))
	}
	if errs := m.Errors(); len(errs) != 0 {
		t.Errorf("Errors() len = %d, want 0", len(errs))
	}
}

// TestManager_HTTPTransportIsStubbed verifies that a server
// configured with server_type: "http" produces a per-server
// KindMCP error from Connect. Today the streamable-HTTP
// transport is a stub that returns "Phase 4.1 follow-on" —
// Connect must surface that to Manager.errors rather than
// silently succeeding or panicking.
func TestManager_HTTPTransportIsStubbed(t *testing.T) {
	m := NewManager([]core.McpServerConfig{{
		Name:       "httpserver",
		ServerType: "http",
		URL:        "http://localhost:0/mcp",
	}}, silentLogger())
	if err := m.Connect(context.Background()); err == nil {
		t.Errorf("Connect returned nil; want an http-stub error")
	}
	errs := m.Errors()
	if len(errs) != 1 {
		t.Fatalf("Errors() len = %d, want 1", len(errs))
	}
	var ce *core.Error
	if !errors.As(errs["httpserver"], &ce) {
		t.Fatalf("err type = %T, want *core.Error", errs["httpserver"])
	}
	if ce.Kind != core.KindMCP {
		t.Errorf("kind = %v, want KindMCP", ce.Kind)
	}
	if !contains(ce.Message, "Phase 4.1") && !contains(ce.Message, "follow-on") {
		t.Errorf("message = %q, want it to mention Phase 4.1 follow-on", ce.Message)
	}
	// No tools registered for a failed server.
	if tools := m.Tools(); len(tools) != 0 {
		t.Errorf("Tools() len = %d, want 0", len(tools))
	}
}

// TestManager_UnknownServerTypeDefaultsToStdio verifies that
// a server with an unrecognized server_type falls back to
// stdio transport instead of failing immediately. The Manager
// logs a warning, then the stdio transport's own error path
// (spawn failure) reports the actual cause. This avoids
// surprising users who typo "httpe" or similar.
func TestManager_UnknownServerTypeDefaultsToStdio(t *testing.T) {
	m := NewManager([]core.McpServerConfig{{
		Name:       "weird",
		ServerType: "bogus-transport",
		// Empty Command — stdio Start will return
		// KindConfig "missing command".
	}}, silentLogger())
	if err := m.Connect(context.Background()); err == nil {
		t.Errorf("Connect returned nil; want a missing-command error")
	}
	errs := m.Errors()
	if len(errs) != 1 {
		t.Fatalf("Errors() len = %d, want 1", len(errs))
	}
	if _, ok := errs["weird"]; !ok {
		t.Errorf("Errors() missing entry for %q", "weird")
	}
}

// TestManager_PartialConnectOnMultipleFailures verifies that
// when 3 servers are configured and 2 of them fail, the
// third's failure doesn't poison the others — Connect
// returns the first error but Manager.errors contains all
// three failures. A user with three flaky MCP servers
// shouldn't have the loop refuse to bring up the rest.
func TestManager_PartialConnectOnMultipleFailures(t *testing.T) {
	m := NewManager([]core.McpServerConfig{
		{Name: "http1", ServerType: "http", URL: "x"},
		{Name: "http2", ServerType: "http", URL: "y"},
		{Name: "http3", ServerType: "http", URL: "z"},
	}, silentLogger())
	err := m.Connect(context.Background())
	if err == nil {
		t.Fatalf("Connect returned nil; want an error from one of the http stubs")
	}
	errs := m.Errors()
	if len(errs) != 3 {
		t.Errorf("Errors() len = %d, want 3 (one per server)", len(errs))
	}
	for _, name := range []string{"http1", "http2", "http3"} {
		if _, ok := errs[name]; !ok {
			t.Errorf("Errors() missing entry for %q", name)
		}
	}
}

// TestManager_CloseIsIdempotent verifies that calling Close
// twice doesn't panic and doesn't return an error the second
// time. Headless mode calls Close in a defer; a user who
// also calls Close in their /reload handler shouldn't crash.
func TestManager_CloseIsIdempotent(t *testing.T) {
	m := NewManager(nil, silentLogger())
	if err := m.Close(context.Background()); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Errorf("second Close returned error; want idempotent nil: %v", err)
	}
}

// contains is a small substring helper to avoid the strings
// import in this test file (mirrors framing_test.go's
// choice).
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}