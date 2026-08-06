package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestThreadStoreSetThreadModelPreservesThreadProjection(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-model-projection", Model: "model-a"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "first"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:              "call-1",
		TurnID:              "turn-1",
		Operation:           UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        10,
		CompletionTokens:    3,
		ContextBudgetTokens: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID:   "turn-1",
		Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer", nil)},
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, state, err := threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:      "checkpoint-model",
		Payload: json.RawMessage(`{"schema_version":2,"summary":"retained"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-2", Input: "second"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:              "call-2",
		TurnID:              "turn-2",
		Operation:           UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        20,
		CompletionTokens:    4,
		ContextBudgetTokens: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID:   "turn-2",
		Messages: []*schema.Message{schema.UserMessage("second"), schema.AssistantMessage("answer two", nil)},
	})
	if err != nil {
		t.Fatal(err)
	}

	before := state
	_, beforeTranscript, err := threadStore.LoadThreadTranscript(ctx, state.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	beforeGroups, err := threadStore.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeCheckpoint, err := threadStore.LoadCheckpoint(ctx, state.ID, checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}

	after, err := threadStore.SetThreadModel(ctx, state.ID, state.Revision, "  model-b  ")
	if err != nil {
		t.Fatalf("SetThreadModel: %v", err)
	}
	if after.Revision != before.Revision+1 || after.HeadSequence != before.HeadSequence+1 {
		t.Fatalf("model change revision = %d/%d, want %d/%d", after.Revision, after.HeadSequence, before.Revision+1, before.HeadSequence+1)
	}
	if after.Meta.Model != "model-b" {
		t.Fatalf("model identity = %q, want model-b", after.Meta.Model)
	}
	if after.SystemPrompt != before.SystemPrompt || after.ActiveCheckpointID != before.ActiveCheckpointID || after.LastHash == before.LastHash {
		t.Fatalf("model change altered unrelated state: before=%#v after=%#v", before, after)
	}
	wantMeta := before.Meta
	wantMeta.Model = "model-b"
	wantMeta.UpdatedAt = after.Meta.UpdatedAt
	if !reflect.DeepEqual(after.Meta, wantMeta) {
		t.Fatalf("metadata projection changed beyond model identity: before=%#v after=%#v", wantMeta, after.Meta)
	}

	_, afterTranscript, err := threadStore.LoadThreadTranscript(ctx, state.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterTranscript, beforeTranscript) {
		t.Fatalf("transcript changed during model replacement: before=%#v after=%#v", beforeTranscript, afterTranscript)
	}
	afterGroups, err := threadStore.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterGroups, beforeGroups) {
		t.Fatalf("turn ledger changed during model replacement: before=%#v after=%#v", beforeGroups, afterGroups)
	}
	afterCheckpoint, err := threadStore.LoadCheckpoint(ctx, state.ID, checkpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterCheckpoint, beforeCheckpoint) {
		t.Fatalf("checkpoint changed during model replacement: before=%#v after=%#v", beforeCheckpoint, afterCheckpoint)
	}
	if after.Meta.LastContext == nil || after.Meta.LastContext.PromptTokens != 20 || after.Meta.ModelCallCount != 2 {
		t.Fatalf("context/usage projection changed during model replacement: %#v", after.Meta)
	}

	_, events, _, err := replayJournal(filepath.Join(threadStore.Root(), sessionsDirName, state.ID, journalFileName), state.ID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != EventModelChanged || last.ExpectedRevision != before.Revision || last.PreviousHash != before.LastHash {
		t.Fatalf("model event = %#v, want hash-chain predecessor revision %d", last, before.Revision)
	}
}

func TestThreadStoreSetThreadModelRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-model-stale", Model: "model-a"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.SetThreadTitle(ctx, state.ID, state.Revision, "external writer"); err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.SetThreadModel(ctx, state.ID, state.Revision, "model-b"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale SetThreadModel error = %v, want ErrRevisionConflict", err)
	}
	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Model != "model-a" || loaded.Revision != state.Revision+1 {
		t.Fatalf("stale model change mutated thread: %#v", loaded)
	}
}

func TestThreadStoreSetThreadModelRequiresIdleLifecycle(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-model-lifecycle", Model: "model-a"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "active-turn", Input: "work"})
	if err != nil {
		t.Fatal(err)
	}
	activeRevision := state.Revision
	if _, err := threadStore.SetThreadModel(ctx, state.ID, activeRevision, "model-b"); !errors.Is(err, ErrModelChangeActiveTurn) {
		t.Fatalf("active model change error = %v, want ErrModelChangeActiveTurn", err)
	}
	if _, err := threadStore.SetThreadModel(ctx, state.ID, activeRevision-1, "model-b"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale active model change error = %v, want ErrRevisionConflict", err)
	}
	unchanged, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != activeRevision || unchanged.Meta.Model != "model-a" {
		t.Fatalf("active rejection mutated thread: %#v", unchanged)
	}

	state, err = threadStore.FailTurn(ctx, state.ID, state.Revision, TurnFailure{TurnID: "active-turn", Error: "test cleanup"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: "pending-compaction"})
	if err != nil {
		t.Fatal(err)
	}
	pendingRevision := state.Revision
	if _, err := threadStore.SetThreadModel(ctx, state.ID, pendingRevision, "model-b"); !errors.Is(err, ErrModelChangePendingCompaction) {
		t.Fatalf("pending model change error = %v, want ErrModelChangePendingCompaction", err)
	}
	unchanged, err = threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != pendingRevision || unchanged.Meta.Model != "model-a" || unchanged.PendingCompaction == nil {
		t.Fatalf("pending rejection mutated thread: %#v", unchanged)
	}
}
