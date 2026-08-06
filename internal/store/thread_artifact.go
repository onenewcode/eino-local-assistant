package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var (
	artifactRetentionLimit = MaxArtifactBytes
	threadRetentionLimit   = MaxThreadArtifactBytes
)

const artifactExcerptBytes int64 = 64 << 10

// PutArtifact prepares immutable evidence for a subsequent tool.completed
// event. The event is the only durable copy; this avoids a second per-session
// artifact directory while retaining bounded re-readable tool output.
func (s *ThreadStore) PutArtifact(ctx context.Context, id string, input ArtifactInput) (ArtifactRef, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ArtifactRef{}, err
	}
	defer unlock()
	_, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ArtifactRef{}, err
	}

	digest := sha256Hex(input.Data)
	refs, err := artifactRefsFromEvents(events)
	if err != nil {
		return ArtifactRef{}, err
	}
	if existing, ok := refs[digest]; ok {
		return existing, nil
	}

	used := artifactStoredBytes(refs)
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
	if originalSize <= fullLimit {
		ref.Data = append([]byte(nil), input.Data...)
		ref.StoredSize = originalSize
	} else {
		ref.Truncated = true
		ref.Head, ref.Tail = artifactExcerpt(input.Data, minInt64(artifactExcerptBytes, fullLimit))
		ref.StoredSize = int64(len(ref.Head) + len(ref.Tail))
	}
	return ref, nil
}

// ReadArtifact returns a bounded range from the immutable artifact reference
// recorded in a tool.completed event in the active JSONL session.
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
	_, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ArtifactRead{}, err
	}
	refs, err := artifactRefsFromEvents(events)
	if err != nil {
		return ArtifactRead{}, err
	}
	ref, ok := refs[digest]
	if !ok {
		return ArtifactRead{}, fmt.Errorf("artifact %q not found", artifactID)
	}
	if ref.Truncated {
		return boundedArtifactRead(ref, retainedArtifactExcerpt(ref), offset, limit), nil
	}
	return boundedArtifactRead(ref, ref.Data, offset, limit), nil
}

func artifactRefsFromEvents(events []ThreadEvent) (map[string]ArtifactRef, error) {
	refs := make(map[string]ArtifactRef)
	for _, event := range events {
		if event.Kind != EventToolCompleted {
			continue
		}
		var payload ToolCompleted
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode artifact event: %w", err)
		}
		if payload.Artifact == nil {
			continue
		}
		ref := *payload.Artifact
		if err := validateArtifactRef(ref); err != nil {
			return nil, err
		}
		if existing, ok := refs[ref.Digest]; ok && !sameArtifactRef(existing, ref) {
			return nil, fmt.Errorf("%w: duplicate artifact digest has different content", ErrJournalCorrupt)
		}
		refs[ref.Digest] = ref
	}
	return refs, nil
}

func artifactStoredBytes(refs map[string]ArtifactRef) int64 {
	var total int64
	for _, ref := range refs {
		total += ref.StoredSize
	}
	return total
}

const artifactTruncationMarker = "\n[... artifact truncated by retention ...]\n"

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
	return ArtifactRead{Ref: ref, Offset: offset, Data: append([]byte(nil), data[int(offset):int(end)]...), HasMore: end < int64(len(data))}
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
		if (char >= 'a' && char <= 'f') || (char >= '0' && char <= '9') {
			continue
		}
		return "", errors.New("artifact id must be sha256-<64 hex characters>")
	}
	return digest, nil
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
	return append([]byte(nil), data[:headLen]...), append([]byte(nil), data[len(data)-tailLen:]...)
}

func validateArtifactRef(ref ArtifactRef) error {
	digest, err := artifactDigestFromID(ref.ID)
	if err != nil || digest != ref.SHA256 || digest != ref.Digest {
		return errorsNewArtifactReference("invalid artifact digest")
	}
	if ref.Truncated {
		if len(ref.Data) != 0 || ref.StoredSize != int64(len(ref.Head)+len(ref.Tail)) {
			return errorsNewArtifactReference("artifact stored size mismatch")
		}
		return nil
	}
	if int64(len(ref.Data)) != ref.StoredSize || ref.StoredSize != ref.OriginalSize || ref.Size != ref.OriginalSize {
		return errorsNewArtifactReference("artifact stored size mismatch")
	}
	if sha256Hex(ref.Data) != ref.SHA256 {
		return errorsNewArtifactReference("artifact content hash mismatch")
	}
	return nil
}

func sameArtifactRef(first, second ArtifactRef) bool {
	return first.ID == second.ID && first.SHA256 == second.SHA256 && first.Digest == second.Digest &&
		first.Kind == second.Kind && first.MediaType == second.MediaType && first.Size == second.Size &&
		first.OriginalSize == second.OriginalSize && first.StoredSize == second.StoredSize &&
		first.Truncated == second.Truncated && string(first.Head) == string(second.Head) &&
		string(first.Tail) == string(second.Tail) && string(first.Data) == string(second.Data)
}

func defaultArtifactKind(kind string) string {
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return "tool-output"
}

func defaultMediaType(mediaType string) string {
	if mediaType = strings.TrimSpace(mediaType); mediaType != "" {
		return mediaType
	}
	return "text/plain"
}

func errorsNewArtifactReference(message string) error {
	return fmt.Errorf("%w: %s", ErrJournalCorrupt, message)
}

func minInt64(first, second int64) int64 {
	if first < second {
		return first
	}
	return second
}
