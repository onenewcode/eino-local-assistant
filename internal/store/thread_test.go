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

func TestThreadStoreJournalLifecycleAndCAS(t *testing.T) {
	t.Parallel()

	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := store.CreateThread(ctx, ThreadMeta{
		ID:        "thread-lifecycle",
		Title:     "initial",
		CreatedAt: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
	}, "system instructions")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if state.Revision != 1 || state.SystemPrompt != "system instructions" {
		t.Fatalf("initial state = %#v", state)
	}

	state, err = store.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "hello"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	state, err = store.ToolStarted(ctx, state.ID, state.Revision, ToolStarted{
		TurnID: "turn-1", ToolCallID: "call-1", ToolName: "clock", Input: `{"timezone":"UTC"}`,
	})
	if err != nil {
		t.Fatalf("ToolStarted: %v", err)
	}
	state, err = store.ToolCompleted(ctx, state.ID, state.Revision, ToolCompleted{
		TurnID: "turn-1", ToolCallID: "call-1", ToolName: "clock", Output: "2026-07-15T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("ToolCompleted: %v", err)
	}
	state, err = store.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:           "model-call-1",
		TurnID:           "turn-1",
		Operation:        UsageOperationAgent,
		HasProviderUsage: true,
		PromptTokens:     10,
		CompletionTokens: 4,
		TotalTokens:      14,
		CostUSD:          0.02,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	state, err = store.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID: "turn-1",
		Messages: []*schema.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("hi", nil),
		},
	})
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	if state.Meta.MessageCount != 3 || state.Meta.TotalTokens != 14 {
		t.Fatalf("committed state = %#v", state)
	}
	if _, err := store.SetThreadTitle(ctx, state.ID, 1, "stale"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale CAS error = %v, want ErrRevisionConflict", err)
	}

	groups, err := store.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Committed == nil || len(groups[0].Tools) != 1 || groups[0].Tools[0].Completed == nil {
		t.Fatalf("turn groups = %#v", groups)
	}
	recent, err := store.LoadRecentMessages(ctx, state.ID, 1)
	if err != nil {
		t.Fatalf("LoadRecentMessages: %v", err)
	}
	if len(recent) != 2 || recent[0].Role != schema.System || recent[1].Content != "hi" {
		t.Fatalf("recent messages = %#v", recent)
	}
	page, hasMore, err := store.LoadMessagesPage(ctx, state.ID, 0, 1)
	if err != nil {
		t.Fatalf("LoadMessagesPage first: %v", err)
	}
	if len(page) != 1 || page[0].Content != "hello" || !hasMore {
		t.Fatalf("first page = %#v, hasMore=%v", page, hasMore)
	}
	page, hasMore, err = store.LoadMessagesPage(ctx, state.ID, 1, 1)
	if err != nil {
		t.Fatalf("LoadMessagesPage second: %v", err)
	}
	if len(page) != 1 || page[0].Content != "hi" || hasMore {
		t.Fatalf("second page = %#v, hasMore=%v", page, hasMore)
	}

	journal := threadJournalPathForTest(t, store, state.ID)
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	firstLine := strings.Split(strings.TrimSpace(string(data)), "\n")[0]
	var event map[string]any
	if err := json.Unmarshal([]byte(firstLine), &event); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "system instructions") {
		t.Fatalf("journal did not preserve the frozen system prompt: %s", data)
	}
	if got := state.SystemPrompt; got != "system instructions" {
		t.Fatalf("stored system prompt = %q", got)
	}
	for _, field := range []string{
		"format_version", "seq", "event_id", "timestamp", "kind", "payload", "hash",
	} {
		if _, ok := event[field]; !ok {
			t.Errorf("journal event missing %q: %s", field, firstLine)
		}
	}
	for _, field := range []string{"thread_id", "expected_revision", "revision", "correlation_id", "payload_hash"} {
		if _, ok := event[field]; ok {
			t.Errorf("compact journal event should omit %q: %s", field, firstLine)
		}
	}
	var toolStart map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var candidate map[string]any
		if err := json.Unmarshal([]byte(line), &candidate); err != nil {
			t.Fatalf("decode journal line: %v", err)
		}
		if candidate["kind"] == string(EventToolStarted) {
			toolStart = candidate
			break
		}
	}
	if toolStart == nil {
		t.Fatal("tool.started event missing")
	}
	payload, ok := toolStart["payload"].(map[string]any)
	if !ok {
		t.Fatalf("tool.started payload = %#v", toolStart["payload"])
	}
	if _, ok := payload["input"]; ok {
		t.Fatalf("tool input leaked into journal payload: %#v", payload)
	}
	var toolCompleted map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var candidate map[string]any
		if err := json.Unmarshal([]byte(line), &candidate); err != nil {
			t.Fatalf("decode journal line: %v", err)
		}
		if candidate["kind"] == string(EventToolCompleted) {
			toolCompleted = candidate
			break
		}
	}
	if toolCompleted == nil {
		t.Fatal("tool.completed event missing")
	}
	completion, ok := toolCompleted["payload"].(map[string]any)
	if !ok {
		t.Fatalf("tool.completed payload = %#v", toolCompleted["payload"])
	}
	if got, want := completion["output"], "2026-07-15T00:00:00Z"; got != want {
		t.Fatalf("inline tool output = %#v, want %q", got, want)
	}
	if _, ok := completion["artifact"]; ok {
		t.Fatalf("small tool output unexpectedly created artifact: %#v", completion)
	}
}

func TestThreadStorePersistsTaskStateAcrossJournalReplay(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-task-state"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "implement feature"})
	if err != nil {
		t.Fatal(err)
	}
	first := json.RawMessage(`{"version":1,"state":"active","tasks":["implement"]}`)
	state, err = threadStore.UpdateTaskState(ctx, state.ID, state.Revision, "turn-1", TaskStateUpdate{Snapshot: first})
	if err != nil {
		t.Fatalf("UpdateTaskState in turn: %v", err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{TurnID: "turn-1", Messages: []*schema.Message{
		schema.UserMessage("implement feature"), schema.AssistantMessage("working", nil),
	}})
	if err != nil {
		t.Fatal(err)
	}
	second := json.RawMessage(`{"version":1,"state":"interrupted","tasks":["implement"]}`)
	state, err = threadStore.UpdateTaskState(ctx, state.ID, state.Revision, "", TaskStateUpdate{Snapshot: second})
	if err != nil {
		t.Fatalf("UpdateTaskState outside turn: %v", err)
	}

	restored, err := threadStore.LoadTaskState(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadTaskState: %v", err)
	}
	if got, want := string(restored), string(second); got != want {
		t.Fatalf("restored task state = %s, want %s", got, want)
	}
	state, err = threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread replay: %v", err)
	}
	if got, want := string(state.TaskState), string(second); got != want {
		t.Fatalf("replayed task state = %s, want %s", got, want)
	}
}

func TestThreadStoreRejectsInvalidLifecycleTransitions(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-lifecycle-invalid"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "again"}); !errors.Is(err, ErrInvalidThreadLifecycle) {
		t.Fatalf("duplicate start error = %v, want ErrInvalidThreadLifecycle", err)
	}
	state, err = threadStore.ToolStarted(ctx, state.ID, state.Revision, ToolStarted{TurnID: "turn-1", ToolCallID: "call-1", ToolName: "search"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{TurnID: "turn-1", Messages: []*schema.Message{schema.UserMessage("hello"), schema.AssistantMessage("done", nil)}}); !errors.Is(err, ErrInvalidThreadLifecycle) {
		t.Fatalf("unfinished tool commit error = %v, want ErrInvalidThreadLifecycle", err)
	}
	state, err = threadStore.ToolCompleted(ctx, state.ID, state.Revision, ToolCompleted{TurnID: "turn-1", ToolCallID: "call-1", ToolName: "search", Output: "result"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{TurnID: "turn-1", Messages: []*schema.Message{schema.UserMessage("hello"), schema.AssistantMessage("done", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{TurnID: "turn-1", Messages: []*schema.Message{schema.UserMessage("hello"), schema.AssistantMessage("done", nil)}}); !errors.Is(err, ErrInvalidThreadLifecycle) {
		t.Fatalf("duplicate commit error = %v, want ErrInvalidThreadLifecycle", err)
	}
}

func TestThreadStoreFinishTurnRebasesUnrelatedRevision(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-finish-rebase"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.SetThreadTitle(ctx, state.ID, state.Revision, "external writer")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.FinishTurn(ctx, state.ID, TurnFinish{TurnID: "turn-1", Reason: "stream failed"})
	if err != nil {
		t.Fatalf("FinishTurn: %v", err)
	}
	if state.Meta.Title != "external writer" {
		t.Fatalf("rebase lost external title: %#v", state.Meta)
	}
	groups, err := threadStore.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Failed == nil || groups[0].Failed.Error != "stream failed" {
		t.Fatalf("terminal group = %#v", groups)
	}
}

func TestThreadStoreRecoversInterruptedActiveTurn(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-interrupted-turn"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-interrupted", Input: "work"})
	if err != nil {
		t.Fatal(err)
	}
	recovered, changed, err := threadStore.RecoverInterruptedTurn(ctx, state.ID, state.Revision, "resume recovery")
	if err != nil {
		t.Fatalf("RecoverInterruptedTurn: %v", err)
	}
	if !changed || recovered.Revision != state.Revision+1 {
		t.Fatalf("recovery result changed=%v state=%#v", changed, recovered)
	}
	groups, err := threadStore.LoadTurnGroups(ctx, state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Failed == nil || groups[0].Failed.Error != "resume recovery" {
		t.Fatalf("recovered lifecycle = %#v", groups)
	}
	if _, err := threadStore.StartTurn(ctx, state.ID, recovered.Revision, TurnStart{TurnID: "turn-next", Input: "next"}); err != nil {
		t.Fatalf("start after recovery: %v", err)
	}
}

func TestThreadStoreRejectsSecondActiveTurn(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-one-active-turn"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "first"})
	if err != nil {
		t.Fatalf("StartTurn turn-1: %v", err)
	}
	if _, err := threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-2", Input: "second"}); !errors.Is(err, ErrInvalidThreadLifecycle) {
		t.Fatalf("second active turn error = %v, want ErrInvalidThreadLifecycle", err)
	}
	reloaded, err := threadStore.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread after rejected start: %v", err)
	}
	if reloaded.Revision != state.Revision {
		t.Fatalf("rejected start changed revision to %d, want %d", reloaded.Revision, state.Revision)
	}
	state, err = threadStore.CancelTurn(ctx, state.ID, state.Revision, TurnCancel{TurnID: "turn-1", Reason: "interrupted"})
	if err != nil {
		t.Fatalf("CancelTurn: %v", err)
	}
	if _, err := threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-2", Input: "second"}); err != nil {
		t.Fatalf("StartTurn after terminal event: %v", err)
	}
}

func TestThreadStoreReplayRejectsSecondActiveTurn(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-replay-one-active-turn"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "first"})
	if err != nil {
		t.Fatalf("StartTurn turn-1: %v", err)
	}
	// Append a hash- and revision-valid record to prove replay enforces the
	// lifecycle invariant rather than trusting a structurally valid journal.
	event, err := newThreadEvent(state, EventTurnStarted, state.ID, "turn-2", state.Revision, TurnStart{TurnID: "turn-2", Input: "second"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("newThreadEvent: %v", err)
	}
	journal := threadJournalPathForTest(t, threadStore, state.ID)
	if err := appendJournalEvent(journal, event); err != nil {
		t.Fatalf("append invalid lifecycle event: %v", err)
	}
	_, err = threadStore.LoadThread(ctx, state.ID)
	if !errors.Is(err, ErrJournalCorrupt) || !errors.Is(err, ErrInvalidThreadLifecycle) {
		t.Fatalf("replay error = %v, want journal corruption wrapping lifecycle error", err)
	}
}

func TestThreadStoreRecoversTornJournalTail(t *testing.T) {
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := store.CreateThread(ctx, ThreadMeta{ID: "thread-recovery"}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	journal := threadJournalPathForTest(t, store, state.ID)
	f, err := os.OpenFile(journal, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"format_version":3,"seq":2`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, err := store.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread recovery: %v", err)
	}
	if recovered.Revision != state.Revision {
		t.Fatalf("revision after recovery = %d, want %d", recovered.Revision, state.Revision)
	}
	next, err := store.StartTurn(ctx, state.ID, recovered.Revision, TurnStart{TurnID: "turn-after-recovery", Input: "next"})
	if err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	if next.Revision != recovered.Revision+1 {
		t.Fatalf("next revision = %d", next.Revision)
	}
	rebuilt, err := store.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread state replay: %v", err)
	}
	if rebuilt.Revision != next.Revision {
		t.Fatalf("rebuilt revision = %d, want %d", rebuilt.Revision, next.Revision)
	}
	if filepath.Base(journal) != journalFileName(state.ID) {
		t.Fatalf("session journal name = %q, want %q", filepath.Base(journal), journalFileName(state.ID))
	}
	if info, err := os.Stat(journal); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("session journal = %v, %v", info, err)
	}
	data, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event ThreadEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("recovered journal has invalid line %q: %v", line, err)
		}
	}
}

func TestThreadStoreHonorsWriterLockContext(t *testing.T) {
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.CreateThread(context.Background(), ThreadMeta{ID: "thread-lock"}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	_, unlock, err := store.lockThread(context.Background(), state.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = store.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "blocked-turn", Input: "wait"})
	if !errors.Is(err, ErrThreadLocked) {
		t.Fatalf("locked writer error = %v, want ErrThreadLocked", err)
	}
}

func TestThreadStoreArtifactPayloadAndCheckpointRecovery(t *testing.T) {
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := store.CreateThread(ctx, ThreadMeta{ID: "thread-checkpoint"}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := store.PutArtifact(ctx, state.ID, ArtifactInput{Kind: "tool-output", MediaType: "text/plain", Data: []byte(strings.Repeat("a", 80))})
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	if artifact.Size != 80 || artifact.Digest == "" || len(artifact.Data) != 80 {
		t.Fatalf("artifact = %#v", artifact)
	}

	payload := json.RawMessage(`{"decisions":["keep system","summarize old work"]}`)
	checkpoint, next, err := store.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:             "cp-test-1",
		Kind:           "compaction",
		WindowNumber:   1,
		Summary:        &artifact,
		Payload:        payload,
		SourceEventIDs: []string{"evt-source-1"},
		Focus:          "current task",
		BeforeTokens:   100,
		AfterTokens:    40,
		Automatic:      true,
		AutoPaused:     true,
	})
	if err != nil {
		t.Fatalf("CommitCheckpoint: %v", err)
	}
	// Successful install always clears the low-gain streak; AutoPaused may still
	// be carried on the checkpoint for historical pause reasons.
	if next.ActiveCheckpointID != checkpoint.ID || !next.AutoCompactionPaused || next.LowGainStreak != 0 {
		t.Fatalf("checkpoint state = %#v", next)
	}
	if string(checkpoint.Payload) != string(payload) || checkpoint.SourceHash == "" {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}

	recovered, err := store.LoadCheckpoint(ctx, state.ID, checkpoint.ID)
	if err != nil {
		t.Fatalf("LoadCheckpoint recovery: %v", err)
	}
	if recovered.Hash != checkpoint.Hash || string(recovered.Payload) != string(payload) {
		t.Fatalf("recovered checkpoint = %#v", recovered)
	}
	loaded, err := store.LoadCheckpoint(ctx, state.ID, checkpoint.ID)
	if err != nil {
		t.Fatalf("LoadCheckpoint authoritative journal: %v", err)
	}
	if loaded.Focus != "current task" || loaded.Hash != checkpoint.Hash {
		t.Fatalf("journal checkpoint is not authoritative: %#v", loaded)
	}
}

func TestThreadStoreCheckpointLineageKeepsDirectSourcesSeparate(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-checkpoint-lineage"}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	first, state, err := threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:             "cp-lineage-1",
		Payload:        json.RawMessage(`{"kind":"first"}`),
		SourceEventIDs: []string{"event-1", "event-2"},
	})
	if err != nil {
		t.Fatalf("CommitCheckpoint first: %v", err)
	}
	second, state, err := threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:             "cp-lineage-2",
		ParentID:       first.ID,
		Payload:        json.RawMessage(`{"kind":"second"}`),
		SourceEventIDs: []string{"event-3"},
	})
	if err != nil {
		t.Fatalf("CommitCheckpoint second: %v", err)
	}
	if got := strings.Join(second.SourceEventIDs, ","); got != "event-3" {
		t.Fatalf("child copied ancestor sources: %q", got)
	}
	lineage, err := threadStore.LoadCheckpointLineage(ctx, state.ID, second.ID)
	if err != nil {
		t.Fatalf("LoadCheckpointLineage: %v", err)
	}
	if len(lineage) != 2 || lineage[0].ID != second.ID || lineage[1].ID != first.ID {
		t.Fatalf("lineage = %#v", lineage)
	}
	if _, _, err := threadStore.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:       "cp-lineage-invalid",
		ParentID: "cp-lineage-1",
		Payload:  json.RawMessage(`{"kind":"invalid"}`),
	}); err == nil {
		t.Fatal("checkpoint with non-active parent was accepted")
	}
}

func TestThreadStoreReadsFullArtifactInBoundedPages(t *testing.T) {
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-artifact-pages"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := threadStore.PutArtifact(ctx, state.ID, ArtifactInput{Data: []byte("0123456789abcdefghijklmnopqrstuvwxyz")})
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	if string(artifact.Data) != "0123456789abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("full artifact = %#v", artifact)
	}
	state = persistArtifactForThreadTest(ctx, t, threadStore, state, artifact, "artifact-pages")
	want := "0123456789abcdefghijklmnopqrstuvwxyz"

	first, err := threadStore.ReadArtifact(ctx, state.ID, artifact.ID, 0, 3)
	if err != nil {
		t.Fatalf("first ReadArtifact: %v", err)
	}
	if got := string(first.Data); got != "012" || !first.HasMore {
		t.Fatalf("first page = %#v, data=%q", first, got)
	}

	var got []byte
	for offset := int64(0); ; {
		page, err := threadStore.ReadArtifact(ctx, state.ID, artifact.ID, offset, 5)
		if err != nil {
			t.Fatalf("ReadArtifact at offset %d: %v", offset, err)
		}
		if page.Offset != offset {
			t.Fatalf("page offset = %d, want %d", page.Offset, offset)
		}
		got = append(got, page.Data...)
		if !page.HasMore {
			break
		}
		if len(page.Data) == 0 {
			t.Fatal("artifact page reported more bytes without data")
		}
		offset += int64(len(page.Data))
	}
	if string(got) != want {
		t.Fatalf("paged artifact = %q, want %q", got, want)
	}

	lastOffset := int64(len(want) - 2)
	last, err := threadStore.ReadArtifact(ctx, state.ID, artifact.ID, lastOffset, 16)
	if err != nil {
		t.Fatalf("last ReadArtifact: %v", err)
	}
	if got := string(last.Data); got != "yz" || last.HasMore {
		t.Fatalf("last page = %#v, data=%q", last, got)
	}
	beyond, err := threadStore.ReadArtifact(ctx, state.ID, artifact.ID, int64(len(want)), 1)
	if err != nil {
		t.Fatalf("ReadArtifact beyond retained excerpt: %v", err)
	}
	if len(beyond.Data) != 0 || beyond.HasMore {
		t.Fatalf("beyond artifact = %#v", beyond)
	}
}

func TestThreadStoreMultipleArtifactsRemainComplete(t *testing.T) {
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	state, err := store.CreateThread(ctx, ThreadMeta{ID: "thread-artifact-cap"}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.PutArtifact(ctx, state.ID, ArtifactInput{Data: []byte("123456789012")})
	if err != nil {
		t.Fatal(err)
	}
	state = persistArtifactForThreadTest(ctx, t, store, state, first, "artifact-full")
	read, err := store.ReadArtifact(ctx, state.ID, first.ID, 2, 4)
	if err != nil {
		t.Fatalf("ReadArtifact full range: %v", err)
	}
	if got, want := string(read.Data), "3456"; got != want || !read.HasMore {
		t.Fatalf("full artifact range = %q hasMore=%v", got, read.HasMore)
	}
	second, err := store.PutArtifact(ctx, state.ID, ArtifactInput{Data: []byte("abcdefghijklmnopqrstuvwxyz")})
	if err != nil {
		t.Fatalf("second artifact: %v", err)
	}
	if second.Size != int64(len("abcdefghijklmnopqrstuvwxyz")) {
		t.Fatalf("second full artifact = %#v", second)
	}
	state = persistArtifactForThreadTest(ctx, t, store, state, second, "artifact-full-2")
	read, err = store.ReadArtifact(ctx, state.ID, second.ID, 0, 16)
	if err != nil {
		t.Fatalf("ReadArtifact full range: %v", err)
	}
	if string(read.Data) != "abcdefghijklmnop" || !read.HasMore {
		t.Fatalf("full second artifact range = %#v", read)
	}
}

func persistArtifactForThreadTest(ctx context.Context, t *testing.T, threadStore *ThreadStore, state ThreadState, artifact ArtifactRef, suffix string) ThreadState {
	t.Helper()
	var err error
	turnID := "turn-" + suffix
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: turnID, Input: suffix})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolStarted(ctx, state.ID, state.Revision, ToolStarted{TurnID: turnID, ToolCallID: suffix + "-call", ToolName: "shell"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolCompleted(ctx, state.ID, state.Revision, ToolCompleted{TurnID: turnID, ToolCallID: suffix + "-call", ToolName: "shell", Artifact: &artifact})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.CancelTurn(ctx, state.ID, state.Revision, TurnCancel{TurnID: turnID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestThreadStoreMetadataListAndDelete(t *testing.T) {
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	newer, err := store.CreateThread(ctx, ThreadMeta{
		ID:        "thread-newer",
		Title:     "newer",
		CreatedAt: time.Date(2030, 7, 15, 9, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2030, 7, 15, 9, 0, 0, 0, time.UTC),
	}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateThread(ctx, ThreadMeta{
		ID:        "thread-older",
		Title:     "older",
		CreatedAt: time.Date(2020, 7, 14, 9, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2020, 7, 14, 9, 0, 0, 0, time.UTC),
	}, "sys"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(store.Root(), sessionsDirName, ".creating-crashed"), 0o700); err != nil {
		t.Fatal(err)
	}
	metas, err := store.ListThreads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].ID != newer.ID {
		t.Fatalf("thread list = %#v", metas)
	}
	state, err := store.SetThreadTitle(ctx, newer.ID, newer.Revision, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := store.LoadThreadMeta(ctx, newer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "renamed" || state.Meta.Title != "renamed" {
		t.Fatalf("thread metadata = %#v, state = %#v", meta, state)
	}
	if err := store.DeleteThread(ctx, "thread-older"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadThread(ctx, "thread-older"); err == nil {
		t.Fatal("deleted thread should not load")
	}
}

func TestThreadStoreRejectsDuplicateIDAcrossDatePaths(t *testing.T) {
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := store.CreateThread(ctx, ThreadMeta{
		ID:        "duplicate-thread",
		CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
	}, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateThread(ctx, ThreadMeta{
		ID:        "duplicate-thread",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}, "system"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("CreateThread duplicate across dates error = %v, want collision", err)
	}
}

func TestNewThreadIDFormat(t *testing.T) {
	id := NewThreadID(time.Date(2026, 7, 15, 9, 30, 45, 0, time.UTC))
	if !strings.HasPrefix(id, "093045-") {
		t.Fatalf("thread id = %q", id)
	}
	if err := validateThreadID(id); err != nil {
		t.Fatalf("validate generated thread id: %v", err)
	}
}
