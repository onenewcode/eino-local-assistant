package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCtrlTTogglesTaskProgressPane(t *testing.T) {
	backend := &taskControlModel{status: chat.TaskRunStatus{
		Available:   true,
		State:       "active",
		Goal:        "add task progress to the terminal UI",
		Tasks:       3,
		DoneTasks:   1,
		ActiveTasks: []string{"step-2"},
		Items: []chat.TaskListItem{
			{ID: "step-1", Goal: "bind the task state to the UI", State: "done"},
			{ID: "step-2", Goal: "render the task list", State: "working"},
			{ID: "step-3", Goal: "verify the TUI layout", State: "pending"},
			{ID: "step-4", Goal: "test the empty state", State: "pending"},
			{ID: "step-5", Goal: "document the result", State: "pending"},
			{ID: "step-6", Goal: "this item stays in /goal", State: "pending"},
		},
	}}
	session := mustSession(t, backend, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	m.width = 80
	m.height = 24
	m.layout()
	closedHeight := m.viewport.Height

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	opened := next.(*model)
	if !opened.taskPaneOpen {
		t.Fatal("ctrl+t should open task progress")
	}
	if opened.viewport.Height >= closedHeight {
		t.Fatalf("task pane should reserve viewport rows: closed=%d open=%d", closedHeight, opened.viewport.Height)
	}
	view := opened.View()
	for _, want := range []string{
		"Tasks · active · 1/3",
		"bind the task state to the UI",
		"render the task list",
		"verify the TUI layout",
		"+1 more via /goal",
		"ctrl+t hide task progress",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("task pane missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "this item stays in /goal") {
		t.Fatalf("task pane must keep overflow in /goal:\n%s", view)
	}

	next, _ = opened.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	closed := next.(*model)
	if closed.taskPaneOpen {
		t.Fatal("ctrl+t should close task progress")
	}
	if closed.viewport.Height != closedHeight {
		t.Fatalf("viewport should restore: got=%d want=%d", closed.viewport.Height, closedHeight)
	}
}

func TestCtrlTClosesOpenTaskPaneWhenTaskStatusBecomesUnavailable(t *testing.T) {
	backend := &taskControlModel{status: chat.TaskRunStatus{Available: true, State: "active"}}
	session := mustSession(t, backend, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	m.width = 80
	m.height = 24
	m.layout()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	opened := next.(*model)
	if !opened.taskPaneOpen {
		t.Fatal("ctrl+t should open the task pane while task status is available")
	}
	backend.status = chat.TaskRunStatus{}

	next, _ = opened.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	closed := next.(*model)
	if closed.taskPaneOpen {
		t.Fatal("ctrl+t must close an already-open task pane even when status is unavailable")
	}
}
