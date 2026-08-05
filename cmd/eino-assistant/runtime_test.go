package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-local-assistant/internal/agent"
	"eino-local-assistant/internal/config"
)

func TestCaptureStartupCWDReadsTheProcessDirectoryOnce(t *testing.T) {
	var calls int
	got, err := captureStartupCWD(func() (string, error) {
		calls++
		return "/workspace/startup", nil
	})
	if err != nil {
		t.Fatalf("captureStartupCWD() error = %v", err)
	}
	if got != "/workspace/startup" {
		t.Fatalf("startup cwd = %q, want /workspace/startup", got)
	}
	if calls != 1 {
		t.Fatalf("getwd calls = %d, want 1", calls)
	}
}

func TestRuntimeReActOptionsEnableExplicitSteer(t *testing.T) {
	options := runtimeReActOptions(4, nil)
	if !options.EnableSteer {
		t.Fatal("production ReAct options must enable explicit steer")
	}
	if options.MaxStep != 4 {
		t.Fatalf("max steps = %d, want 4", options.MaxStep)
	}
}

func TestCaptureStartupCWDPreservesReaderError(t *testing.T) {
	want := errors.New("getcwd failed")
	_, err := captureStartupCWD(func() (string, error) {
		return "", want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped getcwd error", err)
	}
}

func TestRulesReportUsesCapturedSnapshotAndResumeInvalidation(t *testing.T) {
	calls := 0
	runtime := &commandRuntime{
		cfg: config.Config{},
		composePromptSnapshot: func() (string, agent.PromptLayerSnapshot, error) {
			calls++
			return "frozen prompt", agent.PromptLayerSnapshot{
				Available: true,
				User: agent.PromptLayerBundleSnapshot{
					Available: true,
					Found:     true,
					Path:      "/home/tester/.eino-assistant/AGENTS.md",
					Tokens:    12,
				},
				Project: agent.PromptProjectSnapshot{
					Available: true,
					Found:     true,
					Tokens:    20,
					Sources: []agent.PromptProjectSourceSnapshot{{
						Path:   "/workspace/AGENTS.md",
						Title:  "AGENTS.md",
						Tokens: 20,
					}},
				},
			}, nil
		},
	}
	if _, err := runtime.composeSystemPrompt(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(runtime.rulesReport(), "path=/workspace/AGENTS.md") {
		t.Fatalf("report=%q calls=%d", runtime.rulesReport(), calls)
	}
	runtime.invalidateRulesSnapshot()
	if calls != 1 || !strings.Contains(runtime.rulesReport(), "source metadata unavailable") {
		t.Fatalf("resume report=%q calls=%d", runtime.rulesReport(), calls)
	}
}

func TestSystemPromptComposerUsesWorkspaceToStartupInstructionHierarchy(t *testing.T) {
	workspaceRoot := t.TempDir()
	startupDir := filepath.Join(workspaceRoot, "packages", "cli")
	if err := os.MkdirAll(startupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "AGENTS.md"), []byte("root instruction"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(startupDir, "AGENTS.md"), []byte("startup instruction"), 0o644); err != nil {
		t.Fatal(err)
	}

	compose := newSystemPromptComposer(config.Config{
		Assistant: config.AssistantConfig{SystemPrompt: "persona"},
	}, workspaceRoot, startupDir, "", nil)
	got, err := compose()
	if err != nil {
		t.Fatalf("compose() error = %v", err)
	}
	for _, want := range []string{"persona", "root instruction", "startup instruction"} {
		if !strings.Contains(got, want) {
			t.Fatalf("composed prompt missing %q:\n%s", want, got)
		}
	}
}

func TestResolveUserInstructionsRootUsesHomeWithoutStorageConfig(t *testing.T) {
	got, err := resolveUserInstructionsRoot(func() (string, error) {
		return "/home/tester", nil
	})
	if err != nil {
		t.Fatalf("resolveUserInstructionsRoot() error = %v", err)
	}
	if got != filepath.Join("/home/tester", ".eino-assistant") {
		t.Fatalf("root = %q", got)
	}
}

func TestSystemPromptComposerReloadsGlobalInstructionsForFreshCompose(t *testing.T) {
	workspaceRoot := t.TempDir()
	globalRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(globalRoot, "AGENTS.md"), []byte("old user preference"), 0o644); err != nil {
		t.Fatal(err)
	}
	compose := newSystemPromptComposer(config.Config{
		Assistant: config.AssistantConfig{SystemPrompt: "persona"},
		Rules:     config.RulesConfig{GlobalMaxTokens: 100},
	}, workspaceRoot, workspaceRoot, globalRoot, nil)
	first, err := compose()
	if err != nil {
		t.Fatalf("first compose() error = %v", err)
	}
	if !strings.Contains(first, "old user preference") {
		t.Fatalf("first prompt missing old global instructions: %q", first)
	}
	if err := os.WriteFile(filepath.Join(globalRoot, "AGENTS.md"), []byte("new user preference"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := compose()
	if err != nil {
		t.Fatalf("second compose() error = %v", err)
	}
	if !strings.Contains(second, "new user preference") || strings.Contains(second, "old user preference") {
		t.Fatalf("second prompt did not reload global instructions: %q", second)
	}
}
