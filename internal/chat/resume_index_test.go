package chat

import (
	"context"
	"encoding/json"
	"testing"

	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

func TestOpenSessionUsesCheckpointAndUncoveredTail(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, store.ThreadMeta{ID: "resume-checkpoint-tail"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []string{"covered-marker", "tail-marker"} {
		state, err = st.StartTurn(ctx, state.ID, state.Revision, store.TurnStart{TurnID: "turn-" + turn, Input: turn})
		if err != nil {
			t.Fatal(err)
		}
		state, err = st.CommitTurn(ctx, state.ID, state.Revision, store.TurnCommit{TurnID: "turn-" + turn, Messages: []*schema.Message{
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
	source := durableContextGroups(groups[:1])
	checkpoint, err := contextbuild.DeterministicCheckpoint(contextbuild.CompactionRequest{SourceGroups: source})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = st.CommitCheckpoint(ctx, state.ID, state.Revision, store.CheckpointInput{
		ID:                "resume-checkpoint",
		Kind:              "structured",
		Payload:           payload,
		SourceEventIDs:    groups[0].SourceEventIDs,
		SourceHash:        checkpoint.DirectSourceHash(),
		TailStartSequence: groups[1].StartSequence,
	})
	if err != nil {
		t.Fatal(err)
	}

	session, err := OpenSession(&scriptedModel{}, st, state.ID, SessionOptions{Store: st, Context: contextbuild.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	view, _, err := session.threadPrompt(schema.UserMessage("continue"))
	if err != nil {
		t.Fatal(err)
	}
	var rawCovered, rawTail bool
	for _, message := range view {
		if message == nil || message.Role != schema.User {
			continue
		}
		if message.Content == "covered-marker" {
			rawCovered = true
		}
		if message.Content == "tail-marker" {
			rawTail = true
		}
	}
	if rawCovered || !rawTail {
		t.Fatalf("prompt raw groups covered=%v tail=%v: %#v", rawCovered, rawTail, view)
	}
}
