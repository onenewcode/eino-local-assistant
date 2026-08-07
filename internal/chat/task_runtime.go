package chat

import (
	"context"
	"errors"
	"strings"

	"eino-local-assistant/internal/store"
)

// TaskListItem is one display-safe checklist row for the TUI.
type TaskListItem struct {
	ID    string
	Goal  string
	State string
}

// TaskRunStatus is a compact, UI-safe projection of the checklist.
type TaskRunStatus struct {
	Available   bool
	State       string
	Goal        string
	Tasks       int
	DoneTasks   int
	ActiveTasks []string
	Items       []TaskListItem
}

// TaskInterruptReceipt reports whether interruption changed the checklist.
type TaskInterruptReceipt struct {
	Applied bool
	Summary string
}

// TaskRuntime is the narrow session-facing plan contract implemented by the
// agent model. Keeping this interface in chat prevents session from importing
// internal/agent.
type TaskRuntime interface {
	TaskExecutionStatus(context.Context) TaskRunStatus
	InterruptTask(context.Context, string) TaskInterruptReceipt
}

type taskRuntimeContextKey struct{}

type taskTurnContextKey struct{}

type taskStateWriterContextKey struct{}

// TaskStateWriter persists an opaque controller snapshot. It is installed by
// Session so agent can recover task state without importing store directly.
type TaskStateWriter func(context.Context, []byte) error

// WithTaskRunContext scopes plan runtime state to one active chat thread.
func WithTaskRunContext(ctx context.Context, threadID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, taskRuntimeContextKey{}, strings.TrimSpace(threadID))
}

// TaskRunIDFromContext returns the session ID available to the plan runtime.
func TaskRunIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(taskRuntimeContextKey{}).(string)
	id = strings.TrimSpace(id)
	return id, ok && id != ""
}

// WithTaskTurnContext binds the durable turn ID for steer and tool lifecycle.
// It conveys identity only, not ledger access.
func WithTaskTurnContext(ctx context.Context, turnID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, taskTurnContextKey{}, strings.TrimSpace(turnID))
}

// TaskTurnIDFromContext returns the durable turn when execution is inside Session.Ask.
func TaskTurnIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(taskTurnContextKey{}).(string)
	id = strings.TrimSpace(id)
	return id, ok && id != ""
}

// WithTaskStateWriter adds the session-owned persistence bridge to a plan runtime context.
func WithTaskStateWriter(ctx context.Context, writer TaskStateWriter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if writer == nil {
		return ctx
	}
	return context.WithValue(ctx, taskStateWriterContextKey{}, writer)
}

// PersistTaskState writes one controller snapshot when the current Session
// supports durable plan state.
func PersistTaskState(ctx context.Context, snapshot []byte) error {
	if ctx == nil || len(snapshot) == 0 {
		return nil
	}
	writer, ok := ctx.Value(taskStateWriterContextKey{}).(TaskStateWriter)
	if !ok || writer == nil {
		return nil
	}
	copySnapshot := append([]byte(nil), snapshot...)
	return writer(ctx, copySnapshot)
}

// LoadTaskStateSnapshot restores the latest plan projection from the thread ledger.
func LoadTaskStateSnapshot(ctx context.Context) ([]byte, error) {
	recovery, err := LoadTaskStateRecovery(ctx)
	if err != nil {
		return nil, err
	}
	return recovery.Snapshot, nil
}

// TaskStateRecovery keeps the opaque plan snapshot separate from ledger recovery facts.
type TaskStateRecovery struct {
	Snapshot                             []byte
	SnapshotTurnID                       string
	SnapshotTurnCommitted                bool
	PotentiallyMutatingToolAfterSnapshot bool
}

// LoadTaskStateRecovery restores a plan snapshot with optional recovery metadata.
func LoadTaskStateRecovery(ctx context.Context) (TaskStateRecovery, error) {
	access, ok := store.ThreadAccessFromContext(ctx)
	if !ok {
		return TaskStateRecovery{SnapshotTurnCommitted: true}, nil
	}
	repository, ok := access.Repository.(store.TaskStateRepository)
	if !ok {
		return TaskStateRecovery{SnapshotTurnCommitted: true}, nil
	}
	if recoveryRepository, ok := access.Repository.(store.TaskStateRecoveryRepository); ok {
		recovery, err := recoveryRepository.LoadTaskStateRecovery(ctx, access.ThreadID)
		if err != nil {
			return TaskStateRecovery{}, err
		}
		return TaskStateRecovery{
			Snapshot:                             append([]byte(nil), recovery.Snapshot...),
			SnapshotTurnID:                       recovery.SnapshotTurnID,
			SnapshotTurnCommitted:                recovery.SnapshotTurnCommitted,
			PotentiallyMutatingToolAfterSnapshot: recovery.PotentiallyMutatingToolAfterSnapshot,
		}, nil
	}
	snapshot, err := repository.LoadTaskState(ctx, access.ThreadID)
	if err != nil || len(snapshot) == 0 {
		return TaskStateRecovery{SnapshotTurnCommitted: true}, err
	}
	return TaskStateRecovery{Snapshot: append([]byte(nil), snapshot...), SnapshotTurnCommitted: true}, nil
}

// TaskStatus returns the active plan projection when the model supports it.
func (s *Session) TaskStatus() TaskRunStatus {
	if s == nil {
		return TaskRunStatus{}
	}
	runtime, ok := s.model.(TaskRuntime)
	if !ok {
		return TaskRunStatus{}
	}
	return runtime.TaskExecutionStatus(s.taskRuntimeContext(context.Background()))
}

// InterruptTask records a user interruption of the checklist before the model turn is cancelled.
func (s *Session) InterruptTask(ctx context.Context, reason string) TaskInterruptReceipt {
	if s == nil {
		return TaskInterruptReceipt{Summary: "plan runtime is unavailable"}
	}
	runtime, ok := s.model.(TaskRuntime)
	if !ok {
		return TaskInterruptReceipt{Summary: "plan runtime is unavailable"}
	}
	return runtime.InterruptTask(s.taskRuntimeContext(ctx), reason)
}

func (s *Session) taskRuntimeContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = store.WithThreadAccess(ctx, s.threads, s.id)
	ctx = WithTaskRunContext(ctx, s.id)
	if _, ok := s.threads.(store.TaskStateRepository); ok {
		ctx = WithTaskStateWriter(ctx, s.persistTaskState)
	}
	return ctx
}

// persistTaskState records plan changes outside a model turn.
func (s *Session) persistTaskState(ctx context.Context, snapshot []byte) error {
	repository, ok := s.threads.(store.TaskStateRepository)
	if !ok {
		return nil
	}
	if len(snapshot) == 0 {
		return nil
	}
	threadID := s.id
	payload := store.TaskStateUpdate{Snapshot: append([]byte(nil), snapshot...)}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		state, err := s.threads.LoadThread(ctx, threadID)
		if err != nil {
			return err
		}
		next, err := repository.UpdateTaskState(ctx, threadID, state.Revision, "", payload)
		if err == nil {
			s.applyThreadState(next)
			return nil
		}
		if !errors.Is(err, store.ErrRevisionConflict) {
			return err
		}
		lastErr = err
	}
	return lastErr
}
