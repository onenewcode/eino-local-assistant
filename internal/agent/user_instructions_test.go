package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"eino-local-assistant/internal/usage"
)

func TestLoadUserInstructionsMissing(t *testing.T) {
	b, err := LoadUserInstructions(t.TempDir(), 100)
	if err != nil {
		t.Fatalf("LoadUserInstructions: %v", err)
	}
	if b.Found || FormatUserInstructionsBlock(b) != "" {
		t.Fatalf("bundle=%+v block=%q", b, FormatUserInstructionsBlock(b))
	}
}

func TestLoadUserInstructionsOverridePrecedenceAndProvenance(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, agentsFile)
	override := filepath.Join(root, agentsOverrideFile)
	if err := os.WriteFile(base, []byte("base user instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("override user instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadUserInstructions(root, 1000)
	if err != nil {
		t.Fatalf("LoadUserInstructions: %v", err)
	}
	if !b.Found || b.Path != override || b.Text != "override user instructions" {
		t.Fatalf("bundle=%+v", b)
	}
	block := FormatUserInstructionsBlock(b)
	if !strings.Contains(block, "## User instructions (AGENTS.override.md)") ||
		strings.Contains(block, "base user instructions") {
		t.Fatalf("block=%q", block)
	}
}

func TestLoadUserInstructionsEmptyOverrideFallsBackToBase(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, agentsFile), []byte("base user instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, agentsOverrideFile), []byte("\xef\xbb\xbf \n\t"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadUserInstructions(root, 1000)
	if err != nil {
		t.Fatalf("LoadUserInstructions: %v", err)
	}
	if !b.Found || filepath.Base(b.Path) != agentsFile || b.Text != "base user instructions" {
		t.Fatalf("bundle=%+v", b)
	}
}

func TestLoadUserInstructionsSkipsNonRegularOverride(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, agentsOverrideFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, agentsFile), []byte("base user instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadUserInstructions(root, 1000)
	if err != nil {
		t.Fatalf("LoadUserInstructions: %v", err)
	}
	if filepath.Base(b.Path) != agentsFile || b.Text != "base user instructions" {
		t.Fatalf("bundle=%+v", b)
	}
}

func TestLoadUserInstructionsFollowsRegularSymlinkAndKeepsCandidatePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are not portable on Windows")
	}
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "global.md")
	if err := os.WriteFile(target, []byte("linked user instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, agentsOverrideFile)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	b, err := LoadUserInstructions(root, 1000)
	if err != nil {
		t.Fatalf("LoadUserInstructions: %v", err)
	}
	if b.Path != path || b.Text != "linked user instructions" {
		t.Fatalf("bundle=%+v, want candidate path %q", b, path)
	}
}

func TestLoadUserInstructionsReadErrorsFailFast(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based read errors are not portable on Windows")
	}
	root := t.TempDir()
	path := filepath.Join(root, agentsFile)
	if err := os.WriteFile(path, []byte("private user instructions"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
	_, err := LoadUserInstructions(root, 100)
	if err == nil {
		t.Skip("test process can read mode-000 files")
	}
	if !strings.Contains(err.Error(), "read AGENTS.md") {
		t.Fatalf("error=%v, want read failure", err)
	}
}

func TestLoadUserInstructionsUnicodeBudgetIsRuneSafe(t *testing.T) {
	root := t.TempDir()
	body := strings.Repeat("规则😀 ", 100)
	if err := os.WriteFile(filepath.Join(root, agentsFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadUserInstructions(root, 40)
	if err != nil {
		t.Fatalf("LoadUserInstructions: %v", err)
	}
	block := FormatUserInstructionsBlock(b)
	if !b.Truncated || usage.EstimateText(block) > 40 || b.Tokens != usage.EstimateText(block) {
		t.Fatalf("bundle=%+v blockTokens=%d block=%q", b, usage.EstimateText(block), block)
	}
	if !strings.Contains(block, "规则") {
		t.Fatalf("unicode body was not retained: %q", block)
	}
}

func TestComposeWithLayersOrdersGlobalBeforeProjectWithIndependentBudgets(t *testing.T) {
	globalRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	globalBody := strings.Repeat("global rule ", 80)
	projectBody := strings.Repeat("project rule ", 80)
	if err := os.WriteFile(filepath.Join(globalRoot, agentsFile), []byte(globalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, agentsFile), []byte(projectBody), 0o644); err != nil {
		t.Fatal(err)
	}
	globalBudget := 35
	projectBudget := 45
	got, err := ComposeWithLayers("persona", LayerOptions{
		WorkspaceRoot:              workspaceRoot,
		UserInstructionsRoot:       globalRoot,
		UserInstructionsTokens:     globalBudget,
		ProjectInstructionsEnabled: true,
		ProjectInstructionsTokens:  projectBudget,
	})
	if err != nil {
		t.Fatal(err)
	}
	globalIndex := strings.Index(got, "## User instructions")
	projectIndex := strings.Index(got, "## Project instructions")
	if globalIndex < 0 || projectIndex < 0 || globalIndex > projectIndex {
		t.Fatalf("layer order global=%d project=%d prompt=%q", globalIndex, projectIndex, got)
	}
	globalEnd := strings.Index(got[globalIndex:], "## Project instructions") + globalIndex
	globalBlock := got[globalIndex:globalEnd]
	projectBlock := got[projectIndex:]
	if usage.EstimateText(strings.TrimSpace(globalBlock)) > globalBudget || usage.EstimateText(strings.TrimSpace(projectBlock)) > projectBudget {
		t.Fatalf("budgets exceeded global=%d project=%d", usage.EstimateText(globalBlock), usage.EstimateText(projectBlock))
	}
}

func TestComposeWithLayersRulesDisabledSkipsBothInstructionLayers(t *testing.T) {
	globalRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalRoot, agentsFile), []byte("global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, agentsFile), []byte("project rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ComposeWithLayers("persona", LayerOptions{
		WorkspaceRoot:              workspaceRoot,
		UserInstructionsRoot:       globalRoot,
		ProjectInstructionsEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "global rule") || strings.Contains(got, "project rule") {
		t.Fatalf("disabled rules still injected instructions: %q", got)
	}
}
