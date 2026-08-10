package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitShowReturnsCommitPatch(t *testing.T) {
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
	runGit(t, root, "add", "main.go")
	runGit(t, root, "commit", "-m", "update")
	showTool, err := NewGitShow(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := showTool.InvokableRun(context.Background(), `{"commit":"HEAD","path":"main.go"}`)
	if err != nil {
		t.Fatal(err)
	}
	var output GitShowOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.Commit != "HEAD" || output.Path != "main.go" || output.Truncated || !strings.Contains(output.Content, "+after") || !strings.Contains(output.Content, "-before") {
		t.Fatalf("git show output = %+v", output)
	}
}

func TestGitShowRejectsUnsafeInputs(t *testing.T) {
	tool, err := NewGitShow(EditFileOptions{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{}`, `{"commit":"--format=%H"}`, `{"commit":"HEAD","path":"../outside"}`, `{"commit":"HEAD","max_bytes":1048577}`} {
		if _, err := tool.InvokableRun(context.Background(), payload); err == nil {
			t.Fatalf("payload %s should fail", payload)
		}
	}
}
