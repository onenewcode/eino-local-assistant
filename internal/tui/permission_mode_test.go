package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/tools"
)

func TestPermissionsCommandSwitchesIdleModeAndKeepsStaticPolicy(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Deps{
		Ctx: context.Background(),
		Status: StatusInfo{
			CmdPolicy: "cmd=ask",
		},
		PolicyInfo: CommandPolicyInfo{
			Mode:          "ask",
			Approval:      string(tools.ApprovalOnRequest),
			ApprovalState: state,
		},
	})

	next, cmd := m.submit("/permissions auto")
	if cmd != nil {
		t.Fatal("permission switch should not start a turn")
	}
	m = next.(*model)
	if got := state.InteractiveMode(); got != "auto" {
		t.Fatalf("state mode = %q, want auto", got)
	}
	if got := m.statusPolicyFragment(); got != "cmd=auto" {
		t.Fatalf("status policy = %q, want cmd=auto", got)
	}
	if !hasLineContaining(m.lines, lineSystem, "permission mode: auto") {
		t.Fatalf("switch confirmation missing: %#v", m.lines)
	}

	next, cmd = m.submit("/permissions plan")
	if cmd != nil {
		t.Fatal("plan switch should not start a turn")
	}
	m = next.(*model)
	if got := state.InteractiveMode(); got != "plan" {
		t.Fatalf("state mode = %q, want plan", got)
	}
	if got := m.statusPolicyFragment(); got != "cmd=plan" {
		t.Fatalf("plan status policy = %q, want cmd=plan", got)
	}
	next, _ = m.submit("/status")
	m = next.(*model)
	if !hasLineContaining(m.lines, lineSystem, "cmd=plan") {
		t.Fatalf("status report did not reflect plan mode: %#v", m.lines)
	}

	next, _ = m.submit("/permissions")
	m = next.(*model)
	var report string
	for _, line := range m.lines {
		if line.kind == lineSystem && strings.Contains(line.text, "tool permissions") {
			report = line.text
		}
	}
	if !strings.Contains(report, "mode: plan (on_request)") {
		t.Fatalf("dynamic mode report = %q", report)
	}
	if !strings.Contains(report, "approval_policy: on_request") || strings.Contains(report, "approval_policy: never") {
		t.Fatalf("static approval policy was not preserved: %q", report)
	}

	next, _ = m.submit("/permissions ask")
	m = next.(*model)
	if got := state.InteractiveMode(); got != "ask" {
		t.Fatalf("state mode after ask = %q, want ask", got)
	}
}

func TestPermissionsPlanIsDocumentedInHelpAndSlashMenu(t *testing.T) {
	if !strings.Contains(helpText(), "/permissions [ask|auto|plan]") {
		t.Fatalf("help does not document plan mode: %s", helpText())
	}
	for _, command := range slashCatalog() {
		if command.Name == "/permissions" {
			if !strings.Contains(command.Description, "ask|auto|plan") {
				t.Fatalf("permissions menu description = %q", command.Description)
			}
			return
		}
	}
	t.Fatal("permissions command missing from slash catalog")
}

func TestPermissionsModeChangeIsRejectedBusyWithoutDroppingDraft(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Deps{
		Ctx: context.Background(),
		PolicyInfo: CommandPolicyInfo{
			Approval:      string(tools.ApprovalOnRequest),
			ApprovalState: state,
		},
	})
	m.mode = modeBusy
	m.queue = []string{"already queued"}
	m.textarea.SetValue("/permissions auto")

	next, cmd := m.queueWhileBusy(m.textarea.Value())
	if cmd != nil {
		t.Fatal("busy permission switch should not start a command")
	}
	m = next.(*model)
	if got := state.InteractiveMode(); got != "ask" {
		t.Fatalf("busy switch changed state to %q", got)
	}
	if len(m.queue) != 1 || m.queue[0] != "already queued" {
		t.Fatalf("busy switch changed queue: %#v", m.queue)
	}
	if got := m.textarea.Value(); got != "/permissions auto" {
		t.Fatalf("busy switch dropped composer draft: %q", got)
	}
	if !hasLineContaining(m.lines, lineError, "permission mode changes are unavailable while busy") {
		t.Fatalf("busy switch error missing: %#v", m.lines)
	}
}

func TestPermissionsPlanChangeIsRejectedCompactingWithoutDroppingDraft(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Deps{
		Ctx: context.Background(),
		PolicyInfo: CommandPolicyInfo{
			Approval:      string(tools.ApprovalOnRequest),
			ApprovalState: state,
		},
	})
	m.mode = modeCompacting
	m.queue = []string{"already queued"}
	m.textarea.SetValue("/permissions plan")

	next, cmd := m.queueWhileBusy(m.textarea.Value())
	if cmd != nil {
		t.Fatal("compacting permission switch should not start a command")
	}
	m = next.(*model)
	if got := state.InteractiveMode(); got != "ask" {
		t.Fatalf("compacting switch changed state to %q", got)
	}
	if len(m.queue) != 1 || m.queue[0] != "already queued" {
		t.Fatalf("compacting switch changed queue: %#v", m.queue)
	}
	if got := m.textarea.Value(); got != "/permissions plan" {
		t.Fatalf("compacting switch dropped draft: %q", got)
	}
}

func TestPermissionsParseAndBusyClassification(t *testing.T) {
	cases := []struct {
		input  string
		action slashAction
		arg    string
		busy   busyInputDisposition
	}{
		{input: "/permissions", action: slashPermissions, busy: busyInputExecuteImmediately},
		{input: "/permissions ask", action: slashPermissions, arg: "ask", busy: busyInputReject},
		{input: "/permissions auto", action: slashPermissions, arg: "auto", busy: busyInputReject},
		{input: "/permissions plan", action: slashPermissions, arg: "plan", busy: busyInputReject},
	}
	for _, tc := range cases {
		action, arg := parseSlash(tc.input)
		if action != tc.action || arg != tc.arg {
			t.Errorf("parseSlash(%q) = (%v, %q), want (%v, %q)", tc.input, action, arg, tc.action, tc.arg)
		}
		if got := classifyBusyInput(tc.input); got != tc.busy {
			t.Errorf("classifyBusyInput(%q) = %v, want %v", tc.input, got, tc.busy)
		}
	}
}
