package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-local-assistant/internal/runtimeguard"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
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
	info, err := t.inner.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("read guarded tool info: %w", err)
	}
	toolName := ""
	if info != nil {
		toolName = info.Name
	}
	if err := runtimeguard.StartToolCall(ctx, runtimeguard.ToolCall{
		ID:        compose.GetToolCallID(ctx),
		Name:      toolName,
		Arguments: argumentsInJSON,
	}); err != nil {
		if reason := runtimeguard.DenialReason(err); reason != "" {
			return runtimeToolDenial(toolName, argumentsInJSON, reason)
		}
		return "", err
	}
	result, err := t.inner.InvokableRun(ctx, argumentsInJSON, opts...)
	if err != nil {
		return result, err
	}
	markRuntimeStateChange(ctx, toolName, result)
	return result, nil
}

func guardRuntimeTool(base tool.BaseTool) tool.BaseTool {
	if _, alreadyGuarded := base.(*runtimeGuardedTool); alreadyGuarded {
		return base
	}
	invokable, ok := base.(tool.InvokableTool)
	if !ok {
		return base
	}
	return &runtimeGuardedTool{inner: invokable}
}

func runtimeToolDenial(toolName, argumentsInJSON, reason string) (string, error) {
	switch toolName {
	case "shell":
		var input ShellInput
		if err := json.Unmarshal([]byte(argumentsInJSON), &input); err != nil {
			// A runtime admission denial must remain a model-readable shell result
			// even when the original malformed arguments would have failed tool
			// validation later.
			input = ShellInput{}
		}
		return marshalRuntimeDenial(ShellOutput{
			Command:      input.Command,
			WorkingDir:   input.WorkingDir,
			ExitCode:     commandExitCodeUnavailable,
			Stderr:       "denied: " + reason,
			Denied:       true,
			Decision:     string(DecisionDeny),
			Reason:       reason,
			StopRetrying: true,
			Impact:       ClassifyShellCommand(input.Command),
		})
	case "apply_patch":
		return marshalRuntimeDenial(ApplyPatchOutput{
			Denied:       true,
			Decision:     string(DecisionDeny),
			Reason:       reason,
			StopRetrying: true,
			Impact:       ToolImpactWorkspaceWrite,
		})
	default:
		return marshalRuntimeDenial(struct {
			Denied       bool   `json:"denied"`
			Reason       string `json:"reason"`
			StopRetrying bool   `json:"stop_retrying"`
		}{
			Denied:       true,
			Reason:       reason,
			StopRetrying: true,
		})
	}
}

func marshalRuntimeDenial(output any) (string, error) {
	encoded, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("encode runtime tool denial: %w", err)
	}
	return string(encoded), nil
}

func markRuntimeStateChange(ctx context.Context, toolName, output string) {
	switch toolName {
	case "apply_patch":
		var result ApplyPatchOutput
		if err := json.Unmarshal([]byte(output), &result); err == nil && !result.Denied && len(result.Results) > 0 {
			runtimeguard.MarkStateChanged(ctx)
		}
	case "shell":
		var result ShellOutput
		if err := json.Unmarshal([]byte(output), &result); err != nil || result.Denied || result.ExitCode != 0 {
			return
		}
		if result.Impact == ToolImpactWorkspaceWrite || result.Impact == ToolImpactExternalSideEffect {
			runtimeguard.MarkStateChanged(ctx)
		}
	}
}
