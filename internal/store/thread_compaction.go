package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// StartCompaction writes the durable operation boundary before any provider
// call can charge usage. A pending operation must reach a terminal event or be
// explicitly recovered on resume.
func (s *ThreadStore) StartCompaction(ctx context.Context, id string, expectedRevision uint64, input CompactionStart) (ThreadState, error) {
	input.OperationID = strings.TrimSpace(input.OperationID)
	if input.OperationID == "" {
		return ThreadState{}, errors.New("compaction operation id is required")
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, err
	}
	defer unlock()
	state, _, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, err
	}
	if err := checkExpectedRevision(state, expectedRevision); err != nil {
		return ThreadState{}, err
	}
	if hasRecordedCompactionOperationID(state, input.OperationID) {
		return ThreadState{}, fmt.Errorf("compaction operation %q already exists in journal", input.OperationID)
	}
	if state.PendingCompaction != nil {
		return ThreadState{}, fmt.Errorf("compaction operation %q is already pending", state.PendingCompaction.OperationID)
	}
	return s.appendLocked(dir, state, expectedRevision, EventContextCompactionStarted, "", input)
}

// RecordCompactionFailure records an unsuccessful compact transaction without
// installing a replacement view. Automatic pause and low-gain streak updates
// are decided under the write lock from the latest projected state.
func (s *ThreadStore) RecordCompactionFailure(ctx context.Context, id string, expectedRevision uint64, input CompactionFailure) (ThreadState, error) {
	input, err := normalizeCompactionFailureInput(input)
	if err != nil {
		return ThreadState{}, err
	}

	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, err
	}
	defer unlock()
	state, _, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, err
	}
	if err := checkExpectedRevision(state, expectedRevision); err != nil {
		return ThreadState{}, err
	}
	if state.PendingCompaction != nil {
		if input.OperationID != state.PendingCompaction.OperationID || input.Automatic != state.PendingCompaction.Automatic {
			return ThreadState{}, fmt.Errorf("compaction failure does not match pending operation %q", state.PendingCompaction.OperationID)
		}
	} else if input.OperationID != "" && hasRecordedCompactionOperationID(state, input.OperationID) {
		return ThreadState{}, fmt.Errorf("compaction operation %q already exists in journal", input.OperationID)
	}
	input, err = materializeCompactionFailure(state, input)
	if err != nil {
		return ThreadState{}, err
	}
	return s.appendLocked(dir, state, expectedRevision, EventContextCompactionFailed, "", input)
}

// FinishCompaction atomically closes the exact pending compaction after an
// unrelated revision invalidated the original candidate CAS. Pause policy is
// re-evaluated against the latest LowGainStreak under the write lock.
func (s *ThreadStore) FinishCompaction(ctx context.Context, id string, input CompactionFailure) (ThreadState, error) {
	input, err := normalizeCompactionFailureInput(input)
	if err != nil {
		return ThreadState{}, err
	}
	if input.OperationID == "" {
		return ThreadState{}, errors.New("compaction operation id is required")
	}

	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, err
	}
	defer unlock()
	state, _, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, err
	}
	if state.PendingCompaction == nil {
		return ThreadState{}, fmt.Errorf("%w: %q", ErrCompactionOperationNotPending, input.OperationID)
	}
	if input.OperationID != state.PendingCompaction.OperationID || input.Automatic != state.PendingCompaction.Automatic {
		return ThreadState{}, fmt.Errorf("%w: compaction failure does not match pending operation %q", ErrCompactionOperationNotPending, state.PendingCompaction.OperationID)
	}
	input, err = materializeCompactionFailure(state, input)
	if err != nil {
		return ThreadState{}, err
	}
	return s.appendLocked(dir, state, state.Revision, EventContextCompactionFailed, "", input)
}

// normalizeCompactionFailureInput trims identity fields. Automatic pause fields
// are filled later under the write lock so concurrent writers cannot latch a
// stale LowGainStreak decision.
func normalizeCompactionFailureInput(input CompactionFailure) (CompactionFailure, error) {
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.Reason = strings.TrimSpace(input.Reason)
	input.AutoPauseReason = strings.TrimSpace(input.AutoPauseReason)
	if !input.Cancelled && input.Reason == "" {
		return CompactionFailure{}, errors.New("compaction failure reason is required")
	}
	if input.MaxLowGainAttempts < 0 {
		return CompactionFailure{}, errors.New("max low gain attempts must be >= 0")
	}
	if input.Automatic {
		// Drop any caller-supplied pause/streak decision. The store recomputes them.
		input.AutoPaused = false
		input.AutoPauseReason = ""
		input.ResultingLowGainStreak = nil
		return input, nil
	}
	if input.AutoPaused && input.AutoPauseReason == "" {
		return CompactionFailure{}, errors.New("auto pause reason is required")
	}
	if !input.AutoPaused && input.AutoPauseReason != "" {
		return CompactionFailure{}, errors.New("auto pause reason requires auto pause")
	}
	return input, nil
}

// materializeCompactionFailure binds automatic pause policy to the locked
// thread projection. Manual failures keep the caller's AutoPaused snapshot.
func materializeCompactionFailure(state ThreadState, input CompactionFailure) (CompactionFailure, error) {
	if !input.Automatic {
		return input, nil
	}
	input = applyAutomaticCompactionFailurePolicy(state, input)
	if input.AutoPaused && input.AutoPauseReason == "" {
		return CompactionFailure{}, errors.New("auto pause reason is required")
	}
	if !input.AutoPaused && input.AutoPauseReason != "" {
		return CompactionFailure{}, errors.New("auto pause reason requires auto pause")
	}
	if !input.AutoPaused && !allowsUnpausedAutomaticCompactionFailure(input) {
		return CompactionFailure{}, errors.New("automatic compaction failures must pause automatic compaction")
	}
	return input, nil
}

// applyAutomaticCompactionFailurePolicy is the single owner of automatic pause
// and low-gain streak interaction. It stamps ResultingLowGainStreak so journal
// write and replay share one absolute projection instead of dual increment.
func applyAutomaticCompactionFailurePolicy(state ThreadState, input CompactionFailure) CompactionFailure {
	switch {
	case isStaleCompactionFailure(input):
		input.AutoPaused = false
		input.AutoPauseReason = ""
		input.ResultingLowGainStreak = nil
	case isLowGainCompactionFailure(input):
		nextStreak := state.LowGainStreak + 1
		input.ResultingLowGainStreak = uint64Ptr(nextStreak)
		maxAttempts := uint64(input.MaxLowGainAttempts)
		if maxAttempts == 0 {
			maxAttempts = DefaultMaxLowGainAttempts
		}
		if nextStreak >= maxAttempts {
			input.AutoPaused = true
			input.AutoPauseReason = fmt.Sprintf(
				"automatic compaction paused after %d consecutive low-gain attempts",
				nextStreak,
			)
		} else {
			input.AutoPaused = false
			input.AutoPauseReason = ""
		}
	default:
		input.AutoPaused = true
		input.AutoPauseReason = "automatic compaction failed: " + input.Reason
		input.ResultingLowGainStreak = uint64Ptr(0)
	}
	return input
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}

func allowsUnpausedAutomaticCompactionFailure(input CompactionFailure) bool {
	return isStaleCompactionFailure(input) || isLowGainCompactionFailure(input)
}

func isStaleCompactionFailure(input CompactionFailure) bool {
	return input.Automatic && input.Cancelled && input.Reason == CompactionFailureReasonStale
}

func isLowGainCompactionFailure(input CompactionFailure) bool {
	return input.Automatic && !input.Cancelled && input.Reason == CompactionFailureReasonLowGain
}

// ResetIncompatibleCheckpoint clears only the active checkpoint pointer after
// a schema upgrade. The raw event ledger and immutable old checkpoint remain
// available for audit, but the next prompt is rebuilt from raw groups.
func (s *ThreadStore) ResetIncompatibleCheckpoint(ctx context.Context, id string, expectedRevision uint64, input CheckpointSchemaReset) (ThreadState, error) {
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.CheckpointID = strings.TrimSpace(input.CheckpointID)
	input.Reason = strings.TrimSpace(input.Reason)
	input.AutoPauseReason = strings.TrimSpace(input.AutoPauseReason)
	if input.CheckpointID == "" || input.Reason == "" {
		return ThreadState{}, errors.New("checkpoint reset id and reason are required")
	}
	if input.AutoPaused && input.AutoPauseReason == "" {
		return ThreadState{}, errors.New("auto pause reason is required")
	}
	if !input.AutoPaused && input.AutoPauseReason != "" {
		return ThreadState{}, errors.New("auto pause reason requires auto pause")
	}

	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, err
	}
	defer unlock()
	state, _, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, err
	}
	if err := checkExpectedRevision(state, expectedRevision); err != nil {
		return ThreadState{}, err
	}
	if state.ActiveCheckpointID == "" || state.ActiveCheckpointID != input.CheckpointID {
		return ThreadState{}, fmt.Errorf("checkpoint reset target %q does not match active checkpoint %q", input.CheckpointID, state.ActiveCheckpointID)
	}
	return s.appendLocked(dir, state, expectedRevision, EventContextCheckpointReset, "", input)
}
