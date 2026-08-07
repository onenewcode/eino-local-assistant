package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"eino-local-assistant/internal/chat"
)

// Snapshot version 3 is the Codex-style checklist. Older versions are ignored
// on load so recovery does not re-arm the retired proof/completion gate.
const planSnapshotVersion = 3

type planSnapshot struct {
	Version     int                `json:"version"`
	Explanation string             `json:"explanation,omitempty"`
	State       string             `json:"state"`
	Items       []planItemSnapshot `json:"items"`
}

type planItemSnapshot struct {
	Step   string `json:"step"`
	Status string `json:"status"`
}

func (c *TaskController) loadRunLocked(ctx context.Context, threadID string) error {
	if c.loaded[threadID] {
		return nil
	}
	recovery, err := chat.LoadTaskStateRecovery(ctx)
	if err != nil {
		return fmt.Errorf("load durable plan state: %w", err)
	}
	raw := recovery.Snapshot
	if len(raw) == 0 {
		c.loaded[threadID] = true
		return nil
	}
	run, err := planRunFromSnapshot(raw)
	if err != nil {
		// Drop incompatible legacy snapshots rather than blocking the session.
		c.loaded[threadID] = true
		return nil
	}
	c.runs[threadID] = run
	c.loaded[threadID] = true
	return nil
}

func (c *TaskController) persistPlanLocked(ctx context.Context, run *planRun) error {
	if run == nil {
		return nil
	}
	raw, err := encodePlanSnapshot(run)
	if err != nil {
		return err
	}
	return chat.PersistTaskState(ctx, raw)
}

func encodePlanSnapshot(run *planRun) ([]byte, error) {
	if run == nil {
		return nil, errors.New("plan run is required")
	}
	snapshot := planSnapshot{
		Version:     planSnapshotVersion,
		Explanation: run.explanation,
		State:       run.state,
		Items:       make([]planItemSnapshot, 0, len(run.items)),
	}
	for _, item := range run.items {
		snapshot.Items = append(snapshot.Items, planItemSnapshot{Step: item.Step, Status: item.Status})
	}
	return json.Marshal(snapshot)
}

func planRunFromSnapshot(raw []byte) (*planRun, error) {
	var snapshot planSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Version != planSnapshotVersion {
		return nil, fmt.Errorf("unsupported plan snapshot version %d", snapshot.Version)
	}
	if len(snapshot.Items) == 0 {
		return nil, fmt.Errorf("plan snapshot has no items")
	}
	state := strings.TrimSpace(snapshot.State)
	if state == "" {
		state = planRunActive
	}
	if state != planRunActive && state != planRunInterrupted {
		return nil, fmt.Errorf("invalid plan state %q", state)
	}
	items := make([]planItem, 0, len(snapshot.Items))
	inProgress := 0
	for i, item := range snapshot.Items {
		step := strings.Join(strings.Fields(strings.TrimSpace(item.Step)), " ")
		status := strings.ToLower(strings.TrimSpace(item.Status))
		if step == "" {
			return nil, fmt.Errorf("plan snapshot item %d missing step", i)
		}
		switch status {
		case planStatusPending, planStatusInProgress, planStatusCompleted:
		default:
			return nil, fmt.Errorf("plan snapshot item %d has invalid status %q", i, status)
		}
		if status == planStatusInProgress {
			inProgress++
		}
		items = append(items, planItem{Step: step, Status: status})
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("plan snapshot has %d in_progress steps", inProgress)
	}
	return &planRun{
		explanation: strings.TrimSpace(snapshot.Explanation),
		state:       state,
		items:       items,
	}, nil
}
