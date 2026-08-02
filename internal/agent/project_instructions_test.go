package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectInstructionsMissing(t *testing.T) {
	t.Parallel()
	b, err := LoadProjectInstructions(t.TempDir(), 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if b.Found {
		t.Fatal("expected not found")
	}
	if FormatProjectInstructionsBlock(b) != "" {
		t.Fatal("expected empty block")
	}
}

func TestLoadProjectInstructionsTruncate(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	body := strings.Repeat("rule line about builds and tests\n", 500)
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadProjectInstructions(ws, 50)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || !b.Truncated {
		t.Fatalf("found=%v truncated=%v tokens=%d", b.Found, b.Truncated, b.Tokens)
	}
	block := FormatProjectInstructionsBlock(b)
	if !strings.Contains(block, "Project instructions") {
		t.Fatalf("block: %q", block)
	}
}
