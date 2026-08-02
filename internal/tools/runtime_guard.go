package tools

import (
	"context"
	"errors"

	"eino-local-assistant/internal/runtimeguard"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// runtimeGuardedTool applies a turn-scoped invocation budget to every
// invokable tool, including host-only helpers such as time and artifacts.
// Returning JSON keeps a budget exhaustion model-readable rather than turning
// it into an opaque framework error.
type runtimeGuardedTool struct {
	inner tool.InvokableTool
}

func (t *runtimeGuardedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return t.inner.Info(ctx)
}

func (t *runtimeGuardedTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if err := runtimeguard.AcquireToolCall(ctx); err != nil {
		if errors.Is(err, runtimeguard.ErrToolCallBudgetExceeded) {
			return `{"denied":true,"reason":"runtime_tool_budget_exceeded","stop_retrying":true}`, nil
		}
		return "", err
	}
	return t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
}

func guardRuntimeTool(base tool.BaseTool) tool.BaseTool {
	invokable, ok := base.(tool.InvokableTool)
	if !ok {
		return base
	}
	return &runtimeGuardedTool{inner: invokable}
}
