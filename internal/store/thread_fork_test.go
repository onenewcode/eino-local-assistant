package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestThreadStoreForkThreadRebuildsCommittedPrefixAndPreservesSource(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "fork-source", Title: "source"}, "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	state := appendForkTestTurn(ctx, t, threadStore, source, "turn-1", true)
	state = appendForkTestTurn(ctx, t, threadStore, state, "turn-2", false)
	sourceFilesBefore := allForkThreadFiles(t, threadStore.Root(), source.ID)

	result, err := threadStore.ForkThread(ctx, source.ID, "fork-child", "turn-1")
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if result.SourceID != source.ID || result.ChildID != "fork-child" || result.LastTurnID != "turn-1" {
		t.Fatalf("fork result = %#v", result)
	}
	if len(result.SourceHash) != 64 || result.SourceHash != result.ChildState.Meta.ForkSourceHash {
		t.Fatalf("fork source hash = %q, child state = %#v", result.SourceHash, result.ChildState)
	}

	sourceFilesAfter := allForkThreadFiles(t, threadStore.Root(), source.ID)
	if !reflect.DeepEqual(sourceFilesAfter, sourceFilesBefore) {
		t.Fatalf("source bytes changed after fork:\nbefore=%#v\nafter=%#v", sourceFilesBefore, sourceFilesAfter)
	}
	childDir := filepath.Join(threadStore.Root(), sessionsDirName, result.ChildID)
	if _, err := os.Lstat(filepath.Join(childDir, locksDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fork copied locks: %v", err)
	}

	child, err := threadStore.LoadThread(ctx, result.ChildID)
	if err != nil {
		t.Fatalf("LoadThread child: %v", err)
	}
	if child.ID != result.ChildID || child.Revision != result.ChildState.Revision {
		t.Fatalf("child state = %#v, result = %#v", child, result.ChildState)
	}
	if child.Meta.ParentID != source.ID || child.Meta.ForkBoundaryTurnID != "turn-1" {
		t.Fatalf("child provenance = %#v", child.Meta)
	}
	groups, err := threadStore.LoadTurnGroups(ctx, result.ChildID)
	if err != nil {
		t.Fatalf("LoadTurnGroups child: %v", err)
	}
	if len(groups) != 1 || groups[0].TurnID != "turn-1" || groups[0].Committed == nil || len(groups[0].Tools) != 1 || len(groups[0].Usages) != 1 {
		t.Fatalf("child groups = %#v", groups)
	}
	if groups[0].Tools[0].Completed == nil || groups[0].Tools[0].Completed.Artifact == nil {
		t.Fatalf("child tool group = %#v", groups[0].Tools)
	}
	artifactID := groups[0].Tools[0].Completed.Artifact.ID
	artifact, err := threadStore.ReadArtifact(ctx, result.ChildID, artifactID, 0, 1024)
	if err != nil {
		t.Fatalf("ReadArtifact child: %v", err)
	}
	if string(artifact.Data) != "artifact from turn one" || artifact.HasMore {
		t.Fatalf("child artifact = %#v", artifact)
	}

	childEvents := readForkTestEvents(t, childDir)
	sourceEvents := readForkTestEvents(t, filepath.Join(threadStore.Root(), sessionsDirName, source.ID))
	if len(childEvents) != 6 {
		t.Fatalf("child event count = %d, want 6", len(childEvents))
	}
	if childEvents[0].ID == sourceEvents[0].ID || childEvents[len(childEvents)-1].ID == sourceEvents[len(childEvents)-1].ID {
		t.Fatal("fork reused source event IDs")
	}
	var previousHash string
	for index, event := range childEvents {
		if event.ThreadID != result.ChildID || event.Sequence != uint64(index+1) || event.Revision != uint64(index+1) || event.ExpectedRevision != uint64(index) || event.PreviousHash != previousHash {
			t.Fatalf("rebuilt envelope at %d = %#v", index, event)
		}
		if sha256Hex(event.Payload) != event.PayloadHash || threadEventHash(event) != event.Hash {
			t.Fatalf("rebuilt hashes at %d = %#v", index, event)
		}
		previousHash = event.Hash
	}
	var created threadCreatedPayload
	if err := json.Unmarshal(childEvents[0].Payload, &created); err != nil {
		t.Fatal(err)
	}
	if created.Meta.ID != result.ChildID || created.Meta.ParentID != source.ID || created.Meta.ForkBoundaryTurnID != "turn-1" || created.Meta.ForkSourceHash != result.SourceHash {
		t.Fatalf("child thread.created payload = %#v", created)
	}

	next, err := threadStore.StartTurn(ctx, result.ChildID, child.Revision, TurnStart{TurnID: "turn-child", Input: "continue"})
	if err != nil {
		t.Fatalf("StartTurn child: %v", err)
	}
	if _, err := threadStore.CommitTurn(ctx, result.ChildID, next.Revision, TurnCommit{
		TurnID:   "turn-child",
		Messages: []*schema.Message{schema.UserMessage("continue"), schema.AssistantMessage("continued", nil)},
	}); err != nil {
		t.Fatalf("CommitTurn child: %v", err)
	}
	if !reflect.DeepEqual(allForkThreadFiles(t, threadStore.Root(), source.ID), sourceFilesBefore) {
		t.Fatal("source changed after child append")
	}
}

func TestThreadStoreForkThreadGeneratesIDAtCommittedHead(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "fork-generated-source"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	source = appendForkTestTurn(ctx, t, threadStore, source, "turn-only", false)

	result, err := threadStore.ForkThread(ctx, source.ID, "", "")
	if err != nil {
		t.Fatalf("ForkThread generated ID: %v", err)
	}
	if result.ChildID == "" || result.ChildID == source.ID || result.LastTurnID != "turn-only" {
		t.Fatalf("generated fork result = %#v", result)
	}
	child, err := threadStore.LoadThread(ctx, result.ChildID)
	if err != nil {
		t.Fatalf("LoadThread generated child: %v", err)
	}
	if child.Meta.ParentID != source.ID || child.Meta.ForkBoundaryTurnID != "turn-only" || child.Revision != source.Revision {
		t.Fatalf("generated child = %#v, source = %#v", child, source)
	}
}

func TestThreadStoreForkThreadAcceptsReplayableJournalWithoutFinalNewline(t *testing.T) {
	ctx := context.Background()
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateThread(ctx, ThreadMeta{ID: "fork-no-final-newline"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	appendForkTestTurn(ctx, t, store, source, "turn-no-final-newline", false)

	journalPath := filepath.Join(store.Root(), sessionsDirName, source.ID, journalFileName)
	sourceJournal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sourceJournal) == 0 || sourceJournal[len(sourceJournal)-1] != '\n' {
		t.Fatal("test journal does not have a final newline")
	}
	sourceJournal = sourceJournal[:len(sourceJournal)-1]
	if err := os.WriteFile(journalPath, sourceJournal, 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := store.ForkThread(ctx, source.ID, "fork-no-final-newline-child", "turn-no-final-newline")
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	if _, err := store.LoadThread(ctx, result.ChildID); err != nil {
		t.Fatalf("LoadThread child: %v", err)
	}
	gotSourceJournal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSourceJournal, sourceJournal) {
		t.Fatal("fork repaired or changed the source journal")
	}
}

func TestThreadStoreForkThreadRejectsUnsafeBoundariesWithoutChild(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(context.Context, *ThreadStore, ThreadState) ThreadState
		lastID  string
		wantErr error
	}{
		{
			name:    "no committed turn",
			wantErr: ErrForkNoCommittedTurn,
		},
		{
			name: "active turn",
			prepare: func(ctx context.Context, store *ThreadStore, state ThreadState) ThreadState {
				next, err := store.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-active", Input: "work"})
				if err != nil {
					t.Fatal(err)
				}
				return next
			},
			wantErr: ErrForkActiveTurn,
		},
		{
			name: "pending compaction",
			prepare: func(ctx context.Context, store *ThreadStore, state ThreadState) ThreadState {
				state = appendForkTestTurn(ctx, t, store, state, "turn-committed", false)
				next, err := store.StartCompaction(ctx, state.ID, state.Revision, CompactionStart{OperationID: "compact-pending"})
				if err != nil {
					t.Fatal(err)
				}
				return next
			},
			wantErr: ErrForkPendingCompaction,
		},
		{
			name: "unknown boundary",
			prepare: func(ctx context.Context, store *ThreadStore, state ThreadState) ThreadState {
				return appendForkTestTurn(ctx, t, store, state, "turn-committed", false)
			},
			lastID:  "turn-missing",
			wantErr: ErrForkInvalidBoundary,
		},
		{
			name: "task state",
			prepare: func(ctx context.Context, store *ThreadStore, state ThreadState) ThreadState {
				state = appendForkTestTurn(ctx, t, store, state, "turn-task", false)
				next, err := store.UpdateTaskState(ctx, state.ID, state.Revision, "", TaskStateUpdate{Snapshot: json.RawMessage(`{"status":"done"}`)})
				if err != nil {
					t.Fatal(err)
				}
				return next
			},
			wantErr: ErrForkUnsupportedState,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewThreadStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source, err := store.CreateThread(ctx, ThreadMeta{ID: "fork-reject-source"}, "system")
			if err != nil {
				t.Fatal(err)
			}
			if test.prepare != nil {
				source = test.prepare(ctx, store, source)
			}
			before := allForkThreadFiles(t, store.Root(), source.ID)
			_, err = store.ForkThread(ctx, source.ID, "fork-reject-child", test.lastID)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ForkThread error = %v, want %v", err, test.wantErr)
			}
			if _, statErr := os.Lstat(filepath.Join(store.Root(), sessionsDirName, "fork-reject-child")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected fork published child: %v", statErr)
			}
			if !reflect.DeepEqual(allForkThreadFiles(t, store.Root(), source.ID), before) {
				t.Fatal("rejected fork changed source")
			}
		})
	}
}

func TestThreadStoreForkThreadRejectsCheckpointAndDestinationCollision(t *testing.T) {
	ctx := context.Background()
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateThread(ctx, ThreadMeta{ID: "fork-checkpoint-source"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	source = appendForkTestTurn(ctx, t, store, source, "turn-checkpoint", false)
	destination, err := store.CreateThread(ctx, ThreadMeta{ID: "fork-collision"}, "existing")
	if err != nil {
		t.Fatal(err)
	}
	if destination.ID == "" {
		t.Fatal("destination thread has no ID")
	}
	_, err = store.ForkThread(ctx, source.ID, destination.ID, "turn-checkpoint")
	if !errors.Is(err, ErrForkDestinationExists) {
		t.Fatalf("collision error = %v, want destination collision", err)
	}

	if _, source, err = store.CommitCheckpoint(ctx, source.ID, source.Revision, CheckpointInput{ID: "checkpoint-fork-reject", Payload: json.RawMessage(`{"summary":"not copied"}`)}); err != nil {
		t.Fatal(err)
	}
	_, err = store.ForkThread(ctx, source.ID, "fork-checkpoint-child", "")
	if !errors.Is(err, ErrForkUnsupportedState) {
		t.Fatalf("checkpoint fork error = %v, want unsupported state", err)
	}
	if _, statErr := os.Lstat(filepath.Join(store.Root(), sessionsDirName, "fork-checkpoint-child")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("checkpoint rejection published child: %v", statErr)
	}
}

func TestThreadStoreForkThreadAtomicFailureLeavesNoStagingOrChild(t *testing.T) {
	ctx := context.Background()
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateThread(ctx, ThreadMeta{ID: "fork-atomic-source"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	source = appendForkTestTurn(ctx, t, store, source, "turn-atomic", false)
	before := allForkThreadFiles(t, store.Root(), source.ID)
	originalMaterialize := store.materialize
	store.materialize = func(string, ThreadState) error {
		return errors.New("injected fork projection failure")
	}
	_, err = store.ForkThread(ctx, source.ID, "fork-atomic-child", "turn-atomic")
	store.materialize = originalMaterialize
	if err == nil || !strings.Contains(err.Error(), "injected fork projection failure") {
		t.Fatalf("atomic fork error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(store.Root(), sessionsDirName, "fork-atomic-child")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("atomic failure published child: %v", statErr)
	}
	entries, err := os.ReadDir(filepath.Join(store.Root(), sessionsDirName))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".fork-") {
			t.Fatalf("atomic failure left staging directory %q", entry.Name())
		}
	}
	if !reflect.DeepEqual(allForkThreadFiles(t, store.Root(), source.ID), before) {
		t.Fatal("atomic failure changed source")
	}
}

func TestThreadStoreForkThreadRejectsCorruptSourceAndArtifactFailure(t *testing.T) {
	ctx := context.Background()
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateThread(ctx, ThreadMeta{ID: "fork-corrupt-source"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	source = appendForkTestTurn(ctx, t, store, source, "turn-corrupt", true)
	groups, err := store.LoadTurnGroups(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifactID := groups[0].Tools[0].Completed.Artifact.ID
	artifactDigest, err := artifactDigestFromID(artifactID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.Root(), sessionsDirName, source.ID, artifactsDir, artifactDigest+".blob")); err != nil {
		t.Fatal(err)
	}
	_, err = store.ForkThread(ctx, source.ID, "fork-missing-artifact", "turn-corrupt")
	if !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("missing artifact error = %v, want journal corruption", err)
	}
	if _, statErr := os.Lstat(filepath.Join(store.Root(), sessionsDirName, "fork-missing-artifact")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing artifact published child: %v", statErr)
	}

	corruptSource, err := store.CreateThread(ctx, ThreadMeta{ID: "fork-journal-corrupt"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	corruptSource = appendForkTestTurn(ctx, t, store, corruptSource, "turn-corrupt-journal", false)
	journalPath := filepath.Join(store.Root(), sessionsDirName, corruptSource.ID, journalFileName)
	journal, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, append(append([]byte(nil), journal...), []byte("not-json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = store.ForkThread(ctx, corruptSource.ID, "fork-corrupt-child", "turn-corrupt-journal")
	if !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("corrupt source error = %v, want journal corruption", err)
	}
	if _, statErr := os.Lstat(filepath.Join(store.Root(), sessionsDirName, "fork-corrupt-child")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("corrupt source published child: %v", statErr)
	}
}

func TestThreadStoreForkThreadRejectsSymlinkedSourceLock(t *testing.T) {
	ctx := context.Background()
	store, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateThread(ctx, ThreadMeta{ID: "fork-symlink-source"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	appendForkTestTurn(ctx, t, store, source, "turn-symlink", false)
	lockPath := filepath.Join(store.Root(), sessionsDirName, source.ID, locksDir, writeLockName)
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, lockPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err = store.ForkThread(ctx, source.ID, "fork-symlink-child", "turn-symlink")
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlinked lock error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(store.Root(), sessionsDirName, "fork-symlink-child")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("symlinked lock published child: %v", statErr)
	}
}

func appendForkTestTurn(ctx context.Context, t *testing.T, store *ThreadStore, state ThreadState, turnID string, withArtifact bool) ThreadState {
	t.Helper()
	var err error
	state, err = store.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: turnID, Input: turnID + " input"})
	if err != nil {
		t.Fatalf("StartTurn %s: %v", turnID, err)
	}
	if withArtifact {
		artifact, putErr := store.PutArtifact(ctx, state.ID, ArtifactInput{Kind: "tool-output", MediaType: "text/plain", Data: []byte("artifact from turn one")})
		if putErr != nil {
			t.Fatalf("PutArtifact %s: %v", turnID, putErr)
		}
		state, err = store.ToolStarted(ctx, state.ID, state.Revision, ToolStarted{TurnID: turnID, ToolCallID: turnID + "-call", ToolName: "shell", Input: "{}"})
		if err != nil {
			t.Fatalf("ToolStarted %s: %v", turnID, err)
		}
		state, err = store.ToolCompleted(ctx, state.ID, state.Revision, ToolCompleted{TurnID: turnID, ToolCallID: turnID + "-call", ToolName: "shell", Artifact: &artifact})
		if err != nil {
			t.Fatalf("ToolCompleted %s: %v", turnID, err)
		}
	}
	state, err = store.RecordUsage(ctx, state.ID, ModelUsage{
		CallID:           turnID + "-usage",
		TurnID:           turnID,
		Operation:        UsageOperationAgent,
		HasProviderUsage: true,
		PromptTokens:     10,
		CompletionTokens: 4,
		TotalTokens:      14,
	})
	if err != nil {
		t.Fatalf("RecordUsage %s: %v", turnID, err)
	}
	state, err = store.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID:   turnID,
		Messages: []*schema.Message{schema.UserMessage(turnID + " input"), schema.AssistantMessage(turnID+" answer", nil)},
	})
	if err != nil {
		t.Fatalf("CommitTurn %s: %v", turnID, err)
	}
	return state
}

func allForkThreadFiles(t *testing.T, root, id string) map[string][]byte {
	t.Helper()
	dir := filepath.Join(root, sessionsDirName, id)
	files := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[relative] = append([]byte(nil), data...)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot thread files %s: %v", id, err)
	}
	return files
}

func readForkTestEvents(t *testing.T, dir string) []ThreadEvent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, journalFileName))
	if err != nil {
		t.Fatal(err)
	}
	var events []ThreadEvent
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event ThreadEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, event)
	}
	return events
}
