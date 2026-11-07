package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ArpitK24/forge/internal/api"
	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/query"
)

// twoWritesThenText scripts one assistant turn with two Write
// tool calls (two permission prompts in one turn), then a text
// turn that ends the loop.
func twoWritesThenText(dir string) [][]api.StreamEvent {
	write := func(idx int, id, name string) api.StreamEvent {
		path, _ := json.Marshal(filepath.Join(dir, name))
		in := `{"file_path":` + string(path) + `,"content":"x"}`
		return api.EventOfBlockStart(idx, core.ContentBlock{
			Kind:    core.BlockToolUse,
			ToolUse: &core.ToolUse{ID: id, Name: core.ToolWrite, Input: []byte(in)},
		})
	}
	return [][]api.StreamEvent{
		{
			api.EventOfMessageStart("fake-model", nil),
			write(0, "call_a", "a.txt"),
			api.EventOfBlockStop(0),
			write(1, "call_b", "b.txt"),
			api.EventOfBlockStop(1),
			api.EventOfMessageDelta(api.StopToolUse, nil),
			api.EventOfMessageStop(),
		},
		api.ScriptTextResponse("both written"),
	}
}

// TestTUI_TwoPermissionPromptsInOneTurn: the second tool call in
// a turn needs its own dialog. Before the fix respondPermission
// never re-armed the permission subscription, so the loop
// blocked forever on the second request.
func TestTUI_TwoPermissionPromptsInOneTurn(t *testing.T) {
	dir := t.TempDir()
	provider := api.NewFakeProvider(twoWritesThenText(dir)...)
	d := newTeaDriver(t, newLoopModel(provider, dir))

	dialogs := 0
	allow := func(d *teaDriver) {
		if d.m.PermissionDialog != nil {
			dialogs++
			d.send(tea.KeyMsg{Type: tea.KeyEnter}) // Allow is focused by default
		}
	}
	d.submit("write two files")
	if !d.pump(5*time.Second, turnDone, allow) {
		t.Fatalf("turn with two permission prompts never finished: dialogs=%d Streaming=%v Status=%q",
			dialogs, d.m.Streaming, d.m.Status)
	}
	if dialogs != 2 {
		t.Errorf("dialogs shown = %d, want 2", dialogs)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
	if d.m.Status != "Ready" {
		t.Errorf("Status = %q, want Ready", d.m.Status)
	}
}

// TestUpdate_LoopEventWhileDialogOpenKeepsSubscription: a loop
// event that arrives while the permission dialog is open must be
// handled and the event subscription re-armed. Before the fix
// Update dropped it and returned no Cmd, so the TUI stopped
// listening to the loop for the rest of the turn.
func TestUpdate_LoopEventWhileDialogOpenKeepsSubscription(t *testing.T) {
	m := newDialogModel()
	ch := make(chan query.Event, 1)
	m.currentEventCh = ch
	openDialog(t, m, "Bash", "echo hi")

	next, cmd := m.Update(queryEventMsg(query.StatusEvent{Message: "working"}))
	got := next.(Model)
	if got.PermissionDialog == nil {
		t.Error("dialog should stay open")
	}
	if got.Status != "working" {
		t.Errorf("Status = %q, want the event's status", got.Status)
	}
	assertResubscribed(t, cmd, ch)
}

// TestUpdate_LoopEventWhilePickerOpenKeepsSubscription: same as
// above for the sessions picker modal.
func TestUpdate_LoopEventWhilePickerOpenKeepsSubscription(t *testing.T) {
	m := newDialogModel()
	ch := make(chan query.Event, 1)
	m.currentEventCh = ch
	m.SessionPicker = &SessionPickerState{}

	next, cmd := m.Update(queryEventMsg(query.StatusEvent{Message: "working"}))
	if next.(Model).SessionPicker == nil {
		t.Error("picker should stay open")
	}
	assertResubscribed(t, cmd, ch)
}

func assertResubscribed(t *testing.T, cmd tea.Cmd, ch chan query.Event) {
	t.Helper()
	if cmd == nil {
		t.Fatal("Update returned no Cmd: the loop-event subscription was dropped")
	}
	ch <- query.StatusEvent{Message: "next"}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		if _, ok := msg.(queryEventMsg); !ok {
			t.Fatalf("re-armed Cmd returned %T, want queryEventMsg", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("re-armed Cmd did not read the next event")
	}
}

// TestRespondPermission_ReArmsPermissionSubscription: answering a
// dialog must return a Cmd that waits for the loop's next
// permission request.
func TestRespondPermission_ReArmsPermissionSubscription(t *testing.T) {
	m := newDialogModel()
	permCh := make(chan permRequest, 1)
	m.bridge = &programState{PermReqCh: permCh}
	openDialog(t, m, "Bash", "echo one")

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("respondPermission returned no Cmd: the next permission request would never be read")
	}
	permCh <- permRequest{
		Request:   core.PermissionRequest{ToolName: "Bash", Description: "echo two"},
		RespondCh: make(chan core.PermissionDecision, 1),
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		pm, ok := msg.(permissionRequestMsg)
		if !ok {
			t.Fatalf("re-armed Cmd returned %T, want permissionRequestMsg", msg)
		}
		if pm.req.Request.Description != "echo two" {
			t.Errorf("got request %q, want the second one", pm.req.Request.Description)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("re-armed Cmd did not read the next permission request")
	}
}
