package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestListFilesBoundsDepthAndHidesDotFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"README.md":       "readme",
		"src/main.go":     "package main",
		"src/nested/deep": "deep",
		".env":            "secret",
		".git/config":     "internal",
	} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool, err := NewListFiles(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"depth":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var output ListFilesOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.Root != "." || output.Truncated || len(output.Entries) != 2 {
		t.Fatalf("listing = %+v", output)
	}
	for _, entry := range output.Entries {
		if entry.Path == ".env" || entry.Path == ".git" || entry.Path == "src/nested" {
			t.Fatalf("unexpected hidden/deep entry: %+v", entry)
		}
	}
}

func TestListFilesIncludesHiddenAndTruncates(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".env", "one", "two"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool, err := NewListFiles(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"include_hidden":true,"max_entries":2}`)
	if err != nil {
		t.Fatal(err)
	}
	var output ListFilesOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if !output.Truncated || len(output.Entries) != 2 {
		t.Fatalf("truncated listing = %+v", output)
	}
}

func TestListFilesRejectsOutsidePathAndDoesNotFollowSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool, err := NewListFiles(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"path":"../"}`); err == nil {
		t.Fatal("outside path should be rejected")
	}
	raw, err := tool.InvokableRun(context.Background(), `{"include_hidden":true}`)
	if err != nil {
		t.Fatal(err)
	}
	var output ListFilesOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Entries) != 1 || output.Entries[0].Type != "symlink" {
		t.Fatalf("symlink listing = %+v", output)
	}
}
