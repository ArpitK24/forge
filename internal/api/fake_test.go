package api

import (
	"context"
	"testing"
	"time"

	"github.com/ArpitK24/forge/internal/core"
)

func TestFakeProviderTextOnly(t *testing.T) {
	fp := NewFakeProvider(ScriptTextResponse("hello world"))
	events, errs := fp.Stream(context.Background(), Request{Model: "x"})

	var seen []StreamEvent
	for ev := range events {
		seen = append(seen, ev)
	}
	// errs channel should be closed with no errors.
	if err := <-errs; err != nil {
		t.Errorf("unexpected error from errs channel: %v", err)
	}

	if len(seen) == 0 {
		t.Fatalf("no events emitted")
	}
	if seen[0].Kind != EventMessageStart {
		t.Errorf("first event = %v, want EventMessageStart", seen[0].Kind)
	}
	if seen[len(seen)-1].Kind != EventMessageStop {
		t.Errorf("last event = %v, want EventMessageStop", seen[len(seen)-1].Kind)
	}
	// There should be exactly one text delta with the scripted text.
	textFound := false
	for _, ev := range seen {
		if ev.Kind == EventContentBlockDelta && ev.Delta.Kind == DeltaText {
			if ev.Delta.Text == "hello world" {
				textFound = true
			}
		}
	}
	if !textFound {
		t.Errorf("scripted text 'hello world' not found in events")
	}
}

func TestFakeProviderChannelClosesOnStop(t *testing.T) {
	fp := NewFakeProvider(ScriptTextResponse("x"))
	events, _ := fp.Stream(context.Background(), Request{})

	// Drain with a timeout. If the channel doesn't close, the
	// test hangs and `go test -timeout` catches it.
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatalf("events channel did not close after EventMessageStop")
	}
}

func TestFakeProviderScriptsRepeat(t *testing.T) {
	// A single script that's a tool call. The loop will call
	// Stream multiple times; the FakeProvider replays the last
	// script for any overflow call.
	fp := NewFakeProvider(ScriptAlwaysToolCall("Bash", []byte(`{"command":"echo hi"}`))...)
	// Three calls — should all produce the same script.
	for i := 0; i < 3; i++ {
		events, _ := fp.Stream(context.Background(), Request{})
		var stopReason string
		for ev := range events {
			if ev.Kind == EventMessageDelta {
				stopReason = ev.StopReason
			}
		}
		if stopReason != StopToolUse {
			t.Errorf("call %d: stopReason = %q, want %q", i, stopReason, StopToolUse)
		}
	}
}

func TestFakeProviderContextCancel(t *testing.T) {
	// A long-ish script we can cancel mid-stream.
	script := []StreamEvent{
		EventOfMessageStart("fake-model", nil),
		EventOfBlockStart(0, core.TextBlock("")),
	}
	for i := 0; i < 100; i++ {
		script = append(script, EventOfBlockDelta(0, TextDelta("x")))
	}
	script = append(script, EventOfBlockStop(0), EventOfMessageStop())

	fp := NewFakeProvider(script)
	ctx, cancel := context.WithCancel(context.Background())
	events, _ := fp.Stream(ctx, Request{})

	// Read a couple of events, then cancel.
	<-events
	<-events
	cancel()

	// The events channel must close promptly after cancellation.
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatalf("events channel did not close after ctx cancel")
	}
}

func TestFakeProviderNilIsNoPanic(t *testing.T) {
	var fp *FakeProvider
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil FakeProvider panicked: %v", r)
		}
	}()
	info := fp.Info()
	if info.ID == "" {
		t.Errorf("Info on nil FakeProvider returned zero value")
	}
	events, errs := fp.Stream(context.Background(), Request{})
	for range events {
	}
	// errs should have a value (since nil = "no scripted response").
	if err := <-errs; err == nil {
		t.Errorf("expected error from nil FakeProvider, got nil")
	}
}

func TestScriptToolCallThenTextShape(t *testing.T) {
	scripts := ScriptToolCallThenText("Bash", []byte(`{"command":"echo hi"}`), "done")
	if len(scripts) != 2 {
		t.Fatalf("script count = %d, want 2", len(scripts))
	}
	// First script: tool call, stop reason = tool_use.
	first := scripts[0]
	gotToolUse := false
	for _, ev := range first {
		if ev.Kind == EventContentBlockStart && ev.Block.Kind == core.BlockToolUse {
			gotToolUse = true
		}
	}
	if !gotToolUse {
		t.Errorf("first script missing tool-use block")
	}

	// Second script: text-only.
	second := scripts[1]
	gotText := false
	for _, ev := range second {
		if ev.Kind == EventContentBlockDelta && ev.Delta.Text == "done" {
			gotText = true
		}
	}
	if !gotText {
		t.Errorf("second script missing 'done' text delta")
	}
}
