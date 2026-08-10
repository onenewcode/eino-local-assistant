package agent

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"

	"github.com/cloudwego/eino/schema"
)

func TestNewDirectModelRejectsNil(t *testing.T) {
	if _, err := NewDirectModel(nil); err == nil || !strings.Contains(err.Error(), "chat model is required") {
		t.Fatalf("NewDirectModel(nil) error = %v", err)
	}
}

func TestDirectModelStreamsWithoutToolBinding(t *testing.T) {
	fake := &scriptedToolModel{responses: []modelResponse{{message: schema.AssistantMessage("direct answer", nil)}}}
	direct, err := NewDirectModel(fake)
	if err != nil {
		t.Fatalf("NewDirectModel() error = %v", err)
	}
	session, err := chat.NewSession(direct, "system prompt", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	var output strings.Builder
	if err := session.Ask(context.Background(), "answer without tools", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if got, want := output.String(), "direct answer"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := fake.calls, 1; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
}
