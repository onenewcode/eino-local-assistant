package chat

import (
	"context"
	"errors"
	"fmt"
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

func TestNewSessionRequiresThreadStore(t *testing.T) {
	_, err := NewSession(&scriptedModel{}, "system instructions", SessionOptions{})
	if err == nil || !strings.Contains(err.Error(), "thread store is required") {
		t.Fatalf("NewSession error = %v, want required thread store", err)
	}
}

func TestSessionAskAggregatesStreamAndCommitsCompleteTurn(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("Hello, ", nil)},
		{message: schema.AssistantMessage("world!", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var output strings.Builder
	err = session.Ask(context.Background(), "say hello", func(chunk string) error {
		_, err := output.WriteString(chunk)
		return err
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}

	if got, want := output.String(), "Hello, world!"; got != want {
		t.Errorf("streamed output = %q, want %q", got, want)
	}
	if !stream.closed {
		t.Error("Ask() did not close the response stream")
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "say hello"},
		{role: schema.Assistant, content: "Hello, world!"},
	})
	assertMessages(t, model.requests[0], []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "say hello"},
	})
}

func TestSessionAskSendsPriorCompleteTurnsAsContext(t *testing.T) {
	firstStream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("first answer", nil)},
	}}
	secondStream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("second answer", nil)},
	}}
	model := &scriptedModel{streams: []Stream{firstStream, secondStream}}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := session.Ask(context.Background(), "first question", nil); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if err := session.Ask(context.Background(), "second question", nil); err != nil {
		t.Fatalf("second Ask() error = %v", err)
	}

	if got, want := len(model.requests), 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	assertMessages(t, model.requests[1], []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "first question"},
		{role: schema.Assistant, content: "first answer"},
		{role: schema.User, content: "second question"},
	})
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "first question"},
		{role: schema.Assistant, content: "first answer"},
		{role: schema.User, content: "second question"},
		{role: schema.Assistant, content: "second answer"},
	})
}

func TestSessionAskRollsBackOnStreamFailure(t *testing.T) {
	wantErr := errors.New("connection dropped")
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial reply", nil)},
		{err: wantErr},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var output strings.Builder
	err = session.Ask(context.Background(), "question", func(chunk string) error {
		_, err := output.WriteString(chunk)
		return err
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask() error = %v, want wrapped %v", err, wantErr)
	}
	if got, want := output.String(), "partial reply"; got != want {
		t.Errorf("streamed output = %q, want %q", got, want)
	}
	if !stream.closed {
		t.Error("Ask() did not close the failed response stream")
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionAskRollsBackOnChunkCallbackFailure(t *testing.T) {
	wantErr := errors.New("terminal write failed")
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial reply", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "question", func(string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask() error = %v, want wrapped %v", err, wantErr)
	}
	if !stream.closed {
		t.Error("Ask() did not close the response stream after callback failure")
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionAskRejectsEmptyInputWithoutCallingModel(t *testing.T) {
	model := &scriptedModel{}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), " \t\n ", nil)
	if !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("Ask() error = %v, want %v", err, ErrEmptyInput)
	}
	if got := len(model.requests); got != 0 {
		t.Errorf("model calls = %d, want 0", got)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionAskRollsBackWhenStreamCannotStart(t *testing.T) {
	wantErr := errors.New("model unavailable")
	model := &scriptedModel{err: wantErr}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "question", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask() error = %v, want wrapped %v", err, wantErr)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionCancellationClosesBlockedStream(t *testing.T) {
	stream := &blockingStream{closed: make(chan struct{})}
	started := make(chan struct{})
	model := &scriptedModel{
		streams: []Stream{stream},
		beforeStream: func() {
			close(started)
		},
	}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- session.Ask(ctx, "question", nil)
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ask error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled blocked stream did not return")
	}
}

func TestSessionCancellationAtEOFDoesNotCommitPartialTurn(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial reply", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	st := newDurableThreadStore(t)
	session, err := NewSession(model, "system instructions", SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = session.Ask(ctx, "question", func(string) error {
		// A provider can acknowledge Stream.Close by returning EOF. The turn must
		// still be terminally cancelled rather than committed as a partial answer.
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ask error = %v, want context.Canceled", err)
	}
	state, err := st.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != 1 {
		t.Fatalf("cancelled EOF turn committed %d messages", state.Meta.MessageCount)
	}
	groups, err := st.LoadTurnGroups(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Cancelled == nil || groups[0].Committed != nil {
		t.Fatalf("cancelled EOF lifecycle = %#v", groups)
	}
}

func TestSessionPersistsSuccessfulTurnBeforeMemoryCommit(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("ok", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	st := newDurableThreadStore(t)
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store: st,
		ID:    "sess-1",
		Title: "t",
		Now:   func() time.Time { return time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := session.Ask(context.Background(), "hi", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	state, err := st.LoadThread(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != 3 {
		t.Fatalf("message count = %d, want 3", state.Meta.MessageCount)
	}
	groups, err := st.LoadTurnGroups(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Started == nil || groups[0].Committed == nil {
		t.Fatalf("completed lifecycle = %#v", groups)
	}
	assertMessages(t, groups[0].Committed.Messages, []messageExpectation{
		{role: schema.User, content: "hi"},
		{role: schema.Assistant, content: "ok"},
	})
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "hi"},
		{role: schema.Assistant, content: "ok"},
	})
}

func TestSessionDoesNotPersistFailedTurn(t *testing.T) {
	wantErr := errors.New("boom")
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial", nil)},
		{err: wantErr},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	st := newDurableThreadStore(t)
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store: st,
		ID:    "sess-fail",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "q", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask error = %v", err)
	}
	state, err := st.LoadThread(context.Background(), "sess-fail")
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != 1 {
		t.Fatalf("message count = %d, want only the system message", state.Meta.MessageCount)
	}
	groups, err := st.LoadTurnGroups(context.Background(), "sess-fail")
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Committed != nil || groups[0].Failed == nil {
		t.Fatalf("failed lifecycle = %#v", groups)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionDoesNotCommitMemoryWhenThreadRevisionChanges(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("ok", nil)},
	}}
	st := newDurableThreadStore(t)
	var concurrentWriteErr error
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store: st,
		ID:    "sess-disk",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	model.beforeStream = func() {
		state, loadErr := st.LoadThread(context.Background(), session.ID())
		if loadErr != nil {
			concurrentWriteErr = loadErr
			return
		}
		_, concurrentWriteErr = st.SetThreadTitle(context.Background(), session.ID(), state.Revision, "external writer")
	}

	err = session.Ask(context.Background(), "hi", nil)
	if concurrentWriteErr != nil {
		t.Fatalf("concurrent thread write: %v", concurrentWriteErr)
	}
	if !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("Ask error = %v, want revision conflict", err)
	}
	groups, loadErr := st.LoadTurnGroups(context.Background(), session.ID())
	if loadErr != nil {
		t.Fatalf("LoadTurnGroups: %v", loadErr)
	}
	if len(groups) != 1 || groups[0].Failed == nil || groups[0].Committed != nil {
		t.Fatalf("lost-CAS turn was left active: %#v", groups)
	}
	resumedModel := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("recovered", nil)}}},
	}}
	resumed, openErr := OpenSession(resumedModel, st, session.ID(), SessionOptions{Store: st})
	if openErr != nil {
		t.Fatalf("OpenSession after CAS conflict: %v", openErr)
	}
	if askErr := resumed.Ask(context.Background(), "retry", nil); askErr != nil {
		t.Fatalf("Ask after CAS conflict: %v", askErr)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionRecordsUsageFromResponseMeta(t *testing.T) {
	answer := schema.AssistantMessage("ok", nil)
	answer.ResponseMeta = &schema.ResponseMeta{
		Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	stream := &scriptedStream{events: []streamEvent{{message: answer}}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store:   newDurableThreadStore(t),
		Pricing: usage.Pricing{InputPerMillion: 1, OutputPerMillion: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	turn := session.LastTurnUsage()
	if turn.PromptTokens != 10 || turn.CompletionTokens != 5 || turn.Estimated {
		t.Fatalf("turn=%+v", turn)
	}
	if turn.CostUSD <= 0 {
		t.Fatalf("cost=%v", turn.CostUSD)
	}
	p, c, total, cost, est := session.UsageTotals()
	if p != 10 || c != 5 || total != 15 || est || cost <= 0 {
		t.Fatalf("totals p=%d c=%d t=%d cost=%v est=%v", p, c, total, cost, est)
	}
}

func TestSessionSendsBudgetedViewNotFullTranscript(t *testing.T) {
	ctx := context.Background()
	st := newDurableThreadStore(t)
	seedModel := &scriptedModel{streams: make([]Stream, 0, 8)}
	for range 8 {
		seedModel.streams = append(seedModel.streams, &scriptedStream{events: []streamEvent{
			{message: schema.AssistantMessage(strings.Repeat("a", 160), nil)},
		}})
	}
	cfg := contextbuild.Config{
		ModelContextTokens:        500,
		OutputReserveTokens:       100,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
		KeepRecentTurns:           2,
	}
	seed, err := NewSession(seedModel, "system instructions", SessionOptions{
		Store:   st,
		Context: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		input := fmt.Sprintf("old-%02d %s", i, strings.Repeat("u", 160))
		if err := seed.Ask(ctx, input, nil); err != nil {
			t.Fatalf("seed Ask(%d): %v", i, err)
		}
	}

	// Reopening forces the next request to be assembled from durable turn groups,
	// rather than from a mutable flat message slice.
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("ok", nil)}}},
	}}
	session, err := OpenSession(model, st, seed.ID(), SessionOptions{
		Store: st,
		Context: contextbuild.Config{
			ModelContextTokens:        500,
			OutputReserveTokens:       100,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			KeepRecentTurns:           2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := session.Ask(ctx, "latest question", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(model.requests) != 1 {
		t.Fatalf("model calls=%d", len(model.requests))
	}
	// The durable planner omits whole old turn groups, never a hand-mutated
	// message prefix, before sending the request to the model.
	if len(model.requests[0]) >= 18 {
		t.Fatalf("model received all durable messages (%d); want bounded view", len(model.requests[0]))
	}
	if model.requests[0][0].Role != schema.System {
		t.Fatalf("first role=%s", model.requests[0][0].Role)
	}
	status := session.ContextStatus()
	if status.OmittedTurnGroups == 0 || status.CurrentTokens > status.BudgetTokens {
		t.Fatalf("bounded prompt status = %+v", status)
	}
}

func TestOpenSessionLoadsTranscript(t *testing.T) {
	st := newDurableThreadStore(t)
	ctx := context.Background()
	seedModel := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("a1", nil)}}},
	}}
	seed, err := NewSession(seedModel, "sys", SessionOptions{
		Store: st,
		ID:    "open-1",
		Title: "loaded",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := seed.Ask(ctx, "u1", nil); err != nil {
		t.Fatalf("seed Ask: %v", err)
	}

	model := &scriptedModel{}
	session, err := OpenSession(model, st, "open-1", SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if session.ID() != "open-1" || session.Title() != "loaded" {
		t.Errorf("id/title = %q / %q", session.ID(), session.Title())
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "sys"},
		{role: schema.User, content: "u1"},
		{role: schema.Assistant, content: "a1"},
	})
}

type scriptedModel struct {
	streams      []Stream
	err          error
	beforeStream func()
	requests     [][]*schema.Message
}

func (m *scriptedModel) Stream(_ context.Context, messages []*schema.Message) (Stream, error) {
	m.requests = append(m.requests, append([]*schema.Message(nil), messages...))
	if m.beforeStream != nil {
		m.beforeStream()
	}
	if m.err != nil {
		return nil, m.err
	}
	if len(m.streams) == 0 {
		return nil, errors.New("unexpected model call")
	}

	stream := m.streams[0]
	m.streams = m.streams[1:]
	return stream, nil
}

type streamEvent struct {
	message *schema.Message
	err     error
}

type scriptedStream struct {
	events []streamEvent
	next   int
	closed bool
}

func (s *scriptedStream) Recv() (*schema.Message, error) {
	if s.next >= len(s.events) {
		return nil, io.EOF
	}

	event := s.events[s.next]
	s.next++
	return event.message, event.err
}

func (s *scriptedStream) Close() {
	s.closed = true
}

type blockingStream struct {
	closed chan struct{}
	once   sync.Once
}

func (s *blockingStream) Recv() (*schema.Message, error) {
	<-s.closed
	return nil, context.Canceled
}

func (s *blockingStream) Close() {
	s.once.Do(func() { close(s.closed) })
}

type messageExpectation struct {
	role    schema.RoleType
	content string
}

func assertMessages(t *testing.T, got []*schema.Message, want []messageExpectation) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("messages = %d, want %d: %#v", len(got), len(want), got)
	}
	for i, expected := range want {
		if got[i] == nil {
			t.Errorf("message %d = nil", i)
			continue
		}
		if got[i].Role != expected.role {
			t.Errorf("message %d role = %q, want %q", i, got[i].Role, expected.role)
		}
		if got[i].Content != expected.content {
			t.Errorf("message %d content = %q, want %q", i, got[i].Content, expected.content)
		}
	}
}

func newDurableThreadStore(t *testing.T) *store.ThreadStore {
	t.Helper()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	return st
}
