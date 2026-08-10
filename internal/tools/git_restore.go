package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// GitRestoreInput describes one explicit restore operation. Path is required
// so the model cannot accidentally discard the entire workspace.
type GitRestoreInput struct {
	Path   string `json:"path" jsonschema:"description=Required workspace-relative file path to restore."`
	Staged bool   `json:"staged,omitempty" jsonschema:"description=Only restore the index entry, leaving the working tree unchanged."`
}

// GitRestoreOutput reports the path restored from Git.
type GitRestoreOutput struct {
	Path    string `json:"path"`
	Staged  bool   `json:"staged"`
	Preview string `json:"preview,omitempty"`
}

// NewGitRestore creates a permission-gated, single-path Git restore tool.
func NewGitRestore(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"git_restore",
		"Restore exactly one workspace-relative path from Git. This can discard working-tree changes or unstage a path; it always requires the current permission policy and never accepts an empty path.",
		func(ctx context.Context, input GitRestoreInput) (GitRestoreOutput, error) {
			return gitRestore(ctx, root, input)
		},
	)
}

func gitRestore(ctx context.Context, root string, input GitRestoreInput) (GitRestoreOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return GitRestoreOutput{}, err
	}
	path := strings.TrimSpace(input.Path)
	if path == "" {
		return GitRestoreOutput{}, errors.New("path is required; refusing to restore the entire workspace")
	}
	resolved, err := safeEditPath(root, path)
	if err != nil {
		return GitRestoreOutput{}, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return GitRestoreOutput{}, fmt.Errorf("resolve restore path: %w", err)
	}
	rel = filepath.ToSlash(rel)
	action := "restore_worktree"
	if input.Staged {
		action = "restore_staged"
	}
	preview, _ := gitRestorePreview(ctx, root, rel, input.Staged)
	if err := RequirePermission(ctx, PermissionRequest{Tool: "git_restore", Action: action, Detail: rel, Preview: preview, Risk: RiskHigh}); err != nil {
		return GitRestoreOutput{}, err
	}
	args := []string{"-C", root, "restore"}
	if input.Staged {
		args = append(args, "--staged")
	} else {
		args = append(args, "--worktree")
	}
	args = append(args, "--", filepath.FromSlash(rel))
	cmd := exec.CommandContext(ctx, "git", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return GitRestoreOutput{}, fmt.Errorf("git restore: %s", strings.TrimSpace(string(output)))
		}
		return GitRestoreOutput{}, fmt.Errorf("run git restore: %w", err)
	}
	return GitRestoreOutput{Path: rel, Staged: input.Staged, Preview: preview}, nil
}

func gitRestorePreview(ctx context.Context, root, path string, staged bool) (string, error) {
	args := []string{"-C", root, "diff", "--no-ext-diff"}
	if staged {
		args = append(args, "--cached")
	}
	args = append(args, "--", filepath.FromSlash(path))
	output, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return "", err
	}
	if len(output) > maxGitRestorePreviewBytes {
		output = output[:maxGitRestorePreviewBytes]
	}
	return string(output), nil
}

const maxGitRestorePreviewBytes = 64 << 10
