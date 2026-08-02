//go:build unix

package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSandboxWorkerSignalsInterruptChildShell(t *testing.T) {
	tests := []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "sigterm", signal: syscall.SIGTERM},
		{name: "sigint", signal: syscall.SIGINT},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runSandboxWorkerSignalCase(t, tt.signal)
		})
	}
}

func runSandboxWorkerSignalCase(t *testing.T, signal syscall.Signal) {
	t.Helper()
	workspace := t.TempDir()
	started := filepath.Join(workspace, "started")
	pidFile := filepath.Join(workspace, "child.pid")
	request, err := json.Marshal(SandboxWorkerRequest{
		Kind:          sandboxWorkerShell,
		WorkspaceRoot: workspace,
		WorkingDir:    workspace,
		Command: "sleep 20 & child=$!; trap 'wait \"$child\"; exit 143' TERM INT; printf '%s' \"$child\" > " +
			shellQuoteForTest(pidFile) + "; touch " + shellQuoteForTest(started) + "; wait",
		TimeoutSeconds: 30,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("marshal worker request: %v", err)
	}

	worker := exec.Command(os.Args[0], "-test.run=^TestSandboxWorkerSignalHelper$", "--")
	worker.Env = append(os.Environ(), "GO_WANT_SANDBOX_WORKER_SIGNAL_HELPER=1")
	worker.Stdin = bytes.NewReader(request)
	var stdout, stderr bytes.Buffer
	worker.Stdout = &stdout
	worker.Stderr = &stderr
	if err := worker.Start(); err != nil {
		t.Fatalf("start worker helper: %v", err)
	}
	waitForFile(t, started)

	pidRaw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || pid <= 0 {
		t.Fatalf("child pid = %q, parse error = %v", pidRaw, err)
	}

	if err := worker.Process.Signal(signal); err != nil {
		t.Fatalf("signal worker with %v: %v", signal, err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- worker.Wait() }()
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("worker helper exit: %v; stderr=%s", err, stderr.String())
		}
	case <-time.After(3 * time.Second):
		_ = worker.Process.Kill()
		t.Fatalf("worker helper did not exit after %v", signal)
	}

	var response SandboxWorkerResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("decode worker response %q: %v", stdout.String(), err)
	}
	if response.Error != "" || response.Shell == nil {
		t.Fatalf("worker response = %#v, want cancelled shell result", response)
	}
	if !response.Shell.Cancelled || response.Shell.TimedOut {
		t.Fatalf("shell result = %#v, want cancelled without timeout", response.Shell)
	}
	waitForProcessExit(t, pid)
}

func TestSandboxWorkerSignalHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SANDBOX_WORKER_SIGNAL_HELPER") != "1" {
		return
	}
	if err := RunSandboxWorker(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "worker helper: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
