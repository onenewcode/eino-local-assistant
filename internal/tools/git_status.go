package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// GitStatusInput selects a read-only status view.
type GitStatusInput struct {
	Path string `json:"path,omitempty" jsonschema:"description=Optional workspace-relative path prefix to inspect."`
}

// GitStatusEntry is one porcelain-v1 status record.
type GitStatusEntry struct {
	Path         string `json:"path"`
	OriginalPath string `json:"original_path,omitempty"`
	Index        string `json:"index"`
	Worktree     string `json:"worktree"`
	Untracked    bool   `json:"untracked"`
	Renamed      bool   `json:"renamed"`
	Conflicted   bool   `json:"conflicted"`
}

// GitStatusOutput is structured so the model need not parse terminal output.
type GitStatusOutput struct {
	Entries []GitStatusEntry `json:"entries"`
}

// NewGitStatus creates a read-only workspace-scoped status tool.
func NewGitStatus(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"git_status",
		"Show structured, read-only git status for the workspace. Index and worktree states use porcelain-v1 codes; this tool never modifies the repository.",
		func(ctx context.Context, input GitStatusInput) (GitStatusOutput, error) {
			return gitStatus(ctx, root, input)
		},
	)
}

func gitStatus(ctx context.Context, root string, input GitStatusInput) (GitStatusOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return GitStatusOutput{}, err
	}
	args := []string{"-C", root, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--"}
	if path := strings.TrimSpace(input.Path); path != "" {
		resolved, err := safeEditPath(root, path)
		if err != nil {
			return GitStatusOutput{}, err
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil {
			return GitStatusOutput{}, fmt.Errorf("resolve status path: %w", err)
		}
		args = append(args, filepath.FromSlash(filepath.ToSlash(rel)))
	}
	output, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return GitStatusOutput{}, fmt.Errorf("git status: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return GitStatusOutput{}, fmt.Errorf("run git status: %w", err)
	}
	return GitStatusOutput{Entries: parseGitStatus(output)}, nil
}

func parseGitStatus(data []byte) []GitStatusEntry {
	entries := make([]GitStatusEntry, 0)
	for offset := 0; offset < len(data); {
		end := bytes.IndexByte(data[offset:], 0)
		if end < 0 {
			end = len(data) - offset
		}
		record := data[offset : offset+end]
		offset += end + 1
		if len(record) < 4 {
			continue
		}
		index, worktree := record[0], record[1]
		entry := GitStatusEntry{Index: string(index), Worktree: string(worktree)}
		path := string(record[3:])
		entry.Untracked = index == '?' && worktree == '?'
		entry.Renamed = index == 'R' || index == 'C' || worktree == 'R' || worktree == 'C'
		entry.Conflicted = gitStatusConflict(index, worktree)
		if entry.Renamed && offset <= len(data) {
			end = bytes.IndexByte(data[offset:], 0)
			if end < 0 {
				end = len(data) - offset
			}
			entry.OriginalPath = filepath.ToSlash(string(data[offset : offset+end]))
			offset += end + 1
		}
		entry.Path = filepath.ToSlash(path)
		entries = append(entries, entry)
	}
	return entries
}

func gitStatusConflict(index, worktree byte) bool {
	if index == 'U' || worktree == 'U' {
		return true
	}
	switch string([]byte{index, worktree}) {
	case "DD", "AU", "UD", "UA", "DU", "AA", "UU":
		return true
	default:
		return false
	}
}
