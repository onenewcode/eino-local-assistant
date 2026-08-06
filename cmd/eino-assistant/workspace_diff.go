package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	workspaceDiffMaxBytes       = 128 * 1024
	workspaceDiffMaxStderrBytes = 8 * 1024
	workspaceDiffMaxFiles       = 1024
	workspaceDiffTimeout        = 5 * time.Second
)

var (
	errWorkspaceDiffRootUnavailable = errors.New("workspace root is unavailable")
	errWorkspaceDiffNotGit          = errors.New("workspace is not a Git repository")
	errWorkspaceDiffNoHead          = errors.New("Git repository has no HEAD commit")
	errWorkspaceDiffGitUnavailable  = errors.New("git executable is unavailable")
	errWorkspaceDiffTimedOut        = errors.New("git diff timed out")
	errWorkspaceDiffCanceled        = errors.New("git diff canceled")
	errWorkspaceDiffFailed          = errors.New("git diff failed")
	errWorkspaceDiffRunnerMissing   = errors.New("workspace diff runner is unavailable")
	errWorkspaceDiffUnsafePath      = errors.New("workspace diff path is unsafe")
	errWorkspaceDiffUntrackedList   = errors.New("untracked file list is invalid or too large")
)

// workspaceDiffCommandResult is intentionally private to the runtime. The
// TUI receives only the bounded text returned by readWorkspaceDiff.
type workspaceDiffCommandResult struct {
	stdout          []byte
	stderr          []byte
	stdoutTruncated bool
	stderrTruncated bool
}

type workspaceDiffCommandRunner func(context.Context, string, []string) (workspaceDiffCommandResult, error)

func (r *commandRuntime) workspaceDiff(ctx context.Context) (string, error) {
	if r == nil {
		return "", errWorkspaceDiffRootUnavailable
	}
	return readWorkspaceDiff(ctx, r.workspaceRoot)
}

func readWorkspaceDiff(ctx context.Context, workspaceRoot string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return "", errWorkspaceDiffRootUnavailable
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil || !info.IsDir() {
		return "", errWorkspaceDiffRootUnavailable
	}

	return readWorkspaceDiffWithRunner(ctx, workspaceRoot, runWorkspaceDiffCommand)
}

func readWorkspaceDiffWithRunner(ctx context.Context, workspaceRoot string, runner workspaceDiffCommandRunner) (string, error) {
	if runner == nil {
		return "", errWorkspaceDiffRunnerMissing
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// One deadline and the shared output/stderr accumulators cover the complete
	// tracked snapshot, untracked enumeration, and every per-file diff.
	diffCtx, cancel := context.WithTimeout(ctx, workspaceDiffTimeout)
	defer cancel()

	var output []byte
	var stderr []byte
	run := func(args []string) (workspaceDiffCommandResult, error) {
		result, err := runner(diffCtx, workspaceRoot, args)
		stderr, _ = appendWorkspaceDiffBytes(stderr, result.stderr, workspaceDiffMaxStderrBytes)
		if err != nil {
			return result, classifyWorkspaceDiffError(diffCtx, stderr, err)
		}
		return result, nil
	}

	tracked, err := run(workspaceDiffArguments())
	if err != nil {
		return "", err
	}
	var truncated bool
	output, truncated = appendWorkspaceDiffBytes(output, tracked.stdout, workspaceDiffMaxBytes)
	if tracked.stdoutTruncated || truncated {
		return formatWorkspaceDiffOutput(output, true), nil
	}

	untrackedList, err := run(workspaceUntrackedEnumerationArguments())
	if err != nil {
		return "", err
	}
	if untrackedList.stdoutTruncated {
		return "", errWorkspaceDiffUntrackedList
	}
	paths, err := parseWorkspaceDiffUntrackedPaths(untrackedList.stdout)
	if err != nil {
		return "", err
	}
	for i, path := range paths {
		untrackedDiff, err := run(workspaceUntrackedDiffArguments(path))
		if err != nil {
			return "", err
		}
		var diffTruncated bool
		output, diffTruncated = appendWorkspaceDiffBytes(output, untrackedDiff.stdout, workspaceDiffMaxBytes)
		if untrackedDiff.stdoutTruncated || diffTruncated || i+1 < len(paths) && len(output) >= workspaceDiffMaxBytes {
			truncated = true
			break
		}
	}
	return formatWorkspaceDiffOutput(output, truncated), nil
}

func formatWorkspaceDiffOutput(output []byte, truncated bool) string {
	if !truncated {
		return string(output)
	}

	var b strings.Builder
	b.Write(output)
	if b.Len() > 0 && !bytes.HasSuffix(output, []byte("\n")) {
		b.WriteByte('\n')
	}
	b.WriteString("[diff output truncated after ")
	b.WriteString(strconv.Itoa(workspaceDiffMaxBytes))
	b.WriteString(" bytes]")
	return b.String()
}

func workspaceDiffArguments() []string {
	// HEAD includes both staged and unstaged changes. The final -- keeps the
	// command scoped to the worktree. Untracked files are added separately below.
	// --no-renames avoids config-dependent similarity matching and keeps this V1
	// snapshot bounded; rename-aware review is a separate product surface.
	return []string{
		"--no-pager",
		"diff",
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--no-renames",
		"HEAD",
		"--",
	}
}

func workspaceUntrackedEnumerationArguments() []string {
	return []string{
		"--no-pager",
		"ls-files",
		"--others",
		"--exclude-standard",
		"-z",
		"--",
	}
}

func workspaceUntrackedDiffArguments(path string) []string {
	return []string{
		"--no-pager",
		"diff",
		"--no-index",
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--no-renames",
		"--",
		os.DevNull,
		path,
	}
}

func parseWorkspaceDiffUntrackedPaths(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != 0 {
		return nil, errWorkspaceDiffUntrackedList
	}

	entries := bytes.Split(data[:len(data)-1], []byte{0})
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		path := string(entry)
		if path == "" {
			return nil, errWorkspaceDiffUntrackedList
		}
		if !isSafeWorkspaceDiffPath(path) {
			return nil, errWorkspaceDiffUnsafePath
		}
		paths = append(paths, path)
		if len(paths) > workspaceDiffMaxFiles {
			return nil, errWorkspaceDiffUntrackedList
		}
	}
	return paths, nil
}

func isSafeWorkspaceDiffPath(path string) bool {
	if path == "" || strings.IndexByte(path, 0) >= 0 || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || clean == ".." {
		return false
	}
	return !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func classifyWorkspaceDiffError(ctx context.Context, stderr []byte, err error) error {
	if ctx != nil {
		switch ctx.Err() {
		case context.DeadlineExceeded:
			return errWorkspaceDiffTimedOut
		case context.Canceled:
			return errWorkspaceDiffCanceled
		}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return errWorkspaceDiffGitUnavailable
	}

	diagnostic := strings.ToLower(string(stderr))
	if err != nil {
		diagnostic += " " + strings.ToLower(err.Error())
	}
	switch {
	case strings.Contains(diagnostic, "not a git repository"),
		strings.Contains(diagnostic, "must be run in a work tree"):
		return errWorkspaceDiffNotGit
	case strings.Contains(diagnostic, "ambiguous argument 'head'"),
		strings.Contains(diagnostic, "unknown revision or path not in the working tree 'head'"):
		return errWorkspaceDiffNoHead
	default:
		return errWorkspaceDiffFailed
	}
}

type boundedPipeResult struct {
	data      []byte
	truncated bool
	err       error
}

func runWorkspaceDiffCommand(ctx context.Context, workspaceRoot string, args []string) (workspaceDiffCommandResult, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = workspaceRoot
	cmd.Env = withEnvironmentValue(os.Environ(), "GIT_OPTIONAL_LOCKS", "0")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return workspaceDiffCommandResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return workspaceDiffCommandResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return workspaceDiffCommandResult{}, err
	}

	var killOnce sync.Once
	kill := func() {
		killOnce.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
	}
	stdoutCh := make(chan boundedPipeResult, 1)
	stderrCh := make(chan boundedPipeResult, 1)
	go func() {
		stdoutCh <- readBoundedPipe(stdout, workspaceDiffMaxBytes, kill)
	}()
	go func() {
		stderrCh <- readBoundedPipe(stderr, workspaceDiffMaxStderrBytes, kill)
	}()

	// Read both pipes before Wait so StdoutPipe/StderrPipe are fully drained
	// without relying on os/exec's internal copy goroutines.
	stdoutResult := <-stdoutCh
	stderrResult := <-stderrCh
	waitErr := cmd.Wait()
	result := workspaceDiffCommandResult{
		stdout:          stdoutResult.data,
		stderr:          stderrResult.data,
		stdoutTruncated: stdoutResult.truncated,
		stderrTruncated: stderrResult.truncated,
	}
	if stdoutResult.err != nil && !result.stdoutTruncated {
		return result, stdoutResult.err
	}
	if stderrResult.err != nil && !result.stderrTruncated {
		return result, stderrResult.err
	}
	if result.stdoutTruncated {
		// The output itself is the successful read-only result. Killing Git after
		// the cap prevents a hostile diff from making the TUI wait indefinitely.
		return result, nil
	}
	if isWorkspaceDiffDifferenceExit(args, result.stderr, waitErr) {
		return result, nil
	}
	return result, waitErr
}

func isWorkspaceDiffDifferenceExit(args []string, stderr []byte, err error) bool {
	if err == nil || len(stderr) != 0 || !containsWorkspaceDiffArgument(args, "--no-index") {
		return false
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func containsWorkspaceDiffArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func appendWorkspaceDiffBytes(current, next []byte, limit int) ([]byte, bool) {
	if len(next) == 0 {
		return current, false
	}
	if limit <= len(current) {
		return current, true
	}
	remaining := limit - len(current)
	if len(next) > remaining {
		current = append(current, next[:remaining]...)
		return current, true
	}
	return append(current, next...), false
}

func readBoundedPipe(reader io.Reader, limit int, kill func()) boundedPipeResult {
	if limit < 0 {
		limit = 0
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
		if kill != nil {
			kill()
		}
	}
	return boundedPipeResult{data: data, truncated: truncated, err: err}
}

func withEnvironmentValue(environment []string, key, value string) []string {
	prefix := key + "="
	updated := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		updated = append(updated, entry)
	}
	return append(updated, prefix+value)
}
