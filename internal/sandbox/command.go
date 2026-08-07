package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// current operating system. Network access remains available inside the
// filesystem sandbox.
func BuildCommand(ctx context.Context, policy Policy, workerPath string, workerArgs []string) (CommandSpec, error) {
	return BuildCommandWithAvailability(ctx, policy, workerPath, workerArgs, CurrentAvailability())
}

// BuildCommandWithAvailability is BuildCommand with an already-resolved
// backend. Runners capture this before a model can change the workspace or
// ambient PATH, so a later tool call cannot replace the sandbox launcher.
// It fully normalizes policy (including the workspace hard-link scan).
func BuildCommandWithAvailability(ctx context.Context, policy Policy, workerPath string, workerArgs []string, availability Availability) (CommandSpec, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return CommandSpec{}, err
		}
	}
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return CommandSpec{}, err
	}
	return buildCommandFromNormalized(normalized, workerPath, workerArgs, availability)
}

// BuildCommandFromNormalized builds a launcher command from a policy that was
// already produced by NormalizePolicy or Policy.WithTempDir. It skips the
// workspace hard-link walk so per-tool execution stays cheap after session
// startup validation.
func BuildCommandFromNormalized(ctx context.Context, policy Policy, workerPath string, workerArgs []string, availability Availability) (CommandSpec, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return CommandSpec{}, err
		}
	}
	if err := requireNormalizedPolicy(policy); err != nil {
		return CommandSpec{}, err
	}
	return buildCommandFromNormalized(policy, workerPath, workerArgs, availability)
}

func buildCommandFromNormalized(policy Policy, workerPath string, workerArgs []string, availability Availability) (CommandSpec, error) {
	worker, err := normalizeWorkerPath(workerPath)
	if err != nil {
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
	return buildCurrentCommand(policy, worker, workerArgs, launcher)
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

func sandboxEnvironment(policy Policy) []string {
	return policy.Environment.ForExecution(policy.TempDir)
}
