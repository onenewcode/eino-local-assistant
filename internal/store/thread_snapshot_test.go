package store

import (
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

func TestThreadStoreSnapshotThreadCopiesLedgerAndKeepsSourceImmutable(t *testing.T) {
	ctx := context.Background()
	source, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore source: %v", err)
	}
	destination, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore destination: %v", err)
	}

	state, err := source.CreateThread(ctx, ThreadMeta{ID: "thread-snapshot", Title: "source"}, "stored system prompt")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	state, err = source.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-source", Input: "source prompt"})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	artifact, err := source.PutArtifact(ctx, state.ID, ArtifactInput{Kind: "tool-output", Data: []byte("complete artifact output")})
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	state, err = source.ToolStarted(ctx, state.ID, state.Revision, ToolStarted{
		TurnID: "turn-source", ToolCallID: "call-source", ToolName: "shell", Input: "{}",
	})
	if err != nil {
		t.Fatalf("ToolStarted: %v", err)
	}
	state, err = source.ToolCompleted(ctx, state.ID, state.Revision, ToolCompleted{
		TurnID: "turn-source", ToolCallID: "call-source", ToolName: "shell", Artifact: &artifact,
	})
	if err != nil {
		t.Fatalf("ToolCompleted: %v", err)
	}
	state, err = source.RecordUsage(ctx, state.ID, ModelUsage{
		CallID: "usage-source", TurnID: "turn-source", Operation: UsageOperationAgent,
		HasProviderUsage: true, PromptTokens: 12, CompletionTokens: 5, TotalTokens: 17,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	state, err = source.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{
		TurnID:   "turn-source",
		Messages: []*schema.Message{schema.UserMessage("source prompt"), schema.AssistantMessage("source answer", nil)},
	})
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	state, err = source.UpdateTaskState(ctx, state.ID, state.Revision, "", TaskStateUpdate{
		Snapshot: json.RawMessage(`{"version":1,"status":"complete"}`),
	})
	if err != nil {
		t.Fatalf("UpdateTaskState: %v", err)
	}
	checkpoint, state, err := source.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:      "checkpoint-source",
		Payload: json.RawMessage(`{"summary":"source context"}`),
	})
	if err != nil {
		t.Fatalf("CommitCheckpoint: %v", err)
	}

	before, err := snapshotThreadFiles(source.Root(), state.ID)
	if err != nil {
		t.Fatalf("snapshot source files before clone: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(source.Root(), sessionsDirName, state.ID, locksDir)); err != nil {
		t.Fatalf("remove source locks before clone: %v", err)
	}
	if err := source.SnapshotThread(ctx, state.ID, destination); err != nil {
		t.Fatalf("SnapshotThread: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(source.Root(), sessionsDirName, state.ID, locksDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only snapshot created source locks: stat error=%v", err)
	}

	destinationDir := filepath.Join(destination.Root(), sessionsDirName, state.ID)
	if _, err := os.Stat(filepath.Join(destinationDir, locksDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot copied source locks directory: stat error=%v", err)
	}
	afterClone, err := snapshotThreadFiles(destination.Root(), state.ID)
	if err != nil {
		t.Fatalf("snapshot destination files: %v", err)
	}
	if !reflect.DeepEqual(afterClone, before) {
		t.Fatalf("destination snapshot differs from source:\nsource=%#v\ndestination=%#v", before, afterClone)
	}

	loaded, err := destination.LoadThread(ctx, state.ID)
	if err != nil {
		t.Fatalf("LoadThread destination: %v", err)
	}
	if loaded.Revision != state.Revision || loaded.SystemPrompt != state.SystemPrompt || string(loaded.TaskState) != string(state.TaskState) {
		t.Fatalf("destination state = %#v, want revision/system/task state from source %#v", loaded, state)
	}
	loadedCheckpoint, err := destination.LoadCheckpoint(ctx, state.ID, checkpoint.ID)
	if err != nil {
		t.Fatalf("LoadCheckpoint destination: %v", err)
	}
	if !reflect.DeepEqual(loadedCheckpoint, checkpoint) {
		t.Fatalf("destination checkpoint = %#v, want %#v", loadedCheckpoint, checkpoint)
	}
	read, err := destination.ReadArtifact(ctx, state.ID, artifact.ID, 0, artifact.StoredSize)
	if err != nil {
		t.Fatalf("ReadArtifact destination: %v", err)
	}
	if string(read.Data) != "complete artifact output" || read.HasMore {
		t.Fatalf("destination artifact = %#v", read)
	}

	mutated, err := destination.StartTurn(ctx, state.ID, loaded.Revision, TurnStart{TurnID: "turn-temporary", Input: "temporary prompt"})
	if err != nil {
		t.Fatalf("StartTurn destination: %v", err)
	}
	if _, err := destination.CommitTurn(ctx, state.ID, mutated.Revision, TurnCommit{
		TurnID:   "turn-temporary",
		Messages: []*schema.Message{schema.UserMessage("temporary prompt"), schema.AssistantMessage("temporary answer", nil)},
	}); err != nil {
		t.Fatalf("CommitTurn destination: %v", err)
	}

	after, err := snapshotThreadFiles(source.Root(), state.ID)
	if err != nil {
		t.Fatalf("snapshot source files after clone: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("source changed after destination mutation:\nbefore=%#v\nafter=%#v", before, after)
	}
}

func TestThreadStoreListThreadsReadOnlyDoesNotRepairProjections(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "thread-read-only"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	statePath := filepath.Join(threadStore.Root(), sessionsDirName, state.ID, stateFileName)
	metaPath := filepath.Join(threadStore.Root(), sessionsDirName, state.ID, metaFileName)
	staleState := []byte(`{"id":"stale-state"}`)
	staleMeta := []byte(`{"id":"stale-meta"}`)
	if err := os.WriteFile(statePath, staleState, 0o600); err != nil {
		t.Fatalf("write stale state: %v", err)
	}
	if err := os.WriteFile(metaPath, staleMeta, 0o600); err != nil {
		t.Fatalf("write stale meta: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(threadStore.Root(), sessionsDirName, state.ID, locksDir)); err != nil {
		t.Fatalf("remove source locks before read-only list: %v", err)
	}

	threads, err := threadStore.ListThreadsReadOnly(ctx)
	if err != nil {
		t.Fatalf("ListThreadsReadOnly: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != state.ID {
		t.Fatalf("read-only threads = %#v", threads)
	}
	gotState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after read-only list: %v", err)
	}
	gotMeta, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta after read-only list: %v", err)
	}
	if !reflect.DeepEqual(gotState, staleState) || !reflect.DeepEqual(gotMeta, staleMeta) {
		t.Fatalf("read-only list repaired projections: state=%q meta=%q", gotState, gotMeta)
	}
	if _, err := os.Lstat(filepath.Join(threadStore.Root(), sessionsDirName, state.ID, locksDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only list created source locks: stat error=%v", err)
	}
}

func TestThreadStoreSnapshotThreadRejectsSymlinkedSupportFile(t *testing.T) {
	ctx := context.Background()
	source, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore source: %v", err)
	}
	destination, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore destination: %v", err)
	}
	state, err := source.CreateThread(ctx, ThreadMeta{ID: "thread-symlink"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := os.WriteFile(outside, []byte(`{"unsafe":true}`), 0o600); err != nil {
		t.Fatalf("write outside checkpoint: %v", err)
	}
	symlink := filepath.Join(source.Root(), sessionsDirName, state.ID, checkpointsDir, "checkpoint-link.json")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err = source.SnapshotThread(ctx, state.ID, destination)
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("SnapshotThread error=%v, want symlink rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(destination.Root(), sessionsDirName, state.ID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed snapshot published destination thread: stat error=%v", statErr)
	}
}

func TestCopySnapshotFileRejectsPathReplacement(t *testing.T) {
	sourceDir := t.TempDir()
	destinationDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "source")
	replacementPath := filepath.Join(sourceDir, "replacement")
	if err := os.WriteFile(sourcePath, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original source: %v", err)
	}
	initialInfo, err := os.Lstat(sourcePath)
	if err != nil {
		t.Fatalf("lstat original source: %v", err)
	}
	if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement source: %v", err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatalf("remove original source: %v", err)
	}
	if err := os.Rename(replacementPath, sourcePath); err != nil {
		t.Fatalf("replace source path: %v", err)
	}

	destinationPath := filepath.Join(destinationDir, "copy")
	err = copySnapshotFile(sourcePath, destinationPath, initialInfo)
	if err == nil || !strings.Contains(err.Error(), "source changed after inspection") {
		t.Fatalf("copySnapshotFile error = %v, want path replacement rejection", err)
	}
	if _, statErr := os.Stat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("path replacement published destination: stat error=%v", statErr)
	}
}

func snapshotThreadFiles(root, id string) (map[string][]byte, error) {
	dir := filepath.Join(root, sessionsDirName, id)
	files := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if relative == locksDir || strings.HasPrefix(relative, locksDir+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = data
		return nil
	})
	return files, err
}
