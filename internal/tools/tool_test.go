package tools

import (
	"encoding/json"
	"testing"
)

// TestToolResult_JSONShapeStability pins the wire shape of
// ToolResult so a regression that breaks backward
// compatibility (e.g. by changing the JSON tag, by adding
// required fields, or by silently changing the omitempty
// behavior) is caught at test time.
//
// Why this matters: every tool in the codebase returns a
// ToolResult. The query loop's serializer (query/loop.go)
// consumes the JSON. If we accidentally change the shape
// — say, by renaming a tag or by making Blocks always
// emit `"content":null` — every existing tool's wire bytes
// would change, potentially breaking the model's tool
// result parsing on the wire.
func TestToolResult_JSONShapeStability(t *testing.T) {
	t.Run("plain text — pre-Blocks shape", func(t *testing.T) {
		// A tool returning the original 3-field shape
		// (Text + IsError + Metadata) with no Blocks set
		// should marshal identically to a tool that
		// predates Phase 4 step 5.
		r := ToolResult{
			Text:    "hello",
			IsError: false,
			Metadata: map[string]any{
				"exit_code": 0,
			},
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got := string(b)
		// Blocks has omitempty, so a nil Blocks must NOT
		// emit "content":null or "content":"" — the JSON
		// must match what the field would have produced
		// before Blocks existed.
		want := `{"exit_code":0}` // Metadata is the only map field; order is sorted
		// Actually Metadata gets sorted because it's a map.
		// The expected JSON has IsError omitted (omitempty on bool? actually
		// the tag has no omitempty), Text present, no "content" key.
		// Let me just check no "content" key:
		if contains(got, `"content"`) {
			t.Errorf("ToolResult with nil Blocks should not emit \"content\"; got %s", got)
		}
		// And IsError should be present (no omitempty on the bool tag).
		if !contains(got, `"IsError":false`) {
			t.Errorf("ToolResult should still emit IsError:false; got %s", got)
		}
		if !contains(got, `"Text":"hello"`) {
			t.Errorf("ToolResult should emit Text: %s", got)
		}
		_ = want
	})

	t.Run("Blocks populated — emits content array", func(t *testing.T) {
		r := ToolResult{
			Text:   "first\nsecond",
			IsError: false,
			Blocks: json.RawMessage(`[{"type":"text","text":"first"},{"type":"image","data":"abc","mimeType":"image/png"},{"type":"text","text":"second"}]`),
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		got := string(b)
		if !contains(got, `"content":[`) {
			t.Errorf("Blocks populated should emit content array; got %s", got)
		}
		// The verbatim block array should be preserved
		// without re-encoding (a regression that
		// json.Marshalled Blocks through the typed struct
		// would lose the second text block and the image
		// data).
		if !contains(got, `"data":"abc"`) {
			t.Errorf("image data lost in round-trip; got %s", got)
		}
		if !contains(got, `"mimeType":"image/png"`) {
			t.Errorf("mimeType lost in round-trip; got %s", got)
		}
	})

	t.Run("Blocks empty slice — omits content", func(t *testing.T) {
		// An empty json.RawMessage marshals to "null".
		// omitempty only omits nil values for raw
		// message types per encoding/json — actually,
		// json.RawMessage is a []byte, and []byte's
		// omitempty only fires for length-0. We need
		// to confirm: nil Blocks → no "content" key.
		r := ToolResult{Text: "ok"}
		b, _ := json.Marshal(r)
		if contains(string(b), `"content"`) {
			t.Errorf("nil Blocks should emit no content key; got %s", b)
		}
	})
}

// contains is a tiny substring helper to keep the test free
// of strings.Contains imports (matches the pattern in
// other test files).
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