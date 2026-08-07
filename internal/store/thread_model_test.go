package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestThreadStoreModelBindingReplaysRequestedEffort(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, ThreadMeta{
		ID:              "thread-model-binding-replay",
		Model:           "model-a",
		ReasoningEffort: " medium ",
	}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.ReasoningEffort != "medium" {
		t.Fatalf("created effort = %q, want medium", state.Meta.ReasoningEffort)
	}

	state, err = threadStore.SetThreadModelBinding(ctx, state.ID, state.Revision, " model-b ", " high ")
	if err != nil {
		t.Fatalf("SetThreadModelBinding: %v", err)
	}
	if state.Meta.Model != "model-b" || state.Meta.ReasoningEffort != "high" {
		t.Fatalf("binding projection = %#v, want model-b/high", state.Meta)
	}

	reloadedStore, err := NewThreadStore(threadStore.Root())
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := reloadedStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread after replay: %v", err)
	}
	if reloaded.Meta.Model != "model-b" || reloaded.Meta.ReasoningEffort != "high" {
		t.Fatalf("replayed binding = %#v, want model-b/high", reloaded.Meta)
	}

	_, events, _, err := replayJournal(threadJournalPathForTest(t, threadStore, state.ID), state.ID)
	if err != nil {
		t.Fatal(err)
	}
	var created threadCreatedPayload
	if err := json.Unmarshal(events[0].Payload, &created); err != nil {
		t.Fatalf("decode created payload: %v", err)
	}
	if created.Meta.Model != "model-a" || created.Meta.ReasoningEffort != "medium" {
		t.Fatalf("created binding payload = %#v, want model-a/medium", created.Meta)
	}
	var changed ModelChange
	if err := json.Unmarshal(events[len(events)-1].Payload, &changed); err != nil {
		t.Fatalf("decode model change payload: %v", err)
	}
	if changed.Model != "model-b" || changed.ReasoningEffort != "high" {
		t.Fatalf("model change payload = %#v, want model-b/high", changed)
	}
	if events[len(events)-1].Revision != events[0].Revision+1 {
		t.Fatalf("model binding revision = %d, want one CAS mutation after creation", events[len(events)-1].Revision)
	}
}

func TestThreadStoreReplaysLegacyModelPayloadWithoutReasoningEffort(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-model-legacy-payload", Model: "model-a"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.SetThreadModel(ctx, state.ID, state.Revision, "model-b")
	if err != nil {
		t.Fatal(err)
	}

	journalPath := threadJournalPathForTest(t, threadStore, state.ID)
	_, events, _, err := replayJournal(journalPath, state.ID)
	if err != nil {
		t.Fatalf("replay journal before legacy assertions: %v", err)
	}
	for _, event := range events {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode event %s payload: %v", event.Kind, err)
		}
		if _, exists := payload["reasoning_effort"]; exists {
			t.Fatalf("legacy-compatible %s payload unexpectedly contains reasoning_effort: %s", event.Kind, event.Payload)
		}
	}

	reloadedStore, err := NewThreadStore(threadStore.Root())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reloadedStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread legacy payload: %v", err)
	}
	if loaded.Meta.Model != "model-b" || loaded.Meta.ReasoningEffort != "" {
		t.Fatalf("legacy replay = %#v, want model-b with empty effort", loaded.Meta)
	}
}

func TestThreadStoreModelBindingSupportsModelOnlyAndEffortOnly(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, ThreadMeta{
		ID:              "thread-model-binding-mutations",
		Model:           "model-a",
		ReasoningEffort: "low",
	}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.SetThreadModelBinding(ctx, state.ID, state.Revision, "model-b", "")
	if err != nil {
		t.Fatalf("model-only binding: %v", err)
	}
	if state.Meta.Model != "model-b" || state.Meta.ReasoningEffort != "" {
		t.Fatalf("model-only binding = %#v, want model-b with empty effort", state.Meta)
	}
	state, err = threadStore.SetThreadModelBinding(ctx, state.ID, state.Revision, "", "high")
	if err != nil {
		t.Fatalf("effort-only binding: %v", err)
	}
	if state.Meta.Model != "model-b" || state.Meta.ReasoningEffort != "high" {
		t.Fatalf("effort-only binding = %#v, want model-b/high", state.Meta)
	}
}

func TestThreadStoreSetThreadModelBindingRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, ThreadMeta{
		ID:              "thread-model-binding-stale",
		Model:           "model-a",
		ReasoningEffort: "low",
	}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.SetThreadTitle(ctx, state.ID, state.Revision, "external writer"); err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.SetThreadModelBinding(ctx, state.ID, state.Revision, "", "high"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale binding error = %v, want ErrRevisionConflict", err)
	}
	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.Model != "model-a" || loaded.Meta.ReasoningEffort != "low" || loaded.Revision != state.Revision+1 {
		t.Fatalf("stale binding mutated tuple: %#v", loaded)
	}
}

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
		ContextWindowTokens: 100,
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
		ContextWindowTokens: 120,
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

	_, events, _, err := replayJournal(threadJournalPathForTest(t, threadStore, state.ID), state.ID)
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
	if _, err := threadStore.SetThreadModelBinding(ctx, state.ID, activeRevision, "model-b", "high"); !errors.Is(err, ErrModelChangeActiveTurn) {
		t.Fatalf("active model binding error = %v, want ErrModelChangeActiveTurn", err)
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
	if _, err := threadStore.SetThreadModelBinding(ctx, state.ID, pendingRevision, "", "high"); !errors.Is(err, ErrModelChangePendingCompaction) {
		t.Fatalf("pending model binding error = %v, want ErrModelChangePendingCompaction", err)
	}
	unchanged, err = threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Revision != pendingRevision || unchanged.Meta.Model != "model-a" || unchanged.PendingCompaction == nil {
		t.Fatalf("pending rejection mutated thread: %#v", unchanged)
	}
}
