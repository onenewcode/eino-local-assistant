package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackgroundAgentWorkspaceFilesAreBoundedAndWorkspaceScoped(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "one.go"), []byte("package one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "two.go"), []byte(strings.Repeat("x", maxBackgroundAgentFileBytes+32)), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &commandRuntime{workspaceRoot: root}
	snapshot, err := runtime.backgroundAgentWorkspaceFiles(context.Background(), []string{"one.go", "internal/two.go"})
	if err != nil || !strings.Contains(snapshot, "[FILE one.go; encoding=utf-8; bytes=12]") ||
		!strings.Contains(snapshot, "package one") || !strings.Contains(snapshot, "[File content truncated by the bounded reader.]") {
		t.Fatalf("snapshot=%q err=%v", snapshot, err)
	}
	if len(snapshot) > maxBackgroundAgentFilesTotalSize {
		t.Fatalf("snapshot exceeded total limit: %d", len(snapshot))
	}
}

func TestBackgroundAgentWorkspaceFilesRejectsUnsafeSources(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &commandRuntime{workspaceRoot: root}
	if _, err := runtime.backgroundAgentWorkspaceFiles(context.Background(), []string{".git/config"}); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("hidden Git path error = %v", err)
	}
	if _, err := runtime.backgroundAgentWorkspaceFiles(context.Background(), []string{"../outside"}); err == nil || !strings.Contains(err.Error(), "path must") {
		t.Fatalf("outside path error = %v", err)
	}
	if _, err := (&commandRuntime{}).backgroundAgentWorkspaceFiles(context.Background(), []string{"one.go"}); !errors.Is(err, errBackgroundAgentWorkspaceUnavailable) {
		t.Fatalf("missing workspace error = %v", err)
	}
}
