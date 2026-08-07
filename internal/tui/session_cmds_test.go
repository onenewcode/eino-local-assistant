package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/schema"
)

type tuiCheckpointCompactor func(context.Context, contextbuild.CompactionRequest, contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error)

func (f tuiCheckpointCompactor) Compact(ctx context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
	return f(ctx, request, observer)
}

// contextPressureModel creates one oversized completed turn without making the
// incoming user request itself exceed the pre-turn admission gate.
type contextPressureModel struct{}

func (contextPressureModel) Stream(context.Context, []*schema.Message) (chat.Stream, error) {
	return &contextPressureStream{message: schema.AssistantMessage(strings.Repeat("retained evidence ", 700), nil)}, nil
}

type contextPressureStream struct {
	message *schema.Message
}

func (s *contextPressureStream) Recv() (*schema.Message, error) {
	if s.message == nil {
		return nil, io.EOF
	}
	message := s.message
	s.message = nil
	return message, nil
}

func (*contextPressureStream) Close() {}

const validLegacyV1CheckpointJSON = `{"schema_version":1,"source_range":{"from":"event-1","to":"event-1","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event_ids":["event-1"]},"source_event_ids":["event-1"],"source_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","task_goal":"resume legacy task","constraints":[{"text":"constraint","source_event_ids":["event-1"],"confidence":"observed"}],"confirmed_facts":[{"text":"fact","source_event_ids":["event-1"],"confidence":"observed"}],"decisions":[{"decision":"decision","reason":"reason","source_event_ids":["event-1"],"confidence":"observed"}],"attempts_and_results":[{"text":"attempt","result":"result","source_event_ids":["event-1"],"confidence":"observed"}],"files_or_artifacts":[{"ref":"event://event-1","description":"source","source_event_ids":["event-1"],"confidence":"observed"}],"open_questions":[{"text":"question","source_event_ids":["event-1"],"confidence":"unknown"}],"next_actions":[{"text":"next","source_event_ids":["event-1"],"confidence":"inferred"}]}`

func TestSessionsListIncludesAPIUsageContextAndCost(t *testing.T) {
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
	state, err = st.RecordUsage(ctx, session.ID(), store.ModelUsage{
		CallID:              "list-usage-model-1",
		TurnID:              "list-usage",
		Operation:           store.UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        1200,
		CompletionTokens:    300,
		TotalTokens:         1500,
		ContextWindowTokens: 4000,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	_, err = st.CommitTurn(ctx, session.ID(), state.Revision, store.TurnCommit{
		TurnID: "list-usage",
		Messages: []*schema.Message{
			schema.UserMessage("seed"),
			schema.AssistantMessage("done", nil),
		},
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
	if !hasLineContaining(mm.lines, lineSystem, "API usage (exact)") {
		t.Fatalf("sessions missing exact API usage: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "context=1.2k/4.0k (30%)") {
		t.Fatalf("sessions missing context snapshot: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "cost~=") {
		t.Fatalf("sessions missing cost estimate: %#v", mm.lines)
	}
	if hasLineContaining(mm.lines, lineSystem, "tokens=") {
		t.Fatalf("sessions should not call API usage tokens=: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "/delete") {
		t.Fatalf("sessions footer should mention /delete: %#v", mm.lines)
	}
}

func TestContextCommandSeparatesAPISnapshotFromPlannerEstimate(t *testing.T) {
	m := newTestModel(t)
	next, _ := m.submit("/context")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "Last provider request: context=unknown") {
		t.Fatalf("/context should identify an unavailable provider request: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "Planner estimate (local truncation/compaction only; not API usage)") {
		t.Fatalf("/context should label the planner estimate: %#v", mm.lines)
	}
	if hasLineContaining(mm.lines, lineSystem, "\ncurrent=") {
		t.Fatalf("/context should not present the planning estimate as current API context: %#v", mm.lines)
	}
}

func TestStatusReportUsesAPISnapshotRatherThanPlannerTokens(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	session, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	state, err = st.StartTurn(ctx, session.ID(), state.Revision, store.TurnStart{TurnID: "status-usage", Input: "seed"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	state, err = st.RecordUsage(ctx, session.ID(), store.ModelUsage{
		CallID:              "status-usage-model-1",
		TurnID:              "status-usage",
		Operation:           store.UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        1200,
		CompletionTokens:    34,
		TotalTokens:         1234,
		ContextWindowTokens: 4000,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	_, err = st.CommitTurn(ctx, session.ID(), state.Revision, store.TurnCommit{
		TurnID: "status-usage",
		Messages: []*schema.Message{
			schema.UserMessage("seed"),
			schema.AssistantMessage("done", nil),
		},
	})
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	resumed, err := chat.OpenSession(&staticModel{}, st, session.ID(), chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	m := newModel(Deps{Ctx: ctx, Session: resumed, Status: StatusInfo{Model: "m"}})
	report := m.statusReport()
	for _, want := range []string{
		"API usage (exact): input=1.2k completion=34 cached=0 total=1.2k calls=1",
		"cost~=$0", "context=1.2k/4.0k (30%)", "context planner estimate:",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("status report missing %q:\n%s", want, report)
		}
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
	m.queuePaused = true
	m.queue = []string{"one", "two"}
	next, _ := m.submit("/queue")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "Queue (2) [paused]:") {
		t.Fatalf("queue list missing: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "1. one") {
		t.Fatalf("queue item missing: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue edit <1-based-index> <new text>") {
		t.Fatalf("queue edit hint missing: %#v", mm.lines)
	}

	next, _ = mm.submit("/queue clear")
	mm = next.(*model)
	if len(mm.queue) != 0 {
		t.Fatalf("queue should be empty, got %#v", mm.queue)
	}
	if mm.queuePaused {
		t.Fatal("queue clear must clear the pause marker")
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue cleared") {
		t.Fatalf("clear confirmation missing: %#v", mm.lines)
	}
}

func TestQueueClearRunsImmediatelyWhileBusy(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 7
	m.queuePaused = true
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
	if mm.queuePaused {
		t.Fatal("busy queue clear must clear the pause marker")
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
	m.queuePaused = true
	next, _ := m.submit("/clear")
	mm := next.(*model)
	if mm.deps.Session.ID() == oldID {
		t.Fatalf("/clear reused active thread %q", oldID)
	}
	if len(mm.queue) != 0 {
		t.Fatalf("old queue crossed /clear: %#v", mm.queue)
	}
	if mm.queuePaused {
		t.Fatal("/clear must clear the queue pause marker")
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
	compactor := tuiCheckpointCompactor(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := chat.NewSession(contextPressureModel{}, "system", chat.SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens: 1_000,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(ctx, "first", nil); err != nil {
		t.Fatalf("first Ask: %v", err)
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
	if hasLineContaining(m.lines, lineSystem, "context pressure reached") ||
		hasLineContaining(m.lines, lineSystem, "compacting") {
		t.Fatalf("auto compact start must be silent (status-bar only): %#v", m.lines)
	}
	if m.compactCancel != nil {
		m.compactCancel()
	}
}

func TestNoCompactionCandidatesIsSilent(t *testing.T) {
	m := newTestModel(t)
	before := len(m.lines)
	m.mode = modeCompacting
	m.compactAutomatic = false
	cmd := m.finishCompaction(compactDoneMsg{
		err: chat.ErrNoCompactionCandidates,
	})
	if m.mode != modeIdle {
		t.Fatalf("mode=%s want idle", modeName(m.mode))
	}
	if len(m.lines) != before {
		t.Fatalf("no-candidate compact must not append transcript lines: before=%d after=%d lines=%#v",
			before, len(m.lines), m.lines)
	}
	if hasLineContaining(m.lines, lineError, "stable turns") ||
		hasLineContaining(m.lines, lineSystem, "not needed") ||
		hasLineContaining(m.lines, lineSystem, "compacting") {
		t.Fatalf("unexpected compaction chrome: %#v", m.lines)
	}
	// Drain should be a no-op with empty queue; cmd may be nil.
	_ = cmd

	// Automatic path is silent too.
	m.mode = modeCompacting
	m.compactAutomatic = true
	_ = m.finishCompaction(compactDoneMsg{
		automatic: true,
		err:       chat.ErrNoCompactionCandidates,
	})
	if len(m.lines) != before {
		t.Fatalf("automatic no-candidate compact must stay silent: %#v", m.lines)
	}
}

func TestAutomaticCompactionStaleIsSilentAndDefersRetry(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	compactor := tuiCheckpointCompactor(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := chat.NewSession(contextPressureModel{}, "system", chat.SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens: 1_000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatal("expected automatic compaction")
	}

	m := newModel(Deps{Ctx: ctx, Session: session, Store: st, Status: StatusInfo{Model: "test"}})
	m.mode = modeCompacting
	m.compactAutomatic = true
	before := len(m.lines)
	cmd := m.finishCompaction(compactDoneMsg{automatic: true, err: chat.ErrCompactionStale})
	if cmd != nil || m.mode != modeIdle || m.compactAutomatic {
		t.Fatalf("stale automatic compaction should finish without an immediate retry: mode=%s automatic=%v cmd=%v", modeName(m.mode), m.compactAutomatic, cmd)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatal("stale compaction should leave the next stable boundary eligible to retry")
	}
	if len(m.lines) != before || hasLineContaining(m.lines, lineSystem, "automatic context compaction failed") {
		t.Fatalf("stale automatic compaction should stay silent: %#v", m.lines)
	}

	// The normal post-turn boundary performs the deferred retry. This keeps a
	// changing ledger from trapping the TUI in a charged retry loop.
	m.mode = modeBusy
	cmd = m.finishTurn(nil)
	if cmd == nil || m.mode != modeCompacting || !m.compactAutomatic {
		t.Fatalf("next stable boundary did not retry automatic compaction: mode=%s automatic=%v cmd=%v", modeName(m.mode), m.compactAutomatic, cmd)
	}
	if m.compactCancel != nil {
		m.compactCancel()
	}
}

func TestAutomaticCompactionFailurePausesThenDrainsQueue(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	compactor := tuiCheckpointCompactor(func(context.Context, contextbuild.CompactionRequest, contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		return contextbuild.Checkpoint{}, errors.New("provider unavailable")
	})
	session, err := chat.NewSession(contextPressureModel{}, "system", chat.SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens: 1_000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatal("expected automatic compaction")
	}
	_, compactErr := session.CompactAutomatically(ctx)
	if compactErr == nil {
		t.Fatal("automatic compaction unexpectedly succeeded")
	}
	if !session.ContextStatus().AutoCompactionPaused {
		t.Fatalf("failed automatic compaction did not pause: %+v", session.ContextStatus())
	}

	m := newModel(Deps{Ctx: ctx, Session: session, Store: st, Status: StatusInfo{Model: "test"}})
	m.mode = modeCompacting
	m.compactAutomatic = true
	// A local command proves the queue drains without starting an unrelated
	// asynchronous turn that would outlive this focused UI test.
	m.queue = []string{"/context"}
	_ = m.finishCompaction(compactDoneMsg{automatic: true, err: compactErr})
	if !hasLineContaining(m.lines, lineSystem, "automatic compaction paused") {
		t.Fatalf("automatic failure feedback missing: %#v", m.lines)
	}
	if len(m.queue) != 0 {
		t.Fatalf("queue did not drain after automatic failure: %#v", m.queue)
	}
	if session.NeedsAutoCompaction() {
		t.Fatal("paused session still advertises automatic compaction")
	}

	next, _ := m.submit("/context")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "last_compaction=failed") ||
		!hasLineContaining(mm.lines, lineSystem, "auto_pause_reason=automatic compaction failed") ||
		!hasLineContaining(mm.lines, lineSystem, "cache_read is provider-reported") {
		t.Fatalf("/context omitted failed compaction state: %#v", mm.lines)
	}
}

func TestResumeShowsLegacyCheckpointResetNotice(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	active, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := st.CreateThread(ctx, store.ThreadMeta{ID: "tui-legacy-checkpoint"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.CommitCheckpoint(ctx, legacy.ID, legacy.Revision, store.CheckpointInput{
		ID:      "legacy-checkpoint",
		Payload: json.RawMessage(validLegacyV1CheckpointJSON),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Deps{Ctx: ctx, Session: active, Store: st, Status: StatusInfo{Model: "test"}})
	next, _ := m.cmdResume(legacy.ID)
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "legacy checkpoint reset; raw history retained") {
		t.Fatalf("resume reset notice missing: %#v", mm.lines)
	}
	// A CLI-created TUI model receives the same per-open signal.
	cliModel := newModel(Deps{Ctx: ctx, Session: mm.deps.Session, Store: st, Status: StatusInfo{Model: "test"}})
	if !hasLineContaining(cliModel.lines, lineSystem, "legacy checkpoint reset; raw history retained") {
		t.Fatalf("startup reset notice missing: %#v", cliModel.lines)
	}
	// The durable reset outcome remains in history, but a later open did not
	// perform another reset and therefore must not repeat the notice.
	next, _ = mm.cmdResume(legacy.ID)
	again := next.(*model)
	if hasLineContaining(again.lines, lineSystem, "legacy checkpoint reset; raw history retained") {
		t.Fatalf("later resume repeated old reset notice: %#v", again.lines)
	}
}

func TestResumeRecoveryIsExplicitAndStrictlyParsed(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	active, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	target, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.LoadThread(ctx, target.ID())
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartCompaction(ctx, target.ID(), state.Revision, store.CompactionStart{
		OperationID: "tui-interrupted-compact",
	})
	if err != nil {
		t.Fatal(err)
	}

	// A startup recovery option only authorizes the initial OpenSession. A
	// plain in-TUI resume must still leave a live/pending operation alone.
	m := newModel(Deps{
		Ctx:         ctx,
		Session:     active,
		Store:       st,
		SessionOpts: chat.SessionOptions{Store: st, RecoverInterrupted: true},
		Status:      StatusInfo{Model: "test"},
	})
	next, _ := m.submit("/resume " + target.ID())
	mm := next.(*model)
	if mm.deps.Session.ID() != active.ID() {
		t.Fatalf("plain resume unexpectedly switched to pending session %q", target.ID())
	}
	if !hasLineContaining(mm.lines, lineError, "thread has a pending compaction") {
		t.Fatalf("plain resume did not require explicit recovery: %#v", mm.lines)
	}
	state, err = st.LoadThread(ctx, target.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingCompaction == nil {
		t.Fatal("plain resume recovered pending compaction despite no --recover")
	}

	next, _ = mm.submit("/resume " + target.ID() + " --recover")
	mm = next.(*model)
	if mm.deps.Session.ID() != target.ID() {
		t.Fatalf("explicit recovery did not resume target: got %q want %q", mm.deps.Session.ID(), target.ID())
	}
	state, err = st.LoadThread(ctx, target.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingCompaction != nil || state.LastCompaction == nil || state.LastCompaction.Status != store.CompactionOutcomeCancelled {
		t.Fatalf("explicit recovery did not terminally cancel pending compaction: %#v", state)
	}

	beforeRevision := state.Revision
	for _, input := range []string{
		"/resume " + target.ID() + " unexpected",
		"/resume " + target.ID() + " --recover extra",
		"/resume --recover",
	} {
		beforeLines := len(mm.lines)
		next, _ = mm.submit(input)
		mm = next.(*model)
		if len(mm.lines) != beforeLines+1 || mm.lines[len(mm.lines)-1].kind != lineError ||
			!strings.Contains(mm.lines[len(mm.lines)-1].text, "usage: /resume <session-id> [--recover]") {
			t.Fatalf("unsupported resume args %q were not rejected cleanly: %#v", input, mm.lines)
		}
	}
	state, err = st.LoadThread(ctx, target.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != beforeRevision {
		t.Fatalf("invalid resume arguments changed target state: revision=%d want %d", state.Revision, beforeRevision)
	}
}

func TestAutomaticCompactionCancellationIsReportedAsInterrupted(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeCompacting
	m.compactAutomatic = true
	_ = m.finishCompaction(compactDoneMsg{automatic: true, err: context.Canceled})
	if !hasLineContaining(m.lines, lineSystem, "automatic context compaction interrupted") {
		t.Fatalf("automatic cancellation feedback missing: %#v", m.lines)
	}
	if hasLineContaining(m.lines, lineSystem, "automatic context compaction failed") {
		t.Fatalf("automatic cancellation was reported as failure: %#v", m.lines)
	}
}

func TestBlankCheckpointResponseHasActionableCompactionFeedback(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeCompacting
	_ = m.finishCompaction(compactDoneMsg{err: &contextbuild.EmptyCheckpointResponseError{FinishReason: "length"}})
	if !hasLineContaining(m.lines, lineError, "produced no checkpoint") ||
		!hasLineContaining(m.lines, lineError, "finish reason: length") ||
		!hasLineContaining(m.lines, lineError, "check provider/model output limits") {
		t.Fatalf("blank response feedback = %#v", m.lines)
	}
	if hasLineContaining(m.lines, lineError, "decode checkpoint: EOF") {
		t.Fatalf("blank response leaked parser implementation detail: %#v", m.lines)
	}
}

func TestManualCompactDoesNotBannerOnStart(t *testing.T) {
	m := newTestModel(t)
	before := len(m.lines)
	next, _ := m.cmdCompact("")
	mm := next.(*model)
	if mm.mode != modeCompacting {
		t.Fatalf("mode=%s want compacting", modeName(mm.mode))
	}
	if len(mm.lines) != before {
		t.Fatalf("/compact must not emit start banner: %#v", mm.lines)
	}
	if mm.compactCancel != nil {
		mm.compactCancel()
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
		{"/statusline", false},
		{"/tasks", false},
		{"/rules", false},
		{"/btw question", false},
		{"/side question", false},
		{"/usage", false},
		{"/usage off", false},
		{"/context", false},
		{"/sessions", false},
		{"/clear", false},
		{"/compact", false},
		{"/compact preserve constraints", false},
		{"/queue", false},
		{"/queue clear", false},
		{"/queue drop 2", false},
		{"/queue edit 2 replacement", false},
		{"/unknown", true},
		{"/new", false},
		{"/new title", false},
		{"/resume id", false},
		{"/fork", false},
		{"/fork title", false},
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
		{"/statusline", busyInputExecuteImmediately},
		{"/tasks", busyInputExecuteImmediately},
		{"/goal", busyInputExecuteImmediately},
		{"/rules", busyInputExecuteImmediately},
		{"/btw question", busyInputExecuteImmediately},
		{"/side question", busyInputExecuteImmediately},
		{"/usage", busyInputExecuteImmediately},
		{"/usage off", busyInputExecuteImmediately},
		{"/sessions", busyInputExecuteImmediately},
		{"/queue", busyInputExecuteImmediately},
		{"/queue clear", busyInputExecuteImmediately},
		{"/queue drop 2", busyInputExecuteImmediately},
		{"/queue edit 2 replacement", busyInputExecuteImmediately},
		{"/compact", busyInputReject},
		{"/compact keep test evidence", busyInputReject},
		{"/clear", busyInputReject},
		{"/new topic", busyInputReject},
		{"/resume sess-1", busyInputReject},
		{"/fork title", busyInputReject},
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
	if !isImmediatelyExecutableWhileBusy("/queue drop 2") {
		t.Fatal("/queue drop must remove a queued follow-up immediately while busy")
	}
	if !isImmediatelyExecutableWhileBusy("/queue edit 2 replacement") {
		t.Fatal("/queue edit must edit a queued follow-up immediately while busy")
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
