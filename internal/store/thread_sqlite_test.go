package store

import (
	"context"
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
	if _, err := st.db.Exec(`DELETE FROM thread_catalog WHERE id=?`, state.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadThread(context.Background(), state.ID); err != nil {
		t.Fatal(err)
	}
	var catalog int
	if err := st.db.QueryRow(`SELECT count(*) FROM thread_catalog WHERE id=?`, state.ID).Scan(&catalog); err != nil {
		t.Fatal(err)
	}
	if catalog != 1 {
		t.Fatalf("catalog rows = %d, want 1", catalog)
	}
	var eventTables int
	if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('events','messages','turns','usage_records','checkpoints')`).Scan(&eventTables); err != nil {
		t.Fatal(err)
	}
	if eventTables != 0 {
		t.Fatalf("event payload mirror tables remain: %d", eventTables)
	}
	journal := threadJournalPathForTest(t, st, state.ID)
	wantPath := filepath.Join(sessionDayDir(filepath.Join(root, sessionsDirName), state.CreatedAt), journalFileName(state.ID))
	if journal != wantPath {
		t.Fatalf("session journal = %q, want %q", journal, wantPath)
	}
	if info, err := os.Stat(journal); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("session journal = %v, %v", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(journal))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != journalFileName(state.ID) {
		t.Fatalf("session day entries = %#v, want only %q", entries, journalFileName(state.ID))
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
