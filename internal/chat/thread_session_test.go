package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

type checkpointCompactorFunc func(context.Context, contextbuild.CompactionRequest, contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error)

func (f checkpointCompactorFunc) Compact(ctx context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
	return f(ctx, request, observer)
}

type usageRecordingRepository struct {
	store.ThreadRepository

	mu      sync.Mutex
	records []store.ModelUsage
}

type titleMutationBeforeUsageRepository struct {
	store.ThreadRepository

	mu      sync.Mutex
	match   func(store.ModelUsage) bool
	mutated bool
}

type titleMutationBeforeCompactionFailureRepository struct {
	store.ThreadRepository

	mu                  sync.Mutex
	remaining           int
	replaceBeforeFinish bool
	replacementStarted  bool
}

func (r *titleMutationBeforeUsageRepository) RecordUsage(ctx context.Context, id string, input store.ModelUsage) (store.ThreadState, error) {
	r.mu.Lock()
	shouldMutate := !r.mutated && (r.match == nil || r.match(input))
	if shouldMutate {
		r.mutated = true
	}
	r.mu.Unlock()
	if shouldMutate {
		state, err := r.LoadThread(ctx, id)
		if err != nil {
			return store.ThreadState{}, err
		}
		if _, err := r.SetThreadTitle(ctx, id, state.Revision, "external writer"); err != nil {
			return store.ThreadState{}, err
		}
	}
	return r.ThreadRepository.RecordUsage(ctx, id, input)
}

func (r *titleMutationBeforeUsageRepository) didMutate() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.mutated
}

func (r *titleMutationBeforeCompactionFailureRepository) RecordCompactionFailure(ctx context.Context, id string, expectedRevision uint64, input store.CompactionFailure) (store.ThreadState, error) {
	r.mu.Lock()
	shouldMutate := r.remaining > 0
	if shouldMutate {
		r.remaining--
	}
	r.mu.Unlock()
	if shouldMutate {
		state, err := r.LoadThread(ctx, id)
		if err != nil {
			return store.ThreadState{}, err
		}
		if _, err := r.SetThreadTitle(ctx, id, state.Revision, fmt.Sprintf("external writer %d", state.Revision)); err != nil {
			return store.ThreadState{}, err
		}
	}
	return r.ThreadRepository.RecordCompactionFailure(ctx, id, expectedRevision, input)
}

func (r *titleMutationBeforeCompactionFailureRepository) FinishCompaction(ctx context.Context, id string, input store.CompactionFailure) (store.ThreadState, error) {
	r.mu.Lock()
	shouldReplace := r.replaceBeforeFinish && !r.replacementStarted
	if shouldReplace {
		r.replacementStarted = true
	}
	r.mu.Unlock()
	if shouldReplace {
		state, err := r.LoadThread(ctx, id)
		if err != nil {
			return store.ThreadState{}, err
		}
		state, err = r.ThreadRepository.RecordCompactionFailure(ctx, id, state.Revision, input)
		if err != nil {
			return store.ThreadState{}, err
		}
		if _, err := r.ThreadRepository.StartCompaction(ctx, id, state.Revision, store.CompactionStart{OperationID: "replacement-operation"}); err != nil {
			return store.ThreadState{}, err
		}
	}
	return r.ThreadRepository.FinishCompaction(ctx, id, input)
}

func (r *usageRecordingRepository) RecordUsage(ctx context.Context, id string, input store.ModelUsage) (store.ThreadState, error) {
	r.mu.Lock()
	r.records = append(r.records, input)
	r.mu.Unlock()
	return r.ThreadRepository.RecordUsage(ctx, id, input)
}

func (r *usageRecordingRepository) usageRecords() []store.ModelUsage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]store.ModelUsage(nil), r.records...)
}

func assistantWithProviderUsage(content string, promptTokens, completionTokens int) *schema.Message {
	answer := schema.AssistantMessage(content, nil)
	answer.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      promptTokens + completionTokens,
	}}
	return answer
}

func askThreadTestTurns(t *testing.T, session *Session, inputs ...string) {
	t.Helper()
	for _, input := range inputs {
		if err := session.Ask(context.Background(), compactionTestInput(input), nil); err != nil {
			t.Fatalf("Ask(%q): %v", input, err)
		}
	}
}

// Keep successful compaction tests above the configured gain floor. The
// fixture checkpoint is deliberately structured and therefore not useful for
// tiny one-line source turns.
func compactionTestInput(input string) string {
	return input + " " + strings.Repeat("retained evidence ", 300)
}

const validLegacyV1CheckpointJSON = `{"schema_version":1,"source_range":{"from":"event-1","to":"event-1","content_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","event_ids":["event-1"]},"source_event_ids":["event-1"],"source_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","task_goal":"resume legacy task","constraints":[{"text":"constraint","source_event_ids":["event-1"],"confidence":"observed"}],"confirmed_facts":[{"text":"fact","source_event_ids":["event-1"],"confidence":"observed"}],"decisions":[{"decision":"decision","reason":"reason","source_event_ids":["event-1"],"confidence":"observed"}],"attempts_and_results":[{"text":"attempt","result":"result","source_event_ids":["event-1"],"confidence":"observed"}],"files_or_artifacts":[{"ref":"event://event-1","description":"source","source_event_ids":["event-1"],"confidence":"observed"}],"open_questions":[{"text":"question","source_event_ids":["event-1"],"confidence":"unknown"}],"next_actions":[{"text":"next","source_event_ids":["event-1"],"confidence":"inferred"}]}`

func usageCompactionConfig() contextbuild.Config {
	return contextbuild.Config{
		WindowTokens:              8_000,
		MaxOutputTokens:           1_000,
		KeepRecentTurns:           1,
		LowGainThresholdPercent:   1,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
	}
}

func TestThreadSessionCompactionRecordsUsageAndClearsContextSnapshot(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("first answer", 10, 1)}}},
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("second answer", 20, 2)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		if observer == nil {
			t.Fatal("compactor usage observer is nil")
		}
		observer("model", usage.Turn{PromptTokens: 30, CompletionTokens: 3, TotalTokens: 33, CachedTokens: 4}, true)
		return contextbuild.DeterministicCheckpoint(request)
	})
	opts := SessionOptions{
		Store:     threadStore,
		Compactor: compactor,
		Context:   usageCompactionConfig(),
	}
	session, err := NewSession(model, "system", opts)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	askThreadTestTurns(t, session, "first", "second")
	if status := session.ContextStatus(); !status.MeasuredKnown || status.MeasuredTokens != 20 {
		t.Fatalf("context before compaction = %+v, want last agent prompt snapshot", status)
	}

	result, err := session.Compact(ctx, "preserve decisions")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.CheckpointID == "" || result.OperationID == "" {
		t.Fatalf("compaction result has no checkpoint: %+v", result)
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.PromptTokens != 60 || state.Meta.CompletionTokens != 6 || state.Meta.TotalTokens != 66 || state.Meta.ModelCallCount != 3 {
		t.Fatalf("compaction usage projection = %#v", state.Meta)
	}
	if state.Meta.UsageStatus != store.UsageStatusExact {
		t.Fatalf("usage status = %q, want exact", state.Meta.UsageStatus)
	}
	if state.LastCompaction == nil || state.LastCompaction.Status != store.CompactionOutcomeSucceeded || state.LastCompaction.OperationID != result.OperationID || state.AutoCompactionPauseReason != "" {
		t.Fatalf("successful compaction outcome = %#v state=%#v", state.LastCompaction, state)
	}
	if compactUsage := session.ContextStatus().LastCompactionUsage; compactUsage == nil || compactUsage.OperationID != result.OperationID || compactUsage.ModelCallCount != 1 || compactUsage.CachedTokens != 4 || compactUsage.Status != store.UsageStatusExact {
		t.Fatalf("compaction operation usage = %#v", compactUsage)
	}
	if state.Meta.LastContext != nil || session.ContextStatus().MeasuredKnown {
		t.Fatalf("compaction must clear context snapshot: meta=%#v status=%+v", state.Meta.LastContext, session.ContextStatus())
	}

	resumed, err := OpenSession(&scriptedModel{}, threadStore, session.ID(), opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if summary := resumed.UsageSummary(); summary.PromptTokens != 60 || summary.CompletionTokens != 6 || summary.TotalTokens != 66 || summary.ModelCallCount != 3 || summary.Status != store.UsageStatusExact {
		t.Fatalf("resumed usage summary = %+v", summary)
	}
	if resumed.ContextStatus().MeasuredKnown {
		t.Fatalf("resumed compaction context snapshot remained known: %+v", resumed.ContextStatus())
	}
	if compactUsage := resumed.ContextStatus().LastCompactionUsage; compactUsage == nil || compactUsage.OperationID != result.OperationID || compactUsage.CachedTokens != 4 {
		t.Fatalf("resumed compaction operation usage = %#v", compactUsage)
	}
}

func TestThreadSessionCompactionRecordsUsageForInvalidCheckpoint(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("first answer", 10, 1)}}},
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("second answer", 20, 2)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, _ contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		if observer == nil {
			t.Fatal("compactor usage observer is nil")
		}
		observer("model", usage.Turn{PromptTokens: 7, CompletionTokens: 3, TotalTokens: 10}, true)
		// RecursiveCompactor must account for this completed API call before it
		// rejects the invalid checkpoint without installing a fallback.
		return contextbuild.Checkpoint{}, nil
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     threadStore,
		Compactor: compactor,
		Context:   usageCompactionConfig(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	askThreadTestTurns(t, session, "first", "second")

	_, err = session.Compact(ctx, "preserve facts")
	if err == nil {
		t.Fatal("invalid compactor checkpoint was accepted")
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.PromptTokens != 37 || state.Meta.CompletionTokens != 6 || state.Meta.TotalTokens != 43 || state.Meta.ModelCallCount != 3 {
		t.Fatalf("invalid checkpoint lost compactor usage: %#v", state.Meta)
	}
	if state.Meta.LastContext == nil {
		t.Fatalf("failed compaction cleared the existing context snapshot: %#v", state.Meta)
	}
	if state.ActiveCheckpointID != "" || state.LastCompaction == nil || state.LastCompaction.Status != store.CompactionOutcomeFailed || state.AutoCompactionPaused {
		t.Fatalf("invalid checkpoint failure state = %#v", state)
	}
}

func TestThreadSessionCompactionUsageCallbackIDsAreUniqueAndIdempotent(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	repository := &usageRecordingRepository{ThreadRepository: threadStore}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("one", 5, 1)}}},
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("two", 5, 1)}}},
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("three", 5, 1)}}},
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("four", 5, 1)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		if observer == nil {
			t.Fatal("compactor usage observer is nil")
		}
		observer("model", usage.Turn{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}, true)
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     repository,
		Compactor: compactor,
		Context:   usageCompactionConfig(),
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	askThreadTestTurns(t, session, "one", "two", "three")
	if _, err := session.Compact(ctx, "first compact"); err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	askThreadTestTurns(t, session, "four")
	if _, err := session.Compact(ctx, "second compact"); err != nil {
		t.Fatalf("second Compact: %v", err)
	}

	var compactionRecords []store.ModelUsage
	for _, record := range repository.usageRecords() {
		if record.Operation == store.UsageOperationCompaction {
			compactionRecords = append(compactionRecords, record)
		}
	}
	if len(compactionRecords) != 2 {
		t.Fatalf("compaction usage records = %#v, want two callbacks", compactionRecords)
	}
	if compactionRecords[0].CallID == "" || compactionRecords[0].CallID == compactionRecords[1].CallID {
		t.Fatalf("compaction callback IDs are not unique: %#v", compactionRecords)
	}
	beforeRetry, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread before retry: %v", err)
	}
	afterRetry, err := threadStore.RecordUsage(ctx, session.ID(), compactionRecords[0])
	if err != nil {
		t.Fatalf("idempotent compaction usage retry: %v", err)
	}
	if afterRetry.Revision != beforeRetry.Revision || afterRetry.Meta.ModelCallCount != beforeRetry.Meta.ModelCallCount || afterRetry.Meta.TotalTokens != beforeRetry.Meta.TotalTokens {
		t.Fatalf("duplicate callback record changed usage: before=%#v after=%#v", beforeRetry.Meta, afterRetry.Meta)
	}
}

func TestThreadSessionCompactionReplayDoesNotDoubleCountUsage(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("one", 5, 1)}}},
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("two", 5, 1)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		turn := usage.Turn{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}
		observer("replayed-call", turn, true)
		observer("replayed-call", turn, true)
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     threadStore,
		Compactor: compactor,
		Context:   usageCompactionConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	askThreadTestTurns(t, session, "one", "two")
	if _, err := session.Compact(ctx, "preserve facts"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.ModelCallCount != 3 || state.Meta.TotalTokens != 20 {
		t.Fatalf("replayed compaction usage was counted twice: %#v", state.Meta)
	}
}

func TestThreadSessionCompactionPersistsUsageButRejectsRebasedCandidate(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := &titleMutationBeforeUsageRepository{
		ThreadRepository: threadStore,
		match: func(input store.ModelUsage) bool {
			return input.Operation == store.UsageOperationCompaction
		},
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("one", 5, 1)}}},
		&scriptedStream{events: []streamEvent{{message: assistantWithProviderUsage("two", 5, 1)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		observer("model", usage.Turn{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}, true)
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     repository,
		Compactor: compactor,
		Context:   usageCompactionConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	askThreadTestTurns(t, session, "one", "two")
	_, err = session.Compact(ctx, "preserve facts")
	if !errors.Is(err, ErrCompactionStale) {
		t.Fatalf("Compact error = %v, want ErrCompactionStale", err)
	}
	if !repository.didMutate() {
		t.Fatal("test did not inject an external revision")
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.ModelCallCount != 3 || state.Meta.TotalTokens != 20 {
		t.Fatalf("rebased compaction usage was lost: %#v", state.Meta)
	}
	if state.ActiveCheckpointID != "" {
		t.Fatalf("stale compaction installed checkpoint %q", state.ActiveCheckpointID)
	}
}

func TestAutomaticCompactionStaleCandidateDoesNotPauseRetries(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := &titleMutationBeforeUsageRepository{
		ThreadRepository: threadStore,
		match: func(input store.ModelUsage) bool {
			return input.Operation == store.UsageOperationCompaction
		},
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage(strings.Repeat("large answer ", 700), nil)}}},
	}}
	calls := 0
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		calls++
		observer(fmt.Sprintf("model-%d", calls), usage.Turn{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}, true)
		return contextbuild.DeterministicCheckpoint(request)
	})
	opts := SessionOptions{
		Store:     repository,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens:              1_200,
			MaxOutputTokens:           200,
			KeepRecentTurns:           12,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			LowGainThresholdPercent:   1,
		},
	}
	session, err := NewSession(model, "system", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(ctx, "small input", nil); err != nil {
		t.Fatal(err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatalf("automatic compaction was not requested: %+v", session.ContextStatus())
	}
	if _, err := session.CompactAutomatically(ctx); !errors.Is(err, ErrCompactionStale) {
		t.Fatalf("first CompactAutomatically error = %v, want ErrCompactionStale", err)
	}
	state, err := threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingCompaction != nil || state.AutoCompactionPaused || state.AutoCompactionPauseReason != "" || state.LastCompaction == nil || state.LastCompaction.Status != store.CompactionOutcomeCancelled || !state.LastCompaction.Automatic || state.LastCompaction.Reason != store.CompactionFailureReasonStale {
		t.Fatalf("stale automatic compaction state = %#v", state)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatalf("stale automatic candidate disabled retries: %+v", session.ContextStatus())
	}
	if _, err := session.CompactAutomatically(ctx); err != nil {
		t.Fatalf("automatic retry after stale candidate: %v", err)
	}
	state, err = threadStore.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.AutoCompactionPaused || state.LastCompaction == nil || state.LastCompaction.Status != store.CompactionOutcomeSucceeded || calls != 2 {
		t.Fatalf("automatic retry state = %#v calls=%d", state, calls)
	}
}

func TestStaleRecursiveCompactionCancelsRemainingProviderCalls(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "thread-stale-recursive-compaction"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		turnID := fmt.Sprintf("turn-%d", i)
		state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: turnID, Input: "source"})
		if err != nil {
			t.Fatal(err)
		}
		state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{TurnID: turnID, Messages: []*schema.Message{
			schema.UserMessage(strings.Repeat("recursive source evidence ", 3_000)),
			schema.AssistantMessage("recorded answer", nil),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	repository := &titleMutationBeforeUsageRepository{
		ThreadRepository: threadStore,
		match: func(input store.ModelUsage) bool {
			return input.Operation == store.UsageOperationCompaction
		},
	}
	calls := 0
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		calls++
		observer(fmt.Sprintf("recursive-call-%d", calls), usage.Turn{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8}, true)
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := OpenSession(&scriptedModel{}, repository, state.ID, SessionOptions{
		Store:     repository,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens:              6_000,
			MaxOutputTokens:           500,
			KeepRecentTurns:           1,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			LowGainThresholdPercent:   1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Compact(ctx, "preserve recursive evidence"); !errors.Is(err, ErrCompactionStale) {
		t.Fatalf("Compact error = %v, want ErrCompactionStale", err)
	}
	if calls != 1 {
		t.Fatalf("stale recursive compaction made %d provider calls, want 1", calls)
	}
	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCompaction != nil || loaded.ActiveCheckpointID != "" || loaded.LastCompaction == nil || loaded.LastCompaction.Status != store.CompactionOutcomeCancelled {
		t.Fatalf("stale recursive compaction state = %#v", loaded)
	}
}

func TestCancelledStaleAutomaticCompactionClosesWithoutPause(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-cancelled-stale-automatic"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{
		OperationID: "cancelled-stale-operation",
		Automatic:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{threads: st, id: state.ID}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := session.persistCompactionFailure(cancelled, state, "cancelled-stale-operation", true, ErrCompactionStale); !errors.Is(err, ErrCompactionStale) {
		t.Fatalf("persistCompactionFailure error = %v, want ErrCompactionStale", err)
	}
	loaded, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCompaction != nil || loaded.AutoCompactionPaused || loaded.LastCompaction == nil || loaded.LastCompaction.Status != store.CompactionOutcomeCancelled || loaded.LastCompaction.Reason != store.CompactionFailureReasonStale {
		t.Fatalf("cancelled stale operation state = %#v", loaded)
	}
}

func TestCompactionFailureReconcilesAfterRepeatedConcurrentRevisions(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "thread-compaction-failure-reconcile"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{
		OperationID: "charged-operation",
		Automatic:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &titleMutationBeforeCompactionFailureRepository{
		ThreadRepository: threadStore,
		remaining:        2,
	}
	session := &Session{threads: repository, id: state.ID}
	cause := errors.New("provider unavailable after charging")
	if err := session.persistCompactionFailure(ctx, state, "charged-operation", true, cause); !errors.Is(err, cause) {
		t.Fatalf("persistCompactionFailure error = %v, want provider failure", err)
	}

	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCompaction != nil || !loaded.AutoCompactionPaused || loaded.LastCompaction == nil ||
		loaded.LastCompaction.Status != store.CompactionOutcomeFailed || loaded.LastCompaction.OperationID != "charged-operation" {
		t.Fatalf("reconciled compaction failure state = %#v", loaded)
	}
}

func TestPreflightCompactionFailureWithStaleRevisionDoesNotCreateOperation(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "thread-stale-compaction-preflight"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.SetThreadTitle(ctx, state.ID, state.Revision, "external writer"); err != nil {
		t.Fatal(err)
	}
	session := &Session{threads: threadStore, id: state.ID}
	if err := session.persistCompactionFailure(ctx, state, "preflight-operation", true, errors.New("artifact unavailable")); !errors.Is(err, ErrCompactionStale) {
		t.Fatalf("persistCompactionFailure error = %v, want ErrCompactionStale", err)
	}
	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCompaction != nil || loaded.LastCompaction != nil || loaded.AutoCompactionPaused {
		t.Fatalf("stale preflight changed compaction state: %#v", loaded)
	}
}

func TestStaleCompactionFailureCannotCloseNewerPendingOperation(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "thread-stale-compaction-operation"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	started, err := threadStore.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{
		OperationID: "old-operation",
		Automatic:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordCompactionFailure(ctx, state.ID, started.Revision, store.CompactionFailure{
		OperationID:     "old-operation",
		Automatic:       true,
		Reason:          "provider unavailable",
		AutoPaused:      true,
		AutoPauseReason: "automatic compaction failed: generation_failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{
		OperationID: "new-operation",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &Session{threads: threadStore, id: state.ID}
	if err := session.persistCompactionFailure(ctx, started, "old-operation", true, errors.New("late provider failure")); !errors.Is(err, ErrCompactionStale) {
		t.Fatalf("persistCompactionFailure error = %v, want ErrCompactionStale", err)
	}
	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCompaction == nil || loaded.PendingCompaction.OperationID != "new-operation" || loaded.LastCompaction == nil || loaded.LastCompaction.OperationID != "old-operation" {
		t.Fatalf("late failure changed newer operation: %#v", loaded)
	}
}

func TestCompactionFailureHandlesReplacementDuringFinishReconciliation(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(ctx, store.ThreadMeta{ID: "thread-compaction-finish-replacement"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	started, err := threadStore.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{
		OperationID: "old-operation",
		Automatic:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &titleMutationBeforeCompactionFailureRepository{
		ThreadRepository:    threadStore,
		remaining:           1,
		replaceBeforeFinish: true,
	}
	session := &Session{threads: repository, id: state.ID}
	if err := session.persistCompactionFailure(ctx, started, "old-operation", true, errors.New("late provider failure")); !errors.Is(err, ErrCompactionStale) {
		t.Fatalf("persistCompactionFailure error = %v, want ErrCompactionStale", err)
	}
	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCompaction == nil || loaded.PendingCompaction.OperationID != "replacement-operation" || loaded.LastCompaction == nil || loaded.LastCompaction.OperationID != "old-operation" {
		t.Fatalf("finish reconciliation changed replacement operation: %#v", loaded)
	}
}

func TestThreadSessionCompactionRetainsRawTurnsAndUsesCheckpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("first answer", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("second answer", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("third answer", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("after answer", nil)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	opts := SessionOptions{
		Store:     st,
		Title:     "thread compaction",
		Compactor: compactor,
		Context: contextbuild.Config{
			KeepRecentTurns: 1,
		},
	}
	session, err := NewSession(model, "system", opts)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for _, input := range []string{"first unique source", "second unique source", "third live turn"} {
		if err := session.Ask(ctx, compactionTestInput(input), nil); err != nil {
			t.Fatalf("Ask(%q): %v", input, err)
		}
	}

	result, err := session.Compact(ctx, "preserve decisions")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.CheckpointID == "" || len(result.SourceEventIDs) == 0 {
		t.Fatalf("compaction result = %+v", result)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.ActiveCheckpointID != result.CheckpointID {
		t.Fatalf("active checkpoint = %q, want %q", state.ActiveCheckpointID, result.CheckpointID)
	}
	groups, err := st.LoadTurnGroups(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 3 || groups[0].Committed == nil || groups[1].Committed == nil || groups[2].Committed == nil {
		t.Fatalf("raw committed groups were not retained: %#v", groups)
	}
	if got := groups[0].Committed.Messages[0].Content; !strings.HasPrefix(got, "first unique source ") {
		t.Fatalf("first raw source = %q", got)
	}

	resumed, err := OpenSession(model, st, session.ID(), opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := resumed.Ask(ctx, "continue", nil); err != nil {
		t.Fatalf("Ask after resume: %v", err)
	}
	request := model.requests[len(model.requests)-1]
	var checkpointVisible bool
	for _, message := range request {
		if message != nil && message.Role == schema.System && strings.Contains(message.Content, "Structured checkpoint") {
			checkpointVisible = true
		}
		if message != nil && (strings.Contains(message.Content, "first unique source") || strings.Contains(message.Content, "second unique source")) {
			t.Fatalf("covered raw turn leaked into post-compaction prompt: %#v", request)
		}
	}
	if !checkpointVisible {
		t.Fatalf("post-compaction prompt has no checkpoint: %#v", request)
	}
}

func TestCheckpointLineageKeepsHotPayloadBoundedAcrossRepeatedCompaction(t *testing.T) {
	const turns = 22
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	streams := make([]Stream, 0, turns+1)
	for i := 0; i <= turns; i++ {
		streams = append(streams, &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage(fmt.Sprintf("answer-%02d", i), nil)}}})
	}
	model := &scriptedModel{streams: streams}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	opts := SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens:              8_000,
			MaxOutputTokens:           1_000,
			KeepRecentTurns:           1,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			LowGainThresholdPercent:   1,
		},
	}
	session, err := NewSession(model, "system", opts)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for i := 0; i < turns; i++ {
		if err := session.Ask(ctx, compactionTestInput(fmt.Sprintf("turn-%02d", i)), nil); err != nil {
			t.Fatalf("Ask(%d): %v", i, err)
		}
		if i == 0 {
			continue
		}
		if _, err := session.Compact(ctx, "retain progress"); err != nil {
			t.Fatalf("Compact after turn %d: %v", i, err)
		}
	}

	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	lineage, err := st.LoadCheckpointLineage(ctx, session.ID(), state.ActiveCheckpointID)
	if err != nil {
		t.Fatalf("LoadCheckpointLineage: %v", err)
	}
	if got, want := len(lineage), turns-1; got != want {
		t.Fatalf("lineage length = %d, want %d", got, want)
	}
	var directIDs []string
	for _, checkpoint := range lineage {
		directIDs = append(directIDs, checkpoint.SourceEventIDs...)
	}
	if got := len(uniqueSourceEventIDs(directIDs)); got <= contextbuild.MaxCheckpointEvidenceRefs {
		t.Fatalf("cold lineage did not retain more than the hot evidence cap: %d", got)
	}
	persisted, err := st.LoadCheckpoint(ctx, session.ID(), state.ActiveCheckpointID)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	checkpoint, err := contextbuild.ParseCheckpointJSON(persisted.Payload)
	if err != nil {
		t.Fatalf("ParseCheckpointJSON: %v", err)
	}
	if len(checkpoint.DirectEvidenceEventIDs()) > contextbuild.MaxCheckpointEvidenceRefs {
		t.Fatalf("hot checkpoint leaked complete source manifest: %d refs", len(checkpoint.DirectEvidenceEventIDs()))
	}
	if checkpoint.EstimatedTokens() > opts.Context.Normalize().SummaryMaxTokens {
		t.Fatalf("hot checkpoint exceeds summary budget: %d", checkpoint.EstimatedTokens())
	}

	resumed, err := OpenSession(model, st, session.ID(), opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if err := resumed.Ask(ctx, "continue", nil); err != nil {
		t.Fatalf("Ask after resume: %v", err)
	}
	request := model.requests[len(model.requests)-1]
	for _, message := range request {
		if message != nil && strings.Contains(message.Content, "turn-00") {
			t.Fatalf("ancestor-covered raw turn leaked into resumed prompt: %#v", request)
		}
	}
}

func TestThreadSessionCancelledCompactionLeavesActiveCheckpointUnchanged(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("one", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("two", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("three", nil)}}},
	}}
	calls := 0
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, observer contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		calls++
		if calls == 1 {
			return contextbuild.DeterministicCheckpoint(request)
		}
		observer("cancelled-call", usage.Turn{PromptTokens: 11, CompletionTokens: 1, TotalTokens: 12}, true)
		return contextbuild.Checkpoint{}, context.Canceled
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			KeepRecentTurns:         1,
			LowGainThresholdPercent: 1,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for _, input := range []string{"first", "second"} {
		if err := session.Ask(ctx, compactionTestInput(input), nil); err != nil {
			t.Fatalf("Ask(%q): %v", input, err)
		}
	}
	first, err := session.Compact(ctx, "preserve first range")
	if err != nil {
		t.Fatalf("first Compact: %v", err)
	}
	if err := session.Ask(ctx, compactionTestInput("third"), nil); err != nil {
		t.Fatalf("Ask(third): %v", err)
	}
	_, err = session.Compact(ctx, "preserve facts")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact error = %v, want context.Canceled", err)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.ActiveCheckpointID != first.CheckpointID {
		t.Fatalf("cancelled compact replaced checkpoint %q, want %q", state.ActiveCheckpointID, first.CheckpointID)
	}
	if state.LastCompaction == nil || state.LastCompaction.Status != store.CompactionOutcomeCancelled || state.LastCompaction.Automatic || state.AutoCompactionPaused {
		t.Fatalf("cancelled compaction state = %#v", state)
	}
	if _, err := st.LoadCheckpoint(ctx, session.ID(), first.CheckpointID); err != nil {
		t.Fatalf("active checkpoint was not retained: %v", err)
	}
	operationUsage, err := st.LoadCompactionUsage(ctx, session.ID(), state.LastCompaction.OperationID)
	if err != nil {
		t.Fatalf("LoadCompactionUsage cancelled: %v", err)
	}
	if len(operationUsage) != 1 || operationUsage[0].TotalTokens != 12 {
		t.Fatalf("cancelled operation usage = %#v", operationUsage)
	}
}

func TestAutomaticLowGainFailureUsesStreakBeforePause(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage(strings.Repeat("large answer ", 700), nil)}}},
	}}
	calls := 0
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		calls++
		return contextbuild.DeterministicCheckpoint(request)
	})
	opts := SessionOptions{
		Store:              st,
		Compactor:          compactor,
		MaxLowGainAttempts: 2,
		Context: contextbuild.Config{
			WindowTokens:              1_200,
			MaxOutputTokens:           200,
			KeepRecentTurns:           12,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			// Any non-empty checkpoint fails a 100% release threshold.
			LowGainThresholdPercent: 100,
		},
	}
	session, err := NewSession(model, "system", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(ctx, "small input", nil); err != nil {
		t.Fatal(err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatalf("automatic compaction was not requested: %+v", session.ContextStatus())
	}

	if _, err := session.CompactAutomatically(ctx); !errors.Is(err, contextbuild.ErrCompactionLowGain) {
		t.Fatalf("first automatic compaction error = %v, want ErrCompactionLowGain", err)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.AutoCompactionPaused || state.LowGainStreak != 1 || state.ActiveCheckpointID != "" {
		t.Fatalf("first low-gain should not pause: %#v", state)
	}
	if !session.NeedsAutoCompaction() || session.ContextStatus().AutoCompactionPaused || session.ContextStatus().LowGainStreak != 1 {
		t.Fatalf("session after first low-gain: %+v", session.ContextStatus())
	}

	if _, err := session.CompactAutomatically(ctx); !errors.Is(err, contextbuild.ErrCompactionLowGain) {
		t.Fatalf("second automatic compaction error = %v, want ErrCompactionLowGain", err)
	}
	state, err = st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !state.AutoCompactionPaused || state.LowGainStreak != 2 || state.AutoCompactionPauseReason == "" {
		t.Fatalf("second low-gain should pause: %#v", state)
	}
	if session.NeedsAutoCompaction() || !session.ContextStatus().AutoCompactionPaused {
		t.Fatalf("session after streak pause: %+v", session.ContextStatus())
	}
	if calls != 2 {
		t.Fatalf("compactor calls = %d, want 2", calls)
	}

	if _, err := session.CompactAutomatically(ctx); !errors.Is(err, ErrNoCompactionCandidates) {
		t.Fatalf("paused CompactAutomatically error = %v, want ErrNoCompactionCandidates", err)
	}
	if calls != 2 {
		t.Fatalf("paused automatic compaction made extra provider calls: %d", calls)
	}
}

func TestAutomaticCompactionFailurePausesAndPersists(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage(strings.Repeat("large answer ", 700), nil)}}},
	}}
	calls := 0
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		calls++
		if calls == 1 {
			return contextbuild.Checkpoint{}, errors.New("provider unavailable")
		}
		return contextbuild.DeterministicCheckpoint(request)
	})
	opts := SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens:              1_200,
			MaxOutputTokens:           200,
			KeepRecentTurns:           12,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			LowGainThresholdPercent:   1,
		},
	}
	session, err := NewSession(model, "system", opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(ctx, "small input", nil); err != nil {
		t.Fatal(err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatalf("automatic compaction was not requested: %+v", session.ContextStatus())
	}
	if _, err := session.CompactAutomatically(ctx); err == nil {
		t.Fatal("automatic compaction unexpectedly succeeded")
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveCheckpointID != "" || !state.AutoCompactionPaused || state.AutoCompactionPauseReason == "" || state.LastCompaction == nil || state.LastCompaction.Status != store.CompactionOutcomeFailed || !state.LastCompaction.Automatic {
		t.Fatalf("automatic failure state = %#v", state)
	}
	if session.NeedsAutoCompaction() || !session.ContextStatus().AutoCompactionPaused {
		t.Fatalf("automatic failure did not pause local session: %+v", session.ContextStatus())
	}
	resumed, err := OpenSession(model, st, session.ID(), opts)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if resumed.NeedsAutoCompaction() {
		t.Fatalf("resumed session retried a paused automatic compaction: %+v", resumed.ContextStatus())
	}
	if _, err := resumed.CompactAutomatically(ctx); !errors.Is(err, ErrNoCompactionCandidates) {
		t.Fatalf("paused CompactAutomatically error = %v, want ErrNoCompactionCandidates", err)
	}
	if calls != 1 {
		t.Fatalf("paused automatic compaction made %d provider calls, want 1", calls)
	}
	manual, err := resumed.Compact(ctx, "retry after automatic failure")
	if err != nil {
		t.Fatalf("manual retry after automatic failure: %v", err)
	}
	if manual.CheckpointID == "" {
		t.Fatalf("manual retry did not install checkpoint: %+v", manual)
	}
	afterManual, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	if afterManual.AutoCompactionPaused || afterManual.AutoCompactionPauseReason != "" || afterManual.LastCompaction == nil || afterManual.LastCompaction.Status != store.CompactionOutcomeSucceeded {
		t.Fatalf("manual success did not clear automatic pause: %#v", afterManual)
	}
}

func TestCompactionCandidatesPromotePlannerOmittedHotTurn(t *testing.T) {
	group := contextbuild.TurnGroup{
		ID:             "hot-overflow",
		SourceEventIDs: []string{"event-hot"},
		TokenEstimate:  600,
		Messages:       []*schema.Message{schema.UserMessage("large retained turn")},
	}
	plan, err := contextbuild.PlanContext(contextbuild.PlannerInput{
		ImmutableMessages: []*schema.Message{schema.SystemMessage("system")},
		TurnGroups:        []contextbuild.TurnGroup{group},
	}, contextbuild.Config{
		WindowTokens:              300,
		MaxOutputTokens:           100,
		KeepRecentTurns:           12,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
	})
	if err != nil {
		t.Fatalf("PlanContext: %v", err)
	}
	if got := strings.Join(plan.OmittedGroupIDs, ","); got != "hot-overflow" {
		t.Fatalf("omitted groups = %q", got)
	}
	candidates := compactionCandidates([]contextbuild.TurnGroup{group}, nil, 12, plan.OmittedGroupIDs)
	if len(candidates) != 1 || candidates[0].ID != group.ID {
		t.Fatalf("hot overflow was not promoted to a compaction candidate: %#v", candidates)
	}
}

func TestAutomaticCompactionCompactsSingleOversizedRecentTurn(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage(strings.Repeat("large answer ", 700), nil)}}},
	}}
	var requests []contextbuild.CompactionRequest
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		requests = append(requests, request)
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			WindowTokens:              1_200,
			MaxOutputTokens:           200,
			KeepRecentTurns:           12,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			LowGainThresholdPercent:   1,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(ctx, "small input", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatalf("oversized hot turn did not request automatic compaction: %+v", session.ContextStatus())
	}
	result, err := session.CompactAutomatically(ctx)
	if err != nil {
		t.Fatalf("CompactAutomatically: %v", err)
	}
	if result.CheckpointID == "" || len(requests) == 0 || len(requests[0].SourceGroups) != 1 {
		t.Fatalf("automatic compaction did not receive the oversized hot group: result=%+v requests=%#v", result, requests)
	}
}

func TestCandidateCheckpointMustFitBeforeInstallation(t *testing.T) {
	group := contextbuild.TurnGroup{
		ID:             "turn-1",
		SourceEventIDs: []string{"event-1"},
		Messages:       []*schema.Message{schema.UserMessage("source")},
	}
	checkpoint, err := contextbuild.DeterministicCheckpoint(contextbuild.CompactionRequest{
		SourceGroups: []contextbuild.TurnGroup{group},
	})
	if err != nil {
		t.Fatalf("DeterministicCheckpoint: %v", err)
	}
	session := &Session{
		systemPrompt: "system",
		contextCfg: contextbuild.Config{
			WindowTokens:    1_000,
			MaxOutputTokens: 900,
		},
	}
	plan, err := session.planWithCheckpoint([]contextbuild.TurnGroup{group}, &checkpoint, group.SourceEventIDs, nil)
	if err != nil {
		t.Fatalf("planWithCheckpoint: %v", err)
	}
	if !planHasFallback(plan, "checkpoint_omitted") {
		t.Fatalf("oversized candidate checkpoint was unexpectedly admitted: %+v", plan)
	}
}

func TestAutomaticCompactionArtifactReadFailurePauses(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-artifact-compaction-failure"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "turn-one", Input: "first"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.ToolStarted(ctx, state.ID, state.Revision, store.ToolStarted{TurnID: "turn-one", ToolCallID: "tool-one", ToolName: "read"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := st.PutArtifact(ctx, state.ID, store.ArtifactInput{Kind: "tool.output", MediaType: "text/plain", Data: []byte("artifact evidence")})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.ToolCompleted(ctx, state.ID, state.Revision, store.ToolCompleted{
		TurnID: "turn-one", ToolCallID: "tool-one", ToolName: "read", Output: "artifact stored", Artifact: &artifact,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{TurnID: "turn-one", Messages: []*schema.Message{
		schema.UserMessage("first"), schema.AssistantMessage("first answer", nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "turn-two", Input: "second"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{TurnID: "turn-two", Messages: []*schema.Message{
		schema.UserMessage("second"), schema.AssistantMessage("second answer", nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(st.Root(), "sessions", state.ID, "artifacts", artifact.Digest+".blob")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	session, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{
		Store: st,
		Compactor: checkpointCompactorFunc(func(context.Context, contextbuild.CompactionRequest, contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
			calls++
			return contextbuild.Checkpoint{}, errors.New("compactor should not run")
		}),
		Context: contextbuild.Config{
			WindowTokens:              1_200,
			MaxOutputTokens:           200,
			KeepRecentTurns:           1,
			AutoCompactTriggerPercent: 1,
			PostCompactTargetPercent:  1,
			SummaryMaxTokens:          2_048,
			LowGainThresholdPercent:   1,
		},
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatalf("automatic compaction was not requested: %+v", session.ContextStatus())
	}
	if _, err := session.CompactAutomatically(ctx); err == nil || !strings.Contains(err.Error(), "load compaction artifacts") {
		t.Fatalf("CompactAutomatically error = %v, want artifact read failure", err)
	}
	loaded, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.AutoCompactionPaused || loaded.LastCompaction == nil || loaded.LastCompaction.Status != store.CompactionOutcomeFailed || !loaded.LastCompaction.Automatic || calls != 0 {
		t.Fatalf("artifact failure state = %#v calls=%d", loaded, calls)
	}
	if session.NeedsAutoCompaction() {
		t.Fatalf("artifact failure left automatic compaction runnable: %+v", session.ContextStatus())
	}
}

func TestOpenSessionSkipsUnrelatedMissingArtifactDuringCheckpointVerification(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-checkpoint-unrelated-artifact"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "covered-turn", Input: "covered source"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{TurnID: "covered-turn", Messages: []*schema.Message{
		schema.UserMessage("covered source"),
		schema.AssistantMessage("covered answer", nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	coveredGroups, err := st.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	coveredSource := durableContextGroups(coveredGroups)
	checkpoint, err := contextbuild.DeterministicCheckpoint(contextbuild.CompactionRequest{SourceGroups: coveredSource})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	persisted, state, err := st.CommitCheckpoint(ctx, state.ID, state.Revision, store.CheckpointInput{
		ID:             "covered-checkpoint",
		Payload:        payload,
		SourceEventIDs: coveredSource[0].SourceEventIDs,
		SourceHash:     checkpoint.DirectSourceHash(),
	})
	if err != nil {
		t.Fatal(err)
	}

	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "unrelated-turn", Input: "unrelated source"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.ToolStarted(ctx, state.ID, state.Revision, store.ToolStarted{TurnID: "unrelated-turn", ToolCallID: "unrelated-tool", ToolName: "read"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := st.PutArtifact(ctx, state.ID, store.ArtifactInput{Kind: "tool.output", MediaType: "text/plain", Data: []byte("unrelated artifact")})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.ToolCompleted(ctx, state.ID, state.Revision, store.ToolCompleted{
		TurnID: "unrelated-turn", ToolCallID: "unrelated-tool", ToolName: "read", Output: "artifact stored", Artifact: &artifact,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{TurnID: "unrelated-turn", Messages: []*schema.Message{
		schema.UserMessage("unrelated source"),
		schema.AssistantMessage("unrelated answer", nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(st.Root(), "sessions", state.ID, "artifacts", artifact.Digest+".blob")); err != nil {
		t.Fatal(err)
	}

	resumed, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("OpenSession with unrelated missing artifact: %v", err)
	}
	if got := resumed.ContextStatus().ActiveCheckpointID; got != persisted.ID {
		t.Fatalf("active checkpoint = %q, want %q", got, persisted.ID)
	}
}

func TestAutomaticCompactionSkipsUnselectedMissingArtifact(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-candidate-unrelated-artifact"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "candidate-turn", Input: "candidate source"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{TurnID: "candidate-turn", Messages: []*schema.Message{
		schema.UserMessage(strings.Repeat("candidate evidence ", 2_000)),
		schema.AssistantMessage("candidate answer", nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "hot-turn", Input: "hot source"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.ToolStarted(ctx, state.ID, state.Revision, store.ToolStarted{TurnID: "hot-turn", ToolCallID: "hot-tool", ToolName: "read"})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := st.PutArtifact(ctx, state.ID, store.ArtifactInput{Kind: "tool.output", MediaType: "text/plain", Data: []byte("hot artifact")})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.ToolCompleted(ctx, state.ID, state.Revision, store.ToolCompleted{
		TurnID: "hot-turn", ToolCallID: "hot-tool", ToolName: "read", Output: "artifact stored", Artifact: &artifact,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{TurnID: "hot-turn", Messages: []*schema.Message{
		schema.UserMessage("hot source"),
		schema.AssistantMessage("hot answer", nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(st.Root(), "sessions", state.ID, "artifacts", artifact.Digest+".blob")); err != nil {
		t.Fatal(err)
	}

	calls := 0
	session, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{
		Store: st,
		Compactor: checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
			calls++
			return contextbuild.DeterministicCheckpoint(request)
		}),
		Context: contextbuild.Config{
			WindowTokens:              6_000,
			MaxOutputTokens:           500,
			KeepRecentTurns:           1,
			SummaryMaxTokens:          2_048,
			AutoCompactTriggerPercent: 1,
			PostCompactTargetPercent:  1,
			LowGainThresholdPercent:   1,
		},
	})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if !session.NeedsAutoCompaction() {
		t.Fatalf("automatic compaction was not requested: %+v", session.ContextStatus())
	}
	result, err := session.CompactAutomatically(ctx)
	if err != nil {
		t.Fatalf("CompactAutomatically with unselected missing artifact: %v", err)
	}
	if result.CheckpointID == "" || calls != 1 {
		t.Fatalf("automatic compaction result = %+v calls=%d", result, calls)
	}
}

func TestThreadSessionDoesNotInstallCheckpointWhenRecursiveMergeCannotFit(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("one", nil)}}},
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("two", nil)}}},
	}}
	compactor := checkpointCompactorFunc(func(_ context.Context, request contextbuild.CompactionRequest, _ contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
		return contextbuild.DeterministicCheckpoint(request)
	})
	session, err := NewSession(model, "system", SessionOptions{
		Store:     st,
		Compactor: compactor,
		Context: contextbuild.Config{
			KeepRecentTurns:         1,
			LowGainThresholdPercent: 1,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for _, input := range []string{"first", "second"} {
		if err := session.Ask(ctx, compactionTestInput(input), nil); err != nil {
			t.Fatalf("Ask(%q): %v", input, err)
		}
	}
	// Keep the completed raw ledger, but reduce the compactor budget until its
	// final synthetic merge cannot be sent safely to the provider.
	session.contextCfg = contextbuild.Config{
		WindowTokens:              1_000,
		MaxOutputTokens:           900,
		KeepRecentTurns:           1,
		LowGainThresholdPercent:   1,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
	}
	_, err = session.Compact(ctx, "preserve facts")
	if !errors.Is(err, contextbuild.ErrCompactionRecursionLimit) {
		t.Fatalf("Compact error = %v, want ErrCompactionRecursionLimit", err)
	}
	state, err := st.LoadThread(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.ActiveCheckpointID != "" {
		t.Fatalf("unusable checkpoint was installed: %q", state.ActiveCheckpointID)
	}
}

func TestThreadSessionPersistsRawToolArtifactsWithoutUITruncation(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	rawOutput := strings.Repeat("raw tool output ", maxInlineToolOutputBytes/len("raw tool output ")+100)
	model := &eventScriptedModel{
		stream: &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("done", nil)}}},
		raw:    rawOutput,
	}
	session, err := NewSession(model, "system", SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var observed string
	if err := session.AskWithEvents(ctx, "inspect", nil, func(event TurnEvent) {
		if event.Kind == TurnEventToolEnd {
			observed = event.Output
		}
	}); err != nil {
		t.Fatalf("AskWithEvents: %v", err)
	}
	if observed != rawOutput {
		t.Fatalf("UI event output was truncated: got %d, want %d", len(observed), len(rawOutput))
	}
	groups, err := st.LoadTurnGroups(ctx, session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Tools) != 1 || groups[0].Tools[0].Completed == nil {
		t.Fatalf("tool lifecycle missing: %#v", groups)
	}
	completed := groups[0].Tools[0].Completed
	if completed.Artifact == nil || completed.Artifact.OriginalSize != int64(len(rawOutput)) {
		t.Fatalf("raw artifact missing: %#v", completed)
	}
	if strings.Contains(completed.Output, rawOutput) {
		t.Fatalf("journal completion duplicated full artifact output")
	}
	if !strings.Contains(completed.Output, "read_artifact") {
		t.Fatalf("tool completion does not advertise bounded evidence retrieval: %q", completed.Output)
	}
	read, err := st.ReadArtifact(ctx, session.ID(), completed.Artifact.ID, 0, 64)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if len(read.Data) == 0 && !read.Ref.Truncated {
		t.Fatalf("retained artifact cannot be read: %#v", read)
	}
}

func TestThreadTurnRecorderCorrelatesSameNamedToolsByCallID(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-tool-ids"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "turn-1", Input: "inspect"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	recorder := newThreadTurnRecorder(st, state.ID, state.Revision, "turn-1")
	recorder.record(TurnEvent{Kind: TurnEventToolStart, Tool: "search", ToolCallID: "call-a", Input: "A"})
	recorder.record(TurnEvent{Kind: TurnEventToolStart, Tool: "search", ToolCallID: "call-b", Input: "B"})
	// Completion order intentionally differs from start order.
	recorder.record(TurnEvent{Kind: TurnEventToolEnd, Tool: "search", ToolCallID: "call-b", Output: "output B"})
	recorder.record(TurnEvent{Kind: TurnEventToolEnd, Tool: "search", ToolCallID: "call-a", Output: "output A"})
	if err := recorder.err(); err != nil {
		t.Fatalf("recorder error: %v", err)
	}
	state, err = recorder.commit(store.TurnCommit{
		TurnID: "turn-1",
		Messages: []*schema.Message{
			schema.UserMessage("inspect"),
			schema.AssistantMessage("done", nil),
		},
	})
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	groups, err := st.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Tools) != 2 {
		t.Fatalf("tool groups = %#v", groups)
	}
	outputs := map[string]string{}
	for _, tool := range groups[0].Tools {
		if tool.Completed == nil || tool.Completed.Artifact != nil {
			t.Fatalf("short tool completion should stay inline: %#v", tool)
		}
		outputs[tool.ToolCallID] = tool.Completed.Output
	}
	if outputs["call-a"] != "output A" || outputs["call-b"] != "output B" {
		t.Fatalf("tool outputs were cross-wired: %#v", outputs)
	}
	compactionGroups, err := durableCompactionGroups(ctx, st, state.ID, groups)
	if err != nil {
		t.Fatalf("durableCompactionGroups: %v", err)
	}
	if len(compactionGroups) != 1 || len(compactionGroups[0].Artifacts) != 0 {
		t.Fatalf("compaction artifacts = %#v", compactionGroups)
	}
	if outputs["call-a"] != "output A" || outputs["call-b"] != "output B" {
		t.Fatalf("compactor source omitted inline tool evidence: %#v", outputs)
	}
}

func TestThreadSessionResumeLoadsRecentTranscriptThenPagesOlderTranscript(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-page"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i := 0; i < 60; i++ {
		turnID := fmt.Sprintf("turn-%d", i)
		state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: turnID, Input: fmt.Sprintf("user-%02d", i)})
		if err != nil {
			t.Fatalf("StartTurn(%d): %v", i, err)
		}
		state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{
			TurnID: turnID,
			Messages: []*schema.Message{
				schema.UserMessage(fmt.Sprintf("user-%02d", i)),
				schema.AssistantMessage(fmt.Sprintf("assistant-%02d", i), nil),
			},
		})
		if err != nil {
			t.Fatalf("CommitTurn(%d): %v", i, err)
		}
	}
	resumed, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	status := resumed.ContextStatus()
	if status.CurrentTokens == 0 || status.OriginalTokens == 0 || status.HotTurnGroups == 0 {
		t.Fatalf("resumed context status was not projected from the ledger: %+v", status)
	}
	initial := resumed.Transcript()
	if got, want := len(initial), 101; got != want {
		t.Fatalf("initial transcript len = %d, want system + 100 latest messages", got)
	}
	if initial[1].Content != "user-10" {
		t.Fatalf("first initial body = %q, want user-10", initial[1].Content)
	}
	page, hasMore, err := resumed.LoadOlderTranscript(ctx, 100)
	if err != nil {
		t.Fatalf("LoadOlderTranscript: %v", err)
	}
	if hasMore || len(page) != 20 || page[0].Content != "user-00" {
		t.Fatalf("older page = %d hasMore=%v first=%q", len(page), hasMore, page[0].Content)
	}
	if got, want := len(resumed.Transcript()), 121; got != want {
		t.Fatalf("paged transcript len = %d, want %d", got, want)
	}
}

func TestOpenSessionResetsOnlyActiveV1Checkpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-v1-reset"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	for i, input := range []string{"legacy first raw turn", "legacy second raw turn"} {
		turnID := fmt.Sprintf("turn-%d", i+1)
		state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: turnID, Input: input})
		if err != nil {
			t.Fatal(err)
		}
		state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{
			TurnID: turnID,
			Messages: []*schema.Message{
				schema.UserMessage(input),
				schema.AssistantMessage("retained raw answer", nil),
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	legacyPayload := strings.Replace(validLegacyV1CheckpointJSON,
		`{"text":"constraint","source_event_ids":["event-1"],"confidence":"observed"}`,
		`{"text":"constraint","source_event_ids":["event-from-parent-lineage"],"confidence":"observed"}`,
		1,
	)
	legacy, state, err := st.CommitCheckpoint(ctx, state.ID, state.Revision, store.CheckpointInput{
		ID:             "legacy-v1-checkpoint",
		Payload:        json.RawMessage(legacyPayload),
		SourceEventIDs: []string{"event-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeResetRevision := state.Revision
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("continued", nil)}}},
	}}
	resumed, err := OpenSession(model, st, state.ID, SessionOptions{Store: st, Context: contextbuild.Config{KeepRecentTurns: 1}})
	if err != nil {
		t.Fatalf("OpenSession v1: %v", err)
	}
	resetState, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resetState.Revision != beforeResetRevision+1 || resetState.ActiveCheckpointID != "" || resetState.LastCompaction == nil || resetState.LastCompaction.Status != store.CompactionOutcomeCheckpointReset {
		t.Fatalf("v1 reset state = %#v", resetState)
	}
	if _, err := st.LoadCheckpoint(ctx, state.ID, legacy.ID); err != nil {
		t.Fatalf("legacy checkpoint was removed: %v", err)
	}
	groups, err := st.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Committed == nil || groups[1].Committed == nil {
		t.Fatalf("v1 reset lost raw turns: %#v", groups)
	}
	view, _, err := resumed.threadPrompt(schema.UserMessage("continue"))
	if err != nil {
		t.Fatalf("threadPrompt after reset: %v", err)
	}
	var rawVisible, checkpointVisible bool
	for _, message := range view {
		if message == nil {
			continue
		}
		rawVisible = rawVisible || strings.Contains(message.Content, "legacy first raw turn")
		checkpointVisible = checkpointVisible || strings.Contains(message.Content, "Structured checkpoint")
	}
	if !rawVisible || checkpointVisible {
		t.Fatalf("v1 reset prompt = %#v", view)
	}
	if err := resumed.Ask(ctx, "continue", nil); err != nil {
		t.Fatalf("Ask after v1 reset: %v", err)
	}
	beforeSecondOpen, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSession(model, st, state.ID, SessionOptions{Store: st}); err != nil {
		t.Fatalf("second OpenSession after v1 reset: %v", err)
	}
	afterSecondOpen, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSecondOpen.Revision != beforeSecondOpen.Revision || afterSecondOpen.ActiveCheckpointID != "" {
		t.Fatalf("second resume appended another reset: before=%#v after=%#v", beforeSecondOpen, afterSecondOpen)
	}
}

func TestOpenSessionDoesNotResetInvalidV2Checkpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-invalid-v2"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, state, err := st.CommitCheckpoint(ctx, state.ID, state.Revision, store.CheckpointInput{
		ID:      "invalid-v2-checkpoint",
		Payload: json.RawMessage(`{"schema_version":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st}); err == nil {
		t.Fatal("invalid v2 checkpoint was silently reset")
	}
	loaded, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != state.Revision || loaded.ActiveCheckpointID != checkpoint.ID {
		t.Fatalf("invalid v2 checkpoint state changed: %#v", loaded)
	}
}

func TestOpenSessionDoesNotResetMalformedV1Checkpoint(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-malformed-v1"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, state, err := st.CommitCheckpoint(ctx, state.ID, state.Revision, store.CheckpointInput{
		ID:      "malformed-v1-checkpoint",
		Payload: json.RawMessage(`{"schema_version":1,"unexpected":"not a legacy checkpoint"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st}); err == nil {
		t.Fatal("malformed v1 checkpoint was silently reset")
	}
	loaded, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != state.Revision || loaded.ActiveCheckpointID != checkpoint.ID {
		t.Fatalf("malformed v1 checkpoint state changed: %#v", loaded)
	}
}

func TestOpenSessionRequiresExplicitRecoveryForPendingCompaction(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-pending-compaction"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{
		OperationID: "interrupted-compact",
		Automatic:   true,
	})
	if err != nil {
		t.Fatalf("StartCompaction: %v", err)
	}
	state, err = st.RecordUsage(ctx, state.ID, store.ModelUsage{
		CallID:           "charged-before-crash",
		Operation:        store.UsageOperationCompaction,
		OperationID:      "interrupted-compact",
		HasProviderUsage: true,
		PromptTokens:     12,
		CompletionTokens: 3,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	if _, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st}); !errors.Is(err, ErrThreadHasPendingCompaction) {
		t.Fatalf("OpenSession error = %v, want ErrThreadHasPendingCompaction", err)
	}
	recovered, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st, RecoverInterrupted: true})
	if err != nil {
		t.Fatalf("OpenSession recovery: %v", err)
	}
	if recovered.NeedsAutoCompaction() {
		t.Fatalf("recovered pending automatic compaction remained runnable: %+v", recovered.ContextStatus())
	}
	loaded, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCompaction != nil || !loaded.AutoCompactionPaused || loaded.LastCompaction == nil || loaded.LastCompaction.Status != store.CompactionOutcomeCancelled || loaded.LastCompaction.OperationID != "interrupted-compact" {
		t.Fatalf("recovered pending compaction state = %#v", loaded)
	}
	usageRecords, err := st.LoadCompactionUsage(ctx, state.ID, "interrupted-compact")
	if err != nil {
		t.Fatal(err)
	}
	if len(usageRecords) != 1 || usageRecords[0].TotalTokens != 15 {
		t.Fatalf("recovered pending compaction usage = %#v", usageRecords)
	}
}

func TestOpenSessionRecoveryOfManualCompactionPreservesAutomaticPause(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-pending-manual-after-auto-pause"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{
		OperationID: "automatic-failure",
		Automatic:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	const pauseReason = "automatic compaction failed: provider unavailable"
	state, err = st.RecordCompactionFailure(ctx, state.ID, state.Revision, store.CompactionFailure{
		OperationID:     "automatic-failure",
		Automatic:       true,
		Reason:          "provider unavailable",
		AutoPaused:      true,
		AutoPauseReason: pauseReason,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartCompaction(ctx, state.ID, state.Revision, store.CompactionStart{
		OperationID: "interrupted-manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st, RecoverInterrupted: true}); err != nil {
		t.Fatalf("OpenSession recovery: %v", err)
	}
	loaded, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCompaction != nil || !loaded.AutoCompactionPaused || loaded.AutoCompactionPauseReason != pauseReason || loaded.LastCompaction == nil || loaded.LastCompaction.Status != store.CompactionOutcomeCancelled || loaded.LastCompaction.Automatic || loaded.LastCompaction.OperationID != "interrupted-manual" {
		t.Fatalf("manual recovery cleared automatic pause: %#v", loaded)
	}
}

func TestOpenSessionRejectsV2CheckpointWithMismatchedColdProvenance(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-mismatched-v2"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	makePayload := func(eventID string) (contextbuild.Checkpoint, json.RawMessage) {
		t.Helper()
		checkpoint, checkpointErr := contextbuild.DeterministicCheckpoint(contextbuild.CompactionRequest{
			SourceGroups: []contextbuild.TurnGroup{{
				ID:             "group-" + eventID,
				SourceEventIDs: []string{eventID},
				Messages:       []*schema.Message{schema.UserMessage("source " + eventID)},
			}},
		})
		if checkpointErr != nil {
			t.Fatal(checkpointErr)
		}
		payload, marshalErr := json.Marshal(checkpoint)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return checkpoint, payload
	}
	parentPayloadCheckpoint, parentPayload := makePayload("event-parent")
	parent, state, err := st.CommitCheckpoint(ctx, state.ID, state.Revision, store.CheckpointInput{
		ID:             "parent-checkpoint",
		Kind:           "structured",
		Payload:        parentPayload,
		SourceEventIDs: []string{"event-parent"},
		SourceHash:     parentPayloadCheckpoint.DirectSourceHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	childPayloadCheckpoint, childPayload := makePayload("event-cold-only")
	child, state, err := st.CommitCheckpoint(ctx, state.ID, state.Revision, store.CheckpointInput{
		ID:             "mismatched-child",
		ParentID:       parent.ID,
		Kind:           "structured",
		Payload:        childPayload,
		SourceEventIDs: []string{"event-cold-only"},
		SourceHash:     childPayloadCheckpoint.DirectSourceHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st}); err == nil {
		t.Fatal("mismatched v2 checkpoint provenance was accepted")
	}
	loaded, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != state.Revision || loaded.ActiveCheckpointID != child.ID {
		t.Fatalf("mismatched v2 checkpoint state changed: %#v", loaded)
	}
}

func TestOpenSessionRejectsV2CheckpointWhoseColdSourceIsNotInLedger(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-cold-source-ledger"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "real-turn", Input: "real source"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{
		TurnID: "real-turn",
		Messages: []*schema.Message{
			schema.UserMessage("real source"),
			schema.AssistantMessage("real answer", nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fakeCheckpoint, err := contextbuild.DeterministicCheckpoint(contextbuild.CompactionRequest{SourceGroups: []contextbuild.TurnGroup{{
		ID:             "forged-group",
		SourceEventIDs: []string{"forged-event"},
		Messages:       []*schema.Message{schema.UserMessage("forged source")},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(fakeCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, state, err := st.CommitCheckpoint(ctx, state.ID, state.Revision, store.CheckpointInput{
		ID:             "forged-cold-source",
		Kind:           "structured",
		Payload:        payload,
		SourceEventIDs: []string{"forged-event"},
		SourceHash:     fakeCheckpoint.DirectSourceHash(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st}); err == nil || !strings.Contains(err.Error(), "cold source") {
		t.Fatalf("OpenSession error = %v, want durable cold-source rejection", err)
	}
	loaded, err := st.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != state.Revision || loaded.ActiveCheckpointID != checkpoint.ID {
		t.Fatalf("invalid cold source changed state: %#v", loaded)
	}
}

func TestOpenSessionRecoversInterruptedTurnBeforeNextAsk(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "thread-resume-interrupted"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "turn-crashed", Input: "unfinished"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("recovered answer", nil)}}},
	}}
	if _, err := OpenSession(model, st, state.ID, SessionOptions{Store: st}); !errors.Is(err, ErrThreadHasActiveTurn) {
		t.Fatalf("normal OpenSession error = %v, want ErrThreadHasActiveTurn", err)
	}
	groups, err := st.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups before recovery: %v", err)
	}
	if len(groups) != 1 || groups[0].Failed != nil {
		t.Fatalf("normal resume modified active turn: %#v", groups)
	}
	session, err := OpenSession(model, st, state.ID, SessionOptions{Store: st, RecoverInterrupted: true})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	groups, err = st.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Failed == nil || groups[0].Committed != nil {
		t.Fatalf("interrupted turn was not terminally recovered: %#v", groups)
	}
	if err := session.Ask(ctx, "continue", nil); err != nil {
		t.Fatalf("Ask after recovery: %v", err)
	}
}

type eventScriptedModel struct {
	stream *scriptedStream
	raw    string
}

func (m *eventScriptedModel) Stream(ctx context.Context, messages []*schema.Message) (Stream, error) {
	stream, _, err := m.StreamWithEvents(ctx, messages, nil)
	return stream, err
}

func (m *eventScriptedModel) StreamWithEvents(_ context.Context, _ []*schema.Message, emit EventEmitter) (Stream, <-chan struct{}, error) {
	if emit != nil {
		emit(TurnEvent{Kind: TurnEventToolStart, Tool: "read_file", Input: `{"path":"large.log"}`})
		emit(TurnEvent{Kind: TurnEventToolEnd, Tool: "read_file", Output: m.raw})
	}
	done := make(chan struct{})
	close(done)
	return m.stream, done, nil
}
