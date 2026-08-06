package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchCreateUpdateDelete(t *testing.T) {
	root := t.TempDir()
	tool, err := NewApplyPatch(ApplyPatchOptions{WorkspaceRoot: root, Approval: ApprovalNever})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{
		"operations":[
			{"type":"create_file","path":"a.txt","content":"hello"},
			{"type":"update_file","path":"a.txt","old_string":"hello","new_string":"hello world"},
			{"type":"delete_file","path":"a.txt"}
		]
	}`)
	if err != nil {
		t.Fatal(err)
	}
	var out ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Denied || len(out.Results) != 3 {
		t.Fatalf("out = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("file should be deleted: %v", err)
	}
}

func TestApplyPatchUserDeny(t *testing.T) {
	root := t.TempDir()
	tool, err := NewApplyPatch(ApplyPatchOptions{
		WorkspaceRoot: root,
		Approval:      ApprovalOnRequest,
		Approver:      AutoApprover{Action: ApprovalDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":"x.txt","content":"x"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var out ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Denied || !out.StopRetrying {
		t.Fatalf("want user deny stop: %+v", out)
	}
	if !strings.Contains(out.Reason, ReasonUserDenied) {
		t.Fatalf("reason = %q", out.Reason)
	}
}

func TestApplyPatchRejectsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "out.txt")
	tool, err := NewApplyPatch(ApplyPatchOptions{WorkspaceRoot: root, Approval: ApprovalNever})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"operations": []map[string]string{
			{"type": "create_file", "path": outside, "content": "x"},
		},
	})
	raw, err := tool.InvokableRun(context.Background(), string(b))
	if err != nil {
		t.Fatal(err)
	}
	var out ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Denied {
		t.Fatalf("want deny: %+v", out)
	}
}

func TestApplyPatchPlanRejectsBeforeApprovalAndExecution(t *testing.T) {
	root := t.TempDir()
	state, err := NewApprovalState(ApprovalPlan)
	if err != nil {
		t.Fatal(err)
	}
	approver := &recordingApprover{responses: []ApprovalAction{ApprovalOnce}}
	tool, err := NewApplyPatch(ApplyPatchOptions{
		WorkspaceRoot: root,
		Approval:      ApprovalNever,
		ApprovalState: state,
		Approver:      approver,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":"blocked.txt","content":"blocked"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var out ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Denied || out.Decision != string(DecisionDeny) || out.Reason != ReasonPlanReadOnly {
		t.Fatalf("plan output = %+v", out)
	}
	if got := len(approver.Requests()); got != 0 {
		t.Fatalf("plan approval requests = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("plan apply_patch wrote a file: %v", err)
	}
}

func TestApplyPatchPlanRejectsWithSandboxBeforeWorker(t *testing.T) {
	root := t.TempDir()
	runner, err := NewSandboxRunner(SandboxRunnerOptions{
		WorkspaceRoot: root,
		WorkerPath:    filepath.Join(t.TempDir(), "missing-worker"),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := NewApprovalState(ApprovalPlan)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewApplyPatch(ApplyPatchOptions{
		WorkspaceRoot: root,
		Approval:      ApprovalOnRequest,
		ApprovalState: state,
		Sandbox:       runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":"blocked.txt","content":"blocked"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var out ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Denied || out.Decision != string(DecisionDeny) || out.Reason != ReasonPlanReadOnly {
		t.Fatalf("sandbox plan output = %+v", out)
	}
	if out.Sandbox != nil {
		t.Fatalf("plan gate should not start sandbox worker: %#v", out.Sandbox)
	}
}

func TestApplyPatchYoloBypassesApprovalAndSandbox(t *testing.T) {
	root := t.TempDir()
	state, err := NewApprovalState(ApprovalYolo)
	if err != nil {
		t.Fatal(err)
	}
	approver := &recordingApprover{}
	runner := newUnavailablePlanSandboxRunner(t, root)
	tool, err := NewApplyPatch(ApplyPatchOptions{
		WorkspaceRoot: root,
		Approval:      ApprovalYolo,
		ApprovalState: state,
		Approver:      approver,
		Sandbox:       runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":"yolo.txt","content":"host"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var out ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if out.Denied || len(out.Results) != 1 {
		t.Fatalf("yolo patch output = %+v", out)
	}
	if len(approver.Requests()) != 0 {
		t.Fatalf("yolo invoked approver: %#v", approver.Requests())
	}
	if out.Sandbox == nil || !out.Sandbox.Bypassed || out.Sandbox.Backend != "host" || out.Sandbox.Network != "host" {
		t.Fatalf("yolo patch sandbox outcome = %#v", out.Sandbox)
	}
	if got, err := os.ReadFile(filepath.Join(root, "yolo.txt")); err != nil || string(got) != "host" {
		t.Fatalf("yolo patch file = %q, err=%v", got, err)
	}
}

func TestApplyPatchYoloStillEnforcesHardDeny(t *testing.T) {
	root := t.TempDir()
	state, err := NewApprovalState(ApprovalYolo)
	if err != nil {
		t.Fatal(err)
	}
	permissions, err := BuildPermissionSet(ProfileCautious, nil, nil, []string{"ApplyPatch(.env)"})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewApplyPatch(ApplyPatchOptions{
		WorkspaceRoot: root,
		Approval:      ApprovalYolo,
		ApprovalState: state,
		Permissions:   permissions,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), `{"operations":[{"type":"create_file","path":".env","content":"secret"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var out ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Denied || !strings.Contains(out.Reason, ReasonPolicyDenied) {
		t.Fatalf("yolo patch hard-deny output = %+v", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".env")); !os.IsNotExist(err) {
		t.Fatalf("hard-denied yolo patch created .env: %v", err)
	}
}

func TestApplyPatchYoloStillEnforcesWorkspaceAndSymlinkSafety(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	state, err := NewApprovalState(ApprovalYolo)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewApplyPatch(ApplyPatchOptions{WorkspaceRoot: root, Approval: ApprovalYolo, ApprovalState: state})
	if err != nil {
		t.Fatal(err)
	}
	outsideInput, err := json.Marshal(ApplyPatchInput{Operations: []PatchOperation{{Type: patchCreate, Path: outside, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tool.InvokableRun(context.Background(), string(outsideInput))
	if err != nil {
		t.Fatal(err)
	}
	var outsideOut ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &outsideOut); err != nil {
		t.Fatal(err)
	}
	if !outsideOut.Denied || !strings.Contains(outsideOut.Reason, ReasonWorkspaceOnly) {
		t.Fatalf("yolo outside-workspace patch = %+v", outsideOut)
	}

	targetDir := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(targetDir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	symlinkInput, err := json.Marshal(ApplyPatchInput{Operations: []PatchOperation{{Type: patchCreate, Path: "linked/file.txt", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = tool.InvokableRun(context.Background(), string(symlinkInput))
	if err != nil {
		t.Fatal(err)
	}
	var symlinkOut ApplyPatchOutput
	if err := json.Unmarshal([]byte(raw), &symlinkOut); err != nil {
		t.Fatal(err)
	}
	if !symlinkOut.Denied || !strings.Contains(symlinkOut.Reason, ReasonWorkspaceSymlink) {
		t.Fatalf("yolo symlink patch = %+v", symlinkOut)
	}
}
