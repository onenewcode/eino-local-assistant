package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestResumeSnapshotUsesCheckpointTailAndRepairsBadOffset(t *testing.T) {
	ctx := context.Background()
	st, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, ThreadMeta{ID: "resume-tail"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []string{"one", "two"} {
		state, err = st.StartTurn(ctx, state.ID, state.Revision, TurnStart{TurnID: "turn-" + turn, Input: turn})
		if err != nil {
			t.Fatal(err)
		}
		state, err = st.CommitTurn(ctx, state.ID, state.Revision, TurnCommit{TurnID: "turn-" + turn, Messages: []*schema.Message{
			schema.UserMessage(turn), schema.AssistantMessage("answer-"+turn, nil),
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	groups, err := st.LoadTurnGroups(ctx, state.ID)
	if err != nil || len(groups) != 2 {
		t.Fatalf("turn groups = %#v, %v", groups, err)
	}
	_, state, err = st.CommitCheckpoint(ctx, state.ID, state.Revision, CheckpointInput{
		ID:                "checkpoint-tail",
		Kind:              "structured",
		Payload:           json.RawMessage(`{"schema_version":2}`),
		SourceEventIDs:    groups[0].SourceEventIDs,
		TailStartSequence: groups[1].StartSequence,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := st.LoadThreadResumeSnapshot(ctx, state.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.TurnGroups) != 1 || snapshot.TurnGroups[0].TurnID != "turn-two" {
		t.Fatalf("indexed resume groups = %#v, want only uncovered tail", snapshot.TurnGroups)
	}
	if len(snapshot.CheckpointLineage) != 1 || snapshot.CheckpointLineage[0].ID != "checkpoint-tail" {
		t.Fatalf("checkpoint lineage = %#v", snapshot.CheckpointLineage)
	}

	if _, err := st.db.Exec(`UPDATE resume_index SET tail_start_offset=999999 WHERE thread_id=?`, state.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err = st.LoadThreadResumeSnapshot(ctx, state.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.TurnGroups) != 2 || len(snapshot.CheckpointLineage) != 0 {
		t.Fatalf("bad offset must fall back to canonical replay: %#v", snapshot)
	}
}
