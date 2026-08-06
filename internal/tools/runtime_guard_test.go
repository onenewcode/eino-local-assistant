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

func TestRuntimeGuardedShellReturnsNativeStructuredRuntimeDenial(t *testing.T) {
	inner := &countingInvokableTool{name: "shell"}
	guarded := guardRuntimeTool(inner).(tool.InvokableTool)
	ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{
		MaxToolCalls: 1,
		Timeout:      time.Minute,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)
	arguments := `{"command":"find . -maxdepth 2 -type d | sort | head -50"}`
	if _, err := guarded.InvokableRun(ctx, arguments); err != nil {
		t.Fatalf("first invocation: %v", err)
	}

	raw, err := guarded.InvokableRun(ctx, arguments)
	if err != nil {
		t.Fatalf("budget denial should be a structured result: %v", err)
	}
	var result ShellOutput
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode shell denial %q: %v", raw, err)
	}
	if !result.Denied || !result.StopRetrying || result.Reason != "runtime_tool_budget_exceeded" {
		t.Fatalf("shell denial = %+v", result)
	}
	if result.ExitCode != commandExitCodeUnavailable || result.Command != "find . -maxdepth 2 -type d | sort | head -50" {
		t.Fatalf("shell denial lost execution shape: %+v", result)
	}
	if result.Impact != ToolImpactReadOnly {
		t.Fatalf("shell denial impact = %q, want %q", result.Impact, ToolImpactReadOnly)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("inner calls = %d, want denied shell not to run", got)
	}
}

func TestRuntimeGuardedApplyPatchReturnsNativeStructuredRuntimeDenial(t *testing.T) {
	inner := &countingInvokableTool{name: "apply_patch"}
	guarded := guardRuntimeTool(inner).(tool.InvokableTool)
	ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{
		MaxToolCalls: 1,
		Timeout:      time.Minute,
	})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	t.Cleanup(cancel)
	arguments := `{"operations":[{"type":"create_file","path":"one.txt","content":"one"}]}`
	if _, err := guarded.InvokableRun(ctx, arguments); err != nil {
		t.Fatalf("first invocation: %v", err)
	}

	raw, err := guarded.InvokableRun(ctx, arguments)
	if err != nil {
		t.Fatalf("budget denial should be a structured result: %v", err)
	}
	var result ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode apply_patch denial %q: %v", raw, err)
	}
	if !result.Denied || !result.StopRetrying || result.Reason != "runtime_tool_budget_exceeded" {
		t.Fatalf("apply_patch denial = %+v", result)
	}
	if result.Impact != ToolImpactWorkspaceWrite {
		t.Fatalf("apply_patch denial impact = %q, want %q", result.Impact, ToolImpactWorkspaceWrite)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("inner calls = %d, want denied patch not to run", got)
	}
}

func TestRuntimeGuardedToolResetsEquivalentCallsOnlyForConfirmedMutation(t *testing.T) {
	t.Run("confirmed apply patch", func(t *testing.T) {
		inner := &countingInvokableTool{
			name:   "apply_patch",
			output: `{"results":[{"type":"create_file","path":"one.txt","created":true}],"impact":"workspace_write"}`,
		}
		guarded := guardRuntimeTool(inner).(tool.InvokableTool)
		ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{
			MaxToolCalls:                      2,
			MaxConsecutiveEquivalentToolCalls: 1,
			Timeout:                           time.Minute,
		})
		if err != nil {
			t.Fatalf("WithTurnContext() error = %v", err)
		}
		t.Cleanup(cancel)
		arguments := `{"operations":[{"type":"create_file","path":"one.txt","content":"one"}]}`
		if _, err := guarded.InvokableRun(ctx, arguments); err != nil {
			t.Fatalf("first invocation: %v", err)
		}
		if _, err := guarded.InvokableRun(ctx, arguments); err != nil {
			t.Fatalf("second invocation after confirmed mutation: %v", err)
		}
		if got := inner.calls.Load(); got != 2 {
			t.Fatalf("inner calls = %d, want state reset to allow second call", got)
		}
	})

	t.Run("denied apply patch", func(t *testing.T) {
		inner := &countingInvokableTool{
			name:   "apply_patch",
			output: `{"denied":true,"impact":"workspace_write"}`,
		}
		guarded := guardRuntimeTool(inner).(tool.InvokableTool)
		ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{
			MaxToolCalls:                      2,
			MaxConsecutiveEquivalentToolCalls: 1,
			Timeout:                           time.Minute,
		})
		if err != nil {
			t.Fatalf("WithTurnContext() error = %v", err)
		}
		t.Cleanup(cancel)
		arguments := `{"operations":[{"type":"create_file","path":"one.txt","content":"one"}]}`
		if _, err := guarded.InvokableRun(ctx, arguments); err != nil {
			t.Fatalf("first invocation: %v", err)
		}
		raw, err := guarded.InvokableRun(ctx, arguments)
		if err != nil {
			t.Fatalf("equivalent denial should be structured: %v", err)
		}
		var result ApplyPatchOutput
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("decode equivalent denial: %v", err)
		}
		if result.Reason != "runtime_equivalent_tool_call_limit" || !result.Denied {
			t.Fatalf("second result = %+v, want no reset after denial", result)
		}
		if got := inner.calls.Load(); got != 1 {
			t.Fatalf("inner calls = %d, want duplicate denial not to run", got)
		}
	})
}

func TestGuardRuntimeToolDoesNotDoubleWrap(t *testing.T) {
	inner := &countingInvokableTool{}
	guarded := guardRuntimeTool(inner)
	if got := guardRuntimeTool(guarded); got != guarded {
		t.Fatal("guardRuntimeTool wrapped an already guarded tool")
	}
	registry := New(inner)
	registry.Append(guarded)
	for index, base := range registry.All() {
		if _, ok := base.(*runtimeGuardedTool); !ok {
			t.Fatalf("registry tool %d is not runtime guarded: %T", index, base)
		}
	}
}

type countingInvokableTool struct {
	calls  atomic.Int32
	name   string
	output string
}

func (t *countingInvokableTool) Info(context.Context) (*schema.ToolInfo, error) {
	name := t.name
	if name == "" {
		name = "counting"
	}
	return &schema.ToolInfo{Name: name}, nil
}

func (t *countingInvokableTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	t.calls.Add(1)
	if t.output != "" {
		return t.output, nil
	}
	return `{"ok":true}`, nil
}
