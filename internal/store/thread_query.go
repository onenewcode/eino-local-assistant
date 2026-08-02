package store

import (
	"context"
	"encoding/json"
	"fmt"
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
