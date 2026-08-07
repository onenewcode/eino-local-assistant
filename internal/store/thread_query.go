package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// LoadTurnGroups reconstructs starts, tool lifecycle records, and terminal
// outcomes from the append-only journal without hydrating the full transcript.
func (s *ThreadStore) LoadTurnGroups(ctx context.Context, id string) ([]TurnGroup, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	_, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return nil, err
	}
	return turnGroupsFromEvents(events)
}

// LoadCompactionUsage returns the immutable provider-call records associated
// with one compaction transaction. It intentionally returns only compaction
// calls, so a UI cannot confuse cache-read telemetry with the chat turn.
func (s *ThreadStore) LoadCompactionUsage(ctx context.Context, id, operationID string) ([]ModelUsage, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return nil, fmt.Errorf("compaction operation id is required")
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	_, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return nil, err
	}
	usages := make([]ModelUsage, 0)
	seenCallIDs := make(map[string]struct{})
	for _, event := range events {
		if event.Kind != EventUsageRecorded {
			continue
		}
		var usage ModelUsage
		if err := json.Unmarshal(event.Payload, &usage); err != nil {
			return nil, fmt.Errorf("decode usage record: %w", err)
		}
		normalized, err := normalizeModelUsage(usage)
		if err != nil {
			return nil, fmt.Errorf("invalid usage record: %w", err)
		}
		if normalized.Operation == UsageOperationCompaction && normalized.OperationID == operationID {
			if _, seen := seenCallIDs[normalized.CallID]; seen {
				// Keep this query consistent with replay, where an identical
				// usage.recorded event is an idempotent retry rather than a
				// second provider call.
				continue
			}
			seenCallIDs[normalized.CallID] = struct{}{}
			usages = append(usages, normalized)
		}
	}
	return usages, nil
}

func turnGroupsFromEvents(events []ThreadEvent) ([]TurnGroup, error) {
	groups := make([]TurnGroup, 0)
	indexes := make(map[string]int)
	toolIndexes := make(map[string]map[string]int)
	getGroup := func(turnID string) *TurnGroup {
		if index, ok := indexes[turnID]; ok {
			return &groups[index]
		}
		indexes[turnID] = len(groups)
		toolIndexes[turnID] = make(map[string]int)
		groups = append(groups, TurnGroup{TurnID: turnID})
		return &groups[len(groups)-1]
	}
	getTool := func(group *TurnGroup, turnID, toolCallID string) *ToolGroup {
		indexesForTurn := toolIndexes[turnID]
		if index, ok := indexesForTurn[toolCallID]; ok {
			return &group.Tools[index]
		}
		indexesForTurn[toolCallID] = len(group.Tools)
		group.Tools = append(group.Tools, ToolGroup{ToolCallID: toolCallID})
		return &group.Tools[len(group.Tools)-1]
	}

	for _, event := range events {
		if event.TurnID == "" {
			continue
		}
		group := getGroup(event.TurnID)
		group.SourceEventIDs = append(group.SourceEventIDs, event.ID)
		if group.StartSequence == 0 {
			group.StartSequence = event.Sequence
		}
		group.EndSequence = event.Sequence
		switch event.Kind {
		case EventTurnStarted:
			var payload TurnStart
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode turn start: %w", err)
			}
			group.Started = &payload
		case EventToolStarted:
			var payload ToolStarted
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode tool start: %w", err)
			}
			tool := getTool(group, event.TurnID, payload.ToolCallID)
			tool.Started = &payload
			tool.EventIDs = append(tool.EventIDs, event.ID)
		case EventToolCompleted:
			var payload ToolCompleted
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode tool completion: %w", err)
			}
			tool := getTool(group, event.TurnID, payload.ToolCallID)
			tool.Completed = &payload
			tool.EventIDs = append(tool.EventIDs, event.ID)
		case EventUsageRecorded:
			var payload ModelUsage
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode usage record: %w", err)
			}
			group.Usages = append(group.Usages, payload)
		case EventTurnCommitted:
			var payload TurnCommit
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode turn commit: %w", err)
			}
			payload.Messages = cloneMessages(payload.Messages)
			group.Committed = &payload
		case EventTurnCancelled:
			var payload TurnCancel
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode turn cancellation: %w", err)
			}
			group.Cancelled = &payload
		case EventTurnFailed:
			var payload TurnFailure
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode turn failure: %w", err)
			}
			group.Failed = &payload
		}
	}
	return groups, nil
}
