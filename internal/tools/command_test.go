package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCommandEchoesStdout(t *testing.T) {
	tool, err := NewRunCommand(RunCommandOptions{})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}

	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if got, want := info.Name, "run_command"; got != want {
		t.Fatalf("tool name = %q, want %q", got, want)
	}
	if !strings.Contains(info.Desc, "Never invent") {
		t.Errorf("tool description = %q, want guidance to avoid inventing output", info.Desc)
	}

	raw, err := tool.InvokableRun(context.Background(), `{"command":"printf 'hello-world'"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}

	var out RunCommandOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if out.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", out.ExitCode)
	}
	if out.Stdout != "hello-world" {
		t.Errorf("stdout = %q, want %q", out.Stdout, "hello-world")
	}
	if out.WorkingDir == "" {
		t.Error("working_dir is empty")
	}
	if out.TimedOut || out.Cancelled || out.Truncated {
		t.Errorf("unexpected flags: %+v", out)
	}
}

func TestRunCommandReturnsNonZeroExitAsSoftResult(t *testing.T) {
	tool, err := NewRunCommand(RunCommandOptions{})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}

	raw, err := tool.InvokableRun(context.Background(), `{"command":"exit 2"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v, want soft result", err)
	}

	var out RunCommandOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if out.ExitCode != 2 {
		t.Errorf("exit_code = %d, want 2", out.ExitCode)
	}
}

func TestRunCommandRejectsEmptyCommand(t *testing.T) {
	tool, err := NewRunCommand(RunCommandOptions{})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	if _, err := tool.InvokableRun(context.Background(), `{"command":"   "}`); err == nil {
		t.Fatal("InvokableRun() error = nil, want required command error")
	}
}

func TestRunCommandRejectsMissingWorkingDir(t *testing.T) {
	tool, err := NewRunCommand(RunCommandOptions{})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	payload, _ := json.Marshal(map[string]any{
		"command":     "pwd",
		"working_dir": missing,
	})
	if _, err := tool.InvokableRun(context.Background(), string(payload)); err == nil {
		t.Fatal("InvokableRun() error = nil, want missing working_dir error")
	}
}

func TestRunCommandUsesWorkingDir(t *testing.T) {
	dir := t.TempDir()
	tool, err := NewRunCommand(RunCommandOptions{})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"command":     "pwd",
		"working_dir": dir,
	})
	raw, err := tool.InvokableRun(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	var out RunCommandOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit_code = %d stderr=%q", out.ExitCode, out.Stderr)
	}
	got := strings.TrimSpace(out.Stdout)
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		// Some pwd implementations already return the physical path.
		gotResolved = got
	}
	if gotResolved != want {
		t.Errorf("pwd = %q, want %q", gotResolved, want)
	}
	if out.WorkingDir != dir && out.WorkingDir != want {
		// WorkingDir is Abs(dir); compare resolved forms.
		resolvedOut, _ := filepath.EvalSymlinks(out.WorkingDir)
		if resolvedOut != want {
			t.Errorf("working_dir field = %q, want %q", out.WorkingDir, want)
		}
	}
}

func TestRunCommandResolvesRelativeWorkingDirAgainstDefault(t *testing.T) {
	base := t.TempDir()
	child := filepath.Join(base, "sub")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Process CWD is deliberately elsewhere so a wrong Abs(input) would miss base/sub.
	other := t.TempDir()
	t.Chdir(other)

	tool, err := NewRunCommand(RunCommandOptions{WorkingDir: base})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"command":"pwd","working_dir":"sub"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	var out RunCommandOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if out.ExitCode != 0 {
		t.Fatalf("exit_code = %d stderr=%q", out.ExitCode, out.Stderr)
	}
	want, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(out.Stdout))
	if err != nil {
		got = strings.TrimSpace(out.Stdout)
	}
	if got != want {
		t.Fatalf("pwd = %q, want %q (relative path should resolve under default working_dir)", got, want)
	}
}

func TestRunCommandDescriptionDoesNotPromiseArtifactRecovery(t *testing.T) {
	tool, err := NewRunCommand(RunCommandOptions{})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if strings.Contains(strings.ToLower(info.Desc), "use read_artifact") &&
		strings.Contains(strings.ToLower(info.Desc), "more of the original") {
		t.Fatalf("description still promises read_artifact recovery: %q", info.Desc)
	}
	if !strings.Contains(info.Desc, "not retained") {
		t.Errorf("description should state discarded tail is not retained: %q", info.Desc)
	}
}

func TestRunCommandTruncatesLargeOutput(t *testing.T) {
	tool, err := NewRunCommand(RunCommandOptions{MaxOutputBytes: 16})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	// Portable 64-byte payload without relying on bash brace expansion.
	raw, err := tool.InvokableRun(context.Background(), `{"command":"dd if=/dev/zero bs=64 count=1 2>/dev/null | tr '\\0' 'x'"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}
	var out RunCommandOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if !out.Truncated {
		t.Fatalf("truncated = false, want true; output=%+v", out)
	}
	if len(out.Stdout) != 16 {
		t.Errorf("stdout len = %d, want 16", len(out.Stdout))
	}
	if out.StdoutBytes < 16 {
		t.Errorf("stdout_bytes = %d, want >= 16", out.StdoutBytes)
	}
}

func TestRunCommandTimesOut(t *testing.T) {
	tool, err := NewRunCommand(RunCommandOptions{TimeoutSeconds: 1})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	start := time.Now()
	raw, err := tool.InvokableRun(context.Background(), `{"command":"sleep 5","timeout_seconds":1}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v, want soft timeout result", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s, want roughly 1s", elapsed)
	}
	var out RunCommandOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if !out.TimedOut {
		t.Errorf("timed_out = false, want true; output=%+v", out)
	}
	if out.ExitCode != commandExitCodeUnavailable {
		t.Errorf("exit_code = %d, want %d", out.ExitCode, commandExitCodeUnavailable)
	}
}

func TestRunCommandHonorsParentCancel(t *testing.T) {
	tool, err := NewRunCommand(RunCommandOptions{TimeoutSeconds: 30})
	if err != nil {
		t.Fatalf("NewRunCommand() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var raw string
	var runErr error
	go func() {
		defer close(done)
		raw, runErr = tool.InvokableRun(ctx, `{"command":"sleep 20"}`)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("command did not stop after cancel")
	}
	if runErr != nil {
		t.Fatalf("InvokableRun() error = %v, want soft cancel result", runErr)
	}
	var out RunCommandOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if !out.Cancelled && !out.TimedOut {
		// Cancelled is preferred; some runtimes surface deadline if nested.
		t.Errorf("expected cancelled result, got %+v", out)
	}
	if out.ExitCode != commandExitCodeUnavailable {
		t.Errorf("exit_code = %d, want %d", out.ExitCode, commandExitCodeUnavailable)
	}
}

func TestNormalizeRunCommandOptionsDefaults(t *testing.T) {
	opts, err := normalizeRunCommandOptions(RunCommandOptions{})
	if err != nil {
		t.Fatalf("normalizeRunCommandOptions() error = %v", err)
	}
	if opts.TimeoutSeconds != defaultCommandTimeoutSeconds {
		t.Errorf("TimeoutSeconds = %d, want %d", opts.TimeoutSeconds, defaultCommandTimeoutSeconds)
	}
	if opts.MaxOutputBytes != defaultCommandOutputBytes {
		t.Errorf("MaxOutputBytes = %d, want %d", opts.MaxOutputBytes, defaultCommandOutputBytes)
	}
}

func TestNormalizeRunCommandOptionsRejectsOversize(t *testing.T) {
	if _, err := normalizeRunCommandOptions(RunCommandOptions{TimeoutSeconds: 999}); err == nil {
		t.Fatal("expected timeout upper bound error")
	}
	if _, err := normalizeRunCommandOptions(RunCommandOptions{MaxOutputBytes: maxCommandOutputBytes + 1}); err == nil {
		t.Fatal("expected max_output_bytes upper bound error")
	}
}
