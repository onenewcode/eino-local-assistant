package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestUpdatePlanToolDrivesChecklist(t *testing.T) {
	controller := NewTaskController()
	tools, err := NewTaskTools(controller)
	if err != nil {
		t.Fatalf("NewTaskTools: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("task tool count = %d, want 1", len(tools))
	}

	ctx := taskTestContext(t, "update-plan-tools")
	update := taskInvokableByName(ctx, t, tools, "update_plan")
	input := `{"explanation":"implement feature","plan":[{"step":"inspect code","status":"completed"},{"step":"apply patch","status":"in_progress"},{"step":"run tests","status":"pending"}]}`
	output := runTaskTool(ctx, t, update, input)
	if !output.OK || output.Message != "Plan updated" || output.RunState != planRunActive {
		t.Fatalf("update_plan output = %#v", output)
	}
	status := controller.TaskExecutionStatus(ctx)
	if status.Tasks != 3 || status.DoneTasks != 1 || len(status.ActiveTasks) != 1 {
		t.Fatalf("status = %#v", status)
	}
}

func TestUpdatePlanRejectsMultipleInProgress(t *testing.T) {
	controller := NewTaskController()
	tools, err := NewTaskTools(controller)
	if err != nil {
		t.Fatal(err)
	}
	ctx := taskTestContext(t, "update-plan-multi-progress")
	update := taskInvokableByName(ctx, t, tools, "update_plan")
	output := runTaskTool(ctx, t, update, `{"plan":[{"step":"a","status":"in_progress"},{"step":"b","status":"in_progress"}]}`)
	if output.OK {
		t.Fatalf("expected rejection, got %#v", output)
	}
}

func taskInvokableByName(ctx context.Context, t *testing.T, tools []tool.BaseTool, name string) tool.InvokableTool {
	t.Helper()
	for _, candidate := range tools {
		info, err := candidate.Info(ctx)
		if err != nil {
			t.Fatalf("%s Info: %v", name, err)
		}
		if info.Name != name {
			continue
		}
		invokable, ok := candidate.(tool.InvokableTool)
		if !ok {
			t.Fatalf("%s is %T, want InvokableTool", name, candidate)
		}
		return invokable
	}
	t.Fatalf("task tool %q not found", name)
	return nil
}

func runTaskTool(ctx context.Context, t *testing.T, invokable tool.InvokableTool, input string) TaskToolOutput {
	t.Helper()
	raw, err := invokable.InvokableRun(ctx, input)
	if err != nil {
		t.Fatalf("InvokableRun(%s): %v", input, err)
	}
	var output TaskToolOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("decode task tool output %q: %v", raw, err)
	}
	return output
}
