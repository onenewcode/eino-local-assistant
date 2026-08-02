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
