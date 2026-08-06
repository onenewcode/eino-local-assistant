package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestThreadStoreSnapshotThreadCopiesSessionBundleLedger(t *testing.T) {
	ctx := context.Background()
	source, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := source.CreateThread(ctx, ThreadMeta{ID: "snapshot-source", Title: "source"}, "stored system prompt")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := source.PutArtifact(ctx, state.ID, ArtifactInput{Data: []byte("artifact output")})
	if err != nil {
		t.Fatal(err)
	}
	state, err = source.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn", Input: "input"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = source.ToolStarted(ctx, state.ID, state.Revision, ToolStarted{TurnID: "turn", ToolCallID: "call", ToolName: "shell"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = source.ToolCompleted(ctx, state.ID, state.Revision, ToolCompleted{TurnID: "turn", ToolCallID: "call", ToolName: "shell", Artifact: &artifact})
	if err != nil {
		t.Fatal(err)
	}
	state, err = source.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{TurnID: "turn", Messages: []*schema.Message{schema.UserMessage("input"), schema.AssistantMessage("output", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(threadJournalPathForTest(t, source, state.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SnapshotThread(ctx, state.ID, destination); err != nil {
		t.Fatal(err)
	}
	destinationPath := threadJournalPathForTest(t, destination, state.ID)
	if filepath.Base(destinationPath) != journalFileName || filepath.Base(filepath.Dir(destinationPath)) != state.ID {
		t.Fatalf("snapshot path = %q", destinationPath)
	}
	after, err := os.ReadFile(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("snapshot journal differs from source")
	}
	loaded, err := destination.LoadThread(ctx, state.ID)
	if err != nil || loaded.SystemPrompt != "stored system prompt" {
		t.Fatalf("snapshot state = %#v, %v", loaded, err)
	}
	read, err := destination.ReadArtifact(ctx, state.ID, artifact.ID, 0, 64)
	if err != nil || string(read.Data) != "artifact output" {
		t.Fatalf("snapshot artifact = %#v, %v", read, err)
	}
}

func TestSnapshotThreadRejectsSymlinkedJournal(t *testing.T) {
	ctx := context.Background()
	source, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := source.CreateThread(ctx, ThreadMeta{ID: "snapshot-symlink"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	path := threadJournalPathForTest(t, source, state.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := source.SnapshotThread(ctx, state.ID, destination); err == nil {
		t.Fatal("snapshot accepted a symlinked journal")
	}
}
