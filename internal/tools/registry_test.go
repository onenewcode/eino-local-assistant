package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
	if got, want := len(infos), 6; got != want {
		t.Fatalf("tools = %d, want %d", got, want)
	}
	got := map[string]bool{}
	for _, info := range infos {
		got[info.Name] = true
	}
	for _, name := range []string{"get_current_time", "read_artifact", "list_skills", "read_skill", "apply_patch", "shell"} {
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

func TestDefaultRegistryRegistersBoundedProjectSkillTools(t *testing.T) {
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".agents", "skills", "release")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("MkdirAll skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Release checklist\n\nRun the release tests.\n"), 0o600); err != nil {
		t.Fatalf("WriteFile skill: %v", err)
	}
	registry, err := DefaultWithOptions(DefaultOptions{
		Shell:      ShellOptions{WorkspaceRoot: workspace},
		ApplyPatch: ApplyPatchOptions{WorkspaceRoot: workspace, Approval: ApprovalNever},
	})
	if err != nil {
		t.Fatalf("DefaultWithOptions: %v", err)
	}
	listTool := registryToolByName(t, registry, "list_skills")
	listRaw, err := listTool.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("list_skills: %v", err)
	}
	if !strings.Contains(listRaw, `"name":"release"`) || !strings.Contains(listRaw, `.agents/skills/release/SKILL.md`) {
		t.Fatalf("list_skills output = %s", listRaw)
	}
	readTool := registryToolByName(t, registry, "read_skill")
	readRaw, err := readTool.InvokableRun(context.Background(), `{"name":"release"}`)
	if err != nil {
		t.Fatalf("read_skill: %v", err)
	}
	if !strings.Contains(readRaw, "Run the release tests.") {
		t.Fatalf("read_skill output = %s", readRaw)
	}
}

func TestDefaultRegistrySharesDynamicApprovalState(t *testing.T) {
	workspace := t.TempDir()
	state, err := NewApprovalState(ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := DefaultWithOptions(DefaultOptions{
		Shell: ShellOptions{
			WorkspaceRoot: workspace,
			Approval:      ApprovalOnRequest,
			ApprovalState: state,
			Approver:      AutoApprover{Action: ApprovalDeny},
		},
		// Omit ApplyPatch.ApprovalState: registration must share the shell state.
		ApplyPatch: ApplyPatchOptions{WorkspaceRoot: workspace, Approval: ApprovalOnRequest},
	})
	if err != nil {
		t.Fatal(err)
	}
	shell := registryToolByName(t, registry, "shell")
	patch := registryToolByName(t, registry, "apply_patch")
	if err := state.SetInteractiveMode("auto"); err != nil {
		t.Fatal(err)
	}
	shellRaw, err := shell.InvokableRun(context.Background(), `{"command":"printf shared"}`)
	if err != nil {
		t.Fatal(err)
	}
	var shellOut ShellOutput
	if err := json.Unmarshal([]byte(shellRaw), &shellOut); err != nil {
		t.Fatal(err)
	}
	if shellOut.Denied || shellOut.Stdout != "shared" {
		t.Fatalf("shell auto result = %+v", shellOut)
	}
	patchRaw, err := patch.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":"shared.txt","content":"shared"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var patchOut ApplyPatchOutput
	if err := json.Unmarshal([]byte(patchRaw), &patchOut); err != nil {
		t.Fatal(err)
	}
	if patchOut.Denied || len(patchOut.Results) != 1 {
		t.Fatalf("apply_patch auto result = %+v", patchOut)
	}
	if err := state.SetInteractiveMode("ask"); err != nil {
		t.Fatal(err)
	}
	patchRaw, err = patch.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":"second.txt","content":"second"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(patchRaw), &patchOut); err != nil {
		t.Fatal(err)
	}
	if !patchOut.Denied {
		t.Fatalf("apply_patch ask result = %+v", patchOut)
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

func TestRegistryFilterSelectsInvocationToolSurface(t *testing.T) {
	workspace := t.TempDir()
	registry, err := DefaultWithOptions(DefaultOptions{
		Clock:      time.Now,
		Shell:      ShellOptions{WorkspaceRoot: workspace},
		ApplyPatch: ApplyPatchOptions{WorkspaceRoot: workspace, Approval: ApprovalNever},
	})
	if err != nil {
		t.Fatal(err)
	}
	all := registryNames(t, registry)

	tests := []struct {
		name      string
		selection ToolSelection
		want      []string
	}{
		{name: "default", want: all},
		{name: "allow subset", selection: ToolSelection{AllowedSet: true, Allowed: []string{"shell, get_current_time", "shell"}}, want: []string{"get_current_time", "shell"}},
		{name: "deny subset", selection: ToolSelection{Disallowed: []string{"apply_patch, shell"}}, want: []string{"get_current_time", "read_artifact", "list_skills", "read_skill"}},
		{name: "deny overrides allow", selection: ToolSelection{AllowedSet: true, Allowed: []string{"shell", "read_artifact"}, Disallowed: []string{"shell"}}, want: []string{"read_artifact"}},
		{name: "explicit empty", selection: ToolSelection{AllowedSet: true}, want: []string{}},
		{name: "default keyword", selection: ToolSelection{AllowedSet: true, Allowed: []string{"default"}, Disallowed: []string{"shell"}}, want: []string{"get_current_time", "read_artifact", "list_skills", "read_skill", "apply_patch"}},
		{name: "wildcard keyword", selection: ToolSelection{AllowedSet: true, Allowed: []string{"*"}}, want: all},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			filtered, filterErr := registry.Filter(context.Background(), tc.selection)
			if filterErr != nil {
				t.Fatalf("Filter() error = %v", filterErr)
			}
			if got := registryNames(t, filtered); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("names = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRegistryFilterRejectsAmbiguousAndUnknownNames(t *testing.T) {
	workspace := t.TempDir()
	registry, err := DefaultWithOptions(DefaultOptions{
		Clock:      time.Now,
		Shell:      ShellOptions{WorkspaceRoot: workspace},
		ApplyPatch: ApplyPatchOptions{WorkspaceRoot: workspace, Approval: ApprovalNever},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, selection := range []ToolSelection{
		{AllowedSet: true, Allowed: []string{"missing_tool"}},
		{Disallowed: []string{"missing_tool"}},
	} {
		_, filterErr := registry.Filter(context.Background(), selection)
		if filterErr == nil || !strings.Contains(filterErr.Error(), `unknown tool "missing_tool"`) || !strings.Contains(filterErr.Error(), "available: apply_patch") {
			t.Fatalf("Filter(%+v) error = %v", selection, filterErr)
		}
	}
	_, err = registry.Filter(context.Background(), ToolSelection{AllowedSet: true, Allowed: []string{"default", "shell"}})
	if err == nil || !strings.Contains(err.Error(), "only as its sole value") {
		t.Fatalf("mixed default selection error = %v", err)
	}

	duplicate := New(registry.All()[0], registry.All()[0])
	if _, err := duplicate.Filter(context.Background(), ToolSelection{}); err == nil || !strings.Contains(err.Error(), "duplicate tool name") {
		t.Fatalf("duplicate Filter() error = %v", err)
	}
}

func registryNames(t *testing.T, registry *Registry) []string {
	t.Helper()
	infos, err := registry.Infos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
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
