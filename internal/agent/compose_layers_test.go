package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeWithLayersIncludesBlocks(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("use go test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ComposeWithLayers("persona", LayerOptions{
		WorkspaceRoot:              ws,
		ProjectInstructionsEnabled: true,
		ProjectInstructionsTokens:  8000,
		MemoryBlock:                FormatMemoryBlock("- **k**: v"),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"persona", "Tool Guidelines", "Project instructions", "use go test", "Persistent memory", "**k**: v"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in prompt", want)
		}
	}
}

func TestComposeWithLayersEmptyMemory(t *testing.T) {
	t.Parallel()
	got, err := ComposeWithLayers("p", LayerOptions{
		ProjectInstructionsEnabled: false,
		MemoryBlock:                "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "## Persistent memory (bounded") {
		t.Fatalf("unexpected memory block section: %q", got)
	}
}
