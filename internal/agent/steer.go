package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"eino-local-assistant/internal/chat"

	"github.com/cloudwego/eino/schema"
)

// turnSteerRegistry binds one chat mailbox to each durable turn while that
// turn is executing. The registry is process-local; the durable identity and
// lifecycle remain owned by chat.Session and its ThreadStore.
type turnSteerRegistry struct {
	mu        sync.RWMutex
	mailboxes map[string]chat.TurnSteerMailbox
}

func newTurnSteerRegistry() *turnSteerRegistry {
	return &turnSteerRegistry{mailboxes: make(map[string]chat.TurnSteerMailbox)}
}

func (r *turnSteerRegistry) register(turnID string, mailbox chat.TurnSteerMailbox) error {
	if r == nil {
		return chat.ErrSteerUnsupported
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || mailbox == nil {
		return fmt.Errorf("turn ID and steer mailbox are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.mailboxes[turnID]; exists {
		return fmt.Errorf("steer mailbox already registered for turn %q", turnID)
	}
	r.mailboxes[turnID] = mailbox
	return nil
}

func (r *turnSteerRegistry) unregister(turnID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.mailboxes, strings.TrimSpace(turnID))
	r.mu.Unlock()
}

// rewrite is called by Eino immediately before every ChatModel invocation.
// It is deliberately not attached to the tools node, so a steer can affect
// only the next model decision and cannot alter an in-flight tool call.
func (r *turnSteerRegistry) rewrite(ctx context.Context, messages []*schema.Message) []*schema.Message {
	if r == nil {
		return messages
	}
	turnID, ok := chat.TaskTurnIDFromContext(ctx)
	if !ok {
		return messages
	}
	r.mu.RLock()
	mailbox := r.mailboxes[turnID]
	r.mu.RUnlock()
	consumer, ok := mailbox.(chat.TurnSteerConsumer)
	if !ok {
		return messages
	}
	inputs := consumer.TakeTurnSteers()
	if len(inputs) == 0 {
		return messages
	}
	result := make([]*schema.Message, 0, len(messages)+len(inputs))
	result = append(result, messages...)
	for _, input := range inputs {
		result = append(result, schema.UserMessage(input.Content))
	}
	return result
}
