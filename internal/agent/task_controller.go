package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"eino-local-assistant/internal/chat"
)

// Plan step statuses match Codex CLI's update_plan tool
// (pending | in_progress | completed). At most one step may be in_progress.
const (
	planStatusPending    = "pending"
	planStatusInProgress = "in_progress"
	planStatusCompleted  = "completed"

	planRunActive      = "active"
	planRunInterrupted = "interrupted"
)

// TaskController is a Codex-style checklist store for multi-step work.
// It owns display state for update_plan only. Permissions and sandbox remain
// the write boundary; the checklist never gates delivery.
type TaskController struct {
	mu     sync.RWMutex
	runs   map[string]*planRun
	loaded map[string]bool
}

type planRun struct {
	explanation string
	state       string // active | interrupted
	items       []planItem
}

type planItem struct {
	Step   string
	Status string
}

// UpdatePlanInput is the model-facing checklist (Codex UpdatePlanArgs).
type UpdatePlanInput struct {
	Explanation string          `json:"explanation,omitempty" jsonschema:"description=Optional short note for this plan update."`
	Plan        []PlanItemInput `json:"plan" jsonschema:"description=Full replacement checklist. At most one step may be in_progress."`
}

// PlanItemInput is one checklist row.
type PlanItemInput struct {
	Step   string `json:"step" jsonschema:"description=Task step text shown to the user."`
	Status string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed,description=Step status."`
}

// TaskToolOutput is returned by update_plan.
type TaskToolOutput struct {
	OK          bool   `json:"ok"`
	RunState    string `json:"run_state,omitempty"`
	Message     string `json:"message"`
	DisplayHint string `json:"display_hint,omitempty"`
}

// NewTaskController creates the per-process plan store shared by ReAct tools.
func NewTaskController() *TaskController {
	return &TaskController{runs: make(map[string]*planRun), loaded: make(map[string]bool)}
}

func taskThreadID(ctx context.Context) (string, error) {
	threadID, ok := chat.TaskRunIDFromContext(ctx)
	if !ok {
		return "", errors.New("plan runtime is unavailable outside an active session")
	}
	return threadID, nil
}

// UpdatePlan replaces the active checklist. Mirrors Codex: store the plan for
// UI and model context; return a short acknowledgement.
func (c *TaskController) UpdatePlan(ctx context.Context, input UpdatePlanInput) (TaskToolOutput, error) {
	if c == nil {
		return TaskToolOutput{}, errors.New("task controller is required")
	}
	threadID, err := taskThreadID(ctx)
	if err != nil {
		return TaskToolOutput{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return TaskToolOutput{}, err
	}
	if err := c.loadRunLocked(ctx, threadID); err != nil {
		return TaskToolOutput{}, err
	}
	run, err := planRunFromInput(input)
	if err != nil {
		return TaskToolOutput{Message: err.Error()}, nil
	}
	if err := c.persistPlanLocked(ctx, run); err != nil {
		c.runs[threadID] = run
		return TaskToolOutput{}, err
	}
	c.runs[threadID] = run
	return TaskToolOutput{
		OK:          true,
		RunState:    run.state,
		Message:     "Plan updated",
		DisplayHint: "Plan updated",
	}, nil
}

func planRunFromInput(input UpdatePlanInput) (*planRun, error) {
	if len(input.Plan) == 0 {
		return nil, errors.New("plan must include at least one step")
	}
	inProgress := 0
	items := make([]planItem, 0, len(input.Plan))
	for i, raw := range input.Plan {
		step := strings.Join(strings.Fields(strings.TrimSpace(raw.Step)), " ")
		if step == "" {
			return nil, fmt.Errorf("plan item %d needs non-empty step text", i)
		}
		status := strings.ToLower(strings.TrimSpace(raw.Status))
		switch status {
		case planStatusPending, planStatusInProgress, planStatusCompleted:
		case "":
			status = planStatusPending
		default:
			return nil, fmt.Errorf("plan item %d status must be pending, in_progress, or completed", i)
		}
		if status == planStatusInProgress {
			inProgress++
		}
		items = append(items, planItem{Step: step, Status: status})
	}
	if inProgress > 1 {
		return nil, errors.New("at most one plan step may be in_progress")
	}
	return &planRun{
		explanation: strings.TrimSpace(input.Explanation),
		state:       planRunActive,
		items:       items,
	}, nil
}

// ExecutionPacket injects the current checklist into model context.
func (c *TaskController) ExecutionPacket(ctx context.Context) string {
	if c == nil {
		return ""
	}
	threadID, err := taskThreadID(ctx)
	if err != nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadRunLocked(ctx, threadID); err != nil {
		return ""
	}
	run := c.runs[threadID]
	if run == nil || len(run.items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Current plan (checklist)\n\n")
	b.WriteString("Progress checklist only. It does not gate delivery; keep permissions and sandbox in mind while editing.\n")
	if run.explanation != "" {
		fmt.Fprintf(&b, "Note: %s\n", run.explanation)
	}
	if run.state == planRunInterrupted {
		b.WriteString("Prior work was interrupted; refresh the checklist with update_plan if scope changed.\n")
	}
	b.WriteString("Steps:\n")
	for _, item := range run.items {
		fmt.Fprintf(&b, "- [%s] %s\n", item.Status, item.Step)
	}
	return b.String()
}

// TaskExecutionStatus projects the checklist for TUI /goal and Updated Plan cards.
func (c *TaskController) TaskExecutionStatus(ctx context.Context) chat.TaskRunStatus {
	if c == nil {
		return chat.TaskRunStatus{}
	}
	threadID, err := taskThreadID(ctx)
	if err != nil {
		return chat.TaskRunStatus{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadRunLocked(ctx, threadID); err != nil {
		return chat.TaskRunStatus{Available: true, State: "recovery_error"}
	}
	return planStatus(c.runs[threadID])
}

// InterruptTask marks the checklist interrupted for display.
func (c *TaskController) InterruptTask(ctx context.Context, _ string) chat.TaskInterruptReceipt {
	if c == nil {
		return chat.TaskInterruptReceipt{Summary: "plan runtime is unavailable"}
	}
	threadID, err := taskThreadID(ctx)
	if err != nil {
		return chat.TaskInterruptReceipt{Summary: "plan runtime is unavailable"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.loadRunLocked(ctx, threadID); err != nil {
		return chat.TaskInterruptReceipt{Summary: "cannot recover plan: " + err.Error()}
	}
	run := c.runs[threadID]
	if run == nil || run.state != planRunActive {
		return chat.TaskInterruptReceipt{Summary: "no active plan"}
	}
	candidate := clonePlanRun(run)
	candidate.state = planRunInterrupted
	if err := c.persistPlanLocked(ctx, candidate); err != nil {
		c.runs[threadID] = candidate
		return chat.TaskInterruptReceipt{Applied: true, Summary: "plan interrupted (persist deferred): " + err.Error()}
	}
	c.runs[threadID] = candidate
	return chat.TaskInterruptReceipt{Applied: true, Summary: "plan interrupted"}
}

func planStatus(run *planRun) chat.TaskRunStatus {
	if run == nil || len(run.items) == 0 {
		return chat.TaskRunStatus{}
	}
	status := chat.TaskRunStatus{
		Available: true,
		State:     run.state,
		Goal:      run.explanation,
		Tasks:     len(run.items),
	}
	active := make([]string, 0, 1)
	for i, item := range run.items {
		id := fmt.Sprintf("step-%d", i+1)
		state := mapPlanStatusToDisplay(item.Status)
		if item.Status == planStatusCompleted {
			status.DoneTasks++
		}
		if item.Status == planStatusInProgress {
			active = append(active, id)
		}
		status.Items = append(status.Items, chat.TaskListItem{
			ID:    id,
			Goal:  item.Step,
			State: state,
		})
	}
	status.ActiveTasks = active
	return status
}

func mapPlanStatusToDisplay(status string) string {
	switch status {
	case planStatusCompleted:
		return "done"
	case planStatusInProgress:
		return "working"
	default:
		return "pending"
	}
}

func clonePlanRun(source *planRun) *planRun {
	if source == nil {
		return nil
	}
	cloned := *source
	cloned.items = append([]planItem(nil), source.items...)
	return &cloned
}

func isTaskControlTool(name string) bool {
	return name == "update_plan"
}
