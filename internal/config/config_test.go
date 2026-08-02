package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const validConfiguration = `model:
  base_url: "https://api.example.test/v1"
  api_key: "test-api-key"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: "You are a helpful assistant."
`

func TestLoadAcceptsOneCompleteConfiguration(t *testing.T) {
	path := writeConfiguration(t, validConfiguration)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	want := Config{
		Model: ModelConfig{
			BaseURL:        "https://api.example.test/v1",
			APIKey:         "test-api-key",
			Name:           "test-model",
			TimeoutSeconds: 60,
		},
		Assistant: AssistantConfig{SystemPrompt: "You are a helpful assistant."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadRejectsInvalidOrNonStrictYAML(t *testing.T) {
	const secret = "do-not-expose-this-api-key"

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "unknown nested field",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: 60
  retry_count: 3
assistant:
  system_prompt: "You are a helpful assistant."
`,
			wantErr: "field retry_count not found",
		},
		{
			name: "unknown top level field",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: "You are a helpful assistant."
logging:
  level: debug
`,
			wantErr: "field logging not found",
		},
		{
			name: "second YAML document",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: "You are a helpful assistant."
---
model: {}
`,
			wantErr: "exactly one YAML document",
		},
		{
			name: "wrong field type",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: sixty
assistant:
  system_prompt: "You are a helpful assistant."
`,
			wantErr: "cannot unmarshal",
		},
		{
			name: "malformed YAML",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: [
`,
			wantErr: "parse YAML configuration",
		},
		{
			name: "missing base URL",
			yaml: `model:
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: "You are a helpful assistant."
`,
			wantErr: "model.base_url is required",
		},
		{
			name: "missing API key",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: "You are a helpful assistant."
`,
			wantErr: "model.api_key is required",
		},
		{
			name: "missing model name",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  timeout_seconds: 60
assistant:
  system_prompt: "You are a helpful assistant."
`,
			wantErr: "model.name is required",
		},
		{
			name: "unsupported base URL scheme",
			yaml: `model:
  base_url: "ftp://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: "You are a helpful assistant."
`,
			wantErr: "model.base_url must be an absolute http or https URL",
		},
		{
			name: "nonpositive timeout",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: 0
assistant:
  system_prompt: "You are a helpful assistant."
`,
			wantErr: "model.timeout_seconds must be greater than zero",
		},
		{
			name: "blank system prompt",
			yaml: `model:
  base_url: "https://api.example.test/v1"
  api_key: "do-not-expose-this-api-key"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: "  "
`,
			wantErr: "assistant.system_prompt is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfiguration(t, tt.yaml))
			if err == nil {
				t.Fatal("Load() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() error = %q, want it to contain %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("Load() exposed API key in error: %q", err)
			}
		})
	}
}

func TestLoadDoesNotExposeAPIKeyForValidationErrors(t *testing.T) {
	const secret = "secret-value-that-must-never-appear"
	path := writeConfiguration(t, `model:
  base_url: "not an absolute URL"
  api_key: "secret-value-that-must-never-appear"
  name: "test-model"
  timeout_seconds: 60
assistant:
  system_prompt: "You are a helpful assistant."
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() exposed API key in error: %q", err)
	}
}

func TestLoadRequiresYMLExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(validConfiguration), 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want extension validation error")
	}
	if !strings.Contains(err.Error(), ".yml extension") {
		t.Errorf("Load() error = %q, want .yml extension error", err)
	}
}

func writeConfiguration(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write configuration: %v", err)
	}
	return path
}
