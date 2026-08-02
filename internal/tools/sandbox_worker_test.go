package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunSandboxWorkerContextCancelsShell(t *testing.T) {
	workspace := t.TempDir()
	started := filepath.Join(workspace, "started")
	request, err := json.Marshal(SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		WorkspaceRoot:  workspace,
		WorkingDir:     workspace,
		Command:        "printf started > " + shellQuoteForTest(started) + "; sleep 20",
		TimeoutSeconds: 30,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("marshal worker request: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runSandboxWorker(ctx, bytes.NewReader(request), &output)
	}()
	waitForFile(t, started)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runSandboxWorker() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}

	var response SandboxWorkerResponse
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode worker response %q: %v", output.String(), err)
	}
	if response.Error != "" || response.Shell == nil {
		t.Fatalf("worker response = %#v, want cancelled shell result", response)
	}
	if !response.Shell.Cancelled || response.Shell.TimedOut {
		t.Fatalf("shell result = %#v, want cancelled without timeout", response.Shell)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func shellQuoteForTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
