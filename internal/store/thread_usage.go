package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// RecordUsage durably appends one completed model call. Unlike lifecycle
// mutations, this immutable CallID-keyed event rebases under the store lock so
// an unrelated revision change cannot discard completed provider accounting.
func (s *ThreadStore) RecordUsage(ctx context.Context, id string, input ModelUsage) (ThreadState, error) {
	normalized, err := normalizeModelUsage(input)
	if err != nil {
		return ThreadState{}, err
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, err
	}
	defer unlock()

	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, err
	}
	existing, found, err := modelUsageByCallID(events, normalized.CallID)
	if err != nil {
		return ThreadState{}, err
	}
	if found {
		if existing != normalized {
			return ThreadState{}, fmt.Errorf("%w: call id %q", ErrUsageRecordConflict, normalized.CallID)
		}
		return state, nil
	}
	if normalized.Operation == UsageOperationCompaction && normalized.OperationID != "" {
		if state.PendingCompaction == nil {
			return ThreadState{}, fmt.Errorf("compaction usage for operation %q requires a pending compaction", normalized.OperationID)
		}
		if normalized.OperationID != state.PendingCompaction.OperationID {
			return ThreadState{}, fmt.Errorf("compaction usage does not match pending operation %q", state.PendingCompaction.OperationID)
		}
	}
	// Historical journals recorded compaction usage before operation IDs existed.
	// Keep blank IDs replayable and writable for migration compatibility.
	if err := validateLifecycleMutation(events, EventUsageRecorded, normalized.TurnID, normalized); err != nil {
		return ThreadState{}, err
	}
	return s.appendLocked(dir, state, state.Revision, EventUsageRecorded, normalized.TurnID, normalized)
}

func modelUsageByCallID(events []ThreadEvent, callID string) (ModelUsage, bool, error) {
	for _, event := range events {
		if event.Kind != EventUsageRecorded {
			continue
		}
		var payload ModelUsage
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return ModelUsage{}, false, fmt.Errorf("decode usage record: %w", err)
		}
		normalized, err := normalizeModelUsage(payload)
		if err != nil {
			return ModelUsage{}, false, fmt.Errorf("invalid usage record: %w", err)
		}
		if normalized.CallID == callID {
			return normalized, true, nil
		}
	}
	return ModelUsage{}, false, nil
}

func normalizeModelUsage(input ModelUsage) (ModelUsage, error) {
	input.CallID = strings.TrimSpace(input.CallID)
	input.TurnID = strings.TrimSpace(input.TurnID)
	input.Operation = UsageOperation(strings.TrimSpace(string(input.Operation)))
	input.OperationID = strings.TrimSpace(input.OperationID)
	if input.CallID == "" {
		return ModelUsage{}, fmt.Errorf("model usage call id is required")
	}
	if input.Operation != UsageOperationAgent && input.Operation != UsageOperationCompaction {
		return ModelUsage{}, fmt.Errorf("invalid model usage operation %q", input.Operation)
	}
	if input.Operation == UsageOperationAgent && input.TurnID == "" {
		return ModelUsage{}, fmt.Errorf("agent model usage turn id is required")
	}
	if input.Operation != UsageOperationCompaction && input.OperationID != "" {
		return ModelUsage{}, fmt.Errorf("model usage operation id is only valid for compaction")
	}
	if input.PromptTokens < 0 || input.CompletionTokens < 0 || input.TotalTokens < 0 || input.CachedTokens < 0 || input.ReasoningTokens < 0 || input.ContextBudgetTokens < 0 {
		return ModelUsage{}, fmt.Errorf("model usage token counts must not be negative")
	}
	if input.CostUSD < 0 || math.IsNaN(input.CostUSD) || math.IsInf(input.CostUSD, 0) {
		return ModelUsage{}, fmt.Errorf("model usage cost must be finite and non-negative")
	}
	if !input.HasProviderUsage {
		// Estimates must never enter the durable API-usage totals.
		input.PromptTokens = 0
		input.CompletionTokens = 0
		input.TotalTokens = 0
		input.CachedTokens = 0
		input.ReasoningTokens = 0
		input.ContextBudgetTokens = 0
		input.CostUSD = 0
		return input, nil
	}
	if input.TotalTokens == 0 {
		input.TotalTokens = input.PromptTokens + input.CompletionTokens
	}
	return input, nil
}

func validUsageStatus(status UsageStatus) bool {
	switch status {
	case UsageStatusExact, UsageStatusIncomplete, UsageStatusUnavailable:
		return true
	default:
		return false
	}
}

func nextUsageStatus(current UsageStatus, hasProviderUsage bool) UsageStatus {
	if current == UsageStatusUnavailable {
		// New records are still only a partial view of a pre-accounting thread.
		return UsageStatusIncomplete
	}
	if current == UsageStatusIncomplete || !hasProviderUsage {
		return UsageStatusIncomplete
	}
	return UsageStatusExact
}

func clearUsageProjection(meta *ThreadMeta) {
	if meta == nil {
		return
	}
	meta.PromptTokens = 0
	meta.CompletionTokens = 0
	meta.TotalTokens = 0
	meta.CachedTokens = 0
	meta.ReasoningTokens = 0
	meta.ModelCallCount = 0
	meta.CostUSD = 0
	meta.LastContext = nil
}
