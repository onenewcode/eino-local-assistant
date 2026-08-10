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
	defaultGitShowBytes = 64 << 10
	maxGitShowBytes     = 1 << 20
)

// GitShowInput selects one bounded commit view.
type GitShowInput struct {
	Commit   string `json:"commit" jsonschema:"description=Git commit hash or ref to inspect, such as HEAD or HEAD~1."`
	Path     string `json:"path,omitempty" jsonschema:"description=Optional workspace-relative path within the commit."`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"description=Maximum output bytes (default 65536, maximum 1048576)."`
}

// GitShowOutput reports one bounded commit detail view.
type GitShowOutput struct {
	Commit    string `json:"commit"`
	Path      string `json:"path,omitempty"`
	Content   string `json:"content"`
	Bytes     int    `json:"bytes"`
	Truncated bool   `json:"truncated"`
}

// NewGitShow creates a read-only workspace-scoped commit inspection tool.
func NewGitShow(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"git_show",
		"Show a bounded, read-only Git commit and its patch, optionally limited to one workspace-relative path. It never modifies the repository.",
		func(ctx context.Context, input GitShowInput) (GitShowOutput, error) {
			return gitShow(ctx, root, input)
		},
	)
}

func gitShow(ctx context.Context, root string, input GitShowInput) (GitShowOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return GitShowOutput{}, err
	}
	commit := strings.TrimSpace(input.Commit)
	if commit == "" {
		return GitShowOutput{}, errors.New("commit is required")
	}
	if strings.HasPrefix(commit, "-") || strings.ContainsAny(commit, "\x00\r\n\t ") {
		return GitShowOutput{}, errors.New("commit must be a single Git ref, not an option or whitespace-separated value")
	}
	limit := input.MaxBytes
	if limit == 0 {
		limit = defaultGitShowBytes
	}
	if limit < 1 || limit > maxGitShowBytes {
		return GitShowOutput{}, fmt.Errorf("max_bytes must be between 1 and %d", maxGitShowBytes)
	}
	relPath := ""
	if path := strings.TrimSpace(input.Path); path != "" {
		resolved, err := safeEditPath(root, path)
		if err != nil {
			return GitShowOutput{}, err
		}
		relPath, err = filepath.Rel(root, resolved)
		if err != nil {
			return GitShowOutput{}, fmt.Errorf("resolve show path: %w", err)
		}
		relPath = filepath.ToSlash(relPath)
	}
	args := []string{"-C", root, "show", "--no-ext-diff", "--no-color", "--format=fuller", commit, "--"}
	if relPath != "" {
		args = append(args, filepath.FromSlash(relPath))
	}
	raw, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return GitShowOutput{}, fmt.Errorf("git show: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return GitShowOutput{}, fmt.Errorf("run git show: %w", err)
	}
	truncated := len(raw) > limit
	if truncated {
		raw = raw[:limit]
	}
	return GitShowOutput{Commit: commit, Path: relPath, Content: string(raw), Bytes: len(raw), Truncated: truncated}, nil
}
