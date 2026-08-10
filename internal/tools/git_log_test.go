package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitLogReturnsBoundedHistory(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	runGit(t, root, "config", "user.name", "test")
	path := filepath.Join(root, "main.go")
	for _, subject := range []string{"first", "second", "third"} {
		if err := os.WriteFile(path, []byte(subject+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, root, "add", "main.go")
		runGit(t, root, "commit", "-m", subject)
	}
	logTool, err := NewGitLog(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := logTool.InvokableRun(context.Background(), `{"path":"main.go","max_commits":2}`)
	if err != nil {
		t.Fatal(err)
	}
	var output GitLogOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.Path != "main.go" || len(output.Commits) != 2 || !output.Truncated || output.Commits[0].Subject != "third" {
		t.Fatalf("git log output = %+v", output)
	}
}

func TestGitLogRejectsEscapeAndInvalidLimit(t *testing.T) {
	tool, err := NewGitLog(EditFileOptions{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"path":"../outside"}`, `{"max_commits":101}`} {
		if _, err := tool.InvokableRun(context.Background(), payload); err == nil {
			t.Fatalf("payload %s should fail", payload)
		}
	}
}

func TestParseGitLogSkipsMalformedLines(t *testing.T) {
	commits := parseGitLog([]byte("bad\nabc\x1fAlice\x1f2026-01-01T00:00:00Z\x1fsubject\n"))
	if len(commits) != 1 || !strings.Contains(commits[0].Subject, "subject") {
		t.Fatalf("parsed commits = %+v", commits)
	}
}
