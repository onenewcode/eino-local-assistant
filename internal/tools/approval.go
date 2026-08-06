package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ApprovalMode controls how DecisionAsk is handled at the tool boundary.
type ApprovalMode string

const (
	// ApprovalOnRequest prompts the user (or denys when no Approver is wired).
	ApprovalOnRequest ApprovalMode = "on_request"
	// ApprovalNever auto-allows ask decisions (evaluated deny rules still apply).
	ApprovalNever ApprovalMode = "never"
	// ApprovalPlan is a read-only interactive phase. Tool-specific gates must
	// enforce it; it is not an alias for either approval policy.
	ApprovalPlan ApprovalMode = "plan"
	// ApprovalYolo is an explicitly selected dangerous mode. It bypasses
	// approval prompts and the normal OS sandbox. Remaining command and path
	// checks are defense-in-depth, not a host security boundary.
	ApprovalYolo ApprovalMode = "yolo"
)

// YoloModeWarning is shown wherever the dangerous mode becomes active. Keep
// the wording stable so logs and tests can identify the bypass unambiguously.
const YoloModeWarning = "WARNING: YOLO MODE ACTIVE - approvals and OS sandbox bypassed; remaining command and path checks are best-effort, not a security boundary"

// ApprovalState is the process-local approval mode shared by side-effecting
// tools. The mutex makes reads made by tool calls race-free while the TUI
// changes the mode between calls.
type ApprovalState struct {
	mu   sync.RWMutex
	mode ApprovalMode
}

// NewApprovalState creates a shared approval state from a static mode.
func NewApprovalState(mode ApprovalMode) (*ApprovalState, error) {
	state := &ApprovalState{}
	if err := state.Set(mode); err != nil {
		return nil, err
	}
	return state, nil
}

// Mode returns the current canonical approval mode.
func (s *ApprovalState) Mode() ApprovalMode {
	if s == nil {
		return ApprovalOnRequest
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.mode == "" {
		return ApprovalOnRequest
	}
	return s.mode
}

// InteractiveMode returns the TUI-facing name for the current mode.
func (s *ApprovalState) InteractiveMode() string {
	switch s.Mode() {
	case ApprovalNever:
		return "auto"
	case ApprovalPlan:
		return "plan"
	case ApprovalYolo:
		return "yolo"
	default:
		return "ask"
	}
}

// Set updates the state using a canonical or TUI-facing mode name.
func (s *ApprovalState) Set(mode ApprovalMode) error {
	if s == nil {
		return fmt.Errorf("approval state is nil")
	}
	normalized := strings.ToLower(strings.TrimSpace(string(mode)))
	switch normalized {
	case "", string(ApprovalOnRequest), "on-request", "ask":
		normalized = string(ApprovalOnRequest)
	case string(ApprovalNever), "auto":
		normalized = string(ApprovalNever)
	case string(ApprovalPlan):
		normalized = string(ApprovalPlan)
	case string(ApprovalYolo):
		normalized = string(ApprovalYolo)
	default:
		return fmt.Errorf("approval mode must be ask, auto, plan, or yolo, got %q", mode)
	}
	s.mu.Lock()
	s.mode = ApprovalMode(normalized)
	s.mu.Unlock()
	return nil
}

// SetInteractiveMode updates the state using only the supported TUI modes.
func (s *ApprovalState) SetInteractiveMode(mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "ask" && mode != "auto" && mode != "plan" {
		return fmt.Errorf("permission mode must be ask, auto, or plan, got %q", mode)
	}
	return s.Set(ApprovalMode(mode))
}

// SetYolo enables the dangerous mode through an explicit caller-owned path.
// It is deliberately not accepted by SetInteractiveMode, so Shift+Tab cannot
// enter yolo as part of the ordinary safe mode cycle.
func (s *ApprovalState) SetYolo() error {
	return s.Set(ApprovalYolo)
}

func isYoloApprovalMode(mode ApprovalMode) bool {
	return NormalizeApprovalMode(string(mode)) == ApprovalYolo
}

func effectiveApprovalMode(static ApprovalMode, state *ApprovalState) ApprovalMode {
	if state != nil {
		return state.Mode()
	}
	return static
}

// NormalizeApprovalMode maps empty / Codex "on-request" to on_request.
func NormalizeApprovalMode(mode string) ApprovalMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", string(ApprovalOnRequest), "on-request":
		return ApprovalOnRequest
	case string(ApprovalNever):
		return ApprovalNever
	case string(ApprovalPlan):
		return ApprovalPlan
	case string(ApprovalYolo):
		return ApprovalYolo
	default:
		return ApprovalMode(strings.ToLower(strings.TrimSpace(mode)))
	}
}

// ApprovalAction is the user's choice for an ask decision.
type ApprovalAction string

const (
	ApprovalOnce    ApprovalAction = "once"
	ApprovalSession ApprovalAction = "session"
	ApprovalDeny    ApprovalAction = "deny"
)

// Well-known deny / approval reasons (stable strings for models and tests).
const (
	ReasonPolicyDenied = "policy_denied"
	// ReasonPlanReadOnly is stable so a model and an auditor can distinguish a
	// phase gate from an ordinary approval denial.
	ReasonPlanReadOnly      = "plan_read_only"
	ReasonUserDenied        = "user_denied"
	ReasonUserDeniedSession = "user_denied_session"
	ReasonApprovalTimedOut  = "approval_timed_out"
	ReasonApprovalCancelled = "approval_cancelled"
	ReasonApproverNotReady  = "approver_not_ready"
	ReasonApproverMissing   = "approval required but no approver is configured"
	ReasonWorkspaceOnly     = "workspace_only"
	// ReasonWorkspaceSymlink identifies a path that would escape through a
	// workspace-internal symlink.
	ReasonWorkspaceSymlink = "workspace_symlink"
	// ReasonSandboxUnavailable indicates that a strict tool sandbox could not
	// be started. Callers must not silently retry the command on the host.
	ReasonSandboxUnavailable = "sandbox_unavailable"
	// ReasonHostEscalationDenied identifies a user rejection of a one-shot
	// request to leave the sandbox.
	ReasonHostEscalationDenied = "host_escalation_denied"
	ReasonUnknownApproval      = "unknown_approval_action"
	// ReasonStopRetryingSuffix is attached after repeated soft denials of the same rule_key.
	ReasonStopRetryingSuffix = "stop_retrying: repeated denials for this command prefix; change approach or ask the user—do not spam the same blocked prefix"
	// ReasonUserDeniedNoRetry tells the model a human rejection is final for this action.
	// Unlike policy_denied, equivalent shell rewrites (touch→redirect, other write tools) are not appropriate.
	ReasonUserDeniedNoRetry = "stop_retrying: the human rejected this action; do not retry with equivalent shell forms or apply_patch workarounds; acknowledge the rejection and ask what they want instead"
)

// defaultMaxDenyStreak is how many consecutive soft denials for the same
// rule_key trigger a stop_retrying hint (anti R2 storm within MaxStep).
const defaultMaxDenyStreak = 3

// ApprovalRequest is shown to the user when policy returns ask.
type ApprovalRequest struct {
	Tool       string
	Command    string
	WorkingDir string
	Reason     string
	RuleID     string
	// RuleKey is the session-allow fingerprint for "allow for session".
	RuleKey string
	// Escalated marks a request to run a command outside the configured
	// sandbox. It is intentionally displayed as a higher-risk approval.
	Escalated bool
	// AllowSession controls whether the UI may offer an "allow for session"
	// response. Host escalations are always one-shot.
	AllowSession bool
}

// ApprovalResponse is the user's answer to an ApprovalRequest.
type ApprovalResponse struct {
	Action ApprovalAction
	// Reason is set by the Approver on automatic deny (timeout, cancel, not ready).
	Reason string
}

// Approver blocks until the user (or a test double) answers an ask decision.
type Approver interface {
	Request(ctx context.Context, req ApprovalRequest) (ApprovalResponse, error)
}

// SessionAllowlist remembers rule keys approved for the current process/session.
type SessionAllowlist struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

// NewSessionAllowlist returns an empty allowlist.
func NewSessionAllowlist() *SessionAllowlist {
	return &SessionAllowlist{keys: make(map[string]struct{})}
}

// Allow records a session-scoped allow for key.
func (s *SessionAllowlist) Allow(key string) {
	if s == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = make(map[string]struct{})
	}
	s.keys[key] = struct{}{}
}

// Contains reports whether key was session-allowed.
func (s *SessionAllowlist) Contains(key string) bool {
	if s == nil {
		return false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.keys[key]
	return ok
}

// List returns sorted keys for stable /permissions display.
func (s *SessionAllowlist) List() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.keys))
	for k := range s.keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SessionDenylist remembers rule keys the user explicitly denied this session.
// Re-requests with the same RuleKey soft-deny without re-prompting.
// This does not cover every equivalent rewrite (touch vs echo>); user_denied
// results still set stop_retrying so the model is told not to rewrite for effect.
type SessionDenylist struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

// NewSessionDenylist returns an empty denylist.
func NewSessionDenylist() *SessionDenylist {
	return &SessionDenylist{keys: make(map[string]struct{})}
}

// Deny records a session-scoped deny for key.
func (s *SessionDenylist) Deny(key string) {
	if s == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys == nil {
		s.keys = make(map[string]struct{})
	}
	s.keys[key] = struct{}{}
}

// Contains reports whether key was session-denied by the user.
func (s *SessionDenylist) Contains(key string) bool {
	if s == nil {
		return false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.keys[key]
	return ok
}

// Clear removes a session deny (e.g. after an explicit later allow).
func (s *SessionDenylist) Clear(key string) {
	if s == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
}

// List returns sorted keys for /permissions display.
func (s *SessionDenylist) List() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.keys))
	for k := range s.keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DenyStreak tracks consecutive soft denials per rule_key to hint the model
// to stop R2-retrying the same blocked command prefix.
type DenyStreak struct {
	mu        sync.Mutex
	counts    map[string]int
	maxStreak int
}

// NewDenyStreak returns a tracker; limit<=0 uses defaultMaxDenyStreak.
func NewDenyStreak(limit int) *DenyStreak {
	if limit <= 0 {
		limit = defaultMaxDenyStreak
	}
	return &DenyStreak{counts: make(map[string]int), maxStreak: limit}
}

// RecordDeny increments the streak for key and reports whether to attach stop_retrying.
func (d *DenyStreak) RecordDeny(key string) (count int, stopRetrying bool) {
	if d == nil {
		return 0, false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.counts == nil {
		d.counts = make(map[string]int)
	}
	d.counts[key]++
	count = d.counts[key]
	return count, count >= d.maxStreak
}

// Reset clears the streak after a successful authorization/execution path.
func (d *DenyStreak) Reset(key string) {
	if d == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.counts, key)
}

// RuleKey builds a stable session-allow fingerprint for a command.
// Uses the first two tokens when available so "git push origin" and
// "git push --force" share a key without allowing all of "git".
func RuleKey(tool, command, workspaceRoot string) string {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "shell"
	}
	tokens := strings.Fields(strings.TrimSpace(command))
	prefix := ""
	switch {
	case len(tokens) >= 2:
		prefix = tokens[0] + " " + tokens[1]
	case len(tokens) == 1:
		prefix = tokens[0]
	default:
		prefix = "(empty)"
	}
	ws := strings.TrimSpace(workspaceRoot)
	if ws == "" {
		ws = "(no-workspace)"
	}
	return tool + "|" + prefix + "|" + ws
}

// PathRuleKey builds a session-allow fingerprint for path-scoped tools
// (apply_patch). Uses the canonical absolute path.
func PathRuleKey(tool, absPath, workspaceRoot string) string {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		tool = "path"
	}
	path := canonicalizePath(absPath)
	if path == "" {
		path = "(empty)"
	}
	ws := strings.TrimSpace(workspaceRoot)
	if ws == "" {
		ws = "(no-workspace)"
	}
	return tool + "|" + path + "|" + ws
}

// AutoApprover always returns the configured action (for tests / never mode helpers).
type AutoApprover struct {
	Action ApprovalAction
	Reason string
}

// Request implements Approver.
func (a AutoApprover) Request(_ context.Context, _ ApprovalRequest) (ApprovalResponse, error) {
	action := a.Action
	if action == "" {
		action = ApprovalOnce
	}
	return ApprovalResponse{Action: action, Reason: a.Reason}, nil
}
