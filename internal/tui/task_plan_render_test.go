package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestTaskStatusEventRendersDeduplicatedUpdatedPlan(t *testing.T) {
	m := newTestModel(t)
	m.width = 36
	m.height = 20
	m.mode = modeBusy
	m.turnID = 7
	m.layout()
	status := chat.TaskRunStatus{
		Available: true, State: "active", Goal: "render a useful task checklist", Tasks: 2, ActiveTasks: []string{"step-1"},
		Items: []chat.TaskListItem{
			{ID: "step-1", Goal: "inspect the current terminal rendering at narrow widths", State: "working"},
			{ID: "step-2", Goal: "implement the complete transcript plan card", State: "pending"},
		},
	}
	next, _ := m.Update(turnTaskStatusMsg{turnID: 7, status: status})
	mm := next.(*model)
	view := mm.View()
	for _, want := range []string{"Updated Plan", "inspect the current", "implement the complete"} {
		if !strings.Contains(view, want) {
			t.Fatalf("updated plan missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "proofs:") || strings.Contains(view, "depends on:") {
		t.Fatalf("plan must not show proofs or depends_on:\n%s", view)
	}
	countPlans := func() int {
		n := 0
		for _, line := range mm.lines {
			if line.kind == lineTaskPlan {
				n++
			}
		}
		return n
	}
	if countPlans() != 1 {
		t.Fatalf("plan cards = %d, want 1", countPlans())
	}
	next, _ = mm.Update(turnTaskStatusMsg{turnID: 7, status: status})
	mm = next.(*model)
	if countPlans() != 1 {
		t.Fatalf("identical task status duplicated plan card: %#v", mm.lines)
	}
	status.Items[0].State = "done"
	status.DoneTasks = 1
	status.ActiveTasks = nil
	status.Items[1].State = "working"
	status.ActiveTasks = []string{"step-2"}
	next, _ = mm.Update(turnTaskStatusMsg{turnID: 7, status: status})
	mm = next.(*model)
	if countPlans() != 2 {
		t.Fatalf("changed task status did not add snapshot: %#v", mm.lines)
	}
}

func TestGoalCommandListsChecklistWithoutProofs(t *testing.T) {
	backend := &taskControlModel{status: chat.TaskRunStatus{
		Available: true, State: "active", Goal: "show checklist", Tasks: 2, DoneTasks: 1,
		Items: []chat.TaskListItem{
			{ID: "step-1", Goal: "inspect files", State: "done"},
			{ID: "step-2", Goal: "change UI", State: "working"},
		},
	}}
	m := newModel(Deps{Ctx: context.Background(), Session: mustSession(t, backend, "system")})
	output := renderGoalCommand(sessionTaskStatus(m.activeSession()))
	for _, want := range []string{"Steps:", "[done] inspect files", "[working] change UI", "Progress: done=1/2"} {
		if !strings.Contains(output, want) {
			t.Fatalf("/goal checklist missing %q:\n%s", want, output)
		}
	}
	for _, banned := range []string{"proofs:", "depends_on:", "PlanRequired", "requirements="} {
		if strings.Contains(output, banned) {
			t.Fatalf("/goal leaked retired plan fields %q:\n%s", banned, output)
		}
	}
}

func TestUpdatedPlanWrapKeepsContinuationWithinNarrowTerminal(t *testing.T) {
	const width = 24
	rendered := renderUpdatedPlan(width, chat.TaskRunStatus{
		Available: true,
		Items: []chat.TaskListItem{{
			ID:    "step-1",
			Goal:  "render a long task description without overflowing the terminal",
			State: "working",
		}},
	})
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("plan line width = %d, want <= %d: %q", got, width, line)
		}
	}
}

func TestUpdatedPlanShowsStateBeforeChecklistExists(t *testing.T) {
	status := chat.TaskRunStatus{
		Available: true,
		State:     "active",
		Goal:      "implement a bounded task runtime",
	}
	rendered := renderUpdatedPlan(48, status)
	for _, want := range []string{
		"Updated Plan",
		"State: active",
		"no task nodes are available yet",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("updated plan missing %q:\n%s", want, rendered)
		}
	}
	for _, line := range strings.Split(rendered, "\n") {
		if got := lipgloss.Width(line); got > 48 {
			t.Fatalf("summary line width = %d, want <= 48: %q", got, line)
		}
	}
}

func TestTaskStatusFingerprintIncludesProgress(t *testing.T) {
	base := chat.TaskRunStatus{Available: true, State: "active"}
	changed := base
	changed.DoneTasks = 1
	changed.Tasks = 2
	if taskStatusFingerprint(base) == taskStatusFingerprint(changed) {
		t.Fatal("changed progress must produce a new plan snapshot")
	}
}

func TestTaskStatusEventBridgeCarriesSnapshot(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	emit := emitFromTurnEvent(context.Background(), 3, 4, ch)
	status := chat.TaskRunStatus{Available: true, State: "active", Items: []chat.TaskListItem{{ID: "step-1", Goal: "read", State: "working"}}}
	emit(chat.TurnEvent{Kind: chat.TurnEventTaskStatus, TaskStatus: &status})
	msg, ok := (<-ch).(turnTaskStatusMsg)
	if !ok || msg.turnID != 3 || msg.sessionGeneration != 4 || len(msg.status.Items) != 1 || msg.status.Items[0].ID != "step-1" {
		t.Fatalf("task status bridge = %#v", msg)
	}
}
