package config

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"eino-local-assistant/internal/tools"
)

// Config is the complete TOML configuration accepted by the CLI.
//
// Tool posture follows Codex + Claude Code conventions:
//   - approval_policy (Codex): when to ask the human
//   - [workspace] (Codex lite): path clamp root
//   - [permissions] allow/ask/deny (Claude): Tool or Tool(specifier) rules
//   - [sandbox]: OS worker filesystem and network boundary
//   - [runtime]: whole-turn ReAct budgets
//   - [tools.shell] / [tools.apply_patch]: per-tool limits
type Config struct {
	Model ModelConfig `toml:"model"`
	// ApprovalPolicy is Codex-style: on-request | never (also accepts on_request).
	ApprovalPolicy string            `toml:"approval_policy"`
	Assistant      AssistantConfig   `toml:"assistant"`
	Storage        StorageConfig     `toml:"storage"`
	Workspace      WorkspaceConfig   `toml:"workspace"`
	Permissions    PermissionsConfig `toml:"permissions"`
	Tools          ToolsConfig       `toml:"tools"`
	Sandbox        SandboxConfig     `toml:"sandbox"`
	Runtime        RuntimeConfig     `toml:"runtime"`
	UI             UIConfig          `toml:"ui"`
	// Rules loads workspace AGENTS.md as durable project instructions.
	Rules RulesConfig `toml:"rules"`
	// Memory is project-scoped semantic memory (not session resume).
	Memory MemoryConfig `toml:"memory"`
}

// RulesConfig controls loading of workspace-root AGENTS.md.
type RulesConfig struct {
	// Enabled injects AGENTS.md when true. Omitted defaults to true.
	Enabled *bool `toml:"enabled"`
	// MaxTokens caps AGENTS.md injection. Zero uses DefaultRulesMaxTokens.
	MaxTokens int `toml:"max_tokens"`
}

// MemoryConfig controls project-scoped long-term memory under .eino/memory/.
// See docs/memory.md.
type MemoryConfig struct {
	// Enabled controls summary injection and read tools. Omitted defaults to true.
	Enabled *bool `toml:"enabled"`
	// Generate controls idle-session auto extraction. Omitted defaults to true.
	Generate *bool `toml:"generate"`
	// MaxSummaryTokens caps the always-on memory summary. Zero uses DefaultMemorySummaryTokens.
	MaxSummaryTokens int `toml:"max_summary_tokens"`
	// IdleAfter is how long a session must be idle before auto extraction.
	// Empty uses DefaultMemoryIdleAfter (e.g. "6h").
	IdleAfter string `toml:"idle_after"`
	// MaxRolloutsPerScan bounds how many idle threads are claimed per scan.
	// Zero uses DefaultMemoryMaxRolloutsPerScan.
	MaxRolloutsPerScan int `toml:"max_rollouts_per_scan"`
	// ScanMaxAge drops threads older than this from auto extraction.
	// Empty uses DefaultMemoryScanMaxAge (e.g. "10d").
	ScanMaxAge string `toml:"scan_max_age"`
}

const (
	// DefaultRulesMaxTokens is the AGENTS.md injection budget.
	DefaultRulesMaxTokens = 8000
	// DefaultMemorySummaryTokens is the bounded memory summary budget.
	DefaultMemorySummaryTokens = 2500
	// DefaultMemoryIdleAfter is the idle threshold before auto extraction.
	DefaultMemoryIdleAfter = "6h"
	// DefaultMemoryMaxRolloutsPerScan bounds work per consolidator tick.
	DefaultMemoryMaxRolloutsPerScan = 2
	// DefaultMemoryScanMaxAge skips very old sessions for auto extraction.
	DefaultMemoryScanMaxAge = "10d"
)

// RulesEnabled reports whether AGENTS.md injection is on.
func (c RulesConfig) RulesEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// RulesMaxTokens returns the effective AGENTS.md token budget.
func (c RulesConfig) RulesMaxTokens() int {
	if c.MaxTokens <= 0 {
		return DefaultRulesMaxTokens
	}
	return c.MaxTokens
}

// MemoryEnabled reports whether memory use (inject + tools) is on.
func (c MemoryConfig) MemoryEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// MemoryGenerate reports whether auto extraction is on.
func (c MemoryConfig) MemoryGenerate() bool {
	if c.Generate == nil {
		return true
	}
	return *c.Generate
}

// MemorySummaryTokens returns the effective summary budget.
func (c MemoryConfig) MemorySummaryTokens() int {
	if c.MaxSummaryTokens <= 0 {
		return DefaultMemorySummaryTokens
	}
	return c.MaxSummaryTokens
}

// MemoryIdleAfterDuration parses idle_after or returns the default.
func (c MemoryConfig) MemoryIdleAfterDuration() (time.Duration, error) {
	raw := strings.TrimSpace(c.IdleAfter)
	if raw == "" {
		raw = DefaultMemoryIdleAfter
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("memory.idle_after: %w", err)
	}
	if d <= 0 {
		return 0, errors.New("memory.idle_after must be > 0")
	}
	return d, nil
}

// MemoryScanMaxAgeDuration parses scan_max_age or returns the default.
func (c MemoryConfig) MemoryScanMaxAgeDuration() (time.Duration, error) {
	raw := strings.TrimSpace(c.ScanMaxAge)
	if raw == "" {
		raw = DefaultMemoryScanMaxAge
	}
	// Accept day suffix "Nd" which time.ParseDuration does not.
	if strings.HasSuffix(raw, "d") && len(raw) > 1 {
		days := strings.TrimSuffix(raw, "d")
		n, err := parsePositiveInt(days)
		if err != nil {
			return 0, fmt.Errorf("memory.scan_max_age: %w", err)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("memory.scan_max_age: %w", err)
	}
	if d <= 0 {
		return 0, errors.New("memory.scan_max_age must be > 0")
	}
	return d, nil
}

// MemoryMaxRollouts returns the per-scan claim cap.
func (c MemoryConfig) MemoryMaxRollouts() int {
	if c.MaxRolloutsPerScan <= 0 {
		return DefaultMemoryMaxRolloutsPerScan
	}
	return c.MaxRolloutsPerScan
}

func parsePositiveInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer %q", s)
		}
		n = n*10 + int(r-'0')
		if n > 3650 {
			return 0, fmt.Errorf("integer too large: %q", s)
		}
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be > 0: %q", s)
	}
	return n, nil
}

// WorkspaceConfig is the Codex-style workspace root for path tools and shell cwd clamp.
type WorkspaceConfig struct {
	// Root is the workspace clamp. Empty uses process cwd at startup.
	Root string `toml:"root"`
}

// PermissionsConfig is Claude Code-style allow/ask/deny rule lists.
// Rules use Tool or Tool(specifier), e.g. Shell(git status), ApplyPatch(.env).
type PermissionsConfig struct {
	// Profile seeds built-in rules before user lists (default cautious).
	Profile string `toml:"profile"`
	// Allow rules auto-approve matching tool calls (still subject to opaque-shell downgrade for Bash).
	Allow []string `toml:"allow"`
	// Ask rules force a human prompt when matched.
	Ask []string `toml:"ask"`
	// Deny rules block matching tool calls.
	Deny []string `toml:"deny"`
}

// UIConfig controls interactive display options.
type UIConfig struct {
	// ShowTurnUsage controls the post-turn API usage footer in the transcript.
	// Nil (omitted) defaults to true. Set show_turn_usage: false to hide.
	ShowTurnUsage *bool `toml:"show_turn_usage"`
}

// TurnUsageEnabled reports whether the per-turn usage footer should be shown.
func (c UIConfig) TurnUsageEnabled() bool {
	if c.ShowTurnUsage == nil {
		return true
	}
	return *c.ShowTurnUsage
}

// ToolsConfig holds runtime limits for Codex-subset tools (not permission language).
type ToolsConfig struct {
	// Shell configures the shell tool runtime.
	Shell ShellToolConfig `toml:"shell"`
	// ApplyPatch configures the apply_patch tool runtime.
	ApplyPatch ApplyPatchToolConfig `toml:"apply_patch"`
}

// ApplyPatchToolConfig configures apply_patch limits.
type ApplyPatchToolConfig struct {
	Disabled bool `toml:"disabled"`
	// MaxBytes caps create/update file size. Zero uses 256KiB.
	MaxBytes int `toml:"max_bytes"`
}

// ShellToolConfig configures the shell tool runtime.
type ShellToolConfig struct {
	Disabled       bool   `toml:"disabled"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
	MaxOutputBytes int    `toml:"max_output_bytes"`
	WorkingDir     string `toml:"working_dir"`
}

const (
	// SandboxModeWorkspaceWrite permits writes only inside the mounted workspace.
	SandboxModeWorkspaceWrite = "workspace-write"
	// SandboxModeReadOnly mounts the workspace read-only.
	SandboxModeReadOnly = "read-only"

	// DefaultRuntimeMaxTurnSeconds bounds a complete ReAct turn when no runtime
	// override is configured.
	DefaultRuntimeMaxTurnSeconds = 600
	// DefaultRuntimeMaxReactSteps bounds model/tool loop iterations by default.
	DefaultRuntimeMaxReactSteps = 8
	// DefaultRuntimeMaxToolCalls bounds all tool invocations within one turn.
	DefaultRuntimeMaxToolCalls = 16
)

const (
	maxRuntimeTurnSeconds = 3_600
	maxRuntimeReactSteps  = 64
	maxRuntimeToolCalls   = 128
)

// SandboxConfig describes the filesystem and network boundary applied to
// side-effecting tools. The host process remains outside this boundary.
type SandboxConfig struct {
	// Mode is workspace-write (default) or read-only.
	Mode string `toml:"mode"`
	// ReadOnlyRoots are explicit absolute host directories exposed read-only to a worker.
	ReadOnlyRoots []string `toml:"read_only_roots"`
	// ProtectedPaths append workspace-relative literal deny paths to the built-in set.
	ProtectedPaths []string             `toml:"protected_paths"`
	Network        SandboxNetworkConfig `toml:"network"`
}

// SandboxNetworkConfig controls worker HTTP(S) egress through the sandbox proxy.
type SandboxNetworkConfig struct {
	// AllowedDomains is an exact DNS hostname allowlist. An empty list disables egress.
	AllowedDomains []string `toml:"allowed_domains"`
}

// RuntimeConfig bounds one agent turn independently from per-command shell limits.
type RuntimeConfig struct {
	// MaxTurnSeconds is the total ReAct turn deadline. Zero uses 600 seconds.
	MaxTurnSeconds int `toml:"max_turn_seconds"`
	// MaxReactSteps is the ReAct model/tool loop budget. Zero uses 8.
	MaxReactSteps int `toml:"max_react_steps"`
	// MaxToolCalls is the total tool-invocation budget per turn. Zero uses 16.
	MaxToolCalls int `toml:"max_tool_calls"`
}

// ApprovalPolicyNormalized returns Codex-style approval_policy as on_request|never.
func (c Config) ApprovalPolicyNormalized() string {
	switch strings.ToLower(strings.TrimSpace(c.ApprovalPolicy)) {
	case "", "on-request", "on_request":
		return "on_request"
	case "never":
		return "never"
	default:
		return strings.ToLower(strings.TrimSpace(c.ApprovalPolicy))
	}
}

// ModeNormalized returns the effective sandbox mode, using workspace-write
// when the TOML field is omitted. Validation rejects any other value.
func (c SandboxConfig) ModeNormalized() string {
	switch mode := strings.ToLower(strings.TrimSpace(c.Mode)); mode {
	case "", SandboxModeWorkspaceWrite:
		return SandboxModeWorkspaceWrite
	case SandboxModeReadOnly:
		return SandboxModeReadOnly
	default:
		return mode
	}
}

// ResolveReadOnlyRoots validates and canonicalizes configured read-only roots.
// It returns a de-duplicated copy so callers can safely pass it to a worker.
func (c SandboxConfig) ResolveReadOnlyRoots() ([]string, error) {
	if len(c.ReadOnlyRoots) == 0 {
		return nil, nil
	}

	roots := make([]string, 0, len(c.ReadOnlyRoots))
	seen := make(map[string]struct{}, len(c.ReadOnlyRoots))
	for _, raw := range c.ReadOnlyRoots {
		root := strings.TrimSpace(raw)
		if root == "" {
			return nil, errors.New("sandbox.read_only_roots entries must not be empty")
		}
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("sandbox.read_only_roots %q must be an absolute path", raw)
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return nil, fmt.Errorf("sandbox.read_only_roots %q: %w", root, err)
		}
		resolved, err = filepath.Abs(resolved)
		if err != nil {
			return nil, fmt.Errorf("resolve sandbox.read_only_roots %q: %w", root, err)
		}
		resolved = filepath.Clean(resolved)
		if filepath.Dir(resolved) == resolved {
			return nil, fmt.Errorf("sandbox.read_only_roots %q must not be a filesystem root", root)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("sandbox.read_only_roots %q: %w", root, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("sandbox.read_only_roots %q is not a directory", root)
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		roots = append(roots, resolved)
	}
	return roots, nil
}

// EffectiveProtectedPaths returns the immutable built-in deny paths followed
// by the configuration's additional workspace-relative literal paths.
func (c SandboxConfig) EffectiveProtectedPaths() []string {
	paths := make([]string, 0, len(defaultSandboxProtectedPaths)+len(c.ProtectedPaths))
	seen := make(map[string]struct{}, cap(paths))
	for _, candidates := range [][]string{defaultSandboxProtectedPaths, c.ProtectedPaths} {
		for _, protected := range candidates {
			if _, ok := seen[protected]; ok {
				continue
			}
			seen[protected] = struct{}{}
			paths = append(paths, protected)
		}
	}
	return paths
}

// EffectiveMaxTurnSeconds returns the configured turn deadline or its product default.
func (c RuntimeConfig) EffectiveMaxTurnSeconds() int {
	if c.MaxTurnSeconds == 0 {
		return DefaultRuntimeMaxTurnSeconds
	}
	return c.MaxTurnSeconds
}

// EffectiveMaxReactSteps returns the configured ReAct step budget or its product default.
func (c RuntimeConfig) EffectiveMaxReactSteps() int {
	if c.MaxReactSteps == 0 {
		return DefaultRuntimeMaxReactSteps
	}
	return c.MaxReactSteps
}

// EffectiveMaxToolCalls returns the configured per-turn tool budget or its product default.
func (c RuntimeConfig) EffectiveMaxToolCalls() int {
	if c.MaxToolCalls == 0 {
		return DefaultRuntimeMaxToolCalls
	}
	return c.MaxToolCalls
}

// Normalize fills omitted runtime limits with product defaults.
func (c RuntimeConfig) Normalize() RuntimeConfig {
	c.MaxTurnSeconds = c.EffectiveMaxTurnSeconds()
	c.MaxReactSteps = c.EffectiveMaxReactSteps()
	c.MaxToolCalls = c.EffectiveMaxToolCalls()
	return c
}

// PermissionsProfile returns the permissions profile name (default cautious).
func (c PermissionsConfig) PermissionsProfile() string {
	p := strings.ToLower(strings.TrimSpace(c.Profile))
	if p == "" {
		return "cautious"
	}
	return p
}

// StorageConfig controls local session persistence.
type StorageConfig struct {
	// DataDir is the root for session data. Empty uses ~/.eino-assistant.
	// Sessions are stored under <DataDir>/sessions/<id>/.
	DataDir string `toml:"data_dir"`
}

// ModelContextConfig controls the model-facing working context. Raw thread
// turns remain in the event ledger and are never compacted in place.
type ModelContextConfig struct {
	// WindowTokens is the model's total context capacity.
	WindowTokens int `toml:"window_tokens"`
	// MaxOutputTokens limits one model response and is reserved from the prompt.
	MaxOutputTokens int `toml:"max_output_tokens"`
	// KeepRecentTurns keeps this many trailing complete turn groups in the hot window.
	KeepRecentTurns int `toml:"keep_recent_turns"`
	// AutoCompactTriggerPercent starts automatic compaction at this percentage
	// of the usable input budget (default 75).
	AutoCompactTriggerPercent int `toml:"auto_compact_trigger_percent"`
	// PostCompactTargetPercent is the target utilization after compaction (default 45).
	PostCompactTargetPercent int `toml:"post_compact_target_percent"`
	// SummaryMaxTokens caps a structured checkpoint's model-visible rendering.
	SummaryMaxTokens int `toml:"summary_max_tokens"`
	// MaxLowGainAttempts is consecutive automatic low-gain failures before
	// auto-compaction pauses (default 2). Zero uses the product default.
	// Hard failures still pause immediately.
	MaxLowGainAttempts int `toml:"max_low_gain_attempts"`
	// LowGainThresholdPercent defines the minimum useful token release percentage.
	LowGainThresholdPercent int `toml:"low_gain_threshold_percent"`
}

// PricingConfig is USD per 1M tokens for cost display.
type PricingConfig struct {
	InputPerMillion  float64 `toml:"input_per_million"`
	OutputPerMillion float64 `toml:"output_per_million"`
}

const (
	// ProviderOpenAI identifies an OpenAI Chat Completions-compatible endpoint.
	ProviderOpenAI = "openai"
	// ProviderAnthropic identifies an Anthropic Messages API endpoint.
	ProviderAnthropic = "anthropic"
)

// ModelConfig describes a configured chat-model provider endpoint.
type ModelConfig struct {
	Provider       string             `toml:"provider"`
	BaseURL        string             `toml:"base_url"`
	APIKey         string             `toml:"api_key"`
	Name           string             `toml:"name"`
	TimeoutSeconds int                `toml:"timeout_seconds"`
	Context        ModelContextConfig `toml:"context"`
	Pricing        PricingConfig      `toml:"pricing"`
}

// AssistantConfig controls the local assistant's initial conversation state.
type AssistantConfig struct {
	SystemPrompt string `toml:"system_prompt"`
}

// Load reads a strict TOML file and validates the values needed to run.
// Unknown keys are rejected (strict decode). Only the .toml extension is accepted.
func Load(path string) (Config, error) {
	if strings.ToLower(filepath.Ext(path)) != ".toml" {
		return Config{}, errors.New("configuration file must use the .toml extension")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}

	var cfg Config
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("parse TOML configuration: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return Config{}, fmt.Errorf("parse TOML configuration: unknown field %s", undecoded[0].String())
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks configuration without including sensitive values in errors.
func (c *Config) Validate() error {
	if err := c.Model.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Assistant.SystemPrompt) == "" {
		return errors.New("assistant.system_prompt is required")
	}
	// storage.data_dir may be empty (default applied by ResolveDataDir).
	switch c.ApprovalPolicyNormalized() {
	case "on_request", "never":
	default:
		return fmt.Errorf("approval_policy must be on-request or never")
	}
	if profile := c.Permissions.PermissionsProfile(); profile != "cautious" {
		return fmt.Errorf("permissions.profile %q is unsupported (supported: cautious)", c.Permissions.Profile)
	}
	// Fail fast on invalid Claude-style rules.
	if _, err := c.BuildPermissions(); err != nil {
		return err
	}
	if err := c.Workspace.Validate(); err != nil {
		return err
	}
	if err := c.Tools.Shell.Validate(); err != nil {
		return err
	}
	if err := c.Tools.ApplyPatch.Validate(); err != nil {
		return err
	}
	if err := c.Sandbox.Validate(); err != nil {
		return err
	}
	if err := c.Rules.Validate(); err != nil {
		return err
	}
	if err := c.Memory.Validate(); err != nil {
		return err
	}
	return c.Runtime.Validate()
}

// Validate checks rules configuration.
func (c RulesConfig) Validate() error {
	if c.MaxTokens < 0 {
		return errors.New("rules.max_tokens must be >= 0")
	}
	if c.MaxTokens > 100_000 {
		return errors.New("rules.max_tokens must be <= 100000")
	}
	return nil
}

// Validate checks memory configuration.
func (c MemoryConfig) Validate() error {
	if c.MaxSummaryTokens < 0 {
		return errors.New("memory.max_summary_tokens must be >= 0")
	}
	if c.MaxSummaryTokens > 50_000 {
		return errors.New("memory.max_summary_tokens must be <= 50000")
	}
	if c.MaxRolloutsPerScan < 0 {
		return errors.New("memory.max_rollouts_per_scan must be >= 0")
	}
	if c.MaxRolloutsPerScan > 32 {
		return errors.New("memory.max_rollouts_per_scan must be <= 32")
	}
	if _, err := c.MemoryIdleAfterDuration(); err != nil {
		return err
	}
	if _, err := c.MemoryScanMaxAgeDuration(); err != nil {
		return err
	}
	return nil
}

// BuildPermissions compiles Claude-style [permissions] rules for the tool layer.
func (c Config) BuildPermissions() (*tools.PermissionSet, error) {
	return tools.BuildPermissionSet(
		c.Permissions.PermissionsProfile(),
		c.Permissions.Allow,
		c.Permissions.Ask,
		c.Permissions.Deny,
	)
}

// Validate checks workspace.root when set.
func (c WorkspaceConfig) Validate() error {
	root := strings.TrimSpace(c.Root)
	if root == "" {
		return nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("workspace.root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("workspace.root %q: %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("workspace.root %q is not a directory", abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("workspace.root %q: %w", abs, err)
	}
	if filepath.Dir(filepath.Clean(resolved)) == filepath.Clean(resolved) {
		return fmt.Errorf("workspace.root %q must not be a filesystem root", abs)
	}
	return nil
}

func (c ApplyPatchToolConfig) Validate() error {
	const maxBytes = 1 << 20
	if c.MaxBytes < 0 {
		return errors.New("tools.apply_patch.max_bytes must be >= 0")
	}
	if c.MaxBytes > maxBytes {
		return fmt.Errorf("tools.apply_patch.max_bytes must be <= %d", maxBytes)
	}
	return nil
}

const (
	maxRunCommandTimeoutSeconds = 300
	maxRunCommandOutputBytes    = 1 << 20
)

// Validate checks tools.shell runtime settings.
func (c ShellToolConfig) Validate() error {
	if c.TimeoutSeconds < 0 {
		return errors.New("tools.shell.timeout_seconds must be >= 0")
	}
	if c.TimeoutSeconds > maxRunCommandTimeoutSeconds {
		return fmt.Errorf("tools.shell.timeout_seconds must be <= %d", maxRunCommandTimeoutSeconds)
	}
	if c.MaxOutputBytes < 0 {
		return errors.New("tools.shell.max_output_bytes must be >= 0")
	}
	if c.MaxOutputBytes > maxRunCommandOutputBytes {
		return fmt.Errorf("tools.shell.max_output_bytes must be <= %d", maxRunCommandOutputBytes)
	}
	if dir := strings.TrimSpace(c.WorkingDir); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("tools.shell.working_dir: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("tools.shell.working_dir %q: %w", abs, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("tools.shell.working_dir %q is not a directory", abs)
		}
	}
	return nil
}

var defaultSandboxProtectedPaths = []string{
	".git",
	".agents",
	".codex",
	".eino",
	".env",
}

// DefaultSandboxProtectedPaths returns a copy of deny paths that cannot be
// removed from configuration. SandboxConfig.ProtectedPaths only appends to it.
func DefaultSandboxProtectedPaths() []string {
	return append([]string(nil), defaultSandboxProtectedPaths...)
}

// Validate checks sandbox settings and normalizes values that must be stable
// before a worker process is created.
func (c *SandboxConfig) Validate() error {
	if c == nil {
		return nil
	}
	switch c.ModeNormalized() {
	case SandboxModeWorkspaceWrite, SandboxModeReadOnly:
		// valid
	default:
		return fmt.Errorf("sandbox.mode must be %q or %q", SandboxModeWorkspaceWrite, SandboxModeReadOnly)
	}

	roots, err := c.ResolveReadOnlyRoots()
	if err != nil {
		return err
	}
	c.ReadOnlyRoots = roots

	protected, err := normalizeProtectedPaths(c.ProtectedPaths)
	if err != nil {
		return err
	}
	c.ProtectedPaths = protected

	return c.Network.Validate()
}

func normalizeProtectedPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		pattern := strings.TrimSpace(raw)
		if pattern == "" {
			return nil, errors.New("sandbox.protected_paths entries must not be empty")
		}
		if strings.ContainsRune(pattern, 0) || strings.ContainsAny(pattern, "\\:\r\n") || path.IsAbs(pattern) {
			return nil, fmt.Errorf("sandbox.protected_paths %q must be workspace-relative", raw)
		}

		// Backends can enforce a literal leaf or a literal directory subtree. Do
		// not accept general globs: bwrap cannot apply them safely to writable
		// mounts, and divergent backend semantics would weaken the policy.
		recursive := strings.HasSuffix(pattern, "/**")
		literal := pattern
		if recursive {
			literal = strings.TrimSuffix(pattern, "/**")
		}
		if strings.ContainsAny(literal, "*?[]") {
			return nil, fmt.Errorf("sandbox.protected_paths %q must be a literal path or literal /** subtree", raw)
		}
		for _, part := range strings.Split(literal, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, fmt.Errorf("sandbox.protected_paths %q must be a literal workspace-relative path", raw)
			}
		}
		if recursive {
			pattern = literal + "/**"
		}
		if _, ok := seen[pattern]; ok {
			continue
		}
		seen[pattern] = struct{}{}
		normalized = append(normalized, pattern)
	}
	return normalized, nil
}

// Validate checks and canonicalizes network egress hostnames.
func (c *SandboxNetworkConfig) Validate() error {
	if c == nil || len(c.AllowedDomains) == 0 {
		return nil
	}
	domains := make([]string, 0, len(c.AllowedDomains))
	seen := make(map[string]struct{}, len(c.AllowedDomains))
	for _, raw := range c.AllowedDomains {
		domain, err := normalizeAllowedDomain(raw)
		if err != nil {
			return err
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	c.AllowedDomains = domains
	return nil
}

func normalizeAllowedDomain(raw string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(raw))
	if domain == "" {
		return "", errors.New("sandbox.network.allowed_domains entries must not be empty")
	}
	if net.ParseIP(domain) != nil {
		return "", fmt.Errorf("sandbox.network.allowed_domains %q must be a DNS hostname, not an IP address", raw)
	}
	if len(domain) > 253 || strings.ContainsAny(domain, ":/@?#[\\]") || strings.Contains(domain, "*") {
		return "", fmt.Errorf("sandbox.network.allowed_domains %q must be an exact DNS hostname", raw)
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("sandbox.network.allowed_domains %q must be an exact DNS hostname", raw)
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
				return "", fmt.Errorf("sandbox.network.allowed_domains %q must be an exact DNS hostname", raw)
			}
		}
	}
	return domain, nil
}

// Validate checks runtime guardrails. Zero means use the documented product default.
func (c RuntimeConfig) Validate() error {
	for _, limit := range []struct {
		name  string
		value int
		max   int
	}{
		{name: "runtime.max_turn_seconds", value: c.MaxTurnSeconds, max: maxRuntimeTurnSeconds},
		{name: "runtime.max_react_steps", value: c.MaxReactSteps, max: maxRuntimeReactSteps},
		{name: "runtime.max_tool_calls", value: c.MaxToolCalls, max: maxRuntimeToolCalls},
	} {
		if limit.value < 0 {
			return fmt.Errorf("%s must be >= 0", limit.name)
		}
		if limit.value > limit.max {
			return fmt.Errorf("%s must be <= %d", limit.name, limit.max)
		}
	}
	return nil
}

func validatePercent(name string, value int) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("%s must be between 0 and 100", name)
	}
	return nil
}

// ResolveDataDir returns the absolute session storage root.
// Empty DataDir defaults to ~/.eino-assistant. Session files live under
// <root>/sessions/<id>/.
func (c StorageConfig) ResolveDataDir() (string, error) {
	dir := strings.TrimSpace(c.DataDir)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dir = filepath.Join(home, ".eino-assistant")
	}
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve storage.data_dir: %w", err)
		}
		dir = abs
	}
	return dir, nil
}

// Validate checks the model fields independently for model construction.
// An omitted provider is normalized to the backward-compatible OpenAI default.
func (c *ModelConfig) Validate() error {
	c.Provider = strings.TrimSpace(c.Provider)
	if c.Provider == "" {
		c.Provider = ProviderOpenAI
	}
	if c.Provider != ProviderOpenAI && c.Provider != ProviderAnthropic {
		return fmt.Errorf("model.provider must be %q or %q", ProviderOpenAI, ProviderAnthropic)
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("model.base_url is required")
	}

	endpoint, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return errors.New("model.base_url must be an absolute http or https URL")
	}

	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("model.api_key is required")
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("model.name is required")
	}
	if c.TimeoutSeconds <= 0 {
		return errors.New("model.timeout_seconds must be greater than zero")
	}
	if err := c.Context.Validate(); err != nil {
		return err
	}
	return c.Pricing.Validate()
}

// Validate checks that local price rates can safely produce durable costs.
func (c PricingConfig) Validate() error {
	for _, rate := range []struct {
		name  string
		value float64
	}{
		{name: "model.pricing.input_per_million", value: c.InputPerMillion},
		{name: "model.pricing.output_per_million", value: c.OutputPerMillion},
	} {
		if rate.value < 0 || math.IsNaN(rate.value) || math.IsInf(rate.value, 0) {
			return fmt.Errorf("%s must be a finite value >= 0", rate.name)
		}
	}
	return nil
}

// Validate checks the model-specific context budget and compaction settings.
func (c ModelContextConfig) Validate() error {
	if c.WindowTokens <= 0 {
		return errors.New("model.context.window_tokens must be greater than zero")
	}
	if c.MaxOutputTokens <= 0 {
		return errors.New("model.context.max_output_tokens must be greater than zero")
	}
	if c.MaxOutputTokens >= c.WindowTokens {
		return errors.New("model.context.max_output_tokens must be smaller than model.context.window_tokens")
	}
	if c.KeepRecentTurns < 0 {
		return errors.New("model.context.keep_recent_turns must be >= 0")
	}
	if err := validatePercent("model.context.auto_compact_trigger_percent", c.AutoCompactTriggerPercent); err != nil {
		return err
	}
	if err := validatePercent("model.context.post_compact_target_percent", c.PostCompactTargetPercent); err != nil {
		return err
	}
	if err := validatePercent("model.context.low_gain_threshold_percent", c.LowGainThresholdPercent); err != nil {
		return err
	}
	if c.SummaryMaxTokens < 0 {
		return errors.New("model.context.summary_max_tokens must be >= 0")
	}
	if c.MaxLowGainAttempts < 0 {
		return errors.New("model.context.max_low_gain_attempts must be >= 0")
	}
	return nil
}
