package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

type taskGateModel struct {
	scriptedModel
	streamCalls  int
	alwaysActive bool
}

func (m *taskGateModel) Stream(ctx context.Context, messages []*schema.Message) (Stream, error) {
	m.streamCalls++
	return m.scriptedModel.Stream(ctx, messages)
}

func (m *taskGateModel) TaskExecutionStatus(context.Context) TaskRunStatus {
	return TaskRunStatus{Available: true, State: "active"}
}

func (m *taskGateModel) TaskCompletionGate(context.Context) TaskCompletionGate {
	// A controller has no active gate until a task plan was created during the
	// first model pass. The session preflight uses this distinction to identify
	// a prior user turn that may be redirected.
	if m.streamCalls == 0 {
		return TaskCompletionGate{}
	}
	if !m.alwaysActive && m.streamCalls >= 2 {
		return TaskCompletionGate{Complete: true, Summary: "complete"}
	}
	return TaskCompletionGate{
		Active:  true,
		Summary: "task implement is working",
		Gap:     "Task completion rejected. Continue the active task and call task_complete.",
	}
}

func (m *taskGateModel) InterruptTask(context.Context, string) TaskInterruptReceipt {
	return TaskInterruptReceipt{}
}

type redirectTaskModel struct {
	scriptedModel
	active     bool
	interrupts []string
}

func (m *redirectTaskModel) TaskExecutionStatus(context.Context) TaskRunStatus {
	state := "interrupted"
	if m.active {
		state = "active"
	}
	return TaskRunStatus{Available: true, State: state}
}

func (m *redirectTaskModel) TaskCompletionGate(context.Context) TaskCompletionGate {
	if !m.active {
		return TaskCompletionGate{Summary: "task run is interrupted"}
	}
	return TaskCompletionGate{Active: true, Summary: "task remains active", Gap: "continue the task"}
}

func (m *redirectTaskModel) InterruptTask(_ context.Context, reason string) TaskInterruptReceipt {
	if !m.active {
		return TaskInterruptReceipt{Summary: "no active autonomous task"}
	}
	m.active = false
	m.interrupts = append(m.interrupts, reason)
	return TaskInterruptReceipt{Applied: true, Summary: "task run interrupted"}
}

type interruptedTaskModel struct {
	scriptedModel
}

func (m *interruptedTaskModel) TaskExecutionStatus(context.Context) TaskRunStatus {
	return TaskRunStatus{Available: true, State: "interrupted"}
}

func (m *interruptedTaskModel) TaskCompletionGate(context.Context) TaskCompletionGate {
	return TaskCompletionGate{
		Active:  true,
		Summary: "task run is interrupted",
		Gap:     "call task_plan before resuming the interrupted task",
	}
}

func (m *interruptedTaskModel) InterruptTask(context.Context, string) TaskInterruptReceipt {
	return TaskInterruptReceipt{Summary: "no active autonomous task"}
}

type completionAbortModel struct {
	scriptedModel
	complete      bool
	abortedTurnID string
}

func (m *completionAbortModel) TaskExecutionStatus(context.Context) TaskRunStatus {
	return TaskRunStatus{Available: true, State: "active"}
}

func (m *completionAbortModel) TaskCompletionGate(context.Context) TaskCompletionGate {
	if m.complete {
		return TaskCompletionGate{Complete: true, Summary: "provisional completion"}
	}
	return TaskCompletionGate{}
}

func (m *completionAbortModel) InterruptTask(context.Context, string) TaskInterruptReceipt {
	return TaskInterruptReceipt{}
}

func (m *completionAbortModel) AbortTaskCompletion(ctx context.Context, _ string) TaskInterruptReceipt {
	m.abortedTurnID, _ = TaskTurnIDFromContext(ctx)
	m.complete = false
	return TaskInterruptReceipt{Applied: true, Summary: "completion revoked"}
}

type interruptibleCompletionModel struct {
	mu            sync.Mutex
	complete      bool
	started       chan struct{}
	abortedTurnID string
}

func (m *interruptibleCompletionModel) Stream(ctx context.Context, _ []*schema.Message) (Stream, error) {
	return &interruptibleCompletionStream{ctx: ctx, model: m}, nil
}

func (m *interruptibleCompletionModel) TaskExecutionStatus(context.Context) TaskRunStatus {
	return TaskRunStatus{Available: true, State: "active"}
}

func (m *interruptibleCompletionModel) TaskCompletionGate(context.Context) TaskCompletionGate {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.complete {
		return TaskCompletionGate{Complete: true, Summary: "provisional completion"}
	}
	return TaskCompletionGate{}
}

func (m *interruptibleCompletionModel) InterruptTask(context.Context, string) TaskInterruptReceipt {
	return TaskInterruptReceipt{}
}

func (m *interruptibleCompletionModel) AbortTaskCompletion(ctx context.Context, _ string) TaskInterruptReceipt {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.abortedTurnID, _ = TaskTurnIDFromContext(ctx)
	m.complete = false
	return TaskInterruptReceipt{Applied: true, Summary: "completion revoked"}
}

func (m *interruptibleCompletionModel) completionState() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.complete, m.abortedTurnID
}

type interruptibleCompletionStream struct {
	ctx   context.Context
	model *interruptibleCompletionModel
	once  sync.Once
}

func (s *interruptibleCompletionStream) Recv() (*schema.Message, error) {
	s.once.Do(func() {
		s.model.mu.Lock()
		s.model.complete = true
		s.model.mu.Unlock()
		close(s.model.started)
	})
	<-s.ctx.Done()
	return nil, s.ctx.Err()
}

func (s *interruptibleCompletionStream) Close() {}

func TestSessionTaskCompletionGateContinuesWithoutCommittingPrematureAnswer(t *testing.T) {
	model := &taskGateModel{scriptedModel: scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("premature delivery", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("verified delivery", nil)}}},
	}}}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var output strings.Builder
	var gateEvents []TaskCompletionGate
	err = session.AskWithEvents(context.Background(), "implement it", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}, func(event TurnEvent) {
		if event.Kind == TurnEventTaskGate && event.TaskGate != nil {
			gateEvents = append(gateEvents, *event.TaskGate)
		}
	})
	if err != nil {
		t.Fatalf("AskWithEvents: %v", err)
	}
	if got := output.String(); got != "verified delivery" {
		t.Fatalf("streamed output = %q", got)
	}
	if len(gateEvents) != 1 || !gateEvents[0].Active {
		t.Fatalf("completion gate events = %#v", gateEvents)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model calls = %d, want 2", len(model.requests))
	}
	if last := model.requests[1][len(model.requests[1])-1]; last == nil || last.Role != schema.User || !strings.Contains(last.Content, "completion rejected") {
		t.Fatalf("second request should end with GapPacket: %#v", model.requests[1])
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system"},
		{role: schema.User, content: "implement it"},
		{role: schema.Assistant, content: "verified delivery"},
	})
}

func TestSessionTaskCompletionGateFailsRatherThanCommittingUnresolvedRun(t *testing.T) {
	model := &taskGateModel{scriptedModel: scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("try one", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("try two", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("try three", nil)}}},
	}}}
	// Keep the gate active for every continuation attempt.
	model.alwaysActive = true
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(context.Background(), "implement it", nil); !errors.Is(err, ErrTaskCompletionUnresolved) {
		t.Fatalf("Ask error = %v, want unresolved completion", err)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{{role: schema.System, content: "system"}})
}

func TestSessionNewNaturalLanguageInputInterruptsPriorActiveTask(t *testing.T) {
	model := &redirectTaskModel{
		active: true,
		scriptedModel: scriptedModel{streams: []Stream{
			&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("new direction accepted", nil)}}},
		}},
	}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(context.Background(), "instead, change the scope", nil); err != nil {
		t.Fatalf("Ask = %v", err)
	}
	if len(model.interrupts) != 1 || model.interrupts[0] != "superseded by a new user message" {
		t.Fatalf("task interruptions = %#v", model.interrupts)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system"},
		{role: schema.User, content: "instead, change the scope"},
		{role: schema.Assistant, content: "new direction accepted"},
	})
}

func TestSessionAcceptsFollowUpForAlreadyInterruptedTask(t *testing.T) {
	model := &interruptedTaskModel{scriptedModel: scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("first unplanned response", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("second unplanned response", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("third unplanned response", nil)}}},
	}}}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := session.Ask(context.Background(), "continue", nil); !errors.Is(err, ErrTaskCompletionUnresolved) {
		t.Fatalf("Ask error = %v, want unresolved completion", err)
	}
	if len(model.requests) != 3 {
		t.Fatalf("model calls = %d, want 3 so the controller can request a fresh plan", len(model.requests))
	}
}

func TestSessionRevokesProvisionalTaskCompletionWhenTurnFails(t *testing.T) {
	model := &completionAbortModel{scriptedModel: scriptedModel{
		beforeStream: func() {},
		streams:      []Stream{&scriptedStream{events: []streamEvent{{err: errors.New("stream disconnected")}}}},
	}}
	model.beforeStream = func() { model.complete = true }
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(context.Background(), "implement it", nil); err == nil {
		t.Fatal("Ask must fail after the simulated stream error")
	}
	if model.abortedTurnID == "" || model.complete {
		t.Fatalf("provisional completion was not revoked: turn=%q complete=%v", model.abortedTurnID, model.complete)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{{role: schema.System, content: "system"}})
}

func TestSessionInterruptRevokesCompletionBeforeCancelledTurnReturns(t *testing.T) {
	model := &interruptibleCompletionModel{started: make(chan struct{})}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Ask(ctx, "implement it", nil) }()
	<-model.started

	if receipt := session.InterruptTask(context.Background(), "user interrupted"); !receipt.Applied {
		t.Fatalf("InterruptTask = %#v", receipt)
	}
	complete, turnID := model.completionState()
	if complete || turnID == "" {
		t.Fatalf("interrupt did not revoke provisional completion: complete=%v turn=%q", complete, turnID)
	}
	cancel()
	if err := <-done; err == nil {
		t.Fatal("cancelled Ask must return an error")
	}
}

func TestThreadTurnRecorderPersistsTaskStateWithoutBreakingTurnLifecycle(t *testing.T) {
	threadStore := newDurableThreadStore(t)
	state, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: "task-recorder"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(context.Background(), state.ID, state.Revision, store.TurnStart{TurnID: "turn-1", Input: "implement"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := newThreadTurnRecorder(threadStore, state.ID, state.Revision, "turn-1")
	snapshot := []byte(`{"version":1,"state":"active"}`)
	if err := recorder.recordTaskState(context.Background(), snapshot); err != nil {
		t.Fatalf("recordTaskState: %v", err)
	}
	if _, err := recorder.commit(store.TurnCommit{TurnID: "turn-1", Messages: []*schema.Message{
		schema.UserMessage("implement"), schema.AssistantMessage("working", nil),
	}}); err != nil {
		t.Fatalf("commit after task state: %v", err)
	}
	restored, err := threadStore.LoadTaskState(context.Background(), state.ID)
	if err != nil {
		t.Fatalf("LoadTaskState: %v", err)
	}
	if got, want := string(restored), string(snapshot); got != want {
		t.Fatalf("task snapshot = %s, want %s", got, want)
	}
}
