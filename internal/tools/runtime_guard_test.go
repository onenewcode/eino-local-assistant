package tools

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"eino-local-assistant/internal/runtimeguard"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestRuntimeGuardedToolEnforcesSharedTurnBudget(t *testing.T) {
	inner := &countingInvokableTool{}
	guarded, ok := guardRuntimeTool(inner).(tool.InvokableTool)
	if !ok {
		t.Fatal("guardRuntimeTool did not return an invokable tool")
	}
	ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{
		MaxToolCalls: 1,
		Timeout:      time.Minute,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	defer cancel()

	if got, err := guarded.InvokableRun(ctx, `{}`); err != nil || got != `{"ok":true}` {
		t.Fatalf("first invocation = %q, %v", got, err)
	}
	got, err := guarded.InvokableRun(ctx, `{}`)
	if err != nil {
		t.Fatalf("budget exhaustion should be a structured result: %v", err)
	}
	var result struct {
		Denied       bool   `json:"denied"`
		Reason       string `json:"reason"`
		StopRetrying bool   `json:"stop_retrying"`
	}
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("decode budget result %q: %v", got, err)
	}
	if !result.Denied || result.Reason != "runtime_tool_budget_exceeded" || !result.StopRetrying {
		t.Fatalf("budget result = %#v", result)
	}
	if calls := inner.calls.Load(); calls != 1 {
		t.Fatalf("inner calls = %d, want 1", calls)
	}
}

func TestRuntimeGuardedToolReturnsTurnDeadlineCause(t *testing.T) {
	inner := &countingInvokableTool{}
	guarded := guardRuntimeTool(inner).(tool.InvokableTool)
	ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{
		MaxToolCalls: 1,
		Timeout:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	defer cancel()
	<-ctx.Done()

	if _, err := guarded.InvokableRun(ctx, `{}`); !errors.Is(err, runtimeguard.ErrTurnDeadlineExceeded) {
		t.Fatalf("InvokableRun() error = %v, want turn deadline cause", err)
	}
	if calls := inner.calls.Load(); calls != 0 {
		t.Fatalf("inner calls = %d, want 0", calls)
	}
}

type countingInvokableTool struct {
	calls atomic.Int32
}

func (*countingInvokableTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "counting"}, nil
}

func (t *countingInvokableTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	t.calls.Add(1)
	return `{"ok":true}`, nil
}
