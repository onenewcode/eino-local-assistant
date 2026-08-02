package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEnqueueFollowUpHelpers(t *testing.T) {
	q, ok := enqueueFollowUp(nil, "  hello  ")
	if !ok || len(q) != 1 || q[0] != "hello" {
		t.Fatalf("enqueue trimmed = %#v ok=%v", q, ok)
	}
	if got := queuePreview("a\n\tb  c"); got != "a b c" {
		t.Fatalf("preview whitespace collapse = %q", got)
	}
	long := strings.Repeat("x", 80)
	if got := queuePreview(long); !strings.HasSuffix(got, "…") {
		t.Fatalf("long preview should truncate: %q", got)
	}
	if !strings.Contains(queuedSystemLine(2, "hi"), "queued (2): hi") {
		t.Fatalf("queued system line formatting")
	}
}

func TestEnterWhileBusyEnqueues(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 1
	m.textarea.SetValue("follow up please")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		// queue path should not start spinner/event cmds
		t.Fatalf("enqueue should not return turn cmds, got %#v", cmd)
	}
	if mm.mode != modeBusy {
		t.Fatalf("mode should stay busy")
	}
	if mm.turnID != 1 {
		t.Fatalf("turnID should not bump on enqueue, got %d", mm.turnID)
	}
	if len(mm.queue) != 1 || mm.queue[0] != "follow up please" {
		t.Fatalf("queue = %#v", mm.queue)
	}
	if strings.TrimSpace(mm.textarea.Value()) != "" {
		t.Fatalf("composer should reset after enqueue")
	}
	if !hasLineContaining(mm.lines, lineSystem, "queued (1):") {
		t.Fatalf("missing queued system line: %#v", mm.lines)
	}
}

func TestFinishTurnDrainsQueue(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 3
	m.queue = []string{"a", "b"}
	// Avoid a real Ask: use a model that EOFs immediately.
	cmd := m.finishTurn(nil)
	if m.mode != modeBusy {
		t.Fatalf("drain of natural-language item should start a turn, mode=%s", modeName(m.mode))
	}
	if m.turnID != 4 {
		t.Fatalf("turnID = %d, want 4", m.turnID)
	}
	if len(m.queue) != 1 || m.queue[0] != "b" {
		t.Fatalf("remaining queue = %#v", m.queue)
	}
	if !hasLineContaining(m.lines, lineUser, "a") {
		t.Fatalf("drained user line missing: %#v", m.lines)
	}
	if cmd == nil {
		t.Fatalf("startTurn should return spinner/event cmds")
	}
	cancelAndWaitForTurn(t, m)
}

func TestFinishTurnDrainsLocalSlashThenTurn(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 1
	m.queue = []string{"/help", "hello"}
	cmd := m.finishTurn(nil)
	if !hasLineContaining(m.lines, lineSystem, "Commands:") {
		t.Fatalf("help should have been drained: %#v", m.lines)
	}
	if m.mode != modeBusy {
		t.Fatalf("after help, hello should start a turn")
	}
	if m.turnID != 2 {
		t.Fatalf("turnID = %d, want 2", m.turnID)
	}
	if len(m.queue) != 0 {
		t.Fatalf("queue should be empty, got %#v", m.queue)
	}
	if !hasLineContaining(m.lines, lineUser, "hello") {
		t.Fatalf("hello user line missing")
	}
	if cmd == nil {
		t.Fatalf("expected turn cmds")
	}
	cancelAndWaitForTurn(t, m)
}

func TestInterruptKeepsQueue(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 5
	canceled := false
	m.turnCancel = func() { canceled = true }
	m.queue = []string{"next"}

	m.interruptTurn("interrupted")
	if !canceled {
		t.Fatalf("interrupt should cancel turn context")
	}
	if len(m.queue) != 1 || m.queue[0] != "next" {
		t.Fatalf("queue must be kept: %#v", m.queue)
	}
	// Simulate Ask returning canceled.
	cmd := m.finishTurn(context.Canceled)
	if !hasLineContaining(m.lines, lineSystem, "interrupted") {
		t.Fatalf("interrupted system line missing")
	}
	if m.mode != modeBusy {
		t.Fatalf("queued next should auto-start")
	}
	if m.turnID != 6 {
		t.Fatalf("turnID = %d, want 6", m.turnID)
	}
	if cmd == nil {
		t.Fatalf("expected drain startTurn cmds")
	}
	cancelAndWaitForTurn(t, m)
}

func TestQueueMaxRejected(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	for i := range maxQueue {
		m.queue = append(m.queue, fmt.Sprintf("item-%d", i))
	}
	m.textarea.SetValue("one-too-many")
	next, _ := m.queueWhileBusy("one-too-many")
	mm := next.(*model)
	if len(mm.queue) != maxQueue {
		t.Fatalf("queue length = %d, want %d", len(mm.queue), maxQueue)
	}
	if !hasLineContaining(mm.lines, lineError, "queue full") {
		t.Fatalf("expected queue full error: %#v", mm.lines)
	}
	if strings.TrimSpace(mm.textarea.Value()) != "one-too-many" {
		t.Fatalf("full queue must keep draft in composer, got %q", mm.textarea.Value())
	}
}

func TestToolEventsTrackCurrentTool(t *testing.T) {
	m := newTestModel(t)
	m.turnID = 1
	m.mode = modeBusy

	next, _ := m.Update(turnToolStartMsg{turnID: 1, tool: "get_current_time", input: "{}"})
	mm := next.(*model)
	if mm.currentTool != "get_current_time" {
		t.Fatalf("currentTool = %q", mm.currentTool)
	}
	next, _ = mm.Update(turnToolEndMsg{turnID: 1, tool: "get_current_time", output: "ok"})
	mm = next.(*model)
	if mm.currentTool != "" {
		t.Fatalf("currentTool should clear after end, got %q", mm.currentTool)
	}
}

func newTestModel(t *testing.T) *model {
	t.Helper()
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: session,
		Status:  StatusInfo{Model: "test-model", Tools: []string{"get_current_time"}, MaxStep: 8},
	})
	m.width = 80
	m.height = 24
	m.layout()
	return m
}

func cancelAndWaitForTurn(t *testing.T, m *model) {
	t.Helper()
	if m.turnCancel != nil {
		m.turnCancel()
	}
	if m.turnDone == nil {
		return
	}
	select {
	case <-m.turnDone:
		// A completed turn is emitted only after its durable lifecycle is done.
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for durable turn to stop")
	}
}

// Ensure staticModel still satisfies chat.Model for package tests.
var _ chat.Model = staticModel{}
