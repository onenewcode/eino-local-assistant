package chat

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestSessionAskAggregatesStreamAndCommitsCompleteTurn(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("Hello, ", nil)},
		{message: schema.AssistantMessage("world!", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session := NewSession(model, "system instructions")

	var output strings.Builder
	err := session.Ask(context.Background(), "say hello", func(chunk string) error {
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
	assertMessages(t, session.History(), []messageExpectation{
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
	session := NewSession(model, "system instructions")

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
	assertMessages(t, session.History(), []messageExpectation{
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
	session := NewSession(model, "system instructions")

	var output strings.Builder
	err := session.Ask(context.Background(), "question", func(chunk string) error {
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
	assertMessages(t, session.History(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionAskRollsBackOnChunkCallbackFailure(t *testing.T) {
	wantErr := errors.New("terminal write failed")
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial reply", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session := NewSession(model, "system instructions")

	err := session.Ask(context.Background(), "question", func(string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask() error = %v, want wrapped %v", err, wantErr)
	}
	if !stream.closed {
		t.Error("Ask() did not close the response stream after callback failure")
	}
	assertMessages(t, session.History(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionAskRejectsEmptyInputWithoutCallingModel(t *testing.T) {
	model := &scriptedModel{}
	session := NewSession(model, "system instructions")

	err := session.Ask(context.Background(), " \t\n ", nil)
	if !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("Ask() error = %v, want %v", err, ErrEmptyInput)
	}
	if got := len(model.requests); got != 0 {
		t.Errorf("model calls = %d, want 0", got)
	}
	assertMessages(t, session.History(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionAskRollsBackWhenStreamCannotStart(t *testing.T) {
	wantErr := errors.New("model unavailable")
	model := &scriptedModel{err: wantErr}
	session := NewSession(model, "system instructions")

	err := session.Ask(context.Background(), "question", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask() error = %v, want wrapped %v", err, wantErr)
	}
	assertMessages(t, session.History(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

type scriptedModel struct {
	streams  []Stream
	err      error
	requests [][]*schema.Message
}

func (m *scriptedModel) Stream(_ context.Context, messages []*schema.Message) (Stream, error) {
	m.requests = append(m.requests, append([]*schema.Message(nil), messages...))
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
