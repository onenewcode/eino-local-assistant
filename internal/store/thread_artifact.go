package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const artifactExcerptBytes int64 = 16 << 10

// Variables keep the production defaults explicit while allowing focused
// package tests to exercise truncation without allocating tens of MiB.
var (
	artifactRetentionLimit = MaxArtifactBytes
	threadRetentionLimit   = MaxThreadArtifactBytes
)

type storedArtifact struct {
	FormatVersion int         `json:"format_version"`
	Ref           ArtifactRef `json:"ref"`
}

// PutArtifact stores immutable content by digest. Size caps never fail a turn:
// oversized content is represented by a digest plus bounded head/tail excerpts.
func (s *ThreadStore) PutArtifact(ctx context.Context, id string, input ArtifactInput) (ArtifactRef, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ArtifactRef{}, err
	}
	defer unlock()
	if _, _, err := s.loadThreadLocked(dir, id); err != nil {
		return ArtifactRef{}, err
	}

	digest := sha256Hex(input.Data)
	path := artifactMetadataPath(dir, digest)
	if _, err := os.Stat(path); err == nil {
		var existing storedArtifact
		if err := readJSON(path, &existing); err != nil {
			return ArtifactRef{}, fmt.Errorf("read existing artifact: %w", err)
		}
		if existing.Ref.SHA256 != digest || existing.Ref.Digest != digest {
			return ArtifactRef{}, fmt.Errorf("%w: artifact digest mismatch", ErrJournalCorrupt)
		}
		if err := validateArtifactRef(dir, existing.Ref); err != nil {
			return ArtifactRef{}, err
		}
		return existing.Ref, nil
	} else if !os.IsNotExist(err) {
		return ArtifactRef{}, fmt.Errorf("stat artifact: %w", err)
	}

	used, err := artifactStoredBytes(filepath.Join(dir, artifactsDir))
	if err != nil {
		return ArtifactRef{}, err
	}
	available := threadRetentionLimit - used
	if available < 0 {
		available = 0
	}
	fullLimit := minInt64(artifactRetentionLimit, available)
	originalSize := int64(len(input.Data))
	ref := ArtifactRef{
		ID:           "sha256-" + digest,
		SHA256:       digest,
		Digest:       digest,
		Kind:         defaultArtifactKind(input.Kind),
		MediaType:    defaultMediaType(input.MediaType),
		Size:         originalSize,
		OriginalSize: originalSize,
	}
	artifact := storedArtifact{FormatVersion: ThreadFormatVersion, Ref: ref}
	if originalSize <= fullLimit {
		artifact.Ref.StoredSize = originalSize
		if err := writeBytesAtomic(artifactBlobPath(dir, digest), input.Data); err != nil {
			return ArtifactRef{}, fmt.Errorf("write artifact blob: %w", err)
		}
	} else {
		artifact.Ref.Truncated = true
		head, tail := artifactExcerpt(input.Data, minInt64(artifactExcerptBytes, fullLimit))
		artifact.Ref.Head = head
		artifact.Ref.Tail = tail
		artifact.Ref.StoredSize = int64(len(head) + len(tail))
	}
	if err := writeJSONAtomic(path, artifact); err != nil {
		return ArtifactRef{}, fmt.Errorf("write artifact: %w", err)
	}
	return artifact.Ref, nil
}

// ReadArtifact returns a bounded range from an artifact in the active thread.
// Full blobs use offsets into the original bytes. Retention-truncated artifacts
// use offsets into a virtual retained excerpt: head, an explicit omission
// marker, then tail. This makes paged reads usable while Ref.Truncated makes
// it clear that the original middle bytes are unavailable.
func (s *ThreadStore) ReadArtifact(ctx context.Context, id, artifactID string, offset, limit int64) (ArtifactRead, error) {
	if offset < 0 {
		return ArtifactRead{}, errors.New("artifact offset must be >= 0")
	}
	if limit <= 0 {
		return ArtifactRead{}, errors.New("artifact read limit must be > 0")
	}
	digest, err := artifactDigestFromID(artifactID)
	if err != nil {
		return ArtifactRead{}, err
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ArtifactRead{}, err
	}
	defer unlock()
	if _, _, err := s.loadThreadLocked(dir, id); err != nil {
		return ArtifactRead{}, err
	}
	var artifact storedArtifact
	if err := readJSON(artifactMetadataPath(dir, digest), &artifact); err != nil {
		return ArtifactRead{}, fmt.Errorf("read artifact metadata: %w", err)
	}
	if artifact.Ref.ID != "sha256-"+digest || artifact.Ref.SHA256 != digest || artifact.Ref.Digest != digest {
		return ArtifactRead{}, fmt.Errorf("%w: artifact metadata mismatch", ErrJournalCorrupt)
	}
	if artifact.Ref.Truncated {
		excerpt := retainedArtifactExcerpt(artifact.Ref)
		return boundedArtifactRead(artifact.Ref, excerpt, offset, limit), nil
	}
	if err := validateArtifactRef(dir, artifact.Ref); err != nil {
		return ArtifactRead{}, err
	}
	if offset >= artifact.Ref.StoredSize {
		return ArtifactRead{Ref: artifact.Ref, Offset: offset}, nil
	}
	readLen := minInt64(limit, artifact.Ref.StoredSize-offset)
	data := make([]byte, readLen)
	f, err := os.Open(artifactBlobPath(dir, digest))
	if err != nil {
		return ArtifactRead{}, fmt.Errorf("open artifact blob: %w", err)
	}
	defer f.Close()
	if _, err := f.ReadAt(data, offset); err != nil && !errors.Is(err, io.EOF) {
		return ArtifactRead{}, fmt.Errorf("read artifact blob: %w", err)
	}
	return ArtifactRead{
		Ref:     artifact.Ref,
		Offset:  offset,
		Data:    data,
		HasMore: offset+readLen < artifact.Ref.StoredSize,
	}, nil
}

const artifactTruncationMarker = "\n[... artifact truncated by retention ...]\n"

// retainedArtifactExcerpt is deliberately a virtual representation rather
// than a reconstruction attempt: only persisted head/tail bytes are present.
func retainedArtifactExcerpt(ref ArtifactRef) []byte {
	excerpt := append([]byte(nil), ref.Head...)
	if len(ref.Tail) == 0 {
		return excerpt
	}
	excerpt = append(excerpt, artifactTruncationMarker...)
	return append(excerpt, ref.Tail...)
}

func boundedArtifactRead(ref ArtifactRef, data []byte, offset, limit int64) ArtifactRead {
	if offset >= int64(len(data)) {
		return ArtifactRead{Ref: ref, Offset: offset}
	}
	readLen := minInt64(limit, int64(len(data))-offset)
	end := offset + readLen
	return ArtifactRead{
		Ref:     ref,
		Offset:  offset,
		Data:    append([]byte(nil), data[int(offset):int(end)]...),
		HasMore: end < int64(len(data)),
	}
}

func artifactDigestFromID(artifactID string) (string, error) {
	artifactID = strings.TrimSpace(artifactID)
	if !strings.HasPrefix(artifactID, "sha256-") {
		return "", errors.New("artifact id must be sha256-<64 hex characters>")
	}
	digest := strings.TrimPrefix(artifactID, "sha256-")
	if len(digest) != 64 {
		return "", errors.New("artifact id must be sha256-<64 hex characters>")
	}
	for _, char := range digest {
		if (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') {
			continue
		}
		return "", errors.New("artifact id must be sha256-<64 hex characters>")
	}
	return digest, nil
}

func artifactMetadataPath(dir, digest string) string {
	return filepath.Join(dir, artifactsDir, digest+".json")
}

func artifactBlobPath(dir, digest string) string {
	return filepath.Join(dir, artifactsDir, digest+".blob")
}

func artifactStoredBytes(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("list artifacts: %w", err)
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var artifact storedArtifact
		if err := readJSON(filepath.Join(dir, entry.Name()), &artifact); err != nil {
			return 0, fmt.Errorf("read artifact metadata: %w", err)
		}
		total += artifact.Ref.StoredSize
	}
	return total, nil
}

func artifactExcerpt(data []byte, limit int64) ([]byte, []byte) {
	if limit <= 0 || len(data) == 0 {
		return nil, nil
	}
	if int64(len(data)) <= limit {
		return append([]byte(nil), data...), nil
	}
	headLen := int(limit / 2)
	tailLen := int(limit) - headLen
	if headLen == 0 {
		headLen = 1
		tailLen = 0
	}
	head := append([]byte(nil), data[:headLen]...)
	tail := append([]byte(nil), data[len(data)-tailLen:]...)
	return head, tail
}

func validateArtifactRef(dir string, ref ArtifactRef) error {
	if ref.SHA256 == "" || ref.Digest == "" || ref.SHA256 != ref.Digest {
		return errorsNewArtifactReference("invalid artifact digest")
	}
	var artifact storedArtifact
	if err := readJSON(artifactMetadataPath(dir, ref.SHA256), &artifact); err != nil {
		return fmt.Errorf("read artifact reference: %w", err)
	}
	if artifact.Ref.SHA256 != ref.SHA256 || artifact.Ref.Digest != ref.Digest {
		return errorsNewArtifactReference("artifact digest mismatch")
	}
	if artifact.Ref.Truncated {
		if artifact.Ref.StoredSize != int64(len(artifact.Ref.Head)+len(artifact.Ref.Tail)) {
			return errorsNewArtifactReference("artifact stored size mismatch")
		}
		return nil
	}
	blob, err := os.ReadFile(artifactBlobPath(dir, ref.SHA256))
	if err != nil {
		return fmt.Errorf("read artifact blob: %w", err)
	}
	if sha256Hex(blob) != ref.SHA256 {
		return errorsNewArtifactReference("artifact content hash mismatch")
	}
	if artifact.Ref.StoredSize != int64(len(blob)) {
		return errorsNewArtifactReference("artifact stored size mismatch")
	}
	return nil
}

func defaultArtifactKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "blob"
	}
	return strings.TrimSpace(kind)
}

func defaultMediaType(mediaType string) string {
	if strings.TrimSpace(mediaType) == "" {
		return "application/octet-stream"
	}
	return strings.TrimSpace(mediaType)
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func errorsNewArtifactReference(message string) error {
	return fmt.Errorf("artifact reference: %s", message)
}
