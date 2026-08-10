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

// EditFileOptions scopes edit_file to one configured workspace root.
type EditFileOptions struct {
	WorkingDir string
}

// EditFileInput describes one exact, optimistic file replacement.
type EditFileInput struct {
	Path                 string `json:"path" jsonschema:"description=Workspace-relative file path to edit."`
	OldString            string `json:"old_string" jsonschema:"description=Exact text that must be present in the file."`
	NewString            string `json:"new_string" jsonschema:"description=Replacement text; may be empty to delete the matched text."`
	ExpectedReplacements int    `json:"expected_replacements,omitempty" jsonschema:"description=Expected number of exact matches (default 1)."`
}

// EditFileOutput reports the durable result of an edit.
type EditFileOutput struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Bytes        int    `json:"bytes"`
}

// NewEditFile creates a bounded workspace-relative edit tool. Exact matching
// makes stale model context fail safely instead of silently overwriting code.
func NewEditFile(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"edit_file",
		"Replace exact text in a workspace-relative file. The old_string must match exactly; use read_command or run_command first and retry when the file changed. Paths cannot escape the configured workspace.",
		func(ctx context.Context, input EditFileInput) (EditFileOutput, error) {
			return editFile(ctx, root, input)
		},
	)
}

func normalizeEditRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		root, _ = os.Getwd()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve edit workspace: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("edit workspace %q: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("edit workspace %q is not a directory", abs)
	}
	return filepath.EvalSymlinks(abs)
}

func editFile(ctx context.Context, root string, input EditFileInput) (EditFileOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return EditFileOutput{}, err
	}
	path, err := safeEditPath(root, input.Path)
	if err != nil {
		return EditFileOutput{}, err
	}
	if input.OldString == "" {
		return EditFileOutput{}, errors.New("old_string is required")
	}
	if err := RequirePermission(ctx, PermissionRequest{Tool: "edit_file", Action: "modify", Detail: input.Path, Risk: RiskMedium}); err != nil {
		return EditFileOutput{}, err
	}
	expected := input.ExpectedReplacements
	if expected == 0 {
		expected = 1
	}
	if expected < 1 {
		return EditFileOutput{}, errors.New("expected_replacements must be greater than zero")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return EditFileOutput{}, fmt.Errorf("read %q: %w", input.Path, err)
	}
	count := strings.Count(string(data), input.OldString)
	if count != expected {
		return EditFileOutput{}, fmt.Errorf("old_string matched %d times in %q, expected %d", count, input.Path, expected)
	}
	replaced := strings.ReplaceAll(string(data), input.OldString, input.NewString)
	info, err := os.Stat(path)
	if err != nil {
		return EditFileOutput{}, fmt.Errorf("stat %q: %w", input.Path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".edit-file-*")
	if err != nil {
		return EditFileOutput{}, fmt.Errorf("create edit temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.WriteString(replaced)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return EditFileOutput{}, fmt.Errorf("write %q: %w", input.Path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return EditFileOutput{}, fmt.Errorf("install edit %q: %w", input.Path, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return EditFileOutput{}, fmt.Errorf("report edit path: %w", err)
	}
	return EditFileOutput{Path: filepath.ToSlash(rel), Replacements: count, Bytes: len(replaced)}, nil
}

func safeEditPath(root, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || filepath.IsAbs(requested) {
		return "", errors.New("path must be a non-empty workspace-relative path")
	}
	clean := filepath.Clean(requested)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path must stay inside the edit workspace")
	}
	path := filepath.Join(root, clean)
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("resolve edit path: %w", err)
	}
	if !isWithin(root, parent) {
		return "", errors.New("path must stay inside the edit workspace")
	}
	resolved := filepath.Join(parent, filepath.Base(path))
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("editing symlink paths is not allowed")
	}
	return resolved, nil
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
