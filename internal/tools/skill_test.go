package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillsDiscoverAndReadOnDemand(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".claude", "skills", "review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Review workflow\n\nInspect changes carefully.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listTool, err := NewListSkills(SkillOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := listTool.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Skills []SkillSummary `json:"skills"`
	}
	if err := json.Unmarshal([]byte(raw), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Skills) != 1 || listed.Skills[0].Name != "review" || listed.Skills[0].Description != "Review workflow" {
		t.Fatalf("skills = %+v", listed.Skills)
	}
	readTool, err := NewReadSkill(SkillOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = readTool.InvokableRun(context.Background(), `{"name":"review","max_bytes":10}`)
	if err != nil {
		t.Fatal(err)
	}
	var read readSkillOutput
	if err := json.Unmarshal([]byte(raw), &read); err != nil {
		t.Fatal(err)
	}
	if !read.Truncated || read.Bytes != 10 || !strings.HasPrefix(read.Content, "# Review") {
		t.Fatalf("read skill = %+v", read)
	}
}

func TestReadSkillRejectsUnknownAndInvalidLimit(t *testing.T) {
	tool, err := NewReadSkill(SkillOptions{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{}`, `{"name":"../../secret"}`, `{"name":"x","max_bytes":65537}`} {
		if _, err := tool.InvokableRun(context.Background(), payload); err == nil {
			t.Fatalf("payload %s should fail", payload)
		}
	}
}

func TestSkillsPreferEarlierConventionalRoot(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".eino-assistant/skills/build", "skills/build"} {
		path := filepath.Join(root, dir, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("# "+dir), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skills, err := discoverSkills(root)
	if err != nil || len(skills) != 1 || !strings.Contains(skills[0].Path, ".eino-assistant") {
		t.Fatalf("skills=%+v err=%v", skills, err)
	}
}
