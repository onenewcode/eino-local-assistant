package store

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // Register the pure-Go database/sql driver.
)

const (
	threadDatabaseFile             = "state.sqlite3"
	SessionProjectionSchemaVersion = 3
	timeFormat                     = "2006-01-02T15:04:05.999999999Z07:00"
)

// checkpointLocator is deliberately metadata-only. The JSONL event remains the
// only durable copy of the checkpoint payload and its source manifest.
type checkpointLocator struct {
	ID                string
	ParentID          string
	Sequence          uint64
	EventHash         string
	Offset            int64
	EndOffset         int64
	TailStartSequence uint64
	TailStartOffset   int64
	SourceEventIDs    []string
}

func (s *ThreadStore) openProjection(readOnly bool) error {
	path := filepath.Join(s.root, threadDatabaseFile)
	if readOnly {
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
			return fmt.Errorf("unsupported session projection schema %d", version)
		}
		s.db = db
		return nil
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open thread database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return fmt.Errorf("configure thread database: %w", err)
		}
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		_ = db.Close()
		return fmt.Errorf("read session projection schema: %w", err)
	}
	if version != 0 && version != SessionProjectionSchemaVersion {
		// This database is only an index. Dropping it is safer than retaining an
		// old event mirror whose semantics no longer match the JSONL ledger.
		for _, table := range []string{"checkpoint_index", "resume_index", "thread_catalog", "schema_migrations", "checkpoint_sources", "checkpoints", "usage_records", "messages", "turns", "events", "threads"} {
			if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
				_ = db.Close()
				return fmt.Errorf("reset session projection: %w", err)
			}
		}
	}
	schema := `
CREATE TABLE IF NOT EXISTS thread_catalog (
  id TEXT PRIMARY KEY,
  journal_relpath TEXT NOT NULL UNIQUE,
  journal_bytes INTEGER NOT NULL,
  journal_mod_ns INTEGER NOT NULL,
  head_sequence INTEGER NOT NULL,
  head_hash TEXT NOT NULL,
  state_json BLOB NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS thread_catalog_updated_at ON thread_catalog(updated_at DESC, id DESC);
CREATE TABLE IF NOT EXISTS resume_index (
  thread_id TEXT PRIMARY KEY REFERENCES thread_catalog(id) ON DELETE CASCADE,
  checkpoint_id TEXT,
  checkpoint_sequence INTEGER,
  checkpoint_hash TEXT,
  checkpoint_offset INTEGER,
  checkpoint_end_offset INTEGER,
	  tail_start_sequence INTEGER,
	  tail_start_offset INTEGER,
  indexed_head_sequence INTEGER NOT NULL,
  indexed_head_hash TEXT NOT NULL,
  indexed_journal_bytes INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS checkpoint_index (
  thread_id TEXT NOT NULL REFERENCES thread_catalog(id) ON DELETE CASCADE,
  checkpoint_id TEXT NOT NULL,
  parent_id TEXT NOT NULL,
  event_sequence INTEGER NOT NULL,
  event_hash TEXT NOT NULL,
  byte_offset INTEGER NOT NULL,
  end_offset INTEGER NOT NULL,
	  tail_start_sequence INTEGER NOT NULL,
	  tail_start_offset INTEGER NOT NULL,
  PRIMARY KEY(thread_id, checkpoint_id)
);
CREATE INDEX IF NOT EXISTS checkpoint_index_latest ON checkpoint_index(thread_id, event_sequence DESC);`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return fmt.Errorf("create thread projection schema: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, SessionProjectionSchemaVersion)); err != nil {
		_ = db.Close()
		return fmt.Errorf("set session projection schema: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return fmt.Errorf("set thread database permissions: %w", err)
	}
	s.db = db
	return nil
}

func (s *ThreadStore) projectEvent(dir string, state ThreadState, event ThreadEvent) error {
	if s.db == nil || s.readOnly {
		return errors.New("thread projection is unavailable")
	}
	if err := s.projectCatalog(dir, state); err != nil {
		return err
	}
	if event.Kind != EventContextCompacted {
		return nil
	}
	locators, err := checkpointLocatorsFromJournal(journalPath(dir, state.ID), state.ID)
	if err != nil {
		return err
	}
	return s.replaceCheckpointIndex(state.ID, locators, state)
}

func (s *ThreadStore) projectThread(dir string, state ThreadState, _ []ThreadEvent) error {
	if s.db == nil || s.readOnly {
		return nil
	}
	if err := s.projectCatalog(dir, state); err != nil {
		return err
	}
	locators, err := checkpointLocatorsFromJournal(journalPath(dir, state.ID), state.ID)
	if err != nil {
		return err
	}
	return s.replaceCheckpointIndex(state.ID, locators, state)
}

func (s *ThreadStore) projectCatalog(dir string, state ThreadState) error {
	info, err := os.Stat(journalPath(dir, state.ID))
	if err != nil {
		return err
	}
	relPath, err := filepath.Rel(s.sessionsRoot(), journalPath(dir, state.ID))
	if err != nil || relPath == "." || filepath.IsAbs(relPath) {
		return errors.New("session journal is outside the sessions root")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO thread_catalog(id,journal_relpath,journal_bytes,journal_mod_ns,head_sequence,head_hash,state_json,updated_at)
VALUES(?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET journal_relpath=excluded.journal_relpath,journal_bytes=excluded.journal_bytes,journal_mod_ns=excluded.journal_mod_ns,head_sequence=excluded.head_sequence,head_hash=excluded.head_hash,state_json=excluded.state_json,updated_at=excluded.updated_at`,
		state.ID, filepath.ToSlash(relPath), info.Size(), info.ModTime().UnixNano(), state.HeadSequence, state.LastHash, raw, state.UpdatedAt.UTC().Format(timeFormat))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE resume_index SET indexed_head_sequence=?,indexed_head_hash=?,indexed_journal_bytes=? WHERE thread_id=?`, state.HeadSequence, state.LastHash, info.Size(), state.ID)
	return err
}

func (s *ThreadStore) replaceCheckpointIndex(threadID string, locators []checkpointLocator, state ThreadState) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM checkpoint_index WHERE thread_id=?`, threadID); err != nil {
		return err
	}
	var active *checkpointLocator
	for i := range locators {
		locator := locators[i]
		if _, err := tx.Exec(`INSERT INTO checkpoint_index(thread_id,checkpoint_id,parent_id,event_sequence,event_hash,byte_offset,end_offset,tail_start_sequence,tail_start_offset) VALUES(?,?,?,?,?,?,?,?,?)`, threadID, locator.ID, locator.ParentID, locator.Sequence, locator.EventHash, locator.Offset, locator.EndOffset, locator.TailStartSequence, locator.TailStartOffset); err != nil {
			return err
		}
		if locator.ID == state.ActiveCheckpointID {
			active = &locator
		}
	}
	if active == nil {
		_, err = tx.Exec(`INSERT INTO resume_index(thread_id,checkpoint_id,checkpoint_sequence,checkpoint_hash,checkpoint_offset,checkpoint_end_offset,tail_start_sequence,tail_start_offset,indexed_head_sequence,indexed_head_hash,indexed_journal_bytes)
VALUES(?,NULL,NULL,NULL,NULL,NULL,NULL,NULL,?,?,?)
ON CONFLICT(thread_id) DO UPDATE SET checkpoint_id=NULL,checkpoint_sequence=NULL,checkpoint_hash=NULL,checkpoint_offset=NULL,checkpoint_end_offset=NULL,tail_start_sequence=NULL,tail_start_offset=NULL,indexed_head_sequence=excluded.indexed_head_sequence,indexed_head_hash=excluded.indexed_head_hash,indexed_journal_bytes=excluded.indexed_journal_bytes`, threadID, state.HeadSequence, state.LastHash, journalSizeForState(s, state))
	} else {
		_, err = tx.Exec(`INSERT INTO resume_index(thread_id,checkpoint_id,checkpoint_sequence,checkpoint_hash,checkpoint_offset,checkpoint_end_offset,tail_start_sequence,tail_start_offset,indexed_head_sequence,indexed_head_hash,indexed_journal_bytes)
VALUES(?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(thread_id) DO UPDATE SET checkpoint_id=excluded.checkpoint_id,checkpoint_sequence=excluded.checkpoint_sequence,checkpoint_hash=excluded.checkpoint_hash,checkpoint_offset=excluded.checkpoint_offset,checkpoint_end_offset=excluded.checkpoint_end_offset,tail_start_sequence=excluded.tail_start_sequence,tail_start_offset=excluded.tail_start_offset,indexed_head_sequence=excluded.indexed_head_sequence,indexed_head_hash=excluded.indexed_head_hash,indexed_journal_bytes=excluded.indexed_journal_bytes`, threadID, active.ID, active.Sequence, active.EventHash, active.Offset, active.EndOffset, active.TailStartSequence, active.TailStartOffset, state.HeadSequence, state.LastHash, journalSizeForState(s, state))
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func journalSizeForState(s *ThreadStore, state ThreadState) int64 {
	dir, err := s.threadDayDir(state.ID)
	if err != nil {
		return 0
	}
	info, err := os.Stat(journalPath(dir, state.ID))
	if err != nil {
		return 0
	}
	return info.Size()
}

func (s *ThreadStore) projectedMetaIfFresh(id string) (ThreadMeta, bool, error) {
	if s.db == nil {
		return ThreadMeta{}, false, nil
	}
	dir, err := s.threadDayDir(id)
	if err != nil {
		return ThreadMeta{}, false, err
	}
	info, err := os.Stat(journalPath(dir, id))
	if err != nil {
		return ThreadMeta{}, false, err
	}
	var size, modNS int64
	var raw []byte
	err = s.db.QueryRow(`SELECT journal_bytes,journal_mod_ns,state_json FROM thread_catalog WHERE id=?`, id).Scan(&size, &modNS, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadMeta{}, false, nil
	}
	if err != nil || size != info.Size() || modNS != info.ModTime().UnixNano() {
		return ThreadMeta{}, false, nil
	}
	var state ThreadState
	if err := json.Unmarshal(raw, &state); err != nil {
		return ThreadMeta{}, false, nil
	}
	return state.Meta, true, nil
}

// loadProjectedThread intentionally never returns events. A previous schema
// treated SQLite as a full event cache, which could resume an incomplete tail
// and later append an invalid sequence to the canonical ledger.
func (s *ThreadStore) loadProjectedThread(_ string, _ string) (ThreadState, []ThreadEvent, bool, error) {
	return ThreadState{}, nil, false, nil
}

func checkpointLocatorsFromJournal(path, threadID string) ([]checkpointLocator, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	locators := make([]checkpointLocator, 0)
	sequenceOffsets := make(map[uint64]int64)
	eventIDs := make(map[string]struct{})
	var offset int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			end := offset + int64(len(line))
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				event, decodeErr := decodeThreadEvent(trimmed, threadID)
				if decodeErr != nil {
					return nil, decodeErr
				}
				if event.Kind == EventContextCompacted {
					var payload checkpointCommittedPayload
					if err := json.Unmarshal(event.Payload, &payload); err != nil {
						return nil, err
					}
					locators = append(locators, checkpointLocator{ID: payload.Checkpoint.ID, ParentID: payload.Checkpoint.ParentID, Sequence: event.Sequence, EventHash: event.Hash, Offset: offset, EndOffset: end, TailStartSequence: payload.Checkpoint.TailStartSequence, SourceEventIDs: append([]string(nil), payload.Checkpoint.SourceEventIDs...)})
				}
				sequenceOffsets[event.Sequence] = offset
				eventIDs[event.ID] = struct{}{}
			}
			offset = end
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	for index := range locators {
		for _, sourceID := range locators[index].SourceEventIDs {
			if _, ok := eventIDs[sourceID]; !ok {
				return nil, fmt.Errorf("checkpoint %q source event %q is not in the journal", locators[index].ID, sourceID)
			}
		}
		if locators[index].TailStartSequence == 0 {
			locators[index].TailStartSequence = locators[index].Sequence + 1
			locators[index].TailStartOffset = locators[index].EndOffset
			continue
		}
		offset, ok := sequenceOffsets[locators[index].TailStartSequence]
		if !ok {
			return nil, fmt.Errorf("checkpoint %q tail start sequence %d not found", locators[index].ID, locators[index].TailStartSequence)
		}
		locators[index].TailStartOffset = offset
	}
	return locators, nil
}
