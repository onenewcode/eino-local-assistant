package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestThreadStoreRecordUsageAggregatesExactCallsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-usage-exact"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	first := ModelUsage{
		CallID:              "model-call-1",
		TurnID:              "turn-1",
		Operation:           UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        10,
		CompletionTokens:    1,
		TotalTokens:         11,
		CachedTokens:        2,
		ContextBudgetTokens: 100,
		CostUSD:             0.001,
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, first)
	if err != nil {
		t.Fatalf("first RecordUsage: %v", err)
	}
	if state.Meta.ModelCallCount != 1 || state.Meta.TotalTokens != 11 {
		t.Fatalf("first usage state = %#v", state.Meta)
	}

	retried, err := threadStore.RecordUsage(ctx, state.ID, first)
	if err != nil {
		t.Fatalf("idempotent RecordUsage: %v", err)
	}
	if retried.Revision != state.Revision || retried.Meta.ModelCallCount != 1 || retried.Meta.TotalTokens != 11 {
		t.Fatalf("idempotent retry state = %#v", retried.Meta)
	}

	second := ModelUsage{
		CallID:              "model-call-2",
		TurnID:              "turn-1",
		Operation:           UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        20,
		CompletionTokens:    5,
		CachedTokens:        1,
		ReasoningTokens:     2,
		ContextBudgetTokens: 100,
		CostUSD:             0.002,
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, second)
	if err != nil {
		t.Fatalf("second RecordUsage: %v", err)
	}
	if state.Meta.PromptTokens != 30 || state.Meta.CompletionTokens != 6 || state.Meta.TotalTokens != 36 {
		t.Fatalf("aggregated usage = %#v", state.Meta)
	}
	if state.Meta.CachedTokens != 3 || state.Meta.ReasoningTokens != 2 || state.Meta.ModelCallCount != 2 {
		t.Fatalf("usage details = %#v", state.Meta)
	}
	if state.Meta.UsageStatus != UsageStatusExact || state.Meta.LastContext == nil || state.Meta.LastContext.PromptTokens != 20 || state.Meta.LastContext.BudgetTokens != 100 {
		t.Fatalf("usage trust/context = %#v", state.Meta)
	}
	if math.Abs(state.Meta.CostUSD-0.003) > 1e-12 {
		t.Fatalf("cost = %v, want 0.003", state.Meta.CostUSD)
	}

	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID: "turn-1",
		Messages: []*schema.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("done", nil),
		},
	})
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	if state.Meta.TotalTokens != 36 || state.Meta.ModelCallCount != 2 {
		t.Fatalf("CommitTurn double-counted usage: %#v", state.Meta)
	}

	conflicting := first
	conflicting.PromptTokens++
	if _, err := threadStore.RecordUsage(ctx, state.ID, conflicting); !errors.Is(err, ErrUsageRecordConflict) {
		t.Fatalf("conflicting RecordUsage error = %v, want ErrUsageRecordConflict", err)
	}
	groups, err := threadStore.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Usages) != 2 || groups[0].Usages[1].TotalTokens != 25 {
		t.Fatalf("turn usage records = %#v", groups)
	}
	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.TotalTokens != 36 || loaded.Meta.LastContext == nil || loaded.Meta.LastContext.PromptTokens != 20 {
		t.Fatalf("replayed usage state = %#v", loaded.Meta)
	}
}

func TestThreadStoreRecordUsageRequiresActiveAgentTurn(t *testing.T) {
	t.Parallel()

	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-usage-turn"}, "system")
	if err != nil {
		t.Fatal(err)
	}

	missingTurn := ModelUsage{
		CallID:           "agent-without-turn",
		Operation:        UsageOperationAgent,
		HasProviderUsage: true,
		PromptTokens:     1,
	}
	if _, err := threadStore.RecordUsage(ctx, state.ID, missingTurn); err == nil {
		t.Fatal("RecordUsage without an agent turn succeeded")
	}

	beforeStart := missingTurn
	beforeStart.CallID = "agent-before-start"
	beforeStart.TurnID = "turn-1"
	if _, err := threadStore.RecordUsage(ctx, state.ID, beforeStart); !errors.Is(err, ErrInvalidThreadLifecycle) {
		t.Fatalf("RecordUsage before turn start error = %v, want ErrInvalidThreadLifecycle", err)
	}

	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:           "compaction-without-turn",
		Operation:        UsageOperationCompaction,
		HasProviderUsage: true,
		PromptTokens:     2,
	})
	if err != nil {
		t.Fatalf("RecordUsage compaction without turn: %v", err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID: "turn-1",
		Messages: []*schema.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("done", nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	afterTerminal := beforeStart
	afterTerminal.CallID = "agent-after-terminal"
	if _, err := threadStore.RecordUsage(ctx, state.ID, afterTerminal); !errors.Is(err, ErrInvalidThreadLifecycle) {
		t.Fatalf("RecordUsage after turn end error = %v, want ErrInvalidThreadLifecycle", err)
	}
}

func TestThreadStoreRecordUsageAppendsNewCallsWithoutCAS(t *testing.T) {
	t.Parallel()

	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-usage-cas"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:           "model-call-1",
		TurnID:           "turn-1",
		Operation:        UsageOperationAgent,
		HasProviderUsage: true,
		PromptTokens:     3,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:           "model-call-2",
		TurnID:           "turn-1",
		Operation:        UsageOperationAgent,
		HasProviderUsage: true,
		PromptTokens:     5,
	})
	if err != nil {
		t.Fatalf("second RecordUsage: %v", err)
	}
	loaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Meta.ModelCallCount != 2 || loaded.Meta.PromptTokens != 8 {
		t.Fatalf("new call was not appended atomically: %#v", loaded.Meta)
	}
}

func TestThreadStoreRejectsJournalAgentUsageWithoutTurn(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-usage-invalid-journal"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	event, err := newThreadEvent(state, EventUsageRecorded, state.ID, "", state.Revision, ModelUsage{
		CallID:           "agent-without-turn",
		Operation:        UsageOperationAgent,
		HasProviderUsage: true,
		PromptTokens:     1,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(threadStore.Root(), sessionsDirName, state.ID, journalFileName)
	if err := appendJournalEvent(journal, event); err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.LoadThread(ctx, state.ID); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("LoadThread invalid agent usage error = %v, want ErrJournalCorrupt", err)
	}
}

func TestThreadStoreCreateClearsCallerUsageProjection(t *testing.T) {
	t.Parallel()

	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := threadStore.CreateThread(context.Background(), ThreadMeta{
		ID:               "thread-usage-new",
		PromptTokens:     10,
		CompletionTokens: 4,
		TotalTokens:      14,
		CachedTokens:     2,
		ReasoningTokens:  1,
		ModelCallCount:   3,
		CostUSD:          0.02,
		UsageStatus:      UsageStatusUnavailable,
		LastContext:      &ContextSnapshot{PromptTokens: 10, BudgetTokens: 100},
	}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.UsageStatus != UsageStatusExact ||
		state.Meta.PromptTokens != 0 ||
		state.Meta.CompletionTokens != 0 ||
		state.Meta.TotalTokens != 0 ||
		state.Meta.CachedTokens != 0 ||
		state.Meta.ReasoningTokens != 0 ||
		state.Meta.ModelCallCount != 0 ||
		state.Meta.CostUSD != 0 ||
		state.Meta.LastContext != nil {
		t.Fatalf("new thread usage projection = %#v", state.Meta)
	}
	replayed, err := threadStore.LoadThread(context.Background(), state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Meta != state.Meta {
		t.Fatalf("replayed new thread metadata = %#v, want %#v", replayed.Meta, state.Meta)
	}
}

func TestThreadStoreRecordUsageMarksIncompleteAndCompactionClearsContext(t *testing.T) {
	t.Parallel()

	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-usage-incomplete"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:              "model-call-exact",
		TurnID:              "turn-1",
		Operation:           UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        10,
		CompletionTokens:    2,
		ContextBudgetTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID: "turn-1",
		Messages: []*schema.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("done", nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.LastContext == nil || state.Meta.LastContext.PromptTokens != 10 {
		t.Fatalf("agent context = %#v", state.Meta.LastContext)
	}
	_, state, err = threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{ID: "usage-checkpoint"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.LastContext != nil {
		t.Fatalf("compaction context = %#v, want nil", state.Meta.LastContext)
	}
	replayed, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Meta.LastContext != nil {
		t.Fatalf("replayed compaction context = %#v, want nil", replayed.Meta.LastContext)
	}
	state = replayed

	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:           "model-call-compaction",
		Operation:        UsageOperationCompaction,
		HasProviderUsage: true,
		PromptTokens:     7,
		CompletionTokens: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.LastContext != nil {
		t.Fatalf("compaction usage replaced context = %#v", state.Meta.LastContext)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-2", Input: "again"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:              "model-call-unknown",
		TurnID:              "turn-2",
		Operation:           UsageOperationAgent,
		PromptTokens:        999,
		CompletionTokens:    888,
		TotalTokens:         1887,
		ContextBudgetTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID: "turn-2",
		Messages: []*schema.Message{
			schema.UserMessage("again"),
			schema.AssistantMessage("done", nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.UsageStatus != UsageStatusIncomplete || state.Meta.PromptTokens != 17 || state.Meta.CompletionTokens != 5 || state.Meta.TotalTokens != 22 {
		t.Fatalf("incomplete accounting = %#v", state.Meta)
	}
	if state.Meta.ModelCallCount != 3 || state.Meta.LastContext != nil {
		t.Fatalf("incomplete call/context state = %#v", state.Meta)
	}
	groups, err := threadStore.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || len(groups[1].Usages) != 1 || groups[1].Usages[0].PromptTokens != 0 {
		t.Fatalf("incomplete usage record = %#v", groups)
	}
}

func TestThreadStoreRecordUsageSurvivesFailedTurn(t *testing.T) {
	t.Parallel()

	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-usage-failed"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-failed", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:              "model-call-failed",
		TurnID:              "turn-failed",
		Operation:           UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        12,
		CompletionTokens:    3,
		ContextBudgetTokens: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.FailTurn(ctx, state.ID, state.Revision, TurnFailure{TurnID: "turn-failed", Error: "response validation failed"})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Meta.PromptTokens != 12 || replayed.Meta.CompletionTokens != 3 || replayed.Meta.TotalTokens != 15 || replayed.Meta.ModelCallCount != 1 {
		t.Fatalf("failed-turn usage = %#v", replayed.Meta)
	}
	groups, err := threadStore.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Failed == nil || len(groups[0].Usages) != 1 {
		t.Fatalf("failed-turn group = %#v", groups)
	}
}

func TestThreadStoreReplaysPreAccountingJournalAsUnavailable(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-usage-unavailable"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	rewriteThreadCreationWithoutUsageStatus(t, threadStore, state.ID)

	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-old", Input: "old"})
	if err != nil {
		t.Fatal(err)
	}
	legacyEvent, err := newThreadEvent(state, EventTurnCommitted, state.ID, "turn-old", state.Revision, map[string]any{
		"turn_id": "turn-old",
		"messages": []*schema.Message{
			schema.UserMessage("old"),
			schema.AssistantMessage("old answer", nil),
		},
		// This is intentionally an old payload shape. Current replay ignores it.
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14, "cost_usd": 0.02, "estimated": true},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(threadStore.Root(), sessionsDirName, state.ID, journalFileName)
	if err := appendJournalEvent(journal, legacyEvent); err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.UsageStatus != UsageStatusUnavailable ||
		state.Meta.PromptTokens != 0 ||
		state.Meta.CompletionTokens != 0 ||
		state.Meta.TotalTokens != 0 ||
		state.Meta.CachedTokens != 0 ||
		state.Meta.ReasoningTokens != 0 ||
		state.Meta.ModelCallCount != 0 ||
		state.Meta.CostUSD != 0 ||
		state.Meta.LastContext != nil {
		t.Fatalf("pre-accounting replay = %#v", state.Meta)
	}

	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-new", Input: "new"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:           "model-call-new",
		TurnID:           "turn-new",
		Operation:        UsageOperationAgent,
		HasProviderUsage: true,
		PromptTokens:     4,
		CompletionTokens: 2,
		TotalTokens:      6,
		CostUSD:          0.003,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID: "turn-new",
		Messages: []*schema.Message{
			schema.UserMessage("new"),
			schema.AssistantMessage("new answer", nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Meta.UsageStatus != UsageStatusIncomplete || state.Meta.PromptTokens != 4 || state.Meta.CompletionTokens != 2 || state.Meta.TotalTokens != 6 || state.Meta.ModelCallCount != 1 {
		t.Fatalf("pre-accounting/new accounting = %#v", state.Meta)
	}
	if math.Abs(state.Meta.CostUSD-0.003) > 1e-12 {
		t.Fatalf("pre-accounting/new cost = %v, want 0.003", state.Meta.CostUSD)
	}
}

func rewriteThreadCreationWithoutUsageStatus(t *testing.T, threadStore *ThreadStore, threadID string) {
	t.Helper()
	journal := filepath.Join(threadStore.Root(), sessionsDirName, threadID, journalFileName)
	raw, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("initial journal lines = %d, want 1", len(lines))
	}
	event, err := decodeThreadEvent([]byte(lines[0]), threadID)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	meta, ok := payload["meta"].(map[string]any)
	if !ok {
		t.Fatalf("creation metadata = %#v", payload["meta"])
	}
	delete(meta, "usage_status")
	event.Payload, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event.PayloadHash = sha256Hex(event.Payload)
	event.Hash = threadEventHash(event)
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
