package store

import (
	"context"
	"fmt"
)

// LoadThreadResumeSnapshot always replays and validates canonical JSONL while
// holding the session's exclusive lock. The catalog only locates the journal;
// it never supplies recovery state, messages, checkpoint data, or offsets.
func (s *ThreadStore) LoadThreadResumeSnapshot(ctx context.Context, id string, messageLimit int) (ThreadResumeSnapshot, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	defer unlock()

	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	lineage, err := checkpointLineageFromEvents(events, state.ActiveCheckpointID)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	tail, err := resumeTailEvents(events, state, lineage)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	evidence, err := checkpointEvidenceGroups(events, lineage)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	return resumeSnapshotFromEvents(state, tail, lineage, evidence, messageLimit)
}

func resumeSnapshotFromEvents(state ThreadState, events []ThreadEvent, lineage []Checkpoint, evidence []TurnGroup, messageLimit int) (ThreadResumeSnapshot, error) {
	messages, err := messagesFromEvents(events, state.SystemPrompt)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	groups, err := turnGroupsFromEvents(events)
	if err != nil {
		return ThreadResumeSnapshot{}, err
	}
	return ThreadResumeSnapshot{
		State:              state,
		Transcript:         recentMessages(messages, messageLimit),
		TurnGroups:         groups,
		CheckpointLineage:  lineage,
		CheckpointEvidence: evidence,
	}, nil
}

// checkpointEvidenceGroups retains only the direct source turn groups needed
// to validate the durable checkpoint lineage. It is never part of the model
// prompt, and is discarded after OpenSession completes its verification.
func checkpointEvidenceGroups(events []ThreadEvent, lineage []Checkpoint) ([]TurnGroup, error) {
	if len(lineage) == 0 {
		return nil, nil
	}
	wanted := make(map[string]struct{})
	for _, checkpoint := range lineage {
		for _, eventID := range checkpoint.SourceEventIDs {
			wanted[eventID] = struct{}{}
		}
	}
	groups, err := turnGroupsFromEvents(events)
	if err != nil {
		return nil, err
	}
	evidence := make([]TurnGroup, 0, len(groups))
	for _, group := range groups {
		for _, eventID := range group.SourceEventIDs {
			if _, ok := wanted[eventID]; ok {
				evidence = append(evidence, group)
				break
			}
		}
	}
	return evidence, nil
}

// resumeTailEvents drops precisely the raw prefix superseded by the latest
// checkpoint. Checkpoint events may occur after retained turns, so this uses
// the persisted tail boundary rather than the checkpoint event position.
func resumeTailEvents(events []ThreadEvent, state ThreadState, lineage []Checkpoint) ([]ThreadEvent, error) {
	if len(lineage) == 0 {
		return events, nil
	}
	active := lineage[0]
	tailStart := active.TailStartSequence
	if tailStart == 0 {
		tailStart = active.Sequence + 1
	}
	if tailStart == 0 || tailStart > state.HeadSequence+1 {
		return nil, fmt.Errorf("%w: checkpoint %q has invalid tail start sequence %d", ErrJournalCorrupt, active.ID, active.TailStartSequence)
	}
	if tailStart == state.HeadSequence+1 {
		return nil, nil
	}
	for index, event := range events {
		if event.Sequence != tailStart {
			continue
		}
		return append([]ThreadEvent(nil), events[index:]...), nil
	}
	return nil, fmt.Errorf("%w: checkpoint %q tail start sequence %d is absent", ErrJournalCorrupt, active.ID, tailStart)
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
		if err := validateCheckpoint(checkpoint, checkpoint.ThreadID); err != nil {
			return nil, err
		}
		lineage = append(lineage, checkpoint)
		activeID = checkpoint.ParentID
	}
	return lineage, nil
}
