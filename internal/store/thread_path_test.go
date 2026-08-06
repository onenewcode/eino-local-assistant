package store

import (
	"path/filepath"
	"testing"
	"time"
)

func threadPathForTest(t *testing.T, store *ThreadStore, id string) string {
	t.Helper()
	path, err := store.ThreadPath(id)
	if err != nil {
		t.Fatalf("resolve thread path %q: %v", id, err)
	}
	return filepath.Dir(path)
}

func threadJournalPathForTest(t *testing.T, store *ThreadStore, id string) string {
	t.Helper()
	path, err := store.ThreadPath(id)
	if err != nil {
		t.Fatalf("resolve thread journal %q: %v", id, err)
	}
	return path
}

func newThreadPathForTest(t *testing.T, store *ThreadStore, id string) string {
	t.Helper()
	return store.newThreadJournalPath(id, time.Now().UTC())
}
