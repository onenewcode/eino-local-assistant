package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/sandbox"

	"github.com/cloudwego/eino/components/tool"
)

func TestDefaultRegistryCodexSubset(t *testing.T) {
	fixed := time.Date(2026, 7, 14, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	root := t.TempDir()
	reg, err := DefaultWithOptions(DefaultOptions{
		Clock:      func() time.Time { return fixed },
		Shell:      ShellOptions{WorkspaceRoot: root},
		ApplyPatch: ApplyPatchOptions{WorkspaceRoot: root, Approval: ApprovalNever},
	})
	if err != nil {
		t.Fatalf("DefaultWithOptions: %v", err)
	}
	infos, err := reg.Infos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(infos), 4; got != want {
		t.Fatalf("tools = %d, want %d", got, want)
	}
	got := map[string]bool{}
	for _, info := range infos {
		got[info.Name] = true
	}
	for _, name := range []string{"get_current_time", "read_artifact", "apply_patch", "shell"} {
		if !got[name] {
			t.Errorf("missing %q", name)
		}
	}
	for _, banned := range []string{"run_command", "list_dir", "read_file", "write_file", "edit_file"} {
		if got[banned] {
			t.Errorf("unexpected tool %q", banned)
		}
	}
}

func TestDefaultRegistryCanDisableShell(t *testing.T) {
	root := t.TempDir()
	reg, err := DefaultWithOptions(DefaultOptions{
		Clock:      time.Now,
		Shell:      ShellOptions{Disabled: true, WorkspaceRoot: root},
		ApplyPatch: ApplyPatchOptions{WorkspaceRoot: root, Approval: ApprovalNever},
	})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := reg.Infos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.Name == "shell" {
			t.Fatalf("shell should be disabled: %#v", infos)
		}
	}
}

func TestDefaultRegistryInheritsSandboxForApplyPatch(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "must-not-write-on-host.txt")
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		WorkerPath:    filepath.Join(t.TempDir(), "missing-worker"),
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	registry, err := DefaultWithOptions(DefaultOptions{
		Clock: time.Now,
		Shell: ShellOptions{
			WorkspaceRoot: workspace,
			Approval:      ApprovalNever,
			Sandbox:       runner,
		},
		// Deliberately omit ApplyPatch.Sandbox. The registry must inherit the
		// shell boundary instead of direct-writing to the host workspace.
		ApplyPatch: ApplyPatchOptions{WorkspaceRoot: workspace, Approval: ApprovalNever},
	})
	if err != nil {
		t.Fatalf("DefaultWithOptions() error = %v", err)
	}
	patch := registryToolByName(t, registry, "apply_patch")
	raw, err := patch.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":"must-not-write-on-host.txt","content":"blocked"}]}`)
	if err != nil {
		t.Fatalf("apply_patch invocation: %v", err)
	}
	var out ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode apply_patch result: %v", err)
	}
	if !out.Denied || !strings.Contains(out.Reason, ReasonSandboxUnavailable) {
		t.Fatalf("apply_patch output = %+v, want sandbox launch deny", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("apply_patch bypassed inherited sandbox: stat marker = %v", err)
	}
}

func TestDefaultRegistryInheritsApplyPatchSandboxForShell(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "shell-must-not-write-on-host.txt")
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		Mode:          sandbox.WorkspaceWrite,
		WorkspaceRoot: workspace,
		WorkerPath:    filepath.Join(t.TempDir(), "missing-worker"),
	})
	if err != nil {
		t.Fatalf("NewSandboxRunner() error = %v", err)
	}
	registry, err := DefaultWithOptions(DefaultOptions{
		Clock: time.Now,
		Shell: ShellOptions{
			WorkspaceRoot: workspace,
			Approval:      ApprovalNever,
		},
		ApplyPatch: ApplyPatchOptions{
			WorkspaceRoot: workspace,
			Approval:      ApprovalNever,
			Sandbox:       runner,
		},
	})
	if err != nil {
		t.Fatalf("DefaultWithOptions() error = %v", err)
	}
	shell := registryToolByName(t, registry, "shell")
	payload, err := json.Marshal(ShellInput{
		Command:    "printf host > " + shellQuoteForTest(marker),
		WorkingDir: workspace,
	})
	if err != nil {
		t.Fatalf("marshal shell input: %v", err)
	}
	raw, err := shell.InvokableRun(context.Background(), string(payload))
	if err != nil {
		t.Fatalf("shell invocation: %v", err)
	}
	var out ShellOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode shell result: %v", err)
	}
	if !out.Denied || !strings.Contains(out.Reason, ReasonSandboxUnavailable) {
		t.Fatalf("shell output = %+v, want inherited sandbox launch deny", out)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell bypassed inherited sandbox: stat marker = %v", err)
	}
}

func registryToolByName(t *testing.T, registry *Registry, name string) tool.InvokableTool {
	t.Helper()
	for _, base := range registry.All() {
		info, err := base.Info(context.Background())
		if err != nil {
			t.Fatalf("tool info: %v", err)
		}
		if info == nil || info.Name != name {
			continue
		}
		invokable, ok := base.(tool.InvokableTool)
		if !ok {
			t.Fatalf("tool %q is not invokable", name)
		}
		return invokable
	}
	t.Fatalf("tool %q not registered", name)
	return nil
}
