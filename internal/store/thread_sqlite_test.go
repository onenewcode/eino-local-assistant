package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionProjectionRepairsFromCanonicalJournal(t *testing.T) {
	root := t.TempDir()
	st, err := NewThreadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(context.Background(), ThreadMeta{ID: "projection-repair"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	state, err = st.StartTurn(context.Background(), state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM threads WHERE id=?`, state.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadThread(context.Background(), state.ID); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := st.db.QueryRow(`SELECT count(*) FROM events WHERE thread_id=?`, state.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != int(state.HeadSequence) {
		t.Fatalf("projected events = %d, want %d", events, state.HeadSequence)
	}
	for _, name := range []string{stateFileName, metaFileName} {
		if _, err := os.Stat(filepath.Join(root, sessionsDirName, state.ID, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy projection %s exists: %v", name, err)
		}
	}
}

func TestSessionProjectionFailureDoesNotFailCommittedMutation(t *testing.T) {
	root := t.TempDir()
	st, err := NewThreadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(context.Background(), ThreadMeta{ID: "projection-lag"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.db.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = st.StartTurn(context.Background(), state.ID, state.Revision, TurnStart{TurnID: "turn-1", Input: "once"})
	if err != nil {
		t.Fatalf("journal commit reported projection failure: %v", err)
	}
	reopened, err := NewThreadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.LoadThread(context.Background(), state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != state.Revision {
		t.Fatalf("revision = %d, want %d", got.Revision, state.Revision)
	}
}

func TestLegacySessionGateDoesNotCreateProjection(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, sessionsDirName, "legacy-v3")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, journalFileName), []byte("{\"format_version\":3}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewThreadStore(root); err == nil {
		t.Fatal("legacy v3 store opened")
	}
	if _, err := os.Stat(filepath.Join(root, threadDatabaseFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy gate mutated projection: %v", err)
	}
}
