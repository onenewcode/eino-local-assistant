package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
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
	next, _ = m.submit("/permissions")
	m = next.(*model)
	var report string
	for _, line := range m.lines {
		if line.kind == lineSystem && strings.Contains(line.text, "tool policy") {
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

func TestPermissionsYoloIsExplicitAndVisible(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Deps{
		Ctx: context.Background(),
		PolicyInfo: CommandPolicyInfo{
			Mode:          "ask",
			Approval:      string(tools.ApprovalOnRequest),
			ApprovalState: state,
			Sandbox:       SandboxInfo{Mode: "workspace-write", Backend: "seatbelt"},
		},
	})

	next, cmd := m.submit("/permissions yolo")
	if cmd != nil {
		t.Fatal("yolo switch should not start a turn")
	}
	m = next.(*model)
	if got := state.Mode(); got != tools.ApprovalYolo {
		t.Fatalf("state mode = %q, want yolo", got)
	}
	if got, want := m.statusPolicyFragment(), "cmd=yolo · YOLO=UNSAFE"; got != want {
		t.Fatalf("yolo status = %q, want %q", got, want)
	}
	if !hasLineContaining(m.lines, lineError, tools.YoloModeWarning) {
		t.Fatalf("yolo warning missing: %#v", m.lines)
	}

	next, _ = m.submit("/permissions")
	m = next.(*model)
	var report string
	for _, line := range m.lines {
		if line.kind == lineSystem && strings.Contains(line.text, "tool policy") {
			report = line.text
		}
	}
	if !containsAll(report, "mode: yolo (on_request)", "approval: bypassed", "sandbox: BYPASSED by YOLO", "hard_denies: enforced", "workspace_path_safety: enforced") {
		t.Fatalf("yolo permissions report = %q", report)
	}
	if err := state.SetInteractiveMode("ask"); err != nil {
		t.Fatal(err)
	}
	if got, want := m.statusPolicyFragment(), "cmd=ask · sb=rw"; got != want {
		t.Fatalf("status after leaving yolo = %q, want %q", got, want)
	}
	report = m.deps.PolicyInfo.FormatPermissions()
	if strings.Contains(report, "BYPASSED by YOLO") || strings.Contains(report, "mode: yolo") {
		t.Fatalf("yolo state persisted after explicit exit: %q", report)
	}
}

func TestYoloLeavesShiftTabCycleToAskAndStartupWarningIsVisible(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalYolo)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Deps{
		Ctx: context.Background(),
		PolicyInfo: CommandPolicyInfo{
			Mode:          "yolo",
			Approval:      string(tools.ApprovalOnRequest),
			ApprovalState: state,
		},
	})
	if !hasLineContaining(m.lines, lineError, tools.YoloModeWarning) {
		t.Fatalf("startup yolo warning missing: %#v", m.lines)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if cmd != nil {
		t.Fatal("Shift+Tab in yolo returned a command")
	}
	m = next.(*model)
	if got := state.InteractiveMode(); got != "ask" {
		t.Fatalf("Shift+Tab mode = %q, want ask", got)
	}
	if !hasLineContaining(m.lines, lineSystem, "permission mode: ask") {
		t.Fatalf("Shift+Tab ask confirmation missing: %#v", m.lines)
	}
}

func TestPermissionsYoloChangeIsRejectedOutsideIdleWithoutDroppingDraft(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mode          mode
		pending       bool
		sideQuestions int
	}{
		{name: "busy", mode: modeBusy},
		{name: "compacting", mode: modeCompacting},
		{name: "pending approval", mode: modeIdle, pending: true},
		{name: "side question", mode: modeIdle, sideQuestions: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
			if err != nil {
				t.Fatal(err)
			}
			m := newModel(Deps{Ctx: context.Background(), PolicyInfo: CommandPolicyInfo{
				Approval:      string(tools.ApprovalOnRequest),
				ApprovalState: state,
			}})
			m.mode = tc.mode
			m.sideQuestions = tc.sideQuestions
			m.textarea.SetValue("/permissions yolo")
			if tc.pending {
				m.pendingApproval = &approvalRequestMsg{ID: "yolo-approval"}
			}

			next, cmd := m.queueWhileBusy(m.textarea.Value())
			if cmd != nil {
				t.Fatal("rejected yolo switch returned a command")
			}
			m = next.(*model)
			if got := state.InteractiveMode(); got != "ask" {
				t.Fatalf("rejected yolo switch changed mode to %q", got)
			}
			if got := m.textarea.Value(); got != "/permissions yolo" {
				t.Fatalf("rejected yolo switch changed draft to %q", got)
			}
		})
	}
}

func TestShiftTabCyclesPermissionModesWithoutStartingTurn(t *testing.T) {
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
	m.textarea.SetValue("keep this draft")
	for _, want := range []string{"auto", "plan", "yolo", "ask", "auto"} {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
		if cmd != nil {
			t.Fatalf("Shift+Tab %q returned a command", want)
		}
		m = next.(*model)
		if got := state.InteractiveMode(); got != want {
			t.Fatalf("Shift+Tab mode = %q, want %q", got, want)
		}
		if got := m.textarea.Value(); got != "keep this draft" {
			t.Fatalf("Shift+Tab dropped draft: %q", got)
		}
		if !hasLineContaining(m.lines, lineSystem, "permission mode: "+want) {
			t.Fatalf("Shift+Tab confirmation for %q missing: %#v", want, m.lines)
		}
		if want == "yolo" && !hasLineContaining(m.lines, lineError, tools.YoloModeWarning) {
			t.Fatalf("Shift+Tab yolo warning missing: %#v", m.lines)
		}
	}
}

func TestShiftTabDoesNotChangeModeOutsideIdleAdmission(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mode          mode
		pending       bool
		sideQuestions int
	}{
		{name: "busy", mode: modeBusy},
		{name: "compacting", mode: modeCompacting},
		{name: "pending approval", mode: modeIdle, pending: true},
		{name: "side question", mode: modeIdle, sideQuestions: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			m.mode = tc.mode
			m.sideQuestions = tc.sideQuestions
			m.textarea.SetValue("keep this draft")
			if tc.pending {
				m.pendingApproval = &approvalRequestMsg{ID: "shift-tab-approval"}
			}

			next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
			if cmd != nil {
				t.Fatalf("blocked Shift+Tab returned a command")
			}
			m = next.(*model)
			if got := state.InteractiveMode(); got != "ask" {
				t.Fatalf("blocked Shift+Tab changed mode to %q", got)
			}
			if got := m.textarea.Value(); got != "keep this draft" {
				t.Fatalf("blocked Shift+Tab dropped draft: %q", got)
			}
		})
	}
}

func TestPlanAliasSwitchesIdleModeAndPreservesStatus(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Deps{
		Ctx:    context.Background(),
		Status: StatusInfo{CmdPolicy: "cmd=ask"},
		PolicyInfo: CommandPolicyInfo{
			Approval:      string(tools.ApprovalOnRequest),
			ApprovalState: state,
		},
	})

	next, cmd := m.submit("/plan")
	if cmd != nil {
		t.Fatal("plan alias should not start a turn")
	}
	m = next.(*model)
	if got := state.InteractiveMode(); got != "plan" {
		t.Fatalf("state mode = %q, want plan", got)
	}
	if got := m.statusPolicyFragment(); got != "cmd=plan" {
		t.Fatalf("status policy = %q, want cmd=plan", got)
	}
	if !hasLineContaining(m.lines, lineSystem, "permission mode: plan") {
		t.Fatalf("alias switch confirmation missing: %#v", m.lines)
	}

	if m.mode != modeIdle {
		t.Fatalf("no-argument plan alias changed TUI operation mode to %s", modeName(m.mode))
	}
}

func TestPlanParserDistinguishesPromptAndReservedTransitions(t *testing.T) {
	cases := []struct {
		input string
		arg   string
	}{
		{input: "/plan", arg: ""},
		{input: "/plan inspect the repository", arg: "inspect the repository"},
		{input: "/plan exit", arg: "exit"},
		{input: "/plan ask", arg: "ask"},
		{input: "/plan AUTO", arg: "AUTO"},
	}
	for _, tc := range cases {
		action, arg := parseSlash(tc.input)
		if action != slashPlan || arg != tc.arg {
			t.Errorf("parseSlash(%q) = (%v, %q), want (%v, %q)", tc.input, action, arg, slashPlan, tc.arg)
		}
	}
}

func TestPlanPromptStartsOneNormalTurnAndRetainsPlanMode(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t)
	m.deps.PolicyInfo.ApprovalState = state
	beforeTurnID := m.turnID

	next, cmd := m.submit("/plan inspect the repository")
	if cmd == nil {
		t.Fatal("plan prompt should start the normal TUI turn")
	}
	m = next.(*model)
	if state.InteractiveMode() != "plan" {
		t.Fatalf("plan prompt mode = %q, want plan", state.InteractiveMode())
	}
	if m.mode != modeBusy || m.turnID != beforeTurnID+1 {
		t.Fatalf("plan prompt did not start exactly one turn: mode=%s turnID=%d", modeName(m.mode), m.turnID)
	}
	if !hasLineContaining(m.lines, lineUser, "inspect the repository") {
		t.Fatalf("plan prompt was not shown as the model prompt: %#v", m.lines)
	}

	cancelAndWaitForTurn(t, m)
	m.finishTurn(context.Canceled)
	if state.InteractiveMode() != "plan" {
		t.Fatalf("plan mode was not retained after turn cleanup: %q", state.InteractiveMode())
	}
}

func TestPlanReservedTransitionsOnlySwitchMode(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalPlan)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t)
	m.deps.PolicyInfo.ApprovalState = state
	for _, tc := range []struct {
		input string
		want  string
	}{
		{input: "/plan exit", want: "ask"},
		{input: "/plan auto", want: "auto"},
		{input: "/plan ask", want: "ask"},
	} {
		beforeTurnID := m.turnID
		next, cmd := m.submit(tc.input)
		m = next.(*model)
		if cmd != nil || m.mode != modeIdle || m.turnID != beforeTurnID {
			t.Fatalf("%s started a turn: cmd=%v mode=%s turnID=%d", tc.input, cmd, modeName(m.mode), m.turnID)
		}
		if got := state.InteractiveMode(); got != tc.want {
			t.Fatalf("%s mode = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPlanPromptFailureDoesNotStartTurnAndKeepsPlanMode(t *testing.T) {
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
	beforeTurnID := m.turnID

	next, cmd := m.submit("/plan inspect without a session")
	m = next.(*model)
	if cmd != nil || m.mode != modeIdle || m.turnID != beforeTurnID {
		t.Fatalf("failed plan prompt started a turn: cmd=%v mode=%s turnID=%d", cmd, modeName(m.mode), m.turnID)
	}
	if state.InteractiveMode() != "plan" {
		t.Fatalf("failed plan prompt should retain plan mode, got %q", state.InteractiveMode())
	}
	if !hasLineContaining(m.lines, lineError, "session is unavailable") {
		t.Fatalf("failed plan prompt error missing: %#v", m.lines)
	}
}

func TestPermissionsPlanIsDocumentedInHelpAndSlashMenu(t *testing.T) {
	if !strings.Contains(helpText(), "/permissions [ask|auto|plan|yolo]") {
		t.Fatalf("help does not document plan mode: %s", helpText())
	}
	if !strings.Contains(helpText(), "/plan") {
		t.Fatalf("help does not document the direct plan alias: %s", helpText())
	}
	if !strings.Contains(helpText(), "shift+tab cycle permission mode: ask -> auto -> plan -> yolo -> ask") {
		t.Fatalf("help does not document Shift+Tab mode cycle: %s", helpText())
	}
	if !strings.Contains(helpText(), "/permissions yolo") || !strings.Contains(helpText(), "hard denies/path checks") {
		t.Fatalf("help does not document yolo safety boundary: %s", helpText())
	}
	planRow := false
	for _, command := range slashCatalog() {
		if command.Name == "/plan" {
			planRow = true
			if len(command.Aliases) != 0 || command.NeedsArg {
				t.Fatalf("plan catalog row should be independent and no-argument: %#v", command)
			}
			if !strings.Contains(command.Description, "prompt") || !strings.Contains(command.Description, "exit|ask|auto") {
				t.Fatalf("plan menu description does not document prompt/transitions: %q", command.Description)
			}
		}
		if command.Name == "/permissions" {
			if !strings.Contains(command.Description, "ask|auto|plan") || !strings.Contains(command.Description, "yolo") || !strings.Contains(command.Description, "Shift+Tab") {
				t.Fatalf("permissions menu description = %q", command.Description)
			}
		}
	}
	if !planRow {
		t.Fatal("plan command missing from slash catalog")
	}
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

func TestPlanAliasMatchesPermissionsPlanAdmission(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    mode
		pending bool
	}{
		{name: "busy", mode: modeBusy},
		{name: "compacting", mode: modeCompacting},
		{name: "approval", mode: modeBusy, pending: true},
		{name: "pending approval while otherwise idle", mode: modeIdle, pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, input := range []string{"/permissions plan", "/plan", "/plan inspect files", "/plan exit", "/plan ask", "/plan auto"} {
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
				m.mode = tc.mode
				if tc.pending {
					m.pendingApproval = &approvalRequestMsg{ID: "plan-approval"}
				}
				m.queue = []string{"already queued"}
				m.textarea.SetValue(input)

				next, cmd := m.queueWhileBusy(input)
				m = next.(*model)
				if cmd != nil {
					t.Fatalf("%s returned command: %v", input, cmd)
				}
				if got := state.InteractiveMode(); got != "ask" {
					t.Fatalf("%s changed state to %q", input, got)
				}
				if len(m.queue) != 1 || m.queue[0] != "already queued" {
					t.Fatalf("%s changed queue: %#v", input, m.queue)
				}
				if got := m.textarea.Value(); got != input {
					t.Fatalf("%s changed draft to %q", input, got)
				}
				if !hasLineContaining(m.lines, lineError, "permission mode changes are unavailable while busy") {
					t.Fatalf("%s rejection error missing: %#v", input, m.lines)
				}
			}
		})
	}
}

func TestPlanRejectedFromBusySlashMenuKeepsDraftAndDoesNotCancel(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t)
	m.deps.PolicyInfo.ApprovalState = state
	m.mode = modeBusy
	m.queue = []string{"already queued"}
	cancelCalls := 0
	m.turnCancel = func() { cancelCalls++ }
	m.textarea.SetValue("/plan")
	m.syncSlashMenu()
	if !m.slashMenuOpen() {
		t.Fatal("plan slash menu should be open")
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if cmd != nil || cancelCalls != 0 {
		t.Fatalf("busy plan menu action started/cancelled work: cmd=%v cancelCalls=%d", cmd, cancelCalls)
	}
	if got := m.textarea.Value(); got != "/plan" {
		t.Fatalf("busy plan menu action dropped draft: %q", got)
	}
	if len(m.queue) != 1 || m.queue[0] != "already queued" {
		t.Fatalf("busy plan menu action changed queue: %#v", m.queue)
	}
}

func TestPlanRejectedWithPendingApprovalKeepsStateAndDraft(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t)
	m.deps.PolicyInfo.ApprovalState = state
	m.pendingApproval = &approvalRequestMsg{ID: "pending-plan"}
	m.textarea.SetValue("/plan inspect while approval is pending")
	cancelCalls := 0
	m.turnCancel = func() { cancelCalls++ }
	beforeTurnID := m.turnID

	next, cmd := m.submit(m.textarea.Value())
	m = next.(*model)
	if cmd != nil || m.mode != modeIdle || m.turnID != beforeTurnID || cancelCalls != 0 {
		t.Fatalf("pending plan action changed operation: cmd=%v mode=%s turnID=%d cancelCalls=%d", cmd, modeName(m.mode), m.turnID, cancelCalls)
	}
	if state.InteractiveMode() != "ask" {
		t.Fatalf("pending plan action changed mode to %q", state.InteractiveMode())
	}
	if got := m.textarea.Value(); got != "/plan inspect while approval is pending" {
		t.Fatalf("pending plan action dropped draft: %q", got)
	}
	if m.pendingApproval == nil {
		t.Fatal("pending approval was cleared by rejected plan action")
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
		{input: "/permissions yolo", action: slashPermissions, arg: "yolo", busy: busyInputReject},
		{input: "/plan", action: slashPlan, busy: busyInputReject},
		{input: "/plan inspect files", action: slashPlan, arg: "inspect files", busy: busyInputReject},
		{input: "/plan exit", action: slashPlan, arg: "exit", busy: busyInputReject},
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
