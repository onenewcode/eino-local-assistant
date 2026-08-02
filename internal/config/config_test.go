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
		Storage:   StorageConfig{},
		Context:   ContextConfig{},
		Pricing:   PricingConfig{},
		Tools:     ToolsConfig{},
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

func TestLoadAcceptsStorageDataDir(t *testing.T) {
	yaml := validConfiguration + `storage:
  data_dir: "/tmp/eino-sessions-test"
`
	got, err := Load(writeConfiguration(t, yaml))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Storage.DataDir != "/tmp/eino-sessions-test" {
		t.Errorf("DataDir = %q", got.Storage.DataDir)
	}
}

func TestResolveDataDirDefaultAndAbsolute(t *testing.T) {
	abs, err := StorageConfig{DataDir: "/var/tmp/eino"}.ResolveDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if abs != "/var/tmp/eino" {
		t.Errorf("abs = %q", abs)
	}

	def, err := StorageConfig{}.ResolveDataDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(def, ".eino-assistant") {
		t.Errorf("default = %q, want suffix .eino-assistant", def)
	}
}

func TestLoadAcceptsContextConfig(t *testing.T) {
	yaml := validConfiguration + `context:
  keep_recent_turns: 8
  model_context_tokens: 32000
  output_reserve_tokens: 4096
  auto_compact_trigger_percent: 75
  post_compact_target_percent: 45
  summary_max_tokens: 2048
  max_low_gain_attempts: 2
  low_gain_threshold_percent: 15
`
	got, err := Load(writeConfiguration(t, yaml))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Context.KeepRecentTurns != 8 {
		t.Fatalf("context = %+v", got.Context)
	}
	if got.Context.ModelContextTokens != 32000 || got.Context.OutputReserveTokens != 4096 ||
		got.Context.AutoCompactTriggerPercent != 75 || got.Context.PostCompactTargetPercent != 45 ||
		got.Context.SummaryMaxTokens != 2048 || got.Context.MaxLowGainAttempts != 2 ||
		got.Context.LowGainThresholdPercent != 15 {
		t.Fatalf("advanced context = %+v", got.Context)
	}
}

func TestLoadAcceptsRunCommandToolConfig(t *testing.T) {
	dir := t.TempDir()
	yaml := validConfiguration + `tools:
  run_command:
    disabled: true
    timeout_seconds: 90
    max_output_bytes: 8192
    working_dir: "` + dir + `"
`
	got, err := Load(writeConfiguration(t, yaml))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !got.Tools.RunCommand.Disabled {
		t.Fatal("expected run_command.disabled=true")
	}
	if got.Tools.RunCommand.TimeoutSeconds != 90 || got.Tools.RunCommand.MaxOutputBytes != 8192 {
		t.Fatalf("run_command = %+v", got.Tools.RunCommand)
	}
	if got.Tools.RunCommand.WorkingDir != dir {
		t.Fatalf("working_dir = %q, want %q", got.Tools.RunCommand.WorkingDir, dir)
	}
}

func TestLoadRejectsInvalidRunCommandToolConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "timeout too large",
			yaml: validConfiguration + `tools:
  run_command:
    timeout_seconds: 999
`,
			want: "tools.run_command.timeout_seconds must be <=",
		},
		{
			name: "negative max output",
			yaml: validConfiguration + `tools:
  run_command:
    max_output_bytes: -1
`,
			want: "tools.run_command.max_output_bytes must be >= 0",
		},
		{
			name: "missing working dir",
			yaml: validConfiguration + `tools:
  run_command:
    working_dir: "/definitely/missing/eino-run-command-dir"
`,
			want: "tools.run_command.working_dir",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfiguration(t, tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadRejectsInvalidContextCompactionConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "reserve exceeds model context",
			yaml: validConfiguration + `context:
  model_context_tokens: 4096
  output_reserve_tokens: 4096
`,
			want: "output_reserve_tokens must be smaller",
		},
		{
			name: "invalid trigger percent",
			yaml: validConfiguration + `context:
  auto_compact_trigger_percent: 101
`,
			want: "auto_compact_trigger_percent must be between",
		},
		{
			name: "negative attempt count",
			yaml: validConfiguration + `context:
  max_low_gain_attempts: -1
`,
			want: "max_low_gain_attempts must be >= 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfiguration(t, tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
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
