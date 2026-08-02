package tools

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"time"
)

// commandRunResult records the reason a managed command stopped. The caller
// translates it into a tool-specific structured result.
type commandRunResult struct {
	waitErr       error
	contextDone   bool
	outputLimited bool
	waitTimedOut  bool
}

// runCommandWithLifecycle starts cmd and manages its original process group.
// A cancelled context or output cap first sends TERM, then KILL after
// commandWaitGrace to ordinary descendants that remain in that group. A child
// that deliberately creates another session/process group is not covered by
// this portable Unix mechanism.
func runCommandWithLifecycle(ctx context.Context, cmd *exec.Cmd, stdout, stderr *limitedBuffer) (commandRunResult, error) {
	return runCommandWithLifecycleWithGrace(ctx, cmd, stdout, stderr, commandWaitGrace, commandWaitGrace)
}

// runCommandWithLifecycleWithGrace is the internal variant for a command that
// supervises another process group. Its shutdown grace must leave enough time
// for that supervisor to signal ordinary descendants before the outer wrapper
// is forcibly killed.
func runCommandWithLifecycleWithGrace(ctx context.Context, cmd *exec.Cmd, stdout, stderr *limitedBuffer, termGrace, killGrace time.Duration) (commandRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if termGrace <= 0 {
		termGrace = commandWaitGrace
	}
	if killGrace <= 0 {
		killGrace = commandWaitGrace
	}
	if cmd == nil {
		return commandRunResult{}, errors.New("command is required")
	}
	if err := ctx.Err(); err != nil {
		return commandRunResult{contextDone: true}, nil
	}

	outputLimit := make(chan struct{})
	var limitOnce sync.Once
	notifyOutputLimit := func() {
		limitOnce.Do(func() { close(outputLimit) })
	}
	if stdout != nil {
		stdout.setLimitHandler(notifyOutputLimit)
	}
	if stderr != nil {
		stderr.setLimitHandler(notifyOutputLimit)
	}

	if err := cmd.Start(); err != nil {
		return commandRunResult{}, err
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	// Prefer an already-observed exit before acting on a racing cancellation or
	// cap notification. Sending a signal after Wait reaped the leader could hit
	// a recycled process group ID on a busy host.
	if exited, waitErr := completedCommand(waitDone); exited {
		return commandRunResult{
			waitErr:       waitErr,
			outputLimited: outputReachedLimit(stdout, stderr),
		}, nil
	}

	select {
	case waitErr := <-waitDone:
		return commandRunResult{
			waitErr:       waitErr,
			outputLimited: outputReachedLimit(stdout, stderr),
		}, nil
	case <-ctx.Done():
		if exited, waitErr := completedCommand(waitDone); exited {
			return commandRunResult{
				waitErr:       waitErr,
				outputLimited: outputReachedLimit(stdout, stderr),
			}, nil
		}
		waitTimedOut, waitErr := terminateAndWaitForCommand(cmd, waitDone, termGrace, killGrace)
		return commandRunResult{
			waitErr:       waitErr,
			contextDone:   true,
			outputLimited: outputReachedLimit(stdout, stderr),
			waitTimedOut:  waitTimedOut,
		}, nil
	case <-outputLimit:
		if exited, waitErr := completedCommand(waitDone); exited {
			return commandRunResult{
				waitErr:       waitErr,
				outputLimited: outputReachedLimit(stdout, stderr),
			}, nil
		}
		waitTimedOut, waitErr := terminateAndWaitForCommand(cmd, waitDone, termGrace, killGrace)
		return commandRunResult{
			waitErr:       waitErr,
			outputLimited: true,
			waitTimedOut:  waitTimedOut,
		}, nil
	}
}

func outputReachedLimit(stdout, stderr *limitedBuffer) bool {
	return stdout != nil && stdout.LimitReached() || stderr != nil && stderr.LimitReached()
}

func terminateAndWaitForCommand(cmd *exec.Cmd, waitDone <-chan error, termGrace, killGrace time.Duration) (bool, error) {
	if exited, waitErr := completedCommand(waitDone); exited {
		return false, waitErr
	}
	terminateCommandProcessGroup(cmd)
	if exited, waitErr := waitForCommand(waitDone, termGrace); exited {
		return false, waitErr
	}

	killCommandProcessGroup(cmd)
	if exited, waitErr := waitForCommand(waitDone, killGrace); exited {
		return false, waitErr
	}
	return true, errors.New("command did not exit after TERM/KILL")
}

func completedCommand(waitDone <-chan error) (bool, error) {
	select {
	case waitErr := <-waitDone:
		return true, waitErr
	default:
		return false, nil
	}
}

func waitForCommand(waitDone <-chan error, grace time.Duration) (bool, error) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case waitErr := <-waitDone:
		return true, waitErr
	case <-timer.C:
		return false, nil
	}
}
