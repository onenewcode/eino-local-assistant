package store

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

	"github.com/cloudwego/eino/schema"
	"github.com/gofrs/flock"
)

const (
	// sessionsDirName is the on-disk directory for conversation sessions.
	// Internally the store still uses "thread" terminology for the revisioned
	// event ledger, but user-facing storage lives under sessions/.
	sessionsDirName   = "sessions"
	sessionDateLayout = "2006/01/02"
	journalFileName   = "updates.jsonl"
	summaryFileName   = "summary.json"
)

// ThreadStore persists revisioned, append-only local agent sessions. Each
// active session is a date-partitioned directory with an append-only JSONL
// ledger and a rebuildable summary projection.
type ThreadStore struct {
	root     string
	db       *sql.DB
	readOnly bool
	locks    sync.Map // map[string]*localThreadLock; serializes flock use in this process.
}

var _ ThreadRepository = (*ThreadStore)(nil)
var _ ThreadModelRepository = (*ThreadStore)(nil)
var _ ThreadModelBindingRepository = (*ThreadStore)(nil)
var _ ThreadOpenSnapshotRepository = (*ThreadStore)(nil)

type localThreadLock struct {
	held chan struct{}
}

var errThreadAlreadyExists = errors.New("thread already exists")

func newLocalThreadLock() *localThreadLock {
	return &localThreadLock{held: make(chan struct{}, 1)}
}

// NewThreadStore creates the session store root under root/sessions.
func NewThreadStore(root string) (*ThreadStore, error) {
	return OpenThreadStore(root, ThreadStoreOptions{})
}

// ThreadStoreOptions controls whether opening may create or repair projections.
type ThreadStoreOptions struct{ ReadOnly bool }

// OpenThreadStore opens the canonical journals and their rebuildable SQLite view.
func OpenThreadStore(root string, opts ThreadStoreOptions) (*ThreadStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("storage root is required")
	}
	if opts.ReadOnly {
		if _, err := os.Stat(filepath.Join(root, sessionsDirName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		s := &ThreadStore{root: root, readOnly: true}
		if err := s.openProjection(true); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return s, nil
	}
	if err := os.MkdirAll(filepath.Join(root, sessionsDirName), 0o700); err != nil {
		return nil, fmt.Errorf("create sessions directory: %w", err)
	}
	s := &ThreadStore{root: root}
	if err := s.openProjection(false); err != nil {
		return nil, err
	}
	return s, nil
}

// Close releases the SQLite projection. Journals remain durable and authoritative.
func (s *ThreadStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Root returns the data directory containing the sessions directory.
func (s *ThreadStore) Root() string {
	return s.root
}

func (s *ThreadStore) sessionsRoot() string {
	return filepath.Join(s.root, sessionsDirName)
}

// NewThreadID returns a day-local time-sortable ID. The date belongs only in
// the session directory hierarchy, so it is not duplicated in the filename.
func NewThreadID(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC().Format("150405") + "-" + randomHex(3)
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
	path, err := s.threadJournalPath(id)
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

func (s *ThreadStore) threadJournalPath(id string) (string, error) {
	id = strings.TrimSpace(id)
	if err := validateThreadID(id); err != nil {
		return "", err
	}
	var datedCandidate string
	if createdAt, ok := threadIDDate(id); ok {
		datedCandidate = s.newThreadJournalPath(id, createdAt)
		if info, err := os.Lstat(datedCandidate); err == nil && info.Mode().IsRegular() {
			return datedCandidate, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect session journal: %w", err)
		}
	}
	paths, err := s.activeThreadPaths()
	if err != nil {
		return "", err
	}
	if dir, ok := paths[id]; ok {
		return dir, nil
	}
	if datedCandidate != "" {
		return datedCandidate, nil
	}
	// Callers that create a new thread or report a missing thread still need a
	// deterministic target even when a custom test or imported ID has no date.
	return s.newThreadJournalPath(id, time.Now().UTC()), nil
}

// ThreadPath returns the authoritative on-disk JSONL for a session. It is used
// by callers that must protect a resumed ledger from workspace tool access.
func (s *ThreadStore) ThreadPath(id string) (string, error) {
	if s == nil {
		return "", errors.New("thread store is required")
	}
	return s.threadJournalPath(id)
}

func (s *ThreadStore) newThreadDir(id string, createdAt time.Time) string {
	return filepath.Join(sessionDayDir(s.sessionsRoot(), createdAt), id)
}

func (s *ThreadStore) newThreadJournalPath(id string, createdAt time.Time) string {
	return journalPath(s.newThreadDir(id, createdAt), id)
}

func journalPath(dir, _ string) string {
	return filepath.Join(dir, journalFileName)
}

func sessionDayDir(sessionsRoot string, createdAt time.Time) string {
	createdAt = createdAt.UTC()
	return filepath.Join(sessionsRoot, createdAt.Format("2006"), createdAt.Format("01"), createdAt.Format("02"))
}

func threadIDDate(id string) (time.Time, bool) {
	if len(id) < len("20060102-") || id[8] != '-' {
		return time.Time{}, false
	}
	createdAt, err := time.Parse("20060102", id[:8])
	if err != nil {
		return time.Time{}, false
	}
	return createdAt.UTC(), true
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

// CreateThread creates a JSONL-backed thread with its frozen system prompt in
// the initial thread.created event.
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
	now := time.Now().UTC()
	meta.ID = id
	meta.Title = strings.TrimSpace(meta.Title)
	meta.ReasoningEffort = strings.TrimSpace(meta.ReasoningEffort)
	// A thread starts with an empty ledger. Only usage.recorded events may
	// populate token, cost, or context projections after creation.
	clearUsageProjection(&meta)
	meta.UsageStatus = UsageStatusExact
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
	finalDir := s.newThreadDir(id, meta.CreatedAt)
	finalJournal := journalPath(finalDir, id)
	if err := s.ensureThreadIDAbsent(id, finalJournal); err != nil {
		return ThreadState{}, err
	}
	parent := filepath.Dir(finalDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return ThreadState{}, fmt.Errorf("create session date directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(parent, ".creating-")
	if err != nil {
		return ThreadState{}, fmt.Errorf("create temporary session directory: %w", err)
	}
	if err := os.Chmod(tmpDir, 0o700); err != nil {
		_ = os.RemoveAll(tmpDir)
		return ThreadState{}, fmt.Errorf("set temporary session directory permissions: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	initial := ThreadState{
		FormatVersion: SessionJournalFormatVersion,
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
	promptBytes := []byte(systemPrompt)
	payload := threadCreatedPayload{
		Meta: meta,
		SystemPrompt: systemPromptRef{
			Content: systemPrompt,
			SHA256:  sha256Hex(promptBytes),
			Bytes:   int64(len(promptBytes)),
		},
	}
	event, err := newThreadEvent(initial, EventThreadCreated, id, "", initial.Revision, payload, time.Now().UTC())
	if err != nil {
		return ThreadState{}, err
	}
	if err := appendJournalEvent(journalPath(tmpDir, id), event); err != nil {
		return ThreadState{}, err
	}
	state := initial
	if err := applyThreadEvent(&state, event); err != nil {
		return ThreadState{}, err
	}
	state.SystemPrompt = systemPrompt
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return ThreadState{}, err
		}
	}
	if err := os.Rename(tmpDir, finalDir); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ThreadState{}, fmt.Errorf("thread %q already exists", id)
		}
		return ThreadState{}, fmt.Errorf("publish session directory: %w", err)
	}
	cleanup = false
	// The ledger is published atomically with its session directory. A parent
	// directory sync failure must not report creation as failed and invite a
	// duplicate retry, so this final durability hint is best effort.
	_ = syncDirectory(parent)
	_ = s.projectThread(finalDir, state, []ThreadEvent{event})
	return state, nil
}

// DeleteThread removes one session journal. The advisory lock prevents a
// concurrent writer from deleting a live journal underneath itself.
func (s *ThreadStore) DeleteThread(ctx context.Context, id string) error {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete session directory: %w", err)
	}
	if s.db != nil {
		_, _ = s.db.Exec(`DELETE FROM threads WHERE id=?`, id)
	}
	return nil
}

// ListThreads returns thread metadata sorted by most recent update first.
func (s *ThreadStore) ListThreads(ctx context.Context) ([]ThreadMeta, error) {
	paths, err := s.activeThreadPaths()
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}

	metas := make([]ThreadMeta, 0, len(paths))
	for id := range paths {
		meta, fresh, loadErr := s.projectedMetaIfFresh(id)
		if loadErr == nil && !fresh {
			meta, loadErr = s.LoadThreadMeta(ctx, id)
		}
		if loadErr != nil {
			return nil, fmt.Errorf("load thread %q metadata: %w", id, loadErr)
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

// ListThreadsReadOnly returns journal-derived metadata without repairing the
// SQLite projection. It is used when selecting a source session that must
// remain byte-for-byte untouched by an ephemeral run.
func (s *ThreadStore) ListThreadsReadOnly(ctx context.Context) ([]ThreadMeta, error) {
	paths, err := s.activeThreadPaths()
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}

	metas := make([]ThreadMeta, 0, len(paths))
	for id := range paths {
		meta, loadErr := s.loadThreadMetaReadOnly(ctx, id)
		if loadErr != nil {
			return nil, fmt.Errorf("load thread %q metadata: %w", id, loadErr)
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

// activeThreadPaths discovers session directories in the current YYYY/MM/DD
// hierarchy. A valid session owns its updates.jsonl ledger directly.
func (s *ThreadStore) activeThreadPaths() (map[string]string, error) {
	sessions := s.sessionsRoot()
	years, err := os.ReadDir(sessions)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	paths := make(map[string]string)
	for _, year := range years {
		if !year.IsDir() {
			continue
		}
		yearPath := filepath.Join(sessions, year.Name())
		months, err := os.ReadDir(yearPath)
		if err != nil {
			return nil, fmt.Errorf("read session year %q: %w", year.Name(), err)
		}
		for _, month := range months {
			if !month.IsDir() {
				continue
			}
			monthPath := filepath.Join(yearPath, month.Name())
			days, err := os.ReadDir(monthPath)
			if err != nil {
				return nil, fmt.Errorf("read session month %q/%q: %w", year.Name(), month.Name(), err)
			}
			for _, day := range days {
				if !day.IsDir() {
					continue
				}
				if _, err := time.Parse(sessionDateLayout, filepath.Join(year.Name(), month.Name(), day.Name())); err != nil {
					continue
				}
				dayPath := filepath.Join(monthPath, day.Name())
				entries, err := os.ReadDir(dayPath)
				if err != nil {
					return nil, fmt.Errorf("read session day %q/%q/%q: %w", year.Name(), month.Name(), day.Name(), err)
				}
				for _, entry := range entries {
					if !entry.IsDir() || validateThreadID(entry.Name()) != nil {
						continue
					}
					id := entry.Name()
					journal := journalPath(filepath.Join(dayPath, id), id)
					info, err := os.Lstat(journal)
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					if err != nil {
						return nil, fmt.Errorf("inspect session journal %q: %w", id, err)
					}
					if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
						continue
					}
					if _, exists := paths[id]; exists {
						return nil, fmt.Errorf("duplicate session id %q", id)
					}
					paths[id] = journal
				}
			}
		}
	}
	return paths, nil
}

func (s *ThreadStore) ensureThreadIDAbsent(id, candidate string) error {
	paths, err := s.activeThreadPaths()
	if err != nil {
		return fmt.Errorf("inspect existing sessions: %w", err)
	}
	if _, exists := paths[id]; exists {
		return fmt.Errorf("%w: %q", errThreadAlreadyExists, id)
	}
	if _, err := os.Lstat(filepath.Dir(candidate)); err == nil {
		return fmt.Errorf("%w: %q", errThreadAlreadyExists, id)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat session directory: %w", err)
	}
	return nil
}

func (s *ThreadStore) loadThreadMetaReadOnly(ctx context.Context, id string) (ThreadMeta, error) {
	var meta ThreadMeta
	err := s.withReadOnlyThread(ctx, id, func(dir string, locked bool) error {
		var state ThreadState
		var err error
		if locked {
			state, _, _, err = replayJournalReadOnly(journalPath(dir, id), id)
		} else {
			state, err = stableReadThread(ctx, dir, id)
		}
		if err != nil {
			return err
		}
		meta = state.Meta
		return nil
	})
	return meta, err
}

func stableReadThread(_ context.Context, dir, id string) (ThreadState, error) {
	state, _, _, err := replayJournalReadOnly(journalPath(dir, id), id)
	return state, err
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

// LoadThread uses a matching SQLite projection or replays the journal to repair it.
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
	messages, err := messagesFromEvents(events, state.SystemPrompt)
	if err != nil {
		return ThreadState{}, nil, err
	}
	return state, recentMessages(messages, limit), nil
}

func (s *ThreadStore) LoadThreadOpenSnapshot(ctx context.Context, id string, limit int) (ThreadOpenSnapshot, error) {
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadOpenSnapshot{}, err
	}
	defer unlock()
	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadOpenSnapshot{}, err
	}
	messages, err := messagesFromEvents(events, state.SystemPrompt)
	if err != nil {
		return ThreadOpenSnapshot{}, err
	}
	groups, err := turnGroupsFromEvents(events)
	if err != nil {
		return ThreadOpenSnapshot{}, err
	}
	return ThreadOpenSnapshot{State: state, Transcript: recentMessages(messages, limit), TurnGroups: groups}, nil
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
		return validateArtifactRef(*input.Artifact)
	})
}

// CommitTurn appends all completed visible messages as one event. Model usage
// is recorded separately before this call with RecordUsage.
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

// FinishTurn atomically closes the specified active turn. A terminal event is
// immutable and bound to TurnID, so unrelated metadata revisions can safely
// rebase under the store lock without closing a newer turn.
func (s *ThreadStore) FinishTurn(ctx context.Context, id string, input TurnFinish) (ThreadState, error) {
	input.TurnID = strings.TrimSpace(input.TurnID)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.TurnID == "" {
		return ThreadState{}, errors.New("turn id is required")
	}
	dir, unlock, err := s.lockThread(ctx, id)
	if err != nil {
		return ThreadState{}, err
	}
	defer unlock()
	state, events, err := s.loadThreadLocked(dir, id)
	if err != nil {
		return ThreadState{}, err
	}
	if input.Cancelled {
		payload := TurnCancel{TurnID: input.TurnID, Reason: input.Reason}
		if err := validateLifecycleMutation(events, EventTurnCancelled, input.TurnID, payload); err != nil {
			return ThreadState{}, err
		}
		return s.appendLocked(dir, state, state.Revision, EventTurnCancelled, input.TurnID, payload)
	}
	if input.Reason == "" {
		return ThreadState{}, errors.New("turn failure is required")
	}
	payload := TurnFailure{TurnID: input.TurnID, Error: input.Reason}
	if err := validateLifecycleMutation(events, EventTurnFailed, input.TurnID, payload); err != nil {
		return ThreadState{}, err
	}
	return s.appendLocked(dir, state, state.Revision, EventTurnFailed, input.TurnID, payload)
}

// SetThreadTitle emits title.changed using a revision compare-and-swap.
func (s *ThreadStore) SetThreadTitle(ctx context.Context, id string, expectedRevision uint64, title string) (ThreadState, error) {
	return s.mutate(ctx, id, expectedRevision, EventTitleChanged, "", titleUpdatedPayload{Title: strings.TrimSpace(title)})
}

// SetThreadModel emits model.changed after checking the full durable lifecycle
// under the write lock. The caller supplies a constructed provider separately;
// this method only records the selected identity and never constructs one.
// Its empty-model behavior remains the legacy provider-default selection.
func (s *ThreadStore) SetThreadModel(ctx context.Context, id string, expectedRevision uint64, model string) (ThreadState, error) {
	return s.setThreadModelBinding(ctx, id, expectedRevision, model, "", false)
}

// SetThreadModelBinding emits one model.changed event for the complete model
// selection tuple. An empty model retains the current identity so callers can
// update only reasoning effort; an empty effort clears the requested value.
// This method only records opaque selection data and performs no provider or
// catalog validation.
func (s *ThreadStore) SetThreadModelBinding(ctx context.Context, id string, expectedRevision uint64, model, reasoningEffort string) (ThreadState, error) {
	return s.setThreadModelBinding(ctx, id, expectedRevision, model, reasoningEffort, true)
}

func (s *ThreadStore) setThreadModelBinding(ctx context.Context, id string, expectedRevision uint64, model, reasoningEffort string, preserveEmptyModel bool) (ThreadState, error) {
	model = strings.TrimSpace(model)
	reasoningEffort = strings.TrimSpace(reasoningEffort)
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
	if preserveEmptyModel && model == "" {
		model = state.Meta.Model
	}
	tracker, err := lifecycleFromEvents(events)
	if err != nil {
		return ThreadState{}, err
	}
	if tracker.activeTurnID != "" {
		return ThreadState{}, fmt.Errorf("%w: %q", ErrModelChangeActiveTurn, tracker.activeTurnID)
	}
	if state.PendingCompaction != nil {
		return ThreadState{}, fmt.Errorf("%w: %q", ErrModelChangePendingCompaction, state.PendingCompaction.OperationID)
	}
	return s.appendLocked(dir, state, expectedRevision, EventModelChanged, "", ModelChange{
		Model:           model,
		ReasoningEffort: reasoningEffort,
	})
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
	if err := appendJournalEvent(journalPath(dir, state.ID), event); err != nil {
		return ThreadState{}, err
	}
	if err := applyThreadEvent(&state, event); err != nil {
		return ThreadState{}, err
	}
	if err := s.projectEvent(dir, state, event); err != nil {
		// The fsynced journal is already committed. Treat projections as a
		// repairable cache so callers retain the new revision and never retry the
		// same lifecycle operation as though it had failed.
		return state, nil
	}
	return state, nil
}

func (s *ThreadStore) loadThreadLocked(dir, id string) (ThreadState, []ThreadEvent, error) {
	if state, events, fresh, err := s.loadProjectedThread(dir, id); err != nil {
		return ThreadState{}, nil, err
	} else if fresh {
		return state, events, nil
	}
	state, events, _, err := replayJournal(journalPath(dir, id), id)
	if err != nil {
		return ThreadState{}, nil, err
	}
	if state.ID != id {
		return ThreadState{}, nil, fmt.Errorf("%w: journal thread id %q does not match %q", ErrJournalCorrupt, state.ID, id)
	}
	_ = s.projectThread(dir, state, events)
	return state, events, nil
}

func (s *ThreadStore) lockThread(ctx context.Context, id string) (string, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dir, err := s.threadDir(id)
	if err != nil {
		return "", nil, err
	}
	path := journalPath(dir, id)
	if _, err := os.Stat(path); err != nil {
		return "", nil, fmt.Errorf("session journal: %w", err)
	}
	unlockLocal, err := s.holdLocalThreadLock(ctx, id)
	if err != nil {
		return "", nil, err
	}
	fileLock := flock.New(path)
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

func (s *ThreadStore) holdLocalThreadLock(ctx context.Context, id string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	value, _ := s.locks.LoadOrStore(id, newLocalThreadLock())
	local := value.(*localThreadLock)
	select {
	case local.held <- struct{}{}:
		return func() { <-local.held }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %v", ErrThreadLocked, ctx.Err())
	}
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
