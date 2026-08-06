package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFocusToAction(t *testing.T) {
	if got := focusToAction(approvalChoiceOnce); got != tools.ApprovalOnce {
		t.Fatalf("once = %s", got)
	}
	if got := focusToAction(approvalChoiceSession); got != tools.ApprovalSession {
		t.Fatalf("session = %s", got)
	}
	if got := focusToAction(approvalChoiceDeny); got != tools.ApprovalDeny {
		t.Fatalf("deny = %s", got)
	}
}

func TestApprovalKeyChoices(t *testing.T) {
	m := &model{approvalFocus: approvalChoiceOnce}
	resp := make(chan tools.ApprovalResponse, 1)
	m.pendingApproval = &approvalRequestMsg{
		ID:      "apr-1",
		Request: tools.ApprovalRequest{Command: "echo hi", AllowSession: true},
		Respond: resp,
	}

	// Move to session and confirm.
	m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyRight})
	if m.approvalFocus != approvalChoiceSession {
		t.Fatalf("focus = %d, want session", m.approvalFocus)
	}
	m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case r := <-resp:
		if r.Action != tools.ApprovalSession {
			t.Fatalf("action = %s, want session", r.Action)
		}
	default:
		t.Fatal("expected response")
	}
	if m.hasPendingApproval() {
		t.Fatal("pending should clear")
	}
}

func TestApprovalEscDenies(t *testing.T) {
	m := &model{}
	resp := make(chan tools.ApprovalResponse, 1)
	m.pendingApproval = &approvalRequestMsg{
		ID:      "apr-2",
		Request: tools.ApprovalRequest{Command: "echo hi"},
		Respond: resp,
	}
	m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case r := <-resp:
		if r.Action != tools.ApprovalDeny || r.Reason != tools.ReasonUserDenied {
			t.Fatalf("got %+v", r)
		}
	default:
		t.Fatal("expected deny response")
	}
}

func TestApprovalCancelMatchesID(t *testing.T) {
	m := &model{}
	resp := make(chan tools.ApprovalResponse, 1)
	m.pendingApproval = &approvalRequestMsg{
		ID:      "apr-keep",
		Request: tools.ApprovalRequest{Command: "echo hi"},
		Respond: resp,
	}
	m.handleApprovalCancel(approvalCancelMsg{ID: "apr-other"})
	if !m.hasPendingApproval() {
		t.Fatal("stale cancel must not clear current approval")
	}
	m.handleApprovalCancel(approvalCancelMsg{ID: "apr-keep"})
	if m.hasPendingApproval() {
		t.Fatal("matching cancel should clear")
	}
	// Channel must not receive a late response from cancel path.
	select {
	case r := <-resp:
		t.Fatalf("cancel should not write channel, got %+v", r)
	default:
	}
}

func TestApprovalDigitShortcuts(t *testing.T) {
	m := &model{}
	resp := make(chan tools.ApprovalResponse, 1)
	m.pendingApproval = &approvalRequestMsg{
		ID:      "apr-3",
		Respond: resp,
	}
	m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	select {
	case r := <-resp:
		if r.Action != tools.ApprovalOnce {
			t.Fatalf("got %s", r.Action)
		}
	default:
		t.Fatal("expected once")
	}
}

func TestEscalatedApprovalOnlyOffersOnceOrDeny(t *testing.T) {
	m := &model{}
	resp := make(chan tools.ApprovalResponse, 1)
	req := tools.ApprovalRequest{
		Tool:         "shell",
		Command:      "git push origin main",
		Escalated:    true,
		AllowSession: false,
	}
	m.pendingApproval = &approvalRequestMsg{ID: "apr-host", Request: req, Respond: resp}
	m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyRight})
	if m.approvalFocus != approvalChoiceDeny {
		t.Fatalf("focus = %d, want deny", m.approvalFocus)
	}
	m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-resp:
		if got.Action != tools.ApprovalDeny {
			t.Fatalf("action = %s, want deny", got.Action)
		}
	default:
		t.Fatal("expected deny response")
	}

	modal := renderApprovalModal(100, req, approvalChoiceOnce)
	if !containsAll(modal, "host escalation", "one-time only", "1 once", "2 deny") {
		t.Fatalf("escalated modal missing warning:\n%s", modal)
	}
	if stringContains(modal, "session") {
		t.Fatalf("escalated modal must not offer session:\n%s", modal)
	}
}

func TestEscalatedApprovalModalShowsFullEscapedCommand(t *testing.T) {
	command := strings.Repeat("inspect-prefix ", 12) + "; printf HOST-ESCAPE-TAIL\x1b[2J\nnext"
	req := tools.ApprovalRequest{
		Tool:         "shell",
		Command:      command,
		WorkingDir:   "/tmp/host-only",
		Reason:       "needs host access\nwithout terminal injection",
		Escalated:    true,
		AllowSession: false,
	}
	field := approvalDisplayField("cmd: ", command, 40)
	var reconstructed strings.Builder
	for i, line := range field {
		if i == 0 {
			reconstructed.WriteString(strings.TrimPrefix(line, "cmd: "))
			continue
		}
		reconstructed.WriteString(strings.TrimPrefix(line, strings.Repeat(" ", len("cmd: "))))
	}
	if got, want := reconstructed.String(), approvalDisplayText(command); got != want {
		t.Fatalf("wrapped command = %q, want complete %q", got, want)
	}
	modal := renderApprovalModal(48, req, approvalChoiceOnce)
	if !containsAll(modal,
		"HOST-ESCAPE-TAIL",
		"command_sha256:",
		approvalCommandFingerprint(command)[:12],
	) {
		t.Fatalf("host escalation modal omitted command detail:\n%s", modal)
	}
	if strings.Contains(modal, "…") {
		t.Fatalf("host escalation command must not be truncated:\n%s", modal)
	}
	if strings.Contains(modal, "\x1b[2J") {
		t.Fatalf("modal rendered an unescaped terminal control sequence:\n%s", modal)
	}
}

func TestApprovalDisplayTextEscapesControlsAndBidiFormatting(t *testing.T) {
	got := approvalDisplayText("one\\two\tthree\r\nfour\x1b[2J\u202ereversed")
	for _, want := range []string{"one\\\\two", "\\t", "\\r", "\\n", "\\u001b", "\\u202e"} {
		if !strings.Contains(got, want) {
			t.Fatalf("approval display = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\u202e") {
		t.Fatalf("approval display contains raw formatting controls: %q", got)
	}
}

func TestHostEscalationApprovalPagesFullCommandDetails(t *testing.T) {
	command := strings.Repeat("inspect-prefix ", 30) + "; HOST-PAGED-TAIL"
	m := newModel(Deps{Ctx: context.Background()})
	m.width = 48
	m.height = 24
	m.pendingApproval = &approvalRequestMsg{Request: tools.ApprovalRequest{
		Tool:         "shell",
		Command:      command,
		WorkingDir:   "/tmp/host-only",
		Reason:       "review each command segment",
		Escalated:    true,
		AllowSession: false,
	}}
	m.layout()

	first := m.approvalModalPage()
	if first.maxOffset == 0 || !strings.Contains(first.content, "details:") {
		t.Fatalf("expected paged host approval, got max_offset=%d:\n%s", first.maxOffset, first.content)
	}
	if total := m.viewport.Height + m.approvalModalHeight() + m.textarea.Height() + 5; total > m.height {
		t.Fatalf("approval layout overflows terminal: total=%d height=%d", total, m.height)
	}
	m.handleApprovalKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.approvalScroll == 0 {
		t.Fatal("pgdown did not advance host approval details")
	}
	foundTail := false
	for offset := 0; offset <= first.maxOffset; offset++ {
		m.approvalScroll = offset
		page := m.approvalModalPage().content
		if strings.Contains(page, "T-PAGED-TAIL") {
			foundTail = true
			break
		}
	}
	if !foundTail {
		t.Fatal("no approval detail page rendered the command tail")
	}
}

func TestFormatPermissionsExplainsRulesAndImpact(t *testing.T) {
	info := CommandPolicyInfo{
		Mode:     "ask",
		Approval: "on_request",
	}
	text := info.FormatPermissions()
	if !containsAll(text, "Rules decide authorization", "sandbox: (not configured)", "apply_patch") {
		t.Fatalf("permissions text missing notes:\n%s", text)
	}
}

func TestFormatPermissionsShowsSandboxAndRuntime(t *testing.T) {
	info := CommandPolicyInfo{
		Mode:     "ask",
		Approval: "on_request",
		Sandbox: SandboxInfo{
			Mode:           "workspace-write",
			Backend:        "seatbelt",
			ReadOnlyRoots:  []string{"/usr/local"},
			ProtectedPaths: []string{".git", ".env", "secrets"},
			AllowedDomains: []string{"api.example.test", "packages.example.test"},
			HostEscalation: true,
		},
		Runtime: RuntimeInfo{
			MaxTurnSeconds:                    600,
			MaxModelSteps:                     8,
			MaxToolCalls:                      16,
			MaxConsecutiveEquivalentToolCalls: 3,
		},
	}
	text := info.FormatPermissions()
	if !containsAll(text,
		"mode: workspace-write",
		"backend: seatbelt",
		"read_only_roots: 1",
		"- .git",
		"network: allow:2",
		"host_escalation: once/deny only",
		"max_turn_seconds: 600",
		"max_model_steps: 8",
		"max_tool_calls: 16",
		"max_consecutive_equivalent_tool_calls: 3",
	) {
		t.Fatalf("permissions text missing sandbox details:\n%s", text)
	}
}

func TestSandboxStatusFragments(t *testing.T) {
	policy := CommandPolicyInfo{
		Mode: "ask",
		Sandbox: SandboxInfo{
			Mode:           "read-only",
			Backend:        "bubblewrap",
			AllowedDomains: []string{"api.example.test"},
		},
	}
	if got := policy.StatusFragment(); got != "cmd=ask · sb=ro · sb_backend=bubblewrap · net=allow:1" {
		t.Fatalf("policy status = %q", got)
	}
	status := StatusInfo{
		CmdPolicy: "cmd=auto",
		Sandbox: SandboxInfo{
			Mode:    "workspace-write",
			Backend: "unavailable",
		},
	}
	if got := status.StatusFragment(); got != "cmd=auto · sb=rw · sb_backend=unavailable · net=off" {
		t.Fatalf("status fragment = %q", got)
	}
}

func TestStatusUsesPolicySandboxFallbackAndRuntime(t *testing.T) {
	m := &model{deps: Deps{
		Status: StatusInfo{CmdPolicy: "cmd=ask", MaxModelSteps: 8},
		PolicyInfo: CommandPolicyInfo{
			Sandbox: SandboxInfo{Mode: "workspace-write", Backend: "seatbelt"},
			Runtime: RuntimeInfo{
				MaxTurnSeconds:                    600,
				MaxModelSteps:                     8,
				MaxToolCalls:                      16,
				MaxConsecutiveEquivalentToolCalls: 3,
			},
		},
	}}
	if got := m.statusPolicyFragment(); got != "cmd=ask · sb=rw · sb_backend=seatbelt · net=off" {
		t.Fatalf("status policy = %q", got)
	}
	report := m.statusReport()
	if !containsAll(
		report,
		"max_turn_seconds=600",
		"max_model_steps=8",
		"max_tool_calls=16",
		"max_consecutive_equivalent_tool_calls=3",
	) {
		t.Fatalf("status report missing runtime info:\n%s", report)
	}
	if strings.Count(report, "max_model_steps=") != 1 {
		t.Fatalf("status report duplicated max_model_steps: %s", report)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !stringContains(s, p) {
			return false
		}
	}
	return true
}

func stringContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
