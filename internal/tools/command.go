package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	defaultCommandTimeoutSeconds = 60
	maxCommandTimeoutSeconds     = 300
	defaultCommandOutputBytes    = 64 << 10
	maxCommandOutputBytes        = 1 << 20
	commandExitCodeUnavailable   = -1
	// Bound how long we wait for Wait after cancel/kill so a stuck child
	// cannot freeze the ReAct turn indefinitely.
	commandWaitGrace = 2 * time.Second
)

// RunCommandOptions configures the built-in local shell tool.
type RunCommandOptions struct {
	// Disabled skips registering run_command when true.
	Disabled bool
	// TimeoutSeconds is the default per-call timeout. Zero uses 60s.
	TimeoutSeconds int
	// MaxOutputBytes caps each of stdout/stderr returned to the model. Zero uses 64KiB.
	MaxOutputBytes int
	// WorkingDir is the default cwd when the model omits working_dir. Empty uses process cwd.
	WorkingDir string
}

// RunCommandInput is the model-facing argument for run_command.
type RunCommandInput struct {
	Command        string `json:"command" jsonschema:"description=Shell command to execute on the local host, for example ls -la or go test ./...."`
	WorkingDir     string `json:"working_dir,omitempty" jsonschema:"description=Optional working directory. Absolute paths are used as-is; relative paths resolve against the tool default working_dir (or process cwd when unset)."`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"description=Optional timeout in seconds for this call (default 60, maximum 300)."`
}

// RunCommandOutput is the structured shell result returned to the model.
// Non-zero exit codes, timeouts, and cancellations are soft results, not tool errors.
type RunCommandOutput struct {
	Command     string `json:"command"`
	WorkingDir  string `json:"working_dir"`
	ExitCode    int    `json:"exit_code"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	DurationMs  int64  `json:"duration_ms"`
	TimedOut    bool   `json:"timed_out"`
	Cancelled   bool   `json:"cancelled"`
	Truncated   bool   `json:"truncated"`
	StdoutBytes int    `json:"stdout_bytes"`
	StderrBytes int    `json:"stderr_bytes"`
}

// NewRunCommand builds the run_command tool.
func NewRunCommand(opts RunCommandOptions) (tool.InvokableTool, error) {
	defaults, err := normalizeRunCommandOptions(opts)
	if err != nil {
		return nil, err
	}

	return utils.InferTool(
		"run_command",
		"Execute a local shell command via sh -c and return exit_code, stdout, and stderr. Use this to inspect the workspace, run builds/tests, query git, or read files with standard CLI tools. Non-zero exit codes are normal results, not tool failures—read stderr and recover. Long stdout/stderr may be truncated in this tool result (see truncated/stdout_bytes/stderr_bytes); the discarded tail is not retained, so do not expect read_artifact to recover bytes beyond the cap—re-run with a narrower command or higher max_output_bytes instead. Never invent command output.",
		func(ctx context.Context, input RunCommandInput) (RunCommandOutput, error) {
			return runCommand(ctx, defaults, input)
		},
	)
}

func normalizeRunCommandOptions(opts RunCommandOptions) (RunCommandOptions, error) {
	if opts.TimeoutSeconds < 0 {
		return RunCommandOptions{}, errors.New("timeout_seconds must be >= 0")
	}
	if opts.TimeoutSeconds == 0 {
		opts.TimeoutSeconds = defaultCommandTimeoutSeconds
	}
	if opts.TimeoutSeconds > maxCommandTimeoutSeconds {
		return RunCommandOptions{}, fmt.Errorf("timeout_seconds must be <= %d", maxCommandTimeoutSeconds)
	}

	if opts.MaxOutputBytes < 0 {
		return RunCommandOptions{}, errors.New("max_output_bytes must be >= 0")
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = defaultCommandOutputBytes
	}
	if opts.MaxOutputBytes > maxCommandOutputBytes {
		return RunCommandOptions{}, fmt.Errorf("max_output_bytes must be <= %d", maxCommandOutputBytes)
	}

	dir := strings.TrimSpace(opts.WorkingDir)
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return RunCommandOptions{}, fmt.Errorf("resolve working_dir: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return RunCommandOptions{}, fmt.Errorf("working_dir %q: %w", abs, err)
		}
		if !info.IsDir() {
			return RunCommandOptions{}, fmt.Errorf("working_dir %q is not a directory", abs)
		}
		opts.WorkingDir = abs
	} else {
		opts.WorkingDir = ""
	}
	return opts, nil
}

func runCommand(ctx context.Context, defaults RunCommandOptions, input RunCommandInput) (RunCommandOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	command := strings.TrimSpace(input.Command)
	if command == "" {
		return RunCommandOutput{}, errors.New("command is required")
	}

	cwd, err := resolveWorkingDir(defaults.WorkingDir, input.WorkingDir)
	if err != nil {
		return RunCommandOutput{}, err
	}

	timeoutSeconds := defaults.TimeoutSeconds
	if input.TimeoutSeconds != 0 {
		if input.TimeoutSeconds < 0 || input.TimeoutSeconds > maxCommandTimeoutSeconds {
			return RunCommandOutput{}, fmt.Errorf("timeout_seconds must be between 1 and %d", maxCommandTimeoutSeconds)
		}
		timeoutSeconds = input.TimeoutSeconds
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir = cwd
	// Prefer killing the whole process group so sh -c grandchildren stop with Esc/timeout.
	configureCommandProcessGroup(cmd)

	stdoutCap := newLimitedBuffer(defaults.MaxOutputBytes)
	stderrCap := newLimitedBuffer(defaults.MaxOutputBytes)
	cmd.Stdout = stdoutCap
	cmd.Stderr = stderrCap

	started := time.Now()
	if err := cmd.Start(); err != nil {
		return RunCommandOutput{}, fmt.Errorf("start command: %w", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var waitErr error
	select {
	case waitErr = <-waitDone:
		// Do not kill after Wait returned: the leader is already reaped and
		// Kill(-pid) could hit a recycled process group on a busy host.
	case <-runCtx.Done():
		killCommandProcessGroup(cmd)
		// Bound Wait so a stuck unreapable child cannot freeze the turn.
		timer := time.NewTimer(commandWaitGrace)
		select {
		case waitErr = <-waitDone:
			timer.Stop()
		case <-timer.C:
			waitErr = errors.New("command did not exit after cancel/kill")
		}
	}
	duration := time.Since(started)

	out := RunCommandOutput{
		Command:     command,
		WorkingDir:  cwd,
		ExitCode:    0,
		Stdout:      stdoutCap.String(),
		Stderr:      stderrCap.String(),
		DurationMs:  duration.Milliseconds(),
		Truncated:   stdoutCap.Truncated() || stderrCap.Truncated(),
		StdoutBytes: stdoutCap.Total(),
		StderrBytes: stderrCap.Total(),
	}

	// Prefer an observed exit status over a racing deadline: a command that
	// already finished successfully should not be reported as timed out.
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		out.ExitCode = 0
	case errors.As(waitErr, &exitErr):
		out.ExitCode = exitErr.ExitCode()
		// Still surface cancel/timeout when the process was killed by us.
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			out.TimedOut = true
		} else if errors.Is(runCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			out.Cancelled = true
		}
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		out.TimedOut = true
		out.ExitCode = commandExitCodeUnavailable
	case errors.Is(runCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		out.Cancelled = true
		out.ExitCode = commandExitCodeUnavailable
	default:
		return RunCommandOutput{}, fmt.Errorf("wait command: %w", waitErr)
	}

	return out, nil
}

func resolveWorkingDir(defaultDir, inputDir string) (string, error) {
	input := strings.TrimSpace(inputDir)
	base := strings.TrimSpace(defaultDir)

	var dir string
	switch {
	case input == "" && base == "":
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working_dir: %w", err)
		}
		return cwd, nil
	case input == "":
		dir = base
	case filepath.IsAbs(input):
		dir = input
	case base != "":
		// Relative model paths resolve against the configured default base,
		// not the process CWD, so working_dir:"cmd" means default/cmd.
		dir = filepath.Join(base, input)
	default:
		dir = input
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve working_dir: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("working_dir %q: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working_dir %q is not a directory", abs)
	}
	return abs, nil
}

// limitedBuffer stores at most limit bytes while counting the full write size.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	total     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if b.limit <= 0 {
		b.truncated = b.truncated || len(p) > 0
		return len(p), nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	if b == nil {
		return ""
	}
	return b.buf.String()
}

func (b *limitedBuffer) Total() int {
	if b == nil {
		return 0
	}
	return b.total
}

func (b *limitedBuffer) Truncated() bool {
	return b != nil && b.truncated
}
