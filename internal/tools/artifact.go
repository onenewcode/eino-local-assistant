package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"unicode/utf8"

	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	defaultArtifactReadBytes = 16 << 10
	maxArtifactReadBytes     = 64 << 10
)

// ReadArtifactInput selects a bounded range from an artifact referenced in
// prior context as artifact://sha256-....
type ReadArtifactInput struct {
	ArtifactID string `json:"artifact_id" jsonschema:"description=Artifact ID from an artifact:// reference, for example sha256-<digest>."`
	Offset     int64  `json:"offset,omitempty" jsonschema:"description=Zero-based byte offset. Use the returned has_more field to continue reading."`
	MaxBytes   int    `json:"max_bytes,omitempty" jsonschema:"description=Maximum bytes to return (default 16384, maximum 65536)."`
}

// ReadArtifactOutput describes the evidence range without claiming that a
// retention-truncated artifact is complete.
type ReadArtifactOutput struct {
	ArtifactID string `json:"artifact_id"`
	SHA256     string `json:"sha256"`
	Offset     int64  `json:"offset"`
	Content    string `json:"content"`
	Encoding   string `json:"encoding"`
	HasMore    bool   `json:"has_more"`
	Truncated  bool   `json:"truncated"`
}

// NewReadArtifact creates the read-only evidence tool. chat.Session installs a
// thread-scoped repository in the turn context, so model-provided IDs cannot
// escape into another local thread.
func NewReadArtifact() (tool.InvokableTool, error) {
	return utils.InferTool(
		"read_artifact",
		"Read a bounded range from an artifact:// reference in the current thread. Use this when earlier context cites an artifact and its original evidence is needed. Never assume a truncated artifact contains the complete original output.",
		func(ctx context.Context, input ReadArtifactInput) (ReadArtifactOutput, error) {
			access, ok := store.ThreadAccessFromContext(ctx)
			if !ok {
				return ReadArtifactOutput{}, errors.New("artifact access is unavailable outside an active thread")
			}
			limit := input.MaxBytes
			if limit == 0 {
				limit = defaultArtifactReadBytes
			}
			if limit < 0 || limit > maxArtifactReadBytes {
				return ReadArtifactOutput{}, errors.New("max_bytes must be between 1 and 65536")
			}
			read, err := access.Repository.ReadArtifact(ctx, access.ThreadID, input.ArtifactID, input.Offset, int64(limit))
			if err != nil {
				return ReadArtifactOutput{}, err
			}
			content := string(read.Data)
			encoding := "utf-8"
			if !utf8.Valid(read.Data) {
				content = base64.StdEncoding.EncodeToString(read.Data)
				encoding = "base64"
			}
			return ReadArtifactOutput{
				ArtifactID: read.Ref.ID,
				SHA256:     read.Ref.SHA256,
				Offset:     read.Offset,
				Content:    content,
				Encoding:   encoding,
				HasMore:    read.HasMore,
				Truncated:  read.Ref.Truncated,
			}, nil
		},
	)
}
