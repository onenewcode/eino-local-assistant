package chat

import (
	"fmt"
	"strings"
	"sync"

	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

// turnUsageTracker assigns IDs to callbacks that do not supply one. Durable
// deduplication belongs to the store so conflicting replays are never hidden.
type turnUsageTracker struct {
	mu        sync.Mutex
	seen      bool
	nextCall  int
	allocator *turnCallIDAllocator
}

func newTurnUsageTracker(allocator *turnCallIDAllocator) *turnUsageTracker {
	return &turnUsageTracker{allocator: allocator}
}

func (t *turnUsageTracker) normalize(event ModelUsageEvent) ModelUsageEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.TrimSpace(event.CallID) == "" {
		if t.allocator != nil {
			event.CallID = t.allocator.nextModelUsageID()
		} else {
			t.nextCall++
			event.CallID = fmt.Sprintf("model-%d", t.nextCall)
		}
	}
	t.seen = true
	return event
}

func (t *turnUsageTracker) hasEvents() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.seen
}

func (s *Session) normalizedModelUsage(turnID string, event ModelUsageEvent) (ModelUsageEvent, store.ModelUsage) {
	callID := strings.TrimSpace(event.CallID)
	if callID == "" {
		callID = newLocalID("usage")
	}
	if turnID != "" {
		callID = turnID + ":" + callID
	}
	event.CallID = callID
	if event.Operation == "" {
		event.Operation = ModelUsageOperationAgent
	}
	if event.Available {
		event.Usage.CostUSD = usage.CostUSD(event.Usage.PromptTokens, event.Usage.CompletionTokens, s.pricing)
	}

	operation := store.UsageOperation(event.Operation)
	record := store.ModelUsage{
		CallID:           event.CallID,
		TurnID:           turnID,
		Operation:        operation,
		HasProviderUsage: event.Available,
		PromptTokens:     event.Usage.PromptTokens,
		CompletionTokens: event.Usage.CompletionTokens,
		TotalTokens:      event.Usage.TotalTokens,
		CachedTokens:     event.Usage.CachedTokens,
		ReasoningTokens:  event.Usage.ReasoningTokens,
		CostUSD:          event.Usage.CostUSD,
	}
	if event.Operation == ModelUsageOperationAgent && event.Available {
		// Provider usage describes occupancy of the complete model window, not
		// the smaller local input admission budget.
		record.ContextWindowTokens = s.contextCfg.WindowTokens
	}
	return event, record
}

func (s *Session) providerUsageEvent(callID string, answer *schema.Message) ModelUsageEvent {
	turn, available := usage.FromMessageUsage(answer)
	return ModelUsageEvent{
		CallID:    callID,
		Operation: ModelUsageOperationAgent,
		Usage:     turn,
		Available: available,
	}
}
