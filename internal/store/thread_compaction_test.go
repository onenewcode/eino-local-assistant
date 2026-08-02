package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestThreadStoreCompactionFailurePreservesActiveCheckpoint(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-compaction-failure"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, state, err := threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:             "checkpoint-before-failure",
		Payload:        json.RawMessage(`{"schema_version":2}`),
		SourceEventIDs: []string{"event-1"},
	})
	if err != nil {
		t.Fatalf("CommitCheckpoint: %v", err)
	}

	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID:     "compact-op-failed",
		Automatic:       true,
		Reason:          "invalid checkpoint provenance",
		AutoPaused:      true,
		AutoPauseReason: "automatic compaction failed: invalid checkpoint provenance",
	})
	if err != nil {
		t.Fatalf("RecordCompactionFailure: %v", err)
	}
	if state.ActiveCheckpointID != checkpoint.ID || !state.AutoCompactionPaused || state.AutoCompactionPauseReason == "" {
		t.Fatalf("failure changed active/pause state unexpectedly: %#v", state)
	}
	if state.LastCompaction == nil || state.LastCompaction.Status != CompactionOutcomeFailed || state.LastCompaction.OperationID != "compact-op-failed" {
		t.Fatalf("failure outcome = %#v", state.LastCompaction)
	}
	if _, err := threadStore.LoadCheckpoint(ctx, state.ID, checkpoint.ID); err != nil {
		t.Fatalf("LoadCheckpoint after failure: %v", err)
	}

	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread replay: %v", err)
	}
	if loaded.ActiveCheckpointID != checkpoint.ID || !loaded.AutoCompactionPaused || loaded.LastCompaction == nil || loaded.LastCompaction.Status != CompactionOutcomeFailed {
		t.Fatalf("replayed failure state = %#v", loaded)
	}

	if _, err := threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision-1, CompactionFailure{
		Automatic:       true,
		Reason:          "stale",
		AutoPaused:      true,
		AutoPauseReason: "automatic compaction failed: stale",
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale failure error = %v, want ErrRevisionConflict", err)
	}
}

func TestThreadStoreAutomaticLowGainFailureMayRemainUnpaused(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-low-gain-retry"}, "system")
	if err != nil {
		t.Fatal(err)
	}

	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID: "low-gain-1",
		Automatic:   true,
		Reason:      CompactionFailureReasonLowGain,
	})
	if err != nil {
		t.Fatalf("first low-gain failure: %v", err)
	}
	if state.AutoCompactionPaused || state.LowGainStreak != 1 || state.LastCompaction == nil || state.LastCompaction.Reason != CompactionFailureReasonLowGain {
		t.Fatalf("first low-gain state = %#v", state)
	}

	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID: "low-gain-2",
		Automatic:   true,
		Reason:      CompactionFailureReasonLowGain,
		// AutoPaused is recomputed under the write lock; caller must not supply it.
	})
	if err != nil {
		t.Fatalf("second low-gain failure: %v", err)
	}
	if !state.AutoCompactionPaused || state.LowGainStreak != 2 || state.AutoCompactionPauseReason == "" {
		t.Fatalf("second low-gain state = %#v", state)
	}

	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if !loaded.AutoCompactionPaused || loaded.LowGainStreak != 2 {
		t.Fatalf("replayed low-gain state = %#v", loaded)
	}
}

func TestThreadStoreHardAutomaticFailureClearsLowGainStreak(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-hard-fail-clears-streak"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID: "low-gain-before-hard",
		Automatic:   true,
		Reason:      CompactionFailureReasonLowGain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.LowGainStreak != 1 {
		t.Fatalf("low-gain streak = %d", state.LowGainStreak)
	}

	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID: "hard-fail",
		Automatic:   true,
		Reason:      "provider unavailable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !state.AutoCompactionPaused || state.LowGainStreak != 0 {
		t.Fatalf("hard failure state = %#v", state)
	}
}

func TestFinishCompactionRecomputesLowGainPauseFromLatestStreak(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-finish-low-gain-policy"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID:        "low-gain-1",
		Automatic:          true,
		Reason:             CompactionFailureReasonLowGain,
		MaxLowGainAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.AutoCompactionPaused || state.LowGainStreak != 1 {
		t.Fatalf("seed low-gain state = %#v", state)
	}

	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{
		OperationID: "pending-low-gain",
		Automatic:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Unrelated revision advance invalidates a stale CAS, forcing FinishCompaction.
	state, err = threadStore.SetThreadTitle(ctx, state.ID, state.Revision, "other writer")
	if err != nil {
		t.Fatal(err)
	}
	// Caller supplies a deliberately wrong pause decision; the store must
	// recompute from LowGainStreak=1 under the lock and pause on attempt 2.
	state, err = threadStore.FinishCompaction(ctx, state.ID, CompactionFailure{
		OperationID:        "pending-low-gain",
		Automatic:          true,
		Reason:             CompactionFailureReasonLowGain,
		MaxLowGainAttempts: 2,
		AutoPaused:         false,
	})
	if err != nil {
		t.Fatalf("FinishCompaction: %v", err)
	}
	if !state.AutoCompactionPaused || state.LowGainStreak != 2 || !strings.Contains(state.AutoCompactionPauseReason, "2 consecutive") {
		t.Fatalf("finish low-gain policy state = %#v", state)
	}
}

func TestCompactionFailureJournalsAbsoluteLowGainStreak(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-absolute-streak"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID: "absolute-1",
		Automatic:   true,
		Reason:      CompactionFailureReasonLowGain,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.LowGainStreak != 1 {
		t.Fatalf("projected streak = %d", state.LowGainStreak)
	}

	data, err := os.ReadFile(filepath.Join(threadStore.Root(), sessionsDirName, state.ID, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event ThreadEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if event.Kind != EventContextCompactionFailed {
			continue
		}
		var payload CompactionFailure
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode failure: %v", err)
		}
		if payload.OperationID != "absolute-1" {
			continue
		}
		found = true
		if payload.ResultingLowGainStreak == nil || *payload.ResultingLowGainStreak != 1 {
			t.Fatalf("journaled streak = %#v, want 1", payload.ResultingLowGainStreak)
		}
	}
	if !found {
		t.Fatal("compaction failure event not found in journal")
	}

	// Absolute streak must replay identically after a full reload.
	reloaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.LowGainStreak != 1 || reloaded.AutoCompactionPaused {
		t.Fatalf("reloaded state = %#v", reloaded)
	}
}

func TestFinishCompactionRebasesOnlyMatchingPendingOperation(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-finish-compaction"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{
		OperationID: "pending-operation",
		Automatic:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// An unrelated title write advances the original compactor's revision.
	state, err = threadStore.SetThreadTitle(ctx, state.ID, state.Revision, "changed elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	failure := CompactionFailure{
		OperationID:     "pending-operation",
		Automatic:       true,
		Reason:          "provider unavailable",
		AutoPaused:      true,
		AutoPauseReason: "automatic compaction failed: generation_failed",
	}
	if _, err := threadStore.FinishCompaction(ctx, state.ID, CompactionFailure{
		OperationID:     "different-operation",
		Automatic:       true,
		Reason:          "provider unavailable",
		AutoPaused:      true,
		AutoPauseReason: "automatic compaction failed: generation_failed",
	}); err == nil || !strings.Contains(err.Error(), "does not match pending") {
		t.Fatalf("wrong operation FinishCompaction error = %v", err)
	}
	stillPending, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stillPending.PendingCompaction == nil || stillPending.PendingCompaction.OperationID != "pending-operation" {
		t.Fatalf("wrong operation closed pending compaction: %#v", stillPending)
	}
	state, err = threadStore.FinishCompaction(ctx, state.ID, failure)
	if err != nil {
		t.Fatalf("FinishCompaction: %v", err)
	}
	if state.PendingCompaction != nil || !state.AutoCompactionPaused || state.LastCompaction == nil ||
		state.LastCompaction.Status != CompactionOutcomeFailed || state.LastCompaction.OperationID != "pending-operation" {
		t.Fatalf("finished compaction state = %#v", state)
	}
}

func TestThreadStoreCheckpointResetRetainsRawHistoryAndCheckpoint(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-checkpoint-reset"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "keep raw history"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID: "turn-1",
		Messages: []*schema.Message{
			schema.UserMessage("keep raw history"),
			schema.AssistantMessage("raw answer", nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, state, err := threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:             "legacy-active-checkpoint",
		Payload:        json.RawMessage(`{"schema_version":1}`),
		SourceEventIDs: []string{"event-1"},
		AutoPaused:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	state, err = threadStore.ResetIncompatibleCheckpoint(ctx, state.ID, state.Revision, CheckpointSchemaReset{
		OperationID:  "resume-reset",
		CheckpointID: checkpoint.ID,
		Reason:       "checkpoint schema version 1 is incompatible with version 2",
	})
	if err != nil {
		t.Fatalf("ResetIncompatibleCheckpoint: %v", err)
	}
	if state.ActiveCheckpointID != "" || state.LowGainStreak != 0 || state.AutoCompactionPaused {
		t.Fatalf("reset state = %#v", state)
	}
	if state.LastCompaction == nil || state.LastCompaction.Status != CompactionOutcomeCheckpointReset || state.LastCompaction.CheckpointID != checkpoint.ID {
		t.Fatalf("reset outcome = %#v", state.LastCompaction)
	}
	if _, err := threadStore.LoadCheckpoint(ctx, state.ID, checkpoint.ID); err != nil {
		t.Fatalf("old checkpoint was not retained: %v", err)
	}
	transcriptState, transcript, err := threadStore.LoadThreadTranscript(ctx, state.ID, 10)
	if err != nil {
		t.Fatalf("LoadThreadTranscript: %v", err)
	}
	if transcriptState.ActiveCheckpointID != "" || len(transcript) != 3 {
		t.Fatalf("raw history was not retained: state=%#v transcript=%#v", transcriptState, transcript)
	}

	if _, err := threadStore.ResetIncompatibleCheckpoint(ctx, state.ID, state.Revision, CheckpointSchemaReset{
		CheckpointID: checkpoint.ID,
		Reason:       "repeat reset",
	}); err == nil {
		t.Fatal("reset without matching active checkpoint succeeded")
	}
}

func TestCommitCheckpointRejectsDuplicateJournalIDAfterProjectionLoss(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-checkpoint-duplicate-id"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, state, err := threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:             "reused-checkpoint-id",
		Payload:        json.RawMessage(`{"schema_version":2}`),
		SourceEventIDs: []string{"event-one"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := threadStore.threadDir(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(checkpointPath(dir, checkpoint.ID)); err != nil {
		t.Fatal(err)
	}
	_, _, err = threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:             checkpoint.ID,
		ParentID:       checkpoint.ID,
		Payload:        json.RawMessage(`{"schema_version":2,"replacement":true}`),
		SourceEventIDs: []string{"event-two"},
	})
	if err == nil || !strings.Contains(err.Error(), "already exists in journal") {
		t.Fatalf("duplicate checkpoint error = %v", err)
	}
	loaded, err := threadStore.LoadCheckpoint(ctx, state.ID, checkpoint.ID)
	if err != nil {
		t.Fatalf("LoadCheckpoint after duplicate rejection: %v", err)
	}
	if loaded.Hash != checkpoint.Hash || string(loaded.Payload) != string(checkpoint.Payload) {
		t.Fatalf("journal checkpoint changed after duplicate rejection: %#v", loaded)
	}
}

func TestThreadStoreCompactionOperationIDUsageRules(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-compaction-operation"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: "compact-operation-1"})
	if err != nil {
		t.Fatalf("StartCompaction: %v", err)
	}
	compactionUsage := ModelUsage{
		CallID:           "compaction-call-1",
		Operation:        UsageOperationCompaction,
		OperationID:      "compact-operation-1",
		HasProviderUsage: true,
		PromptTokens:     7,
		CachedTokens:     3,
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, compactionUsage)
	if err != nil {
		t.Fatalf("RecordUsage compaction: %v", err)
	}
	if state.Meta.PromptTokens != 7 || state.Meta.CachedTokens != 3 {
		t.Fatalf("compaction usage projection = %#v", state.Meta)
	}
	if _, err := threadStore.RecordUsage(ctx, state.ID, compactionUsage); err != nil {
		t.Fatalf("idempotent compaction usage: %v", err)
	}

	conflicting := compactionUsage
	conflicting.OperationID = "different-operation"
	if _, err := threadStore.RecordUsage(ctx, state.ID, conflicting); !errors.Is(err, ErrUsageRecordConflict) {
		t.Fatalf("operation id conflict = %v, want ErrUsageRecordConflict", err)
	}
	if _, err := threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:      "agent-call-with-operation",
		TurnID:      "turn-1",
		Operation:   UsageOperationAgent,
		OperationID: "not-allowed",
	}); err == nil {
		t.Fatal("agent usage accepted a compaction operation id")
	}
}

func TestLoadCompactionUsageDeduplicatesReplayRetries(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-compaction-usage-replay"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: "compaction-operation-retry"})
	if err != nil {
		t.Fatalf("StartCompaction: %v", err)
	}
	usage := ModelUsage{
		CallID:           "compaction-call-retry",
		Operation:        UsageOperationCompaction,
		OperationID:      "compaction-operation-retry",
		HasProviderUsage: true,
		PromptTokens:     17,
		CachedTokens:     5,
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, usage)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := threadStore.threadDir(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Replay intentionally accepts an identical usage event as an idempotent
	// retry. Append one directly to exercise the query's replay parity.
	duplicate, err := newThreadEvent(state, EventUsageRecorded, state.ID, "", state.Revision, usage, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := appendJournalEvent(filepath.Join(dir, journalFileName), duplicate); err != nil {
		t.Fatal(err)
	}

	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.ModelCallCount != 1 || loaded.Meta.PromptTokens != 17 || loaded.Meta.CachedTokens != 5 {
		t.Fatalf("replay aggregate double counted duplicate usage: %#v", loaded.Meta)
	}
	usages, err := threadStore.LoadCompactionUsage(ctx, state.ID, usage.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(usages) != 1 || usages[0].CallID != usage.CallID || usages[0].PromptTokens != 17 || usages[0].CachedTokens != 5 || usages[0].TotalTokens != 17 {
		t.Fatalf("operation usage = %#v, want one normalized retry record", usages)
	}
}

func TestStartCompactionRejectsOperationIDReuseAfterTerminalOutcome(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-compaction-operation-id-reuse"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "compaction-operation-reused"
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: operationID})
	if err != nil {
		t.Fatalf("StartCompaction: %v", err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:           "compaction-operation-reused-call",
		Operation:        UsageOperationCompaction,
		OperationID:      operationID,
		HasProviderUsage: true,
		PromptTokens:     11,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID: operationID,
		Reason:      "provider unavailable",
	})
	if err != nil {
		t.Fatalf("RecordCompactionFailure: %v", err)
	}
	if _, err := threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: operationID}); err == nil || !strings.Contains(err.Error(), "already exists in journal") {
		t.Fatalf("reused operation id error = %v", err)
	}

	usages, err := threadStore.LoadCompactionUsage(ctx, state.ID, operationID)
	if err != nil {
		t.Fatalf("LoadCompactionUsage: %v", err)
	}
	if len(usages) != 1 || usages[0].CallID != "compaction-operation-reused-call" {
		t.Fatalf("operation usage = %#v, want only the first transaction", usages)
	}
}

func TestLoadThreadRejectsDuplicateStartedCompactionOperationID(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-compaction-duplicate-start"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "duplicate-start-operation"
	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID:     operationID,
		Automatic:       true,
		Reason:          "preflight failed before the operation started",
		AutoPaused:      true,
		AutoPauseReason: "automatic compaction failed before it started",
	})
	if err != nil {
		t.Fatalf("RecordCompactionFailure: %v", err)
	}
	if _, err := threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: operationID}); err == nil || !strings.Contains(err.Error(), "already exists in journal") {
		t.Fatalf("reused preflight operation id error = %v", err)
	}
	dir, err := threadStore.threadDir(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := newThreadEvent(state, EventContextCompactionStarted, state.ID, "", state.Revision, CompactionStart{OperationID: operationID}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := appendJournalEvent(filepath.Join(dir, journalFileName), duplicate); err != nil {
		t.Fatal(err)
	}

	if _, err := threadStore.LoadThread(ctx, state.ID); !errors.Is(err, ErrJournalCorrupt) || !strings.Contains(err.Error(), "duplicate compaction operation id") {
		t.Fatalf("LoadThread duplicate operation id error = %v", err)
	}
}

func TestCheckpointWithOperationIDRequiresPendingCompaction(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-compaction-checkpoint-operation-id"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "checkpoint-after-recovery"
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: operationID})
	if err != nil {
		t.Fatalf("StartCompaction: %v", err)
	}
	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID: operationID,
		Cancelled:   true,
		Reason:      "interrupted and recovered",
	})
	if err != nil {
		t.Fatalf("RecordCompactionFailure: %v", err)
	}
	input := CheckpointInput{
		ID:             "checkpoint-after-recovery",
		Payload:        json.RawMessage(`{"schema_version":2}`),
		SourceEventIDs: []string{"source-event"},
		OperationID:    operationID,
	}
	if _, _, err := threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, input); err == nil || !strings.Contains(err.Error(), "requires a pending compaction") {
		t.Fatalf("delayed checkpoint error = %v", err)
	}
	dir, err := threadStore.threadDir(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := checkpointFromInput(state.ID, state, input, time.Now().UTC())
	delayed, err := newThreadEvent(state, EventContextCompacted, state.ID, "", state.Revision, checkpointCommittedPayload{Checkpoint: checkpoint}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := appendJournalEvent(filepath.Join(dir, journalFileName), delayed); err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.LoadThread(ctx, state.ID); !errors.Is(err, ErrJournalCorrupt) || !strings.Contains(err.Error(), "requires a pending compaction") {
		t.Fatalf("replayed delayed checkpoint error = %v", err)
	}
}

func TestCompactionUsageWithOperationIDRequiresPendingOperation(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-compaction-usage-pending"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "usage-requires-pending"
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: operationID})
	if err != nil {
		t.Fatalf("StartCompaction: %v", err)
	}
	usage := ModelUsage{
		CallID:           "usage-before-terminal",
		Operation:        UsageOperationCompaction,
		OperationID:      operationID,
		HasProviderUsage: true,
		PromptTokens:     7,
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, usage)
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, state.Revision, CompactionFailure{
		OperationID: operationID,
		Reason:      "provider failed",
	})
	if err != nil {
		t.Fatalf("RecordCompactionFailure: %v", err)
	}
	if _, err := threadStore.RecordUsage(ctx, state.ID, usage); err != nil {
		t.Fatalf("idempotent usage retry after terminal: %v", err)
	}
	delayed := usage
	delayed.CallID = "usage-after-terminal"
	if _, err := threadStore.RecordUsage(ctx, state.ID, delayed); err == nil || !strings.Contains(err.Error(), "requires a pending compaction") {
		t.Fatalf("delayed compaction usage error = %v", err)
	}
	dir, err := threadStore.threadDir(state.ID)
	if err != nil {
		t.Fatal(err)
	}
	delayedEvent, err := newThreadEvent(state, EventUsageRecorded, state.ID, "", state.Revision, delayed, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := appendJournalEvent(filepath.Join(dir, journalFileName), delayedEvent); err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.LoadThread(ctx, state.ID); !errors.Is(err, ErrJournalCorrupt) || !strings.Contains(err.Error(), "requires a pending compaction") {
		t.Fatalf("replayed delayed compaction usage error = %v", err)
	}
}
