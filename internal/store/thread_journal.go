package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

type threadCreatedPayload struct {
	Meta         ThreadMeta        `json:"meta"`
	SystemPrompt string            `json:"system_prompt,omitempty"`
	Messages     []*schema.Message `json:"messages,omitempty"`
}

type titleUpdatedPayload struct {
	Title string `json:"title"`
}

type checkpointCommittedPayload struct {
	Checkpoint Checkpoint `json:"checkpoint"`
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
		Version:          ThreadFormatVersion,
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
	data, err := os.ReadFile(path)
	if err != nil {
		return ThreadState{}, nil, false, fmt.Errorf("read journal: %w", err)
	}

	events := make([]ThreadEvent, 0)
	offset := 0
	validEnd := 0
	tornTail := false
	missingFinalNewline := false
	for offset < len(data) {
		relEnd := bytes.IndexByte(data[offset:], '\n')
		if relEnd < 0 {
			line := bytes.TrimSpace(data[offset:])
			if len(line) == 0 {
				break
			}
			event, parseErr := decodeThreadEvent(line)
			if parseErr != nil {
				tornTail = true
				break
			}
			events = append(events, event)
			validEnd = len(data)
			missingFinalNewline = true
			break
		}

		end := offset + relEnd
		line := bytes.TrimSpace(data[offset:end])
		offset = end + 1
		if len(line) == 0 {
			validEnd = offset
			continue
		}
		event, parseErr := decodeThreadEvent(line)
		if parseErr != nil {
			return ThreadState{}, nil, false, fmt.Errorf("%w: journal record at byte %d: %v", ErrJournalCorrupt, validEnd, parseErr)
		}
		events = append(events, event)
		validEnd = offset
	}

	state := ThreadState{FormatVersion: ThreadFormatVersion, ID: id}
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

	if tornTail {
		if err := truncateJournal(path, int64(validEnd)); err != nil {
			return ThreadState{}, nil, false, err
		}
	}
	if missingFinalNewline {
		if err := appendJournalNewline(path); err != nil {
			return ThreadState{}, nil, false, err
		}
	}
	return state, events, tornTail, nil
}

func decodeThreadEvent(line []byte) (ThreadEvent, error) {
	var event ThreadEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return ThreadEvent{}, err
	}
	return event, nil
}

func validateThreadEvent(event ThreadEvent, previous ThreadState) error {
	if event.Version != ThreadFormatVersion {
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
		if state.Meta.CreatedAt.IsZero() {
			state.Meta.CreatedAt = event.Timestamp
		}
		state.Meta.CreatedAt = state.Meta.CreatedAt.UTC()
		state.CreatedAt = state.Meta.CreatedAt
		state.SystemPrompt = payload.SystemPrompt
		if state.SystemPrompt == "" {
			state.SystemPrompt = firstSystemPrompt(payload.Messages)
		}
		state.Meta.MessageCount = len(payload.Messages)
	case EventTurnCommitted:
		var payload TurnCommit
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode turn.committed: %w", err)
		}
		state.Meta.MessageCount += len(payload.Messages)
		state.Meta.PromptTokens += payload.Usage.PromptTokens
		state.Meta.CompletionTokens += payload.Usage.CompletionTokens
		state.Meta.TotalTokens += payload.Usage.TotalTokens
		state.Meta.CostUSD += payload.Usage.CostUSD
		state.Meta.UsageEstimated = state.Meta.UsageEstimated || payload.Usage.Estimated
	case EventTitleChanged:
		var payload titleUpdatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return fmt.Errorf("decode title update: %w", err)
		}
		state.Meta.Title = payload.Title
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
		state.ActiveCheckpointID = payload.Checkpoint.ID
		if payload.Checkpoint.LowGain {
			state.LowGainStreak++
		} else {
			state.LowGainStreak = 0
		}
		state.AutoCompactionPaused = payload.Checkpoint.AutoPaused
	case EventTurnStarted, EventToolStarted, EventToolCompleted, EventTurnCancelled, EventTurnFailed:
		// Lifecycle records do not directly change the materialized projection.
	default:
		return fmt.Errorf("%w: unknown event kind %q", ErrJournalCorrupt, event.Kind)
	}

	state.FormatVersion = ThreadFormatVersion
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

func firstSystemPrompt(messages []*schema.Message) string {
	for _, message := range messages {
		if message != nil && message.Role == schema.System {
			return message.Content
		}
	}
	return ""
}

func messagesFromEvents(events []ThreadEvent) ([]*schema.Message, error) {
	var messages []*schema.Message
	for _, event := range events {
		switch event.Kind {
		case EventThreadCreated:
			var payload threadCreatedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, err
			}
			messages = append(messages, cloneMessages(payload.Messages)...)
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
		var payload threadCreatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, err
		}
		return payload.Messages, nil
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
