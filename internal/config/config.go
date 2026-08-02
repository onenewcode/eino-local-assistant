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

	return nil
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
