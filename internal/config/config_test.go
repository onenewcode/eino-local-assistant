package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/tools"
)

const validConfiguration = `
approval_policy = "on-request"

[model]
base_url = "https://api.example.test/v1"
api_key = "test-api-key"
name = "test-model"
timeout_seconds = 60

[model.context]
window_tokens = 32000
max_output_tokens = 4096

[model.pricing]
input_per_million = 0.0
output_per_million = 0.0

[assistant]
system_prompt = "You are a helpful assistant."
`

func TestLoadAcceptsOneCompleteConfiguration(t *testing.T) {
	got, err := Load(writeConfiguration(t, validConfiguration))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		ApprovalPolicy: "on-request",
		Model: ModelConfig{
			Provider:       ProviderOpenAI,
			BaseURL:        "https://api.example.test/v1",
			APIKey:         "test-api-key",
			Name:           "test-model",
			TimeoutSeconds: 60,
			Context: ModelContextConfig{
				WindowTokens:    32_000,
				MaxOutputTokens: 4_096,
			},
		},
		Assistant: AssistantConfig{SystemPrompt: "You are a helpful assistant."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadAcceptsCodexToolPermissions(t *testing.T) {
	dir := t.TempDir()
	doc := validConfiguration + `

[workspace]
root = "` + dir + `"

[permissions]
profile = "cautious"
allow = ["Shell(go test *)", "ApplyPatch(src/**)"]
ask = ["Shell(git push *)"]
deny = ["ApplyPatch(.env)", "Shell(sudo *)"]

[tools.shell]
timeout_seconds = 90
max_output_bytes = 8192
working_dir = "` + dir + `"

[tools.apply_patch]
max_bytes = 4096
`
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	perms, err := got.BuildPermissions()
	if err != nil {
		t.Fatal(err)
	}
	if ev := perms.EvaluateBash("go test ./..."); ev.Decision != tools.DecisionAllow {
		t.Fatalf("go test = %+v", ev)
	}
	if ev := perms.EvaluateBash("git push origin main"); ev.Decision != tools.DecisionAsk {
		t.Fatalf("git push = %+v", ev)
	}
	if got.Tools.Shell.TimeoutSeconds != 90 || got.Tools.ApplyPatch.MaxBytes != 4096 {
		t.Fatalf("tools = %#v", got.Tools)
	}
}

func TestLoadRejectsBadPermissionRule(t *testing.T) {
	doc := validConfiguration + `
[permissions]
allow = ["NotATool(foo)"]
`
	_, err := Load(writeConfiguration(t, doc))
	if err == nil || !strings.Contains(err.Error(), "unknown permission tool") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRequiresTOMLExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	_ = os.WriteFile(path, []byte(validConfiguration), 0o600)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), ".toml") {
		t.Fatalf("error = %v", err)
	}
}

func TestRulesGlobalMaxTokensDefaultsIndependently(t *testing.T) {
	if got := (RulesConfig{}).RulesGlobalMaxTokens(); got != DefaultGlobalRulesMaxTokens {
		t.Fatalf("default global rules budget = %d, want %d", got, DefaultGlobalRulesMaxTokens)
	}
	if got := (RulesConfig{GlobalMaxTokens: 37}).RulesGlobalMaxTokens(); got != 37 {
		t.Fatalf("configured global rules budget = %d, want 37", got)
	}
	if got := (RulesConfig{MaxTokens: 19, GlobalMaxTokens: 37}).RulesMaxTokens(); got != 19 {
		t.Fatalf("project rules budget = %d, want 19", got)
	}
}

func TestRulesValidateGlobalMaxTokens(t *testing.T) {
	if err := (RulesConfig{GlobalMaxTokens: -1}).Validate(); err == nil || !strings.Contains(err.Error(), "rules.global_max_tokens must be >= 0") {
		t.Fatalf("negative global budget error = %v", err)
	}
	if err := (RulesConfig{GlobalMaxTokens: 100001}).Validate(); err == nil || !strings.Contains(err.Error(), "rules.global_max_tokens must be <= 100000") {
		t.Fatalf("oversized global budget error = %v", err)
	}
}

func TestLoadRejectsInvalidShellTimeout(t *testing.T) {
	doc := validConfiguration + `
[tools.shell]
timeout_seconds = 999
`
	_, err := Load(writeConfiguration(t, doc))
	if err == nil || !strings.Contains(err.Error(), "tools.shell.timeout_seconds") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadSandboxAndRuntimeDefaults(t *testing.T) {
	got, err := Load(writeConfiguration(t, validConfiguration))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Sandbox.ModeNormalized() != SandboxModeWorkspaceWrite {
		t.Fatalf("sandbox mode = %q", got.Sandbox.ModeNormalized())
	}
	if roots, err := got.Sandbox.ResolveReadOnlyRoots(); err != nil || len(roots) != 0 {
		t.Fatalf("read-only roots = %v, %v", roots, err)
	}
	if got.Runtime.EffectiveMaxTurnSeconds() != DefaultRuntimeMaxTurnSeconds ||
		got.Runtime.EffectiveMaxReactSteps() != DefaultRuntimeMaxReactSteps ||
		got.Runtime.EffectiveMaxToolCalls() != DefaultRuntimeMaxToolCalls {
		t.Fatalf("runtime defaults = %#v", got.Runtime.Normalize())
	}

	protected := got.Sandbox.EffectiveProtectedPaths()
	if !reflect.DeepEqual(protected, DefaultSandboxProtectedPaths()) {
		t.Fatalf("protected paths = %#v", protected)
	}
}

func TestLoadNormalizesSandboxSettings(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "toolchain-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	doc := validConfiguration + fmt.Sprintf(`
[sandbox]
mode = "read-only"
read_only_roots = [%q, %q]
protected_paths = ["secrets", "secrets"]

[sandbox.network]
allowed_domains = ["API.Example.TEST", "api.example.test"]

[runtime]
max_turn_seconds = 120
max_react_steps = 4
max_tool_calls = 12
`, link, realRoot)
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Sandbox.ModeNormalized() != SandboxModeReadOnly {
		t.Fatalf("sandbox mode = %q", got.Sandbox.ModeNormalized())
	}
	canonicalRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Sandbox.ReadOnlyRoots, []string{canonicalRoot}) {
		t.Fatalf("read-only roots = %#v, want %#v", got.Sandbox.ReadOnlyRoots, []string{canonicalRoot})
	}
	if !reflect.DeepEqual(got.Sandbox.Network.AllowedDomains, []string{"api.example.test"}) {
		t.Fatalf("allowed domains = %#v", got.Sandbox.Network.AllowedDomains)
	}
	wantProtected := append(DefaultSandboxProtectedPaths(), "secrets")
	if !reflect.DeepEqual(got.Sandbox.EffectiveProtectedPaths(), wantProtected) {
		t.Fatalf("effective protected paths = %#v, want %#v", got.Sandbox.EffectiveProtectedPaths(), wantProtected)
	}
	if got.Runtime.Normalize() != (RuntimeConfig{MaxTurnSeconds: 120, MaxReactSteps: 4, MaxToolCalls: 12}) {
		t.Fatalf("runtime = %#v", got.Runtime.Normalize())
	}
}

func TestLoadAllowsLiteralProtectedSubtree(t *testing.T) {
	doc := validConfiguration + `
[sandbox]
protected_paths = ["private/**", "private/**", "config/local/**"]
`
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := append(DefaultSandboxProtectedPaths(), "private/**", "config/local/**")
	if !reflect.DeepEqual(got.Sandbox.EffectiveProtectedPaths(), want) {
		t.Fatalf("effective protected paths = %#v, want %#v", got.Sandbox.EffectiveProtectedPaths(), want)
	}
}

func TestLoadRejectsInvalidSandboxSettings(t *testing.T) {
	absentRoot := filepath.Join(t.TempDir(), "missing")
	fileRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(fileRoot, []byte("not a root"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "mode",
			doc:  "[sandbox]\nmode = \"permissive\"\n",
			want: "sandbox.mode",
		},
		{
			name: "filesystem root workspace",
			doc:  fmt.Sprintf("[workspace]\nroot = %q\n", string(filepath.Separator)),
			want: "workspace.root",
		},
		{
			name: "relative read-only root",
			doc:  "[sandbox]\nread_only_roots = [\"relative/path\"]\n",
			want: "sandbox.read_only_roots",
		},
		{
			name: "missing read-only root",
			doc:  fmt.Sprintf("[sandbox]\nread_only_roots = [%q]\n", absentRoot),
			want: "sandbox.read_only_roots",
		},
		{
			name: "file read-only root",
			doc:  fmt.Sprintf("[sandbox]\nread_only_roots = [%q]\n", fileRoot),
			want: "sandbox.read_only_roots",
		},
		{
			name: "filesystem root read-only root",
			doc:  fmt.Sprintf("[sandbox]\nread_only_roots = [%q]\n", string(filepath.Separator)),
			want: "sandbox.read_only_roots",
		},
		{
			name: "absolute protected path",
			doc:  "[sandbox]\nprotected_paths = [\"/tmp/secret\"]\n",
			want: "sandbox.protected_paths",
		},
		{
			name: "escaping protected path",
			doc:  "[sandbox]\nprotected_paths = [\"../secret\"]\n",
			want: "sandbox.protected_paths",
		},
		{
			name: "glob protected path",
			doc:  "[sandbox]\nprotected_paths = [\".env*\"]\n",
			want: "sandbox.protected_paths",
		},
		{
			name: "nonliteral recursive protected path",
			doc:  "[sandbox]\nprotected_paths = [\"private*/**\"]\n",
			want: "sandbox.protected_paths",
		},
		{
			name: "network URL",
			doc:  "[sandbox.network]\nallowed_domains = [\"https://api.example.test\"]\n",
			want: "sandbox.network.allowed_domains",
		},
		{
			name: "network wildcard",
			doc:  "[sandbox.network]\nallowed_domains = [\"*.example.test\"]\n",
			want: "sandbox.network.allowed_domains",
		},
		{
			name: "network port",
			doc:  "[sandbox.network]\nallowed_domains = [\"api.example.test:443\"]\n",
			want: "sandbox.network.allowed_domains",
		},
		{
			name: "network IP",
			doc:  "[sandbox.network]\nallowed_domains = [\"127.0.0.1\"]\n",
			want: "sandbox.network.allowed_domains",
		},
		{
			name: "negative runtime",
			doc:  "[runtime]\nmax_turn_seconds = -1\n",
			want: "runtime.max_turn_seconds",
		},
		{
			name: "runtime turn bound",
			doc:  "[runtime]\nmax_turn_seconds = 3601\n",
			want: "runtime.max_turn_seconds",
		},
		{
			name: "runtime react bound",
			doc:  "[runtime]\nmax_react_steps = 65\n",
			want: "runtime.max_react_steps",
		},
		{
			name: "runtime tool-call bound",
			doc:  "[runtime]\nmax_tool_calls = 129\n",
			want: "runtime.max_tool_calls",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfiguration(t, validConfiguration+"\n"+tc.doc))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func writeConfiguration(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
