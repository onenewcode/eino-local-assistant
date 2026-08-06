package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/tui"
)

func TestEffectiveSandboxProtectedPathsProtectsHostControlFilesInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.toml")
	dataDir := filepath.Join(workspace, ".eino-assistant")
	if err := os.WriteFile(configPath, []byte("api_key = \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	paths, err := effectiveSandboxProtectedPaths(workspace, []string{".env"}, configPath, dataDir)
	if err != nil {
		t.Fatalf("effectiveSandboxProtectedPaths() error = %v", err)
	}
	if want := []string{".env", "config.toml", ".eino-assistant"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("protected paths = %#v, want %#v", paths, want)
	}
}

func TestApplyModelOverrideChangesOnlyInvocationModel(t *testing.T) {
	cfg := config.Config{
		Model: config.ModelConfig{
			Provider:       config.ProviderOpenAI,
			BaseURL:        "https://api.example.test/v1",
			APIKey:         "test-api-key",
			Name:           "configured-model",
			TimeoutSeconds: 60,
			Context: config.ModelContextConfig{
				WindowTokens:    32000,
				MaxOutputTokens: 4096,
			},
		},
	}
	if err := applyModelOverride(&cfg, "  invocation-model  "); err != nil {
		t.Fatalf("applyModelOverride() error = %v", err)
	}
	if cfg.Model.Name != "invocation-model" {
		t.Fatalf("model name = %q, want invocation-model", cfg.Model.Name)
	}
}

func TestApplyModelOverrideResolvesConfiguredCatalogAlias(t *testing.T) {
	cfg := config.Config{
		Model: config.ModelConfig{
			Provider:       config.ProviderOpenAI,
			BaseURL:        "https://api.example.test/v1",
			APIKey:         "test-api-key",
			Name:           "configured-model",
			TimeoutSeconds: 60,
			Context: config.ModelContextConfig{
				WindowTokens:    32000,
				MaxOutputTokens: 4096,
			},
			Catalog: []config.ModelCatalogEntry{
				{Name: "gpt-5.2-coding", DisplayName: "Coding 5.2", Aliases: []string{"coding"}},
			},
		},
	}
	if err := applyModelOverride(&cfg, "CODING"); err != nil {
		t.Fatalf("applyModelOverride() error = %v", err)
	}
	if cfg.Model.Name != "gpt-5.2-coding" {
		t.Fatalf("model name = %q, want canonical catalog name", cfg.Model.Name)
	}
	if got := cfg.Model.CatalogDisplayName(cfg.Model.Name); got != "Coding 5.2" {
		t.Fatalf("catalog display name = %q, want Coding 5.2", got)
	}
}

func TestApplyModelOverrideRejectsInvalidModel(t *testing.T) {
	cfg := config.Config{Model: config.ModelConfig{Name: "configured-model"}}
	if err := applyModelOverride(&cfg, ""); err != nil {
		t.Fatalf("blank model override error = %v, want config unchanged", err)
	}
	if err := applyModelOverride(&cfg, " "); err != nil {
		t.Fatalf("whitespace model override error = %v, want config unchanged", err)
	}
	if err := applyModelOverride(&cfg, "invocation-model"); err == nil || !strings.Contains(err.Error(), "validate model override") {
		t.Fatalf("invalid model override error = %v, want validation error", err)
	}
}

func TestStatusFromCarriesRequestedReasoningEffort(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "explicit", want: "high"},
		{name: "provider default", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := statusFrom("openai/test", test.want, nil, "ask", 8, tui.SandboxInfo{}, tui.RuntimeInfo{})
			if status.ReasoningEffort != test.want {
				t.Fatalf("reasoning effort = %q, want %q", status.ReasoningEffort, test.want)
			}
		})
	}
}

func TestEffectiveSandboxProtectedPathsRejectsSessionStoreContainingWorkspace(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.toml")
	if err := os.WriteFile(configPath, []byte("api_key = \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := effectiveSandboxProtectedPaths(workspace, nil, configPath, workspace)
	if err == nil || !strings.Contains(err.Error(), "must not contain workspace") {
		t.Fatalf("effectiveSandboxProtectedPaths() error = %v, want storage/workspace overlap rejection", err)
	}
}

func TestEffectiveSandboxProtectedPathsProtectsEphemeralSourceStoreInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.toml")
	sourceDataDir := filepath.Join(workspace, ".eino-assistant")
	if err := os.WriteFile(configPath, []byte("api_key = \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sourceDataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := effectiveSandboxProtectedPaths(workspace, nil, configPath, t.TempDir(), sourceDataDir)
	if err != nil {
		t.Fatalf("effectiveSandboxProtectedPaths() error = %v", err)
	}
	if want := []string{"config.toml", ".eino-assistant"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("protected paths = %#v, want %#v", paths, want)
	}
}

func TestEffectiveSandboxProtectedPathsProtectsResolvedEphemeralSourceThreadAlias(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.toml")
	sourceDataDir := t.TempDir()
	sourceSessions := filepath.Join(sourceDataDir, "sessions")
	workspaceSource := filepath.Join(workspace, "source-sessions")
	sourceID := "selected-thread"
	if err := os.WriteFile(configPath, []byte("api_key = \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspaceSource, sourceID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(workspaceSource, sourceSessions); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sourceThreadPath := filepath.Join(sourceSessions, sourceID)
	paths, err := effectiveSandboxProtectedPathsWithSourceThreadPaths(
		workspace,
		nil,
		configPath,
		t.TempDir(),
		[]string{sourceDataDir},
		[]string{sourceThreadPath},
	)
	if err != nil {
		t.Fatalf("effectiveSandboxProtectedPathsWithSourceThreadPaths() error = %v", err)
	}
	want := []string{"config.toml", filepath.ToSlash(filepath.Join("source-sessions", sourceID))}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("protected paths = %#v, want %#v", paths, want)
	}
}

func TestEffectiveSandboxProtectedPathsRejectsEphemeralSourceStoreSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	configPath := filepath.Join(workspace, "config.toml")
	sourceDataDir := filepath.Join(workspace, "source-link")
	if err := os.WriteFile(configPath, []byte("api_key = \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, sourceDataDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := effectiveSandboxProtectedPaths(workspace, nil, configPath, t.TempDir(), sourceDataDir)
	if err == nil || !strings.Contains(err.Error(), "source session storage") || !strings.Contains(err.Error(), "must not be a symlink inside workspace") {
		t.Fatalf("effectiveSandboxProtectedPaths() error = %v, want source symlink rejection", err)
	}
}

func TestEffectiveSandboxProtectedPathsRejectsSymlinkedConfigInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "real-config.toml")
	configPath := filepath.Join(workspace, "config.toml")
	dataDir := t.TempDir()
	if err := os.WriteFile(target, []byte("api_key = \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, configPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := effectiveSandboxProtectedPaths(workspace, nil, configPath, dataDir)
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("effectiveSandboxProtectedPaths() error = %v, want config symlink rejection", err)
	}
}

func TestEffectiveSandboxProtectedPathsRejectsConfigThroughWorkspaceSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(workspace, "linked-config")
	configPath := filepath.Join(link, "config.toml")
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "config.toml"), []byte("api_key = \"secret\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := effectiveSandboxProtectedPaths(workspace, nil, configPath, dataDir)
	if err == nil || !strings.Contains(err.Error(), "must not traverse workspace symlink") {
		t.Fatalf("effectiveSandboxProtectedPaths() error = %v, want parent symlink rejection", err)
	}
}
