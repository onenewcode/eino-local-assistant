package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// TaskCompleteInput is intentionally empty. Completion state is derived from
// the controller's recorded graph and shell observations, never caller input.
type TaskCompleteInput struct{}

// NewTaskTools creates the small control surface used by the autonomous-task
// runtime. They are agent orchestration tools, not workspace mutation tools:
// shell and apply_patch remain owned by internal/tools and their hard policy.
func NewTaskTools(controller *TaskController) ([]tool.BaseTool, error) {
	if controller == nil {
		return nil, errors.New("task controller is required")
	}
	plan, err := utils.InferTool(
		"task_plan",
		`Create or safely update the active autonomous coding task graph. Use this before editing for a substantial coding request. Include every direct user requirement, concrete normal/empty/failure/boundary scenarios, dependency-aware tasks, and at least one exact shell proof command per task. The controller automatically reserves requirement_id user-request for the current raw user request; map it to at least one scenario but do not redefine it. After an interrupted run, omitting user-request preserves the original scope and its unchanged accepted evidence. To replace scope, include user-request with the current raw user request exactly. Calling this updates controller-owned state; it does not mark anything done.`,
		func(ctx context.Context, input TaskPlanInput) (TaskToolOutput, error) {
			return controller.SetPlan(ctx, input)
		},
	)
	if err != nil {
		return nil, err
	}
	progress, err := utils.InferTool(
		"task_progress",
		`Advance or repair one controller-owned task. action=start claims a ready task before implementation. action=record_proof accepts only a completed shell tool call whose exact command matches a proof declared in task_plan; provide task_id, proof_id, and tool_call_id when available. action=replan invalidates the task and downstream dependents after a failed proof or changed assumption. Never claim completion in prose instead of using these transitions.`,
		func(ctx context.Context, input TaskProgressInput) (TaskToolOutput, error) {
			switch strings.ToLower(strings.TrimSpace(input.Action)) {
			case "start":
				return controller.StartTask(ctx, input.TaskID)
			case "record_proof":
				return controller.RecordProof(ctx, input.TaskID, input.ProofID, input.ToolCallID)
			case "replan":
				return controller.ReplanTask(ctx, input.TaskID, input.Reason)
			default:
				return TaskToolOutput{}, errors.New("action must be start, record_proof, or replan")
			}
		},
	)
	if err != nil {
		return nil, err
	}
	complete, err := utils.InferTool(
		"task_complete",
		`Request deterministic completion approval for the active autonomous task. Call this only after every planned task has passing proof evidence. If it returns complete=false, treat the returned gaps as a mandatory GapPacket: repair, rerun proofs, and call task_complete again. Only after complete=true may you give a final delivery.`,
		func(ctx context.Context, _ TaskCompleteInput) (TaskToolOutput, error) {
			return controller.RequestCompletion(ctx)
		},
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{plan, progress, complete}, nil
}
