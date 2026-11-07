//go:build mcp_smoke

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// realServerEnv opts in to TestMcpRealServer. It needs a real
// MCP server, which usually means npm and network access, so it
// never runs by default (CI's smoke step filters on TestMcpMock).
//
//	FORGE_MCP_REAL_SERVER=1
//	    runs `npx -y @modelcontextprotocol/server-everything stdio`
//	FORGE_MCP_REAL_SERVER="node /path/to/server-everything/dist/index.js stdio"
//	    runs that command line instead (split on spaces)
//
// Example:
//
//	FORGE_MCP_REAL_SERVER=1 go test -count=1 -tags mcp_smoke -run TestMcpRealServer ./internal/mcp/...
const realServerEnv = "FORGE_MCP_REAL_SERVER"

// TestMcpRealServer connects the full Manager stack to the
// reference MCP "everything" server over stdio and calls its
// echo tool. It is the end-to-end proof that Forge's stdio
// framing interoperates with the official TypeScript SDK.
func TestMcpRealServer(t *testing.T) {
	spec := os.Getenv(realServerEnv)
	if spec == "" {
		t.Skipf("set %s=1 (or to a server command line) to run against a real MCP server", realServerEnv)
	}
	argv := []string{"npx", "-y", "@modelcontextprotocol/server-everything", "stdio"}
	if spec != "1" {
		argv = strings.Fields(spec)
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		t.Skipf("%s not on PATH: %v", argv[0], err)
	}

	mgr := NewManager([]core.McpServerConfig{{
		Name:       "everything",
		Command:    argv[0],
		Args:       argv[1:],
		ServerType: "stdio",
	}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Generous: npx may have to download the package first.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = mgr.Close(c)
	})
	if err := mgr.Connect(ctx); err != nil {
		t.Fatalf("Connect to real MCP server: %v", err)
	}
	all := mgr.Tools()
	t.Logf("real server listed %d tools: %v", len(all), toolNames(all))

	var echo tools.Tool
	for _, tl := range all {
		if strings.HasSuffix(tl.Name(), "__echo") {
			echo = tl
		}
	}
	if echo == nil {
		t.Fatalf("no echo tool; tools=%v", toolNames(all))
	}
	msg := "hello from forge — multi\nline ✓"
	input, _ := json.Marshal(map[string]string{"message": msg})
	res := echo.Execute(ctx, input, nil)
	if res.IsError {
		t.Fatalf("echo: IsError=true, Text=%q", res.Text)
	}
	if !strings.Contains(res.Text, msg) {
		t.Errorf("echo Text = %q, want it to contain %q", res.Text, msg)
	}
}
