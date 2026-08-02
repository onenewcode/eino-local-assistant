package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-local-assistant/internal/sandbox"
)

func TestSandboxRunnerExecuteDoesNotRescanWorkspaceHardLinks(t *testing.T) {
	workspace := t.TempDir()
	original := filepath.Join(workspace, "original.txt")
	if err := os.WriteFile(original, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		WorkerPath:    filepath.Join(t.TempDir(), "missing-worker"),
		currentAvailability: func() sandbox.Availability {
			return sandbox.Availability{Backend: sandbox.BackendSeatbelt, Reason: "unavailable for hard-link hot-path test"}
		},
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	// A hard link created after session validation must not fail Execute's
	// temp-dir rebind path (the full workspace walk is startup-only).
	if err := os.Link(original, filepath.Join(workspace, "alias.txt")); err != nil {
		t.Fatalf("create hard link after runner construction: %v", err)
	}
	_, _, err = runner.Execute(context.Background(), SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "true",
		WorkingDir:     workspace,
		TimeoutSeconds: 1,
		MaxOutputBytes: 1024,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want backend/worker failure")
	}
	if strings.Contains(err.Error(), "hard links") {
		t.Fatalf("Execute() re-scanned workspace hard links: %v", err)
	}
}

func TestNewSandboxRunnerValidatesEffectivePolicyAtStartup(t *testing.T) {
	workspace := t.TempDir()
	cases := []struct {
		name string
		opts SandboxRunnerOptions
		want string
	}{
		{
			name: "read only root overlaps workspace",
			opts: SandboxRunnerOptions{
				WorkspaceRoot: workspace,
				ReadOnlyRoots: []string{workspace},
			},
			want: "overlaps workspace",
		},
		{
			name: "protected path parent is absent",
			opts: SandboxRunnerOptions{
				WorkspaceRoot:  workspace,
				ProtectedPaths: []string{"missing/child"},
			},
			want: "parent does not exist",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSandboxRunner(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewSandboxRunner() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNewSandboxRunnerRejectsWorkspaceBackendLauncher(t *testing.T) {
	workspace := t.TempDir()
	launcher := filepath.Join(workspace, "sandbox-launcher")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write launcher: %v", err)
	}
	availability := sandbox.Availability{
		Backend:    sandbox.BackendSeatbelt,
		Available:  true,
		Executable: launcher,
	}
	_, err := NewSandboxRunner(SandboxRunnerOptions{
		WorkspaceRoot: workspace,
		currentAvailability: func() sandbox.Availability {
			return availability
		},
	})
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("NewSandboxRunner() error = %v, want workspace launcher rejection", err)
	}
}

func TestNewSandboxRunnerStagesAndPinsWorkspaceWorker(t *testing.T) {
	workspace := t.TempDir()
	worker := filepath.Join(workspace, "worker")
	original := "#!/bin/sh\nprintf original\n"
	if err := os.WriteFile(worker, []byte(original), 0o700); err != nil {
		t.Fatalf("write worker: %v", err)
	}
	launcher := filepath.Join(t.TempDir(), "sandbox-launcher")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write launcher: %v", err)
	}
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		WorkspaceRoot: workspace,
		WorkerPath:    worker,
		currentAvailability: func() sandbox.Availability {
			return sandbox.Availability{Backend: sandbox.BackendSeatbelt, Available: true, Executable: launcher}
		},
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	staged := runner.workerPath
	if PathWithinWorkspace(workspace, staged) {
		t.Fatalf("staged worker %q is inside workspace %q", staged, workspace)
	}
	if got, err := os.ReadFile(staged); err != nil || string(got) != original {
		t.Fatalf("staged worker = %q, %v; want original bytes", got, err)
	}
	if err := os.WriteFile(worker, []byte("#!/bin/sh\nprintf replaced\n"), 0o700); err != nil {
		t.Fatalf("replace workspace worker: %v", err)
	}
	if got, err := os.ReadFile(staged); err != nil || string(got) != original {
		t.Fatalf("staged worker changed with workspace source: %q, %v", got, err)
	}
	if err := runner.Close(); err != nil {
		t.Fatalf("SandboxRunner.Close() error = %v", err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged worker remains after Close(): %v", err)
	}
}

func TestSandboxRunnerPinsBackendBeforeWorkspacePathChanges(t *testing.T) {
	backend := sandbox.CurrentAvailability()
	if backend.Backend == "" {
		t.Skip("no platform sandbox backend")
	}
	workspace := t.TempDir()
	trustedDir := t.TempDir()
	trustedMarker := filepath.Join(t.TempDir(), "trusted-launcher-ran")
	workspaceMarker := filepath.Join(t.TempDir(), "workspace-launcher-ran")
	trusted := filepath.Join(trustedDir, "trusted-launcher")
	if err := os.WriteFile(trusted, []byte("#!/bin/sh\nprintf trusted > "+shellQuoteForSandboxRunnerTest(trustedMarker)+"\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write trusted launcher: %v", err)
	}
	backend.Available = true
	backend.Executable = trusted
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		WorkspaceRoot: workspace,
		currentAvailability: func() sandbox.Availability {
			return backend
		},
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}

	launcherName := "bwrap"
	if backend.Backend == sandbox.BackendSeatbelt {
		launcherName = "sandbox-exec"
	}
	if err := os.WriteFile(filepath.Join(workspace, launcherName), []byte("#!/bin/sh\nprintf workspace > "+shellQuoteForSandboxRunnerTest(workspaceMarker)+"\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("write workspace launcher: %v", err)
	}
	t.Setenv("PATH", workspace)
	_, _, err = runner.Execute(context.Background(), SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		Command:        "true",
		WorkingDir:     workspace,
		TimeoutSeconds: 1,
		MaxOutputBytes: 1024,
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want fake trusted launcher failure")
	}
	if _, err := os.Stat(trustedMarker); err != nil {
		t.Fatalf("pinned launcher did not run: %v", err)
	}
	if _, err := os.Stat(workspaceMarker); !os.IsNotExist(err) {
		t.Fatalf("workspace PATH launcher ran after runner construction: %v", err)
	}
}

func shellQuoteForSandboxRunnerTest(value string) string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(value, "'", "'\\''"))
}
