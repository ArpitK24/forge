package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func toolUse(id, name string, input any) core.ContentBlock {
	b, _ := json.Marshal(input)
	return core.ContentBlock{Kind: core.BlockToolUse, ToolUse: &core.ToolUse{ID: id, Name: name, Input: b}}
}

func toolResult(id, text string) core.ContentBlock {
	b, _ := json.Marshal(text)
	return core.ContentBlock{Kind: core.BlockToolResult, ToolResult: &core.ToolResult{ToolUseID: id, Content: b}}
}

func newEngine(t *testing.T, root string) *Engine {
	t.Helper()
	e, err := New(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

func TestEngineAssemblesAndSeesEdits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, root, "billing/invoice.go", `package billing

// ComputeInvoiceTotal sums line items and applies tax.
func ComputeInvoiceTotal(items []int, taxRate float64) float64 {
	total := 0
	for _, it := range items {
		total += it
	}
	return float64(total) * (1 + taxRate)
}
`)
	writeFile(t, root, "auth/login.go", "package auth\n\nfunc Login(user string) bool { return user != \"\" }\n")
	e := newEngine(t, root)

	if _, err := os.Stat(filepath.Join(root, ".forge", "index", ".gitignore")); err != nil {
		t.Fatalf("index dir not git-ignored: %v", err)
	}
	msgs := []core.Message{core.NewUserText("the invoice total ignores the tax rate")}
	block, err := e.Assemble(ctx, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(block, "<forge-context turn=1 budget=8192") ||
		!strings.Contains(block, "=== billing/invoice.go ===") ||
		!strings.Contains(block, "func ComputeInvoiceTotal(") {
		t.Fatalf("block:\n%s", block)
	}

	// The agent adds a function; Observe + Assemble reindex it
	// synchronously so the very next turn can retrieve it.
	writeFile(t, root, "billing/refund.go", "package billing\n\n// IssueRefundVoucher refunds a voucher.\nfunc IssueRefundVoucher() {}\n")
	call := core.ToolUse{ID: "t1", Name: core.ToolWrite, Input: json.RawMessage(`{"file_path":` + mustQuote(filepath.Join(root, "billing", "refund.go")) + `}`)}
	e.Observe(ctx, call, "ok", false)
	msgs = append(msgs,
		core.Message{Role: core.RoleAssistant, Content: core.BlocksContent(toolUse("t1", core.ToolWrite, map[string]string{"file_path": filepath.Join(root, "billing", "refund.go")}))},
		core.Message{Role: core.RoleUser, Content: core.BlocksContent(toolResult("t1", "wrote file"))},
		core.NewUserText("now the refund voucher"),
	)
	block, err = e.Assemble(ctx, msgs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(block, "IssueRefundVoucher") || !strings.Contains(block, "turn=2") {
		t.Fatalf("edit not visible next turn:\n%s", block)
	}
}

func mustQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestEngineEmptyConversation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\n")
	e := newEngine(t, root)
	block, err := e.Assemble(context.Background(), nil)
	if err != nil || block != "" {
		t.Fatalf("block=%q err=%v", block, err)
	}
	if st, err := e.Sync(context.Background()); err != nil || st.Changed() {
		t.Fatalf("second sync should be a no-op: %+v %v", st, err)
	}
}

func TestObserveIgnoresReadsErrorsAndOutsidePaths(t *testing.T) {
	root := t.TempDir()
	e := newEngine(t, root)
	ctx := context.Background()
	e.Observe(ctx, core.ToolUse{Name: core.ToolRead, Input: json.RawMessage(`{"file_path":"a.go"}`)}, "", false)
	e.Observe(ctx, core.ToolUse{Name: core.ToolEdit, Input: json.RawMessage(`{"file_path":"b.go"}`)}, "", true)
	outside := filepath.Join(t.TempDir(), "outside.go") // absolute on every OS
	e.Observe(ctx, core.ToolUse{Name: core.ToolEdit, Input: json.RawMessage(`{"file_path":` + mustQuote(outside) + `}`)}, "", false)
	e.Observe(ctx, core.ToolUse{Name: core.ToolEdit, Input: json.RawMessage(`not json`)}, "", false)
	e.Observe(ctx, core.ToolUse{Name: core.ToolEdit, Input: json.RawMessage(`{"file_path":"sub/c.go"}`)}, "", false)
	if len(e.pending) != 1 || !e.pending["sub/c.go"] {
		t.Fatalf("pending = %v", e.pending)
	}
}

func TestQueryFromMessages(t *testing.T) {
	msgs := []core.Message{
		core.NewUserText("fix the parser"),
		{Role: core.RoleAssistant, Content: core.BlocksContent(
			core.ContentBlock{Kind: core.BlockText, Text: "assistant prose is not a query"},
			toolUse("a", core.ToolGrep, map[string]string{"pattern": "parseHeader"}),
			toolUse("b", core.ToolBash, map[string]string{"command": "go test"}),
		)},
		{Role: core.RoleUser, Content: core.BlocksContent(
			toolResult("a", "x/parse.go:12: func parseHeader"),
			core.ContentBlock{Kind: core.BlockToolResult, ToolResult: &core.ToolResult{ToolUseID: "b",
				Content: json.RawMessage(`[{"type":"text","text":"--- FAIL: TestParse"}]`)}},
		)},
	}
	q := QueryFromMessages(msgs)
	if q.ProblemStatement != "fix the parser" {
		t.Fatalf("problem = %q", q.ProblemStatement)
	}
	if len(q.Trajectory) != 2 || q.Trajectory[0].Tool != core.ToolGrep ||
		q.Trajectory[0].Output != "x/parse.go:12: func parseHeader" ||
		q.Trajectory[1].Output != "--- FAIL: TestParse" {
		t.Fatalf("trajectory = %+v", q.Trajectory)
	}
	if got := resultText(json.RawMessage(`{"stdout":"x"}`)); got != `{"stdout":"x"}` {
		t.Fatalf("raw json result = %q", got)
	}
}
