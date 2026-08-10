package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestLoadThreadTranscriptReadOnlyDoesNotCreateProjection(t *testing.T) {
	root := t.TempDir()
	writable, err := NewThreadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := writable.CreateThread(context.Background(), ThreadMeta{ID: "read-only-export"}, "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	state, err = writable.StartTurn(context.Background(), state.ID, state.Revision, TurnStart{TurnID: "turn", Input: "question"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writable.CommitTurn(context.Background(), state.ID, state.Revision, TurnCommit{TurnID: "turn", Messages: []*schema.Message{schema.UserMessage("question")}}); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}

	projection := filepath.Join(root, threadDatabaseFile)
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenThreadStore(root, ThreadStoreOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	loaded, messages, err := readOnly.LoadThreadTranscriptReadOnly(context.Background(), "read-only-export", 0)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != "read-only-export" || len(messages) != 2 || messages[0].Content != "system prompt" || messages[1].Content != "question" {
		t.Fatalf("read-only transcript = state=%+v messages=%#v", loaded, messages)
	}
	if _, err := os.Stat(projection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only transcript recreated projection: %v", err)
	}
}
