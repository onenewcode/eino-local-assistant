package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitRestoreRequiresApprovalAndRestoresOneFile(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.invalid")
	runGit(t, root, "config", "user.name", "test")
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "file.txt")
	runGit(t, root, "commit", "-m", "initial")
	if err := os.WriteFile(path, []byte("discard me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore, err := NewGitRestore(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPermissionHandler(context.Background(), func(_ context.Context, request PermissionRequest) (bool, error) {
		if request.Tool != "git_restore" || request.Action != "restore_worktree" || request.Detail != "file.txt" {
			t.Fatalf("request = %+v", request)
		}
		if !strings.Contains(request.Preview, "-original") || !strings.Contains(request.Preview, "+discard me") {
			t.Fatalf("preview = %q", request.Preview)
		}
		return true, nil
	})
	raw, err := restore.InvokableRun(ctx, `{"path":"file.txt"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	var output GitRestoreOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.Path != "file.txt" || output.Staged || !strings.Contains(output.Preview, "-original") {
		t.Fatalf("output = %+v", output)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "original\n" {
		t.Fatalf("restored file = %q", data)
	}
}

func TestGitRestoreDenialLeavesFileUntouched(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restore, err := NewGitRestore(EditFileOptions{WorkingDir: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithPermissionHandler(context.Background(), func(context.Context, PermissionRequest) (bool, error) { return false, nil })
	if _, err := restore.InvokableRun(ctx, `{"path":"file.txt"}`); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("error = %v, want ErrPermissionDenied", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "changed\n" {
		t.Fatalf("file changed after denial: %q", data)
	}
}

func TestGitRestoreRejectsEmptyPath(t *testing.T) {
	restore, err := NewGitRestore(EditFileOptions{WorkingDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restore.InvokableRun(context.Background(), `{}`); err == nil {
		t.Fatal("expected empty path error")
	}
}
