package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"eino-local-assistant/internal/tools"
	"eino-local-assistant/internal/tui"

	"github.com/cloudwego/eino/components/tool"
)

type projectSkillsListResponse struct {
	Skills    []tools.SkillSummary `json:"skills"`
	Truncated bool                 `json:"truncated"`
}

type projectSkillReadResponse struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

// listProjectSkills translates the registry-owned, bounded skill tool into
// TUI-only data. It remains unavailable when invocation tool filtering has
// removed list_skills from this runtime.
func (r *commandRuntime) listProjectSkills(ctx context.Context) (tui.ProjectSkillsCatalog, error) {
	if r == nil || r.registry == nil {
		return tui.ProjectSkillsCatalog{}, errors.New("project skill discovery is unavailable")
	}
	listTool, err := r.projectSkillTool(ctx, "list_skills")
	if err != nil {
		return tui.ProjectSkillsCatalog{}, err
	}
	raw, err := listTool.InvokableRun(ctx, `{}`)
	if err != nil {
		return tui.ProjectSkillsCatalog{}, err
	}
	var response projectSkillsListResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return tui.ProjectSkillsCatalog{}, fmt.Errorf("decode list_skills result: %w", err)
	}
	catalog := tui.ProjectSkillsCatalog{
		Skills:    make([]tui.ProjectSkillSummary, 0, len(response.Skills)),
		Truncated: response.Truncated,
	}
	for _, skill := range response.Skills {
		catalog.Skills = append(catalog.Skills, tui.ProjectSkillSummary{
			Name:        skill.Name,
			Path:        skill.Path,
			Description: skill.Description,
		})
	}
	return catalog, nil
}

// readProjectSkill delegates path/name authorization and byte limiting to the
// default registry's read_skill implementation. The TUI never reads an
// arbitrary local path directly.
func (r *commandRuntime) readProjectSkill(ctx context.Context, name string) (tui.ProjectSkillDetails, error) {
	if r == nil || r.registry == nil {
		return tui.ProjectSkillDetails{}, errors.New("project skill reader is unavailable")
	}
	readTool, err := r.projectSkillTool(ctx, "read_skill")
	if err != nil {
		return tui.ProjectSkillDetails{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return tui.ProjectSkillDetails{}, errors.New("skill name is required")
	}
	input, err := json.Marshal(struct {
		Name string `json:"name"`
	}{Name: name})
	if err != nil {
		return tui.ProjectSkillDetails{}, fmt.Errorf("encode read_skill input: %w", err)
	}
	raw, err := readTool.InvokableRun(ctx, string(input))
	if err != nil {
		return tui.ProjectSkillDetails{}, err
	}
	var response projectSkillReadResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return tui.ProjectSkillDetails{}, fmt.Errorf("decode read_skill result: %w", err)
	}
	return tui.ProjectSkillDetails{
		Name:      response.Name,
		Path:      response.Path,
		Content:   response.Content,
		Bytes:     response.Bytes,
		Truncated: response.Truncated,
	}, nil
}

func (r *commandRuntime) projectSkillTool(ctx context.Context, name string) (tool.InvokableTool, error) {
	if r == nil || r.registry == nil {
		return nil, errors.New("project skill tools are unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, base := range r.registry.All() {
		info, err := base.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("inspect registered tool: %w", err)
		}
		if info == nil || info.Name != name {
			continue
		}
		invokable, ok := base.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("%s is registered but not invokable", name)
		}
		return invokable, nil
	}
	return nil, fmt.Errorf("%s is unavailable for this invocation", name)
}
