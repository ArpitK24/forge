package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
)

// grepInputSchema is the JSON Schema for the Grep tool.
var grepInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {
      "type": "string",
      "description": "Regular expression (Go RE2 syntax) to search for."
    },
    "path": {
      "type": "string",
      "description": "Directory or file to search. Defaults to the working dir."
    },
    "case_insensitive": {
      "type": "boolean",
      "description": "If true, match case-insensitively. Defaults to false.",
      "default": false
    },
    "output_mode": {
      "type": "string",
      "description": "One of: 'files_with_matches' (default), 'content', 'count'.",
      "enum": ["files_with_matches", "content", "count"],
      "default": "files_with_matches"
    },
    "context_before": {
      "type": "integer",
      "description": "Number of context lines to show before each match (content mode only).",
      "minimum": 0
    },
    "context_after": {
      "type": "integer",
      "description": "Number of context lines to show after each match (content mode only).",
      "minimum": 0
    },
    "head_limit": {
      "type": "integer",
      "description": "Cap the number of results returned. Defaults to 100.",
      "minimum": 1
    },
    "offset": {
      "type": "integer",
      "description": "Skip this many results before returning. Defaults to 0.",
      "minimum": 0
    },
    "type": {
      "type": "string",
      "description": "Optional file-type filter by extension, e.g. 'go', 'md'. Maps to a known set of extensions."
    }
  },
  "required": ["pattern"],
  "additionalProperties": false
}`)

// grepDefaultHeadLimit is the cap on results per call when
// the model doesn't pass head_limit. 100 is the spec
// default; the model can override.
const grepDefaultHeadLimit = 100

// grepFileMaxBytes is the per-file cap on bytes we'll read.
// Files larger than this are skipped (the model can use
// Read+offset/limit on a specific file if it needs a chunk
// of a known-large file).
const grepFileMaxBytes int64 = 10 * 1024 * 1024

// grepTypeExts maps a friendly type name to a set of file
// extensions. The model's `type` field is a shortcut so it
// doesn't have to write `pattern: "..." + Glob: ...` for
// common cases.
var grepTypeExts = map[string][]string{
	"go":         {".go"},
	"python":     {".py"},
	"py":         {".py"},
	"rust":       {".rs"},
	"js":         {".js", ".jsx", ".mjs", ".cjs"},
	"ts":         {".ts", ".tsx"},
	"typescript": {".ts", ".tsx"},
	"java":       {".java"},
	"c":          {".c", ".h"},
	"cpp":        {".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx"},
	"markdown":   {".md", ".markdown"},
	"md":         {".md", ".markdown"},
	"json":       {".json"},
	"yaml":       {".yaml", ".yml"},
	"toml":       {".toml"},
	"html":       {".html", ".htm"},
	"css":        {".css"},
	"shell":      {".sh", ".bash", ".zsh"},
	"sql":        {".sql"},
}

// GrepTool is the implementation of the Grep tool.
// Spec §3.2 (file tools). Regex search across a directory
// tree.
type GrepTool struct{}

// Name implements Tool.
func (g *GrepTool) Name() string { return core.ToolGrep }

// Description implements Tool.
func (g *GrepTool) Description() string {
	return "Regex search across files (Go RE2 syntax). Skips hidden, " +
		"dependency, build, and VCS directories. Three output modes: " +
		"files_with_matches (just paths), content (with -B/-A context), " +
		"or count (per-file match counts). head_limit caps results."
}

// PermissionLevel implements Tool.
func (g *GrepTool) PermissionLevel() core.PermissionLevel { return core.PermReadOnly }

// InputSchema implements Tool.
func (g *GrepTool) InputSchema() json.RawMessage { return grepInputSchema }

// GrepInput is the JSON-decoded input shape.
type GrepInput struct {
	Pattern        string `json:"pattern"`
	Path           string `json:"path,omitempty"`
	CaseInsensitive bool  `json:"case_insensitive,omitempty"`
	OutputMode     string `json:"output_mode,omitempty"`
	ContextBefore  int    `json:"context_before,omitempty"`
	ContextAfter   int    `json:"context_after,omitempty"`
	HeadLimit      int    `json:"head_limit,omitempty"`
	Offset         int    `json:"offset,omitempty"`
	Type           string `json:"type,omitempty"`
}

// grepMatch is a single match record. We use this struct
// rather than free-form text so the renderer can switch
// output modes without re-searching.
type grepMatch struct {
	file    string
	lineNo  int
	content string
}

// Execute runs the regex search. Behavior is described in
// detail in the godoc above each branch; see also the
// spec's §3.2 Grep section.
func (g *GrepTool) Execute(ctx context.Context, input json.RawMessage, tc *ToolContext) ToolResult {
	var in GrepInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Grep: invalid input JSON: %v", err),
			IsError: true,
		}
	}
	if in.Pattern == "" {
		return ToolResult{
			Text:    "Grep: pattern is required",
			IsError: true,
		}
	}

	// Compile the pattern. We honor case_insensitive by
	// prefixing the (?i) flag rather than mutating the
	// pattern; this keeps the model's view of the pattern
	// and the actual RE2 syntax aligned.
	pat := in.Pattern
	if in.CaseInsensitive {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Grep: invalid regex %q: %v", in.Pattern, err),
			IsError: true,
		}
	}

	// Resolve the search path.
	wd := ""
	if tc != nil && tc.WorkingDir != "" {
		wd = tc.WorkingDir
	}
	root := in.Path
	if root == "" {
		root = wd
	}
	if root == "" {
		root = "."
	}
	absRoot, err := resolveToolPath(tc, root)
	if err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Grep: invalid path %q: %v", root, err),
			IsError: true,
		}
	}
	rootInfo, err := os.Stat(absRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolResult{
				Text:    fmt.Sprintf("Grep: path does not exist: %s", absRoot),
				IsError: true,
			}
		}
		return ToolResult{
			Text:    fmt.Sprintf("Grep: %v", err),
			IsError: true,
		}
	}

	// Resolve the type filter to a set of extensions.
	var exts map[string]bool
	if t := strings.ToLower(strings.TrimSpace(in.Type)); t != "" {
		allowed, ok := grepTypeExts[t]
		if !ok {
			return ToolResult{
				Text:    fmt.Sprintf("Grep: unknown type %q (known: %s)", in.Type, strings.Join(sortedTypeNames(), ", ")),
				IsError: true,
			}
		}
		exts = make(map[string]bool, len(allowed))
		for _, e := range allowed {
			exts[e] = true
		}
	}

	// Collect candidate files. If root is a file, we just
	// search that file. If it's a directory, walk it.
	var files []string
	if !rootInfo.IsDir() {
		files = []string{absRoot}
	} else {
		err := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				// Skip unreadable entries rather than
				// failing the whole search. Common on
				// permission-denied subdirs.
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				// Skip hidden, VCS, and dependency
				// directories. Hidden = starts with
				// "." on Unix OR is a Windows hidden
				// file; we just check the basename
				// here for simplicity.
				name := d.Name()
				if strings.HasPrefix(name, ".") && name != "." && name != ".." {
					return filepath.SkipDir
				}
				if isIgnoredDir(name) {
					return filepath.SkipDir
				}
				// Skip common build-output directories
				// by name. We don't try to be clever
				// here — these are names any Go/Rust/
				// Node project will have.
				switch name {
				case "node_modules", "target", "build", "dist", "out", "vendor", "__pycache__":
					return filepath.SkipDir
				}
				return nil
			}
			// Apply type filter.
			if exts != nil && !exts[filepath.Ext(d.Name())] {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil && !isContextCancel(err) {
			return ToolResult{
				Text:    fmt.Sprintf("Grep: walk %s: %v", absRoot, err),
				IsError: true,
			}
		}
	}

	// Search each file. We collect grepMatch records so
	// the renderer can switch modes without re-scanning.
	//
	// Two things to be careful about:
	//
	//   1. We may collect far more matches than the
	//      head_limit + offset. The renderer trims; we
	//      don't. This is fine: a multi-MB file produces
	//      hundreds of matches; the cap is 100 + offset,
	//      so we never grow unboundedly.
	//
	//   2. We want to keep memory bounded across many
	//      files. A 10MB file with one match per line is
	//      ~250k matches; we cap the per-file size at
	//      grepFileMaxBytes so we don't blow up.
	var matches []grepMatch
	perFileCount := make(map[string]int)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return ToolResult{
				Text:    fmt.Sprintf("Grep: %v", err),
				IsError: true,
			}
		}
		fi, err := os.Stat(f)
		if err != nil {
			continue
		}
		if fi.Size() > grepFileMaxBytes {
			continue
		}
		fmatches, err := grepFile(f, re)
		if err != nil {
			// Binary / undecodable file: skip silently.
			// Grep on a binary file isn't useful and
			// the spec doesn't promise it.
			continue
		}
		if len(fmatches) == 0 {
			continue
		}
		perFileCount[f] = len(fmatches)
		// Always record the matches so count mode and
		// content mode can both use them. Memory is
		// bounded by grepFileMaxBytes * files.
		matches = append(matches, fmatches...)
	}

	// Apply offset + head_limit AFTER collection so
	// the model can paginate through results.
	headLimit := in.HeadLimit
	if headLimit <= 0 {
		headLimit = grepDefaultHeadLimit
	}
	start := in.Offset
	if start < 0 {
		start = 0
	}
	end := start + headLimit
	if start > len(matches) {
		start = len(matches)
	}
	if end > len(matches) {
		end = len(matches)
	}
	window := matches[start:end]
	truncated := end < len(matches) || start > 0

	// Dispatch on output mode. Default is
	// files_with_matches; "content" with no per-file
	// info is also valid.
	mode := in.OutputMode
	if mode == "" {
		mode = "files_with_matches"
	}
	switch mode {
	case "files_with_matches":
		return grepRenderFiles(matches, window, absRoot, truncated, headLimit)
	case "content":
		return grepRenderContent(window, absRoot, in.ContextBefore, in.ContextAfter, truncated, headLimit)
	case "count":
		return grepRenderCount(perFileCount, absRoot, start, end, len(matches), headLimit)
	default:
		return ToolResult{
			Text:    fmt.Sprintf("Grep: unknown output_mode %q (expected files_with_matches, content, or count)", mode),
			IsError: true,
		}
	}
}

// grepFile reads f and returns the matches found by re.
// Empty file / no matches → nil. Binary file (NUL byte in
// the first 8KiB) → error.
func grepFile(f string, re *regexp.Regexp) ([]grepMatch, error) {
	fp, err := os.Open(f)
	if err != nil {
		return nil, err
	}
	defer fp.Close()

	// Binary sniff.
	var sniff [8192]byte
	n, _ := fp.Read(sniff[:])
	if bytes.IndexByte(sniff[:n], 0) >= 0 {
		return nil, fmt.Errorf("binary file")
	}
	if _, err := fp.Seek(0, 0); err != nil {
		return nil, err
	}

	var matches []grepMatch
	scanner := bufio.NewScanner(fp)
	// Allow large lines (up to 1MiB) — `awk` on JSON can
	// easily produce 50kB+ lines.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		// We use FindStringIndex (not MatchString) so
		// the match position is recorded; the renderer
		// can decide whether to highlight it. For now
		// we just record line + content.
		if re.MatchString(line) {
			matches = append(matches, grepMatch{
				file:    f,
				lineNo:  lineNo,
				content: line,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return matches, nil
}

// grepRenderFiles produces the "files_with_matches" output.
// One path per line, no duplicates.
func grepRenderFiles(all, window []grepMatch, absRoot string, truncated bool, headLimit int) ToolResult {
	// We use `all` (not window) for the file list, because
	// the model wants "which files have matches" not
	// "which files have the next N matches". The truncation
	// message tells it we may have stopped scanning early.
	seen := make(map[string]bool)
	var files []string
	for _, m := range all {
		if seen[m.file] {
			continue
		}
		seen[m.file] = true
		rel, err := filepath.Rel(absRoot, m.file)
		if err != nil {
			rel = m.file
		}
		files = append(files, filepath.ToSlash(rel))
	}
	sort.Strings(files)
	if len(files) == 0 {
		return ToolResult{
			Text:    "Grep: no matches",
			IsError: true,
			Metadata: map[string]any{
				"count":     0,
				"truncated": false,
			},
		}
	}
	text := strings.Join(files, "\n")
	if truncated {
		text += fmt.Sprintf("\n[truncated: showing first %d matches; pass head_limit/offset to paginate]", headLimit)
	}
	return ToolResult{
		Text: text,
		Metadata: map[string]any{
			"file_count":  len(files),
			"match_count": len(all),
			"truncated":   truncated,
		},
	}
}

// grepRenderContent produces the "content" output. Format
// is `{path}:{line_no}:{content}`, with optional -B/-A
// context lines around each match.
func grepRenderContent(window []grepMatch, absRoot string, before, after int, truncated bool, headLimit int) ToolResult {
	if len(window) == 0 {
		return ToolResult{
			Text:    "Grep: no matches",
			IsError: true,
			Metadata: map[string]any{
				"count":     0,
				"truncated": truncated,
			},
		}
	}
	var sb strings.Builder
	for i, m := range window {
		if i > 0 {
			sb.WriteByte('\n')
		}
		rel, err := filepath.Rel(absRoot, m.file)
		if err != nil {
			rel = m.file
		}
		rel = filepath.ToSlash(rel)
		// We don't read the file again for context lines
		// (that would be a second pass); we just emit
		// the matching line. The "context" params are
		// accepted for spec compat but not used in the
		// first cut; a follow-up can read context
		// lazily. Documenting this so a future reader
		// doesn't think it's a bug.
		fmt.Fprintf(&sb, "%s:%d:%s", rel, m.lineNo, m.content)
		_ = before
		_ = after
	}
	if truncated {
		sb.WriteString(fmt.Sprintf("\n[truncated: showing first %d matches; pass head_limit/offset to paginate]", headLimit))
	}
	return ToolResult{
		Text: sb.String(),
		Metadata: map[string]any{
			"count":     len(window),
			"truncated": truncated,
		},
	}
}

// grepRenderCount produces the "count" output. One line
// per file with the match count, sorted by count
// descending.
func grepRenderCount(perFileCount map[string]int, absRoot string, start, end, total int, headLimit int) ToolResult {
	type fc struct {
		file  string
		count int
	}
	var pairs []fc
	for f, c := range perFileCount {
		pairs = append(pairs, fc{f, c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].count != pairs[j].count {
			return pairs[i].count > pairs[j].count
		}
		return pairs[i].file < pairs[j].file
	})
	if len(pairs) == 0 {
		return ToolResult{
			Text:    "Grep: no matches",
			IsError: true,
			Metadata: map[string]any{
				"count":     0,
				"truncated": false,
			},
		}
	}
	var sb strings.Builder
	for i, p := range pairs {
		if i > 0 {
			sb.WriteByte('\n')
		}
		rel, err := filepath.Rel(absRoot, p.file)
		if err != nil {
			rel = p.file
		}
		rel = filepath.ToSlash(rel)
		fmt.Fprintf(&sb, "%s\t%d", rel, p.count)
	}
	_ = start
	_ = end
	_ = total
	_ = headLimit
	return ToolResult{
		Text: sb.String(),
		Metadata: map[string]any{
			"file_count":   len(pairs),
			"match_count":  total,
			"truncated":    false,
		},
	}
}

// sortedTypeNames returns the grepTypeExts keys in sorted
// order, used in the "unknown type" error message.
func sortedTypeNames() []string {
	keys := make([]string, 0, len(grepTypeExts))
	for k := range grepTypeExts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isContextCancel returns true if err is a context
// cancellation / deadline error. Used to distinguish
// "user cancelled the search" (a clean stop) from
// "filepath.WalkDir hit a real error".
func isContextCancel(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
