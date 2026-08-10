package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGitStatusParsesIndexWorktreeAndUntracked(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	runGit(t, root, "config", "user.name", "test")
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(tracked, []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusTool, err := NewGitStatus(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := statusTool.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	var output GitStatusOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Entries) != 2 {
		t.Fatalf("entries = %+v", output.Entries)
	}
	seen := map[string]GitStatusEntry{}
	for _, entry := range output.Entries {
		seen[entry.Path] = entry
	}
	if got := seen["tracked.txt"]; got.Index != " " || got.Worktree != "M" || got.Untracked {
		t.Fatalf("tracked status = %+v", got)
	}
	if got := seen["new.txt"]; !got.Untracked || got.Index != "?" || got.Worktree != "?" {
		t.Fatalf("untracked status = %+v", got)
	}
}

func TestGitStatusRejectsWorkspaceEscape(t *testing.T) {
	statusTool, err := NewGitStatus(EditFileOptions{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := statusTool.InvokableRun(context.Background(), `{"path":"../outside"}`); err == nil {
		t.Fatal("expected path escape error")
	}
}

func TestParseGitStatusHandlesRenameAndConflict(t *testing.T) {
	data := []byte("R  renamed.txt\x00original.txt\x00UU conflict.txt\x00")
	entries := parseGitStatus(data)
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if got := entries[0]; got.Path != "renamed.txt" || got.OriginalPath != "original.txt" || !got.Renamed || got.Conflicted {
		t.Fatalf("rename entry = %+v", got)
	}
	if got := entries[1]; got.Path != "conflict.txt" || !got.Conflicted {
		t.Fatalf("conflict entry = %+v", got)
	}
}
