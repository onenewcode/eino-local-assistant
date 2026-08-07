package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"

	tea "github.com/charmbracelet/bubbletea"
)

func TestModelSwitchIdleKeepsSessionAndUpdatesSnapshots(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	oldModel := &staticModel{}
	session, err := chat.NewSession(oldModel, "frozen system", chat.SessionOptions{
		Store:     threadStore,
		ModelName: "old-model",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	oldTranscript := session.Transcript()
	oldStatus := StatusInfo{
		Model:                    "openai/old-model",
		DeclaredCatalogLifecycle: "active",
		Tools:                    []string{"shell", "apply_patch"},
		ReasoningEffort:          "medium",
		MaxModelSteps:            8,
		CmdPolicy:                "cmd=ask",
		Sandbox:                  SandboxInfo{Mode: "workspace", Backend: "seatbelt"},
		Runtime:                  RuntimeInfo{MaxModelSteps: 8, MaxToolCalls: 12, MaxConsecutiveEquivalentToolCalls: 3},
	}
	oldOpts := chat.SessionOptions{Store: threadStore, ModelName: "old-model"}
	m := newModel(Deps{
		Ctx:         ctx,
		Session:     session,
		Store:       threadStore,
		Status:      oldStatus,
		SessionOpts: oldOpts,
	})
	m.queue = []string{"queued follow-up"}
	m.queuePaused = true
	m.taskPaneOpen = true
	m.reasoningDetailsVisible = false
	m.textarea.SetValue("draft that is not the slash submission")
	generation := m.sessionGeneration
	turnID := m.turnID
	replacement := &staticModel{}
	wantStatus := StatusInfo{
		Model:                    "openai/new-model",
		DeclaredCatalogLifecycle: "deprecated",
		Tools:                    []string{"shell", "apply_patch", "memory_read"},
		ReasoningEffort:          "high",
		MaxModelSteps:            12,
		CmdPolicy:                oldStatus.CmdPolicy,
		Sandbox:                  oldStatus.Sandbox,
		Runtime:                  oldStatus.Runtime,
	}
	wantOpts := chat.SessionOptions{Store: threadStore, ModelName: "new-model"}
	var callbackCalls int
	m.deps.SwitchModel = func(callbackCtx context.Context, got *chat.Session, name string) (ModelSwitchResult, error) {
		callbackCalls++
		if got != session {
			t.Fatalf("callback session=%p, want active session %p", got, session)
		}
		if name != "new-model" {
			t.Fatalf("callback model name=%q", name)
		}
		if err := got.ReplaceModel(callbackCtx, chat.ModelBinding{Model: replacement, ModelName: name}); err != nil {
			return ModelSwitchResult{}, err
		}
		return ModelSwitchResult{Status: wantStatus, SessionOpts: wantOpts}, nil
	}

	next, cmd := m.submit("/model new-model")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("model switch should be synchronous control-plane work, got command %v", cmd)
	}
	if callbackCalls != 1 {
		t.Fatalf("callback calls=%d, want 1", callbackCalls)
	}
	if mm.activeSession() != session || session.Model() != replacement || session.ModelName() != "new-model" {
		t.Fatalf("session binding changed incorrectly: active=%p model=%p name=%q", mm.activeSession(), session.Model(), session.ModelName())
	}
	if mm.sessionGeneration != generation || mm.turnID != turnID {
		t.Fatalf("in-place switch changed transient identity: generation=%d/%d turn=%d/%d", mm.sessionGeneration, generation, mm.turnID, turnID)
	}
	if !reflect.DeepEqual(mm.deps.Status, wantStatus) || !reflect.DeepEqual(mm.deps.SessionOpts, wantOpts) {
		t.Fatalf("runtime snapshots not replaced atomically: status=%#v opts=%#v", mm.deps.Status, mm.deps.SessionOpts)
	}
	if !reflect.DeepEqual(mm.queue, []string{"queued follow-up"}) || !mm.queuePaused || !mm.taskPaneOpen || mm.reasoningDetailsVisible {
		t.Fatalf("in-place switch changed TUI state: queue=%#v paused=%v task=%v reasoning=%v", mm.queue, mm.queuePaused, mm.taskPaneOpen, mm.reasoningDetailsVisible)
	}
	if got := mm.textarea.Value(); got != "draft that is not the slash submission" {
		t.Fatalf("model switch changed unrelated draft: %q", got)
	}
	if !reflect.DeepEqual(session.Transcript(), oldTranscript) || session.SystemPrompt() != "frozen system" {
		t.Fatalf("model switch changed transcript/system prompt: transcript=%#v prompt=%q", session.Transcript(), session.SystemPrompt())
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.Model != "new-model" {
		t.Fatalf("durable model=%q, want new-model", state.Meta.Model)
	}
	if !hasLineContaining(mm.lines, lineSystem, "model switched to new-model") {
		t.Fatalf("switch confirmation missing: %#v", mm.lines)
	}
}

func TestModelSwitchRejectsBusyApprovalAndSideQuestionWithoutQueueing(t *testing.T) {
	cases := []struct {
		name          string
		mode          mode
		pending       bool
		sideQuestions int
	}{
		{name: "busy", mode: modeBusy},
		{name: "compacting", mode: modeCompacting},
		{name: "approval", mode: modeIdle, pending: true},
		{name: "side question", mode: modeIdle, sideQuestions: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.mode = tc.mode
			m.pendingApproval = nil
			if tc.pending {
				m.pendingApproval = &approvalRequestMsg{}
			}
			m.sideQuestions = tc.sideQuestions
			m.queue = []string{"must remain"}
			m.textarea.SetValue("/model new-model")
			beforeQueue := append([]string(nil), m.queue...)
			beforeDraft := m.textarea.Value()
			var callbackCalls int
			m.deps.SwitchModel = func(context.Context, *chat.Session, string) (ModelSwitchResult, error) {
				callbackCalls++
				return ModelSwitchResult{}, errors.New("must not be called")
			}

			var next tea.Model
			if m.mode == modeIdle {
				next, _ = m.submit("/model new-model")
			} else {
				next, _ = m.queueWhileBusy("/model new-model")
			}
			mm := next.(*model)
			if callbackCalls != 0 {
				t.Fatal("rejected model switch invoked callback")
			}
			if !reflect.DeepEqual(mm.queue, beforeQueue) {
				t.Fatalf("rejected model switch changed queue: %#v", mm.queue)
			}
			if mm.textarea.Value() != beforeDraft {
				t.Fatalf("rejected model switch changed draft: %q", mm.textarea.Value())
			}
			if !hasLineContaining(mm.lines, lineError, "model switch unavailable") {
				t.Fatalf("rejection feedback missing: %#v", mm.lines)
			}
		})
	}
}

func TestModelSwitchCallbackFailureLeavesSnapshotsAndSessionUnchanged(t *testing.T) {
	m := newTestModel(t)
	oldStatus := m.deps.Status
	oldOpts := m.deps.SessionOpts
	oldSession := m.activeSession()
	oldModel := oldSession.Model()
	oldName := oldSession.ModelName()
	wantErr := errors.New("provider construction failed")
	m.deps.SwitchModel = func(context.Context, *chat.Session, string) (ModelSwitchResult, error) {
		return ModelSwitchResult{}, wantErr
	}

	next, _ := m.submit("/model new-model")
	mm := next.(*model)
	if !errors.Is(wantErr, wantErr) {
		t.Fatal("test error was not initialized")
	}
	if mm.deps.Session != oldSession || !reflect.DeepEqual(mm.deps.Status, oldStatus) || !reflect.DeepEqual(mm.deps.SessionOpts, oldOpts) {
		t.Fatalf("callback failure changed active state: session=%p status=%#v opts=%#v", mm.deps.Session, mm.deps.Status, mm.deps.SessionOpts)
	}
	if oldSession.Model() != oldModel || oldSession.ModelName() != oldName {
		t.Fatalf("callback failure changed session binding: model=%p name=%q", oldSession.Model(), oldSession.ModelName())
	}
	if !hasLineContaining(mm.lines, lineError, "provider construction failed") {
		t.Fatalf("callback error missing: %#v", mm.lines)
	}
}

func TestResumeCallbackReplacesRuntimeSnapshotAfterOpeningTarget(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	active, err := chat.NewSession(&staticModel{}, "active", chat.SessionOptions{Store: threadStore, ModelName: "active-model"})
	if err != nil {
		t.Fatalf("active NewSession: %v", err)
	}
	target, err := chat.NewSession(&staticModel{}, "target", chat.SessionOptions{Store: threadStore, ModelName: "target-model"})
	if err != nil {
		t.Fatalf("target NewSession: %v", err)
	}
	m := newModel(Deps{
		Ctx:         ctx,
		Session:     active,
		Store:       threadStore,
		Status:      StatusInfo{Model: "openai/active-model"},
		SessionOpts: chat.SessionOptions{Store: threadStore, ModelName: "active-model"},
	})
	wantStatus := StatusInfo{Model: "openai/target-model", ReasoningEffort: "high", MaxModelSteps: 9}
	wantOpts := chat.SessionOptions{Store: threadStore, ModelName: "target-model"}
	var gotID string
	var gotRecover bool
	m.deps.OpenSession = func(_ context.Context, id string, recoverInterrupted bool) (SessionOpenResult, error) {
		gotID = id
		gotRecover = recoverInterrupted
		return SessionOpenResult{Session: target, Status: wantStatus, SessionOpts: wantOpts}, nil
	}

	next, _ := m.submit("/resume " + target.ID() + " --recover")
	mm := next.(*model)
	if gotID != target.ID() || !gotRecover || mm.activeSession() != target {
		t.Fatalf("resume callback arguments/session: id=%q recover=%v active=%p target=%p", gotID, gotRecover, mm.activeSession(), target)
	}
	if !reflect.DeepEqual(mm.deps.Status, wantStatus) || !reflect.DeepEqual(mm.deps.SessionOpts, wantOpts) {
		t.Fatalf("resume snapshots = status=%#v opts=%#v", mm.deps.Status, mm.deps.SessionOpts)
	}
	if strings.Contains(mm.statusReport(), "active-model") {
		t.Fatalf("resume status retained old model: %q", mm.statusReport())
	}
}
