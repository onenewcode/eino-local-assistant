package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ResolveWorkspaceRoot picks an absolute workspace root for path clamping.
// Explicit root wins; otherwise process cwd is used. Paths are cleaned and,
// when possible, symlink-resolved so macOS /var vs /private/var compares match.
func ResolveWorkspaceRoot(explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	var abs string
	if explicit != "" {
		var err error
		abs, err = filepath.Abs(explicit)
		if err != nil {
			return "", fmt.Errorf("resolve workspace_root: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("workspace_root %q: %w", abs, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("workspace_root %q is not a directory", abs)
		}
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve workspace_root: %w", err)
		}
		abs, err = filepath.Abs(cwd)
		if err != nil {
			return "", fmt.Errorf("resolve workspace_root: %w", err)
		}
	}
	root := canonicalizePath(abs)
	if filepath.Dir(root) == root {
		return "", errors.New("workspace_root must not be a filesystem root")
	}
	return root, nil
}

// PathWithinWorkspace reports whether path is the workspace root or a descendant.
// Both sides are cleaned and symlink-resolved when possible before comparison.
func PathWithinWorkspace(workspaceRoot, path string) bool {
	root := canonicalizePath(workspaceRoot)
	target := canonicalizePath(path)
	if root == "" || target == "" {
		return false
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// canonicalizePath returns Abs+Clean+EvalSymlinks when possible.
// On EvalSymlinks failure it falls back to Clean(Abs) so callers still work
// for non-existent intermediate paths during tests.
func canonicalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
