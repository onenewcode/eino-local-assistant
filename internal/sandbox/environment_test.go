package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDiscoverHostEnvironmentFiltersSecretsAndFindsToolchainRoots(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	home := t.TempDir()
	toolDir := filepath.Join(home, "go", "bin")
	targetDir := filepath.Join(home, "go", "toolchain", "bin")
	cacheDir := filepath.Join(home, "go", "pkg", "mod")
	for _, dir := range []string{toolDir, targetDir, cacheDir, filepath.Join(workspace, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(targetDir, "probe"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(targetDir, "probe"), filepath.Join(toolDir, "probe")); err != nil {
		t.Fatal(err)
	}

	snapshot, err := DiscoverHostEnvironment([]string{
		"HOME=" + home,
		"PATH=" + toolDir + string(os.PathListSeparator) + filepath.Join(workspace, "bin") + string(os.PathListSeparator) + "relative",
		"GOPATH=" + filepath.Join(home, "go"),
		"GOMODCACHE=" + cacheDir,
		"LANG=en_US.UTF-8",
		"SSH_AUTH_SOCK=" + filepath.Join(home, "agent.sock"),
		"OPENAI_API_KEY=do-not-copy",
	}, workspace, ToolchainVisibilityAuto)
	if err != nil {
		t.Fatalf("DiscoverHostEnvironment() error = %v", err)
	}

	if snapshot.Mode != ToolchainVisibilityAuto {
		t.Fatalf("Mode = %q, want auto", snapshot.Mode)
	}
	if !containsPath(snapshot.PathEntries, toolDir) {
		t.Fatalf("PathEntries = %#v, missing %q", snapshot.PathEntries, toolDir)
	}
	if containsPath(snapshot.PathEntries, "relative") {
		t.Fatalf("PathEntries retained relative entry: %#v", snapshot.PathEntries)
	}
	if !containsPath(snapshot.ReadOnlyRoots, filepath.Join(home, "go")) {
		t.Fatalf("ReadOnlyRoots = %#v, missing Go root", snapshot.ReadOnlyRoots)
	}
	if !containsPath(snapshot.ReadOnlyRoots, targetDir) || !containsPath(snapshot.ReadOnlyRoots, cacheDir) {
		t.Fatalf("ReadOnlyRoots = %#v, missing symlink target/cache root", snapshot.ReadOnlyRoots)
	}
	if containsPath(snapshot.ReadOnlyRoots, home) || containsPath(snapshot.ReadOnlyRoots, workspace) || containsPath(snapshot.ReadOnlyRoots, string(filepath.Separator)) {
		t.Fatalf("ReadOnlyRoots exposes a broad or workspace root: %#v", snapshot.ReadOnlyRoots)
	}
	if !containsEnvironmentEntry(snapshot.Variables, "GOPATH="+filepath.Join(home, "go")) || !containsEnvironmentEntry(snapshot.Variables, "LANG=en_US.UTF-8") {
		t.Fatalf("Variables = %#v, missing safe variables", snapshot.Variables)
	}
	if containsEnvironmentPrefixEntry(snapshot.Variables, "SSH_AUTH_SOCK=") || containsEnvironmentPrefixEntry(snapshot.Variables, "OPENAI_API_KEY=") {
		t.Fatalf("Variables leaked a sensitive value: %#v", snapshot.Variables)
	}

	executionEnv := snapshot.ForExecution(filepath.Join(t.TempDir(), "execution"))
	if !containsEnvironmentPrefixEntry(executionEnv, "HOME=") || !containsEnvironmentPrefixEntry(executionEnv, "GOCACHE=") {
		t.Fatalf("execution environment = %#v, missing temporary overrides", executionEnv)
	}
	if strings.Contains(strings.Join(executionEnv, "\n"), "do-not-copy") {
		t.Fatalf("execution environment leaked secret: %#v", executionEnv)
	}
}

func TestDiscoverHostEnvironmentPreservesNetworkProxySettings(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	snapshot, err := DiscoverHostEnvironment([]string{
		"HOME=" + t.TempDir(),
		"HTTP_PROXY=http://proxy.example.test:8080",
		"HTTPS_PROXY=http://proxy.example.test:8080",
		"NO_PROXY=localhost,127.0.0.1",
	}, workspace, ToolchainVisibilityAuto)
	if err != nil {
		t.Fatalf("DiscoverHostEnvironment() error = %v", err)
	}
	executionEnv := snapshot.ForExecution(t.TempDir())
	for _, want := range []string{
		"HTTP_PROXY=http://proxy.example.test:8080",
		"HTTPS_PROXY=http://proxy.example.test:8080",
		"NO_PROXY=localhost,127.0.0.1",
	} {
		if !containsEnvironmentEntry(executionEnv, want) {
			t.Fatalf("execution environment = %#v, missing %q", executionEnv, want)
		}
	}
}

func TestDiscoverHostEnvironmentExplicitModeDoesNotInheritHostPaths(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	home := t.TempDir()
	toolDir := filepath.Join(home, "custom", "bin")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatal(err)
	}

	snapshot, err := DiscoverHostEnvironment([]string{
		"HOME=" + home,
		"PATH=" + toolDir,
		"GOPATH=" + filepath.Join(home, "go"),
		"LANG=C",
	}, workspace, ToolchainVisibilityExplicit)
	if err != nil {
		t.Fatalf("DiscoverHostEnvironment() error = %v", err)
	}
	if snapshot.Mode != ToolchainVisibilityExplicit {
		t.Fatalf("Mode = %q, want explicit", snapshot.Mode)
	}
	if containsPath(snapshot.PathEntries, toolDir) || containsEnvironmentPrefixEntry(snapshot.Variables, "GOPATH=") {
		t.Fatalf("explicit snapshot inherited host state: %#v", snapshot)
	}
	if runtime.GOOS == "darwin" {
		for _, entry := range snapshot.ForExecution(t.TempDir()) {
			if strings.HasPrefix(entry, "PATH=") {
				if strings.Contains(entry, toolDir) {
					t.Fatalf("explicit Darwin PATH inherited host path: %q", entry)
				}
				break
			}
		}
	}
}

func TestDiscoverHostEnvironmentDoesNotExposeHostHomeFromPath(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	home := t.TempDir()
	pathDir := filepath.Join(home, "bin")
	targetDir := filepath.Join(home, "target", "bin")
	for _, dir := range []string{pathDir, targetDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(targetDir, "tool")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(pathDir, "tool")); err != nil {
		t.Fatal(err)
	}

	snapshot, err := DiscoverHostEnvironment([]string{
		"HOME=" + home,
		"PATH=" + home + string(os.PathListSeparator) + pathDir,
	}, workspace, ToolchainVisibilityAuto)
	if err != nil {
		t.Fatalf("DiscoverHostEnvironment() error = %v", err)
	}
	if containsPath(snapshot.ReadOnlyRoots, home) {
		t.Fatalf("ReadOnlyRoots exposed host HOME: %#v", snapshot.ReadOnlyRoots)
	}
}

func containsPath(values []string, wanted string) bool {
	if resolved, err := filepath.EvalSymlinks(wanted); err == nil {
		wanted = resolved
	}
	for _, value := range values {
		if filepath.Clean(value) == filepath.Clean(wanted) {
			return true
		}
	}
	return false
}

func containsEnvironmentEntry(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsEnvironmentPrefixEntry(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
