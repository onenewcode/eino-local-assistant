package tui

import (
	"context"
	"testing"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

type taskControlModel struct {
	staticModel
	status                    chat.TaskRunStatus
	interrupts                []string
	turnCancelled             *bool
	interruptSawTurnCancelled bool
}

func (m *taskControlModel) TaskExecutionStatus(context.Context) chat.TaskRunStatus {
	return m.status
}

func (m *taskControlModel) TaskCompletionGate(context.Context) chat.TaskCompletionGate {
	return chat.TaskCompletionGate{}
}

func (m *taskControlModel) InterruptTask(_ context.Context, reason string) chat.TaskInterruptReceipt {
	m.interrupts = append(m.interrupts, reason)
	if m.turnCancelled != nil {
		m.interruptSawTurnCancelled = *m.turnCancelled
	}
	if m.status.State == "" {
		return chat.TaskInterruptReceipt{Summary: "no active autonomous task"}
	}
	return chat.TaskInterruptReceipt{Applied: true, Summary: "task run interrupted; completed evidence is retained"}
}

func TestEscInterruptsActiveAutonomousTask(t *testing.T) {
	backend := &taskControlModel{status: chat.TaskRunStatus{Available: true, State: "active"}}
	session := mustSession(t, backend, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	m.mode = modeBusy
	cancelled := false
	backend.turnCancelled = &cancelled
	m.turnCancel = func() { cancelled = true }

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	interrupted := next.(*model)
	if !cancelled {
		t.Fatal("Esc must cancel the active turn")
	}
	if len(backend.interrupts) != 1 || backend.interrupts[0] != "user interrupted the active turn" {
		t.Fatalf("task interrupts = %#v", backend.interrupts)
	}
	if !backend.interruptSawTurnCancelled {
		t.Fatal("Esc must cancel the active turn before recording task interruption")
	}
	if !hasLineContaining(interrupted.lines, lineSystem, "task run interrupted") {
		t.Fatalf("interrupt receipt missing from transcript: %#v", interrupted.lines)
	}
}
