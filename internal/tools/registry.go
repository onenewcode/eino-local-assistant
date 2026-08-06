package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"eino-local-assistant/internal/memory"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Registry holds the executable tools exposed to the ReAct agent.
type Registry struct {
	tools []tool.BaseTool
}

// DefaultOptions configures built-in tool registration.
// Codex-oriented subset: shell + apply_patch, plus product helpers.
type DefaultOptions struct {
	Clock      func() time.Time
	Shell      ShellOptions
	ApplyPatch ApplyPatchOptions
	// MemoryStore enables read-only memory_list / memory_search / memory_read.
	MemoryStore *memory.Store
}

// DefaultWithOptions registers the Codex-subset tool surface:
//   - shell — terminal / process (Codex shell)
//   - apply_patch — structured file create/update/delete (Codex apply_patch subset)
//   - get_current_time — host wall clock (product)
//   - read_artifact — thread-scoped evidence (product)
//   - memory_* — project-scoped persistent memory reads (product)
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
	if opts.MemoryStore != nil {
		memTools, err := NewMemoryTools(opts.MemoryStore)
		if err != nil {
			return nil, err
		}
		registered = append(registered, memTools...)
	}

	patchOpts := opts.ApplyPatch
	sharedApprovalState := opts.Shell.ApprovalState
	if sharedApprovalState == nil {
		sharedApprovalState = opts.ApplyPatch.ApprovalState
	}
	patchOpts.ApprovalState = sharedApprovalState
	if strings.TrimSpace(patchOpts.WorkspaceRoot) == "" && strings.TrimSpace(opts.Shell.WorkspaceRoot) != "" {
		patchOpts.WorkspaceRoot = opts.Shell.WorkspaceRoot
	}
	if patchOpts.Approver == nil {
		patchOpts.Approver = opts.Shell.Approver
	}
	if patchOpts.SessionAllows == nil {
		patchOpts.SessionAllows = opts.Shell.SessionAllows
	}
	if patchOpts.SessionDenies == nil {
		patchOpts.SessionDenies = opts.Shell.SessionDenies
	}
	if patchOpts.Approval == "" {
		patchOpts.Approval = opts.Shell.Approval
	}
	// Side-effecting tools share the same OS boundary by default. This keeps a
	// caller that configures only one options struct from silently leaving the
	// other tool on the host.
	if patchOpts.Sandbox == nil {
		patchOpts.Sandbox = opts.Shell.Sandbox
	}
	shellOpts := opts.Shell
	shellOpts.ApprovalState = sharedApprovalState
	if shellOpts.Sandbox == nil {
		shellOpts.Sandbox = patchOpts.Sandbox
	}

	if !patchOpts.Disabled {
		patchTool, err := NewApplyPatch(patchOpts)
		if err != nil {
			return nil, fmt.Errorf("create apply_patch tool: %w", err)
		}
		registered = append(registered, patchTool)
	}

	if !shellOpts.Disabled {
		shellTool, err := NewShell(shellOpts)
		if err != nil {
			return nil, fmt.Errorf("create shell tool: %w", err)
		}
		registered = append(registered, shellTool)
	}
	return New(registered...), nil
}

// New builds a registry from an explicit tool list. Invokable tools are
// runtime-guarded once so product-owned additions cannot bypass turn budgets.
func New(tools ...tool.BaseTool) *Registry {
	registry := &Registry{}
	registry.Append(tools...)
	return registry
}

// Append adds tools to an existing registry through the same runtime guard as
// built-ins. It is intended for product-owned tools such as task_plan,
// task_progress, and task_complete that are registered after construction.
// Passing an already guarded tool is idempotent and does not double-charge its
// runtime budget.
func (r *Registry) Append(tools ...tool.BaseTool) {
	if r == nil {
		return
	}
	for _, base := range tools {
		r.tools = append(r.tools, guardRuntimeTool(base))
	}
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
