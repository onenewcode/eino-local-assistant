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
	if _, err := st.db.Exec(`DELETE FROM session_catalog WHERE id=?`, state.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadThread(context.Background(), state.ID); err != nil {
		t.Fatal(err)
	}
	var catalog int
	if err := st.db.QueryRow(`SELECT count(*) FROM session_catalog WHERE id=?`, state.ID).Scan(&catalog); err != nil {
		t.Fatal(err)
	}
	if catalog != 1 {
		t.Fatalf("catalog rows = %d, want 1", catalog)
	}
	var extraTables int
	if err := st.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'session_catalog'`).Scan(&extraTables); err != nil {
		t.Fatal(err)
	}
	if extraTables != 0 {
		t.Fatalf("non-catalog tables remain: %d", extraTables)
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
	metas, err := st.ListThreads(context.Background())
	if err != nil || len(metas) != 1 || metas[0].ID != state.ID {
		t.Fatalf("list after catalog query failure = %#v, %v", metas, err)
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

func TestSessionCatalogRebuildsAfterDamageAndOldSchema(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := NewThreadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	state, err := st.CreateThread(ctx, ThreadMeta{ID: "catalog-rebuild"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	journal := threadJournalPathForTest(t, st, state.ID)
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	databasePath := filepath.Join(root, threadDatabaseFile)
	if err := os.WriteFile(databasePath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewThreadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.LoadThread(ctx, state.ID)
	if err != nil || loaded.HeadSequence != state.HeadSequence || loaded.LastHash != state.LastHash {
		t.Fatalf("load after catalog damage = %#v, %v", loaded, err)
	}
	afterDamage, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterDamage) != string(before) {
		t.Fatal("catalog rebuild changed the canonical journal")
	}
	if _, err := reopened.db.Exec(`PRAGMA user_version=3`); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	withOldSchema, err := NewThreadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer withOldSchema.Close()
	loaded, err = withOldSchema.LoadThread(ctx, state.ID)
	if err != nil || loaded.HeadSequence != state.HeadSequence || loaded.LastHash != state.LastHash {
		t.Fatalf("load after old catalog schema = %#v, %v", loaded, err)
	}
	var tableCount int
	if err := withOldSchema.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_catalog'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatalf("session_catalog table count = %d, want 1", tableCount)
	}
	var extraTables int
	if err := withOldSchema.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != 'session_catalog'`).Scan(&extraTables); err != nil {
		t.Fatal(err)
	}
	if extraTables != 0 {
		t.Fatalf("old projection tables survived rebuild: %d", extraTables)
	}
}

func TestSessionCatalogFallsBackToScanAndPrunesDeletedJournal(t *testing.T) {
	ctx := context.Background()
	st, err := NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.CreateThread(ctx, ThreadMeta{ID: "catalog-first"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateThread(ctx, ThreadMeta{ID: "catalog-second"}, "system")
	if err != nil {
		t.Fatal(err)
	}
	secondPath := threadJournalPathForTest(t, st, second.ID)
	if _, err := st.db.Exec(`UPDATE session_catalog SET journal_relpath=? WHERE id=?`, "2000/01/01/"+journalFileName(first.ID), first.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadThread(ctx, first.ID)
	if err != nil || loaded.LastHash != first.LastHash {
		t.Fatalf("catalog path fallback load = %#v, %v", loaded, err)
	}
	if err := os.Remove(secondPath); err != nil {
		t.Fatal(err)
	}
	metas, err := st.ListThreads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != first.ID {
		t.Fatalf("sessions after external deletion = %#v", metas)
	}
	var staleRows int
	if err := st.db.QueryRow(`SELECT count(*) FROM session_catalog WHERE id=?`, second.ID).Scan(&staleRows); err != nil {
		t.Fatal(err)
	}
	if staleRows != 0 {
		t.Fatalf("deleted journal still has %d catalog rows", staleRows)
	}
}
