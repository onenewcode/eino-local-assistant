package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/schema"
)

func TestInputHistoryUpDown(t *testing.T) {
	h := newInputHistory()
	h.push("one")
	h.push("two")
	h.push("two") // consecutive dup skipped
	if len(h.entries) != 2 {
		t.Fatalf("entries=%v", h.entries)
	}

	next, ok := h.up("draft")
	if !ok || next != "two" || h.draft != "draft" {
		t.Fatalf("up to newest: next=%q ok=%v draft=%q", next, ok, h.draft)
	}
	next, ok = h.up("ignored")
	if !ok || next != "one" {
		t.Fatalf("up older: next=%q ok=%v", next, ok)
	}
	// Already oldest: stay.
	next, ok = h.up("ignored")
	if ok || next != "one" {
		t.Fatalf("up at oldest should not move: next=%q ok=%v", next, ok)
	}

	next, ok = h.down()
	if !ok || next != "two" {
		t.Fatalf("down: next=%q ok=%v", next, ok)
	}
	next, ok = h.down()
	if !ok || next != "draft" || h.browsing() {
		t.Fatalf("down to draft: next=%q ok=%v browsing=%v", next, ok, h.browsing())
	}
	_, ok = h.down()
	if ok {
		t.Fatal("down on draft should be no-op")
	}
}

func TestInputHistoryMaxAndSeed(t *testing.T) {
	h := newInputHistory()
	for i := range maxInputHistory + 5 {
		h.push(strings.Repeat("x", 1) + string(rune('a'+i%26)) + string(rune('0'+i%10)))
	}
	if len(h.entries) != maxInputHistory {
		t.Fatalf("len=%d want %d", len(h.entries), maxInputHistory)
	}

	h.seedFromMessages([]*schema.Message{
		{Role: schema.System, Content: "sys"},
		{Role: schema.User, Content: "u1"},
		{Role: schema.Assistant, Content: "a1"},
		{Role: schema.User, Content: "u2"},
		{Role: schema.User, Content: "  "},
	})
	if len(h.entries) != 2 || h.entries[0] != "u1" || h.entries[1] != "u2" {
		t.Fatalf("seeded=%v", h.entries)
	}
}

func TestComposerHistoryKeys(t *testing.T) {
	m := newTestModel(t)
	// No history yet: Up is a no-op for history (single-line empty → still Line 0).
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm := next.(*model)
	if mm.inputHist.browsing() {
		t.Fatal("empty history should not enter browse mode")
	}

	mm.inputHist.push("first")
	mm.inputHist.push("second")
	mm.textarea.SetValue("live")
	mm.syncComposerHeight()

	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm = next.(*model)
	if mm.textarea.Value() != "second" {
		t.Fatalf("Up should show newest history, got %q", mm.textarea.Value())
	}
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm = next.(*model)
	if mm.textarea.Value() != "first" {
		t.Fatalf("Up older = %q", mm.textarea.Value())
	}
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm = next.(*model)
	if mm.textarea.Value() != "second" {
		t.Fatalf("Down = %q", mm.textarea.Value())
	}
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm = next.(*model)
	if mm.textarea.Value() != "live" {
		t.Fatalf("Down to draft = %q", mm.textarea.Value())
	}
}

func TestSubmitPushesHistory(t *testing.T) {
	m := newTestModel(t)
	// /help is a local slash; still records what the user typed.
	next, _ := m.submit("/help")
	mm := next.(*model)
	if len(mm.inputHist.entries) != 1 || mm.inputHist.entries[0] != "/help" {
		t.Fatalf("history after /help = %#v", mm.inputHist.entries)
	}
}

func TestQueuePushesHistory(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	next, _ := m.queueWhileBusy("queued-msg")
	mm := next.(*model)
	if len(mm.inputHist.entries) != 1 || mm.inputHist.entries[0] != "queued-msg" {
		t.Fatalf("history after queue = %#v", mm.inputHist.entries)
	}
}

func TestNewClearsHistory(t *testing.T) {
	// Without store, /new fails — exercise clear via helper used by cmdNew.
	m := newTestModel(t)
	m.inputHist.push("keep-me")
	m.inputHist.clear()
	if len(m.inputHist.entries) != 0 {
		t.Fatalf("clear failed: %#v", m.inputHist.entries)
	}
}

func TestMultiLineUpDoesNotSteal(t *testing.T) {
	m := newTestModel(t)
	m.inputHist.push("hist")
	m.textarea.SetValue("line1\nline2")
	m.syncComposerHeight()
	// Move cursor to second line via Line() — SetValue leaves cursor at end (last line).
	if m.textarea.Line() == 0 {
		t.Skip("textarea cursor ended on first line; cannot assert multi-line Up routing")
	}
	before := m.textarea.Value()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm := next.(*model)
	// Should not jump to history while not on first line.
	if mm.textarea.Value() == "hist" && before != "hist" {
		t.Fatalf("Up on non-first line should not browse history")
	}
}
