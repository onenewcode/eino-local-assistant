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
	"sync"
	"time"

	"eino-local-assistant/internal/sandbox"

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

	// shellToolDescription follows Codex "Tool Guidelines / Shell commands" tone.
	shellToolDescription = `Runs a shell command and returns its output.

When using the shell, you must adhere to the following guidelines:
- Prefer the apply_patch tool to edit files (NEVER try applypatch or apply-patch; only apply_patch).
- Do not use shell for ordinary file create/edit/delete (no touch/echo-redirection/sed/heredoc when apply_patch can do it).
- Use shell for terminal work: git, builds, tests, package managers, process inspection, and reading/searching with cat/head/rg when appropriate.
- Non-zero exit codes are normal results—read stderr and recover. Never invent command output.
- If denied=true with user_denied, do not retry an equivalent command or bypass via apply_patch; stop and ask the user.
- Reaching the stdout/stderr cap signals the command's original process group; on macOS a deliberately detached descendant may survive. The discarded tail is not retained—re-run with a narrower command or higher max_output_bytes.`
)

// ShellOptions configures the Codex-style shell tool.
type ShellOptions struct {
	// Disabled skips registering shell when true.
	Disabled bool
	// TimeoutSeconds is the default per-call timeout. Zero uses 60s.
	TimeoutSeconds int
	// MaxOutputBytes caps each of stdout/stderr returned to the model. Zero uses 64KiB.
	MaxOutputBytes int
	// WorkingDir is the default cwd when the model omits working_dir. Empty uses process cwd.
	WorkingDir string
	// Approval controls how DecisionAsk is handled. Empty defaults to on_request.
	Approval ApprovalMode
	// ApprovalState, when set, supplies the current mode for each invocation.
	// It is shared with apply_patch by the production registry.
	ApprovalState *ApprovalState
	// WorkspaceOnly rejects working_dir outside WorkspaceRoot.
	WorkspaceOnly bool
	// WorkspaceRoot is the path clamp root. Empty resolves to process cwd at normalize time.
	WorkspaceRoot string
	// Rules is the loaded Codex execpolicy. Nil retains the built-in known-safe
	// fallback and asks for commands that have no matching rule.
	Rules *ToolPolicy
	// Approver is invoked for ask decisions when Approval is on_request.
	// Nil with on_request yields a soft deny (fail-closed).
	Approver Approver
	// SessionAllows remembers "allow for session" keys. Nil disables session memory.
	SessionAllows *SessionAllowlist
	// SessionDenies remembers rule keys the user explicitly denied this session.
	SessionDenies *SessionDenylist
	// DenyStreaks tracks consecutive soft denials per rule_key.
	DenyStreaks *DenyStreak
	// Sandbox runs normal shell calls in a strict one-shot worker. Nil keeps the
	// direct runner for focused unit tests and callers that deliberately do not
	// install the product sandbox.
	Sandbox *SandboxRunner
}

// ShellInput is the model-facing argument for shell (Codex: command as a string).
type ShellInput struct {
	Command        string `json:"command" jsonschema:"description=The shell command to run as a single string, e.g. git status or go test ./...."`
	WorkingDir     string `json:"working_dir,omitempty" jsonschema:"description=Optional working directory. Absolute paths are used as-is; relative paths resolve against the tool default working_dir (or process cwd when unset)."`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"description=Optional timeout in seconds for this call (default 60, maximum 300)."`
	// SandboxPermissions requests the normal sandbox or a one-shot host
	// escalation. The latter always requires explicit human approval.
	SandboxPermissions string `json:"sandbox_permissions,omitempty" jsonschema:"enum=use_default,enum=require_escalated,description=Use use_default for the strict sandbox. require_escalated requests one-time host execution and requires a justification."`
	Justification      string `json:"justification,omitempty" jsonschema:"description=Required when sandbox_permissions is require_escalated; explain why the strict sandbox cannot perform the command."`
}

// ShellOutput is the structured shell result returned to the model.
// Non-zero exit codes, timeouts, cancellations, and policy denials are soft results, not tool errors.
type ShellOutput struct {
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
	// OutputLimited reports that the command exceeded a configured output cap.
	OutputLimited bool `json:"output_limited,omitempty"`
	// Denied is true when policy or the user blocked execution (no process started).
	Denied bool `json:"denied"`
	// Decision is allow|ask|deny when authorization ran.
	Decision string `json:"decision,omitempty"`
	// Reason explains a deny or the policy hit that required approval.
	Reason string `json:"reason,omitempty"`
	// StopRetrying hints the model should not re-issue the same blocked command prefix.
	StopRetrying bool `json:"stop_retrying,omitempty"`
	// Impact is the policy-derived command tier, independent of authorization.
	Impact ToolImpact `json:"impact"`
	// Sandbox records the actual boundary used for this execution.
	Sandbox *SandboxOutcome `json:"sandbox,omitempty"`
}

// NewShell builds the shell tool (registered name: shell).
func NewShell(opts ShellOptions) (tool.InvokableTool, error) {
	defaults, err := normalizeShellOptions(opts)
	if err != nil {
		return nil, err
	}

	return utils.InferTool(
		"shell",
		shellToolDescription,
		func(ctx context.Context, input ShellInput) (ShellOutput, error) {
			impact := ClassifyShellCommand(input.Command)
			output, err := runShell(ctx, defaults, input)
			if err != nil {
				return ShellOutput{}, err
			}
			output.Impact = impact
			return output, nil
		},
	)
}

func normalizeShellOptions(opts ShellOptions) (ShellOptions, error) {
	if opts.TimeoutSeconds < 0 {
		return ShellOptions{}, errors.New("timeout_seconds must be >= 0")
	}
	if opts.TimeoutSeconds == 0 {
		opts.TimeoutSeconds = defaultCommandTimeoutSeconds
	}
	if opts.TimeoutSeconds > maxCommandTimeoutSeconds {
		return ShellOptions{}, fmt.Errorf("timeout_seconds must be <= %d", maxCommandTimeoutSeconds)
	}

	if opts.MaxOutputBytes < 0 {
		return ShellOptions{}, errors.New("max_output_bytes must be >= 0")
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = defaultCommandOutputBytes
	}
	if opts.MaxOutputBytes > maxCommandOutputBytes {
		return ShellOptions{}, fmt.Errorf("max_output_bytes must be <= %d", maxCommandOutputBytes)
	}

	dir := strings.TrimSpace(opts.WorkingDir)
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return ShellOptions{}, fmt.Errorf("resolve working_dir: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return ShellOptions{}, fmt.Errorf("working_dir %q: %w", abs, err)
		}
		if !info.IsDir() {
			return ShellOptions{}, fmt.Errorf("working_dir %q is not a directory", abs)
		}
		opts.WorkingDir = abs
	} else {
		opts.WorkingDir = ""
	}

	switch opts.Approval {
	case "", ApprovalOnRequest:
		opts.Approval = ApprovalOnRequest
	case ApprovalNever:
		// ok
	case ApprovalPlan:
		// Plan is normally supplied through ApprovalState, but accepting the
		// canonical value keeps direct tool construction consistent.
	case ApprovalYolo:
		// Yolo is only enabled by an explicit runtime or TUI entrypoint;
		// configuration validation rejects it as a static policy value.
	default:
		return ShellOptions{}, fmt.Errorf("approval must be %q, %q, %q, or %q", ApprovalOnRequest, ApprovalNever, ApprovalPlan, ApprovalYolo)
	}

	root, err := ResolveWorkspaceRoot(opts.WorkspaceRoot)
	if err != nil {
		return ShellOptions{}, err
	}
	opts.WorkspaceRoot = root

	if opts.DenyStreaks == nil {
		opts.DenyStreaks = NewDenyStreak(0)
	}
	return opts, nil
}

func runShell(ctx context.Context, defaults ShellOptions, input ShellInput) (ShellOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	command := strings.TrimSpace(input.Command)
	impact := ClassifyShellCommand(command)
	approvalMode := effectiveApprovalMode(defaults.Approval, defaults.ApprovalState)
	// Reject an explicit host escape before validating its justification or
	// resolving/pinning its executable. Plan mode must not even inspect the
	// host command, and this path deliberately avoids all deny bookkeeping.
	if approvalMode == ApprovalPlan && strings.EqualFold(strings.TrimSpace(input.SandboxPermissions), string(sandboxPermissionsEscalated)) {
		if ev := defaults.Rules.EvaluateShell(command); ev.Decision == DecisionDeny {
			reason := fmt.Sprintf("%s: %s", ReasonPolicyDenied, ev.Reason)
			return softDeny(command, defaults.WorkingDir, DecisionDeny, reason, false), nil
		}
		return softDeny(command, defaults.WorkingDir, DecisionDeny, ReasonPlanReadOnly, false), nil
	}
	if command == "" {
		return ShellOutput{}, errors.New("command is required")
	}
	permissions := sandboxPermissionsDefault
	if isYoloApprovalMode(approvalMode) {
		// Explicit yolo already selects direct host execution. Accept the
		// model's optional escalation spelling without invoking the ordinary
		// justification/approval path; path and hard-policy checks still run.
		raw := strings.ToLower(strings.TrimSpace(input.SandboxPermissions))
		if raw != "" && raw != string(sandboxPermissionsDefault) && raw != string(sandboxPermissionsEscalated) {
			return ShellOutput{}, fmt.Errorf("sandbox_permissions must be %q or %q", sandboxPermissionsDefault, sandboxPermissionsEscalated)
		}
	} else {
		var err error
		permissions, err = normalizeSandboxPermissions(input.SandboxPermissions, input.Justification)
		if err != nil {
			return ShellOutput{}, err
		}
	}

	cwd, err := resolveWorkingDir(defaults.WorkingDir, input.WorkingDir)
	if err != nil {
		return ShellOutput{}, err
	}

	// Pin host-escalation argv before authorization so the approval modal shows
	// the exact absolute executable that will be exec'd after the user answers.
	var hostCommand hostEscalationCommand
	forceHost := permissions == sandboxPermissionsEscalated
	if forceHost {
		var safety Evaluation
		hostCommand, safety = prepareHostEscalationCommand(command, cwd, defaults.WorkspaceRoot)
		if safety.Decision == DecisionDeny {
			reason := fmt.Sprintf("%s: %s", ReasonPolicyDenied, safety.Reason)
			if approvalMode == ApprovalPlan {
				return softDeny(command, cwd, DecisionDeny, ReasonPlanReadOnly, false), nil
			}
			return finishDeny(defaults, command, cwd, DecisionDeny, reason, RuleKey("shell", command, defaults.WorkspaceRoot), true), nil
		}
	}

	// Authorization runs before the per-command timeout so approval wait is not
	// charged against the shell timeout budget.
	if out, blocked, err := authorizeShell(ctx, defaults, command, cwd, forceHost, input.Justification, hostCommand, approvalMode, impact); err != nil {
		return ShellOutput{}, err
	} else if blocked {
		return out, nil
	}

	timeoutSeconds := defaults.TimeoutSeconds
	if input.TimeoutSeconds != 0 {
		if input.TimeoutSeconds < 0 || input.TimeoutSeconds > maxCommandTimeoutSeconds {
			return ShellOutput{}, fmt.Errorf("timeout_seconds must be between 1 and %d", maxCommandTimeoutSeconds)
		}
		timeoutSeconds = input.TimeoutSeconds
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	if defaults.Sandbox != nil && permissions == sandboxPermissionsDefault && !isYoloApprovalMode(approvalMode) {
		return runSandboxShell(runCtx, defaults, command, cwd, timeoutSeconds, approvalMode == ApprovalPlan)
	}
	// authorizeShell rejects this path before execution. Keep a second guard at
	// the execution boundary so a future authorization change cannot fall back
	// to an unsandboxed host shell while plan mode is active.
	if approvalMode == ApprovalPlan {
		return softDeny(command, cwd, DecisionDeny, ReasonSandboxUnavailable+": plan mode requires an enforced read-only sandbox", true), nil
	}

	var out ShellOutput
	if forceHost {
		out, err = runHostEscalatedCommand(runCtx, ctx, defaults, command, cwd, hostCommand)
	} else {
		out, err = runShellDirect(runCtx, ctx, defaults, command, cwd)
	}
	if err != nil {
		return ShellOutput{}, err
	}
	if isYoloApprovalMode(approvalMode) {
		out.Sandbox = func() *SandboxOutcome {
			outcome := yoloSandboxOutcome()
			return &outcome
		}()
	} else if forceHost {
		out.Sandbox = &SandboxOutcome{
			Mode:      "host",
			Backend:   "host",
			Network:   "host",
			Escalated: true,
		}
	}
	return out, nil
}

func runSandboxShell(ctx context.Context, defaults ShellOptions, command, cwd string, timeoutSeconds int, readOnly bool) (ShellOutput, error) {
	response, outcome, err := defaults.Sandbox.Execute(ctx, SandboxWorkerRequest{
		Kind:           sandboxWorkerShell,
		WorkingDir:     cwd,
		Command:        command,
		TimeoutSeconds: timeoutSeconds,
		MaxOutputBytes: defaults.MaxOutputBytes,
		ReadOnly:       readOnly,
	})
	if err != nil {
		if stopped, ok := shellContextStopResult(ctx, command, cwd, outcome); ok {
			return stopped, nil
		}
		out := softDeny(command, cwd, DecisionDeny, ReasonSandboxUnavailable+": "+err.Error(), true)
		out.Sandbox = &outcome
		return out, nil
	}
	if readOnly && (outcome.Mode != string(sandbox.ReadOnly) || !outcome.Enforced || outcome.Escalated) {
		out := softDeny(command, cwd, DecisionDeny, ReasonSandboxUnavailable+": plan mode requires an enforced read-only sandbox", true)
		out.Sandbox = &outcome
		return out, nil
	}
	if response.Error != "" {
		out := softDeny(command, cwd, DecisionDeny, "sandbox_worker_error: "+response.Error, true)
		out.Sandbox = &outcome
		return out, nil
	}
	if response.Shell == nil {
		out := softDeny(command, cwd, DecisionDeny, "sandbox_worker_error: missing shell result", true)
		out.Sandbox = &outcome
		return out, nil
	}
	out := *response.Shell
	out.Sandbox = &outcome
	return out, nil
}

func shellContextStopResult(ctx context.Context, command, cwd string, outcome SandboxOutcome) (ShellOutput, bool) {
	if ctx == nil || ctx.Err() == nil {
		return ShellOutput{}, false
	}
	out := ShellOutput{
		Command:    command,
		WorkingDir: cwd,
		ExitCode:   commandExitCodeUnavailable,
		Sandbox:    &outcome,
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		out.TimedOut = true
	} else {
		out.Cancelled = true
	}
	return out, true
}

// runShellDirect retains host shell semantics for focused unit tests and
// callers that deliberately do not install the product sandbox. One-shot host
// escalations use runHostEscalatedCommand so sh cannot reinterpret the approved
// text after it is rendered in the approval modal.
func runShellDirect(runCtx, parentCtx context.Context, defaults ShellOptions, command, cwd string) (ShellOutput, error) {
	return runDirectCommand(runCtx, parentCtx, defaults, command, cwd, "sh", []string{"-c", command})
}

func runHostEscalatedCommand(runCtx, parentCtx context.Context, defaults ShellOptions, command, cwd string, host hostEscalationCommand) (ShellOutput, error) {
	if host.program == "" {
		return ShellOutput{}, errors.New("host escalation command is required")
	}
	return runDirectCommand(runCtx, parentCtx, defaults, command, cwd, host.program, host.args)
}

func runDirectCommand(runCtx, parentCtx context.Context, defaults ShellOptions, command, cwd, program string, args []string) (ShellOutput, error) {
	cmd := exec.Command(program, args...)
	cmd.Dir = cwd
	// Keep ordinary descendants in the command's original process group so the
	// lifecycle runner can signal them together. A process that creates a new
	// session cannot be fully contained by process-group signals on macOS.
	configureCommandProcessGroup(cmd)

	stdoutCap := newLimitedBuffer(defaults.MaxOutputBytes)
	stderrCap := newLimitedBuffer(defaults.MaxOutputBytes)
	cmd.Stdout = stdoutCap
	cmd.Stderr = stderrCap

	started := time.Now()
	run, err := runCommandWithLifecycle(runCtx, cmd, stdoutCap, stderrCap)
	if err != nil {
		return ShellOutput{}, fmt.Errorf("start command: %w", err)
	}
	duration := time.Since(started)

	out := ShellOutput{
		Command:       command,
		WorkingDir:    cwd,
		ExitCode:      0,
		Stdout:        stdoutCap.String(),
		Stderr:        stderrCap.String(),
		DurationMs:    duration.Milliseconds(),
		Truncated:     stdoutCap.Truncated() || stderrCap.Truncated(),
		StdoutBytes:   stdoutCap.Total(),
		StderrBytes:   stderrCap.Total(),
		OutputLimited: run.outputLimited,
	}

	// Output exhaustion wins over a concurrent cancellation: the model needs a
	// stable reason that the command was stopped for exceeding its hard cap.
	var exitErr *exec.ExitError
	switch {
	case run.outputLimited:
		out.ExitCode = commandExitCodeUnavailable
	case run.contextDone:
		out.ExitCode = commandExitCodeUnavailable
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			out.TimedOut = true
		} else if errors.Is(runCtx.Err(), context.Canceled) || errors.Is(parentCtx.Err(), context.Canceled) {
			out.Cancelled = true
		}
	case run.waitErr == nil:
		out.ExitCode = 0
	case errors.As(run.waitErr, &exitErr):
		out.ExitCode = exitErr.ExitCode()
		// Context cancellation may race an observed non-zero process exit.
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			out.TimedOut = true
		} else if errors.Is(runCtx.Err(), context.Canceled) || errors.Is(parentCtx.Err(), context.Canceled) {
			out.Cancelled = true
		}
	default:
		return ShellOutput{}, fmt.Errorf("wait command: %w", run.waitErr)
	}

	out.Decision = string(DecisionAllow)
	return out, nil
}

// authorizeShell applies workspace clamp + policy + approval. A host
// escalation still evaluates hard/config denies, but collapses ordinary
// approval and the host-escape confirmation into one one-shot prompt.
// host must already be prepared when forceHost is true so the modal displays
// the pinned absolute executable before the user answers.
// blocked=true means a soft result is ready and the shell must not start.
func authorizeShell(ctx context.Context, defaults ShellOptions, command, cwd string, forceHost bool, justification string, host hostEscalationCommand, approvalMode ApprovalMode, impact ToolImpact) (ShellOutput, bool, error) {
	ruleKey := RuleKey("shell", command, defaults.WorkspaceRoot)

	if forceHost && host.program == "" {
		if approvalMode == ApprovalPlan {
			return softDeny(command, cwd, DecisionDeny, ReasonPlanReadOnly, false), true, nil
		}
		return finishDeny(defaults, command, cwd, DecisionDeny, ReasonPolicyDenied+": host escalation command is required", ruleKey, true), true, nil
	}

	if (!forceHost || isYoloApprovalMode(approvalMode)) && defaults.WorkspaceOnly && !PathWithinWorkspace(defaults.WorkspaceRoot, cwd) {
		reason := fmt.Sprintf("%s: working_dir is outside workspace root %s", ReasonWorkspaceOnly, defaults.WorkspaceRoot)
		if approvalMode == ApprovalPlan {
			return softDeny(command, cwd, DecisionDeny, reason, false), true, nil
		}
		return finishDeny(defaults, command, cwd, DecisionDeny, reason, ruleKey, false), true, nil
	}

	// Prior user deny for this rule_key: soft-deny without re-prompting.
	if !forceHost && !isYoloApprovalMode(approvalMode) && defaults.SessionDenies != nil && defaults.SessionDenies.Contains(ruleKey) {
		reason := fmt.Sprintf("%s: %s; %s", ReasonUserDeniedSession, ruleKey, ReasonUserDeniedNoRetry)
		if approvalMode == ApprovalPlan {
			return softDeny(command, cwd, DecisionDeny, reason, false), true, nil
		}
		return finishDeny(defaults, command, cwd, DecisionDeny, reason, ruleKey, true), true, nil
	}

	ev := defaults.Rules.EvaluateShell(command)
	// An evaluated deny is not bypassed by yolo. The string-based command checks
	// in that evaluation are defense-in-depth, not a complete host boundary.
	if ev.Decision == DecisionDeny {
		reason := fmt.Sprintf("%s: %s", ReasonPolicyDenied, ev.Reason)
		if approvalMode == ApprovalPlan {
			return softDeny(command, cwd, DecisionDeny, reason, false), true, nil
		}
		return finishDeny(defaults, command, cwd, DecisionDeny, reason, ruleKey, false), true, nil
	}
	if approvalMode == ApprovalPlan {
		if forceHost || ev.Decision != DecisionAllow || impact != ToolImpactReadOnly {
			return softDeny(command, cwd, DecisionDeny, ReasonPlanReadOnly, true), true, nil
		}
		if defaults.Sandbox == nil {
			return softDeny(command, cwd, DecisionDeny, ReasonSandboxUnavailable+": plan mode requires an enforced read-only sandbox", true), true, nil
		}
		return ShellOutput{}, false, nil
	}
	// Yolo is an explicit process-local bypass of ordinary approval. Evaluated
	// rule denies and tool path validation above still run, but the remaining
	// string checks are not a complete host security boundary.
	if isYoloApprovalMode(approvalMode) {
		if defaults.DenyStreaks != nil {
			defaults.DenyStreaks.Reset(ruleKey)
		}
		return ShellOutput{}, false, nil
	}

	// Session allow upgrades ordinary approval requests only; it never changes a
	// Codex prompt rule or an opaque shell command into a blanket allow.
	if !forceHost && ev.Decision == DecisionAsk && ev.RuleID != "opaque-shell" && !ev.PolicyPrompt &&
		defaults.SessionAllows != nil && defaults.SessionAllows.Contains(ruleKey) {
		ev.Decision = DecisionAllow
		ev.Reason = "session allow " + ruleKey
		ev.RuleID = "session-allow"
	}
	if forceHost && ev.Decision != DecisionDeny {
		return authorizeHostEscalation(ctx, defaults, command, cwd, ruleKey, justification, host)
	}

	switch ev.Decision {
	case DecisionDeny:
		reason := fmt.Sprintf("%s: %s", ReasonPolicyDenied, ev.Reason)
		return finishDeny(defaults, command, cwd, DecisionDeny, reason, ruleKey, false), true, nil
	case DecisionAllow:
		if defaults.DenyStreaks != nil {
			defaults.DenyStreaks.Reset(ruleKey)
		}
		return ShellOutput{}, false, nil
	case DecisionAsk:
		if mode := effectiveApprovalMode(defaults.Approval, defaults.ApprovalState); isYoloApprovalMode(mode) {
			if defaults.DenyStreaks != nil {
				defaults.DenyStreaks.Reset(ruleKey)
			}
			return ShellOutput{}, false, nil
		}
		if mode := effectiveApprovalMode(defaults.Approval, defaults.ApprovalState); mode == ApprovalNever && ev.PolicyPrompt {
			return finishDeny(defaults, command, cwd, DecisionDeny, "approval required by Codex rule, but approval_policy is never", ruleKey, false), true, nil
		}
		if mode := effectiveApprovalMode(defaults.Approval, defaults.ApprovalState); mode == ApprovalNever {
			if defaults.DenyStreaks != nil {
				defaults.DenyStreaks.Reset(ruleKey)
			}
			return ShellOutput{}, false, nil
		}
		if defaults.Approver == nil {
			return finishDeny(defaults, command, cwd, DecisionDeny, ReasonApproverMissing, ruleKey, false), true, nil
		}
		resp, err := defaults.Approver.Request(ctx, ApprovalRequest{
			Tool:         "shell",
			Command:      command,
			WorkingDir:   cwd,
			Reason:       ev.Reason,
			RuleID:       ev.RuleID,
			RuleKey:      ruleKey,
			AllowSession: true,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				return finishDeny(defaults, command, cwd, DecisionDeny, ReasonApprovalCancelled, ruleKey, false), true, nil
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return finishDeny(defaults, command, cwd, DecisionDeny, ReasonApprovalTimedOut, ruleKey, false), true, nil
			}
			return ShellOutput{}, false, fmt.Errorf("approval: %w", err)
		}
		switch resp.Action {
		case ApprovalOnce:
			if defaults.SessionDenies != nil {
				defaults.SessionDenies.Clear(ruleKey)
			}
			if defaults.DenyStreaks != nil {
				defaults.DenyStreaks.Reset(ruleKey)
			}
			return ShellOutput{}, false, nil
		case ApprovalSession:
			if defaults.SessionDenies != nil {
				defaults.SessionDenies.Clear(ruleKey)
			}
			if defaults.SessionAllows != nil {
				defaults.SessionAllows.Allow(ruleKey)
			}
			if defaults.DenyStreaks != nil {
				defaults.DenyStreaks.Reset(ruleKey)
			}
			return ShellOutput{}, false, nil
		case ApprovalDeny, "":
			reason := ReasonUserDenied
			if resp.Reason != "" {
				reason = resp.Reason
			}
			// Explicit human reject (empty reason is normalized to user_denied by TUI).
			// Timeout / cancel / not-ready are not "user rejected the action".
			if isExplicitUserDeny(reason) {
				if reason == ReasonUserDenied {
					reason = fmt.Sprintf("%s: %s", ReasonUserDenied, ev.Reason)
				}
				if defaults.SessionDenies != nil {
					defaults.SessionDenies.Deny(ruleKey)
				}
				reason = reason + "; " + ReasonUserDeniedNoRetry
				return finishDeny(defaults, command, cwd, DecisionDeny, reason, ruleKey, true), true, nil
			}
			return finishDeny(defaults, command, cwd, DecisionDeny, reason, ruleKey, false), true, nil
		default:
			return finishDeny(defaults, command, cwd, DecisionDeny, ReasonUnknownApproval, ruleKey, false), true, nil
		}
	default:
		return finishDeny(defaults, command, cwd, DecisionDeny, "unknown policy decision", ruleKey, false), true, nil
	}
}

type hostEscalationCommand struct {
	program string
	args    []string
}

// displayCommand is the exact argv form shown in the approval modal and
// fingerprinted for the human decision. It must match what runHostEscalatedCommand execs.
func (c hostEscalationCommand) displayCommand() string {
	if c.program == "" {
		return ""
	}
	parts := make([]string, 0, 1+len(c.args))
	parts = append(parts, c.program)
	parts = append(parts, c.args...)
	return strings.Join(parts, " ")
}

// prepareHostEscalationCommand resolves the executable before approval and
// pins the absolute target that will be exec'd after approval. A workspace
// executable is model-writable and therefore never eligible for a host escape.
func prepareHostEscalationCommand(command, cwd, workspaceRoot string) (hostEscalationCommand, Evaluation) {
	argv, evaluation := hostEscalationArgv(command)
	if evaluation.Decision == DecisionDeny {
		return hostEscalationCommand{}, evaluation
	}
	program, err := resolveHostEscalationExecutable(argv[0], cwd)
	if err != nil {
		return hostEscalationCommand{}, Evaluation{
			Decision: DecisionDeny,
			RuleID:   "host-escalation-executable",
			Reason:   "resolve host escalation executable: " + err.Error(),
		}
	}
	if PathWithinWorkspace(workspaceRoot, program) {
		return hostEscalationCommand{}, Evaluation{
			Decision: DecisionDeny,
			RuleID:   "host-escalation-executable",
			Reason:   "host escalation executable must be outside the workspace",
		}
	}
	return hostEscalationCommand{program: program, args: append([]string(nil), argv[1:]...)}, Evaluation{}
}

func resolveHostEscalationExecutable(raw, cwd string) (string, error) {
	program := strings.TrimSpace(raw)
	if program == "" {
		return "", errors.New("executable is required")
	}
	if !filepath.IsAbs(program) && !strings.ContainsRune(program, filepath.Separator) {
		resolved, err := exec.LookPath(program)
		if err != nil {
			return "", err
		}
		program = resolved
	} else if !filepath.IsAbs(program) {
		program = filepath.Join(cwd, program)
	}
	abs, err := filepath.Abs(program)
	if err != nil {
		return "", err
	}
	return sandbox.ResolveExecutablePath(abs)
}

func hostEscalationArgv(command string) ([]string, Evaluation) {
	if HasShellMetacharacters(command) || strings.ContainsAny(command, "\\'\"{}!~") {
		return nil, Evaluation{
			Decision: DecisionDeny,
			RuleID:   "host-escalation-literal-argv",
			Reason:   "host escalation requires a literal command without shell syntax",
		}
	}

	argv := strings.Fields(command)
	if len(argv) == 0 {
		return nil, Evaluation{
			Decision: DecisionDeny,
			RuleID:   "host-escalation-literal-argv",
			Reason:   "host escalation requires a literal command without shell syntax",
		}
	}
	for _, token := range argv {
		if isShellAssignmentWord(token) {
			return nil, Evaluation{
				Decision: DecisionDeny,
				RuleID:   "host-escalation-literal-argv",
				Reason:   "host escalation does not permit shell environment assignments",
			}
		}
		name := strings.ToLower(filepath.Base(token))
		switch name {
		case "sudo", "doas", "su":
			return nil, Evaluation{
				Decision: DecisionDeny,
				RuleID:   "deny-privilege-escalation",
				Reason:   "policy deny-privilege-escalation (deny)",
			}
		case "setsid", "nohup":
			return nil, Evaluation{
				Decision: DecisionDeny,
				RuleID:   "deny-detached-process",
				Reason:   "policy deny-detached-process (deny)",
			}
		}
	}
	return argv, Evaluation{}
}

func isShellAssignmentWord(token string) bool {
	name, _, ok := strings.Cut(token, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

func authorizeHostEscalation(ctx context.Context, defaults ShellOptions, command, cwd, ruleKey, justification string, host hostEscalationCommand) (ShellOutput, bool, error) {
	if defaults.Approver == nil {
		return finishDeny(defaults, command, cwd, DecisionDeny, ReasonApproverMissing, ruleKey, true), true, nil
	}
	// Show the pinned absolute argv, not the model-supplied token form, so the
	// human decision covers the exact host executable that will run.
	display := host.displayCommand()
	if display == "" {
		return finishDeny(defaults, command, cwd, DecisionDeny, ReasonPolicyDenied+": host escalation command is required", ruleKey, true), true, nil
	}
	resp, err := defaults.Approver.Request(ctx, ApprovalRequest{
		Tool:         "shell",
		Command:      display,
		WorkingDir:   cwd,
		Reason:       strings.TrimSpace(justification),
		RuleID:       "sandbox-escalation",
		RuleKey:      ruleKey,
		Escalated:    true,
		AllowSession: false,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return finishDeny(defaults, command, cwd, DecisionDeny, ReasonApprovalCancelled, ruleKey, true), true, nil
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return finishDeny(defaults, command, cwd, DecisionDeny, ReasonApprovalTimedOut, ruleKey, true), true, nil
		}
		return ShellOutput{}, false, fmt.Errorf("host escalation approval: %w", err)
	}
	switch resp.Action {
	case ApprovalOnce, ApprovalSession:
		// Treat an incorrectly returned session action as once. The TUI never
		// offers it for a host escalation and no key is persisted.
		if defaults.DenyStreaks != nil {
			defaults.DenyStreaks.Reset(ruleKey)
		}
		return ShellOutput{}, false, nil
	case ApprovalDeny, "":
		reason := ReasonHostEscalationDenied
		if resp.Reason != "" && !isExplicitUserDeny(resp.Reason) {
			reason = resp.Reason
		}
		return finishDeny(defaults, command, cwd, DecisionDeny, reason+"; "+ReasonUserDeniedNoRetry, ruleKey, true), true, nil
	default:
		return finishDeny(defaults, command, cwd, DecisionDeny, ReasonUnknownApproval, ruleKey, true), true, nil
	}
}

// finishDeny builds a soft deny. forceStop always sets stop_retrying (user reject);
// otherwise DenyStreak may set it after repeated same-prefix denials.
func finishDeny(defaults ShellOptions, command, cwd string, decision Decision, reason, ruleKey string, forceStop bool) ShellOutput {
	stop := forceStop
	if defaults.DenyStreaks != nil {
		_, streakStop := defaults.DenyStreaks.RecordDeny(ruleKey)
		if streakStop {
			stop = true
			if !strings.Contains(reason, "stop_retrying") {
				reason = reason + "; " + ReasonStopRetryingSuffix
			}
		}
	}
	return softDeny(command, cwd, decision, reason, stop)
}

// isExplicitUserDeny reports a deliberate human reject (not timeout/cancel/not-ready).
func isExplicitUserDeny(reason string) bool {
	reason = strings.TrimSpace(reason)
	if reason == "" || reason == ReasonUserDenied {
		return true
	}
	return strings.HasPrefix(reason, ReasonUserDenied+":")
}

func softDeny(command, cwd string, decision Decision, reason string, stopRetrying bool) ShellOutput {
	stderr := "denied: " + reason
	return ShellOutput{
		Command:      command,
		WorkingDir:   cwd,
		ExitCode:     commandExitCodeUnavailable,
		Denied:       true,
		Decision:     string(decision),
		Reason:       reason,
		StopRetrying: stopRetrying,
		Stderr:       stderr,
	}
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
// It is safe for stdout and stderr copy goroutines to write concurrently.
type limitedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	total     int
	truncated bool
	limited   bool
	onLimit   func()
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) setLimitHandler(handler func()) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.onLimit = handler
	limited := b.limited
	b.mu.Unlock()
	if limited && handler != nil {
		handler()
	}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}
	var handler func()
	b.mu.Lock()
	b.total += len(p)
	if b.limit <= 0 {
		if len(p) > 0 {
			b.truncated = true
			handler = b.markLimitedLocked()
		}
	} else {
		remaining := b.limit - b.buf.Len()
		switch {
		case remaining <= 0:
			b.truncated = true
			handler = b.markLimitedLocked()
		case len(p) >= remaining:
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
			handler = b.markLimitedLocked()
		default:
			_, _ = b.buf.Write(p)
		}
	}
	b.mu.Unlock()
	if handler != nil {
		handler()
	}
	return len(p), nil
}

func (b *limitedBuffer) markLimitedLocked() func() {
	if b.limited {
		return nil
	}
	b.limited = true
	return b.onLimit
}

func (b *limitedBuffer) String() string {
	if b == nil {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *limitedBuffer) Total() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

func (b *limitedBuffer) Truncated() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

// LimitReached reports whether output reached the cap and triggered command
// termination.
func (b *limitedBuffer) LimitReached() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limited
}
