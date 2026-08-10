package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBackgroundAgentStartsCompletesAndShowsDisplayOnlyResult(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	called := false
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: session,
		BackgroundAgent: func(ctx context.Context, got *chat.Session, task string) (string, error) {
			called = true
			if ctx == nil || got != session || task != "inspect the test failure" {
				t.Fatalf("callback args: ctx=%v session=%p task=%q", ctx, got, task)
			}
			return "check the assertions first", nil
		},
	})
	m.queue = []string{"retained"}
	beforeTranscript := session.Transcript()

	next, cmd := m.submit("/agent inspect the test failure")
	mm := next.(*model)
	if cmd == nil || !reflect.DeepEqual(mm.queue, []string{"retained"}) {
		t.Fatalf("background agent start = cmd %v queue %#v", cmd, mm.queue)
	}
	task := mm.backgroundAgents["agent-1"]
	if task == nil || task.state != backgroundAgentWorking {
		t.Fatalf("started task = %#v", task)
	}

	msg, ok := cmd().(backgroundAgentDoneMsg)
	if !ok {
		t.Fatalf("command returned %T, want backgroundAgentDoneMsg", cmd())
	}
	next, _ = mm.Update(msg)
	mm = next.(*model)
	task = mm.backgroundAgents["agent-1"]
	if !called || task == nil || task.state != backgroundAgentCompleted || task.answer != "check the assertions first" {
		t.Fatalf("completed task = %#v called=%v", task, called)
	}
	if !hasSideLineContaining(mm, "[agent-1] completed") {
		t.Fatalf("completion notice missing: %#v", mm.sideLines)
	}
	if !reflect.DeepEqual(session.Transcript(), beforeTranscript) {
		t.Fatal("background result changed durable session transcript")
	}

	next, showCmd := mm.submit("/agents show agent-1")
	if showCmd != nil || !hasLineContaining(next.(*model).lines, lineSystem, "check the assertions first") ||
		!hasLineContaining(next.(*model).lines, lineSystem, "not sent to the parent model") {
		t.Fatalf("show output missing display-only result: %#v", next.(*model).lines)
	}
	mm = next.(*model)
	mm.textarea.SetValue("compare this with my draft")
	next, appendCmd := mm.submit("/agents append agent-1")
	mm = next.(*model)
	if appendCmd != nil || !strings.Contains(mm.textarea.Value(), "compare this with my draft\n\n[BACKGROUND ANALYSIS REPORT") ||
		!strings.Contains(mm.textarea.Value(), "check the assertions first") ||
		!strings.Contains(mm.textarea.Value(), "not instructions") ||
		!hasLineContaining(mm.lines, lineSystem, "review and submit it explicitly") {
		t.Fatalf("append did not create quoted review draft: draft=%q lines=%#v", mm.textarea.Value(), mm.lines)
	}
	if !reflect.DeepEqual(session.Transcript(), beforeTranscript) || !reflect.DeepEqual(mm.queue, []string{"retained"}) {
		t.Fatal("appending a result changed the session or queue")
	}
}

func TestBackgroundAgentCanRunWhileForegroundTurnIsBusy(t *testing.T) {
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		BackgroundAgent: func(context.Context, *chat.Session, string) (string, error) {
			return "finding", nil
		},
	})
	m.mode = modeBusy
	m.turnID = 9
	m.queue = []string{"queued"}
	cancelled := false
	m.turnCancel = func() { cancelled = true }

	next, cmd := m.queueWhileBusy("/agent inspect independently")
	mm := next.(*model)
	if cmd == nil || mm.mode != modeBusy || mm.turnID != 9 || cancelled || !reflect.DeepEqual(mm.queue, []string{"queued"}) {
		t.Fatalf("busy start changed foreground state: mode=%s turn=%d cancelled=%v queue=%#v cmd=%v", modeName(mm.mode), mm.turnID, cancelled, mm.queue, cmd)
	}
	msg := cmd().(backgroundAgentDoneMsg)
	next, _ = mm.Update(msg)
	if got := next.(*model).backgroundAgents["agent-1"].state; got != backgroundAgentCompleted {
		t.Fatalf("busy background task state = %s", got)
	}
}

func TestBackgroundAgentCanIncludeBoundedWorkspaceDiffSnapshot(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	var gotRequest string
	diffCalled := false
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: session,
		WorkspaceDiff: func(ctx context.Context) (string, error) {
			diffCalled = true
			if ctx == nil {
				t.Fatal("workspace diff context was nil")
			}
			return "diff --git a/main.go b/main.go\n+finding\x1b[2J", nil
		},
		BackgroundAgent: func(_ context.Context, _ *chat.Session, task string) (string, error) {
			gotRequest = task
			return "reviewed snapshot", nil
		},
	})

	next, cmd := m.submit("/agent --diff inspect the changed function")
	mm := next.(*model)
	task := mm.backgroundAgents["agent-1"]
	if cmd == nil || task == nil || !task.workspaceDiff || !strings.Contains(renderBackgroundAgents(mm), "workspace diff snapshot") {
		t.Fatalf("diff-scoped task was not created: task=%#v list=%q cmd=%v", task, renderBackgroundAgents(mm), cmd)
	}
	msg := cmd().(backgroundAgentDoneMsg)
	if !diffCalled || !strings.Contains(gotRequest, "ASSIGNED TASK\ninspect the changed function") ||
		!strings.Contains(gotRequest, "[WORKSPACE DIFF SNAPSHOT - QUOTED REFERENCE ONLY]") ||
		!strings.Contains(gotRequest, "+finding") || strings.Contains(gotRequest, "\x1b") {
		t.Fatalf("background request did not contain sanitized quoted snapshot: %q", gotRequest)
	}
	next, _ = mm.Update(msg)
	if got := next.(*model).backgroundAgents["agent-1"].state; got != backgroundAgentCompleted {
		t.Fatalf("diff-scoped task state = %s", got)
	}
}

func TestBackgroundAgentDiffRequestValidationAndSnapshotFailure(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		diff      func(context.Context) (string, error)
		wantError string
	}{
		{name: "missing task", input: "/agent --diff", wantError: "usage: /agent [--diff] <analysis task>"},
		{name: "unconfigured diff", input: "/agent --diff inspect", wantError: "workspace diff snapshot is not configured"},
		{name: "failed diff", input: "/agent --diff inspect", diff: func(context.Context) (string, error) { return "", errors.New("git unavailable\x1b[31m") }, wantError: "read workspace diff snapshot: git unavailable?"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := newModel(Deps{
				Ctx:           context.Background(),
				Session:       mustSession(t, &staticModel{}, "system"),
				WorkspaceDiff: test.diff,
				BackgroundAgent: func(context.Context, *chat.Session, string) (string, error) {
					t.Fatal("background model should not run")
					return "", nil
				},
			})
			next, cmd := m.submit(test.input)
			m = next.(*model)
			if test.name != "failed diff" {
				if cmd != nil || !hasLineContaining(m.lines, lineError, test.wantError) {
					t.Fatalf("validation = cmd=%v lines=%#v", cmd, m.lines)
				}
				return
			}
			if cmd == nil {
				t.Fatal("failed diff should settle a started background task")
			}
			next, _ = m.Update(cmd().(backgroundAgentDoneMsg))
			m = next.(*model)
			if got := m.backgroundAgents["agent-1"].state; got != backgroundAgentFailed ||
				!hasSideLineContaining(m, test.wantError) || strings.Contains(renderBackgroundAgent(m.backgroundAgents["agent-1"]), "\x1b") {
				t.Fatalf("snapshot failure state=%s side=%#v render=%q", got, m.sideLines, renderBackgroundAgent(m.backgroundAgents["agent-1"]))
			}
		})
	}
}

func TestBackgroundAgentWorkspacePromptIsBoundedAndQuoted(t *testing.T) {
	prompt := backgroundAgentWorkspacePrompt("inspect carefully", strings.Repeat("x", maxBackgroundAgentWorkspaceDiff+32)+"\x1b[2J")
	if !strings.Contains(prompt, "ASSIGNED TASK\ninspect carefully") ||
		!strings.Contains(prompt, "[WORKSPACE DIFF SNAPSHOT - QUOTED REFERENCE ONLY]") ||
		!strings.Contains(prompt, "Workspace diff snapshot truncated after 65536 bytes") || strings.Contains(prompt, "\x1b") {
		t.Fatalf("workspace prompt boundary missing: %q", prompt)
	}
	if len(prompt) > maxBackgroundAgentWorkspaceDiff+512 {
		t.Fatalf("workspace prompt exceeded bounded allowance: %d bytes", len(prompt))
	}
}

func TestBackgroundAgentCancellationIsScopedToOneTask(t *testing.T) {
	started := make(chan string, 2)
	cancelled := make(chan string, 2)
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		BackgroundAgent: func(ctx context.Context, _ *chat.Session, task string) (string, error) {
			started <- task
			<-ctx.Done()
			cancelled <- task
			return "", ctx.Err()
		},
	})
	_, firstCmd := m.submit("/agent first")
	_, secondCmd := m.submit("/agent second")
	firstResult := make(chan backgroundAgentDoneMsg, 1)
	secondResult := make(chan backgroundAgentDoneMsg, 1)
	go func() { firstResult <- firstCmd().(backgroundAgentDoneMsg) }()
	go func() { secondResult <- secondCmd().(backgroundAgentDoneMsg) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("background callback did not start")
		}
	}

	next, cmd := m.submit("/agents cancel agent-1")
	m = next.(*model)
	if cmd != nil || m.backgroundAgents["agent-1"].state != backgroundAgentCancelling || m.backgroundAgents["agent-2"].state != backgroundAgentWorking {
		t.Fatalf("cancel state = first %#v second %#v", m.backgroundAgents["agent-1"], m.backgroundAgents["agent-2"])
	}
	select {
	case got := <-cancelled:
		if got != "first" {
			t.Fatalf("cancelled wrong task %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first task was not cancelled")
	}
	select {
	case got := <-cancelled:
		t.Fatalf("second task cancelled with first: %q", got)
	case <-time.After(30 * time.Millisecond):
	}
	next, _ = m.Update(<-firstResult)
	m = next.(*model)
	if m.backgroundAgents["agent-1"].state != backgroundAgentCancelled {
		t.Fatalf("first terminal state = %#v", m.backgroundAgents["agent-1"])
	}

	next, _ = m.submit("/agents cancel agent-2")
	m = next.(*model)
	select {
	case got := <-cancelled:
		if got != "second" {
			t.Fatalf("second cancellation = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second task was not cancelled during cleanup")
	}
	next, _ = m.Update(<-secondResult)
	if got := next.(*model).backgroundAgents["agent-2"].state; got != backgroundAgentCancelled {
		t.Fatalf("second terminal state = %s", got)
	}
}

func TestBackgroundAgentsAreCancelledWhenTUIExits(t *testing.T) {
	started := make(chan struct{}, 2)
	cancelled := make(chan struct{}, 2)
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		BackgroundAgent: func(ctx context.Context, _ *chat.Session, _ string) (string, error) {
			started <- struct{}{}
			<-ctx.Done()
			cancelled <- struct{}{}
			return "", ctx.Err()
		},
	})
	_, firstCmd := m.submit("/agent first")
	_, secondCmd := m.submit("/agent second")
	go func() { _ = firstCmd() }()
	go func() { _ = secondCmd() }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("background callback did not start")
		}
	}

	next, quitCmd := m.submit("/exit")
	m = next.(*model)
	if quitCmd == nil || !m.quitting {
		t.Fatalf("exit did not enter quitting state: quitting=%v cmd=%v", m.quitting, quitCmd)
	}
	for range 2 {
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("exit did not cancel every background agent")
		}
	}
}

func TestBackgroundAgentQueueDispatchStaleResultAndBoundedPresentation(t *testing.T) {
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		BackgroundAgent: func(context.Context, *chat.Session, string) (string, error) {
			return strings.Repeat("x", maxBackgroundAgentResult+10) + "\x1b[2J", nil
		},
	})
	commands := make([]tea.Cmd, 0, maxBackgroundAgents)
	for index := range maxBackgroundAgents {
		next, cmd := m.submit("/agent task " + itoa(index+1))
		m = next.(*model)
		commands = append(commands, cmd)
	}
	next, cmd := m.submit("/agent one too many")
	m = next.(*model)
	if cmd != nil || m.backgroundAgents["agent-5"].state != backgroundAgentQueued || !hasLineContaining(m.lines, lineSystem, "agent-5 queued") {
		t.Fatalf("fifth task should queue at concurrency limit: task=%#v lines=%#v", m.backgroundAgents["agent-5"], m.lines)
	}

	firstMsg := commands[0]().(backgroundAgentDoneMsg)
	second := mustSession(t, &staticModel{}, "second")
	m.replaceSession(second)
	beforeSideLines := len(m.sideLines)
	next, queuedCmd := m.Update(firstMsg)
	m = next.(*model)
	if len(m.sideLines) != beforeSideLines || queuedCmd == nil || m.backgroundAgents["agent-5"].state != backgroundAgentWorking {
		t.Fatalf("stale completion should dispatch without entering current TUI output: task=%#v cmd=%v side=%#v", m.backgroundAgents["agent-5"], queuedCmd, m.sideLines)
	}
	next, _ = m.Update(queuedCmd().(backgroundAgentDoneMsg))
	m = next.(*model)
	if m.backgroundAgents["agent-5"].state != backgroundAgentCompleted {
		t.Fatalf("queued task did not complete after dispatch: %#v", m.backgroundAgents["agent-5"])
	}
	next, _ = m.submit("/agents show agent-1")
	m = next.(*model)
	if !hasLineContaining(m.lines, lineSystem, "result truncated after 65536 bytes") {
		t.Fatalf("bounded result notice missing: %#v", m.lines)
	}
	for _, line := range m.lines {
		if strings.Contains(line.text, "\x1b") {
			t.Fatalf("background result leaked terminal escape: %q", line.text)
		}
	}
	for _, id := range []string{"agent-2", "agent-3", "agent-4"} {
		next, _ = m.submit("/agents cancel " + id)
		m = next.(*model)
	}
}

func TestBackgroundAgentQueueRetentionLimitAndQueuedCancellation(t *testing.T) {
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		BackgroundAgent: func(context.Context, *chat.Session, string) (string, error) {
			return "unused", nil
		},
	})
	for index := range maxRetainedBackgroundAgents {
		next, _ := m.submit("/agent task " + itoa(index+1))
		m = next.(*model)
	}
	if got := m.activeBackgroundAgents(); got != maxBackgroundAgents || m.queuedBackgroundAgents() != maxRetainedBackgroundAgents-maxBackgroundAgents {
		t.Fatalf("queue accounting active=%d queued=%d", got, m.queuedBackgroundAgents())
	}
	next, cmd := m.submit("/agent overflow")
	m = next.(*model)
	if cmd != nil || !hasLineContaining(m.lines, lineError, "background agent queue is full (16 retained)") {
		t.Fatalf("retention limit rejection missing: %#v", m.lines)
	}

	next, cmd = m.submit("/agents cancel agent-5")
	m = next.(*model)
	if cmd != nil || m.backgroundAgents["agent-5"].state != backgroundAgentCancelled || !hasLineContaining(m.lines, lineSystem, "cancelled before start") {
		t.Fatalf("queued cancellation = task=%#v lines=%#v", m.backgroundAgents["agent-5"], m.lines)
	}
	next, cmd = m.submit("/agent admitted after terminal retention")
	m = next.(*model)
	if cmd != nil || len(m.backgroundAgents) != maxRetainedBackgroundAgents || m.backgroundAgents["agent-5"] != nil || m.backgroundAgents["agent-17"] == nil || m.backgroundAgents["agent-17"].state != backgroundAgentQueued {
		t.Fatalf("terminal retention should make room for a queued task: count=%d old=%#v new=%#v cmd=%v", len(m.backgroundAgents), m.backgroundAgents["agent-5"], m.backgroundAgents["agent-17"], cmd)
	}
}

func TestBackgroundAgentCancelAllOnlyStopsBackgroundChildren(t *testing.T) {
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		BackgroundAgent: func(context.Context, *chat.Session, string) (string, error) {
			return "unused", nil
		},
	})
	m.mode = modeBusy
	m.turnID = 8
	m.queue = []string{"parent follow-up"}
	foregroundCancelled := false
	m.turnCancel = func() { foregroundCancelled = true }
	for index := 0; index < maxBackgroundAgents+2; index++ {
		next, _ := m.submit("/agent task " + itoa(index+1))
		m = next.(*model)
	}

	next, cmd := m.submit("/agents cancel all")
	m = next.(*model)
	if cmd != nil || m.mode != modeBusy || m.turnID != 8 || foregroundCancelled || !reflect.DeepEqual(m.queue, []string{"parent follow-up"}) ||
		m.activeBackgroundAgents() != maxBackgroundAgents || m.queuedBackgroundAgents() != 0 ||
		m.backgroundAgents["agent-5"].state != backgroundAgentCancelled || m.backgroundAgents["agent-6"].state != backgroundAgentCancelled ||
		!hasLineContaining(m.lines, lineSystem, "cancellation requested for 4 running; 2 queued cancelled before start") {
		t.Fatalf("cancel all changed parent state or child states: mode=%s turn=%d foregroundCancelled=%v queue=%#v tasks=%#v lines=%#v", modeName(m.mode), m.turnID, foregroundCancelled, m.queue, m.backgroundAgents, m.lines)
	}
	next, cmd = m.submit("/agents cancel all")
	if cmd != nil || !hasLineContaining(next.(*model).lines, lineSystem, "no running or queued background agents") {
		t.Fatalf("repeated cancel all should be a no-op: %#v", next.(*model).lines)
	}
}

func TestBackgroundAgentAppendRequiresCompletedResultAndClipsComposerPayload(t *testing.T) {
	m := newModel(Deps{Ctx: context.Background(), Session: mustSession(t, &staticModel{}, "system"), BackgroundAgent: func(context.Context, *chat.Session, string) (string, error) {
		return "unused", nil
	}})
	m.backgroundAgents = map[string]*backgroundAgentTask{
		"agent-working": {id: "agent-working", state: backgroundAgentWorking},
		"agent-done": {
			id:              "agent-done",
			sessionID:       "source-session",
			state:           backgroundAgentCompleted,
			answer:          strings.Repeat("x", maxBackgroundAgentAttachment+128) + "\x1b[2J",
			answerTruncated: true,
		},
	}
	m.backgroundAgentOrder = []string{"agent-working", "agent-done"}
	m.textarea.SetValue("keep this draft")

	next, cmd := m.submit("/agents append agent-working")
	m = next.(*model)
	if cmd != nil || m.textarea.Value() != "keep this draft" || !hasLineContaining(m.lines, lineError, "has no completed result") {
		t.Fatalf("working result append rejection = draft=%q lines=%#v", m.textarea.Value(), m.lines)
	}
	next, cmd = m.submit("/agents append agent-done")
	m = next.(*model)
	if cmd != nil || !strings.Contains(m.textarea.Value(), "source-session") ||
		!strings.Contains(m.textarea.Value(), "Report clipped to 16384 bytes") || strings.Contains(m.textarea.Value(), "\x1b") {
		t.Fatalf("bounded append draft = %q", m.textarea.Value())
	}
	if len(m.textarea.Value()) > len("keep this draft\n\n")+maxBackgroundAgentAttachment+512 {
		t.Fatalf("composer attachment exceeded bounded allowance: %d bytes", len(m.textarea.Value()))
	}
}

func TestBackgroundAgentAppendCanPrepareDraftWhileForegroundTurnIsBusy(t *testing.T) {
	m := newModel(Deps{Ctx: context.Background(), Session: mustSession(t, &staticModel{}, "system"), BackgroundAgent: func(context.Context, *chat.Session, string) (string, error) {
		return "unused", nil
	}})
	m.mode = modeBusy
	m.turnID = 3
	m.queue = []string{"keep queued"}
	m.backgroundAgents = map[string]*backgroundAgentTask{
		"agent-1": {id: "agent-1", sessionID: "source", state: backgroundAgentCompleted, answer: "independent finding"},
	}
	m.backgroundAgentOrder = []string{"agent-1"}

	next, cmd := m.queueWhileBusy("/agents append agent-1")
	m = next.(*model)
	if cmd != nil || m.mode != modeBusy || m.turnID != 3 || !reflect.DeepEqual(m.queue, []string{"keep queued"}) ||
		!strings.Contains(m.textarea.Value(), "independent finding") {
		t.Fatalf("busy append changed foreground state: mode=%s turn=%d queue=%#v draft=%q cmd=%v", modeName(m.mode), m.turnID, m.queue, m.textarea.Value(), cmd)
	}
}

func TestBackgroundAgentCommandUsageAndTaskSummary(t *testing.T) {
	m := newTestModel(t)
	next, cmd := m.submit("/agent")
	if cmd != nil || !hasLineContaining(next.(*model).lines, lineError, "usage: /agent [--diff] <analysis task>") {
		t.Fatalf("agent usage = %#v", next.(*model).lines)
	}
	next, _ = m.submit("/agents")
	if !hasLineContaining(next.(*model).lines, lineSystem, "no background analysis runtime is configured") {
		t.Fatalf("unavailable agents list = %#v", next.(*model).lines)
	}

	m = newModel(Deps{Ctx: context.Background(), Session: mustSession(t, &staticModel{}, "system"), BackgroundAgent: func(context.Context, *chat.Session, string) (string, error) {
		return "", errors.New("not started")
	}})
	summary := renderTasksCommand(m)
	if !strings.Contains(summary, "Background analysis agents: 0 active, 0 retained") {
		t.Fatalf("task summary omitted configured background runtime: %q", summary)
	}
}
