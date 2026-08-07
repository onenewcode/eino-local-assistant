package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

type forkCommitBoundary struct {
	index  int
	turnID string
}

type forkBoundary struct {
	beforeFirst bool
	lastTurnID  string
}

var (
	_ ThreadForkRepository            = (*ThreadStore)(nil)
	_ ThreadForkBeforeFirstRepository = (*ThreadStore)(nil)
)

// ForkThread publishes a child session file containing a complete committed
// prefix. The child rebuilds its event chain rather than sharing source IDs.
func (s *ThreadStore) ForkThread(ctx context.Context, sourceID, childID, lastTurnID string) (ForkResult, error) {
	return s.forkThread(ctx, sourceID, childID, forkBoundary{lastTurnID: lastTurnID})
}

// ForkThreadBeforeFirstTurn publishes a child containing only a rebuilt
// thread.created event and source provenance.
func (s *ThreadStore) ForkThreadBeforeFirstTurn(ctx context.Context, sourceID, childID string) (ForkResult, error) {
	return s.forkThread(ctx, sourceID, childID, forkBoundary{beforeFirst: true})
}

func (s *ThreadStore) forkThread(ctx context.Context, sourceID, childID string, boundary forkBoundary) (ForkResult, error) {
	if s == nil {
		return ForkResult{}, errors.New("thread store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sourceID = strings.TrimSpace(sourceID)
	childID = strings.TrimSpace(childID)
	boundary.lastTurnID = strings.TrimSpace(boundary.lastTurnID)
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

	var result ForkResult
	err := s.withReadOnlyThread(ctx, sourceID, func(sourceDir string, _ bool) error {
		state, events, tornTail, err := replayJournalReadOnly(journalPath(sourceDir, sourceID), sourceID)
		if err != nil {
			return err
		}
		if tornTail {
			return fmt.Errorf("%w: torn journal tail cannot be forked", ErrJournalCorrupt)
		}
		source, err := analyzeForkSource(sourceID, boundary, state, events)
		if err != nil {
			return err
		}
		resolvedChildID, err := s.resolveForkChildID(sourceID, childID)
		if err != nil {
			return err
		}
		unlock, err := s.holdLocalThreadLock(ctx, resolvedChildID)
		if err != nil {
			return err
		}
		defer unlock()
		childPath := s.newThreadJournalPath(resolvedChildID, time.Now().UTC())
		if err := s.ensureForkDestinationAbsent(childPath, resolvedChildID); err != nil {
			return err
		}
		result, err = s.publishFork(ctx, childPath, resolvedChildID, source)
		return err
	})
	if err != nil {
		return ForkResult{}, err
	}
	return result, nil
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
		path := s.newThreadJournalPath(candidate, time.Now().UTC())
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("inspect generated fork destination: %w", err)
		}
	}
	return "", fmt.Errorf("%w: generated child id attempts exhausted", ErrForkDestinationExists)
}

func (s *ThreadStore) ensureForkDestinationAbsent(path, childID string) error {
	if err := s.ensureThreadIDAbsent(childID, path); err != nil {
		if errors.Is(err, errThreadAlreadyExists) {
			return fmt.Errorf("%w: thread %q", ErrForkDestinationExists, childID)
		}
		return fmt.Errorf("inspect fork destination: %w", err)
	}
	return nil
}

func analyzeForkSource(sourceID string, boundaryMode forkBoundary, state ThreadState, events []ThreadEvent) (forkSource, error) {
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
		case EventTaskStateUpdated, EventContextCompactionStarted, EventContextCompacted, EventContextCompactionFailed, EventContextCheckpointReset:
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
				if err := validateArtifactRef(*payload.Artifact); err != nil {
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

	var boundary forkCommitBoundary
	if boundaryMode.beforeFirst {
		if len(events) == 0 {
			return forkSource{}, fmt.Errorf("%w: source has no creation event", ErrJournalCorrupt)
		}
		boundary = forkCommitBoundary{index: 0}
	} else {
		if len(commits) == 0 {
			return forkSource{}, ErrForkNoCommittedTurn
		}
		boundary = commits[len(commits)-1]
		if boundaryMode.lastTurnID != "" {
			found := false
			for _, candidate := range commits {
				if candidate.turnID == boundaryMode.lastTurnID {
					boundary, found = candidate, true
					break
				}
			}
			if !found {
				return forkSource{}, fmt.Errorf("%w: turn %q is not a committed boundary", ErrForkInvalidBoundary, boundaryMode.lastTurnID)
			}
		}
	}
	return forkSource{state: state, events: events, boundaryIndex: boundary.index, boundaryTurn: boundary.turnID, sourceHash: events[boundary.index].Hash}, nil
}

func (s *ThreadStore) publishFork(ctx context.Context, childPath, childID string, source forkSource) (ForkResult, error) {
	if err := s.ensureForkDestinationAbsent(childPath, childID); err != nil {
		return ForkResult{}, err
	}
	childEvents, childState, err := rebuildForkEvents(childID, source)
	if err != nil {
		return ForkResult{}, err
	}
	journal, err := encodeForkJournal(childEvents)
	if err != nil {
		return ForkResult{}, err
	}
	childDayDir := filepath.Dir(childPath)
	if err := os.MkdirAll(childDayDir, 0o700); err != nil {
		return ForkResult{}, fmt.Errorf("create fork session date directory: %w", err)
	}
	tmp, err := os.CreateTemp(childDayDir, ".fork-")
	if err != nil {
		return ForkResult{}, fmt.Errorf("create temporary fork journal: %w", err)
	}
	tmpJournal := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpJournal)
		return ForkResult{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpJournal)
		return ForkResult{}, err
	}
	defer func() { _ = os.Remove(tmpJournal) }()
	if err := writeBytesAtomic(tmpJournal, journal); err != nil {
		return ForkResult{}, fmt.Errorf("write fork journal: %w", err)
	}
	if err := stableReadContextError(ctx); err != nil {
		return ForkResult{}, err
	}
	if err := s.ensureForkDestinationAbsent(childPath, childID); err != nil {
		return ForkResult{}, err
	}
	if err := publishNewJournal(tmpJournal, childPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ForkResult{}, fmt.Errorf("%w: thread %q", ErrForkDestinationExists, childID)
		}
		return ForkResult{}, fmt.Errorf("publish fork session journal: %w", err)
	}
	_ = s.projectThread(childDayDir, childState, childEvents)
	return ForkResult{SourceID: source.state.ID, ChildID: childID, LastTurnID: source.boundaryTurn, SourceHash: source.sourceHash, ChildState: childState}, nil
}

func rebuildForkEvents(childID string, source forkSource) ([]ThreadEvent, ThreadState, error) {
	childState := ThreadState{FormatVersion: SessionJournalFormatVersion, ID: childID, SystemPrompt: source.state.SystemPrompt}
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
			created.Meta.Title = source.state.Meta.Title
			created.Meta.Model = source.state.Meta.Model
			created.Meta.ReasoningEffort = source.state.Meta.ReasoningEffort
			created.Meta.ParentID = source.state.ID
			created.Meta.ForkBoundaryTurnID = source.boundaryTurn
			created.Meta.ForkSourceHash = source.sourceHash
			var err error
			payload, err = json.Marshal(created)
			if err != nil {
				return nil, ThreadState{}, fmt.Errorf("encode fork thread.created: %w", err)
			}
		}
		event := ThreadEvent{Version: SessionJournalFormatVersion, Sequence: uint64(index + 1), ID: newRandomID("evt"), ThreadID: childID, Timestamp: sourceEvent.Timestamp.UTC(), Kind: sourceEvent.Kind, TurnID: sourceEvent.TurnID, CorrelationID: sourceEvent.CorrelationID, ExpectedRevision: uint64(index), Revision: uint64(index + 1), Payload: payload, PayloadHash: sha256Hex(payload), PreviousHash: childState.LastHash}
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
