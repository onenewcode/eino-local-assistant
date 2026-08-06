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

func TestComposeWithLayersUsesProjectInstructionsStartDir(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	start := filepath.Join(ws, "nested")
	if err := os.Mkdir(start, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("root rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(start, agentsFile), []byte("nested rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ComposeWithLayers("persona", LayerOptions{
		WorkspaceRoot:               ws,
		ProjectInstructionsStartDir: start,
		ProjectInstructionsEnabled:  true,
		ProjectInstructionsTokens:   8000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(got, "root rule") >= strings.Index(got, "nested rule") {
		t.Fatalf("project instructions are not root-first: %q", got)
	}
}

func TestComposeWithLayersUsesConfiguredProjectFallback(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("fallback project rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt, snapshot, err := ComposeWithLayersSnapshot("persona", LayerOptions{
		WorkspaceRoot:                        ws,
		ProjectInstructionsEnabled:           true,
		ProjectInstructionsTokens:            8000,
		ProjectInstructionsFallbackFilenames: []string{"CLAUDE.md"},
	})
	if err != nil {
		t.Fatalf("ComposeWithLayersSnapshot: %v", err)
	}
	if !strings.Contains(prompt, "fallback project rule") {
		t.Fatalf("prompt missing fallback rule: %q", prompt)
	}
	if len(snapshot.Project.Sources) != 1 || snapshot.Project.Sources[0].Title != "CLAUDE.md" {
		t.Fatalf("project snapshot=%+v, want fallback provenance", snapshot.Project)
	}
}

func TestComposeWithLayersSnapshotCapturesMetadataWithoutText(t *testing.T) {
	t.Parallel()
	globalRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	start := filepath.Join(workspaceRoot, "nested")
	if err := os.Mkdir(start, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalRoot, agentsFile), []byte("global rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, agentsFile), []byte("root rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(start, agentsFile), []byte("nested rule"), 0o644); err != nil {
		t.Fatal(err)
	}

	prompt, snapshot, err := ComposeWithLayersSnapshot("persona", LayerOptions{
		WorkspaceRoot:               workspaceRoot,
		UserInstructionsRoot:        globalRoot,
		ProjectInstructionsStartDir: start,
		ProjectInstructionsEnabled:  true,
		UserInstructionsTokens:      1000,
		ProjectInstructionsTokens:   1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Available || !snapshot.User.Available || !snapshot.User.Found {
		t.Fatalf("snapshot user availability = %+v", snapshot.User)
	}
	if snapshot.User.Path != filepath.Join(globalRoot, agentsFile) || snapshot.User.Tokens == 0 {
		t.Fatalf("user snapshot = %+v", snapshot.User)
	}
	if !snapshot.Project.Available || !snapshot.Project.Found || len(snapshot.Project.Sources) != 2 {
		t.Fatalf("project snapshot = %+v", snapshot.Project)
	}
	if snapshot.Project.Sources[0].Title != "AGENTS.md" || snapshot.Project.Sources[1].Title != "nested/AGENTS.md" {
		t.Fatalf("project source order = %+v", snapshot.Project.Sources)
	}
	if snapshot.Project.Sources[0].Tokens == 0 || snapshot.Project.Sources[1].Tokens == 0 {
		t.Fatalf("project source tokens = %+v", snapshot.Project.Sources)
	}
	if strings.Contains(snapshot.User.Path+snapshot.Project.Sources[0].Title, "global rule") || !strings.Contains(prompt, "nested rule") {
		t.Fatalf("snapshot unexpectedly carries body or prompt misses body: %+v / %q", snapshot, prompt)
	}
}

func TestComposeWithLayersSnapshotReportsNoSources(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	_, snapshot, err := ComposeWithLayersSnapshot("persona", LayerOptions{
		WorkspaceRoot:              workspaceRoot,
		ProjectInstructionsEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Available || !snapshot.Project.Available || snapshot.Project.Found || len(snapshot.Project.Sources) != 0 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
