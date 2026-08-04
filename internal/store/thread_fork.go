package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const forkIDAttempts = 32

type forkSource struct {
	state         ThreadState
	events        []ThreadEvent
	boundaryIndex int
	boundaryTurn  string
	sourceHash    string
	artifacts     map[string]ArtifactRef
	fingerprint   sourceFingerprint
	lockPresent   bool
}

type forkCommitBoundary struct {
	index  int
	turnID string
}

var _ ThreadForkRepository = (*ThreadStore)(nil)

// ForkThread publishes a child ledger containing a complete committed prefix
// of sourceID. The source ledger is only read, and the child journal is rebuilt
// so none of the source envelope hashes or sequence numbers are reused.
func (s *ThreadStore) ForkThread(ctx context.Context, sourceID, childID, lastTurnID string) (ForkResult, error) {
	if s == nil {
		return ForkResult{}, errors.New("thread store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sourceID = strings.TrimSpace(sourceID)
	childID = strings.TrimSpace(childID)
	lastTurnID = strings.TrimSpace(lastTurnID)
	if err := validateThreadID(sourceID); err != nil {
		return ForkResult{}, err
	}
	if childID != "" {
		if err := validateThreadID(childID); err != nil {
			return ForkResult{}, err
		}
		if childID == sourceID {
			return ForkResult{}, errors.New("thread fork child must differ from source")
		}
	}
	if err := validateForkSessionsDirectory(filepath.Join(s.root, sessionsDirName)); err != nil {
		return ForkResult{}, err
	}

	var result ForkResult
	err := s.withReadOnlyThread(ctx, sourceID, func(sourceDir string, locked bool) error {
		source, err := s.readForkSource(ctx, sourceDir, sourceID, lastTurnID, locked)
		if err != nil {
			return err
		}
		resolvedChildID, err := s.resolveForkChildID(sourceID, childID)
		if err != nil {
			return err
		}
		childUnlock, err := s.holdLocalThreadLock(ctx, resolvedChildID)
		if err != nil {
			return err
		}
		defer childUnlock()
		if err := ensureForkDestinationAbsent(s.root, resolvedChildID); err != nil {
			return err
		}
		result, err = s.publishFork(ctx, sourceDir, resolvedChildID, source)
		return err
	})
	if err != nil {
		return ForkResult{}, err
	}
	return result, nil
}

func validateForkSessionsDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect fork sessions directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("fork sessions directory must be a real directory")
	}
	return nil
}

func (s *ThreadStore) resolveForkChildID(sourceID, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	for attempt := 0; attempt < forkIDAttempts; attempt++ {
		candidate := NewThreadID(time.Now().UTC())
		if candidate == sourceID {
			continue
		}
		dir, err := s.threadDir(candidate)
		if err != nil {
			return "", err
		}
		_, statErr := os.Lstat(dir)
		if errors.Is(statErr, os.ErrNotExist) {
			return candidate, nil
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect generated fork destination: %w", statErr)
		}
	}
	return "", fmt.Errorf("%w: generated child id attempts exhausted", ErrForkDestinationExists)
}

func ensureForkDestinationAbsent(root, childID string) error {
	dir := filepath.Join(root, sessionsDirName, childID)
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("%w: thread %q", ErrForkDestinationExists, childID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect fork destination: %w", err)
	}
	return nil
}

func (s *ThreadStore) readForkSource(ctx context.Context, sourceDir, sourceID, lastTurnID string, locked bool) (forkSource, error) {
	var lastErr error
	for attempt := 0; attempt < stableReadAttempts; attempt++ {
		if err := stableReadContextError(ctx); err != nil {
			return forkSource{}, err
		}
		if err := validateForkSourceLayout(sourceDir); err != nil {
			return forkSource{}, err
		}
		before, lockPresent, err := fingerprintForkSource(sourceDir)
		if err != nil {
			if !errors.Is(err, errThreadSourceChanged) {
				return forkSource{}, err
			}
			lastErr = err
			if err := waitForStableReadRetry(ctx, attempt); err != nil {
				return forkSource{}, err
			}
			continue
		}
		if !locked && lockPresent {
			lastErr = fmt.Errorf("%w: source lock appeared", ErrForkSourceChanged)
			if err := waitForStableReadRetry(ctx, attempt); err != nil {
				return forkSource{}, err
			}
			continue
		}

		state, events, tornTail, readErr := readForkJournal(sourceDir, sourceID)
		after, afterLockPresent, fingerprintErr := fingerprintForkSource(sourceDir)
		if fingerprintErr != nil {
			if !errors.Is(fingerprintErr, errThreadSourceChanged) {
				return forkSource{}, fingerprintErr
			}
			lastErr = fingerprintErr
		} else if (!locked && afterLockPresent) || before != after {
			lastErr = fmt.Errorf("%w: source changed during fork read", ErrForkSourceChanged)
		} else if readErr != nil {
			return forkSource{}, readErr
		} else if tornTail {
			return forkSource{}, fmt.Errorf("%w: torn journal tail cannot be forked", ErrJournalCorrupt)
		} else {
			analyzed, analyzeErr := analyzeForkSource(sourceDir, sourceID, lastTurnID, state, events)
			if analyzeErr != nil {
				return forkSource{}, analyzeErr
			}
			analyzed.fingerprint = before
			analyzed.lockPresent = lockPresent
			return analyzed, nil
		}
		if err := waitForStableReadRetry(ctx, attempt); err != nil {
			return forkSource{}, err
		}
	}
	if lastErr == nil {
		lastErr = ErrForkSourceChanged
	}
	return forkSource{}, fmt.Errorf("%w: retry limit reached", lastErr)
}

func readForkJournal(sourceDir, sourceID string) (ThreadState, []ThreadEvent, bool, error) {
	path := filepath.Join(sourceDir, journalFileName)
	info, err := os.Lstat(path)
	if err != nil {
		return ThreadState{}, nil, false, fmt.Errorf("inspect fork source journal: %w", err)
	}
	if err := validateSnapshotRegularFile(info, journalFileName); err != nil {
		return ThreadState{}, nil, false, err
	}
	state, events, tornTail, err := replayJournalReadOnly(path, sourceID)
	if err != nil {
		return ThreadState{}, nil, false, err
	}
	return state, events, tornTail, nil
}

func validateForkSourceLayout(sourceDir string) error {
	for _, name := range []string{stateFileName, metaFileName} {
		path := filepath.Join(sourceDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect fork source file %q: %w", name, err)
		}
		if err := validateSnapshotRegularFile(info, name); err != nil {
			return err
		}
	}
	for _, name := range []string{checkpointsDir, artifactsDir, locksDir} {
		path := filepath.Join(sourceDir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect fork source directory %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("fork source directory %q must be a real directory", name)
		}
	}
	checkpointPath := filepath.Join(sourceDir, checkpointsDir)
	if entries, err := os.ReadDir(checkpointPath); err == nil {
		if len(entries) != 0 {
			return fmt.Errorf("%w: checkpoint files are not copied by v1 fork", ErrForkUnsupportedState)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read fork source checkpoints: %w", err)
	}
	return nil
}

func fingerprintForkSource(sourceDir string) (sourceFingerprint, bool, error) {
	hashValue := sha256.New()
	if err := fingerprintSourceFile(hashValue, journalFileName, filepath.Join(sourceDir, journalFileName), true); err != nil {
		return sourceFingerprint{}, false, err
	}
	for _, name := range []string{stateFileName, metaFileName} {
		if err := fingerprintSourceFile(hashValue, name, filepath.Join(sourceDir, name), false); err != nil {
			return sourceFingerprint{}, false, err
		}
	}
	for _, name := range []string{checkpointsDir, artifactsDir} {
		if err := fingerprintSnapshotDirectory(hashValue, filepath.Join(sourceDir, name), name); err != nil {
			return sourceFingerprint{}, false, err
		}
	}
	lockPresent, err := fingerprintSourceLock(hashValue, filepath.Join(sourceDir, locksDir, writeLockName))
	if err != nil {
		return sourceFingerprint{}, false, err
	}
	return fingerprintFromHash(hashValue), lockPresent, nil
}

func analyzeForkSource(sourceDir, sourceID, lastTurnID string, state ThreadState, events []ThreadEvent) (forkSource, error) {
	if state.ID != sourceID {
		return forkSource{}, fmt.Errorf("%w: source state id %q does not match %q", ErrJournalCorrupt, state.ID, sourceID)
	}
	if state.PendingCompaction != nil {
		return forkSource{}, ErrForkPendingCompaction
	}
	tracker, err := lifecycleFromEvents(events)
	if err != nil {
		return forkSource{}, fmt.Errorf("%w: %v", ErrJournalCorrupt, err)
	}
	if tracker.activeTurnID != "" {
		return forkSource{}, fmt.Errorf("%w: %s", ErrForkActiveTurn, tracker.activeTurnID)
	}

	commits := make([]forkCommitBoundary, 0)
	for index, event := range events {
		switch event.Kind {
		case EventTaskStateUpdated, EventContextCompactionStarted, EventContextCompacted,
			EventContextCompactionFailed, EventContextCheckpointReset:
			return forkSource{}, fmt.Errorf("%w: event %q", ErrForkUnsupportedState, event.Kind)
		case EventUsageRecorded:
			var usage ModelUsage
			if err := json.Unmarshal(event.Payload, &usage); err != nil {
				return forkSource{}, fmt.Errorf("%w: decode usage: %v", ErrJournalCorrupt, err)
			}
			normalized, err := normalizeModelUsage(usage)
			if err != nil {
				return forkSource{}, fmt.Errorf("%w: invalid usage: %v", ErrJournalCorrupt, err)
			}
			if normalized.Operation == UsageOperationCompaction {
				return forkSource{}, fmt.Errorf("%w: compaction usage is not copied by v1 fork", ErrForkUnsupportedState)
			}
		case EventToolCompleted:
			var payload ToolCompleted
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return forkSource{}, fmt.Errorf("%w: decode tool completion: %v", ErrJournalCorrupt, err)
			}
			if payload.Artifact != nil {
				if err := validateForkArtifactRef(sourceDir, *payload.Artifact); err != nil {
					return forkSource{}, fmt.Errorf("%w: tool %q artifact: %v", ErrJournalCorrupt, payload.ToolCallID, err)
				}
			}
		case EventTurnCommitted:
			var payload TurnCommit
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return forkSource{}, fmt.Errorf("%w: decode turn commit: %v", ErrJournalCorrupt, err)
			}
			if err := validateMessages(payload.Messages); err != nil {
				return forkSource{}, fmt.Errorf("%w: turn %q commit: %v", ErrForkInvalidBoundary, event.TurnID, err)
			}
			commits = append(commits, forkCommitBoundary{index: index, turnID: event.TurnID})
		}
	}
	if len(commits) == 0 {
		return forkSource{}, ErrForkNoCommittedTurn
	}

	boundary := commits[len(commits)-1]
	if lastTurnID != "" {
		found := false
		for _, candidate := range commits {
			if candidate.turnID == lastTurnID {
				boundary = candidate
				found = true
				break
			}
		}
		if !found {
			return forkSource{}, fmt.Errorf("%w: turn %q is not a committed boundary", ErrForkInvalidBoundary, lastTurnID)
		}
	}

	artifacts := make(map[string]ArtifactRef)
	for _, event := range events[:boundary.index+1] {
		if event.Kind != EventToolCompleted {
			continue
		}
		var payload ToolCompleted
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return forkSource{}, fmt.Errorf("%w: decode boundary tool completion: %v", ErrJournalCorrupt, err)
		}
		if payload.Artifact != nil {
			artifacts[payload.Artifact.Digest] = *payload.Artifact
		}
	}
	return forkSource{
		state:         state,
		events:        events,
		boundaryIndex: boundary.index,
		boundaryTurn:  boundary.turnID,
		sourceHash:    events[boundary.index].Hash,
		artifacts:     artifacts,
	}, nil
}

func validateForkArtifactRef(dir string, ref ArtifactRef) error {
	digest, err := artifactDigestFromID(ref.ID)
	if err != nil || digest != ref.SHA256 || digest != ref.Digest {
		return errors.New("artifact reference identity is inconsistent")
	}
	var stored storedArtifact
	if err := readJSON(artifactMetadataPath(dir, digest), &stored); err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}
	if !sameForkArtifactRef(stored.Ref, ref) {
		return errors.New("artifact metadata does not match event reference")
	}
	return validateArtifactRef(dir, ref)
}

func sameForkArtifactRef(first, second ArtifactRef) bool {
	return first.ID == second.ID &&
		first.SHA256 == second.SHA256 &&
		first.Digest == second.Digest &&
		first.Kind == second.Kind &&
		first.MediaType == second.MediaType &&
		first.Size == second.Size &&
		first.OriginalSize == second.OriginalSize &&
		first.StoredSize == second.StoredSize &&
		first.Truncated == second.Truncated &&
		bytes.Equal(first.Head, second.Head) &&
		bytes.Equal(first.Tail, second.Tail)
}

func (s *ThreadStore) publishFork(ctx context.Context, sourceDir, childID string, source forkSource) (ForkResult, error) {
	if err := ensureForkDestinationAbsent(s.root, childID); err != nil {
		return ForkResult{}, err
	}
	parent := filepath.Join(s.root, sessionsDirName)
	stagingDir, err := os.MkdirTemp(parent, ".fork-")
	if err != nil {
		return ForkResult{}, fmt.Errorf("create fork staging directory: %w", err)
	}
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		_ = os.RemoveAll(stagingDir)
		return ForkResult{}, fmt.Errorf("set fork staging permissions: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stagingDir)
		}
	}()

	for _, name := range []string{checkpointsDir, artifactsDir} {
		if err := os.Mkdir(filepath.Join(stagingDir, name), 0o700); err != nil {
			return ForkResult{}, fmt.Errorf("create fork %s directory: %w", name, err)
		}
	}
	childEvents, childState, err := rebuildForkEvents(childID, source)
	if err != nil {
		return ForkResult{}, err
	}
	if err := copyForkArtifacts(sourceDir, stagingDir, source.artifacts); err != nil {
		return ForkResult{}, err
	}
	journal, err := encodeForkJournal(childEvents)
	if err != nil {
		return ForkResult{}, err
	}
	if err := writeBytesAtomic(filepath.Join(stagingDir, journalFileName), journal); err != nil {
		return ForkResult{}, fmt.Errorf("write fork journal: %w", err)
	}
	if err := s.materializeState(stagingDir, childState); err != nil {
		return ForkResult{}, fmt.Errorf("write fork projections: %w", err)
	}
	if err := syncDirectory(stagingDir); err != nil {
		return ForkResult{}, fmt.Errorf("sync fork staging directory: %w", err)
	}
	if err := stableReadContextError(ctx); err != nil {
		return ForkResult{}, err
	}
	after, lockPresent, err := fingerprintForkSource(sourceDir)
	if err != nil {
		return ForkResult{}, err
	}
	if lockPresent != source.lockPresent {
		return ForkResult{}, fmt.Errorf("%w: source lock appeared before publish", ErrForkSourceChanged)
	}
	if after != source.fingerprint {
		return ForkResult{}, fmt.Errorf("%w: source changed before publish", ErrForkSourceChanged)
	}
	if err := ensureForkDestinationAbsent(s.root, childID); err != nil {
		return ForkResult{}, err
	}
	if err := stableReadContextError(ctx); err != nil {
		return ForkResult{}, err
	}
	if err := os.Rename(stagingDir, filepath.Join(parent, childID)); err != nil {
		if _, statErr := os.Lstat(filepath.Join(parent, childID)); statErr == nil {
			return ForkResult{}, fmt.Errorf("%w: thread %q", ErrForkDestinationExists, childID)
		}
		return ForkResult{}, fmt.Errorf("publish fork thread: %w", err)
	}
	cleanup = false
	// The child directory is complete before rename. Like CreateThread, treat
	// the parent directory sync as a best-effort durability hint so a successful
	// publication is not reported as a failed fork.
	_ = syncDirectory(parent)
	return ForkResult{
		SourceID:   source.state.ID,
		ChildID:    childID,
		LastTurnID: source.boundaryTurn,
		SourceHash: source.sourceHash,
		ChildState: childState,
	}, nil
}

func rebuildForkEvents(childID string, source forkSource) ([]ThreadEvent, ThreadState, error) {
	childState := ThreadState{FormatVersion: ThreadFormatVersion, ID: childID}
	childEvents := make([]ThreadEvent, 0, source.boundaryIndex+1)
	tracker := newLifecycleTracker()
	for index, sourceEvent := range source.events[:source.boundaryIndex+1] {
		payload := append(json.RawMessage(nil), sourceEvent.Payload...)
		if index == 0 {
			var created threadCreatedPayload
			if err := json.Unmarshal(payload, &created); err != nil {
				return nil, ThreadState{}, fmt.Errorf("decode fork thread.created: %w", err)
			}
			created.Meta.ID = childID
			created.Meta.ParentID = source.state.ID
			created.Meta.ForkBoundaryTurnID = source.boundaryTurn
			created.Meta.ForkSourceHash = source.sourceHash
			var err error
			payload, err = json.Marshal(created)
			if err != nil {
				return nil, ThreadState{}, fmt.Errorf("encode fork thread.created: %w", err)
			}
		}
		event := ThreadEvent{
			Version:          ThreadFormatVersion,
			Sequence:         uint64(index + 1),
			ID:               newRandomID("evt"),
			ThreadID:         childID,
			Timestamp:        sourceEvent.Timestamp.UTC(),
			Kind:             sourceEvent.Kind,
			TurnID:           sourceEvent.TurnID,
			CorrelationID:    sourceEvent.CorrelationID,
			ExpectedRevision: uint64(index),
			Revision:         uint64(index + 1),
			Payload:          payload,
			PayloadHash:      sha256Hex(payload),
			PreviousHash:     childState.LastHash,
		}
		event.Hash = threadEventHash(event)
		if err := validateThreadEvent(event, childState); err != nil {
			return nil, ThreadState{}, fmt.Errorf("validate rebuilt fork event %d: %w", index+1, err)
		}
		if err := tracker.apply(event); err != nil {
			return nil, ThreadState{}, fmt.Errorf("validate rebuilt fork lifecycle %d: %w", index+1, err)
		}
		if err := applyThreadEvent(&childState, event); err != nil {
			return nil, ThreadState{}, fmt.Errorf("apply rebuilt fork event %d: %w", index+1, err)
		}
		childEvents = append(childEvents, event)
	}
	if childState.ID != childID || childState.Meta.ID != childID {
		return nil, ThreadState{}, fmt.Errorf("%w: rebuilt child id mismatch", ErrJournalCorrupt)
	}
	return childEvents, childState, nil
}

func encodeForkJournal(events []ThreadEvent) ([]byte, error) {
	var journal bytes.Buffer
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("encode fork journal event: %w", err)
		}
		journal.Write(encoded)
		journal.WriteByte('\n')
	}
	return journal.Bytes(), nil
}

func copyForkArtifacts(sourceDir, stagingDir string, refs map[string]ArtifactRef) error {
	digests := make([]string, 0, len(refs))
	for digest := range refs {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for _, digest := range digests {
		ref := refs[digest]
		if err := copyForkArtifact(sourceDir, stagingDir, ref); err != nil {
			return err
		}
	}
	return nil
}

func copyForkArtifact(sourceDir, stagingDir string, ref ArtifactRef) error {
	digest := ref.Digest
	for _, suffix := range []string{".json", ".blob"} {
		if suffix == ".blob" && ref.Truncated {
			continue
		}
		sourcePath := filepath.Join(sourceDir, artifactsDir, digest+suffix)
		destinationPath := filepath.Join(stagingDir, artifactsDir, digest+suffix)
		info, err := os.Lstat(sourcePath)
		if err != nil {
			return fmt.Errorf("inspect fork artifact %q: %w", digest+suffix, err)
		}
		if err := validateSnapshotRegularFile(info, digest+suffix); err != nil {
			return err
		}
		if err := copySnapshotFile(sourcePath, destinationPath, info); err != nil {
			return fmt.Errorf("copy fork artifact %q: %w", digest+suffix, err)
		}
	}
	return nil
}
