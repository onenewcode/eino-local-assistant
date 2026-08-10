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
	defaultGitLogCommits = 20
	maxGitLogCommits     = 100
)

// GitLogInput selects a bounded, read-only history query.
type GitLogInput struct {
	Path       string `json:"path,omitempty" jsonschema:"description=Optional workspace-relative path to follow."`
	MaxCommits int    `json:"max_commits,omitempty" jsonschema:"description=Maximum commits to return (default 20, maximum 100)."`
}

// GitLogCommit is a compact commit record suitable for model context.
type GitLogCommit struct {
	Hash    string `json:"hash"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Subject string `json:"subject"`
}

// GitLogOutput reports bounded history results.
type GitLogOutput struct {
	Path      string         `json:"path,omitempty"`
	Commits   []GitLogCommit `json:"commits"`
	Truncated bool           `json:"truncated"`
}

// NewGitLog creates a read-only workspace-scoped Git history tool.
func NewGitLog(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"git_log",
		"Show bounded, read-only Git commit history for the workspace or one relative path. Use it to understand recent changes and project history; it never modifies the repository.",
		func(ctx context.Context, input GitLogInput) (GitLogOutput, error) {
			return gitLog(ctx, root, input)
		},
	)
}

func gitLog(ctx context.Context, root string, input GitLogInput) (GitLogOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return GitLogOutput{}, err
	}
	limit := input.MaxCommits
	if limit == 0 {
		limit = defaultGitLogCommits
	}
	if limit < 1 || limit > maxGitLogCommits {
		return GitLogOutput{}, fmt.Errorf("max_commits must be between 1 and %d", maxGitLogCommits)
	}
	relPath := ""
	if path := strings.TrimSpace(input.Path); path != "" {
		resolved, err := safeEditPath(root, path)
		if err != nil {
			return GitLogOutput{}, err
		}
		relPath, err = filepath.Rel(root, resolved)
		if err != nil {
			return GitLogOutput{}, fmt.Errorf("resolve log path: %w", err)
		}
		relPath = filepath.ToSlash(relPath)
	}
	args := []string{"-C", root, "log", "--no-decorate", "--format=%H%x1f%an%x1f%aI%x1f%s", fmt.Sprintf("--max-count=%d", limit+1), "--"}
	if relPath != "" {
		args = append(args, filepath.FromSlash(relPath))
	}
	raw, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return GitLogOutput{}, fmt.Errorf("git log: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return GitLogOutput{}, fmt.Errorf("run git log: %w", err)
	}
	commits := parseGitLog(raw)
	truncated := len(commits) > limit
	if truncated {
		commits = commits[:limit]
	}
	return GitLogOutput{Path: relPath, Commits: commits, Truncated: truncated}, nil
}

func parseGitLog(raw []byte) []GitLogCommit {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	commits := make([]GitLogCommit, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\x1f", 4)
		if len(fields) != 4 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		commits = append(commits, GitLogCommit{Hash: fields[0], Author: fields[1], Date: fields[2], Subject: fields[3]})
	}
	return commits
}
