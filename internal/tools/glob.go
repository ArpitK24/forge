package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
)

// globInputSchema is the JSON Schema for the Glob tool.
var globInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "Glob pattern, e.g. \"**/*.go\" or \"internal/**/*.md\"."
    },
    "base_path": {
      "type": "string",
      "description": "Optional directory to search in. Defaults to the working dir."
    }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`)

// globMaxResults is the cap on matches returned per call.
// 250 is the same number the spec calls out in §3.2.
const globMaxResults = 250

// GlobTool is the implementation of the Glob tool.
// Spec §3.2 (file tools). Returns file paths matching a
// glob pattern, sorted by most-recently-modified first.
type GlobTool struct{}

// Name implements Tool.
func (g *GlobTool) Name() string { return core.ToolGlob }

// Description implements Tool.
func (g *GlobTool) Description() string {
	return "Find files by glob pattern (e.g. \"**/*.go\"). Returns up " +
		"to 250 paths sorted by most-recently-modified first, as " +
		"relative paths from the base directory."
}

// PermissionLevel implements Tool.
func (g *GlobTool) PermissionLevel() core.PermissionLevel { return core.PermReadOnly }

// InputSchema implements Tool.
func (g *GlobTool) InputSchema() json.RawMessage { return globInputSchema }

// GlobInput is the JSON-decoded input shape.
type GlobInput struct {
	Pattern  string `json:"pattern"`
	BasePath string `json:"base_path,omitempty"`
}

// matchResult is a (path, mtime) pair. We use this struct
// rather than os.FileInfo so the sort is self-contained.
type matchResult struct {
	relPath string
	mtime   int64 // unix nano
}

// globDoubleStar is a small globber that handles `**`
// correctly. It splits the pattern on the path separator,
// finds any `**` segments, and walks the tree, matching
// the rest of the pattern against each candidate path.
//
// Semantics, matching what every shell + most editors do:
//
//   - `**` at the start: matches zero or more directories.
//     So `**/foo.go` matches `foo.go` and `a/b/foo.go`.
//   - `**` in the middle: matches zero or more directories.
//     So `a/**/b.go` matches `a/b.go` and `a/x/y/b.go`.
//   - `**` at the end: matches zero or more directories and
//     any file. So `a/**` matches `a/x` and `a/x/y/z`.
//
// We only support the `**`-as-a-segment form. The `**`
// must be a complete path segment (not embedded in a
// segment like `**.go`); the model can always split such
// patterns into a `**` followed by a `*.go`.
func globDoubleStar(base, pattern string) []string {
	// Normalize: forward slashes only, strip a leading "./".
	pat := filepath.ToSlash(pattern)
	pat = strings.TrimPrefix(pat, "./")
	segments := strings.Split(pat, "/")

	var results []string
	walk(base, segments, "", func(absPath string) {
		// Convert to relative-to-base. We track relative
		// segments in the walk closure, so absPath is
		// already constructed; we just need the form.
		rel, err := filepath.Rel(base, absPath)
		if err != nil {
			rel = absPath
		}
		results = append(results, filepath.Join(base, rel))
	})
	return results
}

// walk recursively descends `dir`, matching `segs` against
// the path. When segs is empty, we have a complete match
// and call onMatch with the absolute path. `relSoFar` is
// the slash-separated path built up so far (for nicer
// debugging, not used in the current implementation).
func walk(dir string, segs []string, relSoFar string, onMatch func(string)) {
	if len(segs) == 0 {
		onMatch(dir)
		return
	}
	seg := segs[0]
	rest := segs[1:]

	if seg == "**" {
		// `**` matches zero or more directories. The
		// zero-match case is the same as dropping `**`
		// entirely; the multi-match case is the same as
		// `**` followed by `rest`, recursively.
		walk(dir, rest, relSoFar, onMatch)

		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Skip the standard "ignore" directories so
			// the model doesn't drown in node_modules
			// or .git noise.
			if isIgnoredDir(e.Name()) {
				continue
			}
			child := filepath.Join(dir, e.Name())
			walk(child, segs, relSoFar, onMatch)
		}
		return
	}

	// Non-`**` segment: enumerate dir entries and try to
	// match this segment against each name.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		matched, err := filepath.Match(seg, name)
		if err != nil {
			// Bad pattern inside the segment; skip.
			continue
		}
		if !matched {
			continue
		}
		child := filepath.Join(dir, name)
		walk(child, rest, relSoFar+"/"+name, onMatch)
	}
}

// isIgnoredDir returns true for the small set of
// directories we always skip during recursive walks. The
// list is short on purpose: hidden dirs (those starting
// with `.`) are skipped by the matcher logic (filepath.Match
// doesn't match them with `*`), and these are the ones the
// model is most likely to NOT care about.
//
// We intentionally do NOT silently filter more aggressively
// (e.g. skipping `node_modules` always) because the spec
// is "list what matches". A user who DID want to find
// something in `target/` (Rust build dir) shouldn't have
// to set a flag.
func isIgnoredDir(name string) bool {
	switch name {
	case "node_modules", ".git", ".svn", ".hg":
		return true
	}
	return false
}

// Execute runs the glob. Behavior:
//
//   - Resolve `base_path` (or default to working dir) to an
//     absolute directory.
//   - Run filepath.Glob. This handles `*`, `?`, `[...]`, and
//     `**` (Go's `**` semantics: `**` matches any number of
//     path elements including zero; in practice this means
//     `**/*.go` matches both `foo.go` and `a/b/foo.go`).
//   - Stat each match, drop directories (Glob doesn't, but
//     the spec is "files"), and sort by most-recent-mtime
//     first.
//   - Truncate to globMaxResults; if we hit the cap, append
//     a notice in the Text so the model knows.
//   - Return paths as RELATIVE to base_path (so the model
//     can pass them back to Read/Edit/Write without
//     re-deriving the base).
//   - On success: ToolResult{Text, Metadata: {"count",
//     "truncated"}}.
func (g *GlobTool) Execute(ctx context.Context, input json.RawMessage, tc *ToolContext) ToolResult {
	var in GlobInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Glob: invalid input JSON: %v", err),
			IsError: true,
		}
	}
	if in.Pattern == "" {
		return ToolResult{
			Text:    "Glob: pattern is required",
			IsError: true,
		}
	}

	// Resolve the base path. An empty base_path means
	// "use working dir"; the resolveToolPath helper would
	// also do this, but Glob needs to reject the case
	// where the resolved path is a file, not a directory.
	wd := ""
	if tc != nil && tc.WorkingDir != "" {
		wd = tc.WorkingDir
	}
	base := in.BasePath
	if base == "" {
		base = wd
	}
	if base == "" {
		base = "."
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Glob: invalid base_path %q: %v", base, err),
			IsError: true,
		}
	}
	absBase = filepath.Clean(absBase)

	// Stat the base. We want a clean error if it's a file
	// rather than letting filepath.Glob silently return no
	// matches.
	info, err := os.Stat(absBase)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolResult{
				Text:    fmt.Sprintf("Glob: base_path does not exist: %s", absBase),
				IsError: true,
			}
		}
		return ToolResult{
			Text:    fmt.Sprintf("Glob: %v", err),
			IsError: true,
		}
	}
	if !info.IsDir() {
		return ToolResult{
			Text:    fmt.Sprintf("Glob: base_path is not a directory: %s", absBase),
			IsError: true,
		}
	}

	// filepath.Glob with a non-absolute pattern searches
	// relative to the current working dir of the Go process,
	// not the ToolContext's working dir. We want the latter
	// so the model can pass patterns like "internal/**/*.go"
	// without re-prefixing the working dir. So we make the
	// pattern absolute ourselves.
	pattern := in.Pattern
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(absBase, pattern)
	}

	if err := ctx.Err(); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Glob: %v", err),
			IsError: true,
		}
	}

	// We use filepath.Glob for the "no double-star" case
	// and a manual walk for the `**` case, because Go's
	// stdlib `filepath.Glob` has the well-known limitation
	// that `**` only matches a single path segment. The
	// spec says "**/*.go" must match `a/b/c.go`; stdlib
	// alone won't do that. Rather than pull in a third-
	// party glob library, we walk the tree ourselves.
	var matches []string
	if strings.Contains(pattern, "**") {
		matches = globDoubleStar(absBase, in.Pattern)
	} else {
		var err error
		matches, err = filepath.Glob(pattern)
		if err != nil {
			// filepath.ErrBadPattern is the only documented
			// error; surface it cleanly.
			return ToolResult{
				Text:    fmt.Sprintf("Glob: bad pattern %q: %v", in.Pattern, err),
				IsError: true,
			}
		}
	}

	// Stat each match, drop directories, collect (relPath,
	// mtime). Errors from stat are tolerated (file may have
	// been deleted between Glob and Stat); the match is
	// dropped silently.
	results := make([]matchResult, 0, len(matches))
	for _, m := range matches {
		if err := ctx.Err(); err != nil {
			return ToolResult{
				Text:    fmt.Sprintf("Glob: %v", err),
				IsError: true,
			}
		}
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			continue
		}
		rel, err := filepath.Rel(absBase, m)
		if err != nil {
			// Shouldn't happen for paths we just glob'd
			// from the same base, but be defensive.
			rel = m
		}
		// Use forward slashes in the output regardless of
		// platform; the model consumes these.
		rel = filepath.ToSlash(rel)
		results = append(results, matchResult{
			relPath: rel,
			mtime:   fi.ModTime().UnixNano(),
		})
	}

	// Sort by most-recent-mtime first.
	sort.Slice(results, func(i, j int) bool {
		return results[i].mtime > results[j].mtime
	})

	truncated := len(results) > globMaxResults
	if truncated {
		results = results[:globMaxResults]
	}

	if len(results) == 0 {
		return ToolResult{
			Text:    fmt.Sprintf("Glob: no files matched %q", in.Pattern),
			IsError: true,
			Metadata: map[string]any{
				"count":     0,
				"truncated": false,
			},
		}
	}

	// Format. One path per line, in the order the model
	// wants (most-recent first).
	var sb strings.Builder
	for i, r := range results {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(r.relPath)
	}
	text := sb.String()
	if truncated {
		text += fmt.Sprintf("\n[truncated: showing %d of %d matches]", globMaxResults, globMaxResults+1)
	}

	return ToolResult{
		Text: text,
		Metadata: map[string]any{
			"count":     len(results),
			"truncated": truncated,
		},
	}
}
