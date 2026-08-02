package config

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the complete YAML configuration accepted by the CLI.
type Config struct {
	Model     ModelConfig     `yaml:"model"`
	Assistant AssistantConfig `yaml:"assistant"`
	Storage   StorageConfig   `yaml:"storage"`
	Context   ContextConfig   `yaml:"context"`
	Pricing   PricingConfig   `yaml:"pricing"`
	Tools     ToolsConfig     `yaml:"tools"`
}

// ToolsConfig controls which built-in agent tools are registered.
type ToolsConfig struct {
	RunCommand RunCommandToolConfig `yaml:"run_command"`
}

// RunCommandToolConfig configures the local shell tool.
// Zero values keep the product defaults (enabled, 60s timeout, 64KiB cap).
type RunCommandToolConfig struct {
	// Disabled skips registering run_command when true. Default is enabled.
	Disabled bool `yaml:"disabled"`
	// TimeoutSeconds is the default per-call timeout. Zero uses 60.
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// MaxOutputBytes caps each of stdout/stderr. Zero uses 65536.
	MaxOutputBytes int `yaml:"max_output_bytes"`
	// WorkingDir is the default cwd when the model omits working_dir.
	// Empty uses the process working directory at execution time.
	WorkingDir string `yaml:"working_dir"`
}

// StorageConfig controls local thread persistence.
type StorageConfig struct {
	// DataDir is the root for thread data. Empty uses ~/.eino-assistant.
	DataDir string `yaml:"data_dir"`
}

// ContextConfig controls the model-facing working context. Raw thread turns
// remain in the event ledger and are never compacted in place.
type ContextConfig struct {
	// KeepRecentTurns keeps this many trailing complete turn groups in the hot window.
	KeepRecentTurns int `yaml:"keep_recent_turns"`
	// ModelContextTokens is the model's total context capacity (default 32000).
	ModelContextTokens int `yaml:"model_context_tokens"`
	// OutputReserveTokens leaves space for the next response (default 4096).
	OutputReserveTokens int `yaml:"output_reserve_tokens"`
	// AutoCompactTriggerPercent starts automatic compaction at this percentage
	// of the usable input budget (default 75).
	AutoCompactTriggerPercent int `yaml:"auto_compact_trigger_percent"`
	// PostCompactTargetPercent is the target utilization after compaction (default 45).
	PostCompactTargetPercent int `yaml:"post_compact_target_percent"`
	// SummaryMaxTokens caps a structured checkpoint's model-visible rendering.
	SummaryMaxTokens int `yaml:"summary_max_tokens"`
	// MaxLowGainAttempts pauses automatic compaction after this many low-gain runs.
	MaxLowGainAttempts int `yaml:"max_low_gain_attempts"`
	// LowGainThresholdPercent defines the minimum useful token release percentage.
	LowGainThresholdPercent int `yaml:"low_gain_threshold_percent"`
}

// PricingConfig is USD per 1M tokens for cost display.
type PricingConfig struct {
	InputPerMillion  float64 `yaml:"input_per_million"`
	OutputPerMillion float64 `yaml:"output_per_million"`
}

// ModelConfig describes an OpenAI Chat Completions-compatible endpoint.
type ModelConfig struct {
	BaseURL        string `yaml:"base_url"`
	APIKey         string `yaml:"api_key"`
	Name           string `yaml:"name"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// AssistantConfig controls the local assistant's initial conversation state.
type AssistantConfig struct {
	SystemPrompt string `yaml:"system_prompt"`
}

// Load reads one strict YAML document and validates the values needed to run.
func Load(path string) (Config, error) {
	if strings.ToLower(filepath.Ext(path)) != ".yml" {
		return Config{}, errors.New("configuration file must use the .yml extension")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read configuration: %w", err)
	}

	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse YAML configuration: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("configuration must contain exactly one YAML document")
		}
		return Config{}, fmt.Errorf("parse YAML configuration: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks configuration without including sensitive values in errors.
func (c Config) Validate() error {
	if err := c.Model.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.Assistant.SystemPrompt) == "" {
		return errors.New("assistant.system_prompt is required")
	}
	if c.Pricing.InputPerMillion < 0 || c.Pricing.OutputPerMillion < 0 {
		return errors.New("pricing rates must be >= 0")
	}
	if c.Context.KeepRecentTurns < 0 {
		return errors.New("context.keep_recent_turns must be >= 0")
	}
	if c.Context.ModelContextTokens < 0 {
		return errors.New("context.model_context_tokens must be >= 0")
	}
	if c.Context.OutputReserveTokens < 0 {
		return errors.New("context.output_reserve_tokens must be >= 0")
	}
	effectiveModelContext := c.Context.ModelContextTokens
	if effectiveModelContext == 0 {
		effectiveModelContext = 32_000
	}
	effectiveOutputReserve := c.Context.OutputReserveTokens
	if effectiveOutputReserve == 0 {
		effectiveOutputReserve = 4_096
	}
	if effectiveOutputReserve >= effectiveModelContext {
		return errors.New("context.output_reserve_tokens must be smaller than context.model_context_tokens")
	}
	if err := validatePercent("context.auto_compact_trigger_percent", c.Context.AutoCompactTriggerPercent); err != nil {
		return err
	}
	if err := validatePercent("context.post_compact_target_percent", c.Context.PostCompactTargetPercent); err != nil {
		return err
	}
	if err := validatePercent("context.low_gain_threshold_percent", c.Context.LowGainThresholdPercent); err != nil {
		return err
	}
	if c.Context.SummaryMaxTokens < 0 {
		return errors.New("context.summary_max_tokens must be >= 0")
	}
	if c.Context.MaxLowGainAttempts < 0 {
		return errors.New("context.max_low_gain_attempts must be >= 0")
	}
	// storage.data_dir may be empty (default applied by ResolveDataDir).
	return c.Tools.RunCommand.Validate()
}

const (
	maxRunCommandTimeoutSeconds = 300
	maxRunCommandOutputBytes    = 1 << 20
)

// Validate checks run_command tool settings.
func (c RunCommandToolConfig) Validate() error {
	if c.TimeoutSeconds < 0 {
		return errors.New("tools.run_command.timeout_seconds must be >= 0")
	}
	if c.TimeoutSeconds > maxRunCommandTimeoutSeconds {
		return fmt.Errorf("tools.run_command.timeout_seconds must be <= %d", maxRunCommandTimeoutSeconds)
	}
	if c.MaxOutputBytes < 0 {
		return errors.New("tools.run_command.max_output_bytes must be >= 0")
	}
	if c.MaxOutputBytes > maxRunCommandOutputBytes {
		return fmt.Errorf("tools.run_command.max_output_bytes must be <= %d", maxRunCommandOutputBytes)
	}
	if dir := strings.TrimSpace(c.WorkingDir); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("tools.run_command.working_dir: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("tools.run_command.working_dir %q: %w", abs, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("tools.run_command.working_dir %q is not a directory", abs)
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

// ResolveDataDir returns the absolute thread storage root.
// Empty DataDir defaults to ~/.eino-assistant.
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
func (c ModelConfig) Validate() error {
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

	return nil
}
