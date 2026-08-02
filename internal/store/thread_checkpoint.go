package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CommitCheckpoint atomically publishes an immutable context checkpoint. The
// checkpoint file is written before its journal event, so an interrupted write
// can only leave an unreferenced file and never a dangling active state.
func (s *ThreadStore) CommitCheckpoint(ctx context.Context, id string, expectedRevision uint64, input CheckpointInput) (Checkpoint, ThreadState, error) {
	if err := validateCheckpointInput(input); err != nil {
		return Checkpoint{}, ThreadState{}, err
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return Checkpoint{}, ThreadState{}, err
	}
	defer unlock()
	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return Checkpoint{}, ThreadState{}, err
	}
	if err := checkExpectedRevision(state, expectedRevision); err != nil {
		return Checkpoint{}, ThreadState{}, err
	}
	if strings.TrimSpace(input.ParentID) != state.ActiveCheckpointID {
		return Checkpoint{}, ThreadState{}, fmt.Errorf("checkpoint parent %q does not match active checkpoint %q", input.ParentID, state.ActiveCheckpointID)
	}
	if input.ParentID != "" {
		if _, err := checkpointFromJournal(events, input.ParentID); err != nil {
			return Checkpoint{}, ThreadState{}, fmt.Errorf("load checkpoint parent: %w", err)
		}
	}
	if input.Summary != nil {
		if err := validateArtifactRef(dir, *input.Summary); err != nil {
			return Checkpoint{}, ThreadState{}, err
		}
	}

	checkpoint := checkpointFromInput(id, state, input, time.Now().UTC())
	path := checkpointPath(dir, checkpoint.ID)
	if _, err := os.Stat(path); err == nil {
		return Checkpoint{}, ThreadState{}, fmt.Errorf("checkpoint %q already exists", checkpoint.ID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Checkpoint{}, ThreadState{}, fmt.Errorf("stat checkpoint: %w", err)
	}
	if err := writeJSONAtomic(path, checkpoint); err != nil {
		return Checkpoint{}, ThreadState{}, fmt.Errorf("write checkpoint: %w", err)
	}

	next, err := s.appendLocked(dir, state, expectedRevision, EventContextCompacted, "", checkpointCommittedPayload{Checkpoint: checkpoint})
	if err != nil {
		return Checkpoint{}, ThreadState{}, err
	}
	return checkpoint, next, nil
}

// LoadCheckpoint loads an immutable checkpoint. If the checkpoint file was
// lost after its event became durable, it is rebuilt from the journal payload.
func (s *ThreadStore) LoadCheckpoint(ctx context.Context, id, checkpointID string) (Checkpoint, error) {
	if err := validateThreadID(checkpointID); err != nil {
		return Checkpoint{}, fmt.Errorf("invalid checkpoint id: %w", err)
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return Checkpoint{}, err
	}
	defer unlock()
	_, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return Checkpoint{}, err
	}
	// The journal payload is authoritative. A checkpoint file is only a
	// materialized convenience copy and must not override the event ledger.
	checkpoint, err := checkpointFromJournal(events, checkpointID)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := validateCheckpoint(checkpoint, id); err != nil {
		return Checkpoint{}, err
	}
	path := checkpointPath(dir, checkpointID)
	var onDisk Checkpoint
	if err := readJSON(path, &onDisk); err != nil || onDisk.Hash != checkpoint.Hash {
		if err := writeJSONAtomic(path, checkpoint); err != nil {
			return Checkpoint{}, fmt.Errorf("repair checkpoint: %w", err)
		}
	}
	return checkpoint, nil
}

// LoadCheckpointLineage returns the active checkpoint followed by each
// ancestor. Each item carries only direct source IDs, so this cold-path walk
// reconstructs exact coverage without growing the active checkpoint payload.
func (s *ThreadStore) LoadCheckpointLineage(ctx context.Context, id, checkpointID string) ([]Checkpoint, error) {
	if err := validateThreadID(checkpointID); err != nil {
		return nil, fmt.Errorf("invalid checkpoint id: %w", err)
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	_, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return nil, err
	}
	lineage := make([]Checkpoint, 0)
	seen := make(map[string]struct{})
	currentID := checkpointID
	for currentID != "" {
		if _, exists := seen[currentID]; exists {
			return nil, fmt.Errorf("%w: checkpoint lineage cycle at %q", ErrJournalCorrupt, currentID)
		}
		seen[currentID] = struct{}{}
		checkpoint, loadErr := checkpointFromJournal(events, currentID)
		if loadErr != nil {
			return nil, fmt.Errorf("load checkpoint lineage %q: %w", currentID, loadErr)
		}
		if validateErr := validateCheckpoint(checkpoint, id); validateErr != nil {
			return nil, validateErr
		}
		lineage = append(lineage, checkpoint)
		currentID = checkpoint.ParentID
	}
	return lineage, nil
}

func checkpointFromInput(threadID string, state ThreadState, input CheckpointInput, now time.Time) Checkpoint {
	checkpointID := strings.TrimSpace(input.ID)
	if checkpointID == "" {
		checkpointID = newRandomID("cp")
	}
	payload := append(json.RawMessage(nil), input.Payload...)
	checkpoint := Checkpoint{
		ID:             checkpointID,
		ThreadID:       threadID,
		Revision:       state.Revision + 1,
		Sequence:       state.HeadSequence + 1,
		CreatedAt:      now.UTC(),
		ParentID:       strings.TrimSpace(input.ParentID),
		Kind:           strings.TrimSpace(input.Kind),
		WindowNumber:   input.WindowNumber,
		MessageRefs:    append([]MessageRef(nil), input.MessageRefs...),
		Summary:        cloneArtifactRef(input.Summary),
		Payload:        payload,
		SourceEventIDs: append([]string(nil), input.SourceEventIDs...),
		SourceHash:     input.SourceHash,
		Focus:          input.Focus,
		BeforeTokens:   input.BeforeTokens,
		AfterTokens:    input.AfterTokens,
		Automatic:      input.Automatic,
		LowGain:        input.LowGain,
		AutoPaused:     input.AutoPaused,
	}
	if checkpoint.Kind == "" {
		checkpoint.Kind = "compaction"
	}
	if checkpoint.SourceHash == "" {
		checkpoint.SourceHash = sha256Hex([]byte(strings.Join(checkpoint.SourceEventIDs, "\n")))
	}
	checkpoint.Hash = checkpointHash(checkpoint)
	return checkpoint
}

func validateCheckpointInput(input CheckpointInput) error {
	if input.ID != "" {
		if err := validateThreadID(input.ID); err != nil {
			return fmt.Errorf("invalid checkpoint id: %w", err)
		}
	}
	if input.ParentID != "" {
		if err := validateThreadID(input.ParentID); err != nil {
			return fmt.Errorf("invalid parent checkpoint id: %w", err)
		}
	}
	if len(input.Payload) > 0 && !json.Valid(input.Payload) {
		return errors.New("checkpoint payload must be valid JSON")
	}
	for _, ref := range input.MessageRefs {
		if strings.TrimSpace(ref.EventID) == "" || ref.MessageIndex < 0 {
			return errors.New("checkpoint message refs must identify an event and non-negative index")
		}
	}
	return nil
}

func checkpointPath(dir, checkpointID string) string {
	return filepath.Join(dir, checkpointsDir, checkpointID+".json")
}

func checkpointHash(checkpoint Checkpoint) string {
	checkpointCopy := checkpoint
	checkpointCopy.Hash = ""
	raw, err := json.Marshal(checkpointCopy)
	if err != nil {
		return ""
	}
	return sha256Hex(raw)
}

func validateCheckpoint(checkpoint Checkpoint, threadID string) error {
	if checkpoint.ID == "" || checkpoint.ThreadID != threadID {
		return fmt.Errorf("%w: checkpoint thread mismatch", ErrJournalCorrupt)
	}
	if checkpoint.ParentID != "" {
		if err := validateThreadID(checkpoint.ParentID); err != nil {
			return fmt.Errorf("%w: invalid checkpoint parent: %v", ErrJournalCorrupt, err)
		}
	}
	if checkpoint.Hash == "" || checkpoint.Hash != checkpointHash(checkpoint) {
		return fmt.Errorf("%w: checkpoint hash mismatch", ErrJournalCorrupt)
	}
	return nil
}

func checkpointFromJournal(events []ThreadEvent, checkpointID string) (Checkpoint, error) {
	for _, event := range events {
		if event.Kind != EventContextCompacted {
			continue
		}
		var payload checkpointCommittedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return Checkpoint{}, err
		}
		if payload.Checkpoint.ID == checkpointID {
			return payload.Checkpoint, nil
		}
	}
	return Checkpoint{}, fmt.Errorf("checkpoint %q not found", checkpointID)
}

func cloneArtifactRef(ref *ArtifactRef) *ArtifactRef {
	if ref == nil {
		return nil
	}
	refCopy := *ref
	refCopy.Head = append([]byte(nil), ref.Head...)
	refCopy.Tail = append([]byte(nil), ref.Tail...)
	return &refCopy
}
