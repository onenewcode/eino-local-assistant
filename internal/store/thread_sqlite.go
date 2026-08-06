package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // Register the pure-Go database/sql driver.
)

const (
	threadDatabaseFile             = "state.sqlite3"
	SessionProjectionSchemaVersion = 1
)

// sessionSummary is the small, atomically replaced per-session projection.
// The JSONL ledger remains authoritative; this file gives directory scans a
// stable metadata entry without replaying a full conversation.
type sessionSummary struct {
	FormatVersion      int        `json:"format_version"`
	ID                 string     `json:"id"`
	Revision           uint64     `json:"revision"`
	HeadSequence       uint64     `json:"head_sequence"`
	ActiveCheckpointID string     `json:"active_checkpoint_id,omitempty"`
	UpdatedAt          string     `json:"updated_at"`
	Meta               ThreadMeta `json:"meta"`
}

func writeSessionSummary(dir string, state ThreadState) error {
	summary := sessionSummary{
		FormatVersion:      SessionJournalFormatVersion,
		ID:                 state.ID,
		Revision:           state.Revision,
		HeadSequence:       state.HeadSequence,
		ActiveCheckpointID: state.ActiveCheckpointID,
		UpdatedAt:          state.UpdatedAt.UTC().Format(timeFormat),
		Meta:               state.Meta,
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	return writeBytesAtomic(filepath.Join(dir, summaryFileName), raw)
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
	schema := `
CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS threads(id TEXT PRIMARY KEY, revision INTEGER NOT NULL, head_sequence INTEGER NOT NULL, head_hash TEXT NOT NULL, journal_bytes INTEGER NOT NULL, journal_mod_ns INTEGER NOT NULL, state_json BLOB NOT NULL, meta_json BLOB NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, title TEXT NOT NULL, message_count INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS events(thread_id TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE, sequence INTEGER NOT NULL, event_id TEXT NOT NULL, kind TEXT NOT NULL, turn_id TEXT NOT NULL, timestamp TEXT NOT NULL, previous_hash TEXT NOT NULL, hash TEXT NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(thread_id,sequence), UNIQUE(event_id));
CREATE TABLE IF NOT EXISTS turns(thread_id TEXT NOT NULL, turn_id TEXT NOT NULL, started_sequence INTEGER, terminal_sequence INTEGER, status TEXT NOT NULL, PRIMARY KEY(thread_id,turn_id), FOREIGN KEY(thread_id) REFERENCES threads(id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS messages(thread_id TEXT NOT NULL, turn_id TEXT NOT NULL, event_sequence INTEGER NOT NULL, ordinal INTEGER NOT NULL, role TEXT NOT NULL, message_json BLOB NOT NULL, PRIMARY KEY(thread_id,event_sequence,ordinal), FOREIGN KEY(thread_id) REFERENCES threads(id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS usage_records(thread_id TEXT NOT NULL, call_id TEXT NOT NULL, operation TEXT NOT NULL, operation_id TEXT NOT NULL, event_sequence INTEGER NOT NULL, usage_json BLOB NOT NULL, PRIMARY KEY(thread_id,call_id), FOREIGN KEY(thread_id) REFERENCES threads(id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS checkpoints(thread_id TEXT NOT NULL, checkpoint_id TEXT NOT NULL, event_sequence INTEGER NOT NULL, checkpoint_json BLOB NOT NULL, PRIMARY KEY(thread_id,checkpoint_id), FOREIGN KEY(thread_id) REFERENCES threads(id) ON DELETE CASCADE);
CREATE TABLE IF NOT EXISTS checkpoint_sources(thread_id TEXT NOT NULL, checkpoint_id TEXT NOT NULL, source_event_id TEXT NOT NULL, ordinal INTEGER NOT NULL, PRIMARY KEY(thread_id,checkpoint_id,source_event_id), FOREIGN KEY(thread_id,checkpoint_id) REFERENCES checkpoints(thread_id,checkpoint_id) ON DELETE CASCADE);`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return fmt.Errorf("create thread schema: %w", err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO schema_migrations(version) VALUES(?)`, SessionProjectionSchemaVersion); err != nil {
		_ = db.Close()
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return fmt.Errorf("set thread database permissions: %w", err)
	}
	s.db = db
	return nil
}

func (s *ThreadStore) projectEvent(dir string, state ThreadState, event ThreadEvent) error {
	if err := writeSessionSummary(dir, state); err != nil {
		return err
	}
	if s.db == nil || s.readOnly {
		return errors.New("thread projection is read-only")
	}
	var headSequence uint64
	var headHash string
	if err := s.db.QueryRow(`SELECT head_sequence,head_hash FROM threads WHERE id=?`, state.ID).Scan(&headSequence, &headHash); err != nil || headSequence+1 != event.Sequence || headHash != event.PreviousHash {
		_, events, _, replayErr := replayJournalReadOnly(journalPath(dir, state.ID), state.ID)
		if replayErr != nil {
			return replayErr
		}
		return s.projectThread(dir, state, events)
	}
	info, err := os.Stat(journalPath(dir, state.ID))
	if err != nil {
		return err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	metaJSON, err := json.Marshal(state.Meta)
	if err != nil {
		return err
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE threads SET revision=?,head_sequence=?,head_hash=?,journal_bytes=?,journal_mod_ns=?,state_json=?,meta_json=?,updated_at=?,title=?,message_count=? WHERE id=? AND head_sequence=? AND head_hash=?`, state.Revision, state.HeadSequence, state.LastHash, info.Size(), info.ModTime().UnixNano(), stateJSON, metaJSON, state.UpdatedAt.UTC().Format(timeFormat), state.Meta.Title, state.Meta.MessageCount, state.ID, headSequence, headHash)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("thread projection head changed")
	}
	if _, err = tx.Exec(`INSERT INTO events(thread_id,sequence,event_id,kind,turn_id,timestamp,previous_hash,hash,payload) VALUES(?,?,?,?,?,?,?,?,?)`, state.ID, event.Sequence, event.ID, event.Kind, event.TurnID, event.Timestamp.UTC().Format(timeFormat), event.PreviousHash, event.Hash, eventJSON); err != nil {
		return err
	}
	if err := projectEventDetails(tx, state.ID, event); err != nil {
		return err
	}
	return tx.Commit()
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func projectEventDetails(tx *sql.Tx, threadID string, event ThreadEvent) error {
	switch event.Kind {
	case EventTurnStarted:
		_, err := tx.Exec(`INSERT INTO turns(thread_id,turn_id,started_sequence,status) VALUES(?,?,?,'running') ON CONFLICT(thread_id,turn_id) DO UPDATE SET started_sequence=excluded.started_sequence,status='running',terminal_sequence=NULL`, threadID, event.TurnID, event.Sequence)
		return err
	case EventTurnCommitted, EventTurnCancelled, EventTurnFailed:
		status := "committed"
		if event.Kind == EventTurnCancelled {
			status = "cancelled"
		} else if event.Kind == EventTurnFailed {
			status = "failed"
		}
		if _, err := tx.Exec(`UPDATE turns SET terminal_sequence=?,status=? WHERE thread_id=? AND turn_id=?`, event.Sequence, status, threadID, event.TurnID); err != nil {
			return err
		}
		if event.Kind == EventTurnCommitted {
			var payload TurnCommit
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return err
			}
			for i, message := range payload.Messages {
				raw, err := json.Marshal(message)
				if err != nil {
					return err
				}
				role := ""
				if message != nil {
					role = string(message.Role)
				}
				if _, err = tx.Exec(`INSERT INTO messages(thread_id,turn_id,event_sequence,ordinal,role,message_json) VALUES(?,?,?,?,?,?)`, threadID, event.TurnID, event.Sequence, i, role, raw); err != nil {
					return err
				}
			}
		}
	case EventUsageRecorded:
		var usage ModelUsage
		if err := json.Unmarshal(event.Payload, &usage); err != nil {
			return err
		}
		raw, err := json.Marshal(usage)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT OR IGNORE INTO usage_records(thread_id,call_id,operation,operation_id,event_sequence,usage_json) VALUES(?,?,?,?,?,?)`, threadID, usage.CallID, usage.Operation, usage.OperationID, event.Sequence, raw)
		return err
	case EventContextCompacted:
		var payload checkpointCommittedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return err
		}
		raw, err := json.Marshal(payload.Checkpoint)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO checkpoints(thread_id,checkpoint_id,event_sequence,checkpoint_json) VALUES(?,?,?,?)`, threadID, payload.Checkpoint.ID, event.Sequence, raw); err != nil {
			return err
		}
		for i, source := range payload.Checkpoint.SourceEventIDs {
			if _, err = tx.Exec(`INSERT INTO checkpoint_sources(thread_id,checkpoint_id,source_event_id,ordinal) VALUES(?,?,?,?)`, threadID, payload.Checkpoint.ID, source, i); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *ThreadStore) projectThread(dir string, state ThreadState, events []ThreadEvent) error {
	if err := writeSessionSummary(dir, state); err != nil {
		return err
	}
	if s.db == nil || s.readOnly {
		return nil
	}
	info, err := os.Stat(journalPath(dir, state.ID))
	if err != nil {
		return err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return err
	}
	metaJSON, err := json.Marshal(state.Meta)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.Exec(`INSERT INTO threads(id,revision,head_sequence,head_hash,journal_bytes,journal_mod_ns,state_json,meta_json,created_at,updated_at,title,message_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,head_sequence=excluded.head_sequence,head_hash=excluded.head_hash,journal_bytes=excluded.journal_bytes,journal_mod_ns=excluded.journal_mod_ns,state_json=excluded.state_json,meta_json=excluded.meta_json,updated_at=excluded.updated_at,title=excluded.title,message_count=excluded.message_count`, state.ID, state.Revision, state.HeadSequence, state.LastHash, info.Size(), info.ModTime().UnixNano(), stateJSON, metaJSON, state.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), state.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), state.Meta.Title, state.Meta.MessageCount); err != nil {
		return err
	}
	for _, table := range []string{"events", "turns", "messages", "usage_records", "checkpoint_sources", "checkpoints"} {
		if _, err = tx.Exec(`DELETE FROM `+table+` WHERE thread_id=?`, state.ID); err != nil {
			return err
		}
	}
	turnStatus := map[string]string{}
	turnStart := map[string]uint64{}
	for _, e := range events {
		raw, _ := json.Marshal(e)
		if _, err = tx.Exec(`INSERT INTO events(thread_id,sequence,event_id,kind,turn_id,timestamp,previous_hash,hash,payload) VALUES(?,?,?,?,?,?,?,?,?)`, state.ID, e.Sequence, e.ID, e.Kind, e.TurnID, e.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), e.PreviousHash, e.Hash, raw); err != nil {
			return err
		}
		switch e.Kind {
		case EventTurnStarted:
			turnStatus[e.TurnID] = "running"
			turnStart[e.TurnID] = e.Sequence
		case EventTurnCommitted:
			turnStatus[e.TurnID] = "committed"
			var p TurnCommit
			if json.Unmarshal(e.Payload, &p) == nil {
				for i, m := range p.Messages {
					mj, _ := json.Marshal(m)
					role := ""
					if m != nil {
						role = string(m.Role)
					}
					if _, err = tx.Exec(`INSERT INTO messages(thread_id,turn_id,event_sequence,ordinal,role,message_json) VALUES(?,?,?,?,?,?)`, state.ID, e.TurnID, e.Sequence, i, role, mj); err != nil {
						return err
					}
				}
			}
		case EventTurnCancelled:
			turnStatus[e.TurnID] = "cancelled"
		case EventTurnFailed:
			turnStatus[e.TurnID] = "failed"
		case EventUsageRecorded:
			var u ModelUsage
			if json.Unmarshal(e.Payload, &u) == nil {
				uj, _ := json.Marshal(u)
				if _, err = tx.Exec(`INSERT OR IGNORE INTO usage_records(thread_id,call_id,operation,operation_id,event_sequence,usage_json) VALUES(?,?,?,?,?,?)`, state.ID, u.CallID, u.Operation, u.OperationID, e.Sequence, uj); err != nil {
					return err
				}
			}
		case EventContextCompacted:
			var p checkpointCommittedPayload
			if json.Unmarshal(e.Payload, &p) == nil {
				cj, _ := json.Marshal(p.Checkpoint)
				if _, err = tx.Exec(`INSERT INTO checkpoints(thread_id,checkpoint_id,event_sequence,checkpoint_json) VALUES(?,?,?,?)`, state.ID, p.Checkpoint.ID, e.Sequence, cj); err != nil {
					return err
				}
				for i, src := range p.Checkpoint.SourceEventIDs {
					if _, err = tx.Exec(`INSERT INTO checkpoint_sources(thread_id,checkpoint_id,source_event_id,ordinal) VALUES(?,?,?,?)`, state.ID, p.Checkpoint.ID, src, i); err != nil {
						return err
					}
				}
			}
		}
	}
	for id, status := range turnStatus {
		var terminal any
		if status != "running" {
			for i := len(events) - 1; i >= 0; i-- {
				if events[i].TurnID == id {
					terminal = events[i].Sequence
					break
				}
			}
		}
		if _, err = tx.Exec(`INSERT INTO turns(thread_id,turn_id,started_sequence,terminal_sequence,status) VALUES(?,?,?,?,?)`, state.ID, id, turnStart[id], terminal, status); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *ThreadStore) projectedMetaIfFresh(id string) (ThreadMeta, bool, error) {
	if s.db == nil {
		return ThreadMeta{}, false, nil
	}
	dir, err := s.threadDir(id)
	if err != nil {
		return ThreadMeta{}, false, err
	}
	info, err := os.Stat(journalPath(dir, id))
	if err != nil {
		return ThreadMeta{}, false, err
	}
	var size, modNS int64
	var raw []byte
	err = s.db.QueryRow(`SELECT journal_bytes,journal_mod_ns,meta_json FROM threads WHERE id=?`, id).Scan(&size, &modNS, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ThreadMeta{}, false, nil
	}
	if err != nil {
		return ThreadMeta{}, false, err
	}
	if size != info.Size() || modNS != info.ModTime().UnixNano() {
		return ThreadMeta{}, false, nil
	}
	var meta ThreadMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return ThreadMeta{}, false, nil
	}
	return meta, true, nil
}

func (s *ThreadStore) loadProjectedThread(dir, id string) (ThreadState, []ThreadEvent, bool, error) {
	if s.db == nil {
		return ThreadState{}, nil, false, nil
	}
	info, err := os.Stat(journalPath(dir, id))
	if err != nil {
		return ThreadState{}, nil, false, err
	}
	var size, modNS int64
	if err := s.db.QueryRow(`SELECT journal_bytes,journal_mod_ns FROM threads WHERE id=?`, id).Scan(&size, &modNS); errors.Is(err, sql.ErrNoRows) {
		return ThreadState{}, nil, false, nil
	} else if err != nil {
		return ThreadState{}, nil, false, nil
	}
	if size != info.Size() || modNS != info.ModTime().UnixNano() {
		return ThreadState{}, nil, false, nil
	}
	rows, err := s.db.Query(`SELECT payload FROM events WHERE thread_id=? ORDER BY sequence`, id)
	if err != nil {
		return ThreadState{}, nil, false, nil
	}
	defer rows.Close()
	events := make([]ThreadEvent, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return ThreadState{}, nil, false, err
		}
		var event ThreadEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			return ThreadState{}, nil, false, nil
		}
		event.ThreadID = id
		event.CorrelationID = event.TurnID
		event.Revision = event.Sequence
		event.ExpectedRevision = event.Sequence - 1
		event.PayloadHash = sha256Hex(event.Payload)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return ThreadState{}, nil, false, err
	}
	state := ThreadState{FormatVersion: SessionJournalFormatVersion, ID: id}
	lifecycle := newLifecycleTracker()
	for _, event := range events {
		if err := validateThreadEvent(event, state); err != nil {
			return ThreadState{}, nil, false, nil
		}
		if err := lifecycle.apply(event); err != nil {
			return ThreadState{}, nil, false, nil
		}
		if err := applyThreadEvent(&state, event); err != nil {
			return ThreadState{}, nil, false, nil
		}
	}
	if len(events) == 0 {
		return ThreadState{}, nil, false, nil
	}
	if err := hydrateSystemPrompt(events[0], &state); err != nil {
		return ThreadState{}, nil, false, err
	}
	return state, events, true, nil
}
