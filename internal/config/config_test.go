package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

[model.pricing]
input_per_million = 0.0
output_per_million = 0.0
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
				WindowTokens: 32_000,
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestUIStatusLineDefaultsAndValidatesConfiguredFields(t *testing.T) {
	got, err := Load(writeConfiguration(t, validConfiguration))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := []string{"model-with-reasoning", "context-used", "used-tokens", "task-progress"}; !reflect.DeepEqual(got.UI.StatusLineFields(), want) {
		t.Fatalf("default status line = %#v, want %#v", got.UI.StatusLineFields(), want)
	}
	if !got.UI.StatusLineThemeColorsEnabled() {
		t.Fatal("theme colors should default to enabled")
	}

	got, err = Load(writeConfiguration(t, validConfiguration+"\n[ui]\nstatus_line = [\" Model \", \"context-used\", \"policy\"]\nstatus_line_use_theme_colors = false\n"))
	if err != nil {
		t.Fatalf("Load(configured status line) error = %v", err)
	}
	if want := []string{"model", "context-used", "policy"}; !reflect.DeepEqual(got.UI.StatusLineFields(), want) {
		t.Fatalf("configured status line = %#v, want %#v", got.UI.StatusLineFields(), want)
	}
	if got.UI.StatusLineThemeColorsEnabled() {
		t.Fatal("explicitly disabled theme colors were ignored")
	}

	for _, doc := range []string{
		"[ui]\nstatus_line = [\"model\", \"unknown\"]\n",
		"[ui]\nstatus_line = [\"model\", \"model\"]\n",
	} {
		if _, err := Load(writeConfiguration(t, validConfiguration+"\n"+doc)); err == nil {
			t.Fatalf("Load(%q) succeeded for an invalid status line", doc)
		}
	}
}

func TestSaveStatusLineConfigPreservesUISettingsAndSourceComments(t *testing.T) {
	path := writeConfiguration(t, validConfiguration+`# keep this comment
[ui]
# keep this UI comment
show_turn_usage = false
status_line = [
  "session",
  "model",
]
`)
	if err := SaveStatusLineConfig(path, []string{"model-with-reasoning", "context-used", "used-tokens"}, false); err != nil {
		t.Fatalf("SaveStatusLineConfig() error = %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(saved configuration) error = %v", err)
	}
	if got.UI.TurnUsageEnabled() {
		t.Fatal("SaveStatusLineFields changed ui.show_turn_usage")
	}
	if want := []string{"model-with-reasoning", "context-used", "used-tokens"}; !reflect.DeepEqual(got.UI.StatusLineFields(), want) {
		t.Fatalf("saved status line = %#v, want %#v", got.UI.StatusLineFields(), want)
	}
	if got.UI.StatusLineThemeColorsEnabled() {
		t.Fatal("SaveStatusLineConfig did not persist ui.status_line_use_theme_colors")
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "# keep this UI comment") || !strings.Contains(string(source), `status_line = ["model-with-reasoning", "context-used", "used-tokens"]`) || !strings.Contains(string(source), "status_line_use_theme_colors = false") {
		t.Fatalf("saved source did not preserve unrelated UI content:\n%s", source)
	}
}

func TestLoadRejectsRemovedSystemPromptSetting(t *testing.T) {
	doc := validConfiguration + "\n[assistant]\nsystem_prompt = \"custom\"\n"
	_, err := Load(writeConfiguration(t, doc))
	if err == nil || !strings.Contains(err.Error(), "unknown field assistant") {
		t.Fatalf("Load(removed system prompt) error = %v", err)
	}
}

func TestLoadRejectsRemovedContextTuning(t *testing.T) {
	doc := strings.Replace(validConfiguration, "window_tokens = 32000", "window_tokens = 32000\nmax_output_tokens = 1024", 1)
	_, err := Load(writeConfiguration(t, doc))
	if err == nil || !strings.Contains(err.Error(), "was removed") {
		t.Fatalf("Load(removed context key) error = %v", err)
	}
}

func TestLoadRejectsYoloAsStaticApprovalPolicy(t *testing.T) {
	doc := strings.Replace(validConfiguration, `approval_policy = "on-request"`, `approval_policy = "yolo"`, 1)
	_, err := Load(writeConfiguration(t, doc))
	if err == nil || !strings.Contains(err.Error(), "approval_policy must be on-request or never") {
		t.Fatalf("Load(yolo approval_policy) error = %v, want static-policy rejection", err)
	}
}

func TestLoadNormalizesModelReasoningEffort(t *testing.T) {
	doc := strings.Replace(validConfiguration, "[model]\n", "[model]\nreasoning_effort = \" high \"\n", 1)
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Model.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q, want %q", got.Model.ReasoningEffort, "high")
	}
}

func TestLoadNormalizesConfiguredModelCatalog(t *testing.T) {
	doc := validConfiguration + `
[[model.catalog]]
name = "gpt-5.2-coding"
display_name = "Coding 5.2"
aliases = ["coding", " fast "]
description = "general coding model"
lifecycle = "ACTIVE"

[model.catalog.capabilities]
context_window_tokens = 128000
reasoning_efforts = ["low", " medium ", "low"]
default_reasoning_effort = " custom-effort "
input_modalities = ["text", "image"]
supports_tools = true
supports_streaming = true

[[model.catalog]]
name = "legacy-coding"
lifecycle = "retired"
`
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Model.Catalog) != 2 {
		t.Fatalf("catalog length = %d, want 2", len(got.Model.Catalog))
	}
	entry := got.Model.Catalog[0]
	if entry.DisplayName != "Coding 5.2" || entry.Lifecycle != "active" || entry.Aliases[1] != "fast" {
		t.Fatalf("normalized entry = %#v", entry)
	}
	if got := entry.Capabilities.ReasoningEfforts; !reflect.DeepEqual(got, []string{"low", "medium"}) {
		t.Fatalf("reasoning efforts = %#v", got)
	}
	if got := entry.Capabilities.DefaultReasoningEffort; got != "custom-effort" {
		t.Fatalf("default reasoning effort = %q, want %q", got, "custom-effort")
	}
	if entry.Capabilities.SupportsReasoning == nil || !*entry.Capabilities.SupportsReasoning {
		t.Fatal("reasoning efforts should declare reasoning support")
	}
	if got, ok := got.Model.ResolveCatalogName("FAST"); !ok || got != "gpt-5.2-coding" {
		t.Fatalf("alias resolution = %q, %v", got, ok)
	}
	if got, ok := got.Model.ResolveCatalogName("custom-deployment"); ok || got != "custom-deployment" {
		t.Fatalf("unknown model resolution = %q, %v", got, ok)
	}
	if got := got.Model.CatalogDisplayName("gpt-5.2-coding"); got != "Coding 5.2" {
		t.Fatalf("catalog display name = %q", got)
	}
}

func TestModelCatalogRejectsAmbiguousOrContradictoryMetadata(t *testing.T) {
	falseValue := false
	cases := []struct {
		name    string
		entries []ModelCatalogEntry
		want    string
	}{
		{
			name: "duplicate alias",
			entries: []ModelCatalogEntry{
				{Name: "one", Aliases: []string{"shared"}},
				{Name: "two", Aliases: []string{"shared"}},
			},
			want: "duplicates",
		},
		{
			name:    "invalid token",
			entries: []ModelCatalogEntry{{Name: "two words"}},
			want:    "single token",
		},
		{
			name: "reasoning contradiction",
			entries: []ModelCatalogEntry{{Name: "one", Capabilities: ModelCatalogCapabilities{
				SupportsReasoning: &falseValue,
				ReasoningEfforts:  []string{"low"},
			}}},
			want: "conflicts with reasoning_efforts",
		},
		{
			name: "default reasoning effort is one token",
			entries: []ModelCatalogEntry{{Name: "one", Capabilities: ModelCatalogCapabilities{
				DefaultReasoningEffort: "low medium",
			}}},
			want: "single token",
		},
		{
			name: "default reasoning effort rejects control characters",
			entries: []ModelCatalogEntry{{Name: "one", Capabilities: ModelCatalogCapabilities{
				DefaultReasoningEffort: "lo\x00w",
			}}},
			want: "single token",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeModelCatalog(tc.entries)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("normalizeModelCatalog() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestModelCatalogNormalizesBlankDefaultReasoningEffort(t *testing.T) {
	entries, err := normalizeModelCatalog([]ModelCatalogEntry{{
		Name: "catalog-model",
		Capabilities: ModelCatalogCapabilities{
			DefaultReasoningEffort: " \t",
		},
	}})
	if err != nil {
		t.Fatalf("normalizeModelCatalog() error = %v", err)
	}
	if got := entries[0].Capabilities.DefaultReasoningEffort; got != "" {
		t.Fatalf("blank default reasoning effort = %q, want empty", got)
	}
}

func TestModelCatalogEntriesReturnsDefensiveCopy(t *testing.T) {
	model := ModelConfig{
		Catalog: []ModelCatalogEntry{{
			Name: "catalog-model",
			Capabilities: ModelCatalogCapabilities{
				DefaultReasoningEffort: "custom-effort",
				ReasoningEfforts:       []string{"low"},
				InputModalities:        []string{"text"},
			},
		}},
	}

	entries := model.CatalogEntries()
	if len(entries) != 1 {
		t.Fatalf("catalog entries length = %d, want 1", len(entries))
	}
	if got := entries[0].Capabilities.DefaultReasoningEffort; got != "custom-effort" {
		t.Fatalf("default reasoning effort = %q, want %q", got, "custom-effort")
	}

	entries[0].Capabilities.DefaultReasoningEffort = "changed"
	entries[0].Capabilities.ReasoningEfforts[0] = "changed"
	entries[0].Capabilities.InputModalities[0] = "changed"
	if got := model.Catalog[0].Capabilities.DefaultReasoningEffort; got != "custom-effort" {
		t.Fatalf("source default reasoning effort = %q, want %q", got, "custom-effort")
	}
	if got := model.Catalog[0].Capabilities.ReasoningEfforts[0]; got != "low" {
		t.Fatalf("source reasoning effort = %q, want %q", got, "low")
	}
	if got := model.Catalog[0].Capabilities.InputModalities[0]; got != "text" {
		t.Fatalf("source input modality = %q, want %q", got, "text")
	}
}

func TestModelValidateNormalizesBlankReasoningEffort(t *testing.T) {
	model := ModelConfig{
		BaseURL:         "https://api.example.test/v1",
		APIKey:          "test-api-key",
		Name:            "test-model",
		ReasoningEffort: " \t",
		TimeoutSeconds:  60,
		Context: ModelContextConfig{
			WindowTokens: 32_000,
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatalf("ModelConfig.Validate() error = %v", err)
	}
	if model.ReasoningEffort != "" {
		t.Fatalf("blank reasoning effort = %q, want empty", model.ReasoningEffort)
	}
}

func TestLoadAcceptsToolRuntimeSettingsWithoutPermissionsTable(t *testing.T) {
	dir := t.TempDir()
	doc := validConfiguration + `

[workspace]
root = "` + dir + `"

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
	if got.Tools.Shell.TimeoutSeconds != 90 || got.Tools.ApplyPatch.MaxBytes != 4096 {
		t.Fatalf("tools = %#v", got.Tools)
	}
}

func TestLoadRejectsRemovedPermissionsTable(t *testing.T) {
	doc := validConfiguration + `
[permissions]
allow = ["NotATool(foo)"]
`
	_, err := Load(writeConfiguration(t, doc))
	if err == nil || !strings.Contains(err.Error(), "[permissions] is no longer supported") || !strings.Contains(err.Error(), "~/.eino-assistant/rules/*.rules") || !strings.Contains(err.Error(), "approval_policy") || !strings.Contains(err.Error(), "[sandbox].protected_paths") {
		t.Fatalf("Load() error = %v, want actionable permissions migration", err)
	}
}

func TestLoadAcceptsProjectTrustRecordInGlobalConfig(t *testing.T) {
	doc := validConfiguration + `
[projects."/absolute/workspace"]
trust_level = "trusted"
`
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Projects["/absolute/workspace"].TrustLevel != "trusted" {
		t.Fatalf("project trust = %#v", got.Projects)
	}
}

func TestUserConfigPathUsesUserApplicationDirectory(t *testing.T) {
	path, err := userConfigPath(func() (string, error) { return "/home/tester", nil })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/tester", UserConfigDirectory, UserConfigFileName); path != want {
		t.Fatalf("config path = %q, want %q", path, want)
	}
}

func TestUserConfigDirReturnsHomeResolutionError(t *testing.T) {
	want := errors.New("home unavailable")
	_, err := userConfigDir(func() (string, error) { return "", want })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestUserConfigDirRejectsRelativeHome(t *testing.T) {
	_, err := userConfigDir(func() (string, error) { return "relative-home", nil })
	if err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("error = %v, want relative-home rejection", err)
	}
}

func TestLoadReportsRemovedPermissionsRegardlessOfUnknownFieldOrder(t *testing.T) {
	doc := validConfiguration + `
[unrelated]
value = true

[permissions]
allow = ["NotATool(foo)"]
`
	_, err := Load(writeConfiguration(t, doc))
	if err == nil || !strings.Contains(err.Error(), "[permissions] is no longer supported") {
		t.Fatalf("Load() error = %v, want permissions migration guidance", err)
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

func TestLoadNormalizesProjectDocFallbackFilenames(t *testing.T) {
	doc := validConfiguration + `
[rules]
project_doc_fallback_filenames = [" CLAUDE.md ", "CLAUDE.md", "CONVENTIONS.md"]
`
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"CLAUDE.md", "CONVENTIONS.md"}
	if !reflect.DeepEqual(got.Rules.ProjectDocFallbackFilenames, want) {
		t.Fatalf("fallback filenames = %#v, want %#v", got.Rules.ProjectDocFallbackFilenames, want)
	}
}

func TestRulesValidateProjectDocFallbackFilenames(t *testing.T) {
	cases := []string{
		"",
		" \t",
		"../CLAUDE.md",
		"nested/CLAUDE.md",
		filepath.Join(string(filepath.Separator), "CLAUDE.md"),
		`..\CLAUDE.md`,
		`C:\CLAUDE.md`,
		".",
		"..",
	}
	for _, name := range cases {
		name := name
		t.Run(name, func(t *testing.T) {
			err := (RulesConfig{ProjectDocFallbackFilenames: []string{name}}).Validate()
			if err == nil || !strings.Contains(err.Error(), "rules.project_doc_fallback_filenames") {
				t.Fatalf("error=%v, want fallback filename validation", err)
			}
		})
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
	if got.Sandbox.ModeNormalized() != "" {
		t.Fatalf("sandbox mode = %q", got.Sandbox.ModeNormalized())
	}
	if got.Sandbox.ToolchainVisibilityNormalized() != SandboxToolchainVisibilityAuto {
		t.Fatalf("toolchain visibility = %q, want auto", got.Sandbox.ToolchainVisibilityNormalized())
	}
	if roots, err := got.Sandbox.ResolveReadOnlyRoots(); err != nil || len(roots) != 0 {
		t.Fatalf("read-only roots = %v, %v", roots, err)
	}
	if got.Runtime.EffectiveMaxTurnSeconds() != DefaultRuntimeMaxTurnSeconds ||
		got.Runtime.EffectiveMaxModelSteps() != DefaultRuntimeMaxModelSteps ||
		got.Runtime.EffectiveMaxToolCalls() != DefaultRuntimeMaxToolCalls ||
		got.Runtime.EffectiveMaxConsecutiveEquivalentToolCalls() != DefaultRuntimeMaxConsecutiveEquivalentToolCalls {
		t.Fatalf("runtime defaults = %#v", got.Runtime.Normalize())
	}

	protected := got.Sandbox.EffectiveProtectedPaths()
	if !reflect.DeepEqual(protected, DefaultSandboxProtectedPaths()) {
		t.Fatalf("protected paths = %#v", protected)
	}
}

func TestLoadRuntimeZeroValuesUseDefaults(t *testing.T) {
	doc := validConfiguration + `
[runtime]
max_turn_seconds = 0
max_model_steps = 0
max_tool_calls = 0
max_consecutive_equivalent_tool_calls = 0
`
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := RuntimeConfig{
		MaxTurnSeconds:                    DefaultRuntimeMaxTurnSeconds,
		MaxModelSteps:                     DefaultRuntimeMaxModelSteps,
		MaxToolCalls:                      DefaultRuntimeMaxToolCalls,
		MaxConsecutiveEquivalentToolCalls: DefaultRuntimeMaxConsecutiveEquivalentToolCalls,
	}
	if got := got.Runtime.Normalize(); got != want {
		t.Fatalf("runtime = %#v, want %#v", got, want)
	}
}

func TestRuntimeConfigCapsDefaultEquivalentToolCallLimitAtToolBudget(t *testing.T) {
	config := RuntimeConfig{MaxToolCalls: 2}
	if got := config.EffectiveMaxConsecutiveEquivalentToolCalls(); got != 2 {
		t.Fatalf("effective equivalent-call limit = %d, want 2", got)
	}
	if got := config.Normalize().MaxConsecutiveEquivalentToolCalls; got != 2 {
		t.Fatalf("normalized equivalent-call limit = %d, want 2", got)
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
toolchain_visibility = "explicit"
read_only_roots = [%q, %q]
protected_paths = ["secrets", "secrets"]

[runtime]
max_turn_seconds = 120
max_model_steps = 4
max_tool_calls = 12
max_consecutive_equivalent_tool_calls = 2
`, link, realRoot)
	got, err := Load(writeConfiguration(t, doc))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Sandbox.ModeNormalized() != SandboxModeReadOnly {
		t.Fatalf("sandbox mode = %q", got.Sandbox.ModeNormalized())
	}
	if got.Sandbox.ToolchainVisibilityNormalized() != SandboxToolchainVisibilityExplicit {
		t.Fatalf("toolchain visibility = %q, want explicit", got.Sandbox.ToolchainVisibilityNormalized())
	}
	canonicalRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Sandbox.ReadOnlyRoots, []string{canonicalRoot}) {
		t.Fatalf("read-only roots = %#v, want %#v", got.Sandbox.ReadOnlyRoots, []string{canonicalRoot})
	}
	wantProtected := append(DefaultSandboxProtectedPaths(), "secrets")
	if !reflect.DeepEqual(got.Sandbox.EffectiveProtectedPaths(), wantProtected) {
		t.Fatalf("effective protected paths = %#v, want %#v", got.Sandbox.EffectiveProtectedPaths(), wantProtected)
	}
	if got.Runtime.Normalize() != (RuntimeConfig{
		MaxTurnSeconds:                    120,
		MaxModelSteps:                     4,
		MaxToolCalls:                      12,
		MaxConsecutiveEquivalentToolCalls: 2,
	}) {
		t.Fatalf("runtime = %#v", got.Runtime.Normalize())
	}
}

func TestLoadRejectsRemovedMaxReactSteps(t *testing.T) {
	doc := validConfiguration + `
[runtime]
max_react_steps = 8
`
	_, err := Load(writeConfiguration(t, doc))
	if err == nil || !strings.Contains(err.Error(), "runtime.max_react_steps is no longer supported") || !strings.Contains(err.Error(), "runtime.max_model_steps") {
		t.Fatalf("Load() error = %v, want actionable model-step migration", err)
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
			name: "toolchain visibility",
			doc:  "[sandbox]\ntoolchain_visibility = \"host\"\n",
			want: "sandbox.toolchain_visibility",
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
			name: "runtime model-step bound",
			doc:  "[runtime]\nmax_model_steps = 33\n",
			want: "runtime.max_model_steps",
		},
		{
			name: "runtime tool-call bound",
			doc:  "[runtime]\nmax_tool_calls = 129\n",
			want: "runtime.max_tool_calls",
		},
		{
			name: "negative equivalent tool-call bound",
			doc:  "[runtime]\nmax_consecutive_equivalent_tool_calls = -1\n",
			want: "runtime.max_consecutive_equivalent_tool_calls",
		},
		{
			name: "equivalent tool-call bound exceeds tool budget",
			doc: "[runtime]\nmax_tool_calls = 2\n" +
				"max_consecutive_equivalent_tool_calls = 3\n",
			want: "runtime.max_consecutive_equivalent_tool_calls",
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
