// Package engine is the Milestone 1 context engine that plugs into the
// query loop (query.ContextProvider): it keeps the project's index at
// .forge/index/index.db current, ranks it with the "m1" retriever
// against the conversation so far, and renders the packed result as a
// <forge-context> block.
//
// It imports only core and the ctxengine subpackages, never
// internal/tools or internal/query: the loop's interface is satisfied
// structurally and tool activity arrives as plain data.
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/ctxengine/index"
	"github.com/ArpitK24/forge/internal/ctxengine/retrieve"
)

// DefaultBudget is the pack budget in estimated tokens. The design's
// window-relative budget (§10) arrives with the M3 allocator.
const DefaultBudget = 8192

// Options configure an Engine.
type Options struct {
	// Root is the project root (the session's working directory).
	Root string
	// IndexPath overrides <Root>/.forge/index/index.db.
	IndexPath string
	// Budget is the pack budget; 0 means DefaultBudget.
	Budget int
	// Retriever overrides the default m1 options.
	Retriever *retrieve.M1
}

// Engine is a per-session context engine. It is safe for concurrent
// use, though the loop calls it from one goroutine.
type Engine struct {
	root   string
	budget int
	ret    retrieve.M1
	src    *index.DirSource

	mu      sync.Mutex
	store   *index.Store
	synced  bool
	pending map[string]bool // edited paths to reindex before the next turn
	turn    int
}

// New opens (or creates) the project's index. The first Assemble runs
// the initial sync; New itself does no file walking.
func New(opts Options) (*Engine, error) {
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, err
	}
	dbPath := opts.IndexPath
	if dbPath == "" {
		dbPath = filepath.Join(root, filepath.FromSlash(index.DefaultPath))
		if err := ensureIgnored(filepath.Dir(dbPath)); err != nil {
			return nil, err
		}
	}
	store, err := index.Open(dbPath)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		root:    root,
		budget:  opts.Budget,
		ret:     retrieve.NewM1(),
		src:     index.NewDirSource(root, retrieve.Indexable),
		store:   store,
		pending: map[string]bool{},
	}
	if e.budget <= 0 {
		e.budget = DefaultBudget
	}
	if opts.Retriever != nil {
		e.ret = *opts.Retriever
	}
	return e, nil
}

// ensureIgnored drops a "*" .gitignore into the index directory so the
// database never shows up in `git status` (design doc §5).
func ensureIgnored(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("context engine: %w", err)
	}
	p := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte("# Forge context-engine index (a cache; safe to delete)\n*\n"), 0o644)
}

// Close closes the index.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.Close()
}

// Sync brings the whole index up to date (incremental: unchanged files
// are only stat'ed).
func (e *Engine) Sync(ctx context.Context) (index.SyncStats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.store.Sync(ctx, e.src)
	if err == nil {
		e.synced = true
	}
	return st, err
}

// Observe implements query.ContextProvider: an Edit or Write marks its
// file for synchronous reindexing before the next turn.
func (e *Engine) Observe(_ context.Context, call core.ToolUse, _ string, isError bool) {
	if isError {
		return
	}
	switch call.Name {
	case core.ToolEdit, core.ToolWrite, "MultiEdit":
	default:
		return
	}
	var in struct {
		FilePath string `json:"file_path"`
	}
	if json.Unmarshal(call.Input, &in) != nil || in.FilePath == "" {
		return
	}
	if rel, ok := e.rel(in.FilePath); ok {
		e.mu.Lock()
		e.pending[rel] = true
		e.mu.Unlock()
	}
}

// rel maps a tool's file path to a slash-separated path under root.
func (e *Engine) rel(p string) (string, bool) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(e.root, p)
	}
	r, err := filepath.Rel(e.root, filepath.Clean(p))
	if err != nil || r == "." || strings.HasPrefix(r, "..") {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// Assemble implements query.ContextProvider. It runs the initial sync
// on first use, reindexes files edited since the last turn, then ranks
// and packs against the conversation so far.
func (e *Engine) Assemble(ctx context.Context, messages []core.Message) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.synced {
		if _, err := e.store.Sync(ctx, e.src); err != nil {
			return "", err
		}
		e.synced = true
	}
	if len(e.pending) > 0 {
		paths := make([]string, 0, len(e.pending))
		for p := range e.pending {
			paths = append(paths, p)
		}
		if _, err := e.store.Refresh(ctx, e.src, paths); err != nil {
			return "", err
		}
		e.pending = map[string]bool{}
	}
	e.turn++
	q := QueryFromMessages(messages)
	if strings.TrimSpace(q.ProblemStatement) == "" && len(q.Trajectory) == 0 {
		return "", nil
	}
	res, err := e.ret.RetrieveIndex(ctx, e.store, q, e.budget, e.read)
	if err != nil {
		return "", err
	}
	return Render(e.turn, e.budget, res), nil
}

func (e *Engine) read(p string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.root, filepath.FromSlash(p)))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Render wraps a pack in the <forge-context> block the model sees.
func Render(turn, budget int, res retrieve.Result) string {
	if res.Pack.Text == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<forge-context turn=%d budget=%d tokens=%d>\n", turn, budget, res.Pack.Tokens)
	b.WriteString("Repository context ranked for this task by Forge's context engine. ")
	b.WriteString("Excerpts may be partial (\"...\" marks elided code) and can be stale after edits; Read a file before editing it.\n")
	b.WriteString(res.Pack.Text)
	b.WriteString("</forge-context>")
	return b.String()
}

// QueryFromMessages turns the conversation into a retrieval query: the
// user's text messages form the problem statement, and every tool call
// paired with its result becomes a trajectory turn (oldest first).
func QueryFromMessages(messages []core.Message) retrieve.Query {
	var q retrieve.Query
	var texts []string
	inputs := map[string]int{} // tool_use id -> trajectory index
	for _, m := range messages {
		if m.Content.IsString {
			if m.Role == core.RoleUser {
				texts = append(texts, m.Content.String)
			}
			continue
		}
		for _, b := range m.Content.Blocks {
			switch b.Kind {
			case core.BlockText:
				// User text, including a compaction summary, seeds the query.
				if m.Role == core.RoleUser {
					texts = append(texts, b.Text)
				}
			case core.BlockToolUse:
				if b.ToolUse != nil {
					inputs[b.ToolUse.ID] = len(q.Trajectory)
					q.Trajectory = append(q.Trajectory, retrieve.Turn{Tool: b.ToolUse.Name, Input: string(b.ToolUse.Input)})
				}
			case core.BlockToolResult:
				if b.ToolResult == nil {
					continue
				}
				if i, ok := inputs[b.ToolResult.ToolUseID]; ok {
					q.Trajectory[i].Output = resultText(b.ToolResult.Content)
				}
			}
		}
	}
	q.ProblemStatement = strings.Join(texts, "\n")
	return q
}

// resultText extracts readable text from a tool_result's JSON content
// (a JSON string, an array of text blocks, or raw JSON).
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

// ForConfig returns the session's engine when cfg turns it on
// (--context-engine or "context_engine": true), rooted at root. It
// returns nil, nil when the engine is off, so callers keep the
// query loop's nil-provider path, which is today's behaviour.
func ForConfig(cfg *core.Config, root string) (*Engine, error) {
	if cfg == nil || !cfg.ContextEngine {
		return nil, nil
	}
	return New(Options{Root: root})
}
