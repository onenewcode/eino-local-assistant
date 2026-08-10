package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"eino-local-assistant/internal/tools"
)

const (
	maxBackgroundAgentFiles          = 4
	maxBackgroundAgentFileBytes      = 16 * 1024
	maxBackgroundAgentFilesTotalSize = 64 * 1024
)

// backgroundAgentWorkspaceFiles captures explicit, workspace-relative source
// files for a tool-free child. It reuses read_file's regular-file and symlink
// boundary rather than granting the child direct file-system access.
func (r *commandRuntime) backgroundAgentWorkspaceFiles(ctx context.Context, paths []string) (string, error) {
	if r == nil || strings.TrimSpace(r.workspaceRoot) == "" {
		return "", errBackgroundAgentWorkspaceUnavailable
	}
	if len(paths) == 0 || len(paths) > maxBackgroundAgentFiles {
		return "", fmt.Errorf("select between 1 and %d workspace files", maxBackgroundAgentFiles)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reader, err := tools.NewReadFile(tools.EditFileOptions{WorkingDir: r.workspaceRoot})
	if err != nil {
		return "", fmt.Errorf("create workspace file reader: %w", err)
	}

	var snapshot strings.Builder
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if isBackgroundAgentHiddenPath(path) {
			return "", fmt.Errorf("workspace file %q is not available for a background snapshot", path)
		}
		input, err := json.Marshal(tools.ReadFileInput{Path: path, MaxBytes: maxBackgroundAgentFileBytes})
		if err != nil {
			return "", fmt.Errorf("encode workspace file request: %w", err)
		}
		raw, err := reader.InvokableRun(ctx, string(input))
		if err != nil {
			return "", fmt.Errorf("read workspace file %q: %w", path, err)
		}
		var result tools.ReadFileOutput
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return "", fmt.Errorf("decode workspace file %q: %w", path, err)
		}
		fmt.Fprintf(&snapshot, "[FILE %s; encoding=%s; bytes=%d]\n", result.Path, result.Encoding, result.Bytes)
		snapshot.WriteString(result.Content)
		if result.HasMore || result.Truncated {
			snapshot.WriteString("\n[File content truncated by the bounded reader.]\n")
		} else {
			snapshot.WriteByte('\n')
		}
		if snapshot.Len() > maxBackgroundAgentFilesTotalSize {
			return truncateBackgroundAgentFileSnapshot(snapshot.String()), nil
		}
	}
	return strings.TrimRight(snapshot.String(), "\n"), nil
}

func isBackgroundAgentHiddenPath(raw string) bool {
	path := filepath.ToSlash(filepath.Clean(strings.TrimSpace(raw)))
	return path == ".git" || strings.HasPrefix(path, ".git/")
}

func truncateBackgroundAgentFileSnapshot(snapshot string) string {
	if len(snapshot) <= maxBackgroundAgentFilesTotalSize {
		return snapshot
	}
	const notice = "\n[Workspace file snapshot truncated after 65536 bytes.]"
	limit := maxBackgroundAgentFilesTotalSize - len(notice)
	if limit < 0 {
		limit = 0
	}
	return snapshot[:limit] + notice
}
