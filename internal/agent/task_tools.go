package agent

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// NewTaskTools registers the Codex-style checklist tool surface.
// Only update_plan is exposed: a soft progress list, not a proof or delivery gate.
func NewTaskTools(controller *TaskController) ([]tool.BaseTool, error) {
	if controller == nil {
		return nil, errors.New("task controller is required")
	}
	updatePlan, err := utils.InferTool(
		"update_plan",
		`Updates the task plan checklist for multi-step work. Provide an optional explanation and a full list of plan items, each with step text and status pending|in_progress|completed. At most one step may be in_progress. This is progress UI only: it does not authorize workspace writes, does not require shell proofs, and does not block final answers. Simple factual replies do not need a plan.`,
		func(ctx context.Context, input UpdatePlanInput) (TaskToolOutput, error) {
			return controller.UpdatePlan(ctx, input)
		},
	)
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{updatePlan}, nil
}
