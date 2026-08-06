package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	defaultPatchMaxBytes = 256 << 10
	maxPatchMaxBytes     = 1 << 20
	maxPatchTotalBytes   = 1 << 20

	patchCreate = "create_file"
	patchUpdate = "update_file"
	patchDelete = "delete_file"

	// applyPatchToolDescription follows Codex wording: use apply_patch only (not applypatch).
	applyPatchToolDescription = `Use the apply_patch tool to edit files (NEVER try applypatch or apply-patch, only apply_patch).

Apply structured file changes inside the workspace (Codex apply_patch subset).

Operations (JSON):
- create_file: path + content (fails if the file already exists)
- update_file: path + old_string + new_string (exact replace; unique match unless replace_all is true)
- delete_file: path

Guidelines:
- Prefer apply_patch over shell for creating, editing, or deleting source files.
- Keep edits small and targeted when possible (update_file with a unique old_string).
- Paths must stay inside the workspace. Host approval may be required.
- If denied=true with user_denied, do not bypass via shell; stop and ask the user.`
)

// ApplyPatchOptions configures the Codex-style apply_patch tool.
type ApplyPatchOptions struct {
	Disabled      bool
	WorkspaceRoot string
	// MaxBytes caps create content and post-update file size. Zero uses 256KiB.
	MaxBytes int
	Approval ApprovalMode
	// ApprovalState, when set, supplies the current mode for each invocation.
	// It is shared with shell by the production registry.
	ApprovalState *ApprovalState
	Approver      Approver
	Permissions   *PermissionSet
	SessionAllows *SessionAllowlist
	SessionDenies *SessionDenylist
	// Sandbox executes approved mutations in a one-shot strict worker. Nil is
	// retained for focused unit tests and non-product callers.
	Sandbox *SandboxRunner
}

// ApplyPatchInput is one or more structured file operations (Codex apply_patch subset).
type ApplyPatchInput struct {
	Operations []PatchOperation `json:"operations" jsonschema:"description=Ordered file operations to apply atomically-ish (each op is validated then applied in order)."`
}

// PatchOperation is a single create/update/delete.
type PatchOperation struct {
	// Type is create_file | update_file | delete_file.
	Type string `json:"type" jsonschema:"description=Operation type: create_file, update_file, or delete_file."`
	// Path is workspace-relative or absolute under the workspace root.
	Path string `json:"path" jsonschema:"description=Target file path inside the workspace."`
	// Content is the full file body for create_file.
	Content string `json:"content,omitempty" jsonschema:"description=Full UTF-8 contents for create_file."`
	// OldString / NewString are used for update_file exact replacement.
	OldString  string `json:"old_string,omitempty" jsonschema:"description=Exact text to find for update_file."`
	NewString  string `json:"new_string,omitempty" jsonschema:"description=Replacement text for update_file."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"description=If true, replace every old_string occurrence on update_file."`
}

// ApplyPatchOutput reports results or a soft denial.
type ApplyPatchOutput struct {
	Results      []PatchOpResult `json:"results,omitempty"`
	Denied       bool            `json:"denied,omitempty"`
	Decision     string          `json:"decision,omitempty"`
	Reason       string          `json:"reason,omitempty"`
	StopRetrying bool            `json:"stop_retrying,omitempty"`
	Sandbox      *SandboxOutcome `json:"sandbox,omitempty"`
}

// PatchOpResult is one applied operation.
type PatchOpResult struct {
	Type         string `json:"type"`
	Path         string `json:"path"`
	Created      bool   `json:"created,omitempty"`
	Deleted      bool   `json:"deleted,omitempty"`
	Replacements int    `json:"replacements,omitempty"`
	Bytes        int    `json:"bytes,omitempty"`
}

// NewApplyPatch builds the apply_patch tool (Codex subset: create/update/delete).
func NewApplyPatch(opts ApplyPatchOptions) (tool.InvokableTool, error) {
	defaults, err := normalizeApplyPatchOptions(opts)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"apply_patch",
		applyPatchToolDescription,
		func(ctx context.Context, input ApplyPatchInput) (ApplyPatchOutput, error) {
			return applyPatch(ctx, defaults, input)
		},
	)
}

func normalizeApplyPatchOptions(opts ApplyPatchOptions) (ApplyPatchOptions, error) {
	root := strings.TrimSpace(opts.WorkspaceRoot)
	if root == "" {
		resolved, err := ResolveWorkspaceRoot("")
		if err != nil {
			return ApplyPatchOptions{}, err
		}
		root = resolved
	} else {
		resolved, err := ResolveWorkspaceRoot(root)
		if err != nil {
			return ApplyPatchOptions{}, err
		}
		root = resolved
	}
	opts.WorkspaceRoot = root
	if opts.MaxBytes < 0 {
		return ApplyPatchOptions{}, errors.New("max_bytes must be >= 0")
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = defaultPatchMaxBytes
	}
	if opts.MaxBytes > maxPatchMaxBytes {
		return ApplyPatchOptions{}, fmt.Errorf("max_bytes must be <= %d", maxPatchMaxBytes)
	}
	opts.Approval = NormalizeApprovalMode(string(opts.Approval))
	return opts, nil
}

func applyPatch(ctx context.Context, opts ApplyPatchOptions, input ApplyPatchInput) (ApplyPatchOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Plan is a hard phase gate: reject before validation, file reads, approval,
	// or sandbox worker startup. The OS read-only sandbox is an additional
	// defense for shell, but apply_patch never enters it in this phase.
	if effectiveApprovalMode(opts.Approval, opts.ApprovalState) == ApprovalPlan {
		return ApplyPatchOutput{
			Denied:   true,
			Decision: string(DecisionDeny),
			Reason:   ReasonPlanReadOnly,
		}, nil
	}
	if len(input.Operations) == 0 {
		return ApplyPatchOutput{}, errors.New("operations is required")
	}
	if len(input.Operations) > 32 {
		return ApplyPatchOutput{}, errors.New("at most 32 operations per apply_patch call")
	}
	if opts.Sandbox != nil {
		return applyPatchSandboxed(ctx, opts, input)
	}

	// Validate ops in order, tracking staged content for same-batch create→update.
	type prepared struct {
		op   PatchOperation
		abs  string
		typ  string
		body string // final content for create/update
		reps int
	}
	steps := make([]prepared, 0, len(input.Operations))
	paths := make([]string, 0, len(input.Operations))
	totalBytes := 0
	staged := map[string]string{} // abs path → content after prior ops in this batch
	deleted := map[string]bool{}

	for i, op := range input.Operations {
		typ := strings.ToLower(strings.TrimSpace(op.Type))
		switch typ {
		case patchCreate, patchUpdate, patchDelete:
		default:
			return ApplyPatchOutput{}, fmt.Errorf("operations[%d]: type must be create_file, update_file, or delete_file", i)
		}
		abs, err := resolveWorkspacePath(opts.WorkspaceRoot, op.Path)
		if err != nil {
			return ApplyPatchOutput{
				Denied:   true,
				Decision: string(DecisionDeny),
				Reason:   err.Error(),
			}, nil
		}
		p := prepared{op: op, abs: abs, typ: typ}
		switch typ {
		case patchCreate:
			if op.OldString != "" {
				return ApplyPatchOutput{}, fmt.Errorf("operations[%d]: create_file uses content, not old_string", i)
			}
			if len(op.Content) > opts.MaxBytes {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: content exceeds max_bytes (%d)", i, opts.MaxBytes),
				}, nil
			}
			if !utf8.ValidString(op.Content) {
				return ApplyPatchOutput{}, fmt.Errorf("operations[%d]: content must be valid UTF-8", i)
			}
			if deleted[abs] {
				delete(deleted, abs)
			} else if _, ok := staged[abs]; ok {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: file already created in this patch", i),
				}, nil
			} else if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: file already exists (use update_file)", i),
				}, nil
			} else if err == nil && st.IsDir() {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: path is a directory", i),
				}, nil
			}
			p.body = op.Content
			staged[abs] = p.body
			totalBytes += len(op.Content)
		case patchUpdate:
			if op.OldString == "" {
				return ApplyPatchOutput{}, fmt.Errorf("operations[%d]: old_string is required for update_file", i)
			}
			if op.OldString == op.NewString {
				return ApplyPatchOutput{}, fmt.Errorf("operations[%d]: old_string and new_string are identical", i)
			}
			var original string
			if deleted[abs] {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: file deleted earlier in this patch", i),
				}, nil
			}
			if body, ok := staged[abs]; ok {
				original = body
			} else {
				data, err := os.ReadFile(abs)
				if err != nil {
					if os.IsNotExist(err) {
						return ApplyPatchOutput{
							Denied: true, Decision: string(DecisionDeny),
							Reason: fmt.Sprintf("operations[%d]: file not found (use create_file)", i),
						}, nil
					}
					return ApplyPatchOutput{}, fmt.Errorf("operations[%d]: read: %w", i, err)
				}
				if !utf8.Valid(data) {
					return ApplyPatchOutput{
						Denied: true, Decision: string(DecisionDeny),
						Reason: fmt.Sprintf("operations[%d]: file is not valid UTF-8", i),
					}, nil
				}
				original = string(data)
			}
			count := strings.Count(original, op.OldString)
			if count == 0 {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: old_string not found", i),
				}, nil
			}
			if !op.ReplaceAll && count > 1 {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: old_string matched %d times; set replace_all=true or use a unique string", i, count),
				}, nil
			}
			if op.ReplaceAll {
				p.body = strings.ReplaceAll(original, op.OldString, op.NewString)
				p.reps = count
			} else {
				p.body = strings.Replace(original, op.OldString, op.NewString, 1)
				p.reps = 1
			}
			if len(p.body) > opts.MaxBytes {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: result exceeds max_bytes (%d)", i, opts.MaxBytes),
				}, nil
			}
			staged[abs] = p.body
			totalBytes += len(p.body)
		case patchDelete:
			if deleted[abs] {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: already deleted in this patch", i),
				}, nil
			}
			if _, ok := staged[abs]; ok {
				delete(staged, abs)
			} else if st, err := os.Stat(abs); err != nil {
				if os.IsNotExist(err) {
					return ApplyPatchOutput{
						Denied: true, Decision: string(DecisionDeny),
						Reason: fmt.Sprintf("operations[%d]: file not found", i),
					}, nil
				}
				return ApplyPatchOutput{}, fmt.Errorf("operations[%d]: stat: %w", i, err)
			} else if st.IsDir() {
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("operations[%d]: path is a directory", i),
				}, nil
			}
			deleted[abs] = true
		}
		steps = append(steps, p)
		paths = append(paths, abs)
	}

	// Permission + approval for the whole patch (strictest path decision).
	if out, blocked := authorizeApplyPatch(ctx, opts, paths, totalBytes); blocked {
		return out, nil
	}

	results := make([]PatchOpResult, 0, len(steps))
	for _, p := range steps {
		switch p.typ {
		case patchCreate:
			if err := atomicWriteFile(opts.WorkspaceRoot, p.abs, p.body); err != nil {
				return ApplyPatchOutput{}, err
			}
			results = append(results, PatchOpResult{Type: p.typ, Path: p.abs, Created: true, Bytes: len(p.body)})
		case patchUpdate:
			if err := atomicWriteFile(opts.WorkspaceRoot, p.abs, p.body); err != nil {
				return ApplyPatchOutput{}, err
			}
			results = append(results, PatchOpResult{Type: p.typ, Path: p.abs, Replacements: p.reps, Bytes: len(p.body)})
		case patchDelete:
			if err := removeWorkspaceFile(opts.WorkspaceRoot, p.abs); err != nil {
				return ApplyPatchOutput{}, fmt.Errorf("delete %s: %w", p.abs, err)
			}
			results = append(results, PatchOpResult{Type: p.typ, Path: p.abs, Deleted: true})
		}
	}
	return ApplyPatchOutput{Results: results, Decision: string(DecisionAllow)}, nil
}

// applyPatchSandboxed performs only metadata validation in the parent, then
// hands all file reads and mutations to the strict worker. This avoids leaking
// workspace content through the unsandboxed TUI/model process before the OS
// boundary has been installed.
func applyPatchSandboxed(ctx context.Context, opts ApplyPatchOptions, input ApplyPatchInput) (ApplyPatchOutput, error) {
	paths, totalBytes, out, err := preflightSandboxPatch(opts, input)
	if err != nil || out != nil {
		if out != nil {
			return *out, nil
		}
		return ApplyPatchOutput{}, err
	}
	if denied, blocked := authorizeApplyPatch(ctx, opts, paths, totalBytes); blocked {
		return denied, nil
	}

	response, outcome, err := opts.Sandbox.Execute(ctx, SandboxWorkerRequest{
		Kind:          sandboxWorkerPatch,
		Operations:    input.Operations,
		PatchMaxBytes: opts.MaxBytes,
	})
	if err != nil {
		return ApplyPatchOutput{
			Denied:       true,
			Decision:     string(DecisionDeny),
			Reason:       ReasonSandboxUnavailable + ": " + err.Error(),
			StopRetrying: true,
			Sandbox:      &outcome,
		}, nil
	}
	if response.Error != "" {
		return ApplyPatchOutput{
			Denied:       true,
			Decision:     string(DecisionDeny),
			Reason:       "sandbox_worker_error: " + response.Error,
			StopRetrying: true,
			Sandbox:      &outcome,
		}, nil
	}
	if response.Patch == nil {
		return ApplyPatchOutput{
			Denied:       true,
			Decision:     string(DecisionDeny),
			Reason:       "sandbox_worker_error: missing patch result",
			StopRetrying: true,
			Sandbox:      &outcome,
		}, nil
	}
	result := *response.Patch
	result.Sandbox = &outcome
	return result, nil
}

func preflightSandboxPatch(opts ApplyPatchOptions, input ApplyPatchInput) ([]string, int, *ApplyPatchOutput, error) {
	paths := make([]string, 0, len(input.Operations))
	totalBytes := 0
	for i, op := range input.Operations {
		typ := strings.ToLower(strings.TrimSpace(op.Type))
		switch typ {
		case patchCreate, patchUpdate, patchDelete:
		default:
			return nil, 0, nil, fmt.Errorf("operations[%d]: type must be create_file, update_file, or delete_file", i)
		}
		abs, err := resolveWorkspacePath(opts.WorkspaceRoot, op.Path)
		if err != nil {
			denied := ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: err.Error(), StopRetrying: true}
			return nil, 0, &denied, nil
		}
		if len(op.Content) > opts.MaxBytes || len(op.NewString) > opts.MaxBytes {
			denied := ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: fmt.Sprintf("operations[%d]: content exceeds max_bytes (%d)", i, opts.MaxBytes)}
			return nil, 0, &denied, nil
		}
		totalBytes += len(op.Content) + len(op.OldString) + len(op.NewString)
		if totalBytes > maxPatchTotalBytes {
			denied := ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: fmt.Sprintf("apply_patch input exceeds total limit (%d)", maxPatchTotalBytes)}
			return nil, 0, &denied, nil
		}
		paths = append(paths, abs)
	}
	return paths, totalBytes, nil, nil
}

func authorizeApplyPatch(ctx context.Context, opts ApplyPatchOptions, paths []string, totalBytes int) (ApplyPatchOutput, bool) {
	// Permissions: any path deny blocks; all must be allow to skip ask; else ask.
	needAsk := true
	if opts.Permissions != nil {
		allAllow := true
		for _, abs := range paths {
			ev := opts.Permissions.EvaluatePath(PermToolApplyPatch, opts.WorkspaceRoot, abs)
			switch ev.Decision {
			case DecisionDeny:
				return ApplyPatchOutput{
					Denied: true, Decision: string(DecisionDeny),
					Reason: fmt.Sprintf("%s: %s", ReasonPolicyDenied, ev.Reason),
				}, true
			case DecisionAllow:
				// keep scanning
			default:
				allAllow = false
			}
		}
		if allAllow {
			needAsk = false
		}
	}

	// Session deny on any path key.
	for _, abs := range paths {
		key := PathRuleKey("apply_patch", abs, opts.WorkspaceRoot)
		if opts.SessionDenies != nil && opts.SessionDenies.Contains(key) {
			reason := fmt.Sprintf("%s: %s; %s", ReasonUserDeniedSession, key, ReasonUserDeniedNoRetry)
			return ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: reason, StopRetrying: true}, true
		}
	}
	// Session allow only if every path is session-allowed.
	if opts.SessionAllows != nil {
		all := true
		for _, abs := range paths {
			if !opts.SessionAllows.Contains(PathRuleKey("apply_patch", abs, opts.WorkspaceRoot)) {
				all = false
				break
			}
		}
		if all && len(paths) > 0 {
			return ApplyPatchOutput{}, false
		}
	}

	if !needAsk || effectiveApprovalMode(opts.Approval, opts.ApprovalState) == ApprovalNever {
		return ApplyPatchOutput{}, false
	}
	if opts.Approver == nil {
		return ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: ReasonApproverMissing}, true
	}

	summary := fmt.Sprintf("apply_patch %d op(s), ~%d bytes", len(paths), totalBytes)
	if len(paths) == 1 {
		summary = fmt.Sprintf("apply_patch %s", paths[0])
	}
	// Use first path as session key anchor; multi-path patches re-prompt unless each path allowed.
	ruleKey := PathRuleKey("apply_patch", paths[0], opts.WorkspaceRoot)
	if len(paths) > 1 {
		ruleKey = PathRuleKey("apply_patch", "multi:"+paths[0], opts.WorkspaceRoot)
	}

	resp, err := opts.Approver.Request(ctx, ApprovalRequest{
		Tool:         "apply_patch",
		Command:      summary,
		WorkingDir:   opts.WorkspaceRoot,
		Reason:       "apply_patch requires approval",
		RuleID:       "apply_patch",
		RuleKey:      ruleKey,
		AllowSession: true,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: ReasonApprovalCancelled}, true
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: ReasonApprovalTimedOut}, true
		}
		return ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: "approval: " + err.Error()}, true
	}
	switch resp.Action {
	case ApprovalOnce:
		return ApplyPatchOutput{}, false
	case ApprovalSession:
		if opts.SessionAllows != nil {
			for _, abs := range paths {
				opts.SessionAllows.Allow(PathRuleKey("apply_patch", abs, opts.WorkspaceRoot))
			}
		}
		return ApplyPatchOutput{}, false
	case ApprovalDeny, "":
		reason := ReasonUserDenied
		if resp.Reason != "" {
			reason = resp.Reason
		}
		if isExplicitUserDeny(reason) {
			if reason == ReasonUserDenied {
				reason = fmt.Sprintf("%s: apply_patch", ReasonUserDenied)
			}
			if opts.SessionDenies != nil {
				for _, abs := range paths {
					opts.SessionDenies.Deny(PathRuleKey("apply_patch", abs, opts.WorkspaceRoot))
				}
			}
			reason = reason + "; " + ReasonUserDeniedNoRetry
			return ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: reason, StopRetrying: true}, true
		}
		return ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: reason}, true
	default:
		return ApplyPatchOutput{Denied: true, Decision: string(DecisionDeny), Reason: ReasonUnknownApproval}, true
	}
}

func resolveWorkspacePath(workspaceRoot, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", errors.New("path is required")
	}
	var abs string
	if filepath.IsAbs(input) {
		abs = filepath.Clean(input)
	} else {
		abs = filepath.Join(workspaceRoot, input)
	}
	root := canonicalizePath(workspaceRoot)
	abs = filepath.Clean(abs)
	if !PathWithinWorkspace(root, abs) {
		return "", fmt.Errorf("%s: path %q is outside workspace root %s", ReasonWorkspaceOnly, input, workspaceRoot)
	}
	if err := rejectWorkspaceSymlinks(root, abs); err != nil {
		return "", err
	}
	return abs, nil
}

// rejectWorkspaceSymlinks rejects every existing symlink below workspaceRoot.
// EvalSymlinks on the final path alone is insufficient for a new leaf below an
// existing symlinked directory: it fails for the leaf and otherwise hides the
// escape. The OS sandbox remains the race-resistant boundary; this check keeps
// the unsandboxed fallback path safe as well.
func rejectWorkspaceSymlinks(workspaceRoot, abs string) error {
	root := canonicalizePath(workspaceRoot)
	target := filepath.Clean(abs)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%s: path %q is outside workspace root %s", ReasonWorkspaceOnly, abs, workspaceRoot)
	}
	if rel == "." {
		return nil
	}

	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				// No later component can exist without this parent.
				return nil
			}
			return fmt.Errorf("%s: inspect %q: %w", ReasonWorkspaceSymlink, current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: path %q contains symlink %q", ReasonWorkspaceSymlink, abs, current)
		}
	}
	return nil
}

func atomicWriteFile(workspaceRoot, abs, content string) error {
	if err := rejectWorkspaceSymlinks(workspaceRoot, abs); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create parent dirs: %w", err)
	}
	if err := rejectWorkspaceSymlinks(workspaceRoot, abs); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".apply_patch-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := io.WriteString(tmp, content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, abs); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

func removeWorkspaceFile(workspaceRoot, abs string) error {
	if err := rejectWorkspaceSymlinks(workspaceRoot, abs); err != nil {
		return err
	}
	if err := os.Remove(abs); err != nil {
		return fmt.Errorf("delete %s: %w", abs, err)
	}
	return nil
}
