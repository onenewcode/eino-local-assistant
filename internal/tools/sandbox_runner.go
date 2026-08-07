package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	// ToolchainVisibility controls whether safe host toolchain paths are
	// discovered and mounted read-only. Empty uses automatic discovery.
	ToolchainVisibility sandbox.ToolchainVisibility
	// HostEnvironment is a test seam. Nil snapshots the parent process
	// environment when the runner is created.
	HostEnvironment []string
	// WorkerPath overrides the current executable for integration tests. Empty
	// uses os.Executable, which is the production private worker entrypoint.
	WorkerPath string
	// currentAvailability is an internal seam for launcher-pinning tests.
	currentAvailability func() sandbox.Availability
}

// SandboxOutcome is persisted in tool results so the model and session ledger
// can distinguish an enforced restriction from a host escalation.
type SandboxOutcome struct {
	Mode                string   `json:"mode,omitempty"`
	Backend             string   `json:"backend,omitempty"`
	Enforced            bool     `json:"enforced,omitempty"`
	Escalated           bool     `json:"escalated,omitempty"`
	EnvironmentMode     string   `json:"environment_mode,omitempty"`
	ToolchainVisibility string   `json:"toolchain_visibility,omitempty"`
	EffectivePath       []string `json:"effective_path,omitempty"`
	VisibleRootCount    int      `json:"visible_root_count,omitempty"`
	CacheRootCount      int      `json:"cache_root_count,omitempty"`
	// Bypassed marks explicit yolo host execution. It is separate from
	// Escalated, which means a single ordinary-mode host request.
	Bypassed bool `json:"bypassed,omitempty"`
}

func yoloSandboxOutcome() SandboxOutcome {
	return SandboxOutcome{
		Mode:     string(ApprovalYolo),
		Backend:  "host",
		Bypassed: true,
	}
}

// SandboxRunner launches the private worker through the current platform's
// strict backend. It never falls back to the host on a backend failure.
type SandboxRunner struct {
	mu         sync.Mutex
	executions sync.WaitGroup
	closed     bool
	// basePolicy is session-validated (NormalizePolicy at construction) with
	// TempDir cleared. Each Execute rebinds a private temp dir via WithTempDir
	// without repeating the workspace hard-link scan.
	basePolicy    sandbox.Policy
	workerPath    string
	availability  sandbox.Availability
	workerCleanup func() error
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
	hostEnvironment := opts.HostEnvironment
	if hostEnvironment == nil {
		hostEnvironment = os.Environ()
	}
	environment, err := sandbox.DiscoverHostEnvironment(hostEnvironment, workspace, opts.ToolchainVisibility)
	if err != nil {
		return nil, fmt.Errorf("discover sandbox environment: %w", err)
	}
	defer os.RemoveAll(tempDir)
	policy, err := sandbox.NormalizePolicy(sandbox.Policy{
		Mode:           opts.Mode,
		Workspace:      workspace,
		TempDir:        tempDir,
		Environment:    environment,
		ReadOnlyRoots:  opts.ReadOnlyRoots,
		ProtectedPaths: opts.ProtectedPaths,
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
	base := policy.WithoutTempDir()
	base.ReadOnlyRoots = append([]string(nil), policy.ReadOnlyRoots...)
	base.ProtectedPaths = append([]string(nil), policy.ProtectedPaths...)
	base.Environment.Variables = append([]string(nil), policy.Environment.Variables...)
	base.Environment.PathEntries = append([]string(nil), policy.Environment.PathEntries...)
	base.Environment.ReadOnlyRoots = append([]string(nil), policy.Environment.ReadOnlyRoots...)
	base.Environment.CacheRoots = append([]string(nil), policy.Environment.CacheRoots...)
	return &SandboxRunner{
		basePolicy:    base,
		workerPath:    stagedWorker,
		availability:  availability,
		workerCleanup: workerCleanup,
	}, nil
}

// EnvironmentSnapshot returns the session-stable environment metadata used by
// workers. It is a copy so display code cannot alter the launch policy.
func (r *SandboxRunner) EnvironmentSnapshot() sandbox.EnvironmentSnapshot {
	if r == nil {
		return sandbox.EnvironmentSnapshot{}
	}
	snapshot := r.basePolicy.Environment
	snapshot.Variables = append([]string(nil), snapshot.Variables...)
	snapshot.PathEntries = append([]string(nil), snapshot.PathEntries...)
	snapshot.ReadOnlyRoots = append([]string(nil), snapshot.ReadOnlyRoots...)
	snapshot.CacheRoots = append([]string(nil), snapshot.CacheRoots...)
	return snapshot
}

// ReadOnlyRoots returns all explicit and automatically discovered roots.
func (r *SandboxRunner) ReadOnlyRoots() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.basePolicy.ReadOnlyRoots...)
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
	if request.ReadOnly {
		// A plan shell call temporarily narrows the session's normal policy. Do
		// not mutate basePolicy: later non-plan calls retain workspace-write.
		policy.Mode = sandbox.ReadOnly
	}

	request.WorkspaceRoot = policy.Workspace
	payload, err := json.Marshal(request)
	if err != nil {
		return SandboxWorkerResponse{}, SandboxOutcome{}, fmt.Errorf("encode sandbox worker request: %w", err)
	}
	spec, err := sandbox.BuildCommandFromNormalized(ctx, policy, workerPath, []string{"__sandbox_worker"}, r.availability)
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
	snapshot := r.EnvironmentSnapshot()
	outcome := SandboxOutcome{
		Mode:                string(policy.Mode),
		Backend:             string(spec.Backend),
		Enforced:            true,
		EnvironmentMode:     sandboxEnvironmentMode(snapshot),
		ToolchainVisibility: string(snapshot.Mode),
		EffectivePath:       append([]string(nil), snapshot.PathEntries...),
		VisibleRootCount:    len(policy.ReadOnlyRoots),
		CacheRootCount:      len(snapshot.CacheRoots),
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

func sandboxEnvironmentMode(snapshot sandbox.EnvironmentSnapshot) string {
	if snapshot.Mode == sandbox.ToolchainVisibilityAuto {
		return "filtered-host"
	}
	return "isolated"
}
