package tools

import (
	"context"
	"encoding/json"
	"testing"

	"eino-local-assistant/internal/store"
)

func TestReadArtifactIsScopedAndRangeBounded(t *testing.T) {
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	state, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: "artifact-tool"}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	artifact, err := threadStore.PutArtifact(context.Background(), state.ID, store.ArtifactInput{
		Kind:      "tool.output",
		MediaType: "text/plain",
		Data:      []byte("abcdefgh"),
	})
	if err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	state, err = threadStore.StartTurn(context.Background(), state.ID, state.Revision, store.TurnStart{TurnID: "turn", Input: "input"})
	if err != nil {
		t.Fatal(err)
	}
	state, err = threadStore.ToolStarted(context.Background(), state.ID, state.Revision, store.ToolStarted{TurnID: "turn", ToolCallID: "call", ToolName: "shell"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = threadStore.ToolCompleted(context.Background(), state.ID, state.Revision, store.ToolCompleted{TurnID: "turn", ToolCallID: "call", ToolName: "shell", Artifact: &artifact}); err != nil {
		t.Fatal(err)
	}
	readTool, err := NewReadArtifact()
	if err != nil {
		t.Fatalf("NewReadArtifact: %v", err)
	}
	ctx := store.WithThreadAccess(context.Background(), threadStore, state.ID)
	raw, err := readTool.InvokableRun(ctx, `{"artifact_id":"`+artifact.ID+`","offset":2,"max_bytes":3}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	var output ReadArtifactOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if output.Content != "cde" || !output.HasMore || output.Truncated {
		t.Fatalf("output = %+v", output)
	}
	if _, err := readTool.InvokableRun(context.Background(), `{"artifact_id":"`+artifact.ID+`"}`); err == nil {
		t.Fatal("unscoped artifact read should fail")
	}
}
