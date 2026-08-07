package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
)

func TestGoalCommandShowsCompactTaskProjection(t *testing.T) {
	backend := &taskControlModel{status: chat.TaskRunStatus{
		Available:   true,
		State:       "active",
		Goal:        "add a read-only goal command to the terminal UI",
		Tasks:       2,
		DoneTasks:   1,
		ActiveTasks: []string{"step-2"},
		Items: []chat.TaskListItem{
			{ID: "step-1", Goal: "wire the command", State: "done"},
			{ID: "step-2", Goal: "render the checklist", State: "working"},
		},
	}}
	m := newModel(Deps{Ctx: context.Background(), Session: mustSession(t, backend, "system")})

	next, cmd := m.submit("/goal")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("/goal must not start a model turn: %#v", cmd)
	}
	for _, want := range []string{
		"Goal",
		"Goal: add a read-only goal command to the terminal UI",
		"State: active",
		"Progress: done=1/2",
		"Active: step-2",
		"Steps:",
		"[done] wire the command",
		"[working] render the checklist",
	} {
		if !hasLineContaining(mm.lines, lineSystem, want) {
			t.Fatalf("/goal output missing %q: %#v", want, mm.lines)
		}
	}
}

func TestGoalCommandCompactsAndTruncatesValues(t *testing.T) {
	longGoal := strings.Repeat("goal ", goalCommandMaxValueWidth)
	status := chat.TaskRunStatus{
		Available:   true,
		State:       "recovery_error",
		Goal:        "line one\nline two\t" + longGoal,
		ActiveTasks: []string{"active\n task"},
		Items: []chat.TaskListItem{
			{ID: "step-1", Goal: "line one\nline two\t" + longGoal, State: "working"},
		},
	}
	output := renderGoalCommand(status)
	if strings.Contains(output, "line one\nline two") || strings.Contains(output, "active\n task") || strings.Contains(output, "\t") {
		t.Fatalf("field control characters leaked into /goal output: %q", output)
	}
	if !strings.Contains(output, "State: recovery error") {
		t.Fatalf("state wording missing: %q", output)
	}
	if !strings.Contains(output, "...") && !strings.Contains(output, "…") {
		t.Fatalf("long fields should be truncated: %q", output)
	}
}

func TestGoalCommandUnavailableIsExplicitAndReadOnly(t *testing.T) {
	m := newTestModel(t)

	next, cmd := m.submit("/goal")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("unavailable /goal must not start a model turn: %#v", cmd)
	}
	if !hasLineContaining(mm.lines, lineSystem, "unavailable: no autonomous task runtime") {
		t.Fatalf("missing explicit unavailable report: %#v", mm.lines)
	}
	if hasLineContaining(mm.lines, lineError, "goal") {
		t.Fatalf("unavailable /goal must not be an error: %#v", mm.lines)
	}
}

func TestGoalCommandRejectsArgumentsWithStableUsage(t *testing.T) {
	m := newTestModel(t)

	next, cmd := m.submit("/goal extra")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("invalid /goal must not start a model turn: %#v", cmd)
	}
	if !hasLineContaining(mm.lines, lineError, goalCommandUsage) {
		t.Fatalf("missing stable usage error: %#v", mm.lines)
	}
	if hasLineContaining(mm.lines, lineSystem, "unavailable") {
		t.Fatalf("invalid /goal should stop at usage: %#v", mm.lines)
	}
}

func TestGoalCommandRunsImmediatelyWhileBusy(t *testing.T) {
	backend := &taskControlModel{status: chat.TaskRunStatus{Available: true, State: "active", Tasks: 1}}
	m := newModel(Deps{Ctx: context.Background(), Session: mustSession(t, backend, "system")})
	m.mode = modeBusy
	m.turnID = 9
	cancelled := false
	m.turnCancel = func() { cancelled = true }

	next, cmd := m.queueWhileBusy("/goal")
	mm := next.(*model)
	if cmd != nil || mm.mode != modeBusy || mm.turnID != 9 {
		t.Fatalf("busy /goal changed active turn: mode=%s turn=%d cmd=%v", modeName(mm.mode), mm.turnID, cmd)
	}
	if cancelled {
		t.Fatal("busy /goal must not interrupt the active turn")
	}
	if len(mm.queue) != 0 {
		t.Fatalf("busy /goal must not enter the FIFO: %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "Goal") {
		t.Fatalf("busy /goal output missing: %#v", mm.lines)
	}
}
