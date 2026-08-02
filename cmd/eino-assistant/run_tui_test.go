package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
