package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGlobFilesFindsBoundedMatches(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"main.go":         "package main",
		"internal/app.go": "package app",
		"README.md":       "readme",
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
	tool, err := NewGlobFiles(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"pattern":"**/*.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	var output GlobFilesOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Matches) != 2 || output.Matches[0].Path != "internal/app.go" || output.Matches[1].Path != "main.go" {
		t.Fatalf("glob output = %+v", output)
	}
}

func TestGlobFilesHidesAndTruncates(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{".env", "one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool, err := NewGlobFiles(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"pattern":"*","max_results":1}`)
	if err != nil {
		t.Fatal(err)
	}
	var output GlobFilesOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if !output.Truncated || len(output.Matches) != 1 || output.Matches[0].Path == ".env" {
		t.Fatalf("glob output = %+v", output)
	}
}

func TestGlobFilesRejectsInvalidAndOutsidePaths(t *testing.T) {
	root := t.TempDir()
	tool, err := NewGlobFiles(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"pattern":"["}`, `{"pattern":"*","path":"../"}`} {
		if _, err := tool.InvokableRun(context.Background(), payload); err == nil {
			t.Fatalf("payload %s should fail", payload)
		}
	}
}
