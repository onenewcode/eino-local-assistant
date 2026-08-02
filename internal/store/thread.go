package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/gofrs/flock"
)

const (
	threadsDirName  = "threads"
	journalFileName = "journal.jsonl"
	stateFileName   = "state.json"
	metaFileName    = "meta.json"
	checkpointsDir  = "checkpoints"
	artifactsDir    = "artifacts"
	locksDir        = "locks"
	writeLockName   = "write.lock"
)

// ThreadStore persists revisioned, append-only local agent threads.
type ThreadStore struct {
	root        string
	locks       sync.Map // map[string]*localThreadLock; serializes flock use in this process.
	materialize func(string, ThreadState) error
}

var _ ThreadRepository = (*ThreadStore)(nil)

type localThreadLock struct {
	held chan struct{}
}

func newLocalThreadLock() *localThreadLock {
	return &localThreadLock{held: make(chan struct{}, 1)}
}

// NewThreadStore creates the v2 thread root under root/threads.
func NewThreadStore(root string) (*ThreadStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("storage root is required")
	}
	if err := os.MkdirAll(filepath.Join(root, threadsDirName), 0o700); err != nil {
		return nil, fmt.Errorf("create threads directory: %w", err)
	}
	return &ThreadStore{root: root, materialize: writeMaterializedState}, nil
}

// Root returns the data directory containing the threads directory.
func (s *ThreadStore) Root() string {
	return s.root
}

// NewThreadID returns a time-sortable id: YYYYMMDD-HHMMSS-<6 hex>.
func NewThreadID(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC().Format("20060102-150405") + "-" + randomHex(3)
}

func newRandomID(prefix string) string {
	return prefix + "-" + time.Now().UTC().Format("20060102T150405000000000Z") + "-" + randomHex(8)
}

func randomHex(nBytes int) string {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%0*x", nBytes*2, time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func (s *ThreadStore) threadDir(id string) (string, error) {
	id = strings.TrimSpace(id)
	if err := validateThreadID(id); err != nil {
		return "", err
	}
	return filepath.Join(s.root, threadsDirName, id), nil
}

func validateThreadID(id string) error {
	if id == "" {
		return errors.New("thread id is required")
	}
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid thread id %q", id)
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("invalid thread id %q", id)
	}
	return nil
}

// CreateThread creates a thread and records the system prompt in its canonical
// thread.created event.
func (s *ThreadStore) CreateThread(ctx context.Context, meta ThreadMeta, systemPrompt string) (ThreadState, error) {
	systemPrompt = strings.TrimSpace(systemPrompt)
	if systemPrompt == "" {
		return ThreadState{}, errors.New("system prompt is required")
	}
	return s.createThread(ctx, meta, systemPrompt)
}

func (s *ThreadStore) createThread(ctx context.Context, meta ThreadMeta, systemPrompt string) (ThreadState, error) {
	id := strings.TrimSpace(meta.ID)
	if err := validateThreadID(id); err != nil {
		return ThreadState{}, err
	}
	finalDir, err := s.threadDir(id)
	if err != nil {
		return ThreadState{}, err
	}
	if _, err := os.Stat(finalDir); err == nil {
		return ThreadState{}, fmt.Errorf("thread %q already exists", id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ThreadState{}, fmt.Errorf("stat thread directory: %w", err)
	}
	parent := filepath.Dir(finalDir)
	dir, err := os.MkdirTemp(parent, ".creating-")
	if err != nil {
		return ThreadState{}, fmt.Errorf("create temporary thread directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return ThreadState{}, fmt.Errorf("set thread directory permissions: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := ensureThreadLayout(dir); err != nil {
		return ThreadState{}, err
	}

	now := time.Now().UTC()
	meta.ID = id
	meta.Title = strings.TrimSpace(meta.Title)
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	} else {
		meta.CreatedAt = meta.CreatedAt.UTC()
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = meta.CreatedAt
	} else {
		meta.UpdatedAt = meta.UpdatedAt.UTC()
	}
	meta.MessageCount = 0

	initial := ThreadState{
		FormatVersion: ThreadFormatVersion,
		ID:            id,
		CreatedAt:     meta.CreatedAt,
		UpdatedAt:     meta.UpdatedAt,
		Meta:          meta,
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return ThreadState{}, err
		}
	}
	messages := []*schema.Message{schema.SystemMessage(systemPrompt)}
	payload := threadCreatedPayload{Meta: meta, SystemPrompt: systemPrompt, Messages: messages}
	event, err := newThreadEvent(initial, EventThreadCreated, id, "", initial.Revision, payload, time.Now().UTC())
	if err != nil {
		return ThreadState{}, err
	}
	if err := appendJournalEvent(filepath.Join(dir, journalFileName), event); err != nil {
		return ThreadState{}, err
	}
	state := initial
	if err := applyThreadEvent(&state, event); err != nil {
		return ThreadState{}, err
	}
	if err := writeMaterializedState(dir, state); err != nil {
		return ThreadState{}, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return ThreadState{}, err
		}
	}
	if err := os.Rename(dir, finalDir); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ThreadState{}, fmt.Errorf("thread %q already exists", id)
		}
		return ThreadState{}, fmt.Errorf("publish thread directory: %w", err)
	}
	cleanup = false
	// The journal and projections are already published atomically. A parent
	// directory sync failure must not report creation as failed and invite a
	// duplicate retry, so this final durability hint is best effort.
	_ = syncDirectory(parent)
	return state, nil
}

// DeleteThread removes an entire thread directory. The advisory lock prevents a
// concurrent v2 writer from deleting a live journal underneath itself.
func (s *ThreadStore) DeleteThread(ctx context.Context, id string) error {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete thread: %w", err)
	}
	return nil
}

// ListThreads returns thread metadata sorted by most recent update first.
func (s *ThreadStore) ListThreads(ctx context.Context) ([]ThreadMeta, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, threadsDirName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list threads: %w", err)
	}

	metas := make([]ThreadMeta, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || validateThreadID(entry.Name()) != nil {
			continue
		}
		meta, loadErr := s.LoadThreadMeta(ctx, entry.Name())
		if loadErr != nil {
			return nil, fmt.Errorf("load thread %q metadata: %w", entry.Name(), loadErr)
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].UpdatedAt.Equal(metas[j].UpdatedAt) {
			return metas[i].ID > metas[j].ID
		}
		return metas[i].UpdatedAt.After(metas[j].UpdatedAt)
	})
	return metas, nil
}

// LoadThreadMeta loads the replayed metadata projection for one thread.
func (s *ThreadStore) LoadThreadMeta(ctx context.Context, id string) (ThreadMeta, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadMeta{}, err
	}
	defer unlock()
	state, _, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadMeta{}, err
	}
	return state.Meta, nil
}

// LoadThread replays the journal and repairs stale state/meta projections.
func (s *ThreadStore) LoadThread(ctx context.Context, id string) (ThreadState, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, err
	}
	defer unlock()
	state, _, err := s.loadThreadLocked(dir, id)
	return state, err
}

// LoadThreadTranscript returns a state snapshot and its visible transcript
// tail under one lock so paging cursors cannot mix different revisions.
func (s *ThreadStore) LoadThreadTranscript(ctx context.Context, id string, limit int) (ThreadState, []*schema.Message, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, nil, err
	}
	defer unlock()
	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, nil, err
	}
	messages, err := messagesFromEvents(events)
	if err != nil {
		return ThreadState{}, nil, err
	}
	return state, recentMessages(messages, limit), nil
}

// LoadRecentMessages returns the system prompt plus at most limit latest
// visible non-system messages. A non-positive limit returns all visible data.
func (s *ThreadStore) LoadRecentMessages(ctx context.Context, id string, limit int) ([]*schema.Message, error) {
	_, messages, err := s.LoadThreadTranscript(ctx, id, limit)
	return messages, err
}

// LoadMessagesPage returns one stable page of visible non-system messages.
// before is a zero-based count already skipped from the oldest visible body;
// callers advance it by len(page). The system prompt is intentionally omitted
// because it belongs to ThreadState and must not repeat on every transcript
// page. hasMore reports whether a later visible body page exists.
func (s *ThreadStore) LoadMessagesPage(ctx context.Context, id string, before, limit int) ([]*schema.Message, bool, error) {
	if before < 0 {
		return nil, false, errors.New("page offset must be >= 0")
	}
	if limit <= 0 {
		return nil, false, errors.New("page limit must be > 0")
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	_, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return nil, false, err
	}
	return messagesPageFromEvents(events, before, limit)
}

// RecoverInterruptedTurn terminally fails the one open turn left by an
// interrupted process. Resume callers pass the state revision they observed;
// a concurrent writer therefore produces ErrRevisionConflict instead of
// silently terminating a newer turn.
func (s *ThreadStore) RecoverInterruptedTurn(ctx context.Context, id string, expectedRevision uint64, reason string) (ThreadState, bool, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, false, err
	}
	defer unlock()
	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, false, err
	}
	if err := checkExpectedRevision(state, expectedRevision); err != nil {
		return ThreadState{}, false, err
	}
	tracker, err := lifecycleFromEvents(events)
	if err != nil {
		return ThreadState{}, false, err
	}
	if tracker.activeTurnID == "" {
		return state, false, nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "interrupted before terminal turn event"
	}
	next, err := s.appendLocked(dir, state, expectedRevision, EventTurnFailed, tracker.activeTurnID, TurnFailure{
		TurnID: tracker.activeTurnID,
		Error:  reason,
	})
	if err != nil {
		return ThreadState{}, false, err
	}
	return next, true, nil
}

func recentMessages(messages []*schema.Message, limit int) []*schema.Message {
	var system *schema.Message
	body := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		if system == nil && message.Role == schema.System {
			messageCopy := *message
			system = &messageCopy
			continue
		}
		messageCopy := *message
		body = append(body, &messageCopy)
	}
	if limit > 0 && len(body) > limit {
		body = body[len(body)-limit:]
	}
	if system == nil {
		return body
	}
	return append([]*schema.Message{system}, body...)
}

// StartTurn appends a turn.started event after a revision compare-and-swap.
func (s *ThreadStore) StartTurn(ctx context.Context, id string, expectedRevision uint64, input TurnStart) (ThreadState, error) {
	if strings.TrimSpace(input.TurnID) == "" {
		return ThreadState{}, errors.New("turn id is required")
	}
	return s.mutate(ctx, id, expectedRevision, EventTurnStarted, input.TurnID, input)
}

// ToolStarted appends a tool.started event after a revision compare-and-swap.
func (s *ThreadStore) ToolStarted(ctx context.Context, id string, expectedRevision uint64, input ToolStarted) (ThreadState, error) {
	if strings.TrimSpace(input.TurnID) == "" || strings.TrimSpace(input.ToolCallID) == "" {
		return ThreadState{}, errors.New("turn id and tool call id are required")
	}
	return s.mutate(ctx, id, expectedRevision, EventToolStarted, input.TurnID, input)
}

// ToolCompleted appends a tool.completed event after verifying any artifact.
func (s *ThreadStore) ToolCompleted(ctx context.Context, id string, expectedRevision uint64, input ToolCompleted) (ThreadState, error) {
	if strings.TrimSpace(input.TurnID) == "" || strings.TrimSpace(input.ToolCallID) == "" {
		return ThreadState{}, errors.New("turn id and tool call id are required")
	}
	return s.mutateWithValidation(ctx, id, expectedRevision, EventToolCompleted, input.TurnID, input, func(dir string) error {
		if input.Artifact == nil {
			return nil
		}
		return validateArtifactRef(dir, *input.Artifact)
	})
}

// CommitTurn appends all completed visible messages and usage as one event.
func (s *ThreadStore) CommitTurn(ctx context.Context, id string, expectedRevision uint64, input TurnCommit) (ThreadState, error) {
	if strings.TrimSpace(input.TurnID) == "" {
		return ThreadState{}, errors.New("turn id is required")
	}
	if err := validateMessages(input.Messages); err != nil {
		return ThreadState{}, err
	}
	input.Messages = cloneMessages(input.Messages)
	return s.mutate(ctx, id, expectedRevision, EventTurnCommitted, input.TurnID, input)
}

// CancelTurn appends a terminal cancellation event for an uncommitted turn.
func (s *ThreadStore) CancelTurn(ctx context.Context, id string, expectedRevision uint64, input TurnCancel) (ThreadState, error) {
	if strings.TrimSpace(input.TurnID) == "" {
		return ThreadState{}, errors.New("turn id is required")
	}
	return s.mutate(ctx, id, expectedRevision, EventTurnCancelled, input.TurnID, input)
}

// FailTurn appends a terminal failure event for an uncommitted turn.
func (s *ThreadStore) FailTurn(ctx context.Context, id string, expectedRevision uint64, input TurnFailure) (ThreadState, error) {
	if strings.TrimSpace(input.TurnID) == "" || strings.TrimSpace(input.Error) == "" {
		return ThreadState{}, errors.New("turn id and failure are required")
	}
	return s.mutate(ctx, id, expectedRevision, EventTurnFailed, input.TurnID, input)
}

// SetThreadTitle emits title.changed using a revision compare-and-swap.
func (s *ThreadStore) SetThreadTitle(ctx context.Context, id string, expectedRevision uint64, title string) (ThreadState, error) {
	return s.mutate(ctx, id, expectedRevision, EventTitleChanged, "", titleUpdatedPayload{Title: strings.TrimSpace(title)})
}

func (s *ThreadStore) mutate(ctx context.Context, id string, expectedRevision uint64, kind EventKind, turnID string, payload any) (ThreadState, error) {
	return s.mutateWithValidation(ctx, id, expectedRevision, kind, turnID, payload, nil)
}

func (s *ThreadStore) mutateWithValidation(ctx context.Context, id string, expectedRevision uint64, kind EventKind, turnID string, payload any, validate func(string) error) (ThreadState, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, err
	}
	defer unlock()
	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, err
	}
	if err := checkExpectedRevision(state, expectedRevision); err != nil {
		return ThreadState{}, err
	}
	if err := validateLifecycleMutation(events, kind, turnID, payload); err != nil {
		return ThreadState{}, err
	}
	if validate != nil {
		if err := validate(dir); err != nil {
			return ThreadState{}, err
		}
	}
	return s.appendLocked(dir, state, expectedRevision, kind, turnID, payload)
}

func checkExpectedRevision(state ThreadState, expected uint64) error {
	if expected == state.Revision {
		return nil
	}
	return fmt.Errorf("%w: expected %d, got %d", ErrRevisionConflict, expected, state.Revision)
}

func (s *ThreadStore) appendLocked(dir string, state ThreadState, expectedRevision uint64, kind EventKind, turnID string, payload any) (ThreadState, error) {
	event, err := newThreadEvent(state, kind, state.ID, turnID, expectedRevision, payload, time.Now().UTC())
	if err != nil {
		return ThreadState{}, err
	}
	if err := appendJournalEvent(filepath.Join(dir, journalFileName), event); err != nil {
		return ThreadState{}, err
	}
	if err := applyThreadEvent(&state, event); err != nil {
		return ThreadState{}, err
	}
	if err := s.materializeState(dir, state); err != nil {
		// The fsynced journal is already committed. Treat projections as a
		// repairable cache so callers retain the new revision and never retry the
		// same lifecycle operation as though it had failed.
		return state, nil
	}
	return state, nil
}

func (s *ThreadStore) loadThreadLocked(dir, id string) (ThreadState, []ThreadEvent, error) {
	state, events, _, err := replayJournal(filepath.Join(dir, journalFileName), id)
	if err != nil {
		return ThreadState{}, nil, err
	}
	if state.ID != id {
		return ThreadState{}, nil, fmt.Errorf("%w: journal thread id %q does not match %q", ErrJournalCorrupt, state.ID, id)
	}
	// state.json and meta.json are rebuildable projections. A later load or
	// mutation retries this materialization; the verified journal stays usable.
	_ = s.materializeState(dir, state)
	return state, events, nil
}

func (s *ThreadStore) materializeState(dir string, state ThreadState) error {
	if s.materialize == nil {
		return writeMaterializedState(dir, state)
	}
	return s.materialize(dir, state)
}

func (s *ThreadStore) lockThread(ctx context.Context, id string) (string, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dir, err := s.threadDir(id)
	if err != nil {
		return "", nil, err
	}
	if _, err := os.Stat(dir); err != nil {
		return "", nil, fmt.Errorf("thread directory: %w", err)
	}
	value, _ := s.locks.LoadOrStore(id, newLocalThreadLock())
	local := value.(*localThreadLock)
	select {
	case local.held <- struct{}{}:
	case <-ctx.Done():
		return "", nil, fmt.Errorf("%w: %v", ErrThreadLocked, ctx.Err())
	}

	unlockLocal := func() { <-local.held }
	if err := os.MkdirAll(filepath.Join(dir, locksDir), 0o700); err != nil {
		unlockLocal()
		return "", nil, fmt.Errorf("create thread locks directory: %w", err)
	}
	fileLock := flock.New(filepath.Join(dir, locksDir, writeLockName))
	locked, err := fileLock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		unlockLocal()
		if ctx.Err() != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrThreadLocked, ctx.Err())
		}
		return "", nil, fmt.Errorf("lock thread: %w", err)
	}
	if !locked {
		unlockLocal()
		return "", nil, ErrThreadLocked
	}
	unlock := func() {
		_ = fileLock.Unlock()
		_ = fileLock.Close()
		unlockLocal()
	}
	return dir, unlock, nil
}

func ensureThreadLayout(dir string) error {
	for _, name := range []string{checkpointsDir, artifactsDir, locksDir} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			return fmt.Errorf("create thread %s directory: %w", name, err)
		}
	}
	journal := filepath.Join(dir, journalFileName)
	if err := os.WriteFile(journal, nil, 0o600); err != nil {
		return fmt.Errorf("create thread journal: %w", err)
	}
	return nil
}

func validateMessages(messages []*schema.Message) error {
	if len(messages) == 0 {
		return errors.New("at least one message is required")
	}
	for _, message := range messages {
		if message == nil {
			return errors.New("cannot persist a nil message")
		}
		if strings.TrimSpace(string(message.Role)) == "" {
			return errors.New("message role is required")
		}
	}
	return nil
}
