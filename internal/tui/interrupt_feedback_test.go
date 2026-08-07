package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBusyInterruptShowsCleanupFeedbackOnce(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyType
	}{
		{name: "escape", key: tea.KeyEsc},
		{name: "ctrl-c", key: tea.KeyCtrlC},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.mode = modeBusy
			m.turnID = 1
			cancelCalls := 0
			m.turnCancel = func() { cancelCalls++ }

			next, _ := m.Update(tea.KeyMsg{Type: tc.key})
			m = next.(*model)
			if got := countLineContaining(m.lines, lineSystem, interruptRequestedMessage); got != 1 {
				t.Fatalf("cleanup feedback count after first interrupt = %d, want 1: %#v", got, m.lines)
			}

			next, _ = m.Update(tea.KeyMsg{Type: tc.key})
			m = next.(*model)
			if got := countLineContaining(m.lines, lineSystem, interruptRequestedMessage); got != 1 {
				t.Fatalf("cleanup feedback count after repeated interrupt = %d, want 1: %#v", got, m.lines)
			}
			if cancelCalls != 2 {
				t.Fatalf("repeated interrupt changed cancel propagation count = %d, want 2", cancelCalls)
			}

			requested := lineIndexContaining(m.lines, lineSystem, interruptRequestedMessage)
			m.finishTurn(context.Canceled)
			interrupted := lineIndexContaining(m.lines, lineSystem, "interrupted")
			if requested < 0 || interrupted < 0 || requested >= interrupted {
				t.Fatalf("cleanup feedback must precede final interruption settlement: %#v", m.lines)
			}
			if m.interruptFeedbackShown {
				t.Fatal("finishTurn must clear transient interrupt feedback state")
			}
		})
	}
}

func TestStatusLabelShowsStoppingUntilTurnCleanup(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLine.Fields = []string{statusFieldModelWithReasoning, statusFieldTaskProgress}
	m.mode = modeBusy
	m.interruptFeedbackShown = true

	line := m.statusLabel()
	if !strings.Contains(line, "stopping") {
		t.Fatalf("stopping status missing cleanup state: %q", line)
	}
	if strings.Contains(line, "thinking") {
		t.Fatalf("stopping status must not report normal activity: %q", line)
	}
	m.finishTurn(context.Canceled)
	if got := m.statusLabel(); strings.Contains(got, "stopping") {
		t.Fatalf("finishTurn should restore idle status, got %q", got)
	}

	m.interruptFeedbackShown = true
	_, _ = m.startTurn("new turn")
	if got := m.statusLabel(); !strings.Contains(got, "thinking") || strings.Contains(got, "stopping") {
		t.Fatalf("new turn should restore working status, got %q", got)
	}
	cancelAndWaitForTurn(t, m)
}

func TestInterruptFeedbackResetsForNewTurnAndSession(t *testing.T) {
	t.Run("new turn", func(t *testing.T) {
		m := newTestModel(t)
		m.interruptFeedbackShown = true
		_, _ = m.startTurn("new turn")
		if m.interruptFeedbackShown {
			t.Fatal("startTurn must clear transient interrupt feedback state")
		}
		cancelAndWaitForTurn(t, m)
	})

	t.Run("session switch", func(t *testing.T) {
		m := newTestModel(t)
		m.interruptFeedbackShown = true
		m.replaceSession(mustSession(t, &staticModel{}, "new session"))
		if m.interruptFeedbackShown {
			t.Fatal("replaceSession must clear transient interrupt feedback state")
		}
	})
}

func countLineContaining(lines []transcriptLine, kind lineKind, text string) int {
	count := 0
	for _, line := range lines {
		if line.kind == kind && strings.Contains(line.text, text) {
			count++
		}
	}
	return count
}

func lineIndexContaining(lines []transcriptLine, kind lineKind, text string) int {
	for i, line := range lines {
		if line.kind == kind && strings.Contains(line.text, text) {
			return i
		}
	}
	return -1
}
