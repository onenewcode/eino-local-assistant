package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	// ErrUnavailable means the current platform does not have a usable strict
	// sandbox backend. Callers must not retry the worker on the host.
	ErrUnavailable = errors.New("sandbox backend unavailable")
	// ErrUnsupportedPlatform means no strict sandbox backend is implemented for
	// the current operating system.
	ErrUnsupportedPlatform = errors.New("sandbox platform is unsupported")
)

const (
	// EnvSandboxProxySocket names the Unix socket used by a Linux worker relay
	// to reach the host-side hostname-filtering proxy.
	EnvSandboxProxySocket = "EINO_SANDBOX_PROXY_SOCKET"
	// EnvSandboxProxyPort names the worker-local loopback port a Linux relay
	// must listen on before a network-enabled tool starts.
	EnvSandboxProxyPort = "EINO_SANDBOX_PROXY_PORT"
)

// Backend identifies the OS sandbox backend used by a command specification.
type Backend string

const (
	// BackendSeatbelt is the macOS sandbox-exec/Seatbelt backend.
	BackendSeatbelt Backend = "seatbelt"
	// BackendBubblewrap is the Linux bubblewrap backend.
	BackendBubblewrap Backend = "bubblewrap"
)

// CommandSpec describes an already-sandboxed executable invocation. Env is a
// complete replacement environment, not a list of additions to the host
// environment. A caller can turn it into an exec.Cmd with Command.
type CommandSpec struct {
	Backend Backend
	Path    string
	Args    []string
	Dir     string
	Env     []string
	// ProxySocket is a host-side Unix socket mounted into a Linux bwrap worker.
	// A worker that receives both relay fields must expose ProxyPort on its own
	// isolated loopback interface and relay it to this socket before running the
	// requested tool. It is empty for macOS and network-disabled commands.
	ProxySocket string
	// ProxyPort is the loopback port the Linux worker relay must listen on. It
	// is zero for macOS and network-disabled commands.
	ProxyPort int
}

// Command creates an exec.Cmd from the specification without starting it.
func (spec CommandSpec) Command(ctx context.Context) *exec.Cmd {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, spec.Path, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = append([]string(nil), spec.Env...)
	return cmd
}

// BuildCommand validates policy and builds the strict backend command for the
// current operating system. A nonzero proxyPort is valid only when the policy
// has an allowed-host list, and vice versa. The proxy must enforce that list.
func BuildCommand(ctx context.Context, policy Policy, workerPath string, workerArgs []string, proxyPort int) (CommandSpec, error) {
	return BuildCommandWithAvailability(ctx, policy, workerPath, workerArgs, proxyPort, CurrentAvailability())
}

// BuildCommandWithAvailability is BuildCommand with an already-resolved
// backend. Runners capture this before a model can change the workspace or
// ambient PATH, so a later tool call cannot replace the sandbox launcher.
// It fully normalizes policy (including the workspace hard-link scan).
func BuildCommandWithAvailability(ctx context.Context, policy Policy, workerPath string, workerArgs []string, proxyPort int, availability Availability) (CommandSpec, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return CommandSpec{}, err
		}
	}
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return CommandSpec{}, err
	}
	return buildCommandFromNormalized(normalized, workerPath, workerArgs, proxyPort, availability)
}

// BuildCommandFromNormalized builds a launcher command from a policy that was
// already produced by NormalizePolicy or Policy.WithTempDir. It skips the
// workspace hard-link walk so per-tool execution stays cheap after session
// startup validation.
func BuildCommandFromNormalized(ctx context.Context, policy Policy, workerPath string, workerArgs []string, proxyPort int, availability Availability) (CommandSpec, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return CommandSpec{}, err
		}
	}
	if err := requireNormalizedPolicy(policy); err != nil {
		return CommandSpec{}, err
	}
	return buildCommandFromNormalized(policy, workerPath, workerArgs, proxyPort, availability)
}

func buildCommandFromNormalized(policy Policy, workerPath string, workerArgs []string, proxyPort int, availability Availability) (CommandSpec, error) {
	worker, err := normalizeWorkerPath(workerPath)
	if err != nil {
		return CommandSpec{}, err
	}
	if err := validateProxyPort(policy, proxyPort); err != nil {
		return CommandSpec{}, err
	}

	if !availability.Available {
		if availability.Backend == "" {
			return CommandSpec{}, fmt.Errorf("%w: %w: %s", ErrUnavailable, ErrUnsupportedPlatform, availability.Reason)
		}
		return CommandSpec{}, fmt.Errorf("%w: %s", ErrUnavailable, availability.Reason)
	}
	launcher, err := normalizeSandboxLauncher(availability.Executable)
	if err != nil {
		return CommandSpec{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return buildCurrentCommand(policy, worker, workerArgs, proxyPort, launcher)
}

// requireNormalizedPolicy is a cheap shape check for session-validated policies.
// It does not re-resolve symlinks or walk the workspace.
func requireNormalizedPolicy(policy Policy) error {
	if policy.Mode != ReadOnly && policy.Mode != WorkspaceWrite {
		return fmt.Errorf("sandbox mode must be %q or %q", ReadOnly, WorkspaceWrite)
	}
	if !filepath.IsAbs(policy.Workspace) || policy.Workspace != filepath.Clean(policy.Workspace) {
		return errors.New("sandbox workspace must be a cleaned absolute path")
	}
	if !filepath.IsAbs(policy.TempDir) || policy.TempDir != filepath.Clean(policy.TempDir) {
		return errors.New("sandbox temp_dir must be a cleaned absolute path")
	}
	return nil
}

// LinuxSandboxRelayPort is the fixed loopback port used inside each bubblewrap
// network namespace for the host proxy relay. The namespace is private, so a
// fixed port avoids host-side listen/close races and concurrent-worker clashes.
// Stay above 1023: even with user-namespace root, privileged ports are a
// footgun for non-bwrap worker entrypaths and for future capability tightening.
const LinuxSandboxRelayPort = 18765

func normalizeSandboxLauncher(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("sandbox backend executable is required")
	}
	if !filepath.IsAbs(raw) {
		return "", errors.New("sandbox backend executable must be absolute")
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox backend executable: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat sandbox backend executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("sandbox backend executable must be a regular executable file")
	}
	multiple, err := hasMultipleHardLinks(info)
	if err != nil {
		return "", fmt.Errorf("inspect sandbox backend executable links: %w", err)
	}
	if multiple {
		return "", errors.New("sandbox backend executable must not have multiple hard links")
	}
	return filepath.Clean(resolved), nil
}

func normalizeWorkerPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("sandbox worker path is required")
	}
	if !filepath.IsAbs(raw) {
		return "", errors.New("sandbox worker path must be absolute")
	}

	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox worker path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat sandbox worker path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("sandbox worker path must be a regular file")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("sandbox worker path must be executable")
	}
	multiple, err := hasMultipleHardLinks(info)
	if err != nil {
		return "", fmt.Errorf("inspect sandbox worker path links: %w", err)
	}
	if multiple {
		return "", errors.New("sandbox worker path must not have multiple hard links")
	}
	return filepath.Clean(resolved), nil
}

// ResolveExecutablePath validates an absolute executable path and returns its
// symlink-resolved form. It rejects multi-linked files so a writable alias
// cannot replace an executable selected before an approval boundary.
func ResolveExecutablePath(raw string) (string, error) {
	return normalizeWorkerPath(raw)
}

func validateProxyPort(policy Policy, proxyPort int) error {
	hasHosts := len(policy.Network.AllowedHosts) > 0
	if proxyPort < 0 || proxyPort > 65535 {
		return errors.New("sandbox proxy port must be between 0 and 65535")
	}
	if hasHosts && proxyPort == 0 {
		return errors.New("sandbox network allowlist requires a loopback proxy port")
	}
	if !hasHosts && proxyPort != 0 {
		return errors.New("sandbox loopback proxy port requires a network allowlist")
	}
	return nil
}

func sandboxEnvironment(policy Policy, proxyPort int, path string) []string {
	env := []string{
		"HOME=" + policy.TempDir,
		"TMPDIR=" + policy.TempDir,
		"TMP=" + policy.TempDir,
		"TEMP=" + policy.TempDir,
		"PATH=" + path,
		"NO_PROXY=",
		"no_proxy=",
	}
	if proxyPort == 0 {
		return append(env,
			"HTTP_PROXY=",
			"HTTPS_PROXY=",
			"ALL_PROXY=",
			"http_proxy=",
			"https_proxy=",
			"all_proxy=",
		)
	}

	proxyURL := "http://127.0.0.1:" + strconv.Itoa(proxyPort)
	return append(env,
		"HTTP_PROXY="+proxyURL,
		"HTTPS_PROXY="+proxyURL,
		"ALL_PROXY="+proxyURL,
		"http_proxy="+proxyURL,
		"https_proxy="+proxyURL,
		"all_proxy="+proxyURL,
	)
}

func bubblewrapEnvironment(policy Policy, proxyPort int) []string {
	env := sandboxEnvironment(policy, proxyPort, "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	if proxyPort == 0 {
		return env
	}
	return append(env,
		EnvSandboxProxySocket+"="+proxySocketPath(policy),
		EnvSandboxProxyPort+"="+strconv.Itoa(proxyPort),
	)
}

func proxySocketPath(policy Policy) string {
	return filepath.Join(policy.TempDir, "proxy.sock")
}
