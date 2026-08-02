package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"eino-local-assistant/internal/sandbox"
)

// SandboxRunnerOptions describes the stable policy shared by all one-shot
// tool workers in a session. TempDir is deliberately allocated per execution.
type SandboxRunnerOptions struct {
	Mode           sandbox.Mode
	WorkspaceRoot  string
	ReadOnlyRoots  []string
	ProtectedPaths []string
	AllowedHosts   []string
	// WorkerPath overrides the current executable for integration tests. Empty
	// uses os.Executable, which is the production private worker entrypoint.
	WorkerPath string
	// startUnixProxy is an internal test seam for the Linux relay transport.
	startUnixProxy unixProxyStarter
	// currentAvailability is an internal seam for launcher-pinning tests.
	currentAvailability func() sandbox.Availability
}

// SandboxOutcome is persisted in tool results so the model and session ledger
// can distinguish an enforced restriction from a host escalation.
type SandboxOutcome struct {
	Mode      string `json:"mode,omitempty"`
	Backend   string `json:"backend,omitempty"`
	Network   string `json:"network,omitempty"`
	Enforced  bool   `json:"enforced,omitempty"`
	Escalated bool   `json:"escalated,omitempty"`
}

type sandboxProxy interface {
	Close() error
}

type unixProxyStarter func(allowedHosts []string, socketPath string) (sandboxProxy, error)

// SandboxRunner launches the private worker through the current platform's
// strict backend. It never falls back to the host on a backend failure.
type SandboxRunner struct {
	mu         sync.Mutex
	executions sync.WaitGroup
	closed     bool
	// basePolicy is session-validated (NormalizePolicy at construction) with
	// TempDir cleared. Each Execute rebinds a private temp dir via WithTempDir
	// without repeating the workspace hard-link scan.
	basePolicy     sandbox.Policy
	workerPath     string
	availability   sandbox.Availability
	startUnixProxy unixProxyStarter
	workerCleanup  func() error
}

// sandboxWorkerShutdownGrace gives the worker's signal handler enough time to
// terminate its separately grouped shell before the outer launcher receives a
// forced KILL. This remains bounded for ordinary process-group descendants on
// macOS; a deliberately detached descendant needs VM/container containment.
const sandboxWorkerShutdownGrace = 2*commandWaitGrace + time.Second

// NewSandboxRunner creates a strict runner. The platform backend is checked
// at execution time so the interactive app can still start and report a
// structured fail-closed tool result when bwrap/sandbox-exec is unavailable.
func NewSandboxRunner(opts SandboxRunnerOptions) (*SandboxRunner, error) {
	workspace, err := ResolveWorkspaceRoot(opts.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	worker := strings.TrimSpace(opts.WorkerPath)
	if worker == "" {
		var err error
		worker, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve sandbox worker executable: %w", err)
		}
	}
	tempDir, err := os.MkdirTemp("", "eino-assistant-sandbox-validate-*")
	if err != nil {
		return nil, fmt.Errorf("create sandbox validation temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)
	policy, err := sandbox.NormalizePolicy(sandbox.Policy{
		Mode:           opts.Mode,
		Workspace:      workspace,
		TempDir:        tempDir,
		ReadOnlyRoots:  opts.ReadOnlyRoots,
		ProtectedPaths: opts.ProtectedPaths,
		Network:        sandbox.NetworkPolicy{AllowedHosts: opts.AllowedHosts},
	})
	if err != nil {
		return nil, fmt.Errorf("validate sandbox policy: %w", err)
	}
	availabilityFn := opts.currentAvailability
	if availabilityFn == nil {
		availabilityFn = sandbox.CurrentAvailability
	}
	availability := availabilityFn()
	if availability.Available && PathWithinWorkspace(policy.Workspace, availability.Executable) {
		return nil, errors.New("sandbox backend executable must be outside the workspace")
	}
	stagedWorker, workerCleanup, err := stageSandboxWorker(worker, policy.Workspace)
	if err != nil {
		return nil, err
	}
	startUnixProxy := opts.startUnixProxy
	if startUnixProxy == nil {
		startUnixProxy = defaultUnixProxyStarter
	}
	base := policy.WithoutTempDir()
	base.ReadOnlyRoots = append([]string(nil), policy.ReadOnlyRoots...)
	base.ProtectedPaths = append([]string(nil), policy.ProtectedPaths...)
	base.Network.AllowedHosts = append([]string(nil), policy.Network.AllowedHosts...)
	return &SandboxRunner{
		basePolicy:     base,
		workerPath:     stagedWorker,
		availability:   availability,
		startUnixProxy: startUnixProxy,
		workerCleanup:  workerCleanup,
	}, nil
}

// Close waits for active workers and removes the host-private worker copy.
// Call it only when no more tool execution should be accepted.
func (r *SandboxRunner) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	r.executions.Wait()
	r.mu.Lock()
	cleanup := r.workerCleanup
	r.workerCleanup = nil
	r.mu.Unlock()
	if cleanup == nil {
		return nil
	}
	return cleanup()
}

func (r *SandboxRunner) beginExecution() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return "", errors.New("sandbox runner is closed")
	}
	r.executions.Add(1)
	return r.workerPath, nil
}

func defaultUnixProxyStarter(allowedHosts []string, socketPath string) (sandboxProxy, error) {
	return sandbox.StartUnixHTTPProxy(allowedHosts, socketPath)
}

// Execute runs one private worker request in the configured OS sandbox.
func (r *SandboxRunner) Execute(ctx context.Context, request SandboxWorkerRequest) (SandboxWorkerResponse, SandboxOutcome, error) {
	if r == nil {
		return SandboxWorkerResponse{}, SandboxOutcome{}, errors.New("sandbox runner is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workerPath, err := r.beginExecution()
	if err != nil {
		return SandboxWorkerResponse{}, SandboxOutcome{}, err
	}
	defer r.executions.Done()
	tempDir, err := os.MkdirTemp("", "eino-assistant-sandbox-*")
	if err != nil {
		return SandboxWorkerResponse{}, SandboxOutcome{}, fmt.Errorf("create sandbox temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Rebind only the per-execution temp root. Workspace hard-link and protected
	// path validation already ran at NewSandboxRunner.
	policy, err := r.basePolicy.WithTempDir(tempDir)
	if err != nil {
		return SandboxWorkerResponse{}, SandboxOutcome{}, err
	}

	var proxy sandboxProxy
	proxyPort := 0
	if len(policy.Network.AllowedHosts) > 0 {
		if runtime.GOOS == "linux" {
			// The relay listens inside bwrap's private netns, so a fixed port is
			// free of host listen/close races and concurrent worker collisions.
			proxyPort = sandbox.LinuxSandboxRelayPort
			proxy, err = r.startUnixProxy(policy.Network.AllowedHosts, filepath.Join(tempDir, "proxy.sock"))
		} else {
			var hostProxy *sandbox.HTTPProxy
			hostProxy, err = sandbox.StartHTTPProxy(policy.Network.AllowedHosts)
			if err == nil {
				proxy = hostProxy
				proxyPort = hostProxy.Port()
			}
		}
		if err != nil {
			return SandboxWorkerResponse{}, SandboxOutcome{}, err
		}
		defer proxy.Close()
	}

	request.WorkspaceRoot = policy.Workspace
	payload, err := json.Marshal(request)
	if err != nil {
		return SandboxWorkerResponse{}, SandboxOutcome{}, fmt.Errorf("encode sandbox worker request: %w", err)
	}
	spec, err := sandbox.BuildCommandFromNormalized(ctx, policy, workerPath, []string{"__sandbox_worker"}, proxyPort, r.availability)
	if err != nil {
		return SandboxWorkerResponse{}, SandboxOutcome{}, err
	}
	// Do not use CommandContext here: the lifecycle helper sends TERM to the
	// worker's original process group before escalating to KILL for ordinary
	// descendants.
	command := exec.Command(spec.Path, spec.Args...)
	command.Dir = spec.Dir
	command.Env = append([]string(nil), spec.Env...)
	command.Stdin = bytes.NewReader(payload)
	stdout := newLimitedBuffer(3 << 20)
	stderr := newLimitedBuffer(64 << 10)
	command.Stdout = stdout
	command.Stderr = stderr
	configureCommandProcessGroup(command)
	outcome := SandboxOutcome{
		Mode:     string(policy.Mode),
		Backend:  string(spec.Backend),
		Network:  sandboxNetworkLabel(policy),
		Enforced: true,
	}
	run, err := runCommandWithLifecycleWithGrace(ctx, command, stdout, stderr, sandboxWorkerShutdownGrace, commandWaitGrace)
	if err != nil {
		return SandboxWorkerResponse{}, outcome, fmt.Errorf("start sandbox worker: %w", err)
	}
	if run.outputLimited {
		return SandboxWorkerResponse{}, outcome, errors.New("sandbox worker response exceeded limit")
	}
	if run.waitErr != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = run.waitErr.Error()
		}
		return SandboxWorkerResponse{}, outcome, fmt.Errorf("sandbox worker: %s", message)
	}
	var response SandboxWorkerResponse
	if err := json.Unmarshal([]byte(stdout.String()), &response); err != nil {
		return SandboxWorkerResponse{}, outcome, fmt.Errorf("decode sandbox worker response: %w", err)
	}
	return response, outcome, nil
}

func sandboxNetworkLabel(policy sandbox.Policy) string {
	if len(policy.Network.AllowedHosts) == 0 {
		return "off"
	}
	return fmt.Sprintf("allow:%d", len(policy.Network.AllowedHosts))
}
