package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Canonical tool names in permission rules (Codex-oriented subset).
const (
	// PermToolShell is the shell tool (Codex shell / our shell).
	PermToolShell = "Shell"
	// PermToolApplyPatch is the structured file-edit tool (Codex apply_patch).
	PermToolApplyPatch = "ApplyPatch"
)

// PermissionSet is a compiled allow/ask/deny rule table.
// Evaluation order: deny, then ask, then allow (Claude-style buckets; used with Codex tools).
type PermissionSet struct {
	Profile string
	// DefaultShell when no Shell rule matches.
	DefaultShell Decision
	// DefaultPatch when no ApplyPatch rule matches.
	DefaultPatch Decision
	Deny         []permRule
	Ask          []permRule
	Allow        []permRule
}

type permRule struct {
	Decision Decision
	ID       string
	Tool     string // Shell or ApplyPatch
	Spec     string
	Raw      string
	bashRE   *regexp.Regexp
	pathRE   *regexp.Regexp
	bashPref []string
}

// parsePermissionRule parses "Tool" or "Tool(specifier)".
//
//	Shell|Bash|run_command → Shell
//	ApplyPatch|Write|Edit|WriteFile → ApplyPatch
func parsePermissionRule(raw string, decision Decision) (permRule, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return permRule{}, fmt.Errorf("empty permission rule")
	}
	tool, spec, err := splitToolSpec(raw)
	if err != nil {
		return permRule{}, err
	}
	canon, err := canonicalizePermTool(tool)
	if err != nil {
		return permRule{}, err
	}
	rule := permRule{
		Decision: decision,
		ID:       string(decision) + ":" + raw,
		Tool:     canon,
		Spec:     spec,
		Raw:      raw,
	}
	if spec == "" {
		return rule, nil
	}
	switch canon {
	case PermToolShell:
		if err := compileBashSpec(&rule, spec); err != nil {
			return permRule{}, fmt.Errorf("%s: %w", raw, err)
		}
	case PermToolApplyPatch:
		if err := compilePathSpec(&rule, spec); err != nil {
			return permRule{}, fmt.Errorf("%s: %w", raw, err)
		}
	}
	return rule, nil
}

// BuildPermissionSet compiles permission lists. Profile "cautious" seeds defaults.
func BuildPermissionSet(profile string, allow, ask, deny []string) (*PermissionSet, error) {
	if strings.TrimSpace(profile) == "" {
		profile = ProfileCautious
	}
	if profile != ProfileCautious {
		return nil, fmt.Errorf("permissions.profile %q is unsupported (supported: %s)", profile, ProfileCautious)
	}

	set := &PermissionSet{
		Profile:      profile,
		DefaultShell: DecisionAsk,
		DefaultPatch: DecisionAsk,
	}

	seedAllow, seedDeny := cautiousPermissionSeeds()
	var err error
	if set.Deny, err = parseRuleList(append(seedDeny, deny...), DecisionDeny); err != nil {
		return nil, err
	}
	if set.Ask, err = parseRuleList(ask, DecisionAsk); err != nil {
		return nil, err
	}
	if set.Allow, err = parseRuleList(append(seedAllow, allow...), DecisionAllow); err != nil {
		return nil, err
	}
	return set, nil
}

func parseRuleList(raw []string, decision Decision) ([]permRule, error) {
	out := make([]permRule, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		rule, err := parsePermissionRule(s, decision)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}

func cautiousPermissionSeeds() (allow, deny []string) {
	// Codex-like: shell for inspection; file writes via apply_patch (default ask).
	allow = []string{
		"Shell(pwd)",
		"Shell(ls)",
		"Shell(ls *)",
		"Shell(git status)",
		"Shell(git status *)",
		"Shell(git diff)",
		"Shell(git diff *)",
		"Shell(git log)",
		"Shell(git log *)",
		"Shell(rg)",
		"Shell(rg *)",
		"Shell(grep)",
		"Shell(grep *)",
		"Shell(find)",
		"Shell(find *)",
		"Shell(cat *)",
		"Shell(head *)",
		"Shell(tail *)",
	}
	deny = []string{
		"Shell(sudo)",
		"Shell(sudo *)",
	}
	return allow, deny
}

// EvaluateBash decides allow/ask/deny for a shell command.
func (p *PermissionSet) EvaluateBash(command string) Evaluation {
	if p == nil {
		return Evaluation{Decision: DecisionAsk, RuleID: "default", Reason: "no permissions configured; default ask"}
	}
	command = strings.TrimSpace(command)
	if hard := hardBashDeny(command); hard.Decision == DecisionDeny {
		return hard
	}
	if ev, ok := p.matchBucket(p.Deny, PermToolShell, command, ""); ok {
		return ev
	}
	if HasShellMetacharacters(command) {
		if ev, ok := p.matchBucket(p.Ask, PermToolShell, command, ""); ok {
			return ev
		}
		if _, ok := p.matchBucket(p.Allow, PermToolShell, command, ""); ok {
			return Evaluation{
				Decision: DecisionAsk,
				RuleID:   "opaque-shell",
				Reason:   "shell metacharacters present; allow downgraded to ask",
			}
		}
		return Evaluation{
			Decision: DecisionAsk,
			RuleID:   "opaque-shell",
			Reason:   "shell metacharacters present; default ask",
		}
	}
	if ev, ok := p.matchBucket(p.Ask, PermToolShell, command, ""); ok {
		return ev
	}
	if ev, ok := p.matchBucket(p.Allow, PermToolShell, command, ""); ok {
		return ev
	}
	return Evaluation{
		Decision: p.DefaultShell,
		RuleID:   "default",
		Reason:   fmt.Sprintf("default %s (%s)", p.DefaultShell, profileOrUnknown(p.Profile)),
	}
}

// EvaluatePath decides allow/ask/deny for apply_patch paths.
func (p *PermissionSet) EvaluatePath(tool, workspaceRoot, absPath string) Evaluation {
	if p == nil {
		return Evaluation{Decision: DecisionAsk, RuleID: "default", Reason: "default ask"}
	}
	tool, _ = canonicalizePermTool(tool)
	candidates := pathMatchCandidates(workspaceRoot, absPath)
	if ev, ok := p.matchPathBucket(p.Deny, tool, candidates); ok {
		return ev
	}
	if ev, ok := p.matchPathBucket(p.Ask, tool, candidates); ok {
		return ev
	}
	if ev, ok := p.matchPathBucket(p.Allow, tool, candidates); ok {
		return ev
	}
	return Evaluation{
		Decision: p.DefaultPatch,
		RuleID:   "default",
		Reason:   fmt.Sprintf("default %s (%s)", p.DefaultPatch, profileOrUnknown(p.Profile)),
	}
}

func pathMatchCandidates(workspaceRoot, absPath string) []string {
	absPath = filepath.ToSlash(strings.TrimSpace(absPath))
	if absPath == "" {
		return nil
	}
	out := []string{absPath, filepath.Base(absPath)}
	root := canonicalizePath(workspaceRoot)
	if root != "" && PathWithinWorkspace(root, absPath) {
		if rel, err := filepath.Rel(root, absPath); err == nil {
			rel = filepath.ToSlash(rel)
			out = append(out, rel, "./"+rel)
		}
	}
	return out
}

func (p *PermissionSet) matchBucket(rules []permRule, tool, command, path string) (Evaluation, bool) {
	for _, rule := range rules {
		if rule.Tool != tool {
			continue
		}
		if rule.matches(command, path) {
			return Evaluation{
				Decision: rule.Decision,
				RuleID:   rule.Raw,
				Reason:   fmt.Sprintf("permissions %s (%s)", rule.Raw, rule.Decision),
			}, true
		}
	}
	return Evaluation{}, false
}

func (p *PermissionSet) matchPathBucket(rules []permRule, tool string, candidates []string) (Evaluation, bool) {
	for _, rule := range rules {
		if rule.Tool != tool {
			continue
		}
		if rule.Spec == "" {
			return Evaluation{
				Decision: rule.Decision,
				RuleID:   rule.Raw,
				Reason:   fmt.Sprintf("permissions %s (%s)", rule.Raw, rule.Decision),
			}, true
		}
		for _, c := range candidates {
			if rule.matchPath(c) {
				return Evaluation{
					Decision: rule.Decision,
					RuleID:   rule.Raw,
					Reason:   fmt.Sprintf("permissions %s (%s)", rule.Raw, rule.Decision),
				}, true
			}
		}
	}
	return Evaluation{}, false
}

func (r permRule) matches(command, path string) bool {
	if r.Spec == "" {
		return true
	}
	switch r.Tool {
	case PermToolShell:
		return r.matchBash(command)
	default:
		return r.matchPath(path)
	}
}

func (r permRule) matchBash(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if len(r.bashPref) > 0 {
		tokens := strings.Fields(command)
		if len(tokens) < len(r.bashPref) {
			return false
		}
		for i, p := range r.bashPref {
			if tokens[i] != p {
				return false
			}
		}
		return true
	}
	if r.bashRE != nil {
		return r.bashRE.MatchString(command)
	}
	return false
}

func (r permRule) matchPath(path string) bool {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" || r.pathRE == nil {
		return false
	}
	candidates := []string{path, filepath.Base(path)}
	for _, c := range candidates {
		if r.pathRE.MatchString(c) {
			return true
		}
	}
	return false
}

func splitToolSpec(raw string) (tool, spec string, err error) {
	open := strings.IndexByte(raw, '(')
	if open < 0 {
		return raw, "", nil
	}
	if !strings.HasSuffix(raw, ")") {
		return "", "", fmt.Errorf("invalid permission rule %q: missing closing )", raw)
	}
	tool = strings.TrimSpace(raw[:open])
	spec = strings.TrimSpace(raw[open+1 : len(raw)-1])
	if tool == "" {
		return "", "", fmt.Errorf("invalid permission rule %q: empty tool name", raw)
	}
	return tool, spec, nil
}

func canonicalizePermTool(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "shell", "bash", "run_command", "sh":
		return PermToolShell, nil
	case "applypatch", "apply_patch", "write", "edit", "write_file", "edit_file":
		return PermToolApplyPatch, nil
	default:
		return "", fmt.Errorf("unknown permission tool %q (use Shell or ApplyPatch)", name)
	}
}

func compileBashSpec(rule *permRule, spec string) error {
	spec = strings.TrimSpace(spec)
	if strings.HasSuffix(spec, ":*") {
		spec = strings.TrimSuffix(spec, ":*") + " *"
	}
	if !strings.Contains(spec, "*") {
		rule.bashPref = strings.Fields(spec)
		if len(rule.bashPref) == 0 {
			return fmt.Errorf("empty Shell specifier")
		}
		return nil
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(spec); i++ {
		ch := spec[i]
		switch ch {
		case '*':
			b.WriteString(".*")
		case ' ', '\t':
			b.WriteString(`\s+`)
		case '.', '+', '?', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			b.WriteByte('\\')
			b.WriteByte(ch)
		default:
			b.WriteByte(ch)
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile("(?i)" + b.String())
	if err != nil {
		return err
	}
	rule.bashRE = re
	return nil
}

func compilePathSpec(rule *permRule, spec string) error {
	spec = filepath.ToSlash(strings.TrimSpace(spec))
	if spec == "" {
		return fmt.Errorf("empty path specifier")
	}
	if spec == "**" || spec == "./**" || spec == "**/*" || spec == "*" {
		rule.pathRE = regexp.MustCompile(`^.*$`)
		return nil
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(spec); {
		if i+1 < len(spec) && spec[i] == '*' && spec[i+1] == '*' {
			b.WriteString(".*")
			i += 2
			if i < len(spec) && spec[i] == '/' {
				i++
			}
			continue
		}
		ch := spec[i]
		switch ch {
		case '*':
			b.WriteString(`[^/]*`)
		case '?', '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			b.WriteByte('\\')
			b.WriteByte(ch)
		default:
			b.WriteByte(ch)
		}
		i++
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return err
	}
	rule.pathRE = re
	return nil
}

func hardBashDeny(command string) Evaluation {
	lower := strings.ToLower(command)
	checks := []struct {
		id string
		re *regexp.Regexp
	}{
		{"deny-curl-pipe-sh", regexp.MustCompile(`(?i)(curl|wget).*\|\s*(ba)?sh`)},
		{"deny-rm-rootish", regexp.MustCompile(`(?i)\brm\s+.*-[a-zA-Z]*f[a-zA-Z]*.*\s(/|~|\*|\.\./)`)},
		{"deny-disk-wipe", regexp.MustCompile(`(?i)\b(mkfs\b|dd\s+.*of=/dev/)`)},
		{"deny-sudo", regexp.MustCompile(`(?i)\bsudo\b`)},
	}
	for _, c := range checks {
		if c.re.MatchString(lower) {
			return Evaluation{
				Decision: DecisionDeny,
				RuleID:   c.id,
				Reason:   fmt.Sprintf("policy %s (deny)", c.id),
			}
		}
	}
	return Evaluation{}
}

// SummaryLines reports permission rules for /permissions.
func (p *PermissionSet) SummaryLines() []string {
	if p == nil {
		return []string{"permissions: (none)"}
	}
	var lines []string
	lines = append(lines, "profile: "+p.Profile)
	lines = append(lines, fmt.Sprintf("rules: deny=%d ask=%d allow=%d", len(p.Deny), len(p.Ask), len(p.Allow)))
	for _, r := range p.Deny {
		lines = append(lines, "  deny  "+r.Raw)
	}
	for _, r := range p.Ask {
		lines = append(lines, "  ask   "+r.Raw)
	}
	const maxAllow = 12
	for i, r := range p.Allow {
		if i >= maxAllow {
			lines = append(lines, fmt.Sprintf("  allow … (%d more)", len(p.Allow)-maxAllow))
			break
		}
		lines = append(lines, "  allow "+r.Raw)
	}
	return lines
}
