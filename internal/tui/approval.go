package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"eino-local-assistant/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wrap"
)

// defaultApprovalTimeout bounds how long we wait for a human decision.
// Turn cancellation still wins earlier via ctx.
const defaultApprovalTimeout = 10 * time.Minute

// ApprovalBridge implements tools.Approver by posting requests into Bubble Tea.
// Concurrent Requests are serialized so only one modal is active at a time.
type ApprovalBridge struct {
	mu        sync.Mutex
	serial    sync.Mutex // held for the full Request lifecycle
	program   *tea.Program
	closed    bool
	timeout   time.Duration
	ready     chan struct{}
	readyOnce sync.Once
	nextID    atomic.Uint64
}

// NewApprovalBridge constructs a bridge. Call BindProgram once the tea.Program exists.
func NewApprovalBridge() *ApprovalBridge {
	return &ApprovalBridge{
		timeout: defaultApprovalTimeout,
		ready:   make(chan struct{}),
	}
}

// BindProgram attaches the running Bubble Tea program so Request can Send messages.
func (b *ApprovalBridge) BindProgram(p *tea.Program) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.program = p
	b.mu.Unlock()
	b.readyOnce.Do(func() { close(b.ready) })
}

// Close rejects further approvals (e.g. on TUI exit).
func (b *ApprovalBridge) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.closed = true
	b.program = nil
	b.mu.Unlock()
}

// Request implements tools.Approver. It blocks until the user answers, ctx ends, or timeout.
func (b *ApprovalBridge) Request(ctx context.Context, req tools.ApprovalRequest) (tools.ApprovalResponse, error) {
	if b == nil {
		return tools.ApprovalResponse{Action: tools.ApprovalDeny, Reason: tools.ReasonApproverNotReady}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Serialize concurrent tool approvals (single modal + no channel overwrite).
	b.serial.Lock()
	defer b.serial.Unlock()

	// Wait briefly for BindProgram if the first tool call races program start.
	select {
	case <-b.ready:
	case <-ctx.Done():
		return denyFromCtx(ctx), nil
	case <-time.After(5 * time.Second):
		return tools.ApprovalResponse{Action: tools.ApprovalDeny, Reason: tools.ReasonApproverNotReady}, nil
	}

	b.mu.Lock()
	if b.closed || b.program == nil {
		b.mu.Unlock()
		return tools.ApprovalResponse{Action: tools.ApprovalDeny, Reason: tools.ReasonApproverNotReady}, nil
	}
	program := b.program
	timeout := b.timeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	b.mu.Unlock()

	id := fmt.Sprintf("apr-%d", b.nextID.Add(1))
	respCh := make(chan tools.ApprovalResponse, 1)
	program.Send(approvalRequestMsg{ID: id, Request: req, Respond: respCh})

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp := <-respCh:
		if resp.Action == "" {
			resp.Action = tools.ApprovalDeny
		}
		if resp.Action == tools.ApprovalDeny && resp.Reason == "" {
			resp.Reason = tools.ReasonUserDenied
		}
		return resp, nil
	case <-ctx.Done():
		program.Send(approvalCancelMsg{ID: id})
		return denyFromCtx(ctx), nil
	case <-timer.C:
		program.Send(approvalCancelMsg{ID: id})
		return tools.ApprovalResponse{Action: tools.ApprovalDeny, Reason: tools.ReasonApprovalTimedOut}, nil
	}
}

func denyFromCtx(ctx context.Context) tools.ApprovalResponse {
	if ctx != nil && errorsIsDeadline(ctx) {
		return tools.ApprovalResponse{Action: tools.ApprovalDeny, Reason: tools.ReasonApprovalTimedOut}
	}
	return tools.ApprovalResponse{Action: tools.ApprovalDeny, Reason: tools.ReasonApprovalCancelled}
}

func errorsIsDeadline(ctx context.Context) bool {
	return ctx.Err() == context.DeadlineExceeded
}

// approvalRequestMsg is delivered from the tool goroutine into the model.
type approvalRequestMsg struct {
	ID      string
	Request tools.ApprovalRequest
	Respond chan tools.ApprovalResponse
}

// approvalCancelMsg clears a pending modal when the tool side gives up.
// ID must match the active request so a stale cancel cannot drop a newer prompt.
type approvalCancelMsg struct {
	ID string
}

const (
	approvalChoiceOnce = iota
	approvalChoiceSession
	approvalChoiceDeny
)

func (m *model) hasPendingApproval() bool {
	return m.pendingApproval != nil
}

func (m *model) clearPendingApproval(action tools.ApprovalAction) {
	m.clearPendingApprovalWithReason(action, "")
}

func (m *model) clearPendingApprovalWithReason(action tools.ApprovalAction, reason string) {
	if m.pendingApproval == nil {
		return
	}
	if m.pendingApproval.Respond != nil {
		resp := tools.ApprovalResponse{Action: action, Reason: reason}
		if action == tools.ApprovalDeny && resp.Reason == "" {
			resp.Reason = tools.ReasonUserDenied
		}
		select {
		case m.pendingApproval.Respond <- resp:
		default:
		}
	}
	m.pendingApproval = nil
	m.approvalFocus = approvalChoiceOnce
	m.approvalScroll = 0
	m.relayoutApproval()
}

func (m *model) handleApprovalRequest(msg approvalRequestMsg) {
	// Replace only after denying any previous in-model slot (should not happen
	// when bridge serializes; still fail-closed if a race slips through).
	if m.pendingApproval != nil && m.pendingApproval.ID != msg.ID {
		m.clearPendingApprovalWithReason(tools.ApprovalDeny, tools.ReasonApprovalCancelled)
	}
	m.pendingApproval = &approvalRequestMsg{ID: msg.ID, Request: msg.Request, Respond: msg.Respond}
	m.approvalFocus = approvalChoiceOnce
	m.approvalScroll = 0
	m.relayoutApproval()
}

func (m *model) handleApprovalCancel(msg approvalCancelMsg) {
	if m.pendingApproval == nil {
		return
	}
	if msg.ID != "" && m.pendingApproval.ID != msg.ID {
		return
	}
	// Tool side already returned; do not write the response channel.
	m.pendingApproval = nil
	m.approvalFocus = approvalChoiceOnce
	m.approvalScroll = 0
	m.relayoutApproval()
}

func (m *model) relayoutApproval() {
	if m == nil || !m.composerReady {
		return
	}
	m.layout()
	m.refreshViewport()
}

func (m *model) handleApprovalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.hasPendingApproval() {
		return m, nil
	}
	req := m.pendingApproval.Request
	switch msg.Type {
	case tea.KeyPgUp:
		m.scrollApprovalDetails(-m.approvalDetailRows())
		return m, nil
	case tea.KeyPgDown:
		m.scrollApprovalDetails(m.approvalDetailRows())
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.clearPendingApproval(tools.ApprovalDeny)
		return m, nil
	case tea.KeyEnter:
		m.clearPendingApproval(focusToActionForRequest(m.approvalFocus, req))
		return m, nil
	case tea.KeyLeft, tea.KeyUp:
		m.approvalFocus = previousApprovalFocus(m.approvalFocus, req)
		return m, nil
	case tea.KeyRight, tea.KeyDown:
		m.approvalFocus = nextApprovalFocus(m.approvalFocus, req)
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) != 1 {
			return m, nil
		}
		switch msg.Runes[0] {
		case '1', 'o', 'O':
			m.clearPendingApproval(tools.ApprovalOnce)
		case '2', 's', 'S':
			if approvalAllowsSession(req) {
				m.clearPendingApproval(tools.ApprovalSession)
			} else {
				m.clearPendingApproval(tools.ApprovalDeny)
			}
		case '3', 'd', 'D':
			m.clearPendingApproval(tools.ApprovalDeny)
		}
		return m, nil
	}
	return m, nil
}

func (m *model) scrollApprovalDetails(delta int) {
	if !m.hasPendingApproval() || delta == 0 {
		return
	}
	page := m.approvalModalPage()
	if page.maxOffset == 0 {
		return
	}
	m.approvalScroll = max(0, min(page.maxOffset, m.approvalScroll+delta))
	m.relayoutApproval()
}

func focusToAction(focus int) tools.ApprovalAction {
	switch focus {
	case approvalChoiceSession:
		return tools.ApprovalSession
	case approvalChoiceDeny:
		return tools.ApprovalDeny
	default:
		return tools.ApprovalOnce
	}
}

func approvalAllowsSession(req tools.ApprovalRequest) bool {
	return req.AllowSession && !req.Escalated
}

func focusToActionForRequest(focus int, req tools.ApprovalRequest) tools.ApprovalAction {
	if focus == approvalChoiceSession && !approvalAllowsSession(req) {
		return tools.ApprovalDeny
	}
	return focusToAction(focus)
}

func previousApprovalFocus(focus int, req tools.ApprovalRequest) int {
	if focus <= approvalChoiceOnce {
		return approvalChoiceOnce
	}
	if focus == approvalChoiceDeny && !approvalAllowsSession(req) {
		return approvalChoiceOnce
	}
	return focus - 1
}

func nextApprovalFocus(focus int, req tools.ApprovalRequest) int {
	if focus >= approvalChoiceDeny {
		return approvalChoiceDeny
	}
	if focus == approvalChoiceOnce && !approvalAllowsSession(req) {
		return approvalChoiceDeny
	}
	return focus + 1
}

type approvalChoice struct {
	focus int
	label string
}

func approvalChoices(req tools.ApprovalRequest) []approvalChoice {
	choices := []approvalChoice{{focus: approvalChoiceOnce, label: "once"}}
	if approvalAllowsSession(req) {
		choices = append(choices, approvalChoice{focus: approvalChoiceSession, label: "session"})
	}
	return append(choices, approvalChoice{focus: approvalChoiceDeny, label: "deny"})
}

type approvalModalPage struct {
	content   string
	maxOffset int
}

// renderApprovalModal renders every approval detail for lightweight callers
// and focused tests. The interactive model uses the paged variant so a hostile
// long command cannot push the confirm controls off a small terminal.
func renderApprovalModal(width int, req tools.ApprovalRequest, focus int) string {
	return renderApprovalModalPage(width, 0, req, focus, 0).content
}

func renderApprovalModalPage(width, detailRows int, req tools.ApprovalRequest, focus, offset int) approvalModalPage {
	if width < 40 {
		width = 40
	}
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("214")).
		Foreground(lipgloss.Color("252")).
		Padding(0, 1).
		Width(width - 2)

	header, details := approvalModalSections(width, req)
	page := approvalModalPage{}
	if detailRows > 0 && len(details) > detailRows {
		detailCount := len(details)
		page.maxOffset = detailCount - detailRows
		offset = max(0, min(offset, page.maxOffset))
		end := min(detailCount, offset+detailRows)
		details = details[offset:end]
		header = append(header, fmt.Sprintf("details: %d-%d/%d (pgup/pgdn review)", offset+1, end, detailCount))
	}

	var rendered []string
	for i, choice := range approvalChoices(req) {
		label := fmt.Sprintf("%d %s", i+1, choice.label)
		if choice.focus == focus {
			rendered = append(rendered, lipgloss.NewStyle().
				Foreground(lipgloss.Color("0")).
				Background(lipgloss.Color("214")).
				Bold(true).
				Render(" "+label+" "))
		} else {
			rendered = append(rendered, " "+label+" ")
		}
	}

	footer := "enter confirm · ←/→ select · esc deny"
	lines := append(header, details...)
	lines = append(lines, "", strings.Join(rendered, "  "), footer)
	body := strings.Join(lines, "\n")
	page.content = border.Render(body)
	return page
}

func approvalModalSections(width int, req tools.ApprovalRequest) (header, details []string) {
	if width < 40 {
		width = 40
	}
	contentWidth := max(16, width-8)
	title := "Allow " + approvalDisplayText(req.Tool) + "?"
	if req.Escalated {
		title = "Allow host escalation for " + approvalDisplayText(req.Tool) + "?"
		header = append(header, title)
		header = append(header, approvalDisplayField("risk: ", "outside sandbox; one-time only; detached descendants may survive", contentWidth)...)
		header = append(header, approvalDisplayField("command_sha256: ", approvalCommandFingerprint(req.Command), contentWidth)...)
	} else {
		header = append(header, title)
	}
	details = append(details, approvalDisplayField("cwd: ", req.WorkingDir, contentWidth)...)
	details = append(details, approvalDisplayField("cmd: ", req.Command, contentWidth)...)
	details = append(details, approvalDisplayField("reason: ", req.Reason, contentWidth)...)
	return header, details
}

// approvalDisplayText makes model-controlled fields safe to render in a
// terminal modal. In particular, escape sequences and newlines must be shown
// literally so they cannot hide or reformat the command a person approves.
func approvalDisplayText(value string) string {
	if value == "" {
		return "(none)"
	}
	var builder strings.Builder
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString("\\\\")
		case '\n':
			builder.WriteString("\\n")
		case '\r':
			builder.WriteString("\\r")
		case '\t':
			builder.WriteString("\\t")
		default:
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				fmt.Fprintf(&builder, "\\u%04x", r)
				continue
			}
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func approvalDisplayField(label, value string, width int) []string {
	value = approvalDisplayText(value)
	available := max(8, width-lipgloss.Width(label))
	wrapped := strings.Split(wrapApprovalText(value, available), "\n")
	lines := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		if i == 0 {
			lines = append(lines, label+line)
			continue
		}
		lines = append(lines, strings.Repeat(" ", lipgloss.Width(label))+line)
	}
	return lines
}

func wrapApprovalText(value string, width int) string {
	wrapper := wrap.NewWriter(width)
	// Whitespace is part of shell syntax; wrap only inserts visual line breaks
	// and must never discard it from the approval representation.
	wrapper.PreserveSpace = true
	_, _ = wrapper.Write([]byte(value))
	return wrapper.String()
}

func approvalCommandFingerprint(command string) string {
	digest := sha256.Sum256([]byte(command))
	return hex.EncodeToString(digest[:])
}

// SandboxInfo is display-only sandbox posture for /permissions, /status, and
// the compact status bar. It deliberately uses strings and slices so the TUI
// does not need to know which OS backend produced the state.
type SandboxInfo struct {
	// Mode is normally workspace-write or read-only.
	Mode string
	// Backend is a short availability label such as seatbelt, bubblewrap, or unavailable.
	Backend string
	// ReadOnlyRoots are host roots exposed read-only to sandbox workers.
	ReadOnlyRoots []string
	// ProtectedPaths contains effective built-in and user-added deny paths.
	ProtectedPaths []string
	// AllowedDomains is the exact HTTP(S) egress allowlist; empty means no network.
	AllowedDomains []string
	// HostEscalation reports whether shell can request a one-time host escape.
	HostEscalation bool
	// Yolo reports that the configured worker boundary is deliberately bypassed
	// by the explicit yolo mode. It is not inferred from backend availability.
	Yolo bool
}

// Configured reports whether a caller supplied any sandbox state.
func (info SandboxInfo) Configured() bool {
	return strings.TrimSpace(info.Mode) != "" ||
		strings.TrimSpace(info.Backend) != "" ||
		len(info.ReadOnlyRoots) > 0 ||
		len(info.ProtectedPaths) > 0 ||
		len(info.AllowedDomains) > 0 ||
		info.HostEscalation ||
		info.Yolo
}

func (info SandboxInfo) modeLabel() string {
	switch strings.ToLower(strings.TrimSpace(info.Mode)) {
	case "workspace-write":
		return "rw"
	case "read-only":
		return "ro"
	default:
		return ""
	}
}

func (info SandboxInfo) statusFragments() []string {
	if !info.Configured() {
		return nil
	}
	if info.Yolo {
		return []string{"YOLO=UNSAFE", "sb=off", "sb_backend=host", "net=host"}
	}
	fragments := make([]string, 0, 3)
	if mode := info.modeLabel(); mode != "" {
		fragments = append(fragments, "sb="+mode)
	}
	if backend := strings.TrimSpace(info.Backend); backend != "" {
		fragments = append(fragments, "sb_backend="+backend)
	}
	if len(info.AllowedDomains) == 0 {
		fragments = append(fragments, "net=off")
	} else {
		fragments = append(fragments, fmt.Sprintf("net=allow:%d", len(info.AllowedDomains)))
	}
	return fragments
}

// RuntimeInfo is display-only per-turn guardrail state. Zero values mean the
// caller has no runtime data to display.
type RuntimeInfo struct {
	MaxTurnSeconds int
	MaxReactSteps  int
	MaxToolCalls   int
}

// Configured reports whether any runtime limit is known.
func (info RuntimeInfo) Configured() bool {
	return info.MaxTurnSeconds > 0 || info.MaxReactSteps > 0 || info.MaxToolCalls > 0
}

// CommandPolicyInfo is display-only policy state for /permissions and the status bar.
type CommandPolicyInfo struct {
	// Mode is "ask", "auto", "plan", or the explicit dangerous "yolo" mode.
	Mode string
	// Approval is the raw config value (on_request|never).
	Approval string
	// ApprovalState is the process-local mode used by registered side-effecting
	// tools. When present, it is the source of truth for display and switching.
	ApprovalState *tools.ApprovalState
	Profile       string
	// WorkspaceOnly and WorkspaceRoot describe path clamping.
	WorkspaceOnly bool
	WorkspaceRoot string
	// Permissions is Claude-style allow/ask/deny.
	Permissions *tools.PermissionSet
	// SessionAllows is optional session memory.
	SessionAllows *tools.SessionAllowlist
	// SessionDenies is optional session memory for user-denied rule keys.
	SessionDenies *tools.SessionDenylist
	// Sandbox is the currently configured worker boundary.
	Sandbox SandboxInfo
	// Runtime is the configured per-turn execution budget.
	Runtime RuntimeInfo
}

// FormatPermissions builds the /permissions report.
func (info CommandPolicyInfo) FormatPermissions() string {
	if info.Mode == "" && info.Approval == "" && info.ApprovalState == nil && info.Permissions == nil &&
		!info.Sandbox.Configured() && !info.Runtime.Configured() && !info.yoloActive() {
		return "tool permissions: (not configured)"
	}
	mode := info.Mode
	approvalPolicy := info.Approval
	if info.ApprovalState != nil {
		mode = info.ApprovalState.InteractiveMode()
	} else if mode == "" {
		if info.Approval == string(tools.ApprovalNever) {
			mode = "auto"
		} else {
			mode = "ask"
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "tool permissions (Codex subset)\n")
	fmt.Fprintf(&b, "  mode: %s (%s)\n", mode, approvalPolicy)
	if info.yoloActive() {
		fmt.Fprintf(&b, "  !!! %s !!!\n", tools.YoloModeWarning)
		fmt.Fprintf(&b, "  approval: bypassed (no interactive prompts)\n")
		fmt.Fprintf(&b, "  sandbox: bypassed (direct host execution)\n")
		fmt.Fprintf(&b, "  hard_denies: enforced\n")
		fmt.Fprintf(&b, "  workspace_path_safety: enforced at tool boundary\n")
	}
	fmt.Fprintf(&b, "  profile: %s\n", info.Profile)
	fmt.Fprintf(&b, "  workspace_only: %v\n", info.WorkspaceOnly)
	if info.WorkspaceRoot != "" {
		fmt.Fprintf(&b, "  workspace: %s\n", info.WorkspaceRoot)
	}
	info.writeSandboxReport(&b)
	info.writeRuntimeReport(&b)
	fmt.Fprintf(&b, "  approval_policy: %s\n", approvalPolicy)
	if info.Permissions != nil {
		fmt.Fprintf(&b, "  permissions (Claude-style):\n")
		for _, line := range info.Permissions.SummaryLines() {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	if info.SessionAllows != nil {
		keys := info.SessionAllows.List()
		if len(keys) == 0 {
			fmt.Fprintf(&b, "  session allows: (none)\n")
		} else {
			fmt.Fprintf(&b, "  session allows:\n")
			for _, k := range keys {
				fmt.Fprintf(&b, "    - %s\n", k)
			}
		}
	}
	if info.SessionDenies != nil {
		keys := info.SessionDenies.List()
		if len(keys) == 0 {
			fmt.Fprintf(&b, "  session denies: (none)\n")
		} else {
			fmt.Fprintf(&b, "  session denies:\n")
			for _, k := range keys {
				fmt.Fprintf(&b, "    - %s\n", k)
			}
		}
	}
	fmt.Fprintf(&b, "\nTool surface (Codex subset): shell · apply_patch · get_current_time · read_artifact.\n")
	fmt.Fprintf(&b, "Hard deny cannot be bypassed by session allow.\n")
	fmt.Fprintf(&b, "User deny sets stop_retrying and session-denies the same rule_key (no re-prompt).\n")
	fmt.Fprintf(&b, "Prefer apply_patch over shell for file create/update/delete.\n")
	fmt.Fprintf(&b, "Shell allow is downgraded to ask when shell metacharacters are present.\n")
	fmt.Fprintf(&b, "Sandboxed tools fail closed when their OS backend is unavailable; host escalation is never remembered.")
	return strings.TrimRight(b.String(), "\n")
}

func (info CommandPolicyInfo) writeSandboxReport(b *strings.Builder) {
	if info.yoloActive() {
		fmt.Fprintln(b, "  sandbox: BYPASSED by YOLO (direct host execution)")
		fmt.Fprintln(b, "    configured sandbox remains available after leaving yolo")
		return
	}
	if !info.Sandbox.Configured() {
		fmt.Fprintln(b, "  sandbox: (not configured)")
		return
	}
	sandbox := info.Sandbox
	mode := strings.TrimSpace(sandbox.Mode)
	if mode == "" {
		mode = "(unknown)"
	}
	backend := strings.TrimSpace(sandbox.Backend)
	if backend == "" {
		backend = "(unknown)"
	}
	fmt.Fprintln(b, "  sandbox:")
	fmt.Fprintf(b, "    mode: %s\n", mode)
	fmt.Fprintf(b, "    backend: %s\n", backend)
	fmt.Fprintf(b, "    read_only_roots: %d\n", len(sandbox.ReadOnlyRoots))
	if len(sandbox.ProtectedPaths) == 0 {
		fmt.Fprintln(b, "    protected_paths: (none)")
	} else {
		fmt.Fprintln(b, "    protected_paths:")
		for _, protected := range sandbox.ProtectedPaths {
			fmt.Fprintf(b, "      - %s\n", protected)
		}
	}
	if len(sandbox.AllowedDomains) == 0 {
		fmt.Fprintln(b, "    network: off")
	} else {
		fmt.Fprintf(b, "    network: allow:%d\n", len(sandbox.AllowedDomains))
		for _, domain := range sandbox.AllowedDomains {
			fmt.Fprintf(b, "      - %s\n", domain)
		}
	}
	if sandbox.HostEscalation {
		fmt.Fprintln(b, "    host_escalation: once/deny only (never remembered)")
	} else {
		fmt.Fprintln(b, "    host_escalation: disabled")
	}
}

func (info CommandPolicyInfo) writeRuntimeReport(b *strings.Builder) {
	if !info.Runtime.Configured() {
		return
	}
	fmt.Fprintln(b, "  runtime:")
	if info.Runtime.MaxTurnSeconds > 0 {
		fmt.Fprintf(b, "    max_turn_seconds: %d\n", info.Runtime.MaxTurnSeconds)
	}
	if info.Runtime.MaxReactSteps > 0 {
		fmt.Fprintf(b, "    max_react_steps: %d\n", info.Runtime.MaxReactSteps)
	}
	if info.Runtime.MaxToolCalls > 0 {
		fmt.Fprintf(b, "    max_tool_calls: %d\n", info.Runtime.MaxToolCalls)
	}
}

// CmdPolicyFragment returns the status-bar badge (cmd=ask|auto|plan).
func (info CommandPolicyInfo) CmdPolicyFragment() string {
	if info.ApprovalState != nil {
		if info.yoloActive() {
			return "cmd=yolo"
		}
		return "cmd=" + info.ApprovalState.InteractiveMode()
	}
	if info.yoloActive() {
		return "cmd=yolo"
	}
	if info.Mode != "" {
		return "cmd=" + info.Mode
	}
	if info.Approval == string(tools.ApprovalNever) {
		return "cmd=auto"
	}
	if info.Approval == string(tools.ApprovalOnRequest) || info.Approval != "" {
		return "cmd=ask"
	}
	return ""
}

// StatusFragment returns compact policy and sandbox state for the status bar.
func (info CommandPolicyInfo) StatusFragment() string {
	if info.yoloActive() {
		return strings.Join([]string{info.CmdPolicyFragment(), "YOLO=UNSAFE", "sb=off", "sb_backend=host", "net=host"}, " · ")
	}
	fragments := make([]string, 0, 4)
	if cmd := info.CmdPolicyFragment(); cmd != "" {
		fragments = append(fragments, cmd)
	}
	fragments = append(fragments, info.Sandbox.statusFragments()...)
	return strings.Join(fragments, " · ")
}

func (info CommandPolicyInfo) yoloActive() bool {
	if info.ApprovalState != nil && info.ApprovalState.Mode() == tools.ApprovalYolo {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(info.Mode), "yolo")
}
