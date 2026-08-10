package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	defaultListDepth   = 1
	maxListDepth       = 5
	defaultListEntries = 200
	maxListEntries     = 1000
)

var errListEntriesLimit = errors.New("directory entry limit reached")

// ListFilesInput selects a bounded directory tree listing.
type ListFilesInput struct {
	Path          string `json:"path,omitempty" jsonschema:"description=Optional workspace-relative directory to list; defaults to workspace root."`
	Depth         int    `json:"depth,omitempty" jsonschema:"description=Directory depth to include (default 1, maximum 5)."`
	MaxEntries    int    `json:"max_entries,omitempty" jsonschema:"description=Maximum entries to return (default 200, maximum 1000)."`
	IncludeHidden bool   `json:"include_hidden,omitempty" jsonschema:"description=Include dotfiles and dot-directories except .git remains hidden."`
}

// ListFileEntry is one directory or regular-file result.
type ListFileEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

// ListFilesOutput reports a bounded, workspace-relative tree listing.
type ListFilesOutput struct {
	Root      string          `json:"root"`
	Entries   []ListFileEntry `json:"entries"`
	Truncated bool            `json:"truncated"`
}

// NewListFiles creates a read-only directory listing tool. WalkDir does not
// follow symlinked directories, so a listing cannot silently escape the root.
func NewListFiles(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"list_files",
		"List a bounded workspace directory tree. Hidden files and .git are skipped by default; use read_file or search_files for file contents. Symlinked directories are not followed.",
		func(ctx context.Context, input ListFilesInput) (ListFilesOutput, error) {
			return listFiles(ctx, root, input)
		},
	)
}

func listFiles(ctx context.Context, root string, input ListFilesInput) (ListFilesOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ListFilesOutput{}, err
	}
	depth := input.Depth
	if depth == 0 {
		depth = defaultListDepth
	}
	if depth < 1 || depth > maxListDepth {
		return ListFilesOutput{}, fmt.Errorf("depth must be between 1 and %d", maxListDepth)
	}
	limit := input.MaxEntries
	if limit == 0 {
		limit = defaultListEntries
	}
	if limit < 1 || limit > maxListEntries {
		return ListFilesOutput{}, fmt.Errorf("max_entries must be between 1 and %d", maxListEntries)
	}
	start := root
	if strings.TrimSpace(input.Path) != "" {
		var err error
		start, err = safeEditPath(root, input.Path)
		if err != nil {
			return ListFilesOutput{}, err
		}
	}
	info, err := os.Stat(start)
	if err != nil {
		return ListFilesOutput{}, fmt.Errorf("stat list path: %w", err)
	}
	if !info.IsDir() {
		return ListFilesOutput{}, errors.New("list path is not a directory")
	}
	relRoot, err := filepath.Rel(root, start)
	if err != nil {
		return ListFilesOutput{}, fmt.Errorf("report list root: %w", err)
	}
	if relRoot == "." {
		relRoot = "."
	}
	output := ListFilesOutput{Root: filepath.ToSlash(relRoot), Entries: make([]ListFileEntry, 0)}
	err = filepath.WalkDir(start, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == start {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !input.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relFromStart, err := filepath.Rel(start, path)
		if err != nil {
			return err
		}
		level := len(strings.Split(filepath.Clean(relFromStart), string(filepath.Separator)))
		if level > depth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		item := ListFileEntry{Path: filepath.ToSlash(rel), Type: "file"}
		if entry.IsDir() {
			item.Type = "directory"
		} else if entry.Type()&os.ModeSymlink != 0 {
			item.Type = "symlink"
		} else if entry.Type().IsRegular() {
			if fileInfo, infoErr := entry.Info(); infoErr == nil {
				item.Size = fileInfo.Size()
			}
		} else {
			item.Type = "other"
		}
		output.Entries = append(output.Entries, item)
		if len(output.Entries) >= limit {
			output.Truncated = true
			return errListEntriesLimit
		}
		return nil
	})
	if errors.Is(err, errListEntriesLimit) {
		err = nil
	}
	if err != nil {
		return ListFilesOutput{}, fmt.Errorf("list files: %w", err)
	}
	return output, nil
}
