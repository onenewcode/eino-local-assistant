package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	defaultReadFileBytes = 16 << 10
	maxReadFileBytes     = 64 << 10
)

// ReadFileInput selects a bounded range from a workspace-relative file.
type ReadFileInput struct {
	Path     string `json:"path" jsonschema:"description=Workspace-relative file path to read."`
	Offset   int64  `json:"offset,omitempty" jsonschema:"description=Zero-based byte offset."`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"description=Maximum bytes to return (default 16384, maximum 65536)."`
}

// ReadFileOutput reports the bounded file content and whether more bytes can
// be requested with a later offset.
type ReadFileOutput struct {
	Path      string `json:"path"`
	Offset    int64  `json:"offset"`
	Content   string `json:"content"`
	Encoding  string `json:"encoding"`
	Bytes     int    `json:"bytes"`
	HasMore   bool   `json:"has_more"`
	Truncated bool   `json:"truncated"`
}

// NewReadFile creates a read-only file tool with the same symlink and path
// boundary rules as edit_file.
func NewReadFile(opts EditFileOptions) (tool.InvokableTool, error) {
	root, err := normalizeEditRoot(opts.WorkingDir)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(
		"read_file",
		"Read a bounded range from a workspace-relative file. Use offset and max_bytes to page through large files; paths cannot escape the configured workspace.",
		func(ctx context.Context, input ReadFileInput) (ReadFileOutput, error) {
			return readFile(ctx, root, input)
		},
	)
}

func readFile(ctx context.Context, root string, input ReadFileInput) (ReadFileOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ReadFileOutput{}, err
	}
	if input.Offset < 0 {
		return ReadFileOutput{}, errors.New("offset must be >= 0")
	}
	limit := input.MaxBytes
	if limit == 0 {
		limit = defaultReadFileBytes
	}
	if limit < 1 || limit > maxReadFileBytes {
		return ReadFileOutput{}, fmt.Errorf("max_bytes must be between 1 and %d", maxReadFileBytes)
	}
	path, err := safeEditPath(root, input.Path)
	if err != nil {
		return ReadFileOutput{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ReadFileOutput{}, fmt.Errorf("stat %q: %w", input.Path, err)
	}
	if !info.Mode().IsRegular() {
		return ReadFileOutput{}, errors.New("path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return ReadFileOutput{}, fmt.Errorf("open %q: %w", input.Path, err)
	}
	defer file.Close()
	if _, err := file.Seek(input.Offset, io.SeekStart); err != nil {
		return ReadFileOutput{}, fmt.Errorf("seek %q: %w", input.Path, err)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return ReadFileOutput{}, fmt.Errorf("read %q: %w", input.Path, err)
	}
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
	}
	hasMore := truncated || input.Offset+int64(len(data)) < info.Size()
	content := string(data)
	encoding := "utf-8"
	if !utf8.Valid(data) {
		content = base64.StdEncoding.EncodeToString(data)
		encoding = "base64"
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ReadFileOutput{}, fmt.Errorf("report read path: %w", err)
	}
	return ReadFileOutput{
		Path:      filepath.ToSlash(rel),
		Offset:    input.Offset,
		Content:   content,
		Encoding:  encoding,
		Bytes:     len(data),
		HasMore:   hasMore,
		Truncated: truncated,
	}, nil
}
