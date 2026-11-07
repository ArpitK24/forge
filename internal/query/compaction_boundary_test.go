package query

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

// --- Bug 4: compaction never orphans a tool_result ----------------

func toolUseMsg(id string) core.Message {
	return core.NewAssistantBlocks(core.ContentBlock{
		Kind:    core.BlockToolUse,
		ToolUse: &core.ToolUse{ID: id, Name: "Bash", Input: []byte(`{}`)},
	})
}

func toolResultMsg(id string) core.Message {
	return core.NewUserBlocks(core.ContentBlock{
		Kind:       core.BlockToolResult,
		ToolResult: &core.ToolResult{ToolUseID: id, Content: json.RawMessage(`"ok"`)},
	})
}

func TestCompactConversationKeepsToolPairTogether(t *testing.T) {
	// 15 messages; the naive cut (len-10 = 5) lands on a
	// tool_result whose tool_use is message 4.
	msgs := []core.Message{
		core.NewUserText("start"),  // 0
		core.NewAssistantText("a"), // 1
		core.NewUserText("u"),      // 2
		core.NewAssistantText("a"), // 3
		toolUseMsg("call_x"),       // 4
		toolResultMsg("call_x"),    // 5 <- naive cutoff
	}
	for len(msgs) < 15 {
		if len(msgs)%2 == 0 {
			msgs = append(msgs, core.NewUserText("u"))
		} else {
			msgs = append(msgs, core.NewAssistantText("a"))
		}
	}

	out, err := CompactConversation(context.Background(),
		fakeSummaryScript("summary"), msgs, "fake-model", "fake-model")
	if err != nil {
		t.Fatalf("CompactConversation err = %v", err)
	}
	// Every tool_result in the output must follow a message
	// carrying its tool_use.
	seen := map[string]bool{}
	for i, m := range out {
		for _, tu := range m.ToolUses() {
			seen[tu.ID] = true
		}
		if m.Content.IsString {
			continue
		}
		for _, b := range m.Content.Blocks {
			if b.Kind == core.BlockToolResult && !seen[b.ToolResult.ToolUseID] {
				t.Errorf("message %d: tool_result %q has no preceding tool_use (orphaned)",
					i, b.ToolResult.ToolUseID)
			}
		}
	}
}
