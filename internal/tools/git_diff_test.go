package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitDiffReturnsWorkingTreeDiff(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	runGit(t, root, "config", "user.name", "test")
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "main.go")
	runGit(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(path, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diffTool, err := NewGitDiff(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := diffTool.InvokableRun(context.Background(), `{"path":"main.go"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	var output GitDiffOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.Path != "main.go" || output.Staged || output.Truncated || !strings.Contains(output.Diff, "-before") || !strings.Contains(output.Diff, "+after") {
		t.Fatalf("output = %+v", output)
	}
}

func TestGitDiffRejectsWorkspaceEscape(t *testing.T) {
	diffTool, err := NewGitDiff(EditFileOptions{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := diffTool.InvokableRun(context.Background(), `{"path":"../outside"}`); err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestGitDiffReturnsMergeBaseDiff(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	runGit(t, root, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "main.go")
	runGit(t, root, "commit", "-m", "base")
	runGit(t, root, "branch", "review-base")
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "commit", "-am", "change")
	diffTool, err := NewGitDiff(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := diffTool.InvokableRun(context.Background(), `{"base":"review-base"}`)
	if err != nil {
		t.Fatal(err)
	}
	var output GitDiffOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.Base != "review-base" || !strings.Contains(output.Diff, "+after") || !strings.Contains(output.Diff, "-before") {
		t.Fatalf("output=%+v", output)
	}
}

func TestGitDiffRejectsUnsafeBase(t *testing.T) {
	diffTool, err := NewGitDiff(EditFileOptions{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"base":"--output=/tmp/file"}`, `{"base":"main other"}`, `{"base":"main","staged":true}`} {
		if _, err := diffTool.InvokableRun(context.Background(), payload); err == nil {
			t.Fatalf("payload %s should fail", payload)
		}
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
