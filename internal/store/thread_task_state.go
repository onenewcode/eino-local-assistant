package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// maxTaskStateBytes limits the compact task-controller projection retained in
// each journal update. Full command output remains in immutable artifacts.
const maxTaskStateBytes = 512 << 10

// TaskStateUpdate is an opaque, versioned runtime snapshot. Store validates
// only the envelope because task-graph schema belongs to its owner in agent.
type TaskStateUpdate struct {
	Snapshot json.RawMessage `json:"snapshot"`
}

var _ TaskStateRepository = (*ThreadStore)(nil)
var _ TaskStateRecoveryRepository = (*ThreadStore)(nil)

// LoadTaskState returns the latest task-controller snapshot rebuilt from the
// hash-chained journal. A nil slice means this thread has not started an
// autonomous task.
func (s *ThreadStore) LoadTaskState(ctx context.Context, id string) (json.RawMessage, error) {
	recovery, err := s.LoadTaskStateRecovery(ctx, id)
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), recovery.Snapshot...), nil
}

// LoadTaskStateRecovery returns the latest controller projection together
// with conservative lifecycle facts needed after a crash. A shell or patch
// event after the projection may have changed the workspace before the
// controller could write its next snapshot.
func (s *ThreadStore) LoadTaskStateRecovery(ctx context.Context, id string) (TaskStateRecovery, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return TaskStateRecovery{}, err
	}
	defer unlock()
	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return TaskStateRecovery{}, err
	}
	return taskStateRecoveryFromEvents(state, events), nil
}

func taskStateRecoveryFromEvents(state ThreadState, events []ThreadEvent) TaskStateRecovery {
	recovery := TaskStateRecovery{Snapshot: append(json.RawMessage(nil), state.TaskState...)}
	latestSnapshot := -1
	for index, event := range events {
		if event.Kind != EventTaskStateUpdated {
			continue
		}
		latestSnapshot = index
		recovery.SnapshotTurnID = strings.TrimSpace(event.TurnID)
	}
	if latestSnapshot < 0 || recovery.SnapshotTurnID == "" {
		// Out-of-band task updates (for example a UI interruption) do not need a
		// turn commit to become durable. When no snapshot exists, scan the whole
		// ledger so a pre-plan shell or patch cannot disappear across a restart.
		recovery.SnapshotTurnCommitted = true
	}
	firstAfterSnapshot := latestSnapshot + 1
	openPotentialMutations := make(map[string]struct{})
	for _, event := range events[firstAfterSnapshot:] {
		if recovery.SnapshotTurnID != "" && event.Kind == EventTurnCommitted && event.TurnID == recovery.SnapshotTurnID {
			recovery.SnapshotTurnCommitted = true
		}
		switch event.Kind {
		case EventToolStarted:
			var payload ToolStarted
			if json.Unmarshal(event.Payload, &payload) != nil {
				recovery.PotentiallyMutatingToolAfterSnapshot = true
				continue
			}
			if taskLifecycleToolNameMayMutate(payload.ToolName) {
				openPotentialMutations[taskLifecycleToolKey(event.TurnID, payload.ToolCallID)] = struct{}{}
			}
		case EventToolCompleted:
			var payload ToolCompleted
			if json.Unmarshal(event.Payload, &payload) != nil {
				recovery.PotentiallyMutatingToolAfterSnapshot = true
				continue
			}
			delete(openPotentialMutations, taskLifecycleToolKey(event.TurnID, payload.ToolCallID))
			if taskLifecycleCompletedToolMayMutate(payload) {
				recovery.PotentiallyMutatingToolAfterSnapshot = true
			}
		}
	}
	if len(openPotentialMutations) > 0 {
		recovery.PotentiallyMutatingToolAfterSnapshot = true
	}
	return recovery
}

func taskLifecycleToolNameMayMutate(name string) bool {
	switch strings.TrimSpace(name) {
	case "shell", "apply_patch":
		return true
	default:
		return false
	}
}

func taskLifecycleCompletedToolMayMutate(input ToolCompleted) bool {
	switch strings.TrimSpace(input.ToolName) {
	case "shell":
		// Older journals and malformed results did not retain impact metadata.
		return strings.TrimSpace(input.Impact) != "read_only"
	case "apply_patch":
		return true
	default:
		return false
	}
}

func taskLifecycleToolKey(turnID, callID string) string {
	return turnID + "\x00" + callID
}

// UpdateTaskState appends one recoverable task-state projection. turnID is
// optional: a model-owned update is bound to its active turn, while an
// interactive interruption may update the graph while that turn winds down.
func (s *ThreadStore) UpdateTaskState(ctx context.Context, id string, expectedRevision uint64, turnID string, input TaskStateUpdate) (ThreadState, error) {
	if err := validateTaskStateUpdate(input); err != nil {
		return ThreadState{}, err
	}
	input.Snapshot = append(json.RawMessage(nil), input.Snapshot...)
	return s.mutate(ctx, id, expectedRevision, EventTaskStateUpdated, strings.TrimSpace(turnID), input)
}

func validateTaskStateUpdate(input TaskStateUpdate) error {
	payload := input.Snapshot
	if len(payload) == 0 {
		return errors.New("task state snapshot is required")
	}
	if len(payload) > maxTaskStateBytes {
		return fmt.Errorf("task state snapshot exceeds %d bytes", maxTaskStateBytes)
	}
	if !json.Valid(payload) {
		return errors.New("task state snapshot must be valid JSON")
	}
	return nil
}
