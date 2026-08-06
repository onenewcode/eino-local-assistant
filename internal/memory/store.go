package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"eino-local-assistant/internal/usage"

	_ "modernc.org/sqlite" // Register the pure-Go database/sql driver.
)

const (
	dirName             = ".eino"
	memoryDirName       = "memory"
	databaseFile        = "memory.sqlite3"
	MemorySchemaVersion = 1
)

var errResetGenerationChanged = errors.New("memory reset generation changed")

// Store is the authoritative, project-scoped semantic-memory database.
type Store struct {
	root   string
	wsRoot string
	db     *sql.DB
	mu     sync.Mutex
	maxSum int
	now    func() time.Time
}

type Options struct {
	WorkspaceRoot    string
	MaxSummaryTokens int
	UseEnabled       bool
	GenerateEnabled  bool
	Now              func() time.Time
}

func Open(opts Options) (*Store, error) {
	ws, err := filepath.Abs(strings.TrimSpace(opts.WorkspaceRoot))
	if err != nil {
		return nil, fmt.Errorf("memory workspace: %w", err)
	}
	info, err := os.Stat(ws)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("memory workspace %q is not a directory", ws)
	}
	root := filepath.Join(ws, dirName, memoryDirName)
	if err := rejectLegacyMemory(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create memory directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("set memory directory permissions: %w", err)
	}
	if err := ensureMemoryGitignore(ws); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, databaseFile))
	if err != nil {
		return nil, fmt.Errorf("open memory database: %w", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{root: root, wsRoot: ws, db: db, maxSum: opts.MaxSummaryTokens, now: opts.Now}
	if s.maxSum <= 0 {
		s.maxSum = 2500
	}
	if s.now == nil {
		s.now = time.Now
	}
	if err := s.bootstrap(opts.UseEnabled, opts.GenerateEnabled); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(filepath.Join(root, databaseFile), 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set memory database permissions: %w", err)
	}
	return s, nil
}

func rejectLegacyMemory(root string) error {
	for _, name := range []string{"meta.json", "entries.jsonl", "summary.md"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return fmt.Errorf("legacy memory format detected at %s; move or remove it before starting", root)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect legacy memory: %w", err)
		}
	}
	return nil
}

func ensureMemoryGitignore(ws string) error {
	dir := filepath.Join(ws, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, ".gitignore")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.WriteFile(path, []byte("memory/\n"), 0o600)
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "memory/" {
			return os.Chmod(path, 0o600)
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, []byte("memory/\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (s *Store) bootstrap(useOn, genOn bool) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000",
	} {
		if _, err := s.db.Exec(pragma); err != nil {
			return fmt.Errorf("configure memory database: %w", err)
		}
	}
	schema := `
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS memory_settings(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1), workspace_root TEXT NOT NULL,
  use_enabled INTEGER NOT NULL CHECK(use_enabled IN (0,1)), generate_enabled INTEGER NOT NULL CHECK(generate_enabled IN (0,1)),
  reset_generation INTEGER NOT NULL DEFAULT 0, last_consolidate_at TEXT, last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS memory_entries(
  id TEXT PRIMARY KEY, key TEXT NOT NULL, claim TEXT NOT NULL,
  trust TEXT NOT NULL CHECK(trust IN ('user','candidate')),
  status TEXT NOT NULL CHECK(status IN ('active','superseded','deleted')),
  version INTEGER NOT NULL CHECK(version > 0), source_thread_id TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, updated_at TEXT NOT NULL, supersedes TEXT NOT NULL DEFAULT '', extracted_from_id TEXT NOT NULL DEFAULT '',
  UNIQUE(key, version)
);
CREATE UNIQUE INDEX IF NOT EXISTS memory_entries_active_key ON memory_entries(key) WHERE status='active';
CREATE TABLE IF NOT EXISTS memory_entry_sources(
  entry_id TEXT NOT NULL REFERENCES memory_entries(id) ON DELETE CASCADE,
  event_id TEXT NOT NULL, ordinal INTEGER NOT NULL, PRIMARY KEY(entry_id, event_id)
);
CREATE TABLE IF NOT EXISTS memory_extractions(
  thread_id TEXT PRIMARY KEY, source_updated_at TEXT NOT NULL DEFAULT '', generation INTEGER NOT NULL,
  status TEXT NOT NULL CHECK(status IN ('running','succeeded','failed')),
  lease_until TEXT, attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at TEXT,
  last_error TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL
);`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("create memory schema: %w", err)
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,?)`, MemorySchemaVersion, now); err != nil {
		return err
	}
	// Startup configuration intentionally overrides persisted switches.
	_, err := s.db.Exec(`INSERT INTO memory_settings(singleton,workspace_root,use_enabled,generate_enabled)
VALUES(1,?,?,?) ON CONFLICT(singleton) DO UPDATE SET workspace_root=excluded.workspace_root,use_enabled=excluded.use_enabled,generate_enabled=excluded.generate_enabled`,
		s.wsRoot, boolInt(useOn), boolInt(genOn))
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}
func (s *Store) WorkspaceRoot() string {
	if s == nil {
		return ""
	}
	return s.wsRoot
}
func (s *Store) DatabasePath() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.root, databaseFile)
}

func (s *Store) UseEnabled() bool      { return s.settingBool("use_enabled") }
func (s *Store) GenerateEnabled() bool { return s.settingBool("generate_enabled") }

func (s *Store) settingBool(column string) bool {
	if s == nil || s.db == nil {
		return false
	}
	var value int
	if err := s.db.QueryRow("SELECT " + column + " FROM memory_settings WHERE singleton=1").Scan(&value); err != nil {
		return false
	}
	return value != 0
}

func (s *Store) SetUseEnabled(on bool) error {
	_, err := s.db.Exec(`UPDATE memory_settings SET use_enabled=? WHERE singleton=1`, boolInt(on))
	return err
}
func (s *Store) SetGenerateEnabled(on bool) error {
	_, err := s.db.Exec(`UPDATE memory_settings SET generate_enabled=? WHERE singleton=1`, boolInt(on))
	return err
}

func (s *Store) Reset() error {
	return s.transaction(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE memory_settings SET reset_generation=reset_generation+1,last_consolidate_at=NULL,last_error='' WHERE singleton=1`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM memory_entry_sources`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM memory_entries`); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM memory_extractions`)
		return err
	})
}

func (s *Store) AddUser(key, claim string) (Entry, error) {
	return s.add(key, claim, TrustUser, "", nil, nil)
}
func (s *Store) AddCandidate(key, claim, threadID string, ids []string) (Entry, error) {
	return s.add(key, claim, TrustCandidate, threadID, ids, nil)
}
func (s *Store) addCandidateAtGeneration(g uint64, key, claim, threadID string, ids []string) (Entry, error) {
	return s.add(key, claim, TrustCandidate, threadID, ids, &g)
}

func (s *Store) add(key, claim string, trust Trust, threadID string, ids []string, generation *uint64) (Entry, error) {
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return Entry{}, errors.New("memory claim is required")
	}
	key = normalizeKey(key, claim)
	var out Entry
	err := s.transaction(func(tx *sql.Tx) error {
		if err := requireGeneration(tx, generation); err != nil {
			return err
		}
		if trust == TrustCandidate {
			var count int
			if err := tx.QueryRow(`SELECT count(*) FROM memory_entries WHERE key=? AND status='active' AND trust='user'`, key).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("candidate refused: key %q has active user memory", key)
			}
		}
		return insertVersion(tx, &out, key, claim, trust, threadID, ids, s.now().UTC())
	})
	return out, err
}

func insertVersion(tx *sql.Tx, out *Entry, key, claim string, trust Trust, threadID string, ids []string, now time.Time) error {
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM memory_entries WHERE key=?`, key).Scan(&version); err != nil {
		return err
	}
	var supersedes string
	err := tx.QueryRow(`SELECT id FROM memory_entries WHERE key=? AND status='active'`, key).Scan(&supersedes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if supersedes != "" {
		if _, err := tx.Exec(`UPDATE memory_entries SET status='superseded',updated_at=? WHERE id=?`, dbTime(now), supersedes); err != nil {
			return err
		}
	}
	*out = Entry{ID: newID(), Key: key, Claim: claim, Trust: trust, Status: StatusActive, Version: version, SourceThreadID: threadID, SourceEventIDs: append([]string(nil), ids...), CreatedAt: now, UpdatedAt: now, Supersedes: supersedes}
	_, err = tx.Exec(`INSERT INTO memory_entries(id,key,claim,trust,status,version,source_thread_id,created_at,updated_at,supersedes,extracted_from_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, out.ID, out.Key, out.Claim, out.Trust, out.Status, out.Version, out.SourceThreadID, dbTime(out.CreatedAt), dbTime(out.UpdatedAt), out.Supersedes, out.ExtractedFromID)
	if err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.Exec(`INSERT INTO memory_entry_sources(entry_id,event_id,ordinal) VALUES(?,?,?)`, out.ID, id, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpdateUser(idOrKey, claim string) (Entry, error) {
	idOrKey, claim = strings.TrimSpace(idOrKey), strings.TrimSpace(claim)
	if idOrKey == "" {
		return Entry{}, errors.New("id or key is required")
	}
	if claim == "" {
		return Entry{}, errors.New("memory claim is required")
	}
	var out Entry
	err := s.transaction(func(tx *sql.Tx) error {
		var key string
		if err := tx.QueryRow(`SELECT key FROM memory_entries WHERE status='active' AND (id=? OR key=?)`, idOrKey, idOrKey).Scan(&key); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("memory not found: %s", idOrKey)
			}
			return err
		}
		return insertVersion(tx, &out, key, claim, TrustUser, "", nil, s.now().UTC())
	})
	return out, err
}

func (s *Store) Delete(idOrKey string) (Entry, error) {
	idOrKey = strings.TrimSpace(idOrKey)
	if idOrKey == "" {
		return Entry{}, errors.New("id or key is required")
	}
	var out Entry
	err := s.transaction(func(tx *sql.Tx) error {
		e, err := queryOne(tx, `WHERE status='active' AND (id=? OR key=?)`, idOrKey, idOrKey)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("memory not found: %s", idOrKey)
			}
			return err
		}
		e.Status, e.UpdatedAt = StatusDeleted, s.now().UTC()
		if _, err := tx.Exec(`UPDATE memory_entries SET status='deleted',updated_at=? WHERE id=?`, dbTime(e.UpdatedAt), e.ID); err != nil {
			return err
		}
		out = e
		return nil
	})
	return out, err
}

func (s *Store) Accept(id string) (Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Entry{}, errors.New("id is required")
	}
	var out Entry
	err := s.transaction(func(tx *sql.Tx) error {
		e, err := queryOne(tx, `WHERE id=? AND status='active'`, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("active memory not found: %s", id)
			}
			return err
		}
		if e.Trust == TrustUser {
			out = e
			return nil
		}
		e.Trust = TrustUser
		e.UpdatedAt = s.now().UTC()
		_, err = tx.Exec(`UPDATE memory_entries SET trust='user',updated_at=? WHERE id=?`, dbTime(e.UpdatedAt), e.ID)
		out = e
		return err
	})
	return out, err
}

type rowQuery interface {
	QueryRow(query string, args ...any) *sql.Row
}

func queryOne(q rowQuery, where string, args ...any) (Entry, error) {
	var e Entry
	var created, updated string
	err := q.QueryRow(`SELECT id,key,claim,trust,status,version,source_thread_id,created_at,updated_at,supersedes,extracted_from_id FROM memory_entries `+where, args...).Scan(&e.ID, &e.Key, &e.Claim, &e.Trust, &e.Status, &e.Version, &e.SourceThreadID, &created, &updated, &e.Supersedes, &e.ExtractedFromID)
	if err != nil {
		return Entry{}, err
	}
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return e, nil
}

func (s *Store) ListActive() ([]Entry, error) {
	return s.queryEntries(`WHERE status='active' ORDER BY CASE trust WHEN 'user' THEN 0 ELSE 1 END,key,updated_at DESC`)
}
func (s *Store) Get(idOrKey string) (Entry, error) {
	e, err := queryOne(s.db, `WHERE status='active' AND (id=? OR key=?)`, strings.TrimSpace(idOrKey), strings.TrimSpace(idOrKey))
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("memory not found: %s", idOrKey)
	}
	return e, err
}
func (s *Store) Search(query string) ([]Entry, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return s.ListActive()
	}
	all, err := s.ListActive()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0)
	for _, e := range all {
		if strings.Contains(strings.ToLower(e.Key), query) || strings.Contains(strings.ToLower(e.Claim), query) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Store) queryEntries(suffix string, args ...any) ([]Entry, error) {
	rows, err := s.db.Query(`SELECT id,key,claim,trust,status,version,source_thread_id,created_at,updated_at,supersedes,extracted_from_id FROM memory_entries `+suffix, args...)
	if err != nil {
		return nil, err
	}
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var c, u string
		if err := rows.Scan(&e.ID, &e.Key, &e.Claim, &e.Trust, &e.Status, &e.Version, &e.SourceThreadID, &c, &u, &e.Supersedes, &e.ExtractedFromID); err != nil {
			return nil, err
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, c)
		e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, u)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		src, err := s.entrySources(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].SourceEventIDs = src
	}
	return out, nil
}
func (s *Store) entrySources(id string) ([]string, error) {
	rows, err := s.db.Query(`SELECT event_id FROM memory_entry_sources WHERE entry_id=? ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Summary() (SummaryBundle, error) {
	entries, err := s.ListActive()
	if err != nil {
		return SummaryBundle{}, err
	}
	text, truncated := renderSummary(entries, s.maxSum)
	users, cands := 0, 0
	for _, e := range entries {
		if e.Trust == TrustUser {
			users++
		} else {
			cands++
		}
	}
	return SummaryBundle{Text: text, Tokens: usage.EstimateText(text), Truncated: truncated, UserCount: users, CandCount: cands}, nil
}
func renderSummary(entries []Entry, maxTokens int) (string, bool) {
	sortEntries(entries)
	var b strings.Builder
	b.WriteString("# Persistent memory (project-scoped)\n\n")
	users, cands := []Entry{}, []Entry{}
	for _, e := range entries {
		if e.Trust == TrustUser {
			users = append(users, e)
		} else {
			cands = append(cands, e)
		}
	}
	if len(users) > 0 {
		b.WriteString("## Confirmed\n\n")
		for _, e := range users {
			fmt.Fprintf(&b, "- **%s**: %s\n", e.Key, e.Claim)
		}
		b.WriteByte('\n')
	}
	if len(cands) > 0 {
		b.WriteString("## Candidates (unverified - do not treat as ground truth)\n\n")
		for _, e := range cands {
			fmt.Fprintf(&b, "- **%s**: %s _(unverified)_\n", e.Key, e.Claim)
		}
		b.WriteByte('\n')
	}
	if len(entries) == 0 {
		b.WriteString("_No memories stored yet._\n")
	}
	text := b.String()
	if usage.EstimateText(text) <= maxTokens {
		return text, false
	}
	notice := "\n\n...(truncated)\n"
	r := []rune(text)
	lo, hi := 0, len(r)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if usage.EstimateText(string(r[:mid])+notice) <= maxTokens {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(r[:lo]) + notice, true
}

func (s *Store) Report() (StatusReport, error) {
	var rep StatusReport
	var use, gen int
	var last sql.NullString
	if err := s.db.QueryRow(`SELECT use_enabled,generate_enabled,last_consolidate_at,last_error FROM memory_settings WHERE singleton=1`).Scan(&use, &gen, &last, &rep.LastError); err != nil {
		return rep, err
	}
	rep.Root = s.root
	rep.DatabasePath = s.DatabasePath()
	rep.SchemaVersion = MemorySchemaVersion
	rep.UseEnabled = use != 0
	rep.GenerateEnabled = gen != 0
	if last.Valid {
		t, _ := time.Parse(time.RFC3339Nano, last.String)
		rep.LastConsolidate = &t
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM memory_entries WHERE status='active' AND trust='user'`).Scan(&rep.UserActive); err != nil {
		return rep, err
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM memory_entries WHERE status='active' AND trust='candidate'`).Scan(&rep.CandidateActive); err != nil {
		return rep, err
	}
	_ = s.db.QueryRow(`SELECT count(*) FROM memory_extractions WHERE status='running'`).Scan(&rep.RunningExtractions)
	_ = s.db.QueryRow(`SELECT count(*) FROM memory_extractions WHERE status='failed'`).Scan(&rep.FailedExtractions)
	return rep, nil
}

func (s *Store) MarkExtracted(threadID string) error { return s.markExtracted(threadID, nil) }
func (s *Store) markExtractedAtGeneration(g uint64, threadID string) error {
	return s.markExtracted(threadID, &g)
}
func (s *Store) markExtracted(threadID string, g *uint64) error {
	return s.transaction(func(tx *sql.Tx) error {
		if err := requireGeneration(tx, g); err != nil {
			return err
		}
		now := s.now().UTC()
		if _, err := tx.Exec(`UPDATE memory_settings SET last_consolidate_at=?,last_error='' WHERE singleton=1`, dbTime(now)); err != nil {
			return err
		}
		if threadID != "" {
			var generation uint64
			if err := tx.QueryRow(`SELECT reset_generation FROM memory_settings WHERE singleton=1`).Scan(&generation); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO memory_extractions(thread_id,generation,status,updated_at) VALUES(?,?,'succeeded',?) ON CONFLICT(thread_id) DO UPDATE SET generation=excluded.generation,status='succeeded',lease_until=NULL,next_attempt_at=NULL,last_error='',updated_at=excluded.updated_at`, threadID, generation, dbTime(now))
			return err
		}
		return nil
	})
}
func (s *Store) RecordExtractError(err error) error { return s.recordExtractError(err, nil) }
func (s *Store) recordExtractErrorAtGeneration(g uint64, err error) error {
	return s.recordExtractError(err, &g)
}
func (s *Store) recordExtractError(extractErr error, g *uint64) error {
	return s.transaction(func(tx *sql.Tx) error {
		if err := requireGeneration(tx, g); err != nil {
			return err
		}
		msg := ""
		if extractErr != nil {
			msg = extractErr.Error()
		}
		_, err := tx.Exec(`UPDATE memory_settings SET last_consolidate_at=?,last_error=? WHERE singleton=1`, dbTime(s.now().UTC()), msg)
		return err
	})
}
func (s *Store) IsProcessed(threadID string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM memory_extractions WHERE thread_id=? AND status='succeeded'`, threadID).Scan(&n)
	return n > 0, err
}

func (s *Store) claimExtraction(threadID string, sourceUpdatedAt time.Time, generation uint64) (bool, error) {
	now := s.now().UTC()
	claimed := false
	err := s.transaction(func(tx *sql.Tx) error {
		if err := requireGeneration(tx, &generation); err != nil {
			return err
		}
		var status, source string
		var lease, next sql.NullString
		err := tx.QueryRow(`SELECT status,source_updated_at,lease_until,next_attempt_at FROM memory_extractions WHERE thread_id=?`, threadID).Scan(&status, &source, &lease, &next)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		stamp := dbTime(sourceUpdatedAt)
		if err == nil && source == stamp && status == "succeeded" {
			return nil
		}
		if err == nil && source == stamp && status == "running" && lease.Valid {
			until, _ := time.Parse(time.RFC3339Nano, lease.String)
			if until.After(now) {
				return nil
			}
		}
		if err == nil && source == stamp && next.Valid {
			retry, _ := time.Parse(time.RFC3339Nano, next.String)
			if retry.After(now) {
				return nil
			}
		}
		_, err = tx.Exec(`INSERT INTO memory_extractions(thread_id,source_updated_at,generation,status,lease_until,attempts,updated_at) VALUES(?,?,?,'running',?,1,?) ON CONFLICT(thread_id) DO UPDATE SET source_updated_at=excluded.source_updated_at,generation=excluded.generation,status='running',lease_until=excluded.lease_until,attempts=CASE WHEN memory_extractions.source_updated_at=excluded.source_updated_at THEN memory_extractions.attempts+1 ELSE 1 END,next_attempt_at=NULL,last_error='',updated_at=excluded.updated_at`, threadID, stamp, generation, dbTime(now.Add(15*time.Minute)), dbTime(now))
		if err == nil {
			claimed = true
		}
		return err
	})
	return claimed, err
}

func (s *Store) finishExtraction(threadID string, sourceUpdatedAt time.Time, generation uint64, drafts []Draft, extractErr error) (int, error) {
	written := 0
	err := s.transaction(func(tx *sql.Tx) error {
		if err := requireGeneration(tx, &generation); err != nil {
			return err
		}
		var status, source string
		if err := tx.QueryRow(`SELECT status,source_updated_at FROM memory_extractions WHERE thread_id=?`, threadID).Scan(&status, &source); err != nil {
			return err
		}
		if status != "running" || source != dbTime(sourceUpdatedAt) {
			return errors.New("memory extraction lease changed")
		}
		now := s.now().UTC()
		if extractErr != nil {
			var attempts int
			_ = tx.QueryRow(`SELECT attempts FROM memory_extractions WHERE thread_id=?`, threadID).Scan(&attempts)
			delay := 5 * time.Minute
			if attempts > 1 {
				delay <<= min(attempts-1, 6)
			}
			if delay > 6*time.Hour {
				delay = 6 * time.Hour
			}
			_, err := tx.Exec(`UPDATE memory_extractions SET status='failed',lease_until=NULL,next_attempt_at=?,last_error=?,updated_at=? WHERE thread_id=?`, dbTime(now.Add(delay)), extractErr.Error(), dbTime(now), threadID)
			if err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE memory_settings SET last_consolidate_at=?,last_error=? WHERE singleton=1`, dbTime(now), extractErr.Error())
			return err
		}
		for _, d := range drafts {
			claim := strings.TrimSpace(d.Claim)
			if claim == "" {
				continue
			}
			key := normalizeKey(d.Key, claim)
			var users int
			if err := tx.QueryRow(`SELECT count(*) FROM memory_entries WHERE key=? AND status='active' AND trust='user'`, key).Scan(&users); err != nil {
				return err
			}
			if users > 0 {
				continue
			}
			var out Entry
			if err := insertVersion(tx, &out, key, claim, TrustCandidate, threadID, nil, now); err != nil {
				return err
			}
			written++
		}
		if _, err := tx.Exec(`UPDATE memory_extractions SET status='succeeded',lease_until=NULL,next_attempt_at=NULL,last_error='',updated_at=? WHERE thread_id=?`, dbTime(now), threadID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE memory_settings SET last_consolidate_at=?,last_error='' WHERE singleton=1`, dbTime(now))
		return err
	})
	return written, err
}
func (s *Store) resetGeneration() (uint64, error) {
	var g uint64
	err := s.db.QueryRow(`SELECT reset_generation FROM memory_settings WHERE singleton=1`).Scan(&g)
	return g, err
}

func requireGeneration(tx *sql.Tx, g *uint64) error {
	if g == nil {
		return nil
	}
	var current uint64
	if err := tx.QueryRow(`SELECT reset_generation FROM memory_settings WHERE singleton=1`).Scan(&current); err != nil {
		return err
	}
	if current != *g {
		return errResetGenerationChanged
	}
	return nil
}
func (s *Store) transaction(fn func(*sql.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// The following helpers keep package-internal callers source-compatible while
// the storage implementation is SQLite-backed. They do not create legacy
// files and should not be used by production code.
func (s *Store) withFileLock(fn func() error) error { return fn() }

func (s *Store) loadEntriesUnlocked() ([]Entry, error) {
	return s.queryEntries(`ORDER BY created_at,id`)
}

func (s *Store) readMetaUnlocked() (Meta, error) {
	var meta Meta
	var useOn, genOn int
	var last sql.NullString
	err := s.db.QueryRow(`SELECT workspace_root,use_enabled,generate_enabled,reset_generation,last_consolidate_at,last_error FROM memory_settings WHERE singleton=1`).Scan(
		&meta.WorkspaceRoot, &useOn, &genOn, &meta.ResetGeneration, &last, &meta.LastError,
	)
	if err != nil {
		return Meta{}, err
	}
	meta.SchemaVersion = MemorySchemaVersion
	meta.UseEnabled, meta.GenerateEnabled = useOn != 0, genOn != 0
	if last.Valid {
		t, _ := time.Parse(time.RFC3339Nano, last.String)
		meta.LastConsolidate = &t
	}
	rows, err := s.db.Query(`SELECT thread_id,status FROM memory_extractions`)
	if err != nil {
		return Meta{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return Meta{}, err
		}
		if status == "succeeded" {
			meta.ProcessedThreads = append(meta.ProcessedThreads, id)
		} else if status == "running" {
			meta.ClaimedThreads = append(meta.ClaimedThreads, id)
		}
	}
	return meta, rows.Err()
}

func (s *Store) writeMetaUnlocked(meta Meta) error {
	return s.transaction(func(tx *sql.Tx) error {
		var last any
		if meta.LastConsolidate != nil {
			last = dbTime(*meta.LastConsolidate)
		}
		if _, err := tx.Exec(`UPDATE memory_settings SET workspace_root=?,use_enabled=?,generate_enabled=?,reset_generation=?,last_consolidate_at=?,last_error=? WHERE singleton=1`,
			meta.WorkspaceRoot, boolInt(meta.UseEnabled), boolInt(meta.GenerateEnabled), meta.ResetGeneration, last, meta.LastError); err != nil {
			return err
		}
		for _, id := range meta.ClaimedThreads {
			if _, err := tx.Exec(`INSERT INTO memory_extractions(thread_id,generation,status,updated_at) VALUES(?,?,'running',?) ON CONFLICT(thread_id) DO UPDATE SET status='running',updated_at=excluded.updated_at`, id, meta.ResetGeneration, dbTime(s.now().UTC())); err != nil {
				return err
			}
		}
		return nil
	})
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func dbTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func normalizeKey(key, claim string) string {
	key = strings.TrimSpace(key)
	if key != "" {
		return slugify(key)
	}
	return slugify(claim)
}
func slugify(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	var b strings.Builder
	dash := false
	for _, r := range v {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "note"
	}
	if len(out) > 48 {
		return out[:48]
	}
	return out
}
func newID() string { var b [8]byte; _, _ = rand.Read(b[:]); return "mem_" + hex.EncodeToString(b[:]) }
func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool { return lessEntry(entries[i], entries[j]) })
}
func lessEntry(a, b Entry) bool {
	if a.Trust != b.Trust {
		return a.Trust == TrustUser
	}
	if a.Key != b.Key {
		return a.Key < b.Key
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}
