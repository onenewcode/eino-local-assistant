package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Registry holds the executable tools exposed to the ReAct agent.
type Registry struct {
	tools []tool.BaseTool
}

// DefaultOptions configures built-in tool registration.
type DefaultOptions struct {
	// Clock defaults to time.Now when nil; inject a fixed clock in tests.
	Clock func() time.Time
	// RunCommand configures the local shell tool. Zero value enables defaults.
	RunCommand RunCommandOptions
}

// Default registers the built-in tools for the local assistant.
// Optional toolsCfg uses the first ToolsConfig-compatible options when provided
// via DefaultOptions; callers typically pass DefaultOptions{Clock, RunCommand}.
func Default(clock func() time.Time, runCommand ...RunCommandOptions) (*Registry, error) {
	opts := DefaultOptions{Clock: clock}
	if len(runCommand) > 0 {
		opts.RunCommand = runCommand[0]
	}
	return DefaultWithOptions(opts)
}

// DefaultWithOptions registers built-in tools from an explicit options struct.
func DefaultWithOptions(opts DefaultOptions) (*Registry, error) {
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}

	timeTool, err := NewGetCurrentTime(clock)
	if err != nil {
		return nil, fmt.Errorf("create get_current_time tool: %w", err)
	}
	artifactTool, err := NewReadArtifact()
	if err != nil {
		return nil, fmt.Errorf("create read_artifact tool: %w", err)
	}

	registered := []tool.BaseTool{timeTool, artifactTool}
	if !opts.RunCommand.Disabled {
		commandTool, err := NewRunCommand(opts.RunCommand)
		if err != nil {
			return nil, fmt.Errorf("create run_command tool: %w", err)
		}
		registered = append(registered, commandTool)
	}
	return New(registered...), nil
}

// New builds a registry from an explicit tool list.
func New(tools ...tool.BaseTool) *Registry {
	copied := make([]tool.BaseTool, 0, len(tools))
	copied = append(copied, tools...)
	return &Registry{tools: copied}
}

// All returns the registered tools for ToolsNode / ReAct configuration.
func (r *Registry) All() []tool.BaseTool {
	if r == nil {
		return nil
	}
	out := make([]tool.BaseTool, len(r.tools))
	copy(out, r.tools)
	return out
}

// Infos returns tool metadata for model binding and diagnostics.
func (r *Registry) Infos(ctx context.Context) ([]*schema.ToolInfo, error) {
	if r == nil {
		return nil, nil
	}
	infos := make([]*schema.ToolInfo, 0, len(r.tools))
	for i, t := range r.tools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("tool info at index %d: %w", i, err)
		}
		infos = append(infos, info)
	}
	return infos, nil
}
