package store

import (
	"encoding/json"
	"fmt"
)

type lifecycleTracker struct {
	turns        map[string]*lifecycleTurn
	activeTurnID string
}

type lifecycleTurn struct {
	started  bool
	terminal EventKind
	tools    map[string]*lifecycleTool
}

type lifecycleTool struct {
	name      string
	started   bool
	completed bool
}

func newLifecycleTracker() *lifecycleTracker {
	return &lifecycleTracker{turns: make(map[string]*lifecycleTurn)}
}

func lifecycleFromEvents(events []ThreadEvent) (*lifecycleTracker, error) {
	tracker := newLifecycleTracker()
	for _, event := range events {
		if err := tracker.apply(event); err != nil {
			return nil, err
		}
	}
	return tracker, nil
}

func validateLifecycleMutation(events []ThreadEvent, kind EventKind, turnID string, payload any) error {
	tracker, err := lifecycleFromEvents(events)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode lifecycle payload: %w", err)
	}
	return tracker.apply(ThreadEvent{Kind: kind, TurnID: turnID, Payload: raw})
}

func (t *lifecycleTracker) apply(event ThreadEvent) error {
	switch event.Kind {
	case EventThreadCreated, EventTitleChanged, EventContextCompactionStarted, EventContextCompacted,
		EventContextCompactionFailed, EventContextCheckpointReset:
		// Compaction lifecycle events are independent of an agent turn. They
		// must not make an idle thread look like it has an unfinished turn.
		return nil
	case EventTurnStarted:
		var payload TurnStart
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return lifecycleError(event, "decode turn start: %v", err)
		}
		if payload.TurnID == "" || payload.TurnID != event.TurnID {
			return lifecycleError(event, "turn start id does not match event")
		}
		if _, exists := t.turns[event.TurnID]; exists {
			return lifecycleError(event, "duplicate turn start")
		}
		if t.activeTurnID != "" {
			return lifecycleError(event, "turn %q is still active", t.activeTurnID)
		}
		t.turns[event.TurnID] = &lifecycleTurn{started: true, tools: make(map[string]*lifecycleTool)}
		t.activeTurnID = event.TurnID
		return nil
	case EventToolStarted:
		var payload ToolStarted
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return lifecycleError(event, "decode tool start: %v", err)
		}
		turn, err := t.openTurn(event, payload.TurnID)
		if err != nil {
			return err
		}
		if payload.ToolCallID == "" {
			return lifecycleError(event, "tool call id is required")
		}
		if _, exists := turn.tools[payload.ToolCallID]; exists {
			return lifecycleError(event, "duplicate tool start for %q", payload.ToolCallID)
		}
		turn.tools[payload.ToolCallID] = &lifecycleTool{name: payload.ToolName, started: true}
		return nil
	case EventToolCompleted:
		var payload ToolCompleted
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return lifecycleError(event, "decode tool completion: %v", err)
		}
		turn, err := t.openTurn(event, payload.TurnID)
		if err != nil {
			return err
		}
		tool := turn.tools[payload.ToolCallID]
		if payload.ToolCallID == "" || tool == nil || !tool.started || tool.completed {
			return lifecycleError(event, "tool completion has no open start for %q", payload.ToolCallID)
		}
		if tool.name != "" && payload.ToolName != "" && tool.name != payload.ToolName {
			return lifecycleError(event, "tool completion name does not match start")
		}
		tool.completed = true
		return nil
	case EventTurnCommitted:
		var payload TurnCommit
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return lifecycleError(event, "decode turn commit: %v", err)
		}
		turn, err := t.openTurn(event, payload.TurnID)
		if err != nil {
			return err
		}
		for id, tool := range turn.tools {
			if !tool.completed {
				return lifecycleError(event, "turn committed with unfinished tool %q", id)
			}
		}
		turn.terminal = EventTurnCommitted
		t.activeTurnID = ""
		return nil
	case EventTurnCancelled:
		var payload TurnCancel
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return lifecycleError(event, "decode turn cancellation: %v", err)
		}
		turn, err := t.openTurn(event, payload.TurnID)
		if err != nil {
			return err
		}
		turn.terminal = EventTurnCancelled
		t.activeTurnID = ""
		return nil
	case EventTurnFailed:
		var payload TurnFailure
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return lifecycleError(event, "decode turn failure: %v", err)
		}
		turn, err := t.openTurn(event, payload.TurnID)
		if err != nil {
			return err
		}
		turn.terminal = EventTurnFailed
		t.activeTurnID = ""
		return nil
	case EventUsageRecorded:
		var payload ModelUsage
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return lifecycleError(event, "decode usage record: %v", err)
		}
		usage, err := normalizeModelUsage(payload)
		if err != nil {
			return lifecycleError(event, "invalid usage record: %v", err)
		}
		if usage.TurnID != event.TurnID {
			return lifecycleError(event, "usage turn id does not match event")
		}
		if usage.Operation == UsageOperationCompaction && usage.TurnID == "" {
			return nil
		}
		_, err = t.openTurn(event, usage.TurnID)
		return err
	default:
		return lifecycleError(event, "unknown lifecycle event kind %q", event.Kind)
	}
}

func (t *lifecycleTracker) openTurn(event ThreadEvent, payloadTurnID string) (*lifecycleTurn, error) {
	if payloadTurnID == "" || payloadTurnID != event.TurnID {
		return nil, lifecycleError(event, "payload turn id does not match event")
	}
	turn := t.turns[event.TurnID]
	if turn == nil || !turn.started {
		return nil, lifecycleError(event, "turn was not started")
	}
	if turn.terminal != "" {
		return nil, lifecycleError(event, "turn already ended as %s", turn.terminal)
	}
	if t.activeTurnID != event.TurnID {
		return nil, lifecycleError(event, "turn is not active")
	}
	return turn, nil
}

func lifecycleError(event ThreadEvent, format string, args ...any) error {
	return fmt.Errorf("%w: turn %q %s", ErrInvalidThreadLifecycle, event.TurnID, fmt.Sprintf(format, args...))
}
