package chat

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

func TestTurnUsageTrackerAssignsDistinctIDsForMissingCallIDs(t *testing.T) {
	tracker := &turnUsageTracker{}
	event := ModelUsageEvent{
		Available: true,
		Usage: usage.Turn{
			PromptTokens:     10,
			CompletionTokens: 2,
			TotalTokens:      12,
		},
	}
	first := tracker.normalize(event)
	second := tracker.normalize(event)
	if first.CallID == "" || second.CallID == "" || first.CallID == second.CallID {
		t.Fatalf("missing call IDs were not assigned uniquely: first=%q second=%q", first.CallID, second.CallID)
	}

	if !tracker.hasEvents() {
		t.Fatal("usage tracker did not retain the first event")
	}
}

func TestSessionAskRecordsEventAwareModelUsageWithoutExternalEmitter(t *testing.T) {
	model := &usageEventModel{
		stream: &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("done", nil)}}},
		events: []ModelUsageEvent{
			{CallID: "model-1", Operation: ModelUsageOperationAgent, Available: true, Usage: usage.Turn{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}},
			{CallID: "model-2", Operation: ModelUsageOperationAgent, Available: true, Usage: usage.Turn{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25}},
		},
	}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(context.Background(), "question", nil); err != nil {
		t.Fatal(err)
	}

	summary := session.UsageSummary()
	if summary.PromptTokens != 30 || summary.CompletionTokens != 6 || summary.TotalTokens != 36 ||
		summary.ModelCallCount != 2 || summary.Status != store.UsageStatusExact {
		t.Fatalf("usage summary = %+v", summary)
	}
}

func TestSessionAskAssignsDistinctRecordsToMissingUsageCallIDs(t *testing.T) {
	model := &usageEventModel{
		stream: &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("done", nil)}}},
		events: []ModelUsageEvent{
			{Available: true, Usage: usage.Turn{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}},
			{Available: true, Usage: usage.Turn{PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22}},
		},
	}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(context.Background(), "question", nil); err != nil {
		t.Fatal(err)
	}

	summary := session.UsageSummary()
	if summary.PromptTokens != 30 || summary.CompletionTokens != 3 || summary.TotalTokens != 33 || summary.ModelCallCount != 2 {
		t.Fatalf("usage summary = %+v, want two distinct missing-ID calls", summary)
	}
}

func TestSessionPublishesNewerContextEstimateAfterToolResult(t *testing.T) {
	model := &inFlightToolEventModel{
		stream: &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("done", nil)}}},
	}
	session, err := NewSession(model, "system", SessionOptions{
		Store:   newDurableThreadStore(t),
		Context: contextbuild.Config{WindowTokens: 20_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	var afterCall, afterResult ContextStatus
	if err := session.AskWithEvents(context.Background(), "inspect output", nil, func(event TurnEvent) {
		switch event.Kind {
		case TurnEventToolStart:
			afterCall = session.ContextStatus()
		case TurnEventToolEnd:
			afterResult = session.ContextStatus()
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !afterCall.MeasuredKnown || afterCall.MeasuredTokens != 120 {
		t.Fatalf("tool-call boundary did not publish prior provider usage: %+v", afterCall)
	}
	if !afterResult.CurrentEstimateIsNewer || afterResult.CurrentTokens <= afterCall.CurrentTokens {
		t.Fatalf("tool result did not advance the in-flight context estimate: before=%+v after=%+v", afterCall, afterResult)
	}
	if final := session.ContextStatus(); final.CurrentEstimateIsNewer {
		t.Fatalf("committed turn retained a stale in-flight estimate: %+v", final)
	}
}

func TestSessionKeepsMeasuredContextDuringInitialRequest(t *testing.T) {
	model := &initialRequestGateModel{
		waiting: make(chan struct{}),
		release: make(chan struct{}),
	}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(context.Background(), "first", nil); err != nil {
		t.Fatalf("initial Ask: %v", err)
	}
	if status := session.ContextStatus(); !status.MeasuredKnown || status.MeasuredTokens != 120 {
		t.Fatalf("initial provider snapshot = %+v", status)
	}

	done := make(chan error, 1)
	go func() {
		done <- session.Ask(context.Background(), "second", nil)
	}()
	select {
	case <-model.waiting:
	case <-time.After(time.Second):
		t.Fatal("second model request did not reach the gate")
	}

	status := session.ContextStatus()
	if !status.MeasuredKnown || status.MeasuredTokens != 120 || status.CurrentEstimateIsNewer {
		t.Fatalf("initial request replaced the measured context snapshot: %+v", status)
	}
	close(model.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second Ask: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Ask did not finish after releasing the gate")
	}
}

func TestSessionAskRejectsConflictingUsageCallID(t *testing.T) {
	model := &usageEventModel{
		stream: &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("done", nil)}}},
		events: []ModelUsageEvent{
			{CallID: "same-call", Available: true, Usage: usage.Turn{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}},
			{CallID: "same-call", Available: true, Usage: usage.Turn{PromptTokens: 20, CompletionTokens: 2, TotalTokens: 22}},
		},
	}
	threadStore := newDurableThreadStore(t)
	session, err := NewSession(model, "system", SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatal(err)
	}
	err = session.Ask(context.Background(), "question", nil)
	if !errors.Is(err, store.ErrUsageRecordConflict) {
		t.Fatalf("Ask error = %v, want usage record conflict", err)
	}
	state, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.ModelCallCount != 1 || state.Meta.PromptTokens != 10 {
		t.Fatalf("conflicting callback changed recorded usage: %#v", state.Meta)
	}
}

func TestSessionWaitsForDelayedModelUsageEvents(t *testing.T) {
	model := &delayedUsageEventModel{
		streamEnded: make(chan struct{}),
		waiting:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- session.Ask(context.Background(), "question", nil)
	}()
	select {
	case <-model.waiting:
	case <-time.After(time.Second):
		t.Fatal("model stream did not reach its delayed usage callback")
	}
	select {
	case err := <-done:
		t.Fatalf("Ask returned before delayed usage event: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(model.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Ask error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Ask did not finish after delayed usage event")
	}
	if summary := session.UsageSummary(); summary.Status != store.UsageStatusExact || summary.ModelCallCount != 1 || summary.TotalTokens != 12 {
		t.Fatalf("delayed usage summary = %+v", summary)
	}
}

type usageEventModel struct {
	stream Stream
	events []ModelUsageEvent
}

type inFlightToolEventModel struct {
	stream Stream
}

type initialRequestGateModel struct {
	mu      sync.Mutex
	calls   int
	waiting chan struct{}
	release chan struct{}
}

func (m *initialRequestGateModel) Stream(_ context.Context, _ []*schema.Message) (Stream, error) {
	return &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("done", nil)}}}, nil
}

func (m *initialRequestGateModel) StreamWithEvents(ctx context.Context, _ []*schema.Message, emit EventEmitter) (Stream, <-chan struct{}, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 2 {
		close(m.waiting)
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	emit(TurnEvent{Kind: TurnEventModelUsage, ModelUsage: &ModelUsageEvent{
		CallID:    "request-usage",
		Available: true,
		Usage:     usage.Turn{PromptTokens: 120, CompletionTokens: 8, TotalTokens: 128},
	}})
	done := make(chan struct{})
	close(done)
	return &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("done", nil)}}}, done, nil
}

func (m *inFlightToolEventModel) Stream(_ context.Context, _ []*schema.Message) (Stream, error) {
	return m.stream, nil
}

func (m *inFlightToolEventModel) StreamWithEvents(_ context.Context, _ []*schema.Message, emit EventEmitter) (Stream, <-chan struct{}, error) {
	emit(TurnEvent{Kind: TurnEventModelUsage, ModelUsage: &ModelUsageEvent{
		CallID:    "first-request",
		Operation: ModelUsageOperationAgent,
		Available: true,
		Usage:     usage.Turn{PromptTokens: 120, CompletionTokens: 8, TotalTokens: 128},
	}})
	emit(TurnEvent{Kind: TurnEventToolStart, Tool: "read_artifact", ToolCallID: "artifact-call", Input: `{"artifact_id":"sha256-test"}`})
	emit(TurnEvent{Kind: TurnEventToolEnd, Tool: "read_artifact", ToolCallID: "artifact-call", Output: strings.Repeat("evidence ", 2_000)})
	done := make(chan struct{})
	close(done)
	return m.stream, done, nil
}

func (m *usageEventModel) Stream(_ context.Context, _ []*schema.Message) (Stream, error) {
	return m.stream, nil
}

func (m *usageEventModel) StreamWithEvents(_ context.Context, _ []*schema.Message, emit EventEmitter) (Stream, <-chan struct{}, error) {
	for _, event := range m.events {
		emit(TurnEvent{Kind: TurnEventModelUsage, ModelUsage: &event})
	}
	done := make(chan struct{})
	close(done)
	return m.stream, done, nil
}

type delayedUsageEventModel struct {
	streamEnded chan struct{}
	waiting     chan struct{}
	release     chan struct{}
}

func (m *delayedUsageEventModel) Stream(_ context.Context, _ []*schema.Message) (Stream, error) {
	return &delayedUsageStream{ended: m.streamEnded}, nil
}

func (m *delayedUsageEventModel) StreamWithEvents(_ context.Context, _ []*schema.Message, emit EventEmitter) (Stream, <-chan struct{}, error) {
	done := make(chan struct{})
	go func() {
		<-m.streamEnded
		close(m.waiting)
		<-m.release
		emit(TurnEvent{Kind: TurnEventModelUsage, ModelUsage: &ModelUsageEvent{
			CallID:    "late-call",
			Available: true,
			Usage:     usage.Turn{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		}})
		close(done)
	}()
	return &delayedUsageStream{ended: m.streamEnded}, done, nil
}

type delayedUsageStream struct {
	ended chan struct{}
	sent  bool
}

func (s *delayedUsageStream) Recv() (*schema.Message, error) {
	if !s.sent {
		s.sent = true
		return schema.AssistantMessage("done", nil), nil
	}
	close(s.ended)
	return nil, io.EOF
}

func (*delayedUsageStream) Close() {}
