package store

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

type threadCreatedPayload struct {
	Meta         ThreadMeta      `json:"meta"`
	SystemPrompt systemPromptRef `json:"system_prompt"`
}

type systemPromptRef struct {
	Content string `json:"content"`
	SHA256  string `json:"sha256"`
	Bytes   int64  `json:"bytes"`
}

type titleUpdatedPayload struct {
	Title string `json:"title"`
}

type checkpointCommittedPayload struct {
	Checkpoint Checkpoint `json:"checkpoint"`
}

func hasRecordedCompactionOperationID(state ThreadState, operationID string) bool {
	_, exists := state.recordedCompactionOperationIDs[strings.TrimSpace(operationID)]
	return exists
}

func recordCompactionOperationID(state *ThreadState, operationID string) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return
	}
	if state.recordedCompactionOperationIDs == nil {
		state.recordedCompactionOperationIDs = make(map[string]struct{})
	}
	state.recordedCompactionOperationIDs[operationID] = struct{}{}
}

func newThreadEvent(state ThreadState, kind EventKind, threadID, turnID string, expectedRevision uint64, payload any, now time.Time) (ThreadEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ThreadEvent{}, fmt.Errorf("encode %s payload: %w", kind, err)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	event := ThreadEvent{
		Version:          SessionJournalFormatVersion,
		Sequence:         state.HeadSequence + 1,
		ID:               newRandomID("evt"),
		ThreadID:         threadID,
		Timestamp:        now.UTC(),
		Kind:             kind,
		TurnID:           turnID,
		CorrelationID:    turnID,
		ExpectedRevision: expectedRevision,
		Revision:         state.Revision + 1,
		Payload:          raw,
		PayloadHash:      sha256Hex(raw),
		PreviousHash:     state.LastHash,
	}
	event.Hash = threadEventHash(event)
	return event, nil
}

func threadEventHash(event ThreadEvent) string {
	data := strings.Join([]string{
		fmt.Sprintf("%d", event.Version),
		fmt.Sprintf("%d", event.Sequence),
		event.ID,
		event.ThreadID,
		event.Timestamp.UTC().Format(time.RFC3339Nano),
		string(event.Kind),
		event.TurnID,
		event.CorrelationID,
		fmt.Sprintf("%d", event.ExpectedRevision),
		fmt.Sprintf("%d", event.Revision),
		event.PayloadHash,
		event.PreviousHash,
	}, "\n")
	return sha256Hex([]byte(data))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func appendJournalEvent(path string, event ThreadEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode journal event: %w", err)
	}
	data = append(data, '\n')

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open journal for append: %w", err)
	}
	defer f.Close()

	n, err := f.Write(data)
	if err != nil {
		return fmt.Errorf("append journal event: %w", err)
	}
	if n != len(data) {
		return ioShortWrite("append journal event", len(data), n)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync journal event: %w", err)
	}
	return nil
}

func replayJournal(path, id string) (ThreadState, []ThreadEvent, bool, error) {
	return replayJournalWithRepair(path, id, true)
}

func replayJournalReadOnly(path, id string) (ThreadState, []ThreadEvent, bool, error) {
	return replayJournalWithRepair(path, id, false)
}

func replayJournalWithRepair(path, id string, repair bool) (ThreadState, []ThreadEvent, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return ThreadState{}, nil, false, fmt.Errorf("read journal: %w", err)
	}
	defer f.Close()

	events := make([]ThreadEvent, 0)
	reader := bufio.NewReader(f)
	var offset int64
	var validEnd int64
	tornTail := false
	missingFinalNewline := false
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			end := offset + int64(len(line))
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				event, parseErr := decodeThreadEvent(trimmed, id)
				if parseErr != nil {
					if errors.Is(readErr, io.EOF) {
						tornTail = true
						break
					}
					return ThreadState{}, nil, false, fmt.Errorf("%w: journal record at byte %d: %v", ErrJournalCorrupt, validEnd, parseErr)
				}
				events = append(events, event)
			}
			if errors.Is(readErr, io.EOF) {
				missingFinalNewline = true
				validEnd = end
				break
			}
			validEnd = end
			offset = end
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return ThreadState{}, nil, false, fmt.Errorf("read journal: %w", readErr)
		}
	}

	state := ThreadState{FormatVersion: SessionJournalFormatVersion, ID: id}
	lifecycle := newLifecycleTracker()
	for index := range events {
		event := events[index]
		if err := validateThreadEvent(event, state); err != nil {
			return ThreadState{}, nil, false, err
		}
		if err := lifecycle.apply(event); err != nil {
			return ThreadState{}, nil, false, fmt.Errorf("%w: %w", ErrJournalCorrupt, err)
		}
		if err := applyThreadEvent(&state, event); err != nil {
			return ThreadState{}, nil, false, err
		}
	}
	if len(events) == 0 {
		return ThreadState{}, nil, false, fmt.Errorf("%w: thread %q has no creation event", ErrJournalCorrupt, id)
	}
	if err := hydrateSystemPrompt(events[0], &state); err != nil {
		return ThreadState{}, nil, false, err
	}

	if repair && tornTail {
		if err := truncateJournal(path, validEnd); err != nil {
			return ThreadState{}, nil, false, err
		}
	}
	if repair && missingFinalNewline {
		if err := appendJournalNewline(path); err != nil {
			return ThreadState{}, nil, false, err
		}
	}
	return state, events, tornTail, nil
}

func decodeThreadEvent(line []byte, threadID string) (ThreadEvent, error) {
	var event ThreadEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return ThreadEvent{}, err
	}
	// The compact envelope derives these invariant fields instead of writing
	// the same sequence/revision/thread data on every line.
	event.ThreadID = threadID
	event.CorrelationID = event.TurnID
	if event.Sequence > 0 {
		event.Revision = event.Sequence
		event.ExpectedRevision = event.Sequence - 1
	}
	event.PayloadHash = sha256Hex(event.Payload)
	return event, nil
}

func validateThreadEvent(event ThreadEvent, previous ThreadState) error {
	if event.Version != SessionJournalFormatVersion {
		return fmt.Errorf("%w: unsupported event version %d", ErrJournalCorrupt, event.Version)
	}
	if event.Sequence != previous.HeadSequence+1 {
		return fmt.Errorf("%w: sequence %d after %d", ErrJournalCorrupt, event.Sequence, previous.HeadSequence)
	}
	if event.Sequence == 1 && event.Kind != EventThreadCreated {
		return fmt.Errorf("%w: first event must be thread.created", ErrJournalCorrupt)
	}
	if event.Sequence > 1 && event.Kind == EventThreadCreated {
		return fmt.Errorf("%w: duplicate thread.created", ErrJournalCorrupt)
	}
	if event.Revision != previous.Revision+1 || event.Revision != event.Sequence {
		return fmt.Errorf("%w: revision %d at sequence %d", ErrJournalCorrupt, event.Revision, event.Sequence)
	}
	if event.ExpectedRevision != previous.Revision {
		return fmt.Errorf("%w: expected revision %d before %d", ErrJournalCorrupt, event.ExpectedRevision, previous.Revision)
	}
	if event.ThreadID == "" || (previous.ID != "" && event.ThreadID != previous.ID) {
		return fmt.Errorf("%w: invalid thread id at %d", ErrJournalCorrupt, event.Sequence)
	}
	if event.ID == "" || event.Kind == "" || len(event.Payload) == 0 {
		return fmt.Errorf("%w: incomplete event %d", ErrJournalCorrupt, event.Sequence)
	}
	if got := sha256Hex(event.Payload); got != event.PayloadHash {
		return fmt.Errorf("%w: payload hash mismatch at %d", ErrJournalCorrupt, event.Sequence)
	}
	if event.PreviousHash != previous.LastHash {
		return fmt.Errorf("%w: previous hash mismatch at %d", ErrJournalCorrupt, event.Sequence)
	}
	if event.Hash != threadEventHash(event) {
		return fmt.Errorf("%w: event hash mismatch at %d", ErrJournalCorrupt, event.Sequence)
	}
	return nil
}

func applyThreadEvent(state *ThreadState, event ThreadEvent) error {
	if state == nil {
		return errors.New("thread state is required")
	}
	switch event.Kind {
	case EventThreadCreated:
		var payload threadCreatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode thread.created: %w", err)
		}
		if payload.Meta.ID == "" {
			return fmt.Errorf("%w: thread.created missing id", ErrJournalCorrupt)
		}
		state.ID = payload.Meta.ID
		state.Meta = payload.Meta
		state.Meta.ID = state.ID
		// Journals written before usage.recorded have no status in their
		// creation payload. Do not pretend their turn-level estimates are exact.
		if state.Meta.UsageStatus == "" {
			state.Meta.UsageStatus = UsageStatusUnavailable
		}
		if !validUsageStatus(state.Meta.UsageStatus) {
			return fmt.Errorf("%w: invalid usage status %q", ErrJournalCorrupt, state.Meta.UsageStatus)
		}
		if state.Meta.UsageStatus == UsageStatusUnavailable {
			clearUsageProjection(&state.Meta)
		}
		if state.Meta.CreatedAt.IsZero() {
			state.Meta.CreatedAt = event.Timestamp
		}
		state.Meta.CreatedAt = state.Meta.CreatedAt.UTC()
		state.CreatedAt = state.Meta.CreatedAt
		if err := validateSystemPromptRef(payload.SystemPrompt); err != nil {
			return fmt.Errorf("%w: invalid system prompt reference: %v", ErrJournalCorrupt, err)
		}
		// Replay hydrates the frozen system message from this validated creation
		// event after the complete chain has been verified.
		state.Meta.MessageCount = 1
	case EventTurnCommitted:
		var payload TurnCommit
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode turn.committed: %w", err)
		}
		state.Meta.MessageCount += len(payload.Messages)
	case EventUsageRecorded:
		var payload ModelUsage
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode usage.recorded: %w", err)
		}
		usage, err := normalizeModelUsage(payload)
		if err != nil {
			return fmt.Errorf("%w: invalid usage.recorded: %v", ErrJournalCorrupt, err)
		}
		if usage.TurnID != event.TurnID {
			return fmt.Errorf("%w: usage record turn id does not match event", ErrJournalCorrupt)
		}
		if state.recordedUsage == nil {
			state.recordedUsage = make(map[string]ModelUsage)
		}
		if previous, exists := state.recordedUsage[usage.CallID]; exists {
			if previous != usage {
				return fmt.Errorf("%w: duplicate usage call id %q differs", ErrJournalCorrupt, usage.CallID)
			}
			break
		}
		if usage.Operation == UsageOperationCompaction && usage.OperationID != "" {
			if state.PendingCompaction == nil {
				return fmt.Errorf("%w: compaction usage for operation %q requires a pending compaction", ErrJournalCorrupt, usage.OperationID)
			}
			if usage.OperationID != state.PendingCompaction.OperationID {
				return fmt.Errorf("%w: compaction usage does not match pending operation %q", ErrJournalCorrupt, state.PendingCompaction.OperationID)
			}
		}
		state.recordedUsage[usage.CallID] = usage
		state.Meta.ModelCallCount++
		state.Meta.UsageStatus = nextUsageStatus(state.Meta.UsageStatus, usage.HasProviderUsage)
		if usage.HasProviderUsage {
			state.Meta.PromptTokens += usage.PromptTokens
			state.Meta.CompletionTokens += usage.CompletionTokens
			state.Meta.TotalTokens += usage.TotalTokens
			state.Meta.CachedTokens += usage.CachedTokens
			state.Meta.ReasoningTokens += usage.ReasoningTokens
			state.Meta.CostUSD += usage.CostUSD
		}
		if usage.Operation == UsageOperationAgent {
			if usage.HasProviderUsage {
				state.Meta.LastContext = &ContextSnapshot{
					PromptTokens: usage.PromptTokens,
					WindowTokens: usage.ContextWindowTokens,
				}
			} else {
				state.Meta.LastContext = nil
			}
		}
	case EventTaskStateUpdated:
		var payload TaskStateUpdate
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode task state update: %w", err)
		}
		if err := validateTaskStateUpdate(payload); err != nil {
			return fmt.Errorf("%w: invalid task state update: %v", ErrJournalCorrupt, err)
		}
		state.TaskState = append(state.TaskState[:0], payload.Snapshot...)
	case EventTitleChanged:
		var payload titleUpdatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode title update: %w", err)
		}
		state.Meta.Title = payload.Title
	case EventModelChanged:
		var payload ModelChange
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode model change: %w", err)
		}
		state.Meta.Model = strings.TrimSpace(payload.Model)
		state.Meta.ReasoningEffort = strings.TrimSpace(payload.ReasoningEffort)
	case EventContextCompactionStarted:
		var payload CompactionStart
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode compaction start: %w", err)
		}
		payload.OperationID = strings.TrimSpace(payload.OperationID)
		if payload.OperationID == "" {
			return fmt.Errorf("%w: compaction operation id is required", ErrJournalCorrupt)
		}
		if hasRecordedCompactionOperationID(*state, payload.OperationID) {
			return fmt.Errorf("%w: duplicate compaction operation id %q", ErrJournalCorrupt, payload.OperationID)
		}
		if state.PendingCompaction != nil {
			return fmt.Errorf("%w: compaction operation %q is already pending", ErrJournalCorrupt, state.PendingCompaction.OperationID)
		}
		recordCompactionOperationID(state, payload.OperationID)
		state.PendingCompaction = &CompactionOperation{
			OperationID: payload.OperationID,
			Automatic:   payload.Automatic,
			StartedAt:   event.Timestamp.UTC(),
		}
		if payload.Automatic {
			// Prevent another process from retrying a charged-but-unfinished
			// automatic operation after a crash or during concurrent resume.
			state.AutoCompactionPaused = true
			state.AutoCompactionPauseReason = "automatic compaction is in progress"
		}
	case EventContextCompacted:
		var payload checkpointCommittedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode checkpoint: %w", err)
		}
		if err := validateCheckpoint(payload.Checkpoint, state.ID); err != nil {
			return err
		}
		if payload.Checkpoint.Revision != event.Revision || payload.Checkpoint.Sequence != event.Sequence {
			return fmt.Errorf("%w: checkpoint revision mismatch", ErrJournalCorrupt)
		}
		operationID := strings.TrimSpace(payload.Checkpoint.OperationID)
		if state.PendingCompaction != nil {
			if operationID != state.PendingCompaction.OperationID || payload.Checkpoint.Automatic != state.PendingCompaction.Automatic {
				return fmt.Errorf("%w: checkpoint does not match pending compaction operation %q", ErrJournalCorrupt, state.PendingCompaction.OperationID)
			}
			state.PendingCompaction = nil
		} else if operationID != "" {
			return fmt.Errorf("%w: checkpoint for operation %q requires a pending compaction", ErrJournalCorrupt, operationID)
		}
		if state.recordedCheckpointIDs == nil {
			state.recordedCheckpointIDs = make(map[string]struct{})
		}
		if _, exists := state.recordedCheckpointIDs[payload.Checkpoint.ID]; exists {
			return fmt.Errorf("%w: duplicate checkpoint id %q", ErrJournalCorrupt, payload.Checkpoint.ID)
		}
		state.recordedCheckpointIDs[payload.Checkpoint.ID] = struct{}{}
		state.ActiveCheckpointID = payload.Checkpoint.ID
		// A successfully installed checkpoint always clears the low-gain streak.
		// Low-gain candidates are rejected before install; the Checkpoint.LowGain
		// field is historical metadata and does not advance the anti-thrash counter.
		state.LowGainStreak = 0
		state.AutoCompactionPaused = payload.Checkpoint.AutoPaused
		state.AutoCompactionPauseReason = strings.TrimSpace(payload.Checkpoint.AutoPauseReason)
		if state.AutoCompactionPaused && state.AutoCompactionPauseReason == "" {
			// Checkpoints written before pause reasons existed remain replayable.
			state.AutoCompactionPauseReason = "automatic compaction paused by a legacy checkpoint"
		}
		state.LastCompaction = &CompactionOutcome{
			Status:       CompactionOutcomeSucceeded,
			OperationID:  operationID,
			CheckpointID: payload.Checkpoint.ID,
			Automatic:    payload.Checkpoint.Automatic,
			At:           event.Timestamp.UTC(),
		}
		// A compacted transcript no longer has a reliable prompt-size snapshot
		// until the next primary model request reports provider usage.
		state.Meta.LastContext = nil
	case EventContextCompactionFailed:
		var payload CompactionFailure
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode compaction failure: %w", err)
		}
		payload.OperationID = strings.TrimSpace(payload.OperationID)
		payload.Reason = strings.TrimSpace(payload.Reason)
		payload.AutoPauseReason = strings.TrimSpace(payload.AutoPauseReason)
		if !payload.Cancelled && payload.Reason == "" {
			return fmt.Errorf("%w: compaction failure reason is required", ErrJournalCorrupt)
		}
		if payload.AutoPaused && payload.AutoPauseReason == "" {
			return fmt.Errorf("%w: compaction pause reason is required", ErrJournalCorrupt)
		}
		if !payload.AutoPaused && payload.AutoPauseReason != "" {
			return fmt.Errorf("%w: compaction pause reason requires pause", ErrJournalCorrupt)
		}
		if payload.Automatic && !payload.AutoPaused && !allowsUnpausedAutomaticCompactionFailure(payload) {
			return fmt.Errorf("%w: automatic compaction failure must pause", ErrJournalCorrupt)
		}
		if state.PendingCompaction != nil {
			if payload.OperationID != state.PendingCompaction.OperationID || payload.Automatic != state.PendingCompaction.Automatic {
				return fmt.Errorf("%w: compaction failure does not match pending operation %q", ErrJournalCorrupt, state.PendingCompaction.OperationID)
			}
			state.PendingCompaction = nil
		} else if payload.OperationID != "" {
			if hasRecordedCompactionOperationID(*state, payload.OperationID) {
				return fmt.Errorf("%w: duplicate compaction operation id %q", ErrJournalCorrupt, payload.OperationID)
			}
			recordCompactionOperationID(state, payload.OperationID)
		}
		// A failure leaves the active checkpoint and raw transcript untouched.
		// Prefer the absolute streak stamped under the write lock; fall back to
		// reason-based mutation only for pre-absolute-streak journal events.
		switch {
		case payload.ResultingLowGainStreak != nil:
			state.LowGainStreak = *payload.ResultingLowGainStreak
		case payload.Automatic && isLowGainCompactionFailure(payload):
			state.LowGainStreak++
		case payload.Automatic && !isStaleCompactionFailure(payload):
			state.LowGainStreak = 0
		}
		state.AutoCompactionPaused = payload.AutoPaused
		state.AutoCompactionPauseReason = payload.AutoPauseReason
		status := CompactionOutcomeFailed
		if payload.Cancelled {
			status = CompactionOutcomeCancelled
		}
		state.LastCompaction = &CompactionOutcome{
			Status:      status,
			OperationID: payload.OperationID,
			Automatic:   payload.Automatic,
			Reason:      payload.Reason,
			At:          event.Timestamp.UTC(),
		}
	case EventContextCheckpointReset:
		var payload CheckpointSchemaReset
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode checkpoint reset: %w", err)
		}
		payload.OperationID = strings.TrimSpace(payload.OperationID)
		payload.CheckpointID = strings.TrimSpace(payload.CheckpointID)
		payload.Reason = strings.TrimSpace(payload.Reason)
		payload.AutoPauseReason = strings.TrimSpace(payload.AutoPauseReason)
		if payload.CheckpointID == "" || payload.Reason == "" {
			return fmt.Errorf("%w: checkpoint reset id and reason are required", ErrJournalCorrupt)
		}
		if payload.CheckpointID != state.ActiveCheckpointID {
			return fmt.Errorf("%w: checkpoint reset target %q does not match active checkpoint %q", ErrJournalCorrupt, payload.CheckpointID, state.ActiveCheckpointID)
		}
		if payload.AutoPaused && payload.AutoPauseReason == "" {
			return fmt.Errorf("%w: checkpoint reset pause reason is required", ErrJournalCorrupt)
		}
		if !payload.AutoPaused && payload.AutoPauseReason != "" {
			return fmt.Errorf("%w: checkpoint reset pause reason requires pause", ErrJournalCorrupt)
		}
		state.ActiveCheckpointID = ""
		state.LowGainStreak = 0
		state.AutoCompactionPaused = payload.AutoPaused
		state.AutoCompactionPauseReason = payload.AutoPauseReason
		state.LastCompaction = &CompactionOutcome{
			Status:       CompactionOutcomeCheckpointReset,
			OperationID:  payload.OperationID,
			CheckpointID: payload.CheckpointID,
			Reason:       payload.Reason,
			At:           event.Timestamp.UTC(),
		}
		// The checkpoint no longer contributes to the prompt, so the former
		// primary-model context measurement is no longer representative.
		state.Meta.LastContext = nil
	case EventTurnStarted, EventToolStarted, EventToolCompleted, EventTurnCancelled, EventTurnFailed:
		// Lifecycle records do not directly change the materialized projection.
	default:
		return fmt.Errorf("%w: unknown event kind %q", ErrJournalCorrupt, event.Kind)
	}

	state.FormatVersion = SessionJournalFormatVersion
	state.Revision = event.Sequence
	state.HeadSequence = event.Sequence
	state.LastHash = event.Hash
	updatedAt := event.Timestamp.UTC()
	// Caller-supplied metadata can carry a future logical update time. Never
	// regress it merely because this process clock is older.
	if state.Meta.UpdatedAt.After(updatedAt) {
		updatedAt = state.Meta.UpdatedAt
	}
	state.UpdatedAt = updatedAt
	state.Meta.ID = state.ID
	state.Meta.UpdatedAt = state.UpdatedAt
	if state.Meta.CreatedAt.IsZero() {
		state.Meta.CreatedAt = state.CreatedAt
	}
	return nil
}

func validateSystemPromptRef(ref systemPromptRef) error {
	if ref.Bytes <= 0 {
		return errors.New("system prompt size must be positive")
	}
	if len(ref.SHA256) != 64 {
		return errors.New("system prompt sha256 is required")
	}
	if int64(len(ref.Content)) != ref.Bytes {
		return errors.New("system prompt size mismatch")
	}
	if sha256Hex([]byte(ref.Content)) != ref.SHA256 {
		return errors.New("system prompt hash mismatch")
	}
	return nil
}

func hydrateSystemPrompt(created ThreadEvent, state *ThreadState) error {
	if state == nil {
		return errors.New("thread state is required")
	}
	var payload threadCreatedPayload
	if err := json.Unmarshal(created.Payload, &payload); err != nil {
		return fmt.Errorf("decode thread.created for system prompt: %w", err)
	}
	if err := validateSystemPromptRef(payload.SystemPrompt); err != nil {
		return fmt.Errorf("%w: invalid system prompt reference: %v", ErrJournalCorrupt, err)
	}
	state.SystemPrompt = payload.SystemPrompt.Content
	return nil
}

func messagesFromEvents(events []ThreadEvent, systemPrompt string) ([]*schema.Message, error) {
	messages := make([]*schema.Message, 0, 1)
	if systemPrompt != "" {
		messages = append(messages, schema.SystemMessage(systemPrompt))
	}
	for _, event := range events {
		switch event.Kind {
		case EventTurnCommitted:
			var payload TurnCommit
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, err
			}
			messages = append(messages, cloneMessages(payload.Messages)...)
		}
	}
	return messages, nil
}

func messagesPageFromEvents(events []ThreadEvent, before, limit int) ([]*schema.Message, bool, error) {
	page := make([]*schema.Message, 0, limit)
	bodyCount := 0
	foundSystem := false
	for _, event := range events {
		messages, err := eventMessages(event)
		if err != nil {
			return nil, false, err
		}
		for _, message := range messages {
			if message == nil {
				continue
			}
			if !foundSystem && message.Role == schema.System {
				foundSystem = true
				continue
			}
			if bodyCount >= before && len(page) < limit {
				messageCopy := *message
				page = append(page, &messageCopy)
			}
			bodyCount++
		}
	}
	return page, bodyCount > before+len(page), nil
}

func eventMessages(event ThreadEvent) ([]*schema.Message, error) {
	switch event.Kind {
	case EventThreadCreated:
		return nil, nil
	case EventTurnCommitted:
		var payload TurnCommit
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, err
		}
		return payload.Messages, nil
	default:
		return nil, nil
	}
}

func cloneMessages(messages []*schema.Message) []*schema.Message {
	cloned := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		messageCopy := *message
		cloned = append(cloned, &messageCopy)
	}
	return cloned
}

func truncateJournal(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open journal for recovery: %w", err)
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		return fmt.Errorf("truncate torn journal tail: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync recovered journal: %w", err)
	}
	return nil
}

func appendJournalNewline(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open journal to finish record: %w", err)
	}
	defer f.Close()
	if _, err := f.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("finish journal record: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync finished journal record: %w", err)
	}
	return nil
}

func ioShortWrite(operation string, want, got int) error {
	return fmt.Errorf("%s: short write: wrote %d of %d bytes", operation, got, want)
}
