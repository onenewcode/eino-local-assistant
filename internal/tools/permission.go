package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// PermissionMode controls whether a mutating or host-executing tool may run.
type PermissionMode string

const (
	PermissionUnrestricted PermissionMode = "unrestricted"
	PermissionConfirm      PermissionMode = "confirm"
	PermissionDeny         PermissionMode = "deny"
	// PermissionPlan allows read-only tools while denying every permission-gated side effect.
	PermissionPlan PermissionMode = "plan"
)

// RiskLevel is an advisory classification shown before a permission decision.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// RiskPolicy controls what happens to high-risk requests after classification.
type RiskPolicy string

const (
	RiskPolicyAdvisory RiskPolicy = "advisory"
	RiskPolicyConfirm  RiskPolicy = "confirm"
	RiskPolicyDeny     RiskPolicy = "deny"
)

// PermissionRequest describes the side effect about to be performed.
type PermissionRequest struct {
	Tool    string
	Action  string
	Detail  string
	Preview string
	Risk    RiskLevel
}

// ClassifyCommand conservatively labels shell commands for human review. It
// never grants or denies permission by itself.
func ClassifyCommand(command string) RiskLevel {
	segments, operators, valid := shellCommandParts(command)
	if !valid || len(segments) == 0 {
		return RiskMedium
	}
	for _, operator := range operators {
		if strings.HasPrefix(operator, ">") || strings.HasPrefix(operator, "<") || operator == "$(" || operator == "`" {
			return RiskHigh
		}
	}
	for _, segment := range segments {
		if commandIsHighRisk(segment) {
			return RiskHigh
		}
	}
	if hasPipe(operators) && lastCommandIsShell(segments) {
		return RiskHigh
	}
	if len(segments) == 1 && len(operators) == 0 && commandIsReadOnly(segments[0]) {
		return RiskLow
	}
	return RiskMedium
}

// shellCommandParts performs only the lexical work needed for an advisory
// risk label. It deliberately does not try to emulate a shell or validate
// whether a command will actually execute.
func shellCommandParts(command string) ([][]string, []string, bool) {
	var segments [][]string
	var current []string
	var word strings.Builder
	var operators []string
	quote := rune(0)
	escaped := false
	flushWord := func() {
		if word.Len() > 0 {
			current = append(current, word.String())
			word.Reset()
		}
	}
	flushSegment := func() {
		flushWord()
		if len(current) > 0 {
			segments = append(segments, current)
			current = nil
		}
	}
	input := []rune(command)
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if escaped {
			word.WriteRune(ch)
			escaped = false
			continue
		}
		if quote == '\'' {
			if ch == quote {
				quote = 0
			} else {
				word.WriteRune(ch)
			}
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else {
				word.WriteRune(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case ' ', '\t', '\r', '\n':
			flushWord()
		case ';':
			flushSegment()
			operators = append(operators, ";")
		case '|':
			flushSegment()
			if i+1 < len(input) && input[i+1] == '|' {
				i++
				operators = append(operators, "||")
			} else {
				operators = append(operators, "|")
			}
		case '&':
			flushSegment()
			if i+1 < len(input) && input[i+1] == '&' {
				i++
				operators = append(operators, "&&")
			} else {
				operators = append(operators, "&")
			}
		case '>', '<':
			flushWord()
			operator := string(ch)
			if i+1 < len(input) && input[i+1] == ch {
				i++
				operator += string(ch)
			}
			operators = append(operators, operator)
		case '$':
			if i+1 < len(input) && input[i+1] == '(' {
				operators = append(operators, "$(")
			}
			word.WriteRune(ch)
		case '`':
			operators = append(operators, "`")
		default:
			word.WriteRune(ch)
		}
	}
	if escaped || quote != 0 {
		return nil, nil, false
	}
	flushSegment()
	return segments, operators, true
}

func commandIsHighRisk(segment []string) bool {
	if len(segment) == 0 {
		return false
	}
	command := strings.ToLower(segment[0])
	if command == "sudo" || command == "mkfs" || command == "dd" {
		return true
	}
	if command == "rm" {
		for _, arg := range segment[1:] {
			if strings.Contains(arg, "r") || strings.Contains(arg, "R") || strings.Contains(arg, "f") || strings.Contains(arg, "F") {
				return true
			}
		}
	}
	if command == "git" && len(segment) > 1 {
		subcommand := strings.ToLower(segment[1])
		return subcommand == "reset" || subcommand == "clean" || subcommand == "restore"
	}
	return command == "chmod" || command == "chown"
}

func commandIsReadOnly(segment []string) bool {
	if len(segment) == 0 {
		return false
	}
	command := strings.ToLower(segment[0])
	for _, readOnly := range []string{"pwd", "ls", "cat", "head", "tail", "rg", "grep", "find"} {
		if command == readOnly {
			return true
		}
	}
	if command == "git" && len(segment) > 1 {
		for _, subcommand := range []string{"status", "diff", "log", "show"} {
			if strings.EqualFold(segment[1], subcommand) {
				return true
			}
		}
	}
	if command == "go" && len(segment) > 1 {
		return strings.EqualFold(segment[1], "test") || strings.EqualFold(segment[1], "vet")
	}
	return false
}

func hasPipe(operators []string) bool {
	for _, operator := range operators {
		if operator == "|" {
			return true
		}
	}
	return false
}

func lastCommandIsShell(segments [][]string) bool {
	if len(segments) == 0 || len(segments[len(segments)-1]) == 0 {
		return false
	}
	switch strings.ToLower(segments[len(segments)-1][0]) {
	case "sh", "bash", "zsh", "fish", "dash":
		return true
	default:
		return false
	}
}

// PermissionHandler is supplied by an interactive host or an embedding app.
// Returning false prevents the tool side effect.
type PermissionHandler func(context.Context, PermissionRequest) (bool, error)

// SwitchablePermissionHandler changes the effective mode between turns while
// preserving the configured delegate for normal (non-plan) operation.
type SwitchablePermissionHandler struct {
	mu       sync.RWMutex
	mode     PermissionMode
	normal   PermissionMode
	delegate PermissionHandler
}

// NewSwitchablePermissionHandler creates a handler suitable for an interactive
// host that exposes a runtime plan-mode command.
func NewSwitchablePermissionHandler(mode PermissionMode, delegate PermissionHandler) (*SwitchablePermissionHandler, error) {
	handler := &SwitchablePermissionHandler{delegate: delegate}
	if err := handler.SetMode(mode); err != nil {
		return nil, err
	}
	handler.mu.Lock()
	handler.normal = handler.mode
	handler.mu.Unlock()
	return handler, nil
}

// SetMode changes the mode used by future permission checks.
func (h *SwitchablePermissionHandler) SetMode(mode PermissionMode) error {
	mode = PermissionMode(strings.ToLower(strings.TrimSpace(string(mode))))
	switch mode {
	case "":
		mode = PermissionUnrestricted
	case PermissionUnrestricted, PermissionConfirm, PermissionDeny, PermissionPlan:
	default:
		return fmt.Errorf("unsupported permission mode %q", mode)
	}
	h.mu.Lock()
	h.mode = mode
	h.mu.Unlock()
	return nil
}

// Mode returns the current effective permission mode.
func (h *SwitchablePermissionHandler) Mode() PermissionMode {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.mode
}

// ExitPlan restores the mode present when the handler was constructed. A
// session started in plan mode cannot silently opt into a looser mode.
func (h *SwitchablePermissionHandler) ExitPlan() error {
	h.mu.RLock()
	normal := h.normal
	h.mu.RUnlock()
	if normal == PermissionPlan {
		return errors.New("plan mode is fixed for this session")
	}
	return h.SetMode(normal)
}

// Decide implements PermissionHandler. Plan and deny are hard refusals.
func (h *SwitchablePermissionHandler) Decide(ctx context.Context, request PermissionRequest) (bool, error) {
	h.mu.RLock()
	mode, delegate := h.mode, h.delegate
	h.mu.RUnlock()
	if mode == PermissionPlan || mode == PermissionDeny {
		return false, nil
	}
	if delegate == nil {
		return true, nil
	}
	return delegate(ctx, request)
}

// CommandPolicyDecision is the outcome of a configured command-prefix rule.
type CommandPolicyDecision string

const (
	CommandPolicyAllow CommandPolicyDecision = "allow"
	CommandPolicyAsk   CommandPolicyDecision = "ask"
	CommandPolicyDeny  CommandPolicyDecision = "deny"
)

// CommandPolicyRule matches a simple shell command whose argv starts with
// Prefix. Rules are a defense-in-depth policy layer, not an OS sandbox.
type CommandPolicyRule struct {
	ID       string
	Decision CommandPolicyDecision
	Prefix   []string
}

// CommandPolicy is evaluated before the regular permission mode.
type CommandPolicy []CommandPolicyRule

// ValidateCommandPolicy checks rule shape before a tool is constructed.
func ValidateCommandPolicy(rules CommandPolicy) error {
	for i, rule := range rules {
		if strings.TrimSpace(rule.ID) == "" {
			return fmt.Errorf("command policy rule %d id is required", i)
		}
		switch rule.Decision {
		case CommandPolicyAllow, CommandPolicyAsk, CommandPolicyDeny:
		default:
			return fmt.Errorf("command policy rule %q has unsupported decision %q", rule.ID, rule.Decision)
		}
		if len(rule.Prefix) == 0 {
			return fmt.Errorf("command policy rule %q prefix is required", rule.ID)
		}
		for _, part := range rule.Prefix {
			if strings.TrimSpace(part) == "" {
				return fmt.Errorf("command policy rule %q contains an empty prefix part", rule.ID)
			}
		}
	}
	return nil
}

// MatchCommandPolicy returns the most restrictive matching rule. Only a
// single, lexically valid command is matched; operators and substitutions are
// intentionally treated as opaque and therefore cannot receive a prefix allow.
func (p CommandPolicy) MatchCommandPolicy(command string) (CommandPolicyDecision, CommandPolicyRule, bool) {
	segments, operators, valid := shellCommandParts(command)
	if !valid || len(segments) != 1 || len(operators) != 0 {
		return "", CommandPolicyRule{}, false
	}
	argv := segments[0]
	var matched CommandPolicyRule
	var decision CommandPolicyDecision
	for _, rule := range p {
		if len(rule.Prefix) > len(argv) || !commandPrefixMatches(argv, rule.Prefix) {
			continue
		}
		if decision == "" || commandPolicyRank(rule.Decision) > commandPolicyRank(decision) {
			matched = rule
			decision = rule.Decision
		}
	}
	return decision, matched, decision != ""
}

func commandPrefixMatches(argv, prefix []string) bool {
	for i := range prefix {
		if argv[i] != prefix[i] {
			return false
		}
	}
	return true
}

func commandPolicyRank(decision CommandPolicyDecision) int {
	switch decision {
	case CommandPolicyDeny:
		return 3
	case CommandPolicyAsk:
		return 2
	case CommandPolicyAllow:
		return 1
	default:
		return 0
	}
}

// ApplyCommandPolicy adds command-prefix rules without weakening the base
// permission mode: allow still passes through confirm/deny, ask forces the
// supplied handler, and deny always wins.
func ApplyCommandPolicy(base PermissionHandler, policy CommandPolicy) PermissionHandler {
	if len(policy) == 0 {
		return base
	}
	return func(ctx context.Context, request PermissionRequest) (bool, error) {
		decision, rule, matched := policy.MatchCommandPolicy(request.Detail)
		if !matched {
			if base == nil {
				return true, nil
			}
			return base(ctx, request)
		}
		switch decision {
		case CommandPolicyDeny:
			return false, fmt.Errorf("command denied by policy %q", rule.ID)
		case CommandPolicyAsk:
			if base == nil {
				return false, fmt.Errorf("command requires approval by policy %q", rule.ID)
			}
			return base(ctx, request)
		default:
			if base == nil {
				return true, nil
			}
			return base(ctx, request)
		}
	}
}

// ApplyRiskPolicy wraps the base permission mode with a policy for high-risk
// requests. Low and medium requests retain the base mode's behavior.
func ApplyRiskPolicy(base, confirmer PermissionHandler, policy RiskPolicy) (PermissionHandler, error) {
	policy = RiskPolicy(strings.ToLower(strings.TrimSpace(string(policy))))
	switch policy {
	case "", RiskPolicyAdvisory:
		return base, nil
	case RiskPolicyConfirm, RiskPolicyDeny:
		return func(ctx context.Context, request PermissionRequest) (bool, error) {
			if request.Risk != RiskHigh {
				if base == nil {
					return true, nil
				}
				return base(ctx, request)
			}
			if policy == RiskPolicyDeny {
				return false, nil
			}
			if confirmer == nil {
				return false, errors.New("high-risk confirmation handler is unavailable")
			}
			return confirmer(ctx, request)
		}, nil
	default:
		return nil, fmt.Errorf("unsupported risk policy %q", policy)
	}
}

var ErrPermissionDenied = errors.New("tool permission denied")

type permissionContextKey struct{}

// WithPermissionHandler attaches a decision callback to one agent turn.
func WithPermissionHandler(ctx context.Context, handler PermissionHandler) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, permissionContextKey{}, handler)
}

// RequirePermission checks the current turn's permission callback. No
// callback preserves the historical unrestricted behavior for library users.
func RequirePermission(ctx context.Context, request PermissionRequest) error {
	return requirePermission(ctx, request, nil)
}

// RequirePermissionWithCommandPolicy applies command-prefix policy to the
// current turn handler while preserving the library's unrestricted default.
func RequirePermissionWithCommandPolicy(ctx context.Context, request PermissionRequest, policy CommandPolicy) error {
	return requirePermission(ctx, request, policy)
}

func requirePermission(ctx context.Context, request PermissionRequest, policy CommandPolicy) error {
	if ctx == nil {
		ctx = context.Background()
	}
	handler, _ := ctx.Value(permissionContextKey{}).(PermissionHandler)
	if len(policy) > 0 {
		handler = ApplyCommandPolicy(handler, policy)
	}
	if handler == nil {
		return nil
	}
	allowed, err := handler(ctx, request)
	if err != nil {
		return fmt.Errorf("check %s permission: %w", request.Tool, err)
	}
	if !allowed {
		return fmt.Errorf("%w: %s", ErrPermissionDenied, request.Tool)
	}
	return nil
}

// HandlerForMode converts a non-interactive configuration mode into a handler.
func HandlerForMode(mode PermissionMode) (PermissionHandler, error) {
	switch PermissionMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case "", PermissionUnrestricted:
		return nil, nil
	case PermissionDeny:
		return func(context.Context, PermissionRequest) (bool, error) { return false, nil }, nil
	case PermissionPlan:
		return func(context.Context, PermissionRequest) (bool, error) { return false, nil }, nil
	default:
		return nil, fmt.Errorf("unsupported permission mode %q", mode)
	}
}
