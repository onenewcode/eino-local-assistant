package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestQueuedMessageRendersAboveComposerWithoutTranscriptEntry(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.textarea.SetValue("follow up after this")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("queue should not start a turn: %#v", cmd)
	}
	if hasLineContaining(mm.lines, lineSystem, "queued (") {
		t.Fatalf("queue admission wrote a transcript entry: %#v", mm.lines)
	}
	view := mm.View()
	queueAt := strings.Index(view, "Queued messages (1)")
	composerAt := strings.Index(view, "Message the assistant…  (/help)")
	if queueAt < 0 || composerAt < 0 || queueAt >= composerAt {
		t.Fatalf("queue must render directly above composer:\n%s", view)
	}
}

func TestQueuePaneFocusNavigationAndCancel(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.queue = []string{"first", "second", "third"}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}, Alt: true})
	mm := next.(*model)
	if !mm.queuePaneFocused || mm.queuePaneSelected != 0 {
		t.Fatalf("alt+q did not focus queue: focused=%v selected=%d", mm.queuePaneFocused, mm.queuePaneSelected)
	}
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm = next.(*model)
	if mm.queuePaneSelected != 1 {
		t.Fatalf("down selection = %d, want 1", mm.queuePaneSelected)
	}
	next, cmd := mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	mm = next.(*model)
	if cmd != nil {
		t.Fatalf("queue cancel returned a command: %#v", cmd)
	}
	if !reflect.DeepEqual(mm.queue, []string{"first", "third"}) {
		t.Fatalf("queue after cancel = %#v", mm.queue)
	}
	if hasLineContaining(mm.lines, lineUser, "second") {
		t.Fatalf("cancelled prompt reached user transcript: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue cancelled (2): second") {
		t.Fatalf("cancel confirmation missing: %#v", mm.lines)
	}
}

func TestQueuePaneSendNowPromotesSelectionAndInterruptsTurn(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 9
	m.queue = []string{"first", "send me", "third"}
	cancelled := false
	m.turnCancel = func() { cancelled = true }

	m.openQueuePane()
	m.queuePaneSelected = 1
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("send now should wait for the cancelled turn to finish: %#v", cmd)
	}
	if !cancelled {
		t.Fatal("send now did not cancel active turn")
	}
	if !reflect.DeepEqual(mm.queue, []string{"send me", "first", "third"}) {
		t.Fatalf("send now queue order = %#v", mm.queue)
	}
	if mm.queuePaneFocused {
		t.Fatal("send now should return focus to composer")
	}
	if hasLineContaining(mm.lines, lineUser, "send me") {
		t.Fatalf("send-now message reached transcript before queue drain: %#v", mm.lines)
	}
}

func TestQueuePaneSendNowDrainsSelectionWhenIdle(t *testing.T) {
	m := newTestModel(t)
	m.queuePaused = true
	m.queue = []string{"first", "send me", "third"}
	m.openQueuePane()
	m.queuePaneSelected = 1

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd == nil || mm.mode != modeBusy {
		t.Fatalf("idle send now should start selected turn: mode=%s cmd=%v", modeName(mm.mode), cmd)
	}
	if !reflect.DeepEqual(mm.queue, []string{"first", "third"}) {
		t.Fatalf("remaining queue = %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineUser, "send me") {
		t.Fatalf("selected prompt was not submitted: %#v", mm.lines)
	}
	cancelAndWaitForTurn(t, mm)
}

func TestQueuePaneBusyEscStillInterruptsTurn(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.queue = []string{"queued"}
	cancelled := false
	m.turnCancel = func() { cancelled = true }
	m.openQueuePane()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := next.(*model)
	if cmd != nil || !cancelled {
		t.Fatalf("busy Esc must interrupt, cmd=%v cancelled=%v", cmd, cancelled)
	}
	if !mm.queuePaneFocused || !reflect.DeepEqual(mm.queue, []string{"queued"}) {
		t.Fatalf("busy Esc changed queue pane state: focused=%v queue=%#v", mm.queuePaneFocused, mm.queue)
	}
}

func TestQueuePaneKeepsSelectedRowVisible(t *testing.T) {
	queue := []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}
	view := renderQueuePane(80, queue, false, true, 7)
	if !strings.Contains(view, "> 8. eight") || !strings.Contains(view, "showing 4-8 of 8") {
		t.Fatalf("selected queue row should remain visible:\n%s", view)
	}
	if strings.Contains(view, "  1. one") {
		t.Fatalf("queue pane should window long queues around selection:\n%s", view)
	}
}
