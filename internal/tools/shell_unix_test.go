//go:build unix

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunShellDirectCancelTerminatesBackgroundChild(t *testing.T) {
	workspace := t.TempDir()
	started := filepath.Join(workspace, "started")
	pidFile := filepath.Join(workspace, "child.pid")
	defaults, err := normalizeShellOptions(ShellOptions{
		Approval:       ApprovalNever,
		TimeoutSeconds: 30,
		WorkingDir:     workspace,
	})
	if err != nil {
		t.Fatalf("normalizeShellOptions() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	var output ShellOutput
	var runErr error
	go func() {
		defer close(done)
		output, runErr = runShell(ctx, defaults, ShellInput{
			Command: "sleep 20 & child=$!; trap 'wait \"$child\"; exit 143' TERM INT; printf '%s' \"$child\" > " + shellQuoteForTest(pidFile) + "; touch " + shellQuoteForTest(started) + "; wait",
		})
	}()
	waitForFile(t, started)

	pidRaw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || pid <= 0 {
		t.Fatalf("child pid = %q, parse error = %v", pidRaw, err)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shell did not stop after cancellation")
	}
	if runErr != nil {
		t.Fatalf("runShell() error = %v", runErr)
	}
	if !output.Cancelled || output.TimedOut {
		t.Fatalf("shell output = %+v, want cancelled without timeout", output)
	}
	waitForProcessExit(t, pid)
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("probe child process %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("background child %d survived cancellation", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
