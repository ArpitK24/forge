package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools/editutil"
)

// editInputSchema is the JSON Schema for the Edit tool.
var editInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "Absolute or relative (to working dir) path of the file to edit."
    },
    "old_string": {
      "type": "string",
      "description": "The exact substring to find in the file. Must match uniquely (or replace_all must be true)."
    },
    "new_string": {
      "type": "string",
      "description": "The substring to replace it with."
    },
    "replace_all": {
      "type": "boolean",
      "description": "If true, replace every occurrence. If false (default), old_string must match exactly once.",
      "default": false
    }
  },
  "required": ["file_path", "old_string", "new_string"],
  "additionalProperties": false
}`)

// EditTool is the implementation of the Edit tool.
// Spec §3.2 (file tools). Performs a unique-string
// find-and-replace on an existing file. Critically, this is
// NOT a line-based editor — old_string may span multiple
// lines and may appear anywhere in the file. The model is
// expected to pass the unique-enough string it wants to
// replace.
type EditTool struct{}

// Name implements Tool.
func (e *EditTool) Name() string { return core.ToolEdit }

// Description implements Tool.
func (e *EditTool) Description() string {
	return "Find and replace a substring in a file. By default, " +
		"old_string must match exactly once in the file; if it " +
		"appears multiple times, the call errors as ambiguous. " +
		"Set replace_all=true to replace every occurrence."
}

// PermissionLevel implements Tool.
func (e *EditTool) PermissionLevel() core.PermissionLevel { return core.PermWrite }

// InputSchema implements Tool.
func (e *EditTool) InputSchema() json.RawMessage { return editInputSchema }

// EditInput is the JSON-decoded input shape.
type EditInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

// Execute performs the edit. Behavior:
//
//   - Resolve `file_path` to an absolute path.
//   - Reject the call if `old_string == new_string` (a no-op
//     that the model shouldn't be making; the error message
//     tells it so).
//   - Reject the call if `old_string` is empty (every
//     position is a match — undefined behavior).
//   - Read the file. Error if missing / permission denied /
//     a directory.
//   - Count occurrences of old_string using
//     editutil.CountOccurrences (the shared helper — do not
//     reimplement).
//   - If count == 0: error "old_string not found in file".
//   - If count > 1 and !ReplaceAll: error as ambiguous.
//   - Otherwise: replace. Write back. Return success with
//     Metadata {"replacements": N, "file_path": ...}.
//   - On error: ToolResult{IsError: true, Text: "..."}.
func (e *EditTool) Execute(ctx context.Context, input json.RawMessage, tc *ToolContext) ToolResult {
	var in EditInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Edit: invalid input JSON: %v", err),
			IsError: true,
		}
	}
	if in.FilePath == "" {
		return ToolResult{
			Text:    "Edit: file_path is required",
			IsError: true,
		}
	}
	if in.OldString == "" {
		return ToolResult{
			Text:    "Edit: old_string is required (empty strings match every position; pass the actual substring you want to replace)",
			IsError: true,
		}
	}
	if in.OldString == in.NewString {
		return ToolResult{
			Text:    "Edit: old_string and new_string are identical; this would be a no-op",
			IsError: true,
		}
	}

	abs, err := resolveToolPath(tc, in.FilePath)
	if err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Edit: invalid path %q: %v", in.FilePath, err),
			IsError: true,
		}
	}

	if err := ctx.Err(); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Edit: %v", err),
			IsError: true,
		}
	}

	// Read existing file. Stat first so we can give a
	// clean error if it's a directory (ReadFile returns a
	// "is a directory" message that doesn't tell the model
	// to use a different tool).
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolResult{
				Text:    fmt.Sprintf("Edit: file not found: %s", abs),
				IsError: true,
			}
		}
		return ToolResult{
			Text:    fmt.Sprintf("Edit: %v", err),
			IsError: true,
		}
	}
	if info.IsDir() {
		return ToolResult{
			Text:    fmt.Sprintf("Edit: %s is a directory", abs),
			IsError: true,
		}
	}

	contents, err := os.ReadFile(abs)
	if err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Edit: %v", err),
			IsError: true,
		}
	}

	// Count matches. The disambiguation rule is enforced
	// here.
	count := editutil.CountOccurrences(string(contents), in.OldString)
	if count == 0 {
		return ToolResult{
			Text:    fmt.Sprintf("Edit: old_string not found in %s", abs),
			IsError: true,
		}
	}
	if count > 1 && !in.ReplaceAll {
		return ToolResult{
			Text: fmt.Sprintf("Edit: old_string appears %d times in %s; the call is ambiguous. "+
				"Either pass more surrounding context in old_string so it matches exactly once, "+
				"or set replace_all=true to replace every occurrence.", count, abs),
			IsError: true,
		}
	}

	// Do the replacement. With ReplaceAll, strings.ReplaceAll
	// does the right thing (no overlapping matches; non-
	// overlapping is the convention). Without, we have
	// exactly one match — strings.Replace with n=1 is fine.
	var updated string
	var replacements int
	if in.ReplaceAll {
		updated = strings.ReplaceAll(string(contents), in.OldString, in.NewString)
		// ReplaceAll returns the count it would have made;
		// we can recompute it from the inputs.
		replacements = count
	} else {
		updated = strings.Replace(string(contents), in.OldString, in.NewString, 1)
		replacements = 1
	}

	if err := os.WriteFile(abs, []byte(updated), info.Mode()); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Edit: write: %v", err),
			IsError: true,
		}
	}

	return ToolResult{
		Text: fmt.Sprintf("replaced %d occurrence(s) in %s", replacements, abs),
		Metadata: map[string]any{
			"file_path":    abs,
			"replacements": replacements,
		},
	}
}
