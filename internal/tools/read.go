package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
)

// readInputSchema is the JSON Schema for the Read tool's
// input. Hand-written (stable, small) so we can iterate on
// the model's view of the tool without bumping the Go types.
var readInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "file_path": {
      "type": "string",
      "description": "Absolute or relative (to working dir) path to the file to read."
    },
    "offset": {
      "type": "integer",
      "description": "Optional 0-based line number to start reading from. Defaults to 0.",
      "minimum": 0
    },
    "limit": {
      "type": "integer",
      "description": "Optional maximum number of lines to return. Defaults to 2000.",
      "minimum": 1
    }
  },
  "required": ["file_path"],
  "additionalProperties": false
}`)

// readDefaultLimit is the default cap on lines returned per
// call. The model can override with `limit`; this is the
// safety net so a single Read on a 5M-line file doesn't
// blow the context.
const readDefaultLimit = 2000

// readBinarySentinelByte is the byte value we use to decide
// a file is binary. The NUL byte is the textbook signal:
// real text files essentially never contain one, while
// almost every binary format (ELF, PE, PNG, ZIP, even
// UTF-16 with BOM) does.
const readBinarySentinelByte byte = 0x00

// ReadTool is the implementation of the Read tool.
// Spec §3.2 (file tools).
type ReadTool struct{}

// Name implements Tool.
func (r *ReadTool) Name() string { return core.ToolRead }

// Description implements Tool.
func (r *ReadTool) Description() string {
	return "Read a file from the local filesystem. Returns content as " +
		"line-numbered text. Supports offset/limit for paging through " +
		"large files. Errors on binary content."
}

// PermissionLevel implements Tool.
func (r *ReadTool) PermissionLevel() core.PermissionLevel { return core.PermReadOnly }

// InputSchema implements Tool.
func (r *ReadTool) InputSchema() json.RawMessage { return readInputSchema }

// ReadInput is the JSON-decoded input shape for the Read
// tool.
type ReadInput struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// Execute reads the file. Behavior:
//
//   - Resolve `file_path` to an absolute path (against
//     tc.WorkingDir / tc.ResolvePath).
//   - Read the file. Errors on missing / permission denied /
//     is-a-directory.
//   - Detect binary content by scanning for NUL bytes in the
//     first 8 KiB. If found, return IsError=true (we don't
//     hand binary content to the model — the Edit/Write tools
//     would corrupt it).
//   - Split into lines (keeping line endings out of the
//     output), apply offset/limit, and format each line as
//     `{line_number}\t{content}` where line_number is 1-based
//     (matching what every editor shows).
//   - On success: ToolResult{Text, Metadata: {"total_lines",
//     "returned_lines", "truncated"}}.
//   - On error: ToolResult{IsError: true, Text: "..."}.
func (r *ReadTool) Execute(ctx context.Context, input json.RawMessage, tc *ToolContext) ToolResult {
	var in ReadInput
	if err := json.Unmarshal(input, &in); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Read: invalid input JSON: %v", err),
			IsError: true,
		}
	}
	if in.FilePath == "" {
		return ToolResult{
			Text:    "Read: file_path is required",
			IsError: true,
		}
	}

	// Resolve the path. The ToolContext may supply a
	// ResolvePath helper (the loop sets one up that consults
	// the working dir); fall back to a plain
	// filepath.Abs+working-dir join so this tool is usable
	// in unit tests where ResolvePath is nil.
	abs, err := resolveToolPath(tc, in.FilePath)
	if err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Read: invalid path %q: %v", in.FilePath, err),
			IsError: true,
		}
	}

	// Stat first so we can give a clean error if the path is
	// a directory (os.ReadFile would return a less-helpful
	// "is a directory" on Unix; on Windows it's even worse).
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return ToolResult{
				Text:    fmt.Sprintf("Read: file not found: %s", abs),
				IsError: true,
			}
		}
		return ToolResult{
			Text:    fmt.Sprintf("Read: %v", err),
			IsError: true,
		}
	}
	if info.IsDir() {
		return ToolResult{
			Text:    fmt.Sprintf("Read: %s is a directory; use Glob/Grep instead", abs),
			IsError: true,
		}
	}

	// Read the whole file. The 8 KiB binary check happens
	// first; if we detect binary, we don't need to slurp the
	// rest.
	const sniffSize = 8 * 1024
	f, err := os.Open(abs)
	if err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Read: %v", err),
			IsError: true,
		}
	}
	defer f.Close()

	// Honor ctx cancellation as we read. ctx.Err() check
	// between the sniff and the full read is a courtesy;
	// most files are small enough that a single ReadAll
	// wins.
	if err := ctx.Err(); err != nil {
		return ToolResult{
			Text:    fmt.Sprintf("Read: %v", err),
			IsError: true,
		}
	}

	// Sniff for binary by reading up to sniffSize bytes and
	// scanning for NUL.
	var sniffBuf [sniffSize]byte
	n, _ := f.Read(sniffBuf[:])
	if bytes.IndexByte(sniffBuf[:n], readBinarySentinelByte) >= 0 {
		return ToolResult{
			Text:    fmt.Sprintf("Read: %s looks like a binary file; refusing to return its contents", abs),
			IsError: true,
		}
	}

	// Read the rest. We could rewind and slurp the whole
	// file; for large files (>sniffSize) we'd rather just
	// read from the current offset.
	var rest []byte
	if n < sniffSize {
		rest = sniffBuf[:n]
	} else {
		// Already at EOF after sniffSize bytes? Cheap check.
		var tmp [4096]byte
		k, _ := f.Read(tmp[:])
		if k == 0 {
			rest = sniffBuf[:n]
		} else {
			// File is larger than the sniff; concatenate
			// sniff + remainder.
			rest = make([]byte, n, info.Size())
			copy(rest, sniffBuf[:n])
			for {
				if err := ctx.Err(); err != nil {
					return ToolResult{
						Text:    fmt.Sprintf("Read: %v", err),
						IsError: true,
					}
				}
				m, rerr := f.Read(tmp[:])
				if m > 0 {
					rest = append(rest, tmp[:m]...)
				}
				if rerr != nil {
					break
				}
			}
		}
	}

	// Split into lines. The contract is `{n}\t{content}` per
	// line, no trailing newline on each entry, and the line
	// count must match what an editor would show.
	//
	// strings.Split("\n") on a file ending in "\n" gives one
	// too many entries (a trailing ""), so we either strip
	// it (when the file ended in \n) or leave it (when it
	// didn't). The result is "n lines" for a file with n
	// physical lines, regardless of the final-newline state.
	var lines []string
	if len(rest) == 0 {
		lines = []string{}
	} else {
		lines = strings.Split(string(rest), "\n")
		// If the file ended with \n, Split produced a
		// trailing ""; drop it.
		if lines[len(lines)-1] == "" && strings.HasSuffix(string(rest), "\n") {
			lines = lines[:len(lines)-1]
		}
		// Also strip a trailing \r on each line so CRLF
		// files don't show ^M at the end of every line.
		for i, l := range lines {
			lines[i] = strings.TrimRight(l, "\r")
		}
	}

	total := len(lines)

	// Apply offset (clamp to total). Negative offset would
	// be a programming error in the model; treat as 0.
	start := in.Offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}

	// Apply limit (defaulting to readDefaultLimit).
	limit := in.Limit
	if limit <= 0 {
		limit = readDefaultLimit
	}

	end := start + limit
	if end > total {
		end = total
	}
	window := lines[start:end]
	truncated := end < total

	// Format as `{line_number}\t{content}`. Line numbers are
	// 1-based and skip the offset (so the model can match
	// what `head -n +5 file` would show).
	var sb strings.Builder
	for i, line := range window {
		// 1-based line number = (start+1) + i.
		fmt.Fprintf(&sb, "%d\t%s\n", start+i+1, line)
	}

	return ToolResult{
		Text: sb.String(),
		Metadata: map[string]any{
			"file_path":      abs,
			"total_lines":    total,
			"returned_lines": len(window),
			"offset":         start,
			"truncated":      truncated,
		},
	}
}

// resolveToolPath resolves a possibly-relative path against
// the ToolContext's working dir. Uses tc.ResolvePath if set;
// otherwise joins with the working dir and calls filepath.Abs.
// Returns the absolute, cleaned path.
func resolveToolPath(tc *ToolContext, p string) (string, error) {
	if tc != nil && tc.ResolvePath != nil {
		return tc.ResolvePath(p)
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	wd := "."
	if tc != nil && tc.WorkingDir != "" {
		wd = tc.WorkingDir
	}
	abs, err := filepath.Abs(filepath.Join(wd, p))
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
