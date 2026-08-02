package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/schema"
)

type tuiCheckpointCompactor func(context.Context, contextbuild.CompactionRequest) (contextbuild.Checkpoint, error)

func (f tuiCheckpointCompactor) Compact(ctx context.Context, request contextbuild.CompactionRequest) (contextbuild.Checkpoint, error) {
	return f(ctx, request)
}

func TestSessionsListIncludesTokensCost(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewThreadStore(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()
	session, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{
		Store: st,
		Title: "tok-test",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	state, err = st.StartTurn(ctx, session.ID(), state.Revision, store.TurnStart{TurnID: "list-usage", Input: "seed"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	_, err = st.CommitTurn(ctx, session.ID(), state.Revision, store.TurnCommit{
		TurnID: "list-usage",
		Messages: []*schema.Message{
			schema.UserMessage("seed"),
			schema.AssistantMessage("done", nil),
		},
		Usage: store.UsageDelta{TotalTokens: 1500, CostUSD: 0.0123},
	})
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}

	m := newModel(Deps{
		Ctx:     ctx,
		Session: session,
		Store:   st,
		Status:  StatusInfo{Model: "m"},
	})
	m.width = 100
	next, _ := m.submit("/sessions")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "tokens=") {
		t.Fatalf("sessions missing tokens=: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "cost=") {
		t.Fatalf("sessions missing cost=: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "/delete") {
		t.Fatalf("sessions footer should mention /delete: %#v", mm.lines)
	}
}

func TestDeleteRefusesActiveAndDeletesOther(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewThreadStore(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx := context.Background()
	active, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: st, Title: "active"})
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	other, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: st, Title: "other"})
	if err != nil {
		t.Fatalf("other: %v", err)
	}

	m := newModel(Deps{
		Ctx:     ctx,
		Session: active,
		Store:   st,
		Status:  StatusInfo{Model: "m"},
	})

	next, _ := m.submit("/delete " + active.ID())
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineError, "cannot delete the active session") {
		t.Fatalf("expected active refuse: %#v", mm.lines)
	}

	next, _ = mm.submit("/delete " + other.ID())
	mm = next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "deleted session "+other.ID()) {
		t.Fatalf("expected delete ok: %#v", mm.lines)
	}
	list, err := st.ListThreads(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, meta := range list {
		if meta.ID == other.ID() {
			t.Fatalf("other session still present")
		}
	}
}

func TestQueueListAndClear(t *testing.T) {
	m := newTestModel(t)
	m.queue = []string{"one", "two"}
	next, _ := m.submit("/queue")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "Queue (2):") {
		t.Fatalf("queue list missing: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "1. one") {
		t.Fatalf("queue item missing: %#v", mm.lines)
	}

	next, _ = mm.submit("/queue clear")
	mm = next.(*model)
	if len(mm.queue) != 0 {
		t.Fatalf("queue should be empty, got %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue cleared") {
		t.Fatalf("clear confirmation missing: %#v", mm.lines)
	}
}

func TestQueueClearRunsImmediatelyWhileBusy(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 7
	m.queue = []string{"first", "second"}
	m.textarea.SetValue("/queue clear")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("immediate queue clear should not start another operation")
	}
	if mm.mode != modeBusy {
		t.Fatalf("queue clear should not interrupt current turn, mode=%s", modeName(mm.mode))
	}
	if len(mm.queue) != 0 {
		t.Fatalf("queued follow-ups survived immediate clear: %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue cleared (2 dropped)") {
		t.Fatalf("missing immediate clear confirmation: %#v", mm.lines)
	}
}

func TestClearCreatesDistinctDurableSession(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	active, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: st, Title: "before clear"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	oldID := active.ID()
	m := newModel(Deps{
		Ctx:          ctx,
		Session:      active,
		Store:        st,
		SystemPrompt: "system",
		SessionOpts:  chat.SessionOptions{Store: st},
		Status:       StatusInfo{Model: "test"},
	})
	m.queue = []string{"must not cross sessions"}
	next, _ := m.submit("/clear")
	mm := next.(*model)
	if mm.deps.Session.ID() == oldID {
		t.Fatalf("/clear reused active thread %q", oldID)
	}
	if len(mm.queue) != 0 {
		t.Fatalf("old queue crossed /clear: %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "previous session retained: "+oldID) {
		t.Fatalf("clear did not make retention explicit: %#v", mm.lines)
	}
	if _, err := chat.OpenSession(&staticModel{}, st, oldID, chat.SessionOptions{Store: st}); err != nil {
		t.Fatalf("old thread is not resumable after /clear: %v", err)
	}
	list, err := st.ListThreads(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("thread count after /clear = %d, want 2", len(list))
	}
}

func TestAutomaticCompactionBlocksQueueDrain(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	compactor := tuiCheckpointCompactor(func(_ context.Context, request contextbuild.CompactionRequest) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			ModelContextTokens:        1_000,
			OutputReserveTokens:       100,
			AutoCompactTriggerPercent: 1,
			PostCompactTargetPercent:  1,
			KeepRecentTurns:           1,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(ctx, "first", nil); err != nil {
		t.Fatalf("first Ask: %v", err)
	}
	if err := session.Ask(ctx, "second", nil); err != nil {
		t.Fatalf("second Ask: %v", err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatal("expected token-pressure auto compaction signal")
	}
	m := newModel(Deps{Ctx: ctx, Session: session, Store: st, Status: StatusInfo{Model: "test"}})
	m.mode = modeBusy
	m.queue = []string{"must wait"}
	cmd := m.finishTurn(nil)
	if cmd == nil || m.mode != modeCompacting {
		t.Fatalf("auto compaction must start before queue drain: mode=%s cmd=%v", modeName(m.mode), cmd)
	}
	if len(m.queue) != 1 || m.queue[0] != "must wait" {
		t.Fatalf("queue drained before compaction: %#v", m.queue)
	}
	if m.compactCancel != nil {
		m.compactCancel()
	}
}

func TestRejectMutativeQueueKeepsDraft(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.textarea.SetValue("/new topic")
	next, _ := m.queueWhileBusy("/new topic")
	mm := next.(*model)
	if len(mm.queue) != 0 {
		t.Fatalf("mutative must not enqueue: %#v", mm.queue)
	}
	if strings.TrimSpace(mm.textarea.Value()) != "/new topic" {
		t.Fatalf("draft must be kept, got %q", mm.textarea.Value())
	}
	if !hasLineContaining(mm.lines, lineError, "cannot queue mutative") {
		t.Fatalf("expected mutative error: %#v", mm.lines)
	}
}

func TestIsQueueableInput(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"hello", true},
		{"/help", false},
		{"/status", false},
		{"/context", false},
		{"/sessions", false},
		{"/clear", false},
		{"/compact", false},
		{"/compact preserve constraints", false},
		{"/queue", false},
		{"/queue clear", false},
		{"/unknown", true},
		{"/new", false},
		{"/new title", false},
		{"/resume id", false},
		{"/title x", false},
		{"/delete id", false},
		{"/exit", false},
		{"/quit", false},
	}
	for _, tc := range cases {
		if got := isQueueableInput(tc.in); got != tc.want {
			t.Errorf("isQueueableInput(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestBusyInputDisposition(t *testing.T) {
	cases := []struct {
		in   string
		want busyInputDisposition
	}{
		{"follow up", busyInputEnqueue},
		{"/help", busyInputExecuteImmediately},
		{"/context", busyInputExecuteImmediately},
		{"/status", busyInputExecuteImmediately},
		{"/sessions", busyInputExecuteImmediately},
		{"/queue", busyInputExecuteImmediately},
		{"/queue clear", busyInputExecuteImmediately},
		{"/compact", busyInputReject},
		{"/compact keep test evidence", busyInputReject},
		{"/clear", busyInputReject},
		{"/new topic", busyInputReject},
		{"/resume sess-1", busyInputReject},
		{"/title title", busyInputReject},
		{"/delete sess-1", busyInputReject},
		{"/exit", busyInputReject},
	}
	for _, tc := range cases {
		if got := classifyBusyInput(tc.in); got != tc.want {
			t.Errorf("classifyBusyInput(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	if !isImmediatelyExecutableWhileBusy("/queue clear") {
		t.Fatal("/queue clear must clear queued follow-ups immediately while busy")
	}
}

func TestHomeEndScrollKeys(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 50; i++ {
		m.appendLine(lineSystem, "line content for scrolling")
	}
	m.width = 80
	m.height = 24
	m.layout()
	m.refreshViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyHome})
	mm := next.(*model)
	if mm.stickBottom {
		t.Fatalf("Home should clear stickBottom")
	}
	if mm.viewport.AtBottom() {
		t.Fatalf("Home should leave bottom")
	}

	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnd})
	mm = next.(*model)
	if !mm.stickBottom {
		t.Fatalf("End should set stickBottom")
	}
	if !mm.viewport.AtBottom() {
		t.Fatalf("End should go to bottom")
	}
}
