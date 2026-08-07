package tools

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"eino-local-assistant/internal/config"

	"github.com/BurntSushi/toml"
	"go.starlark.net/starlark"
)

const (
	defaultToolRulesRelativePath = "rules/default.rules"
	toolRulesDirectory           = config.UserConfigDirectory
	toolPolicyConfigFile         = config.UserConfigFileName
)

//go:embed rules/default.rules
var defaultToolRulesDocument string

// ToolImpact is an internal, conservative description of a completed tool
// call's possible effect. It is deliberately not part of the Codex .rules
// language: rules decide authorization, while this classifier drives task
// progress and plan-mode admission.
type ToolImpact string

const (
	ToolImpactReadOnly           ToolImpact = "read_only"
	ToolImpactWorkspaceWrite     ToolImpact = "workspace_write"
	ToolImpactExternalSideEffect ToolImpact = "external_side_effect"
)

type policyDecision string

const (
	policyAllow     policyDecision = "allow"
	policyPrompt    policyDecision = "prompt"
	policyForbidden policyDecision = "forbidden"
)

type patternToken []string

type prefixRule struct {
	pattern       []patternToken
	decision      policyDecision
	justification string
	id            string
}

// ToolPolicy is a cumulative Codex execpolicy-compatible prefix-rule policy.
// User rules are loaded before project rules, and all matching rules contribute
// to the effective (strictest) decision.
type ToolPolicy struct {
	shellRules          []prefixRule
	hostExecutables     map[string]map[string]struct{}
	sources             []string
	projectRulesChecked bool
	projectRulesTrusted bool
}

// UserToolPolicyRoot returns the user-owned directory that holds tool rules
// and project-trust records. It is independent from the session-storage
// setting, even though their defaults currently share ~/.eino-assistant, so
// callers can protect it whenever it falls below a workspace.
func UserToolPolicyRoot() (string, error) {
	return config.UserConfigDir()
}

// InitializeUserToolRulesAt creates the embedded zero-authorization starter
// file when it is absent. It deliberately does not parse user or project
// rules, which lets command startup provide the starter before reporting an
// unrelated runtime-config migration error.
//
// userRoot is the product's tool-rules home itself (for example
// /tmp/home/.eino-assistant), not the user's home.
func InitializeUserToolRulesAt(userRoot string) error {
	userRoot = strings.TrimSpace(userRoot)
	if userRoot == "" {
		return errors.New("tool rules user root is required")
	}
	return initializeToolRules(filepath.Join(userRoot, defaultToolRulesRelativePath))
}

// LoadUserToolPolicy initializes ~/.eino-assistant/rules/default.rules once
// and then loads all user and project .rules files with Codex-compatible
// low-to-high precedence. Project rules are read from
// <workspace>/.eino-assistant/rules only after the user-owned
// ~/.eino-assistant/config.toml marks that workspace as trusted. The
// application never creates project rules or project trust entries.
func LoadUserToolPolicy(workspaceRoot string) (*ToolPolicy, error) {
	userRoot, err := UserToolPolicyRoot()
	if err != nil {
		return nil, err
	}
	return LoadToolPolicyAt(userRoot, workspaceRoot)
}

// LoadToolPolicyAt exists for isolated tests. userRoot is the product's
// tool-rules home itself (for example /tmp/home/.eino-assistant), not the
// user's home.
func LoadToolPolicyAt(userRoot, workspaceRoot string) (*ToolPolicy, error) {
	userRoot = strings.TrimSpace(userRoot)
	if userRoot == "" {
		return nil, errors.New("tool rules user root is required")
	}
	if err := InitializeUserToolRulesAt(userRoot); err != nil {
		return nil, err
	}

	policy := &ToolPolicy{hostExecutables: make(map[string]map[string]struct{})}
	if err := policy.loadRuleDirectory(filepath.Join(userRoot, "rules"), true); err != nil {
		return nil, err
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot != "" {
		policy.projectRulesChecked = true
		trusted, err := projectRulesTrusted(userRoot, workspaceRoot)
		if err != nil {
			return nil, err
		}
		policy.projectRulesTrusted = trusted
		if trusted {
			projectControlDirectory := filepath.Join(workspaceRoot, toolRulesDirectory)
			if err := validateProjectRuleControlDirectory(projectControlDirectory); err != nil {
				return nil, err
			}
			if err := policy.loadRuleDirectory(filepath.Join(projectControlDirectory, "rules"), false); err != nil {
				return nil, err
			}
		}
	}
	return policy, nil
}

// projectRulesTrusted mirrors Codex's project trust boundary: repository-owned
// files cannot authorize tools unless the user records the workspace as trusted
// in the product's global configuration.
func projectRulesTrusted(userRoot, workspaceRoot string) (bool, error) {
	path := filepath.Join(userRoot, toolPolicyConfigFile)
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect user project trust configuration %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("user project trust configuration %q must be a regular non-symlink file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read user project trust configuration %q: %w", path, err)
	}

	var document toolPolicyUserConfig
	if _, err := toml.Decode(string(data), &document); err != nil {
		return false, fmt.Errorf("parse user project trust configuration %q: %w", path, err)
	}

	workspace := canonicalizePath(workspaceRoot)
	trusted := false
	for rawPath, project := range document.Projects {
		if !filepath.IsAbs(rawPath) {
			return false, fmt.Errorf("user project trust path %q must be absolute", rawPath)
		}
		trustLevel := strings.TrimSpace(project.TrustLevel)
		switch trustLevel {
		case "untrusted":
			// An explicit untrusted entry is authoritative for all path aliases.
			if canonicalizePath(rawPath) == workspace {
				return false, nil
			}
		case "trusted":
			if canonicalizePath(rawPath) == workspace {
				trusted = true
			}
		default:
			return false, fmt.Errorf("user project trust %q has invalid trust_level %q", rawPath, trustLevel)
		}
	}
	return trusted, nil
}

type toolPolicyUserConfig struct {
	Projects map[string]toolPolicyProjectConfig `toml:"projects"`
}

type toolPolicyProjectConfig struct {
	TrustLevel string `toml:"trust_level"`
}

// validateProjectRuleControlDirectory prevents a repository-controlled
// intermediate path from redirecting trusted project rules outside the
// workspace. The workspace root itself may be a user-selected symlink, but
// the product-owned control directory may not be one.
func validateProjectRuleControlDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect project tool rules control directory %q: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("project tool rules control directory %q must be a non-symlink directory", directory)
	}
	return nil
}

func initializeToolRules(path string) error {
	directory := filepath.Dir(path)
	if err := ensureNonSymlinkRuleDirectory(directory); err != nil {
		return err
	}

	// O_EXCL would publish an empty final file before its contents are durable.
	// Write a sibling first, then link it into place: Link fails without
	// replacing an existing user-owned default.rules, so concurrent first starts
	// either observe a complete file or wait for the next load attempt.
	temporary, err := os.CreateTemp(directory, ".default.rules-*")
	if err != nil {
		return fmt.Errorf("create temporary tool rules file in %q: %w", directory, err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary tool rules mode %q: %w", temporaryPath, err)
	}
	if _, err := io.WriteString(temporary, defaultToolRulesDocument); err != nil {
		return fmt.Errorf("initialize tool rules %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync initialized tool rules %q: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close initialized tool rules %q: %w", path, err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if !os.IsExist(err) {
			return fmt.Errorf("publish initialized tool rules %q: %w", path, err)
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("inspect existing tool rules %q: %w", path, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("tool rules file %q must be a regular non-symlink file", path)
		}
	}
	return nil
}

func ensureNonSymlinkRuleDirectory(directory string) error {
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create tool rules parent directory %q: %w", parent, err)
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect tool rules parent directory %q: %w", parent, err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return fmt.Errorf("tool rules parent directory %q must be a non-symlink directory", parent)
	}

	info, err := os.Lstat(directory)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("tool rules directory %q must be a non-symlink directory", directory)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect tool rules directory %q: %w", directory, err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create tool rules directory %q: %w", directory, err)
	}
	info, err = os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect created tool rules directory %q: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("tool rules directory %q must be a non-symlink directory", directory)
	}
	return nil
}

func (p *ToolPolicy) loadRuleDirectory(directory string, required bool) error {
	info, err := os.Lstat(directory)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect tool rules directory %q: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("tool rules directory %q must be a non-symlink directory", directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read tool rules directory %q: %w", directory, err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != ".rules" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		fileInfo, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect tool rule file %q: %w", path, err)
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
			return fmt.Errorf("tool rule file %q must be a regular non-symlink file", path)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read tool rule file %q: %w", path, err)
		}
		if err := p.parse(string(data), path); err != nil {
			return err
		}
		p.sources = append(p.sources, path)
	}
	return nil
}

func (p *ToolPolicy) parse(document, source string) error {
	if p.hostExecutables == nil {
		p.hostExecutables = make(map[string]map[string]struct{})
	}
	collector := &ruleCollector{
		policy: p,
		source: source,
		// Host executable declarations apply to the cumulative rule set, just
		// like prefix rules. A later file may validate or use a path declared
		// by an earlier user or trusted-project file.
		hostExecutables: p.hostExecutables,
	}
	predeclared := starlark.StringDict{
		"prefix_rule":     starlark.NewBuiltin("prefix_rule", collector.prefixRule),
		"host_executable": starlark.NewBuiltin("host_executable", collector.hostExecutable),
	}
	thread := &starlark.Thread{Name: "codex-rules"}
	if _, err := starlark.ExecFile(thread, source, document, predeclared); err != nil {
		return fmt.Errorf("parse tool rules %q: %w", source, err)
	}
	return collector.validateExamples()
}

type ruleCollector struct {
	policy             *ToolPolicy
	source             string
	hostExecutables    map[string]map[string]struct{}
	exampleValidations []ruleExampleValidation
}

type ruleExampleValidation struct {
	rule       prefixRule
	matches    [][]string
	notMatches [][]string
}

func (c *ruleCollector) prefixRule(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		pattern       *starlark.List
		decision      = string(policyAllow)
		justification string
		matches       *starlark.List
		notMatches    *starlark.List
	)
	if err := starlark.UnpackArgs("prefix_rule", args, kwargs,
		"pattern", &pattern,
		"decision?", &decision,
		"justification?", &justification,
		"match??", &matches,
		"not_match??", &notMatches,
	); err != nil {
		return nil, err
	}
	parsedDecision, err := parsePolicyDecision(decision)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(justification) == "" && justification != "" {
		return nil, errors.New("prefix_rule: justification cannot be blank")
	}
	patternTokens, err := parsePattern(pattern)
	if err != nil {
		return nil, fmt.Errorf("prefix_rule: %w", err)
	}
	rule := prefixRule{
		pattern:       patternTokens,
		decision:      parsedDecision,
		justification: justification,
		id:            fmt.Sprintf("%s#%d", c.source, len(c.policy.shellRules)+1),
	}
	matchExamples, err := parseExamples(matches)
	if err != nil {
		return nil, fmt.Errorf("prefix_rule: match: %w", err)
	}
	notMatchExamples, err := parseExamples(notMatches)
	if err != nil {
		return nil, fmt.Errorf("prefix_rule: not_match: %w", err)
	}
	c.policy.shellRules = append(c.policy.shellRules, rule)
	c.exampleValidations = append(c.exampleValidations, ruleExampleValidation{
		rule:       rule,
		matches:    matchExamples,
		notMatches: notMatchExamples,
	})
	return starlark.None, nil
}

func (c *ruleCollector) hostExecutable(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var (
		name  string
		paths *starlark.List
	)
	if err := starlark.UnpackArgs("host_executable", args, kwargs, "name", &name, "paths", &paths); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || strings.ContainsRune(name, filepath.Separator) {
		return nil, fmt.Errorf("host_executable: name must be a bare executable name (got %q)", name)
	}
	allowed := c.hostExecutables[name]
	if allowed == nil {
		allowed = make(map[string]struct{}, paths.Len())
		c.hostExecutables[name] = allowed
	}
	for index := 0; index < paths.Len(); index++ {
		path, ok := starlark.AsString(paths.Index(index))
		if !ok {
			return nil, fmt.Errorf("host_executable: paths[%d] must be a string", index)
		}
		if !filepath.IsAbs(path) || filepath.Base(path) != name {
			return nil, fmt.Errorf("host_executable: path %q must be absolute with basename %q", path, name)
		}
		allowed[filepath.Clean(path)] = struct{}{}
	}
	return starlark.None, nil
}

func parsePolicyDecision(raw string) (policyDecision, error) {
	switch policyDecision(raw) {
	case policyAllow, policyPrompt, policyForbidden:
		return policyDecision(raw), nil
	default:
		return "", fmt.Errorf("decision must be allow, prompt, or forbidden (got %q)", raw)
	}
}

func parsePattern(list *starlark.List) ([]patternToken, error) {
	if list == nil || list.Len() == 0 {
		return nil, errors.New("pattern cannot be empty")
	}
	pattern := make([]patternToken, 0, list.Len())
	for index := 0; index < list.Len(); index++ {
		value := list.Index(index)
		if token, ok := starlark.AsString(value); ok {
			pattern = append(pattern, patternToken{token})
			continue
		}
		alternatives, ok := value.(*starlark.List)
		if !ok || alternatives.Len() == 0 {
			return nil, fmt.Errorf("pattern[%d] must be a string or non-empty list of strings", index)
		}
		parts := make(patternToken, 0, alternatives.Len())
		for alternative := 0; alternative < alternatives.Len(); alternative++ {
			token, ok := starlark.AsString(alternatives.Index(alternative))
			if !ok {
				return nil, fmt.Errorf("pattern[%d][%d] must be a string", index, alternative)
			}
			parts = append(parts, token)
		}
		pattern = append(pattern, parts)
	}
	return pattern, nil
}

func parseExamples(list *starlark.List) ([][]string, error) {
	if list == nil {
		return nil, nil
	}
	examples := make([][]string, 0, list.Len())
	for index := 0; index < list.Len(); index++ {
		value := list.Index(index)
		if raw, ok := starlark.AsString(value); ok {
			argv, ok := tokenizeRuleShell(raw)
			if !ok || len(argv) == 0 {
				return nil, fmt.Errorf("example[%d] has invalid shell syntax", index)
			}
			examples = append(examples, argv)
			continue
		}
		parts, ok := value.(*starlark.List)
		if !ok || parts.Len() == 0 {
			return nil, fmt.Errorf("example[%d] must be a non-empty string or list of strings", index)
		}
		argv := make([]string, 0, parts.Len())
		for part := 0; part < parts.Len(); part++ {
			token, ok := starlark.AsString(parts.Index(part))
			if !ok {
				return nil, fmt.Errorf("example[%d][%d] must be a string", index, part)
			}
			argv = append(argv, token)
		}
		examples = append(examples, argv)
	}
	return examples, nil
}

func (c *ruleCollector) validateExamples() error {
	for _, validation := range c.exampleValidations {
		if err := validateExamples(validation.rule, validation.matches, validation.notMatches, c.hostExecutables); err != nil {
			return fmt.Errorf("parse tool rules %q: prefix_rule: %w", c.source, err)
		}
	}
	return nil
}

func validateExamples(rule prefixRule, matches, notMatches [][]string, hostExecutables map[string]map[string]struct{}) error {
	for _, argv := range matches {
		if !matchesPrefixRuleWithHost(rule, argv, hostExecutables) {
			return fmt.Errorf("match example %q does not match pattern", strings.Join(argv, " "))
		}
	}
	for _, argv := range notMatches {
		if matchesPrefixRuleWithHost(rule, argv, hostExecutables) {
			return fmt.Errorf("not_match example %q matches pattern", strings.Join(argv, " "))
		}
	}
	return nil
}

func matchesPrefixRuleWithHost(rule prefixRule, argv []string, hostExecutables map[string]map[string]struct{}) bool {
	if rule.matches(argv) {
		return true
	}
	hostArgv, ok := hostExecutableArguments(argv, hostExecutables)
	return ok && rule.matches(hostArgv)
}

// hostExecutableArguments lowers an absolute executable to its declared bare
// name only when that exact path is explicitly trusted by host_executable.
// Bare-name rules must never authorize an arbitrary executable merely because
// it shares a basename with a common host tool.
func hostExecutableArguments(argv []string, hostExecutables map[string]map[string]struct{}) ([]string, bool) {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, false
	}
	resolved := filepath.Clean(argv[0])
	name := filepath.Base(resolved)
	allowed, declared := hostExecutables[name]
	if !declared {
		return nil, false
	}
	if _, ok := allowed[resolved]; !ok {
		return nil, false
	}
	return append([]string{name}, argv[1:]...), true
}

func isBareExecutable(program string) bool {
	return program != "" && filepath.Base(program) == program && !strings.ContainsRune(program, filepath.Separator)
}

// EvaluateShell returns the effective command authorization. A matching rule
// can only become stricter when another rule also matches. Unmatched commands
// fall back to the built-in known-safe classifier, then to an approval prompt.
func (p *ToolPolicy) EvaluateShell(command string) Evaluation {
	if hard := hardShellSafetyDeny(command); hard.Decision == DecisionDeny {
		return hard
	}
	segments, ok := splitToolPolicyShell(command)
	if !ok || len(segments) == 0 {
		return Evaluation{Decision: DecisionAsk, RuleID: "opaque-shell", Reason: "unsupported shell syntax; approval required"}
	}

	matched := false
	allKnownSafe := true
	strictest := policyAllow
	var matchedIDs []string
	var justification string
	for _, segment := range segments {
		argv, ok := tokenizeRuleShell(segment)
		if !ok || len(argv) == 0 {
			return Evaluation{Decision: DecisionAsk, RuleID: "opaque-shell", Reason: "unsupported shell syntax; approval required"}
		}
		rules := p.matchingRules(argv)
		if len(rules) == 0 {
			if !isKnownSafeAuthorizationCommand(argv) {
				allKnownSafe = false
			}
			continue
		}
		matched = true
		for _, rule := range rules {
			matchedIDs = append(matchedIDs, rule.id)
			if policyDecisionRank(rule.decision) > policyDecisionRank(strictest) {
				strictest = rule.decision
				justification = rule.justification
			}
		}
	}

	if matched && strictest != policyAllow {
		decision := DecisionAsk
		if strictest == policyForbidden {
			decision = DecisionDeny
		}
		reason := "matched Codex rule " + strings.Join(matchedIDs, ", ")
		if justification != "" {
			reason += ": " + justification
		}
		return Evaluation{Decision: decision, RuleID: matchedIDs[0], Reason: reason, PolicyPrompt: strictest == policyPrompt}
	}
	if matched && !allKnownSafe {
		return Evaluation{Decision: DecisionAsk, RuleID: "mixed-command", Reason: "one or more shell segments have no allow rule"}
	}
	if matched {
		return Evaluation{Decision: DecisionAllow, RuleID: matchedIDs[0], Reason: "matched Codex allow rule"}
	}
	if allKnownSafe {
		return Evaluation{Decision: DecisionAllow, RuleID: "known-safe", Reason: "known read-only command"}
	}
	return Evaluation{Decision: DecisionAsk, RuleID: "default", Reason: "no matching Codex rule"}
}

func (p *ToolPolicy) matchingRules(argv []string) []prefixRule {
	if p == nil || len(argv) == 0 {
		return nil
	}
	matches := make([]prefixRule, 0, 1)
	for _, rule := range p.shellRules {
		if rule.matches(argv) {
			matches = append(matches, rule)
		}
	}
	if len(matches) != 0 {
		return matches
	}
	hostArgv, ok := hostExecutableArguments(argv, p.hostExecutables)
	if !ok {
		return matches
	}
	for _, rule := range p.shellRules {
		if rule.matches(hostArgv) {
			matches = append(matches, rule)
		}
	}
	return matches
}

func (r prefixRule) matches(argv []string) bool {
	if len(argv) < len(r.pattern) {
		return false
	}
	for index, alternatives := range r.pattern {
		found := false
		for _, alternative := range alternatives {
			if argv[index] == alternative {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func policyDecisionRank(decision policyDecision) int {
	switch decision {
	case policyForbidden:
		return 2
	case policyPrompt:
		return 1
	default:
		return 0
	}
}

// ClassifyShellCommand is independent from .rules. It is intentionally
// conservative: a command is read-only only when every parseable segment is
// on the internal safe list; an identified remote action gets the highest tier.
func ClassifyShellCommand(command string) ToolImpact {
	segments, ok := splitToolPolicyShell(command)
	if !ok || len(segments) == 0 {
		if looksExternal(command) {
			return ToolImpactExternalSideEffect
		}
		return ToolImpactWorkspaceWrite
	}
	impact := ToolImpactReadOnly
	for _, segment := range segments {
		argv, ok := tokenizeRuleShell(segment)
		if !ok || len(argv) == 0 {
			return ToolImpactWorkspaceWrite
		}
		if isExternalCommand(argv) {
			impact = ToolImpactExternalSideEffect
			continue
		}
		if !isReadOnlyImpactCommand(argv) && impact != ToolImpactExternalSideEffect {
			impact = ToolImpactWorkspaceWrite
		}
	}
	return impact
}

// isKnownSafeAuthorizationCommand is the narrow Codex-style fallback that can
// auto-authorize a command with no matching .rules entry. It must not grow
// merely because another command is observationally read-only: authorization
// and impact classification intentionally have different failure modes.
func isKnownSafeAuthorizationCommand(argv []string) bool {
	if len(argv) == 0 || !isBareExecutable(argv[0]) {
		return false
	}
	program := argv[0]
	switch program {
	case "cat", "cd", "cut", "echo", "expr", "false", "grep", "head", "id", "ls", "nl", "paste", "pwd", "rev", "seq", "stat", "tail", "tr", "true", "uname", "uniq", "wc", "which", "whoami":
		return true
	case "find":
		return knownSafeFind(argv[1:])
	case "rg":
		return knownSafeRG(argv[1:])
	case "base64":
		return !hasAnyExactOrPrefix(argv[1:], "-o", "--output")
	case "sed":
		return knownSafeSed(argv[1:])
	case "git":
		return knownSafeGit(argv[1:])
	case "go":
		return knownSafeGo(argv[1:])
	default:
		return false
	}
}

// isReadOnlyImpactCommand decides whether a command can be treated as not
// mutating the workspace after it has run. It may recognize more commands than
// the authorization fallback, but never grants execution permission itself.
func isReadOnlyImpactCommand(argv []string) bool {
	if isKnownSafeAuthorizationCommand(argv) {
		return true
	}
	if len(argv) == 0 || !isBareExecutable(argv[0]) {
		return false
	}
	switch argv[0] {
	case "sort":
		return knownReadOnlySort(argv[1:])
	default:
		return false
	}
}

func knownReadOnlySort(args []string) bool {
	for _, arg := range args {
		if arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "--output=") || (strings.HasPrefix(arg, "-o") && arg != "-o") {
			return false
		}
	}
	return true
}

func knownSafeFind(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-exec", "-execdir", "-ok", "-okdir", "-delete", "-fls", "-fprint", "-fprint0", "-fprintf":
			return false
		}
	}
	return true
}

func knownSafeRG(args []string) bool {
	return !hasAnyExactOrPrefix(args, "--pre", "--hostname-bin", "--search-zip", "-z")
}

func knownSafeSed(args []string) bool {
	if len(args) < 3 || args[0] != "-n" || !strings.HasSuffix(args[1], "p") {
		return false
	}
	rangePart := strings.TrimSuffix(args[1], "p")
	if rangePart == "" {
		return false
	}
	for _, part := range strings.Split(rangePart, ",") {
		if part == "" {
			return false
		}
		for _, char := range part {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

func knownSafeGit(args []string) bool {
	if len(args) == 1 && args[0] == "--version" {
		return true
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return false
	}
	subcommand := args[0]
	rest := args[1:]
	if hasAnyExactOrPrefix(rest, "--output", "-c", "--config", "--exec-path") {
		return false
	}
	switch subcommand {
	case "status", "log", "show", "ls-files", "rev-parse":
		return true
	case "diff":
		return !hasAnyExactOrPrefix(rest, "--ext-diff")
	case "branch":
		if len(rest) == 0 {
			return true
		}
		for _, arg := range rest {
			switch arg {
			case "-a", "--all", "-r", "--remotes", "--show-current", "-v", "-vv", "--verbose":
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

func knownSafeGo(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version", "help":
		return true
	case "env":
		return !hasAnyExactOrPrefix(args[1:], "-w")
	default:
		return false
	}
}

func hasAnyExactOrPrefix(args []string, disallowed ...string) bool {
	for _, arg := range args {
		for _, prefix := range disallowed {
			if arg == prefix || strings.HasPrefix(arg, prefix+"=") {
				return true
			}
		}
	}
	return false
}

func isExternalCommand(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch filepath.Base(argv[0]) {
	case "curl", "wget", "ssh", "scp", "sftp":
		return true
	case "git":
		return len(argv) > 1 && (argv[1] == "push" || argv[1] == "fetch" || argv[1] == "clone")
	case "gh":
		return len(argv) > 2 && ((argv[1] == "pr" && argv[2] == "create") || (argv[1] == "issue" && argv[2] == "create"))
	case "npm":
		return len(argv) > 1 && argv[1] == "publish"
	}
	return false
}

func looksExternal(command string) bool {
	lower := strings.ToLower(command)
	for _, program := range []string{"curl", "wget", "ssh", "scp", "git push", "git fetch", "git clone", "npm publish"} {
		if strings.Contains(lower, program) {
			return true
		}
	}
	return false
}

// SummaryLines is display-only metadata for the TUI. It reports rule sources
// without rendering individual commands, which may contain sensitive paths.
func (p *ToolPolicy) SummaryLines() []string {
	if p == nil {
		return []string{"rules: unavailable"}
	}
	counts := map[policyDecision]int{}
	for _, rule := range p.shellRules {
		counts[rule.decision]++
	}
	lines := []string{
		fmt.Sprintf("rules: sources=%d shell=%d allow=%d prompt=%d forbidden=%d", len(p.sources), len(p.shellRules), counts[policyAllow], counts[policyPrompt], counts[policyForbidden]),
	}
	if !p.projectRulesChecked {
		return lines
	}
	if p.projectRulesTrusted {
		return append(lines, "project rules: loaded from trusted workspace")
	}
	return append(lines, "project rules: skipped (workspace is not trusted)")
}

// hardShellSafetyDeny is a small defense-in-depth heuristic for dangerous
// literal shell spellings. It is not a shell parser or a complete execution
// boundary, so yolo must never be described as protected by these checks.
func hardShellSafetyDeny(command string) Evaluation {
	lower := strings.ToLower(command)
	checks := []struct {
		needle string
		id     string
	}{
		{"curl", "deny-curl-pipe-sh"},
		{"wget", "deny-curl-pipe-sh"},
	}
	for _, check := range checks {
		if strings.Contains(lower, check.needle) && strings.Contains(lower, "|") && (strings.Contains(lower, "| sh") || strings.Contains(lower, "| bash")) {
			return Evaluation{Decision: DecisionDeny, RuleID: check.id, Reason: "command safety check blocks remote script execution"}
		}
	}
	if strings.Contains(lower, "sudo") {
		return Evaluation{Decision: DecisionDeny, RuleID: "deny-sudo", Reason: "command safety check blocks privilege escalation"}
	}
	if strings.Contains(lower, "mkfs") || (strings.Contains(lower, "dd") && strings.Contains(lower, "of=/dev/")) {
		return Evaluation{Decision: DecisionDeny, RuleID: "deny-disk-wipe", Reason: "command safety check blocks destructive device writes"}
	}
	return Evaluation{}
}

// tokenizeRuleShell accepts literal argv syntax needed for .rules matching and
// validation. It intentionally rejects shell expansion and control syntax.
func tokenizeRuleShell(command string) ([]string, bool) {
	command = strings.ReplaceAll(command, "2>&1", "")
	command = strings.ReplaceAll(command, "2>/dev/null", "")
	var argv []string
	var current strings.Builder
	var quote byte
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			argv = append(argv, current.String())
			current.Reset()
		}
	}
	for index := 0; index < len(command); index++ {
		ch := command[index]
		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if quote != 0 {
			if ch == quote {
				quote = 0
				continue
			}
			if quote == '"' && (ch == '$' || ch == '`') {
				return nil, false
			}
			if ch == '\\' && quote == '"' {
				escaped = true
				continue
			}
			current.WriteByte(ch)
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case '\\':
			escaped = true
		case ' ', '\t':
			flush()
		case '$', '`', ';', '|', '&', '<', '>', '(', ')', '\n', '\r':
			return nil, false
		default:
			current.WriteByte(ch)
		}
	}
	if quote != 0 || escaped {
		return nil, false
	}
	flush()
	return argv, len(argv) > 0
}

// splitToolPolicyShell recognizes the uncomplicated shell chains that Codex
// can safely lower into independent commands for known-safe classification.
func splitToolPolicyShell(command string) ([]string, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, false
	}
	segments := make([]string, 0, 2)
	start := 0
	var quote byte
	for index := 0; index < len(command); index++ {
		ch := command[index]
		if quote != 0 {
			if ch == quote {
				quote = 0
			} else if quote == '"' && (ch == '\\' || ch == '`' || ch == '$') {
				return nil, false
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
		case '\\', '`', '$', '\n', '\r', '(', ')', '<':
			return nil, false
		case '>':
			if !safeToolPolicyStderrRedirect(command, index) {
				return nil, false
			}
		case ';', '|', '&':
			if ch == '&' && index > 0 && command[index-1] == '>' && index+1 < len(command) && command[index+1] == '1' {
				continue
			}
			if ch == '&' && (index+1 >= len(command) || command[index+1] != '&') {
				return nil, false
			}
			if ch == '|' && index+1 < len(command) && command[index+1] == '&' {
				return nil, false
			}
			segment := strings.TrimSpace(command[start:index])
			if segment == "" {
				return nil, false
			}
			segments = append(segments, segment)
			if ch == '&' || (ch == '|' && index+1 < len(command) && command[index+1] == '|') {
				index++
			}
			start = index + 1
		}
	}
	if quote != 0 {
		return nil, false
	}
	segment := strings.TrimSpace(command[start:])
	if segment == "" {
		return nil, false
	}
	return append(segments, segment), true
}

func safeToolPolicyStderrRedirect(command string, index int) bool {
	if index == 0 || command[index-1] != '2' {
		return false
	}
	if index > 1 && !isToolPolicySeparator(command[index-2]) {
		return false
	}
	if strings.HasPrefix(command[index:], ">&1") {
		return index+3 == len(command) || isToolPolicySeparator(command[index+3])
	}
	if strings.HasPrefix(command[index:], ">/dev/null") {
		return index+10 == len(command) || isToolPolicySeparator(command[index+10])
	}
	return false
}

func isToolPolicySeparator(ch byte) bool {
	switch ch {
	case ' ', '\t', ';', '|', '&':
		return true
	default:
		return false
	}
}
