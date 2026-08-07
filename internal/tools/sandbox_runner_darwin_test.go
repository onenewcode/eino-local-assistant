//go:build darwin

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"eino-local-assistant/internal/sandbox"
)

func TestSandboxRunnerDarwinWorkerSeesHostToolchain(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("sandbox-exec is unavailable")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go is unavailable on the host: %v", err)
	}
	worker := buildSandboxWorkerBinary(t)
	workspace := t.TempDir()
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:                sandbox.WorkspaceWrite,
		WorkspaceRoot:       workspace,
		WorkerPath:          worker,
		ToolchainVisibility: sandbox.ToolchainVisibilityAuto,
		HostEnvironment:     os.Environ(),
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	defer runner.Close()

	response, outcome, err := runner.Execute(context.Background(), SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		WorkingDir:     workspace,
		Command:        "go version",
		TimeoutSeconds: 10,
		MaxOutputBytes: 4096,
	})
	if err != nil {
		t.Fatalf("sandbox go version: %v", err)
	}
	if response.Shell == nil || response.Shell.ExitCode != 0 {
		t.Fatalf("sandbox go version response = %#v; outcome=%#v", response, outcome)
	}
	if !strings.Contains(response.Shell.Stdout, "go version") {
		t.Fatalf("sandbox go version stdout = %q", response.Shell.Stdout)
	}
	if outcome.ToolchainVisibility != string(sandbox.ToolchainVisibilityAuto) || outcome.EnvironmentMode != "filtered-host" {
		t.Fatalf("sandbox environment outcome = %#v", outcome)
	}
}

func TestSandboxRunnerDarwinWorkerEnforcesWorkspaceBoundary(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("sandbox-exec is unavailable")
	}
	worker := buildSandboxWorkerBinary(t)
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	protected := filepath.Join(workspace, ".env")
	if err := os.WriteFile(protected, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write protected fixture: %v", err)
	}
	escape := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatalf("create workspace escape symlink: %v", err)
	}
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:           sandbox.WorkspaceWrite,
		WorkspaceRoot:  workspace,
		ProtectedPaths: []string{".env"},
		WorkerPath:     worker,
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, outcome, err := runner.Execute(ctx, SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "pwd",
		WorkingDir:     workspace,
		TimeoutSeconds: 10,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("sandbox pwd: %v", err)
	}
	if response.Shell == nil || response.Shell.ExitCode != 0 {
		t.Fatalf("sandbox pwd response = %#v", response)
	}
	if outcome.Backend != string(sandbox.BackendSeatbelt) || !outcome.Enforced {
		t.Fatalf("outcome = %#v", outcome)
	}

	response, _, err = runner.Execute(ctx, SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "printf blocked > " + shellQuote(outside),
		WorkingDir:     workspace,
		TimeoutSeconds: 10,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("sandbox external write runner error: %v", err)
	}
	if response.Shell == nil || response.Shell.ExitCode == 0 {
		t.Fatalf("external write unexpectedly succeeded: %#v", response)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("outside file exists or unexpected stat error: %v", err)
	}

	response, _, err = runner.Execute(ctx, SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "printf blocked > escape",
		WorkingDir:     workspace,
		TimeoutSeconds: 10,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("sandbox symlink escape runner error: %v", err)
	}
	if response.Shell == nil || response.Shell.ExitCode == 0 {
		t.Fatalf("symlink escape unexpectedly succeeded: %#v", response)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("symlink escape created outside file or unexpected stat error: %v", err)
	}

	response, _, err = runner.Execute(ctx, SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "cat .env",
		WorkingDir:     workspace,
		TimeoutSeconds: 10,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("sandbox protected read runner error: %v", err)
	}
	if response.Shell == nil || response.Shell.ExitCode == 0 {
		t.Fatalf("protected read unexpectedly succeeded: %#v", response)
	}

	response, _, err = runner.Execute(ctx, SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "printf replaced > .env",
		WorkingDir:     workspace,
		TimeoutSeconds: 10,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("sandbox protected write runner error: %v", err)
	}
	if response.Shell == nil || response.Shell.ExitCode == 0 {
		t.Fatalf("protected write unexpectedly succeeded: %#v", response)
	}
	contents, err := os.ReadFile(protected)
	if err != nil {
		t.Fatalf("read protected fixture: %v", err)
	}
	if string(contents) != "secret\n" {
		t.Fatalf("protected file changed to %q", contents)
	}
}

func TestSandboxRunnerDarwinApplyPatchMutatesOnlyAllowedWorkspacePath(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("sandbox-exec is unavailable")
	}
	worker := buildSandboxWorkerBinary(t)
	workspace := t.TempDir()
	protected := filepath.Join(workspace, ".env")
	if err := os.WriteFile(protected, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("write protected fixture: %v", err)
	}
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:           sandbox.WorkspaceWrite,
		WorkspaceRoot:  workspace,
		ProtectedPaths: []string{".env"},
		WorkerPath:     worker,
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	invokable, err := NewApplyPatch(ApplyPatchOptions{
		WorkspaceRoot: workspace,
		Approval:      ApprovalNever,
		Sandbox:       runner,
	})
	if err != nil {
		t.Fatalf("NewApplyPatch() error = %v", err)
	}

	raw, err := invokable.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":"inside.txt","content":"created in sandbox\n"}]}`)
	if err != nil {
		t.Fatalf("create inside workspace: %v", err)
	}
	var created ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &created); err != nil {
		t.Fatalf("decode create result: %v", err)
	}
	if created.Denied || len(created.Results) != 1 || created.Sandbox == nil || !created.Sandbox.Enforced {
		t.Fatalf("create result = %+v", created)
	}
	contents, err := os.ReadFile(filepath.Join(workspace, "inside.txt"))
	if err != nil || string(contents) != "created in sandbox\n" {
		t.Fatalf("inside workspace content = %q, %v", contents, err)
	}

	raw, err = invokable.InvokableRun(context.Background(), `{"operations":[{"type":"update_file","path":".env","old_string":"secret","new_string":"replaced"}]}`)
	if err != nil {
		t.Fatalf("update protected path: %v", err)
	}
	var blocked ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &blocked); err != nil {
		t.Fatalf("decode protected result: %v", err)
	}
	if !blocked.Denied || blocked.Sandbox == nil || !blocked.Sandbox.Enforced {
		t.Fatalf("protected update must be denied in sandbox, got %+v", blocked)
	}
	contents, err = os.ReadFile(protected)
	if err != nil || string(contents) != "secret\n" {
		t.Fatalf("protected file changed to %q, %v", contents, err)
	}
}

func TestSandboxRunnerDarwinCancellationStopsTermIgnoringNestedShell(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("sandbox-exec is unavailable")
	}
	worker := buildSandboxWorkerBinary(t)
	workspace := t.TempDir()
	started := filepath.Join(workspace, "started")
	pidFile := filepath.Join(workspace, "child.pid")
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		WorkerPath:    worker,
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		response SandboxWorkerResponse
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, _, err := runner.Execute(ctx, SandboxWorkerRequest{
			Kind:       sandboxWorkerShell,
			WorkingDir: workspace,
			Command: "(trap '' TERM; while :; do sleep 1; done) & child=$!; trap '' TERM; printf '%s' \"$child\" > " +
				shellQuote(pidFile) + "; touch " + shellQuote(started) + "; wait \"$child\"",
			TimeoutSeconds: 30,
			MaxOutputBytes: 1024,
		})
		done <- result{response: response, err: err}
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
	case got := <-done:
		if got.err != nil {
			t.Fatalf("sandbox cancellation returned error: %v", got.err)
		}
		if got.response.Shell == nil || !got.response.Shell.Cancelled || got.response.Shell.TimedOut {
			t.Fatalf("sandbox cancellation response = %#v", got.response)
		}
	case <-time.After(sandboxWorkerShutdownGrace + commandWaitGrace + 3*time.Second):
		t.Fatal("sandbox worker did not finish bounded nested-shell cleanup")
	}
	waitForProcessExit(t, pid)
}

func TestSandboxRunnerDarwinDoesNotPassInheritedDescriptorToShell(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("sandbox-exec is unavailable")
	}
	worker := buildSandboxWorkerBinary(t)
	workspace := t.TempDir()
	secret := filepath.Join(t.TempDir(), "inherited-secret")
	if err := os.WriteFile(secret, []byte("must-not-leak\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(secret)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	const inheritedFD = 99
	if err := syscall.Dup2(int(file.Fd()), inheritedFD); err != nil {
		t.Fatalf("Dup2() error = %v", err)
	}
	defer syscall.Close(inheritedFD)

	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		WorkerPath:    worker,
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	response, _, err := runner.Execute(context.Background(), SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		WorkingDir:     workspace,
		Command:        "cat <&99",
		TimeoutSeconds: 10,
		MaxOutputBytes: 1024,
	})
	if err != nil {
		t.Fatalf("sandbox worker: %v", err)
	}
	if response.Shell == nil || response.Shell.ExitCode == 0 {
		t.Fatalf("inherited descriptor command unexpectedly succeeded: %#v", response)
	}
	if strings.Contains(response.Shell.Stdout, "must-not-leak") || strings.Contains(response.Shell.Stderr, "must-not-leak") {
		t.Fatalf("inherited descriptor leaked secret: %#v", response.Shell)
	}
}

func buildSandboxWorkerBinary(t *testing.T) string {
	t.Helper()
	worker := filepath.Join(t.TempDir(), "eino-assistant-worker")
	command := exec.Command("go", "build", "-o", worker, "../../cmd/eino-assistant")
	command.Dir = "."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build sandbox worker: %v\n%s", err, output)
	}
	return worker
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
