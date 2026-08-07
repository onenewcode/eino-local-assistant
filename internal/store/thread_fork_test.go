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

func TestThreadStoreForkThreadRebuildsDirectJSONLPrefix(t *testing.T) {
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
	before, err := os.ReadFile(threadJournalPathForTest(t, threadStore, source.ID))
	if err != nil {
		t.Fatal(err)
	}

	result, err := threadStore.ForkThread(ctx, source.ID, "fork-child", "turn-1")
	if err != nil {
		t.Fatalf("ForkThread: %v", err)
	}
	childPath := threadJournalPathForTest(t, threadStore, result.ChildID)
	if filepath.Base(childPath) != journalFileName(result.ChildID) || filepath.Base(filepath.Dir(childPath)) != source.CreatedAt.UTC().Format("02") {
		t.Fatalf("child session path = %q", childPath)
	}
	if info, err := os.Stat(childPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("child journal = %v, %v", info, err)
	}
	after, err := os.ReadFile(threadJournalPathForTest(t, threadStore, source.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("fork changed the source journal")
	}

	child, err := threadStore.LoadThread(ctx, result.ChildID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Meta.ParentID != source.ID || child.Meta.ForkBoundaryTurnID != "turn-1" || child.Meta.ForkSourceHash != result.SourceHash {
		t.Fatalf("fork provenance = %#v", child.Meta)
	}
	groups, err := threadStore.LoadTurnGroups(ctx, result.ChildID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Committed == nil || groups[0].Tools[0].Completed.Artifact == nil {
		t.Fatalf("fork groups = %#v", groups)
	}
	read, err := threadStore.ReadArtifact(ctx, result.ChildID, groups[0].Tools[0].Completed.Artifact.ID, 0, 1024)
	if err != nil || string(read.Data) != "artifact from turn one" {
		t.Fatalf("fork artifact = %#v, %v", read, err)
	}
}

func TestThreadStoreForkThreadBeforeFirstTurnKeepsPromptInJournal(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "fork-before-source", Title: "source"}, "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	source = appendForkTestTurn(ctx, t, threadStore, source, "turn-1", false)
	source, err = threadStore.SetThreadTitle(ctx, source.ID, source.Revision, "current title")
	if err != nil {
		t.Fatal(err)
	}
	result, err := threadStore.ForkThreadBeforeFirstTurn(ctx, source.ID, "fork-before-child")
	if err != nil {
		t.Fatal(err)
	}
	child, err := threadStore.LoadThread(ctx, result.ChildID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Revision != 1 || child.SystemPrompt != "system prompt" || child.Meta.Title != "current title" || child.Meta.ParentID != source.ID {
		t.Fatalf("before-first child = %#v", child)
	}
	data, err := os.ReadFile(threadJournalPathForTest(t, threadStore, result.ChildID))
	if err != nil {
		t.Fatal(err)
	}
	if !containsJSONString(data, "system prompt") {
		t.Fatalf("frozen prompt is absent from child JSONL: %s", data)
	}
}

func TestThreadStoreForkThreadRejectsUnsafeSourceAndDestination(t *testing.T) {
	ctx := context.Background()
	threadStore, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := threadStore.CreateThread(ctx, ThreadMeta{ID: "fork-reject-source"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.ForkThread(ctx, source.ID, "fork-child", ""); !errors.Is(err, ErrForkNoCommittedTurn) {
		t.Fatalf("empty fork error = %v", err)
	}
	state, err := threadStore.StartTurn(ctx, source.ID, source.Revision, TurnStart{TurnID: "active", Input: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.ForkThread(ctx, source.ID, "fork-child", ""); !errors.Is(err, ErrForkActiveTurn) {
		t.Fatalf("active fork error = %v", err)
	}
	state, err = threadStore.FailTurn(ctx, source.ID, state.Revision, TurnFailure{TurnID: "active", Error: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	state = appendForkTestTurn(ctx, t, threadStore, state, "committed", false)
	if _, err := threadStore.ForkThread(ctx, source.ID, "fork-child", "committed"); err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.ForkThread(ctx, source.ID, "fork-child", "committed"); !errors.Is(err, ErrForkDestinationExists) {
		t.Fatalf("collision error = %v", err)
	}
	_, state, err = threadStore.CommitCheckpoint(ctx, source.ID, state.Revision, CheckpointInput{ID: "checkpoint", Payload: json.RawMessage(`{"summary":"state"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.ForkThread(ctx, source.ID, "checkpoint-child", ""); !errors.Is(err, ErrForkUnsupportedState) {
		t.Fatalf("checkpoint fork error = %v", err)
	}
}

func appendForkTestTurn(ctx context.Context, t *testing.T, threadStore *ThreadStore, state ThreadState, turnID string, withArtifact bool) ThreadState {
	t.Helper()
	var err error
	state, err = threadStore.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: turnID, Input: turnID + " input"})
	if err != nil {
		t.Fatal(err)
	}
	if withArtifact {
		artifact, err := threadStore.PutArtifact(ctx, state.ID, ArtifactInput{Kind: "tool-output", Data: []byte("artifact from turn one")})
		if err != nil {
			t.Fatal(err)
		}
		state, err = threadStore.ToolStarted(ctx, state.ID, state.Revision, ToolStarted{TurnID: turnID, ToolCallID: turnID + "-call", ToolName: "shell"})
		if err != nil {
			t.Fatal(err)
		}
		state, err = threadStore.ToolCompleted(ctx, state.ID, state.Revision, ToolCompleted{TurnID: turnID, ToolCallID: turnID + "-call", ToolName: "shell", Artifact: &artifact})
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err = threadStore.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{TurnID: turnID, Messages: []*schema.Message{schema.UserMessage(turnID + " input"), schema.AssistantMessage(turnID+" answer", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func containsJSONString(data []byte, value string) bool {
	var record map[string]any
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if json.Unmarshal(line, &record) == nil {
			if string(line) != "" && strings.Contains(string(line), value) {
				return true
			}
		}
	}
	return false
}
