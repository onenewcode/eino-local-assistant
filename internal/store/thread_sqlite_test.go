package store

import (
	"context"
	"encoding/json"
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
	entries, err := os.ReadDir(threadPathForTest(t, st, state.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() != summaryFileName || entries[1].Name() != journalFileName {
		t.Fatalf("session directory entries = %#v, want %q and %q", entries, summaryFileName, journalFileName)
	}
	raw, err := os.ReadFile(filepath.Join(threadPathForTest(t, st, state.ID), summaryFileName))
	if err != nil {
		t.Fatal(err)
	}
	var summary sessionSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.ID != state.ID || summary.Revision != state.Revision || summary.Meta.ID != state.ID {
		t.Fatalf("session summary = %#v, want projection of %#v", summary, state)
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

func TestLegacySessionDoesNotBlockNewThread(t *testing.T) {
	root := t.TempDir()
	legacyID := "20260716-030750-13835c"
	dir := filepath.Join(root, sessionsDirName, "2026", "07", "16")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyJournal := []byte("{\"format_version\":2}\n")
	legacyPath := filepath.Join(dir, legacyID+".jsonl")
	if err := os.WriteFile(legacyPath, legacyJournal, 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := NewThreadStore(root)
	if err != nil {
		t.Fatalf("NewThreadStore with legacy session: %v", err)
	}
	defer st.Close()

	fresh, err := st.CreateThread(context.Background(), ThreadMeta{ID: "fresh-v4"}, "system")
	if err != nil {
		t.Fatalf("CreateThread alongside legacy session: %v", err)
	}
	if _, err := st.LoadThread(context.Background(), fresh.ID); err != nil {
		t.Fatalf("LoadThread fresh session: %v", err)
	}
	wantFreshPath := filepath.Join(sessionDayDir(filepath.Join(root, sessionsDirName), fresh.CreatedAt), fresh.ID, journalFileName)
	if got := threadJournalPathForTest(t, st, fresh.ID); got != wantFreshPath {
		t.Fatalf("fresh session path = %q, want %q", got, wantFreshPath)
	}
	if _, err := os.Lstat(filepath.Join(root, sessionsDirName, fresh.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh session leaked into flat namespace: %v", err)
	}
	gotLegacyJournal, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotLegacyJournal) != string(legacyJournal) {
		t.Fatalf("legacy journal changed: got %q, want %q", gotLegacyJournal, legacyJournal)
	}
	threads, err := st.ListThreads(context.Background())
	if err != nil {
		t.Fatalf("ListThreads with legacy session: %v", err)
	}
	if len(threads) != 1 || threads[0].ID != fresh.ID {
		t.Fatalf("ListThreads = %#v, want only fresh session", threads)
	}
	if _, err := st.LoadThread(context.Background(), legacyID); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadThread legacy error = %v, want missing active session", err)
	}
}
