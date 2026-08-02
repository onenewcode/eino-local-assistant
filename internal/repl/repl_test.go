package repl

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"

	"github.com/cloudwego/eino/schema"
)

func TestRunSkipsBlankInputStreamsReplyAndExits(t *testing.T) {
	model := &replModel{streams: []chat.Stream{&replStream{events: []replEvent{
		{message: schema.AssistantMessage("Hello", nil)},
		{message: schema.AssistantMessage(" there", nil)},
	}}}}
	session := chat.NewSession(model, "system prompt")

	var output, errorsOut strings.Builder
	runner := Runner{
		Input:       strings.NewReader(" \nquestion\n/exit\n"),
		Output:      &output,
		ErrorOutput: &errorsOut,
		Session:     session,
	}

	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := len(model.requests), 1; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	if got, want := model.requests[0][1].Content, "question"; got != want {
		t.Errorf("model input = %q, want %q", got, want)
	}
	if !strings.Contains(output.String(), "assistant> Hello there\n") {
		t.Errorf("output = %q, want streamed reply", output.String())
	}
	if !strings.Contains(output.String(), "Goodbye.\n") {
		t.Errorf("output = %q, want exit acknowledgement", output.String())
	}
	if got := errorsOut.String(); got != "" {
		t.Errorf("stderr = %q, want empty", got)
	}
}

func TestRunExitsCleanlyAtEOF(t *testing.T) {
	model := &replModel{streams: []chat.Stream{&replStream{events: []replEvent{
		{message: schema.AssistantMessage("answer", nil)},
	}}}}
	session := chat.NewSession(model, "system prompt")

	var output strings.Builder
	runner := Runner{
		Input:   strings.NewReader("question\n"),
		Output:  &output,
		Session: session,
	}

	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(output.String(), "assistant> answer\n") {
		t.Errorf("output = %q, want reply before EOF", output.String())
	}
	if got := strings.Count(output.String(), "you> "); got != 2 {
		t.Errorf("input prompts = %d, want 2", got)
	}
}

func TestRunReportsStreamFailureAndKeepsSessionUsable(t *testing.T) {
	streamFailure := errors.New("connection dropped")
	failedStream := &replStream{events: []replEvent{
		{message: schema.AssistantMessage("partial", nil)},
		{err: streamFailure},
	}}
	model := &replModel{streams: []chat.Stream{failedStream}}
	session := chat.NewSession(model, "system prompt")

	var output, errorsOut strings.Builder
	runner := Runner{
		Input:       strings.NewReader("question\n/exit\n"),
		Output:      &output,
		ErrorOutput: &errorsOut,
		Session:     session,
	}

	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !failedStream.closed {
		t.Error("failed stream was not closed")
	}
	if !strings.Contains(errorsOut.String(), "assistant error: read response stream: connection dropped") {
		t.Errorf("stderr = %q, want stream failure", errorsOut.String())
	}
	if got, want := len(session.History()), 1; got != want {
		t.Errorf("history messages = %d, want %d after rollback", got, want)
	}
}

func TestRunReturnsWithoutReadingWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	model := &replModel{}
	var output strings.Builder
	runner := Runner{
		Input:   strings.NewReader("question\n"),
		Output:  &output,
		Session: chat.NewSession(model, "system prompt"),
	}

	if err := runner.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := output.String(); got != "" {
		t.Errorf("output = %q, want empty", got)
	}
	if got := len(model.requests); got != 0 {
		t.Errorf("model calls = %d, want 0", got)
	}
}

type replModel struct {
	streams  []chat.Stream
	requests [][]*schema.Message
}

func (m *replModel) Stream(_ context.Context, messages []*schema.Message) (chat.Stream, error) {
	m.requests = append(m.requests, append([]*schema.Message(nil), messages...))
	if len(m.streams) == 0 {
		return nil, errors.New("unexpected model call")
	}

	stream := m.streams[0]
	m.streams = m.streams[1:]
	return stream, nil
}

type replEvent struct {
	message *schema.Message
	err     error
}

type replStream struct {
	events []replEvent
	next   int
	closed bool
}

func (s *replStream) Recv() (*schema.Message, error) {
	if s.next >= len(s.events) {
		return nil, io.EOF
	}

	event := s.events[s.next]
	s.next++
	return event.message, event.err
}

func (s *replStream) Close() {
	s.closed = true
}
