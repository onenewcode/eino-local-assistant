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
	defaultGlobResults = 200
	maxGlobResults     = 1000
)

var errGlobLimitReached = errors.New("glob result limit reached")

// GlobFilesInput selects bounded path-pattern matching in the workspace.
type GlobFilesInput struct {
	Pattern       string `json:"pattern" jsonschema:"description=Filepath glob pattern such as **/*.go or README.md."`
	Path          string `json:"path,omitempty" jsonschema:"description=Optional workspace-relative directory to search from."`
	MaxResults    int    `json:"max_results,omitempty" jsonschema:"description=Maximum matching paths (default 200, maximum 1000)."`
	IncludeHidden bool   `json:"include_hidden,omitempty" jsonschema:"description=Include dotfiles and dot-directories except .git remains hidden."`
}

// GlobFileMatch is one matching workspace-relative path.
type GlobFileMatch struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

// GlobFilesOutput reports bounded glob matches.
type GlobFilesOutput struct {
	Pattern   string          `json:"pattern"`
	Matches   []GlobFileMatch `json:"matches"`
	Truncated bool            `json:"truncated"`
}

// NewGlobFiles creates a read-only, workspace-scoped glob tool.
func NewGlobFiles(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"glob_files",
		"Find workspace paths by a bounded filepath glob. Hidden paths and .git are skipped by default; use list_files for a directory tree and read_file for contents.",
		func(ctx context.Context, input GlobFilesInput) (GlobFilesOutput, error) {
			return globFiles(ctx, root, input)
		},
	)
}

func globFiles(ctx context.Context, root string, input GlobFilesInput) (GlobFilesOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return GlobFilesOutput{}, err
	}
	pattern := strings.TrimSpace(input.Pattern)
	if pattern == "" {
		return GlobFilesOutput{}, errors.New("pattern is required")
	}
	if _, err := filepath.Match(filepath.FromSlash(pattern), "probe"); err != nil {
		return GlobFilesOutput{}, fmt.Errorf("invalid glob: %w", err)
	}
	limit := input.MaxResults
	if limit == 0 {
		limit = defaultGlobResults
	}
	if limit < 1 || limit > maxGlobResults {
		return GlobFilesOutput{}, fmt.Errorf("max_results must be between 1 and %d", maxGlobResults)
	}
	start := root
	if strings.TrimSpace(input.Path) != "" {
		var err error
		start, err = safeEditPath(root, input.Path)
		if err != nil {
			return GlobFilesOutput{}, err
		}
	}
	info, err := os.Stat(start)
	if err != nil {
		return GlobFilesOutput{}, fmt.Errorf("stat glob path: %w", err)
	}
	if !info.IsDir() {
		return GlobFilesOutput{}, errors.New("glob path is not a directory")
	}
	output := GlobFilesOutput{Pattern: pattern, Matches: make([]GlobFileMatch, 0)}
	err = filepath.WalkDir(start, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path != start {
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
			rel = filepath.ToSlash(rel)
			if matchesGlobPattern(pattern, rel) {
				match := GlobFileMatch{Path: rel, Type: "file"}
				if entry.IsDir() {
					match.Type = "directory"
				} else if entry.Type()&os.ModeSymlink != 0 {
					match.Type = "symlink"
				} else if entry.Type().IsRegular() {
					if fileInfo, infoErr := entry.Info(); infoErr == nil {
						match.Size = fileInfo.Size()
					}
				} else {
					match.Type = "other"
				}
				output.Matches = append(output.Matches, match)
				if len(output.Matches) >= limit {
					output.Truncated = true
					return errGlobLimitReached
				}
			}
		}
		return nil
	})
	if errors.Is(err, errGlobLimitReached) {
		err = nil
	}
	if err != nil {
		return GlobFilesOutput{}, fmt.Errorf("glob files: %w", err)
	}
	return output, nil
}

func matchesGlobPattern(pattern, rel string) bool {
	if matchesSearchGlob(pattern, rel) {
		return true
	}
	// filepath.Match treats ** like *, so explicitly support the common
	// recursive prefix while retaining the existing path/base matching rules.
	return strings.HasPrefix(pattern, "**/") && matchesSearchGlob(strings.TrimPrefix(pattern, "**/"), rel)
}
