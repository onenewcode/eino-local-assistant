package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestTaskToolsDriveControllerWithStructuredInvocations(t *testing.T) {
	controller := NewTaskController()
	tools, err := NewTaskTools(controller)
	if err != nil {
		t.Fatalf("NewTaskTools: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("task tool count = %d, want 3", len(tools))
	}

	ctx := taskTestContext(t, "task-tools")
	plan := taskInvokableByName(ctx, t, tools, "task_plan")
	progress := taskInvokableByName(ctx, t, tools, "task_progress")
	complete := taskInvokableByName(ctx, t, tools, "task_complete")

	planInput, err := json.Marshal(simpleTaskPlan())
	if err != nil {
		t.Fatal(err)
	}
	if output := runTaskTool(ctx, t, plan, string(planInput)); !output.OK || output.RunState != taskRunActive {
		t.Fatalf("task_plan output = %#v", output)
	}
	if output := runTaskTool(ctx, t, progress, `{"action":"start","task_id":"implement"}`); !output.OK || output.TaskState != taskWorking {
		t.Fatalf("task_progress start output = %#v", output)
	}

	// task_progress cannot manufacture a proof; it must bind this controller's
	// callback observation from a real shell invocation.
	controller.RecordToolResult(ctx, "shell", "proof-call", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if output := runTaskTool(ctx, t, progress, `{"action":"record_proof","task_id":"implement","proof_id":"unit","tool_call_id":"proof-call"}`); !output.OK || output.TaskState != taskDone {
		t.Fatalf("task_progress record_proof output = %#v", output)
	}
	if output := runTaskTool(ctx, t, complete, `{}`); !output.OK || !output.Complete || output.RunState != taskRunComplete {
		t.Fatalf("task_complete output = %#v", output)
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
