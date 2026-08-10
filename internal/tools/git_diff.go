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

const (
	defaultGitDiffBytes = 64 << 10
	maxGitDiffBytes     = 1 << 20
)

// GitDiffInput selects a read-only diff from the configured workspace.
type GitDiffInput struct {
	Path     string `json:"path,omitempty" jsonschema:"description=Optional workspace-relative file path. Empty returns the complete diff."`
	Staged   bool   `json:"staged,omitempty" jsonschema:"description=Show the staged index diff instead of the working tree diff."`
	Base     string `json:"base,omitempty" jsonschema:"description=Optional Git branch or ref. When set, show committed changes from its merge base through HEAD."`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"description=Maximum diff bytes (default 65536, maximum 1048576)."`
}

// GitDiffOutput is a bounded, read-only source review result.
type GitDiffOutput struct {
	Path      string `json:"path,omitempty"`
	Staged    bool   `json:"staged"`
	Base      string `json:"base,omitempty"`
	Diff      string `json:"diff"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

// NewGitDiff creates a git diff tool scoped to one workspace.
func NewGitDiff(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"git_diff",
		"Show a bounded, read-only git diff for the workspace or one relative file. Use this after edits to review exactly what changed; this tool never modifies files.",
		func(ctx context.Context, input GitDiffInput) (GitDiffOutput, error) {
			return gitDiff(ctx, root, input)
		},
	)
}

func gitDiff(ctx context.Context, root string, input GitDiffInput) (GitDiffOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return GitDiffOutput{}, err
	}
	limit := input.MaxBytes
	if limit == 0 {
		limit = defaultGitDiffBytes
	}
	if limit < 1 || limit > maxGitDiffBytes {
		return GitDiffOutput{}, fmt.Errorf("max_bytes must be between 1 and %d", maxGitDiffBytes)
	}
	path := strings.TrimSpace(input.Path)
	base := strings.TrimSpace(input.Base)
	if base != "" {
		if input.Staged {
			return GitDiffOutput{}, errors.New("base and staged cannot be used together")
		}
		if strings.HasPrefix(base, "-") || strings.ContainsAny(base, "\x00\r\n\t ") {
			return GitDiffOutput{}, errors.New("base must be a single Git ref, not an option or whitespace-separated value")
		}
	}
	relPath := ""
	if path != "" {
		resolved, err := safeEditPath(root, path)
		if err != nil {
			return GitDiffOutput{}, err
		}
		relPath, err = filepath.Rel(root, resolved)
		if err != nil {
			return GitDiffOutput{}, fmt.Errorf("resolve diff path: %w", err)
		}
		relPath = filepath.ToSlash(relPath)
	}
	args := []string{"-C", root, "diff", "--no-ext-diff"}
	if base != "" {
		args = append(args, "--merge-base", base, "HEAD")
	} else if input.Staged {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	if relPath != "" {
		args = append(args, filepath.FromSlash(relPath))
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return GitDiffOutput{}, fmt.Errorf("git diff: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return GitDiffOutput{}, fmt.Errorf("run git diff: %w", err)
	}
	truncated := len(output) > limit
	if truncated {
		output = output[:limit]
	}
	return GitDiffOutput{Path: relPath, Staged: input.Staged, Base: base, Diff: string(output), Bytes: len(output), Truncated: truncated}, nil
}
