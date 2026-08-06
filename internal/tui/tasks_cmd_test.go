package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/tools"
)

func TestTasksCommandReportsBoundedIdleSummaryWithoutSessionOrQueueMutation(t *testing.T) {
	backend := &taskControlModel{status: chat.TaskRunStatus{
		Available: true,
		State:     "active",
	}}
	m := newModel(Deps{Ctx: context.Background(), Session: mustSession(t, backend, "system")})
	m.queue = []string{"first follow-up", "second follow-up"}
	m.turnID = 11
	beforeTranscript := len(m.activeSession().Transcript())
	beforeQueue := append([]string(nil), m.queue...)

	next, cmd := m.submit("/tasks")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("/tasks must not start a model turn: %#v", cmd)
	}
	if mm.mode != modeIdle || mm.turnID != 11 {
		t.Fatalf("/tasks changed foreground turn: mode=%s turn=%d", modeName(mm.mode), mm.turnID)
	}
	if !reflect.DeepEqual(mm.queue, beforeQueue) {
		t.Fatalf("/tasks changed queue: got %#v want %#v", mm.queue, beforeQueue)
	}
	if got := len(mm.activeSession().Transcript()); got != beforeTranscript {
		t.Fatalf("/tasks wrote the session transcript: before=%d after=%d", beforeTranscript, got)
	}
	for _, want := range []string{
		"Foreground turn: idle",
		"Current tool: none",
		"Queued follow-ups: 2",
		"Goal/checklist: available via /goal and Ctrl+T",
		"Background resources: unavailable (no background shell/subagent runtime)",
	} {
		if !hasLineContaining(mm.lines, lineSystem, want) {
			t.Fatalf("/tasks output missing %q: %#v", want, mm.lines)
		}
	}
}

func TestTasksCommandReportsForegroundStatesImmediately(t *testing.T) {
	cases := []struct {
		name     string
		mode     mode
		stopping bool
		approval bool
		want     string
	}{
		{name: "idle", mode: modeIdle, want: "idle"},
		{name: "working", mode: modeBusy, want: "working"},
		{name: "stopping", mode: modeBusy, stopping: true, want: "stopping"},
		{name: "compacting", mode: modeCompacting, want: "compacting"},
		{name: "awaiting approval", mode: modeBusy, approval: true, want: "awaiting approval"},
		{name: "approval while idle", mode: modeIdle, approval: true, want: "awaiting approval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.mode = tc.mode
			m.interruptFeedbackShown = tc.stopping
			if tc.approval {
				m.pendingApproval = &approvalRequestMsg{Request: tools.ApprovalRequest{Tool: "run_command"}}
			}
			if got := tasksForegroundState(m); got != tc.want {
				t.Fatalf("tasksForegroundState = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTasksCommandSanitizesAndTruncatesCurrentTool(t *testing.T) {
	m := newTestModel(t)
	longTool := "shell\x1b[31m\n\t" + strings.Repeat("very-long-tool-name ", 12)
	m.currentTool = longTool

	output := renderTasksCommand(m)
	if strings.ContainsAny(output, "\x00\x1b\t") {
		t.Fatalf("control characters leaked into /tasks output: %q", output)
	}
	if strings.Contains(output, longTool) {
		t.Fatal("/tasks must truncate the current tool instead of exposing the full value")
	}
	if !strings.Contains(output, "…") {
		t.Fatalf("/tasks current tool should be visibly truncated: %q", output)
	}
}

func TestTasksCommandUnavailableBackgroundRuntimeIsExplicit(t *testing.T) {
	m := newTestModel(t)
	output := renderTasksCommand(m)
	if !strings.Contains(output, "Goal/checklist: unavailable via /goal") {
		t.Fatalf("missing unavailable goal/checklist projection: %q", output)
	}
	if !strings.Contains(output, "Background resources: unavailable (no background shell/subagent runtime)") {
		t.Fatalf("missing explicit background runtime boundary: %q", output)
	}
	if strings.Contains(output, "stop") || strings.Contains(output, "resume") {
		t.Fatalf("unavailable background resources must not imply stop/resume controls: %q", output)
	}
}

func TestTasksCommandRejectsArgumentsWithStableUsage(t *testing.T) {
	m := newTestModel(t)

	next, cmd := m.submit("/tasks extra")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("invalid /tasks must not start a model turn: %#v", cmd)
	}
	if !hasLineContaining(mm.lines, lineError, tasksCommandUsage) {
		t.Fatalf("missing stable usage error: %#v", mm.lines)
	}
	if hasLineContaining(mm.lines, lineSystem, "Foreground turn:") {
		t.Fatalf("invalid /tasks should stop at usage: %#v", mm.lines)
	}
}

func TestTasksCommandRunsImmediatelyWithoutChangingBusyOperation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode mode
	}{
		{name: "busy", mode: modeBusy},
		{name: "compacting", mode: modeCompacting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.mode = tc.mode
			m.turnID = 7
			m.queue = []string{"retained"}
			cancelled := false
			m.turnCancel = func() { cancelled = true }
			beforeQueue := append([]string(nil), m.queue...)

			next, cmd := m.queueWhileBusy("/tasks")
			mm := next.(*model)
			if cmd != nil || mm.mode != tc.mode || mm.turnID != 7 {
				t.Fatalf("/tasks changed active operation: mode=%s turn=%d cmd=%v", modeName(mm.mode), mm.turnID, cmd)
			}
			if cancelled {
				t.Fatal("/tasks must not cancel the active operation")
			}
			if !reflect.DeepEqual(mm.queue, beforeQueue) {
				t.Fatalf("/tasks changed queue: got %#v want %#v", mm.queue, beforeQueue)
			}
			if !hasLineContaining(mm.lines, lineSystem, "Foreground turn:") {
				t.Fatalf("/tasks output missing: %#v", mm.lines)
			}
		})
	}
}

func TestTasksCommandRunsImmediatelyWhileApprovalIsPending(t *testing.T) {
	m := newTestModel(t)
	m.pendingApproval = &approvalRequestMsg{Request: tools.ApprovalRequest{Tool: "run_command"}}
	m.queue = []string{"retained"}

	next, cmd := m.queueWhileBusy("/tasks")
	mm := next.(*model)
	if cmd != nil || mm.pendingApproval == nil {
		t.Fatalf("pending approval was changed by /tasks: cmd=%v approval=%v", cmd, mm.pendingApproval)
	}
	if !hasLineContaining(mm.lines, lineSystem, "Foreground turn: awaiting approval") {
		t.Fatalf("pending approval state missing: %#v", mm.lines)
	}
	if !reflect.DeepEqual(mm.queue, []string{"retained"}) {
		t.Fatalf("pending approval /tasks changed queue: %#v", mm.queue)
	}
}
