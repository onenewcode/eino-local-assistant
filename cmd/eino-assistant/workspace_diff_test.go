package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestReadWorkspaceDiffWithRunnerUsesTrackedHEADSnapshot(t *testing.T) {
	workspace := t.TempDir()
	wantArgs := [][]string{
		workspaceDiffArguments(),
		workspaceUntrackedEnumerationArguments(),
		workspaceUntrackedDiffArguments("new file.txt"),
		workspaceUntrackedDiffArguments("nested/line\nname"),
	}
	var gotRoot string
	var gotArgs [][]string

	got, err := readWorkspaceDiffWithRunner(context.Background(), workspace, func(_ context.Context, root string, args []string) (workspaceDiffCommandResult, error) {
		gotRoot = root
		gotArgs = append(gotArgs, append([]string(nil), args...))
		switch len(gotArgs) {
		case 1:
			return workspaceDiffCommandResult{stdout: []byte("diff --git a/file b/file\n+changed\n")}, nil
		case 2:
			return workspaceDiffCommandResult{stdout: []byte("new file.txt\x00nested/line\nname\x00")}, nil
		case 3:
			return workspaceDiffCommandResult{stdout: []byte("diff --git a/new file.txt b/new file.txt\n+new\n")}, nil
		case 4:
			return workspaceDiffCommandResult{stdout: []byte("diff --git a/nested/line b/nested/line\n+name\n")}, nil
		default:
			t.Fatalf("unexpected runner call %d", len(gotArgs))
			return workspaceDiffCommandResult{}, nil
		}
	})
	if err != nil {
		t.Fatalf("readWorkspaceDiffWithRunner() error = %v", err)
	}
	if gotRoot != workspace {
		t.Fatalf("runner root = %q, want %q", gotRoot, workspace)
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", gotArgs, wantArgs)
	}
	if !strings.Contains(got, "diff --git a/new file.txt b/new file.txt") || !strings.Contains(got, "+name") {
		t.Fatalf("diff output = %q", got)
	}
}

func TestWorkspaceUntrackedEnumerationExcludesIgnoredFiles(t *testing.T) {
	args := workspaceUntrackedEnumerationArguments()
	if !reflect.DeepEqual(args, []string{"--no-pager", "ls-files", "--others", "--exclude-standard", "-z", "--"}) {
		t.Fatalf("untracked enumeration args = %#v", args)
	}
}

func TestReadWorkspaceDiffWithRunnerPreservesEmptySnapshot(t *testing.T) {
	calls := 0
	got, err := readWorkspaceDiffWithRunner(context.Background(), t.TempDir(), func(_ context.Context, _ string, _ []string) (workspaceDiffCommandResult, error) {
		calls++
		return workspaceDiffCommandResult{}, nil
	})
	if err != nil {
		t.Fatalf("empty diff error = %v", err)
	}
	if got != "" {
		t.Fatalf("empty diff = %q, want empty", got)
	}
	if calls != 2 {
		t.Fatalf("runner calls = %d, want tracked diff plus untracked enumeration", calls)
	}
}

func TestReadWorkspaceDiffWithRunnerRejectsInvalidOrExcessiveUntrackedPaths(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "missing NUL terminator", data: []byte("file"), want: errWorkspaceDiffUntrackedList},
		{name: "empty entry", data: []byte("file\x00\x00"), want: errWorkspaceDiffUntrackedList},
		{name: "parent traversal", data: []byte("../file\x00"), want: errWorkspaceDiffUnsafePath},
		{name: "absolute", data: []byte("/file\x00"), want: errWorkspaceDiffUnsafePath},
		{name: "too many files", data: []byte(strings.Repeat("a\x00", workspaceDiffMaxFiles+1)), want: errWorkspaceDiffUntrackedList},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseWorkspaceDiffUntrackedPaths(tt.data)
			if !errors.Is(err, tt.want) {
				t.Fatalf("parseWorkspaceDiffUntrackedPaths() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestReadWorkspaceDiffWithRunnerSharesDeadlineAndCapsUntrackedOutput(t *testing.T) {
	var contexts []context.Context
	calls := 0
	got, err := readWorkspaceDiffWithRunner(context.Background(), t.TempDir(), func(ctx context.Context, _ string, _ []string) (workspaceDiffCommandResult, error) {
		contexts = append(contexts, ctx)
		calls++
		switch calls {
		case 1:
			return workspaceDiffCommandResult{stdout: []byte("tracked\n")}, nil
		case 2:
			return workspaceDiffCommandResult{stdout: []byte("file\x00")}, nil
		case 3:
			return workspaceDiffCommandResult{stdout: []byte(strings.Repeat("x", workspaceDiffMaxBytes))}, nil
		default:
			t.Fatalf("unexpected runner call %d", calls)
			return workspaceDiffCommandResult{}, nil
		}
	})
	if err != nil {
		t.Fatalf("readWorkspaceDiffWithRunner() error = %v", err)
	}
	if calls != 3 || !strings.HasSuffix(got, "[diff output truncated after 131072 bytes]") {
		t.Fatalf("calls=%d output suffix=%q", calls, got[max(0, len(got)-48):])
	}
	if len(contexts) != 3 {
		t.Fatalf("runner contexts = %d, want 3", len(contexts))
	}
	firstDeadline, ok := contexts[0].Deadline()
	if !ok {
		t.Fatal("runner context has no deadline")
	}
	for _, ctx := range contexts[1:] {
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(firstDeadline) {
			t.Fatalf("per-command deadline = %v, want shared %v", deadline, firstDeadline)
		}
	}
}

func TestReadWorkspaceDiffWithRunnerClassifiesGitErrors(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		err    error
		want   error
	}{
		{
			name:   "non Git workspace",
			stderr: "fatal: not a git repository (or any of the parent directories): .git",
			err:    errors.New("exit status 128"),
			want:   errWorkspaceDiffNotGit,
		},
		{
			name:   "unborn repository",
			stderr: "fatal: ambiguous argument 'HEAD': unknown revision",
			err:    errors.New("exit status 128"),
			want:   errWorkspaceDiffNoHead,
		},
		{
			name: "generic Git failure",
			err:  errors.New("exit status 1"),
			want: errWorkspaceDiffFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readWorkspaceDiffWithRunner(context.Background(), t.TempDir(), func(_ context.Context, _ string, _ []string) (workspaceDiffCommandResult, error) {
				return workspaceDiffCommandResult{stderr: []byte(tt.stderr)}, tt.err
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestReadWorkspaceDiffWithRunnerCapsRunnerOutput(t *testing.T) {
	raw := []byte(strings.Repeat("x", workspaceDiffMaxBytes+32))
	got, err := readWorkspaceDiffWithRunner(context.Background(), t.TempDir(), func(_ context.Context, _ string, _ []string) (workspaceDiffCommandResult, error) {
		return workspaceDiffCommandResult{stdout: raw}, nil
	})
	if err != nil {
		t.Fatalf("large diff error = %v", err)
	}
	if !strings.HasSuffix(got, "[diff output truncated after 131072 bytes]") {
		t.Fatalf("large diff missing truncation marker")
	}
	if len(got) > workspaceDiffMaxBytes+len("\n[diff output truncated after 131072 bytes]") {
		t.Fatalf("large diff exceeded bounded result: %d bytes", len(got))
	}
}

func TestReadBoundedPipeKillsAfterLimit(t *testing.T) {
	killed := false
	result := readBoundedPipe(strings.NewReader("123456"), 3, func() { killed = true })
	if !result.truncated || !killed || string(result.data) != "123" {
		t.Fatalf("bounded pipe result = %#v killed=%v", result, killed)
	}
}

func TestRunWorkspaceDiffCommandTreatsOnlyCleanNoIndexExitOneAsDifference(t *testing.T) {
	workspace := t.TempDir()
	path := workspace + "/untracked.txt"
	if err := os.WriteFile(path, []byte("contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runWorkspaceDiffCommand(context.Background(), workspace, workspaceUntrackedDiffArguments("untracked.txt"))
	if err != nil {
		t.Fatalf("untracked --no-index diff error = %v", err)
	}
	if len(result.stdout) == 0 || len(result.stderr) != 0 {
		t.Fatalf("unexpected normal difference result: %#v", result)
	}

	_, err = runWorkspaceDiffCommand(context.Background(), workspace, workspaceUntrackedDiffArguments("missing.txt"))
	if err == nil {
		t.Fatal("missing untracked path must not be treated as a normal difference")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("missing untracked path error = %T %v, want Git exit error", err, err)
	}
}
