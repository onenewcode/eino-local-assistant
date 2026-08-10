package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-local-assistant/internal/tools"
)

func TestCommandRuntimeProjectSkillsUseFilteredRegistryTools(t *testing.T) {
	workspace := t.TempDir()
	skillPath := filepath.Join(workspace, ".agents", "skills", "release", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillPath, []byte("# Release\n\nRun all release checks.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := tools.DefaultWithOptions(tools.DefaultOptions{
		Shell:      tools.ShellOptions{WorkspaceRoot: workspace},
		ApplyPatch: tools.ApplyPatchOptions{WorkspaceRoot: workspace, Approval: tools.ApprovalNever},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &commandRuntime{registry: registry}
	catalog, err := runtime.listProjectSkills(context.Background())
	if err != nil {
		t.Fatalf("listProjectSkills: %v", err)
	}
	if len(catalog.Skills) != 1 || catalog.Skills[0].Name != "release" || catalog.Skills[0].Path != ".agents/skills/release/SKILL.md" {
		t.Fatalf("catalog = %#v", catalog)
	}
	details, err := runtime.readProjectSkill(context.Background(), "release")
	if err != nil {
		t.Fatalf("readProjectSkill: %v", err)
	}
	if details.Name != "release" || !strings.Contains(details.Content, "Run all release checks.") {
		t.Fatalf("details = %#v", details)
	}

	runtime.registry = tools.New()
	if _, err := runtime.listProjectSkills(context.Background()); err == nil || !strings.Contains(err.Error(), "unavailable for this invocation") {
		t.Fatalf("filtered listProjectSkills error = %v", err)
	}
	if _, err := runtime.readProjectSkill(context.Background(), "release"); err == nil || !strings.Contains(err.Error(), "unavailable for this invocation") {
		t.Fatalf("filtered readProjectSkill error = %v", err)
	}
}
