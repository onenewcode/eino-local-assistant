package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // Register the pure-Go database/sql driver.
)

const (
	threadDatabaseFile             = "state.sqlite3"
	SessionProjectionSchemaVersion = 4
	timeFormat                     = "2006-01-02T15:04:05.999999999Z07:00"
)

// sessionCatalogEntry is deliberately bounded. JSONL is the only source for
// state, transcript, checkpoints, artifacts, and every event payload.
type sessionCatalogEntry struct {
	ID             string
	JournalRelPath string
	JournalBytes   int64
	JournalModNS   int64
	HeadSequence   uint64
	HeadHash       string
	Meta           ThreadMeta
	UpdatedAt      string
}

func (s *ThreadStore) openProjection(readOnly bool) error {
	path := filepath.Join(s.root, threadDatabaseFile)
	if readOnly {
		return s.openReadOnlyProjection(path)
	}
	return s.openWritableProjection(path, true)
}

func (s *ThreadStore) openReadOnlyProjection(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != SessionProjectionSchemaVersion {
		_ = db.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("unsupported session catalog schema %d", version)
	}
	s.db = db
	return nil
}

func (s *ThreadStore) openWritableProjection(path string, allowReset bool) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open session catalog: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := configureProjection(db); err != nil {
		_ = db.Close()
		return s.retryProjectionAfterReset(path, err, allowReset)
	}

	version, hasTables, err := projectionVersion(db)
	if err != nil {
		_ = db.Close()
		return s.retryProjectionAfterReset(path, err, allowReset)
	}
	if version != SessionProjectionSchemaVersion && (version != 0 || hasTables) {
		_ = db.Close()
		if !allowReset {
			return fmt.Errorf("unsupported session catalog schema %d", version)
		}
		if err := resetProjectionFiles(path); err != nil {
			return fmt.Errorf("reset session catalog schema %d: %w", version, err)
		}
		return s.openWritableProjection(path, false)
	}

	if err := initializeProjectionSchema(db); err != nil {
		_ = db.Close()
		return s.retryProjectionAfterReset(path, err, allowReset)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return fmt.Errorf("set session catalog permissions: %w", err)
	}
	s.db = db
	return nil
}

func configureProjection(db *sql.DB) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return fmt.Errorf("configure session catalog: %w", err)
		}
	}
	return nil
}

func projectionVersion(db *sql.DB) (int, bool, error) {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return 0, false, fmt.Errorf("read session catalog schema: %w", err)
	}
	var tableCount int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&tableCount); err != nil {
		return 0, false, fmt.Errorf("inspect session catalog schema: %w", err)
	}
	return version, tableCount > 0, nil
}

func initializeProjectionSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS session_catalog (
  id TEXT PRIMARY KEY,
  journal_relpath TEXT NOT NULL UNIQUE,
  journal_bytes INTEGER NOT NULL,
  journal_mod_ns INTEGER NOT NULL,
  head_sequence INTEGER NOT NULL,
  head_hash TEXT NOT NULL,
  meta_json BLOB NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS session_catalog_updated_at ON session_catalog(updated_at DESC, id DESC);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create session catalog schema: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, SessionProjectionSchemaVersion)); err != nil {
		return fmt.Errorf("set session catalog schema: %w", err)
	}
	return nil
}

func (s *ThreadStore) retryProjectionAfterReset(path string, cause error, allowReset bool) error {
	if !allowReset || !projectionFailureIsResettable(cause) {
		return cause
	}
	if err := resetProjectionFiles(path); err != nil {
		return fmt.Errorf("reset damaged session catalog: %w", err)
	}
	return s.openWritableProjection(path, false)
}

// projectionFailureIsResettable deliberately excludes locks and permission
// failures. A catalog is disposable, but another process may own its files.
func projectionFailureIsResettable(err error) bool {
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "locked") || strings.Contains(text, "busy") || strings.Contains(text, "permission denied") || strings.Contains(text, "operation not permitted") {
		return false
	}
	for _, marker := range []string{
		"malformed",
		"not a database",
		"file is encrypted",
		"unsupported file format",
		"database disk image is malformed",
		"database schema has changed",
		"no such table",
		"has no column named",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// resetProjectionFiles only removes the exact disposable catalog and its
// SQLite sidecars; canonical session journals are never touched.
func resetProjectionFiles(path string) error {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *ThreadStore) projectEvent(dir string, state ThreadState, _ ThreadEvent) error {
	return s.projectThread(dir, state, nil)
}

func (s *ThreadStore) projectThread(dir string, state ThreadState, _ []ThreadEvent) error {
	if s.db == nil || s.readOnly {
		return nil
	}
	return s.upsertSessionCatalog(dir, state)
}

// SQLite has no recoverable thread projection. Keeping this hook preserves the
// loader shape while making every state read replay the canonical JSONL.
func (s *ThreadStore) loadProjectedThread(_ string, _ string) (ThreadState, []ThreadEvent, bool, error) {
	return ThreadState{}, nil, false, nil
}

func (s *ThreadStore) upsertSessionCatalog(dir string, state ThreadState) error {
	path := journalPath(dir, state.ID)
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	relPath, err := filepath.Rel(s.sessionsRoot(), path)
	if err != nil || !validSessionRelativePath(filepath.ToSlash(relPath), state.ID) {
		return errors.New("session journal is outside the sessions root")
	}
	raw, err := json.Marshal(state.Meta)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO session_catalog(id,journal_relpath,journal_bytes,journal_mod_ns,head_sequence,head_hash,meta_json,updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET journal_relpath=excluded.journal_relpath,journal_bytes=excluded.journal_bytes,journal_mod_ns=excluded.journal_mod_ns,head_sequence=excluded.head_sequence,head_hash=excluded.head_hash,meta_json=excluded.meta_json,updated_at=excluded.updated_at`,
		state.ID, filepath.ToSlash(relPath), info.Size(), info.ModTime().UnixNano(), state.HeadSequence, state.LastHash, raw, state.UpdatedAt.UTC().Format(timeFormat))
	return err
}

func validSessionRelativePath(relPath, id string) bool {
	if relPath == "" || relPath == "." || filepath.IsAbs(relPath) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) != 4 || parts[3] != journalFileName(id) {
		return false
	}
	_, err := time.Parse(sessionDateLayout, strings.Join(parts[:3], "/"))
	return err == nil
}

// catalogJournalPath returns only an existing regular journal beneath the
// current sessions root. Anything stale, malformed, or unreadable falls back
// to the canonical JSONL scan instead of blocking resume.
func (s *ThreadStore) catalogJournalPath(id string) (string, bool) {
	if s.db == nil {
		return "", false
	}
	var relPath string
	if err := s.db.QueryRow(`SELECT journal_relpath FROM session_catalog WHERE id=?`, id).Scan(&relPath); err != nil {
		return "", false
	}
	if !validSessionRelativePath(relPath, id) {
		return "", false
	}
	path := filepath.Join(s.sessionsRoot(), filepath.FromSlash(relPath))
	rootRelative, err := filepath.Rel(s.sessionsRoot(), path)
	if err != nil || !validSessionRelativePath(filepath.ToSlash(rootRelative), id) {
		return "", false
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false
	}
	return path, true
}

func (s *ThreadStore) sessionCatalogEntries() (map[string]sessionCatalogEntry, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id,journal_relpath,journal_bytes,journal_mod_ns,head_sequence,head_hash,meta_json,updated_at FROM session_catalog`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make(map[string]sessionCatalogEntry)
	for rows.Next() {
		var entry sessionCatalogEntry
		var raw []byte
		if err := rows.Scan(&entry.ID, &entry.JournalRelPath, &entry.JournalBytes, &entry.JournalModNS, &entry.HeadSequence, &entry.HeadHash, &raw, &entry.UpdatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &entry.Meta) != nil || entry.Meta.ID != entry.ID || !validSessionRelativePath(entry.JournalRelPath, entry.ID) {
			continue
		}
		entries[entry.ID] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *ThreadStore) catalogEntryIsFresh(entry sessionCatalogEntry, path string) bool {
	relPath, err := filepath.Rel(s.sessionsRoot(), path)
	if err != nil || filepath.ToSlash(relPath) != entry.JournalRelPath {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() == entry.JournalBytes && info.ModTime().UnixNano() == entry.JournalModNS
}

// orderedCatalogMetas delegates the normal sessions/--last ordering to the
// catalog index, but only after the current filesystem scan proves every row
// still names the same journal fingerprint.
func (s *ThreadStore) orderedCatalogMetas(paths map[string]string) ([]ThreadMeta, bool) {
	if s.db == nil {
		return nil, false
	}
	rows, err := s.db.Query(`SELECT id,journal_relpath,journal_bytes,journal_mod_ns,head_sequence,head_hash,meta_json,updated_at FROM session_catalog ORDER BY updated_at DESC,id DESC`)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	metas := make([]ThreadMeta, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for rows.Next() {
		var entry sessionCatalogEntry
		var raw []byte
		if err := rows.Scan(&entry.ID, &entry.JournalRelPath, &entry.JournalBytes, &entry.JournalModNS, &entry.HeadSequence, &entry.HeadHash, &raw, &entry.UpdatedAt); err != nil {
			return nil, false
		}
		path, exists := paths[entry.ID]
		if !exists || json.Unmarshal(raw, &entry.Meta) != nil || entry.Meta.ID != entry.ID || !validSessionRelativePath(entry.JournalRelPath, entry.ID) || !s.catalogEntryIsFresh(entry, path) {
			return nil, false
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return nil, false
		}
		seen[entry.ID] = struct{}{}
		metas = append(metas, entry.Meta)
	}
	if rows.Err() != nil || len(metas) != len(paths) {
		return nil, false
	}
	return metas, true
}

func (s *ThreadStore) pruneSessionCatalog(paths map[string]string) error {
	entries, err := s.sessionCatalogEntries()
	if err != nil || s.db == nil {
		return err
	}
	for id := range entries {
		if _, exists := paths[id]; exists {
			continue
		}
		if _, err := s.db.Exec(`DELETE FROM session_catalog WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}
