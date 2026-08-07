package store

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

type indexedResume struct {
	state               ThreadState
	checkpointID        string
	checkpointSequence  uint64
	checkpointHash      string
	checkpointOffset    int64
	tailStartSequence   uint64
	tailStartOffset     int64
	journalBytes        int64
	journalModNS        int64
	indexedHeadSequence uint64
	indexedHeadHash     string
	indexedJournalBytes int64
}

// LoadThreadResumeSnapshot loads the current bounded prompt tail from the
// canonical journal. SQLite only supplies offsets; a stale index always falls
// back to a full canonical replay and is never used as a source of messages.
func (s *ThreadStore) LoadThreadResumeSnapshot(ctx context.Context, id string, messageLimit int) (ThreadResumeSnapshot, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	defer unlock()
	if snapshot, ok, err := s.loadIndexedResumeSnapshot(dir, id, messageLimit); err != nil {
		return ThreadResumeSnapshot{}, err
	} else if ok {
		return snapshot, nil
	}

	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	messages, err := messagesFromEvents(events, state.SystemPrompt)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	groups, err := turnGroupsFromEvents(events)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	return ThreadResumeSnapshot{
		State:      state,
		Transcript: recentMessages(messages, messageLimit),
		TurnGroups: groups,
	}, nil
}

func (s *ThreadStore) loadIndexedResumeSnapshot(dir, id string, messageLimit int) (ThreadResumeSnapshot, bool, error) {
	if s.db == nil {
		return ThreadResumeSnapshot{}, false, nil
	}
	indexed, ok, err := s.loadIndexedResume(id)
	if err != nil || !ok || indexed.checkpointID == "" {
		return ThreadResumeSnapshot{}, false, err
	}
	path := journalPath(dir, id)
	info, err := os.Stat(path)
	if err != nil || info.Size() != indexed.journalBytes || info.ModTime().UnixNano() != indexed.journalModNS {
		return ThreadResumeSnapshot{}, false, nil
	}
	if endsWithNewline, err := journalEndsWithNewline(path); err != nil || !endsWithNewline {
		// The canonical replay path repairs the sole safe case: a valid final
		// record without its terminating newline.
		return ThreadResumeSnapshot{}, false, nil
	}
	head, err := readJournalTailEvent(path, id)
	if err != nil || head.Sequence != indexed.state.HeadSequence || head.Hash != indexed.state.LastHash {
		return ThreadResumeSnapshot{}, false, nil
	}
	checkpointEvent, err := readJournalEventAt(path, id, indexed.checkpointOffset)
	if err != nil || checkpointEvent.Kind != EventContextCompacted || checkpointEvent.Sequence != indexed.checkpointSequence || checkpointEvent.Hash != indexed.checkpointHash {
		return ThreadResumeSnapshot{}, false, nil
	}
	var checkpointPayload checkpointCommittedPayload
	if err := json.Unmarshal(checkpointEvent.Payload, &checkpointPayload); err != nil || checkpointPayload.Checkpoint.ID != indexed.checkpointID {
		return ThreadResumeSnapshot{}, false, nil
	}
	lineage, err := s.loadIndexedCheckpointLineage(path, id, checkpointPayload.Checkpoint)
	if err != nil {
		return ThreadResumeSnapshot{}, false, nil
	}
	events, err := readJournalEventsFrom(path, id, indexed.tailStartOffset)
	if err != nil || !tailReachesHead(events, indexed.state, indexed.tailStartSequence) {
		return ThreadResumeSnapshot{}, false, nil
	}
	groups, err := turnGroupsFromEvents(events)
	if err != nil {
		return ThreadResumeSnapshot{}, false, nil
	}
	messages, err := messagesFromEvents(events, indexed.state.SystemPrompt)
	if err != nil {
		return ThreadResumeSnapshot{}, false, nil
	}
	return ThreadResumeSnapshot{
		State:             indexed.state,
		Transcript:        recentMessages(messages, messageLimit),
		TurnGroups:        groups,
		CheckpointLineage: lineage,
	}, true, nil
}

func (s *ThreadStore) loadIndexedResume(id string) (indexedResume, bool, error) {
	var indexed indexedResume
	var raw []byte
	err := s.db.QueryRow(`SELECT c.state_json,c.journal_bytes,c.journal_mod_ns,r.checkpoint_id,r.checkpoint_sequence,r.checkpoint_hash,r.checkpoint_offset,r.tail_start_sequence,r.tail_start_offset,r.indexed_head_sequence,r.indexed_head_hash,r.indexed_journal_bytes
FROM thread_catalog c JOIN resume_index r ON r.thread_id=c.id WHERE c.id=?`, id).Scan(
		&raw, &indexed.journalBytes, &indexed.journalModNS, &indexed.checkpointID, &indexed.checkpointSequence, &indexed.checkpointHash, &indexed.checkpointOffset, &indexed.tailStartSequence, &indexed.tailStartOffset, &indexed.indexedHeadSequence, &indexed.indexedHeadHash, &indexed.indexedJournalBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return indexedResume{}, false, nil
	}
	if err != nil || json.Unmarshal(raw, &indexed.state) != nil || indexed.state.ID != id || indexed.checkpointID != indexed.state.ActiveCheckpointID || indexed.indexedHeadSequence != indexed.state.HeadSequence || indexed.indexedHeadHash != indexed.state.LastHash || indexed.indexedJournalBytes != indexed.journalBytes {
		return indexedResume{}, false, nil
	}
	return indexed, true, nil
}

func (s *ThreadStore) loadIndexedCheckpointLineage(path, threadID string, active Checkpoint) ([]Checkpoint, error) {
	lineage := []Checkpoint{active}
	seen := map[string]struct{}{active.ID: {}}
	current := active
	for current.ParentID != "" {
		if _, exists := seen[current.ParentID]; exists {
			return nil, fmt.Errorf("checkpoint lineage cycle at %q", current.ParentID)
		}
		seen[current.ParentID] = struct{}{}
		var sequence uint64
		var eventHash string
		var offset int64
		var parentID string
		err := s.db.QueryRow(`SELECT parent_id,event_sequence,event_hash,byte_offset FROM checkpoint_index WHERE thread_id=? AND checkpoint_id=?`, threadID, current.ParentID).Scan(&parentID, &sequence, &eventHash, &offset)
		if err != nil {
			return nil, err
		}
		event, err := readJournalEventAt(path, threadID, offset)
		if err != nil || event.Kind != EventContextCompacted || event.Sequence != sequence || event.Hash != eventHash {
			return nil, errors.New("checkpoint locator does not match journal")
		}
		var payload checkpointCommittedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, err
		}
		if payload.Checkpoint.ID != current.ParentID || payload.Checkpoint.ParentID != parentID || validateCheckpoint(payload.Checkpoint, threadID) != nil {
			return nil, errors.New("checkpoint payload does not match index")
		}
		lineage = append(lineage, payload.Checkpoint)
		current = payload.Checkpoint
	}
	if err := validateCheckpoint(active, threadID); err != nil {
		return nil, err
	}
	return lineage, nil
}

func checkpointLineageFromEvents(events []ThreadEvent, activeID string) ([]Checkpoint, error) {
	if activeID == "" {
		return nil, nil
	}
	lineage := make([]Checkpoint, 0)
	seen := make(map[string]struct{})
	for activeID != "" {
		if _, exists := seen[activeID]; exists {
			return nil, fmt.Errorf("%w: checkpoint lineage cycle at %q", ErrJournalCorrupt, activeID)
		}
		seen[activeID] = struct{}{}
		checkpoint, err := checkpointFromJournal(events, activeID)
		if err != nil {
			return nil, err
		}
		lineage = append(lineage, checkpoint)
		activeID = checkpoint.ParentID
	}
	return lineage, nil
}

func tailReachesHead(events []ThreadEvent, state ThreadState, firstSequence uint64) bool {
	if len(events) == 0 {
		return state.HeadSequence+1 == firstSequence
	}
	if events[0].Sequence != firstSequence {
		return false
	}
	for i := 1; i < len(events); i++ {
		if events[i].Sequence != events[i-1].Sequence+1 || events[i].PreviousHash != events[i-1].Hash {
			return false
		}
	}
	last := events[len(events)-1]
	return last.Sequence == state.HeadSequence && last.Hash == state.LastHash
}

func readJournalTailEvent(path, threadID string) (ThreadEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return ThreadEvent{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ThreadEvent{}, errors.New("empty journal")
	}
	const blockSize int64 = 64 << 10
	var suffix []byte
	for end := info.Size(); end > 0; {
		start := end - blockSize
		if start < 0 {
			start = 0
		}
		block := make([]byte, end-start)
		if _, err := f.ReadAt(block, start); err != nil {
			return ThreadEvent{}, err
		}
		data := append(block, suffix...)
		trimEnd := len(data)
		for trimEnd > 0 && (data[trimEnd-1] == '\n' || data[trimEnd-1] == '\r') {
			trimEnd--
		}
		if boundary := bytes.LastIndexByte(data[:trimEnd], '\n'); boundary >= 0 {
			return decodeThreadEvent(bytes.TrimSpace(data[boundary+1:trimEnd]), threadID)
		}
		if start == 0 {
			return decodeThreadEvent(bytes.TrimSpace(data[:trimEnd]), threadID)
		}
		suffix = data
		end = start
	}
	return ThreadEvent{}, errors.New("empty journal")
}

func journalEndsWithNewline(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return false, err
	}
	var last [1]byte
	if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
		return false, err
	}
	return last[0] == '\n', nil
}

func readJournalEventAt(path, threadID string, offset int64) (ThreadEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return ThreadEvent{}, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ThreadEvent{}, err
	}
	line, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return ThreadEvent{}, err
	}
	return decodeThreadEvent(bytes.TrimSpace(line), threadID)
}

func readJournalEventsFrom(path, threadID string, offset int64) ([]ThreadEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(f)
	events := make([]ThreadEvent, 0)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			event, err := decodeThreadEvent(bytes.TrimSpace(line), threadID)
			if err != nil {
				return nil, err
			}
			events = append(events, event)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return events, nil
}
