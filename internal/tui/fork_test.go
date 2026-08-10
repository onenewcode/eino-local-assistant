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
	"github.com/cloudwego/eino/schema"
)

func TestForkSwitchesToChildAndPreservesSource(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	source, err := chat.NewSession(&transcriptSeedModel{responses: []string{"source answer"}}, "system", chat.SessionOptions{
		Store: threadStore,
		Title: "source title",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := source.Ask(ctx, "source request", nil); err != nil {
		t.Fatalf("seed Ask: %v", err)
	}

	sourceBefore, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatalf("LoadThread before fork: %v", err)
	}
	sourceTranscript := source.Transcript()
	var notifications []string
	composeCalls := 0
	m := newModel(Deps{
		Ctx:     ctx,
		Session: source,
		Store:   threadStore,
		Status:  StatusInfo{Model: "test-model"},
		ComposeSystemPrompt: func() (string, error) {
			composeCalls++
			return "should not be called", nil
		},
		NotifyActiveSession: func(id string) {
			notifications = append(notifications, id)
		},
	})
	m.sideLines = []transcriptLine{{kind: lineSide, text: "old side result"}}
	m.queue = []string{"old queued input"}
	m.taskPaneOpen = true
	m.currentTool = "old-tool"
	m.openReasoning = 0
	m.openToolCards = map[string]int{"old-call": 1}
	m.openToolNames = map[string]string{"old-call": "old-tool"}
	oldGeneration := m.sessionGeneration

	next, cmd := m.submit("/fork")
	mm := next.(*model)
	if cmd != nil {
		t.Fatal("fork should complete synchronously")
	}
	if composeCalls != 0 {
		t.Fatalf("fork recomposed the system prompt %d times", composeCalls)
	}
	child := mm.activeSession()
	if child == nil || child.ID() == source.ID() {
		t.Fatalf("active session after fork = %#v, want a distinct child", child)
	}
	if mm.sessionGeneration != oldGeneration+1 {
		t.Fatalf("session generation = %d, want %d", mm.sessionGeneration, oldGeneration+1)
	}
	if len(notifications) != 1 || notifications[0] != child.ID() {
		t.Fatalf("active-session notifications = %#v, want [%q]", notifications, child.ID())
	}

	childMeta, err := threadStore.LoadThreadMeta(ctx, child.ID())
	if err != nil {
		t.Fatalf("LoadThreadMeta child: %v", err)
	}
	if childMeta.ParentID != source.ID() {
		t.Fatalf("child parent = %q, want %q", childMeta.ParentID, source.ID())
	}
	if childMeta.Title != "source title" || child.Title() != "source title" {
		t.Fatalf("child title = meta %q/session %q, want inherited source title", childMeta.Title, child.Title())
	}
	if childMeta.ForkBoundaryTurnID == "" {
		t.Fatal("child is missing its committed fork boundary")
	}

	sourceAfter, err := threadStore.LoadThread(ctx, source.ID())
	if err != nil {
		t.Fatalf("LoadThread after fork: %v", err)
	}
	if sourceAfter.Revision != sourceBefore.Revision {
		t.Fatalf("source revision changed from %d to %d", sourceBefore.Revision, sourceAfter.Revision)
	}
	if !reflect.DeepEqual(sourceTranscript, source.Transcript()) {
		t.Fatalf("source session transcript changed: before=%#v after=%#v", sourceTranscript, source.Transcript())
	}
	if !hasLineContaining(mm.lines, lineSystem, "forked "+child.ID()+" from "+source.ID()) {
		t.Fatalf("fork banner missing: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "title: source title") {
		t.Fatalf("inherited child title banner missing: %#v", mm.lines)
	}
	if len(mm.sideLines) != 0 {
		t.Fatalf("side-only state crossed fork: %#v", mm.sideLines)
	}
	if len(mm.queue) != 0 {
		t.Fatalf("queue crossed fork: %#v", mm.queue)
	}
	if mm.taskPaneOpen {
		t.Fatal("task pane should close after a session switch")
	}
	if mm.currentTool != "" || mm.openReasoning != noOpenReasoning || len(mm.openToolCards) != 0 || len(mm.openToolNames) != 0 {
		t.Fatalf("old tool/reasoning UI crossed fork: tool=%q reasoning=%d cards=%v names=%v", mm.currentTool, mm.openReasoning, mm.openToolCards, mm.openToolNames)
	}
	if len(mm.inputHist.entries) != 1 || mm.inputHist.entries[0] != "source request" {
		t.Fatalf("child input history = %#v, want source committed user input", mm.inputHist.entries)
	}
	if hasLineContaining(mm.sideLines, lineSide, "old side result") {
		t.Fatal("fork result was rendered as side output")
	}
}

func TestForkRejectsUnknownSelectorWithoutCreatingAChild(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	source, err := chat.NewSession(&transcriptSeedModel{responses: []string{"source answer"}}, "system", chat.SessionOptions{
		Store: threadStore,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := source.Ask(ctx, "source request", nil); err != nil {
		t.Fatalf("seed Ask: %v", err)
	}
	before, err := threadStore.ListThreads(ctx)
	if err != nil {
		t.Fatalf("ListThreads before: %v", err)
	}
	m := newModel(Deps{Ctx: ctx, Session: source, Store: threadStore})

	next, _ := m.submit("/fork ordinary title")
	mm := next.(*model)
	if mm.activeSession() != source {
		t.Fatal("unknown fork selector unexpectedly changed the active session")
	}
	if !hasLineContaining(mm.lines, lineError, "no active session with ID or name") {
		t.Fatalf("unknown fork selector error missing: %#v", mm.lines)
	}
	after, err := threadStore.ListThreads(ctx)
	if err != nil {
		t.Fatalf("ListThreads after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("fork argument created a child: before=%d after=%d", len(before), len(after))
	}
}

func TestForkSelectedActiveSessionByName(t *testing.T) {
	ctx := context.Background()
	m, threadStore, current := newSessionPickerTestModel(t)
	source := newSessionPickerTestSession(t, threadStore, "Completed investigation")
	if err := source.Ask(ctx, "capture the working answer", nil); err != nil {
		t.Fatalf("seed source Ask: %v", err)
	}
	currentBefore, err := threadStore.LoadThread(ctx, current.ID())
	if err != nil {
		t.Fatalf("LoadThread current before fork: %v", err)
	}
	m.textarea.SetValue("draft remains available after a selected fork")

	next, _ := m.submit("/fork Completed investigation")
	mm := next.(*model)
	child := mm.activeSession()
	if child == nil || child.ID() == current.ID() || child.ID() == source.ID() {
		t.Fatalf("selected fork child = %#v, want a new session", child)
	}
	childMeta, err := threadStore.LoadThreadMeta(ctx, child.ID())
	if err != nil {
		t.Fatalf("LoadThreadMeta child: %v", err)
	}
	if childMeta.ParentID != source.ID() {
		t.Fatalf("child parent = %q, want selected source %q", childMeta.ParentID, source.ID())
	}
	currentAfter, err := threadStore.LoadThread(ctx, current.ID())
	if err != nil {
		t.Fatalf("LoadThread current after fork: %v", err)
	}
	if !reflect.DeepEqual(currentAfter, currentBefore) {
		t.Fatalf("selected fork changed the previously active session: before=%#v after=%#v", currentBefore, currentAfter)
	}
	if !hasLineContaining(mm.lines, lineSystem, "forked "+child.ID()+" from "+source.ID()) {
		t.Fatalf("selected fork banner missing: %#v", mm.lines)
	}
	if mm.textarea.Value() != "draft remains available after a selected fork" {
		t.Fatalf("selected fork changed composer draft: %q", mm.textarea.Value())
	}
}

func TestForkSelectedSourceUsesRuntimeBindingCallback(t *testing.T) {
	ctx := context.Background()
	m, threadStore, current := newSessionPickerTestModel(t)
	source := newSessionPickerTestSession(t, threadStore, "Different durable model")
	if err := source.Ask(ctx, "persist a parent turn", nil); err != nil {
		t.Fatalf("seed source Ask: %v", err)
	}
	wantStatus := StatusInfo{Model: "openai/source-model", ReasoningEffort: "high"}
	wantOpts := chat.SessionOptions{Store: threadStore, ModelName: "source-model", ReasoningEffort: "high"}
	var gotID string
	m.deps.ForkSession = func(ctx context.Context, id string) (SessionForkResult, error) {
		gotID = id
		child, result, err := source.Fork(ctx, "", "")
		if err != nil {
			return SessionForkResult{}, err
		}
		return SessionForkResult{Session: child, Fork: result, Status: wantStatus, SessionOpts: wantOpts}, nil
	}

	next, _ := m.submit("/fork " + source.ID())
	mm := next.(*model)
	if gotID != source.ID() || mm.activeSession() == current || mm.activeSession().ID() == source.ID() {
		t.Fatalf("runtime fork callback/source switch = id=%q active=%#v", gotID, mm.activeSession())
	}
	if mm.deps.Status.Model != wantStatus.Model || mm.deps.Status.ReasoningEffort != wantStatus.ReasoningEffort ||
		mm.deps.SessionOpts.ModelName != wantOpts.ModelName || mm.deps.SessionOpts.ReasoningEffort != wantOpts.ReasoningEffort {
		t.Fatalf("runtime fork snapshot was not installed: status=%#v opts=%#v", mm.deps.Status, mm.deps.SessionOpts)
	}
}

func TestForkLastAndArchivedSelectors(t *testing.T) {
	ctx := context.Background()
	m, threadStore, current := newSessionPickerTestModel(t)
	latest := newSessionPickerTestSession(t, threadStore, "Newest finished work")
	if err := latest.Ask(ctx, "persist a latest parent turn", nil); err != nil {
		t.Fatalf("seed latest Ask: %v", err)
	}
	next, _ := m.submit("/fork --last")
	mm := next.(*model)
	child := mm.activeSession()
	if child == nil || child.ID() == current.ID() {
		t.Fatalf("--last did not create a child: %#v", child)
	}
	childMeta, err := threadStore.LoadThreadMeta(ctx, child.ID())
	if err != nil {
		t.Fatalf("LoadThreadMeta --last child: %v", err)
	}
	if childMeta.ParentID != latest.ID() {
		t.Fatalf("--last parent = %q, want newest %q", childMeta.ParentID, latest.ID())
	}

	archived := newSessionPickerTestSession(t, threadStore, "Archived branch")
	if err := archived.Ask(ctx, "persist archived parent turn", nil); err != nil {
		t.Fatalf("seed archived Ask: %v", err)
	}
	state, err := threadStore.LoadThread(ctx, archived.ID())
	if err != nil {
		t.Fatalf("LoadThread archived: %v", err)
	}
	if _, err := threadStore.ArchiveThread(ctx, archived.ID(), state.Revision); err != nil {
		t.Fatalf("ArchiveThread: %v", err)
	}
	next, _ = mm.submit("/fork Archived branch")
	mm = next.(*model)
	if mm.activeSession() != child || !hasLineContaining(mm.lines, lineError, "no active session with ID or name") {
		t.Fatalf("archived selector changed state or was accepted: active=%#v lines=%#v", mm.activeSession(), mm.lines)
	}
	next, _ = mm.submit("/fork " + archived.ID())
	mm = next.(*model)
	if mm.activeSession() != child || !hasLineContaining(mm.lines, lineError, "thread is archived") {
		t.Fatalf("archived ID changed state or was accepted: active=%#v lines=%#v", mm.activeSession(), mm.lines)
	}
}

func TestForkRejectsPendingApproval(t *testing.T) {
	m := newTestModel(t)
	m.pendingApproval = &approvalRequestMsg{ID: "fork-approval"}
	activeID := m.activeSession().ID()

	next, cmd := m.submit("/fork")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("approval fork returned command: %v", cmd)
	}
	if mm.activeSession().ID() != activeID {
		t.Fatal("fork changed active session while approval was pending")
	}
	if !hasLineContaining(mm.lines, lineError, "resolve the pending approval") {
		t.Fatalf("approval rejection missing: %#v", mm.lines)
	}
}

func TestForkChildOpenFailureLeavesSourceActive(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	repository := &failingChildOpenRepository{
		ThreadRepository: threadStore,
		fork:             threadStore,
	}
	source, err := chat.NewSession(&transcriptSeedModel{responses: []string{"source answer"}}, "system", chat.SessionOptions{
		Store: repository,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := source.Ask(ctx, "source request", nil); err != nil {
		t.Fatalf("seed Ask: %v", err)
	}
	var notifications []string
	m := newModel(Deps{
		Ctx:     ctx,
		Session: source,
		Store:   repository,
		NotifyActiveSession: func(id string) {
			notifications = append(notifications, id)
		},
	})

	next, _ := m.submit("/fork")
	mm := next.(*model)
	if mm.activeSession() != source {
		t.Fatalf("child open failure switched active session to %q", mm.activeSession().ID())
	}
	if len(notifications) != 0 {
		t.Fatalf("child open failure notified a child: %#v", notifications)
	}
	if repository.childID == "" || !hasLineContaining(mm.lines, lineError, "forked child \""+repository.childID+"\" was published but could not open") ||
		!hasLineContaining(mm.lines, lineError, "child open failed") {
		t.Fatalf("child open failure missing: %#v", mm.lines)
	}
}

func TestForkRejectsBusyAndCompactingWithoutQueueing(t *testing.T) {
	for _, testCase := range []struct {
		name string
		mode mode
	}{
		{name: "busy", mode: modeBusy},
		{name: "compacting", mode: modeCompacting},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			m := newTestModel(t)
			activeID := m.activeSession().ID()
			m.mode = testCase.mode
			m.textarea.SetValue("/fork named child")

			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			mm := next.(*model)
			if cmd != nil {
				t.Fatalf("busy fork returned command: %v", cmd)
			}
			if mm.activeSession().ID() != activeID {
				t.Fatalf("busy fork changed active session to %q", mm.activeSession().ID())
			}
			if len(mm.queue) != 0 {
				t.Fatalf("busy fork was queued: %#v", mm.queue)
			}
			if strings.TrimSpace(mm.textarea.Value()) != "/fork named child" {
				t.Fatalf("rejected fork draft was lost: %q", mm.textarea.Value())
			}
			if !hasLineContaining(mm.lines, lineError, "cannot queue mutative") {
				t.Fatalf("busy fork rejection missing: %#v", mm.lines)
			}
		})
	}
}

func TestForkSurfacesUnavailableUnsupportedAndDurableErrors(t *testing.T) {
	t.Run("unavailable", func(t *testing.T) {
		m := newModel(Deps{Ctx: context.Background()})
		next, _ := m.submit("/fork")
		mm := next.(*model)
		if !hasLineContaining(mm.lines, lineError, "fork: session is unavailable") {
			t.Fatalf("unavailable session error missing: %#v", mm.lines)
		}
	})

	t.Run("unsupported repository", func(t *testing.T) {
		threadStore, err := store.NewThreadStore(t.TempDir())
		if err != nil {
			t.Fatalf("NewThreadStore: %v", err)
		}
		repository := noForkRepository{ThreadRepository: threadStore}
		source, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: repository})
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		m := newModel(Deps{Ctx: context.Background(), Session: source, Store: repository})
		next, _ := m.submit("/fork")
		mm := next.(*model)
		if mm.activeSession() != source {
			t.Fatal("unsupported fork changed the active session")
		}
		if !hasLineContaining(mm.lines, lineError, "fork: session fork is unsupported") {
			t.Fatalf("unsupported error missing: %#v", mm.lines)
		}
	})

	for _, testCase := range []struct {
		name      string
		setup     func(context.Context, *store.ThreadStore, string) error
		wantError string
	}{
		{
			name:      "empty source",
			wantError: store.ErrForkNoCommittedTurn.Error(),
		},
		{
			name: "active turn",
			setup: func(ctx context.Context, threadStore *store.ThreadStore, id string) error {
				state, err := threadStore.LoadThread(ctx, id)
				if err != nil {
					return err
				}
				_, err = threadStore.StartTurn(ctx, id, state.Revision, store.TurnStart{TurnID: "active-turn", Input: "unfinished"})
				return err
			},
			wantError: store.ErrForkActiveTurn.Error(),
		},
		{
			name: "pending compaction",
			setup: func(ctx context.Context, threadStore *store.ThreadStore, id string) error {
				state, err := threadStore.LoadThread(ctx, id)
				if err != nil {
					return err
				}
				_, err = threadStore.StartCompaction(ctx, id, state.Revision, store.CompactionStart{OperationID: "pending-compaction"})
				return err
			},
			wantError: store.ErrForkPendingCompaction.Error(),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			threadStore, err := store.NewThreadStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewThreadStore: %v", err)
			}
			source, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: threadStore})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			if testCase.setup != nil {
				if err := testCase.setup(context.Background(), threadStore, source.ID()); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			m := newModel(Deps{Ctx: context.Background(), Session: source, Store: threadStore})
			next, _ := m.submit("/fork")
			mm := next.(*model)
			if mm.activeSession() != source {
				t.Fatal("durable fork rejection changed the active session")
			}
			if !hasLineContaining(mm.lines, lineError, "fork: "+testCase.wantError) {
				t.Fatalf("fork error %q missing: %#v", testCase.wantError, mm.lines)
			}
		})
	}
}

type noForkRepository struct {
	store.ThreadRepository
}

type failingChildOpenRepository struct {
	store.ThreadRepository
	fork    store.ThreadForkRepository
	childID string
}

func (r *failingChildOpenRepository) ForkThread(ctx context.Context, sourceID, childID, lastTurnID string) (store.ForkResult, error) {
	result, err := r.fork.ForkThread(ctx, sourceID, childID, lastTurnID)
	if err == nil {
		r.childID = result.ChildID
	}
	return result, err
}

func (r *failingChildOpenRepository) ForkThreadBeforeFirstTurn(ctx context.Context, sourceID, childID string) (store.ForkResult, error) {
	forkRepository, ok := r.fork.(store.ThreadForkBeforeFirstRepository)
	if !ok {
		return store.ForkResult{}, errors.New("before-first fork is unsupported")
	}
	result, err := forkRepository.ForkThreadBeforeFirstTurn(ctx, sourceID, childID)
	if err == nil {
		r.childID = result.ChildID
	}
	return result, err
}

func (r *failingChildOpenRepository) LoadThreadTranscript(ctx context.Context, id string, limit int) (store.ThreadState, []*schema.Message, error) {
	if id == r.childID {
		return store.ThreadState{}, nil, errors.New("child open failed")
	}
	return r.ThreadRepository.LoadThreadTranscript(ctx, id, limit)
}
