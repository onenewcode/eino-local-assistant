package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBubblewrapArgsUseNamespacesAndNarrowMounts(t *testing.T) {
	t.Parallel()
	policy, worker := testPolicyAndWorker(t, WorkspaceWrite)
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		t.Fatalf("NormalizePolicy() error = %v", err)
	}
	args, err := bubblewrapArgs(normalized, worker, []string{"--worker"}, 3128, []string{"/runtime"})
	if err != nil {
		t.Fatalf("bubblewrapArgs() error = %v", err)
	}

	for _, sequence := range [][]string{
		{"--die-with-parent"},
		{"--new-session"},
		{"--unshare-user"},
		{"--unshare-pid"},
		{"--unshare-net"},
		{"--cap-drop", "ALL"},
		{"--tmpfs", "/"},
		{"--bind", normalized.Workspace, normalized.Workspace},
		{"--bind", normalized.TempDir, normalized.TempDir},
		{"--ro-bind", "/runtime", "/runtime"},
		{"--tmpfs", filepath.Join(normalized.Workspace, ".git")},
		{"--ro-bind", "/dev/null", filepath.Join(normalized.Workspace, ".env")},
		{"--clearenv"},
		{"--setenv", "HTTP_PROXY", "http://127.0.0.1:3128"},
		{"--setenv", EnvSandboxProxySocket, filepath.Join(normalized.TempDir, "proxy.sock")},
		{"--setenv", EnvSandboxProxyPort, "3128"},
		{"--chdir", normalized.Workspace},
		{"--", worker, "--worker"},
	} {
		if !containsSequence(args, sequence) {
			t.Errorf("argv does not contain %#v:\n%#v", sequence, args)
		}
	}
	if containsSequence(args, []string{"--ro-bind", "/", "/"}) || containsSequence(args, []string{"--bind", "/", "/"}) {
		t.Errorf("argv exposes the host root: %#v", args)
	}
}

func TestExistingLinuxRuntimeMountsExcludeBroadUsr(t *testing.T) {
	t.Parallel()
	for _, root := range existingLinuxRuntimeMounts() {
		if root == "/usr" {
			t.Fatalf("runtime mounts must not expose broad /usr: %#v", existingLinuxRuntimeMounts())
		}
	}
}

func TestBubblewrapArgsReadOnlyAndAbsentProtectedPath(t *testing.T) {
	t.Parallel()
	policy, worker := testPolicyAndWorker(t, ReadOnly)
	policy.ProtectedPaths = append(policy.ProtectedPaths, ".eino-assistant")
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		t.Fatalf("NormalizePolicy() error = %v", err)
	}
	args, err := bubblewrapArgs(normalized, worker, nil, 0, nil)
	if err != nil {
		t.Fatalf("bubblewrapArgs() error = %v", err)
	}
	if containsSequence(args, []string{"--bind", normalized.Workspace, normalized.Workspace}) {
		t.Errorf("read-only argv has writable workspace bind: %#v", args)
	}
	if !containsSequence(args, []string{"--ro-bind", normalized.Workspace, normalized.Workspace}) {
		t.Errorf("read-only argv is missing read-only workspace bind: %#v", args)
	}
	if !containsSequence(args, []string{"--tmpfs", filepath.Join(normalized.Workspace, ".eino-assistant")}) {
		t.Errorf("absent protected path is not reserved with tmpfs: %#v", args)
	}
	if containsSequence(args, []string{"--setenv", "HTTP_PROXY", "http://127.0.0.1:"}) {
		t.Errorf("network-disabled argv unexpectedly has an HTTP proxy: %#v", args)
	}
	if containsSequence(args, []string{"--setenv", EnvSandboxProxySocket}) || containsSequence(args, []string{"--setenv", EnvSandboxProxyPort}) {
		t.Errorf("network-disabled argv unexpectedly has relay configuration: %#v", args)
	}
}

func TestBubblewrapArgsRejectsHostRootReadOnlyMount(t *testing.T) {
	t.Parallel()
	policy, worker := testPolicyAndWorker(t, WorkspaceWrite)
	policy.ReadOnlyRoots = []string{string(filepath.Separator)}

	_, err := BubblewrapArgs(policy, worker, nil, 0)
	if err == nil || !strings.Contains(err.Error(), "overlaps workspace") {
		t.Fatalf("BubblewrapArgs() error = %v, want host root overlap rejection", err)
	}
}

func TestBuildBubblewrapCommandExposesRelayContract(t *testing.T) {
	t.Parallel()
	policy, worker := testPolicyAndWorker(t, WorkspaceWrite)
	spec, err := BuildBubblewrapCommand(policy, worker, nil, 3128)
	if err != nil {
		t.Fatalf("BuildBubblewrapCommand() error = %v", err)
	}
	wantSocket := filepath.Join(canonicalTestPath(t, policy.TempDir), "proxy.sock")
	if spec.ProxySocket != wantSocket || spec.ProxyPort != 3128 {
		t.Errorf("relay = socket %q port %d, want %q and 3128", spec.ProxySocket, spec.ProxyPort, wantSocket)
	}
	if !containsEnvironment(spec.Env, EnvSandboxProxySocket+"="+wantSocket) || !containsEnvironment(spec.Env, EnvSandboxProxyPort+"=3128") {
		t.Errorf("Env = %#v, want Linux relay variables", spec.Env)
	}
}

func TestAvailabilityForMissingExecutableFailsClosed(t *testing.T) {
	t.Parallel()
	availability := availabilityFor(BackendBubblewrap, "bwrap", func(string) (string, error) {
		return "", errMissingSandboxBinary
	})
	if availability.Available || availability.Executable != "" || !strings.Contains(availability.Reason, "not available") {
		t.Errorf("availability = %#v, want unavailable backend", availability)
	}
}

func TestBuildCommandRejectsHardLinkedBackendLauncher(t *testing.T) {
	policy, worker := testPolicyAndWorker(t, WorkspaceWrite)
	launcher := filepath.Join(t.TempDir(), "launcher")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(launcher, filepath.Join(t.TempDir(), "launcher-alias")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	availability := CurrentAvailability()
	if availability.Backend == "" {
		t.Skip("no platform sandbox backend")
	}
	availability.Available = true
	availability.Executable = launcher
	_, err := BuildCommandWithAvailability(context.Background(), policy, worker, nil, 3128, availability)
	if err == nil || !strings.Contains(err.Error(), "multiple hard links") {
		t.Fatalf("BuildCommandWithAvailability() error = %v, want hard-linked launcher rejection", err)
	}
}

func containsSequence(values, sequence []string) bool {
	if len(sequence) == 0 {
		return true
	}
	for start := 0; start+len(sequence) <= len(values); start++ {
		matched := true
		for i := range sequence {
			if values[start+i] != sequence[i] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

var errMissingSandboxBinary = missingSandboxBinaryError("missing")

type missingSandboxBinaryError string

func (err missingSandboxBinaryError) Error() string {
	return string(err)
}
