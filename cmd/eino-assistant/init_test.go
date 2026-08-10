package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCommandCreatesDefaultAGENTSFileAndRefusesOverwrite(t *testing.T) {
	workspace := t.TempDir()
	t.Chdir(workspace)
	stdout, _, err := executeForTest("init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	target := filepath.Join(workspace, "AGENTS.md")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if string(content) != defaultAgentInstructions || !strings.Contains(stdout, target) {
		t.Fatalf("init output=%q content=%q", stdout, content)
	}
	_, _, err = executeForTest("init")
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second init error = %v", err)
	}
}

func TestInitCommandSupportsExplicitPathAndRejectsInvalidParent(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "nested", "AGENTS.md")
	if err := os.Mkdir(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeForTest("init", target)
	if err != nil || !strings.Contains(stdout, target) {
		t.Fatalf("init explicit path stdout=%q err=%v", stdout, err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("stat explicit AGENTS.md: %v", err)
	}
	_, _, err = executeForTest("init", filepath.Join(workspace, "missing", "AGENTS.md"))
	if err == nil || !strings.Contains(err.Error(), "inspect instruction directory") {
		t.Fatalf("init missing parent error = %v", err)
	}
}
