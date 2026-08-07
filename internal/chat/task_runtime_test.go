package chat

import (
	"context"
	"testing"

	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

type planStatusModel struct {
	scriptedModel
	status     TaskRunStatus
	interrupts []string
}

func (m *planStatusModel) TaskExecutionStatus(context.Context) TaskRunStatus {
	return m.status
}

func (m *planStatusModel) InterruptTask(_ context.Context, reason string) TaskInterruptReceipt {
	m.interrupts = append(m.interrupts, reason)
	if !m.status.Available || m.status.State != "active" {
		return TaskInterruptReceipt{Summary: "no active plan"}
	}
	m.status.State = "interrupted"
	return TaskInterruptReceipt{Applied: true, Summary: "plan interrupted"}
}

func TestSessionNewNaturalLanguageInputInterruptsPriorActivePlan(t *testing.T) {
	model := &planStatusModel{
		status: TaskRunStatus{Available: true, State: "active", Tasks: 1},
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
		t.Fatalf("plan interruptions = %#v", model.interrupts)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system"},
		{role: schema.User, content: "instead, change the scope"},
		{role: schema.Assistant, content: "new direction accepted"},
	})
}

func TestSessionDoesNotInterruptWhenPlanAlreadyInterrupted(t *testing.T) {
	model := &planStatusModel{
		status: TaskRunStatus{Available: true, State: "interrupted", Tasks: 1},
		scriptedModel: scriptedModel{streams: []Stream{
			&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("continue", nil)}}},
		}},
	}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(context.Background(), "continue", nil); err != nil {
		t.Fatalf("Ask = %v", err)
	}
	if len(model.interrupts) != 0 {
		t.Fatalf("already interrupted plan should not re-interrupt: %#v", model.interrupts)
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
	snapshot := []byte(`{"version":3,"state":"active","items":[{"step":"inspect","status":"pending"}]}`)
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
