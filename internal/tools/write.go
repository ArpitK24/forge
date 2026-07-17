package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ArpitK24/forge/internal/core"
)

// writeInputSchema is the JSON Schema for the Write tool.
var writeInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "Absolute or relative (to working dir) path of the file to write."
    },
    "content": {
      "type": "string",
      "description": "The full file contents to write. Existing files are overwritten."
    }
  },
  "required": ["file_path", "content"],
  "additionalProperties": false
}`)

// WriteTool is the implementation of the Write tool.
// Spec §3.2 (file tools).
type WriteTool struct{}

// Name implements Tool.
func (w *WriteTool) Name() string { return core.ToolWrite }

// Description implements Tool.
func (w *WriteTool) Description() string {
	return "Write a file to the local filesystem. Creates parent " +
		"directories as needed. Overwrites existing files. Returns " +
		"the line and byte counts."
}

// PermissionLevel implements Tool. Write mutates the
// filesystem, so it requires write permission.
func (w *WriteTool) PermissionLevel() core.PermissionLevel { return core.PermWrite }

// InputSchema implements Tool.
func (w *WriteTool) InputSchema() json.RawMessage { return writeInputSchema }

// WriteInput is the JSON-decoded input shape.
type WriteInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// Execute writes the file. Behavior:
//
//   - Resolve `file_path` to an absolute path.
//   - Create the parent directory (and any missing
//     ancestors) with mode 0o755 — same as `mkdir -p`. We
//     don't try to be clever about preserving the mode of
//     an existing parent dir; `mkdir -p` semantics is the
//     spec contract.
//   - Write the file with mode 0o644 (or whatever the OS
//     umask narrows it to). Phase 3 doesn't expose a mode
//     knob; a later phase may add one for executable
//     scripts.
//   - On success: ToolResult{Text, Metadata: {"bytes_written",
//     "lines_written", "file_path"}}.
//   - On error: ToolResult{IsError: true, Text: "..."}.
func (w *WriteTool) Execute(ctx context.Context, input json.RawMessage, tc *ToolContext) ToolResult {
	var in WriteInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Write: invalid input JSON: %v", err),
			IsError: true,
		}
	}
	if in.FilePath == "" {
		return ToolResult{
			Text:    "Write: file_path is required",
			IsError: true,
		}
	}

	abs, err := resolveToolPath(tc, in.FilePath)
	if err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Write: invalid path %q: %v", in.FilePath, err),
			IsError: true,
		}
	}

	// Honor ctx before doing any FS work. A Write that
	// gets cancelled mid-way can leave a half-written file;
	// failing fast is more diagnostic.
	if err := ctx.Err(); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Write: %v", err),
			IsError: true,
		}
	}

	// Create parent directories. filepath.Dir of "foo.txt"
	// is "." — MkdirAll(".") is a no-op on every OS, so we
	// don't need a special case.
	parent := filepath.Dir(abs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Write: mkdir %s: %v", parent, err),
			IsError: true,
		}
	}

	// Write the file. We use WriteFile (truncate + write +
	// close) rather than Open+Truncate so the model can
	// call Write repeatedly with no "first call creates,
	// subsequent calls append" surprise.
	if err := os.WriteFile(abs, []byte(in.Content), 0o644); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Write: %v", err),
			IsError: true,
		}
	}

	// Count lines. The contract is "the line count the
	// model would see if it Read the file back". Empty
	// content → 0 lines; one trailing newline → 1 line.
	lines := countLines(in.Content)

	return ToolResult{
		Text: fmt.Sprintf("wrote %d bytes (%d lines) to %s", len(in.Content), lines, abs),
		Metadata: map[string]any{
			"file_path":     abs,
			"bytes_written": len(in.Content),
			"lines_written": lines,
		},
	}
}

// countLines returns the number of physical lines in s.
// Empty string → 0. "a\n" → 1. "a\nb" → 2. "a\nb\n" → 2.
//
// A "line" is either terminated by a newline, or is the
// final line that doesn't have a trailing newline. This
// matches `wc -l` on Unix and what every editor shows.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	// If the file doesn't end in \n, the last "line"
	// wasn't counted by the loop above; add it.
	if s[len(s)-1] != '\n' {
		n++
	}
	return n
}
