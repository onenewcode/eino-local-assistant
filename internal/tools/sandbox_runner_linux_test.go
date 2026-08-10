//go:build linux

package tools

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/sandbox"
)

func TestSandboxRunnerLinuxKeepsDirectNetworkOpen(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("bwrap is unavailable")
	}

	workspace := t.TempDir()
	worker := buildSandboxWorkerBinary(t)
	probe := buildSandboxNetworkProbe(t, workspace)
	directTarget := startDirectNetworkTarget(t)
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		WorkerPath:    worker,
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	defer runner.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	direct := executeLinuxSandboxProbe(ctx, t, runner, shellQuote(probe)+" "+shellQuote(directTarget))
	if direct.ExitCode != 0 {
		t.Fatalf("direct host network was blocked: %#v", direct)
	}
}

func TestSandboxRunnerLinuxFailsClosedWithoutBwrap(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(t.TempDir(), "host-fallback-ran")
	worker := filepath.Join(t.TempDir(), "host-fallback")
	script := "#!/bin/sh\nprintf host > " + shellQuote(marker) + "\n"
	if err := os.WriteFile(worker, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		WorkerPath:    worker,
		currentAvailability: func() sandbox.Availability {
			return sandbox.Availability{Backend: sandbox.BackendBubblewrap, Reason: "bwrap unavailable for test"}
		},
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	defer runner.Close()
	_, _, err = runner.Execute(context.Background(), SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "true",
		WorkingDir:     workspace,
		TimeoutSeconds: 1,
		MaxOutputBytes: 1024,
	})
	if !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("Execute() error = %v, want sandbox.ErrUnavailable", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unavailable sandbox executed host worker: stat marker = %v", statErr)
	}
}

func executeLinuxSandboxProbe(ctx context.Context, t *testing.T, runner *SandboxRunner, command string) ShellOutput {
	t.Helper()
	response, outcome, err := runner.Execute(ctx, SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        command,
		WorkingDir:     runner.basePolicy.Workspace,
		TimeoutSeconds: 10,
		MaxOutputBytes: 16 << 10,
	})
	if err != nil {
		if linuxSandboxCannotCreateNamespace(err) {
			t.Skipf("bwrap is present but cannot create a sandbox here: %v", err)
		}
		t.Fatalf("sandbox worker: %v", err)
	}
	if response.Error != "" {
		t.Fatalf("sandbox worker response error: %s", response.Error)
	}
	if response.Shell == nil {
		t.Fatalf("sandbox worker response = %#v", response)
	}
	if outcome.Backend != string(sandbox.BackendBubblewrap) || !outcome.Enforced {
		t.Fatalf("sandbox outcome = %#v", outcome)
	}
	return *response.Shell
}

func linuxSandboxCannotCreateNamespace(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{
		"operation not permitted",
		"permission denied",
		"no permissions to create new namespace",
		"creating new namespace",
		"user namespace",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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

func buildSandboxNetworkProbe(t *testing.T, workspace string) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "probe.go")
	const program = `package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: probe target")
		os.Exit(2)
	}
	connection, err := net.DialTimeout("tcp", os.Args[1], 2*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	connection.Close()
	fmt.Println("direct response")
}
`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(workspace, "network-probe")
	command := exec.Command("go", "build", "-o", probe, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build network probe: %v\n%s", err, output)
	}
	return probe
}

func startDirectNetworkTarget(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.Close()
		}
	}()
	return listener.Addr().String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
