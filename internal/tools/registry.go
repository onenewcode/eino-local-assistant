package tools

import (
	"context"
	"fmt"
	"sort"
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

// ToolSelection restricts the model-visible tools for one process invocation.
// It intentionally does not represent permission, command-policy, or sandbox
// rules: those boundaries still govern every selected tool call.
type ToolSelection struct {
	Allowed    []string
	AllowedSet bool
	Disallowed []string
}

// DefaultOptions configures built-in tool registration.
// Codex-oriented tool surface: shell + apply_patch, bounded project skills,
// and product helpers.
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
//   - list_skills / read_skill — on-demand project workflows (read-only)
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
	skillOpts := SkillOptions{WorkingDir: shellOpts.WorkspaceRoot}
	listSkillsTool, err := NewListSkills(skillOpts)
	if err != nil {
		return nil, fmt.Errorf("create list_skills tool: %w", err)
	}
	readSkillTool, err := NewReadSkill(skillOpts)
	if err != nil {
		return nil, fmt.Errorf("create read_skill tool: %w", err)
	}
	registered = append(registered, listSkillsTool, readSkillTool)

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
// built-ins. It is intended for product-owned tools such as update_plan that
// are registered after construction.
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

// Filter returns a registry restricted by an invocation-scoped selection.
// Unset Allowed keeps every registered tool, while an explicitly empty Allowed
// set exposes none. A sole "default" or "*" Allowed value restores the full
// registry. Exact Disallowed names always take precedence over Allowed names.
func (r *Registry) Filter(ctx context.Context, selection ToolSelection) (*Registry, error) {
	if r == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	allowed := normalizeToolNames(selection.Allowed)
	disallowed := normalizeToolNames(selection.Disallowed)
	if selection.AllowedSet && containsToolDefault(allowed) {
		if len(allowed) != 1 {
			return nil, fmt.Errorf("--tools accepts default or * only as its sole value")
		}
		selection.AllowedSet = false
		allowed = nil
	}

	infos, err := r.Infos(ctx)
	if err != nil {
		return nil, err
	}
	available := make(map[string]tool.BaseTool, len(infos))
	for index, info := range infos {
		if info == nil || strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("tool info at index %d has no name", index)
		}
		if _, exists := available[info.Name]; exists {
			return nil, fmt.Errorf("duplicate tool name %q", info.Name)
		}
		available[info.Name] = r.tools[index]
	}
	if err := validateRequestedToolNames(available, append(append([]string(nil), allowed...), disallowed...)); err != nil {
		return nil, err
	}

	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	disallowedSet := make(map[string]struct{}, len(disallowed))
	for _, name := range disallowed {
		disallowedSet[name] = struct{}{}
	}
	filtered := make([]tool.BaseTool, 0, len(r.tools))
	for index, info := range infos {
		if _, denied := disallowedSet[info.Name]; denied {
			continue
		}
		if selection.AllowedSet {
			if _, included := allowedSet[info.Name]; !included {
				continue
			}
		}
		filtered = append(filtered, r.tools[index])
	}
	return New(filtered...), nil
}

func containsToolDefault(names []string) bool {
	for _, name := range names {
		if name == "default" || name == "*" {
			return true
		}
	}
	return false
}

func normalizeToolNames(values []string) []string {
	names := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	return names
}

func validateRequestedToolNames(available map[string]tool.BaseTool, requested []string) error {
	for _, name := range requested {
		if _, exists := available[name]; exists {
			continue
		}
		availableNames := make([]string, 0, len(available))
		for availableName := range available {
			availableNames = append(availableNames, availableName)
		}
		sort.Strings(availableNames)
		return fmt.Errorf("unknown tool %q (available: %s)", name, strings.Join(availableNames, ", "))
	}
	return nil
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
