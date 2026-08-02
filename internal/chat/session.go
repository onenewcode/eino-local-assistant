package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/schema"
)

var ErrEmptyInput = errors.New("message cannot be empty")

// Stream is the portion of an Eino message stream needed by a conversation.
type Stream interface {
	Recv() (*schema.Message, error)
	Close()
}

// Model is a chat model that returns a one-pass stream of assistant messages.
type Model interface {
	Stream(context.Context, []*schema.Message) (Stream, error)
}

// Session owns the in-memory history for one CLI process.
type Session struct {
	model   Model
	history []*schema.Message
}

// NewSession starts a conversation with the configured system prompt.
func NewSession(model Model, systemPrompt string) *Session {
	return &Session{
		model:   model,
		history: []*schema.Message{schema.SystemMessage(systemPrompt)},
	}
}

// Ask streams one reply. A turn is committed only after the complete reply arrives.
func (s *Session) Ask(ctx context.Context, input string, onChunk func(string) error) error {
	if strings.TrimSpace(input) == "" {
		return ErrEmptyInput
	}

	pending := append(append([]*schema.Message(nil), s.history...), schema.UserMessage(input))
	stream, err := s.model.Stream(ctx, pending)
	if err != nil {
		return fmt.Errorf("start response stream: %w", err)
	}
	defer stream.Close()

	chunks := make([]*schema.Message, 0)
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read response stream: %w", err)
		}
		if chunk == nil {
			return errors.New("read response stream: received an empty message chunk")
		}

		chunks = append(chunks, chunk)
		if onChunk != nil && chunk.Content != "" {
			if err := onChunk(chunk.Content); err != nil {
				return fmt.Errorf("write response chunk: %w", err)
			}
		}
	}

	answer, err := completeMessage(chunks)
	if err != nil {
		return fmt.Errorf("combine response stream: %w", err)
	}

	s.history = append(pending, answer)
	return nil
}

func completeMessage(chunks []*schema.Message) (*schema.Message, error) {
	if len(chunks) == 0 {
		return schema.AssistantMessage("", nil), nil
	}

	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, err
	}
	if message.Role == "" {
		message.Role = schema.Assistant
	}
	return message, nil
}

// History returns a snapshot for diagnostics and tests. Messages remain process-local.
func (s *Session) History() []*schema.Message {
	return append([]*schema.Message(nil), s.history...)
}
