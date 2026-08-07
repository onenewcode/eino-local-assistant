package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeatbeltProfileConstrictsFilesystemAndKeepsNetworkOpen(t *testing.T) {
	t.Parallel()
	policy, worker := testPolicyAndWorker(t, WorkspaceWrite)
	profile, err := SeatbeltProfile(policy, worker)
	if err != nil {
		t.Fatalf("SeatbeltProfile() error = %v", err)
	}

	for _, want := range []string{
		"(version 1)",
		"(deny default)",
		"(allow process-exec)",
		"(allow process-info* (target same-sandbox))",
		"(allow mach-priv-task-port (target same-sandbox))",
		"(allow ipc-posix-shm)",
		"(allow system-socket (require-all (socket-domain AF_SYSTEM) (socket-protocol 2)))",
		"(global-name \"com.apple.bsd.dirhelper\")",
		"(allow file-read* (literal \"/\"))",
		"(allow file-read* (literal \"/var\"))",
		"(allow file-ioctl file-read-data file-write-data",
		"(literal \"/dev/null\")",
		"(allow file-read-metadata (vnode-type DIRECTORY))",
		"(allow file-read* (literal \"" + policy.Workspace + "\"))",
		"(allow file-write* (subpath \"" + policy.Workspace + "\"))",
		"(allow file-write* (subpath \"" + policy.TempDir + "\"))",
		"(deny file-read* (literal \"" + filepath.Join(policy.Workspace, ".git") + "\"))",
		"(deny file-read* (subpath \"" + filepath.Join(policy.Workspace, ".git") + "\"))",
		"(deny file-write* (subpath \"" + filepath.Join(policy.Workspace, ".env") + "\"))",
		"(allow network*)",
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile does not contain %q:\n%s", want, profile)
		}
	}
	if strings.Contains(profile, "(subpath \"/\"))") {
		t.Errorf("profile grants a broad host root:\n%s", profile)
	}
	if strings.Contains(profile, "(allow process*)") {
		t.Errorf("profile must not use a broad process permission:\n%s", profile)
	}
	if strings.Contains(profile, "(deny network*)") {
		t.Errorf("profile unexpectedly denies network access:\n%s", profile)
	}
}

func TestMacOSPathVariantsKeepDarwinAliasesNarrow(t *testing.T) {
	t.Parallel()
	paths := macOSPathVariants([]string{
		"/private/var/folders/example/workspace",
		"/private/tmp/example",
		"/private/etc/resolv.conf",
	})
	for _, want := range []string{
		"/var/folders/example/workspace",
		"/tmp/example",
		"/etc/resolv.conf",
	} {
		if !containsString(paths, want) {
			t.Errorf("macOSPathVariants() = %#v, missing %q", paths, want)
		}
	}
	for _, forbidden := range []string{"/var", "/tmp", "/etc"} {
		if containsString(paths, forbidden) {
			t.Errorf("macOSPathVariants() broadened an alias root: %#v", paths)
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestSeatbeltProfileReadOnlyDoesNotGrantWorkspaceWrites(t *testing.T) {
	t.Parallel()
	policy, worker := testPolicyAndWorker(t, ReadOnly)
	profile, err := SeatbeltProfile(policy, worker)
	if err != nil {
		t.Fatalf("SeatbeltProfile() error = %v", err)
	}
	workspaceWrite := "(allow file-write* (subpath \"" + policy.Workspace + "\"))"
	if strings.Contains(profile, workspaceWrite) {
		t.Errorf("read-only profile unexpectedly grants workspace writes:\n%s", profile)
	}
	tempWrite := "(allow file-write* (subpath \"" + policy.TempDir + "\"))"
	if !strings.Contains(profile, tempWrite) {
		t.Errorf("read-only profile does not grant private temporary writes:\n%s", profile)
	}
	if !strings.Contains(profile, "(allow network*)") {
		t.Errorf("read-only profile does not keep network access open:\n%s", profile)
	}
}

func TestSeatbeltCommandUsesReplacementEnvironment(t *testing.T) {
	t.Parallel()
	policy, worker := testPolicyAndWorker(t, WorkspaceWrite)
	spec, err := BuildSeatbeltCommand(policy, worker, []string{"--worker"})
	if err != nil {
		t.Fatalf("BuildSeatbeltCommand() error = %v", err)
	}
	if spec.Backend != BackendSeatbelt || spec.Path != "sandbox-exec" || spec.Dir != policy.Workspace {
		t.Errorf("spec = %#v, want Seatbelt sandbox-exec at workspace", spec)
	}
	if len(spec.Args) < 4 || spec.Args[0] != "-p" || spec.Args[2] != worker || spec.Args[3] != "--worker" {
		t.Errorf("spec.Args = %#v, want sandbox-exec profile and worker argv", spec.Args)
	}
	if containsEnvironmentPrefix(spec.Env, "SSH_AUTH_SOCK=") {
		t.Errorf("Env must not inherit SSH_AUTH_SOCK: %#v", spec.Env)
	}
	if containsEnvironmentPrefix(spec.Env, "EINO_SANDBOX_PROXY_") {
		t.Errorf("Seatbelt command contains removed relay variables: %#v", spec)
	}
}

func testPolicyAndWorker(t *testing.T, mode Mode) (Policy, string) {
	t.Helper()
	workspace := t.TempDir()
	tempDir := t.TempDir()
	readOnlyRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, ".git"), 0o755); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("create .env: %v", err)
	}
	worker := filepath.Join(t.TempDir(), "worker")
	if err := os.WriteFile(worker, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	return Policy{
		Mode:           mode,
		Workspace:      canonicalTestPath(t, workspace),
		TempDir:        canonicalTestPath(t, tempDir),
		ReadOnlyRoots:  []string{canonicalTestPath(t, readOnlyRoot)},
		ProtectedPaths: []string{".git/**", ".env"},
	}, canonicalTestPath(t, worker)
}

func containsEnvironmentPrefix(entries []string, prefix string) bool {
	for _, entry := range entries {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}
