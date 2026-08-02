package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestWithTempDirRebindsWithoutFullRenormalize(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempA := t.TempDir()
	tempB := t.TempDir()
	normalized, err := NormalizePolicy(Policy{
		Mode:      WorkspaceWrite,
		Workspace: workspace,
		TempDir:   tempA,
	})
	if err != nil {
		t.Fatalf("NormalizePolicy() error = %v", err)
	}
	base := normalized.WithoutTempDir()
	if base.TempDir != "" {
		t.Fatalf("WithoutTempDir() TempDir = %q, want empty", base.TempDir)
	}
	rebound, err := base.WithTempDir(tempB)
	if err != nil {
		t.Fatalf("WithTempDir() error = %v", err)
	}
	if rebound.Workspace != normalized.Workspace {
		t.Fatalf("Workspace = %q, want %q", rebound.Workspace, normalized.Workspace)
	}
	if rebound.TempDir != canonicalTestPath(t, tempB) {
		t.Fatalf("TempDir = %q, want %q", rebound.TempDir, canonicalTestPath(t, tempB))
	}
	if _, err := base.WithTempDir(workspace); err == nil {
		t.Fatal("WithTempDir(workspace) error = nil, want overlap error")
	}
}

func TestNormalizePolicyCanonicalizesStrictInputs(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempDir := t.TempDir()
	readOnlyRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatalf("create protected directory: %v", err)
	}

	normalized, err := NormalizePolicy(Policy{
		Workspace:      workspace,
		TempDir:        tempDir,
		ReadOnlyRoots:  []string{readOnlyRoot, readOnlyRoot},
		ProtectedPaths: []string{".git/**", ".env", ".git/**"},
		Network: NetworkPolicy{AllowedHosts: []string{
			"API.Example.COM.",
			"api.example.com",
		}},
	})
	if err != nil {
		t.Fatalf("NormalizePolicy() error = %v", err)
	}
	if normalized.Mode != WorkspaceWrite {
		t.Errorf("Mode = %q, want %q", normalized.Mode, WorkspaceWrite)
	}
	canonicalWorkspace := canonicalTestPath(t, workspace)
	canonicalTempDir := canonicalTestPath(t, tempDir)
	canonicalReadOnlyRoot := canonicalTestPath(t, readOnlyRoot)
	if normalized.Workspace != canonicalWorkspace || normalized.TempDir != canonicalTempDir {
		t.Errorf("roots = workspace %q temp %q, want %q and %q", normalized.Workspace, normalized.TempDir, canonicalWorkspace, canonicalTempDir)
	}
	if got, want := normalized.ReadOnlyRoots, []string{canonicalReadOnlyRoot}; !reflect.DeepEqual(got, want) {
		t.Errorf("ReadOnlyRoots = %#v, want %#v", got, want)
	}
	if got, want := normalized.ProtectedPaths, []string{".env", ".git/**"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ProtectedPaths = %#v, want %#v", got, want)
	}
	if got, want := normalized.Network.AllowedHosts, []string{"api.example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AllowedHosts = %#v, want %#v", got, want)
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return filepath.Clean(resolved)
}

func TestNormalizePolicyRejectsAmbiguousOrUnsafeBoundaries(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempDir := t.TempDir()
	readOnlyRoot := t.TempDir()

	tests := []struct {
		name   string
		policy Policy
		want   string
	}{
		{
			name:   "overlapping temporary directory",
			policy: Policy{Workspace: workspace, TempDir: filepath.Join(workspace, "tmp")},
			want:   "temp_dir",
		},
		{
			name:   "filesystem root workspace overlaps temporary directory",
			policy: Policy{Workspace: string(filepath.Separator), TempDir: tempDir},
			want:   "workspace and temp_dir must not overlap",
		},
		{
			name: "read only root overlaps workspace",
			policy: Policy{
				Workspace: workspace, TempDir: tempDir, ReadOnlyRoots: []string{workspace},
			},
			want: "overlaps",
		},
		{
			name: "host root read only root overlaps workspace",
			policy: Policy{
				Workspace: workspace, TempDir: tempDir, ReadOnlyRoots: []string{string(filepath.Separator)},
			},
			want: "overlaps",
		},
		{
			name: "parent protected path",
			policy: Policy{
				Workspace: workspace, TempDir: tempDir, ProtectedPaths: []string{"../secret"},
			},
			want: "parent",
		},
		{
			name: "protected path glob",
			policy: Policy{
				Workspace: workspace, TempDir: tempDir, ProtectedPaths: []string{".env*"},
			},
			want: "glob",
		},
		{
			name: "protected path absolute",
			policy: Policy{
				Workspace: workspace, TempDir: tempDir, ProtectedPaths: []string{filepath.Join(workspace, ".env")},
			},
			want: "workspace-relative",
		},
		{
			name: "IP allowlist entry",
			policy: Policy{
				Workspace: workspace, TempDir: tempDir, Network: NetworkPolicy{AllowedHosts: []string{"127.0.0.1"}},
			},
			want: "IP literals",
		},
		{
			name: "wildcard allowlist entry",
			policy: Policy{
				Workspace: workspace, TempDir: tempDir, Network: NetworkPolicy{AllowedHosts: []string{"*.example.com"}},
			},
			want: "exact DNS",
		},
		{
			name: "invalid mode",
			policy: Policy{
				Mode: "danger-full-access", Workspace: workspace, TempDir: tempDir, ReadOnlyRoots: []string{readOnlyRoot},
			},
			want: "mode",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizePolicy(test.policy)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NormalizePolicy() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestProtectedMatcherUsesAnchoredLiteralSubtrees(t *testing.T) {
	t.Parallel()
	matcher, err := NewProtectedMatcher([]string{".git/**", ".env", "config/private/**"})
	if err != nil {
		t.Fatalf("NewProtectedMatcher() error = %v", err)
	}

	protected := []string{".git", ".git/config", ".env", ".env/local", "config/private", "config/private/key"}
	for _, candidate := range protected {
		if !matcher.Matches(candidate) {
			t.Errorf("Matches(%q) = false, want true", candidate)
		}
	}
	for _, candidate := range []string{".env.local", "other/.env", "config/public/key", "../.git", "/tmp/.env"} {
		if matcher.Matches(candidate) {
			t.Errorf("Matches(%q) = true, want false", candidate)
		}
	}
	if got, want := matcher.Patterns(), []string{".env", ".git/**", "config/private/**"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Patterns() = %#v, want %#v", got, want)
	}
}

func TestProtectedPatternRejectsMissingIntermediateParentAndSymlink(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempDir := t.TempDir()
	if _, err := NormalizePolicy(Policy{
		Workspace: workspace, TempDir: tempDir, ProtectedPaths: []string{"missing/child"},
	}); err == nil || !strings.Contains(err.Error(), "parent does not exist") {
		t.Fatalf("missing parent error = %v, want parent does not exist", err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := NormalizePolicy(Policy{
		Workspace: workspace, TempDir: tempDir, ProtectedPaths: []string{"link/secret"},
	}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink error = %v, want symlink", err)
	}
}

func TestProtectedRegularFileRejectsHardLinkBypass(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempDir := t.TempDir()
	protected := filepath.Join(workspace, ".env")
	if err := os.WriteFile(protected, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(protected, filepath.Join(workspace, "public-copy")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	_, err := NormalizePolicy(Policy{
		Workspace: workspace,
		TempDir:   tempDir,
		ProtectedPaths: []string{
			".env",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "multiple hard links") {
		t.Fatalf("NormalizePolicy() error = %v, want hard-link rejection", err)
	}
}

func TestWorkspaceWriteRejectsExternalHardLinkBypass(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "host-control-file")
	if err := os.WriteFile(outside, []byte("must-not-change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(workspace, "workspace-alias")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	_, err := NormalizePolicy(Policy{Mode: WorkspaceWrite, Workspace: workspace, TempDir: tempDir})
	if err == nil || !strings.Contains(err.Error(), "workspace regular file has multiple hard links") {
		t.Fatalf("NormalizePolicy() error = %v, want workspace hard-link rejection", err)
	}
}

func TestReadOnlyWorkspaceRejectsExternalHardLinkBypass(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(outside, []byte("read-only fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(workspace, "workspace-alias")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	_, err := NormalizePolicy(Policy{Mode: ReadOnly, Workspace: workspace, TempDir: tempDir})
	if err == nil || !strings.Contains(err.Error(), "workspace regular file has multiple hard links") {
		t.Fatalf("NormalizePolicy() error = %v, want read-only hard-link rejection", err)
	}
}

func TestProtectedDirectoryRejectsHardLinkBypass(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tempDir := t.TempDir()
	protectedDir := filepath.Join(workspace, ".git")
	if err := os.Mkdir(protectedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(protectedDir, "config")
	if err := os.WriteFile(protected, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(protected, filepath.Join(workspace, "public-copy")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	_, err := NormalizePolicy(Policy{
		Workspace: workspace,
		TempDir:   tempDir,
		ProtectedPaths: []string{
			".git",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "multiple hard links") {
		t.Fatalf("NormalizePolicy() error = %v, want protected-directory hard-link rejection", err)
	}
}
