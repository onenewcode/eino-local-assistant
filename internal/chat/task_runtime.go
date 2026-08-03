package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

// ErrTaskCompletionUnresolved means an autonomous task still has deterministic
// gaps after the controller gave the model several chances to repair them.
// The turn is deliberately not committed as a successful delivery in this case.
var ErrTaskCompletionUnresolved = errors.New("autonomous task completion remains unresolved")

// TaskRunStatus is a compact, UI-safe projection of controller-owned task
// state. The full graph remains private to the runtime and is injected into
// model context only when a run is active.
type TaskRunStatus struct {
	Available    bool
	State        string
	Goal         string
	Requirements int
	Scenarios    int
	Tasks        int
	DoneTasks    int
	ActiveTask   string
	PlanRequired bool
	Gaps         []string
}

// TaskCompletionGate is evaluated by chat.Session after a model response. A
// live run is not allowed to become a user-visible completed delivery until
// Complete is true. Gap is an actionable controller message for the next
// internal model call.
type TaskCompletionGate struct {
	Active   bool
	Complete bool
	Summary  string
	Gap      string
}

// TaskInterruptReceipt reports whether an interactive interruption changed an
// active autonomous run. The task graph stays an internal runtime detail.
type TaskInterruptReceipt struct {
	Applied bool
	Summary string
}

// TaskRuntime is the narrow session-facing contract implemented by an agent
// model that supports autonomous task execution. Keeping this interface in
// chat prevents the session lifecycle from importing internal/agent.
type TaskRuntime interface {
	TaskExecutionStatus(context.Context) TaskRunStatus
	TaskCompletionGate(context.Context) TaskCompletionGate
	InterruptTask(context.Context, string) TaskInterruptReceipt
}

// TaskCompletionRevoker is implemented by runtimes that bind completion
// approval to the enclosing durable turn. Session invokes it only when that
// turn ends before its final delivery can commit.
type TaskCompletionRevoker interface {
	AbortTaskCompletion(context.Context, string) TaskInterruptReceipt
}

type taskRuntimeContextKey struct{}

type taskRequestContextKey struct{}

type taskTurnContextKey struct{}

type taskStateWriterContextKey struct{}

// TaskStateWriter persists an opaque controller snapshot. It is installed by
// Session so agent can recover task state without importing store directly.
type TaskStateWriter func(context.Context, []byte) error

// WithTaskRunContext scopes an autonomous task runtime to one active chat
// thread without exposing the store implementation to internal/agent. Session
// installs this alongside the separate store.ThreadAccess context used by
// thread-scoped tools.
func WithTaskRunContext(ctx context.Context, threadID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, taskRuntimeContextKey{}, strings.TrimSpace(threadID))
}

// TaskRunIDFromContext returns the session ID available to the optional task
// runtime. It intentionally carries no repository capability.
func TaskRunIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(taskRuntimeContextKey{}).(string)
	id = strings.TrimSpace(id)
	return id, ok && id != ""
}

// WithTaskRequestContext preserves the current user request for a task-plan
// controller. It is deliberately a string-only context value: the controller
// needs an immutable root requirement, not access to Session or the ledger.
func WithTaskRequestContext(ctx context.Context, input string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, taskRequestContextKey{}, strings.TrimSpace(input))
}

// TaskRequestFromContext returns the direct user request associated with the
// current durable turn, when the caller is running inside Session.Ask.
func TaskRequestFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	input, ok := ctx.Value(taskRequestContextKey{}).(string)
	input = strings.TrimSpace(input)
	return input, ok && input != ""
}

// WithTaskTurnContext binds controller completion approval to the durable turn
// currently being recorded by Session. It conveys identity only, not ledger
// access or any additional capability.
func WithTaskTurnContext(ctx context.Context, turnID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, taskTurnContextKey{}, strings.TrimSpace(turnID))
}

// TaskTurnIDFromContext returns the durable turn that owns the current task
// action, when execution is inside Session.Ask.
func TaskTurnIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(taskTurnContextKey{}).(string)
	id = strings.TrimSpace(id)
	return id, ok && id != ""
}

// WithTaskStateWriter adds the session-owned persistence bridge to a task
// runtime context. The payload schema remains owned by internal/agent.
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
// supports durable task state. Models without the optional capability remain
// compatible and simply keep the task graph in memory for that process.
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

// LoadTaskStateSnapshot restores the latest task-controller projection from
// the thread ledger when its repository supports the optional extension.
func LoadTaskStateSnapshot(ctx context.Context) ([]byte, error) {
	recovery, err := LoadTaskStateRecovery(ctx)
	if err != nil {
		return nil, err
	}
	return recovery.Snapshot, nil
}

// TaskStateRecovery keeps the opaque controller snapshot separate from the
// conservative ledger facts needed to avoid trusting a snapshot that predates
// a potentially mutating tool event or an uncommitted completion turn.
type TaskStateRecovery struct {
	Snapshot                             []byte
	SnapshotTurnID                       string
	SnapshotTurnCommitted                bool
	PotentiallyMutatingToolAfterSnapshot bool
}

// LoadTaskStateRecovery restores a task snapshot with the strongest recovery
// metadata offered by the active repository. Older repository implementations
// retain the original snapshot-only behavior.
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

// TaskStatus returns the active task projection when the configured model
// supports the optional autonomous-task runtime. Models without the feature
// remain fully compatible and report Available=false.
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

// InterruptTask records a user interruption before the active model turn is
// cancelled. Models without the optional runtime remain compatible.
func (s *Session) InterruptTask(ctx context.Context, reason string) TaskInterruptReceipt {
	if s == nil {
		return TaskInterruptReceipt{Summary: "task runtime is unavailable"}
	}
	runtime, ok := s.model.(TaskRuntime)
	if !ok {
		return TaskInterruptReceipt{Summary: "task runtime is unavailable"}
	}
	interruptCtx := s.taskRuntimeContext(ctx)
	receipt := runtime.InterruptTask(interruptCtx, reason)
	if revocation := s.abortTaskCompletionForActiveTurn(ctx, reason); revocation.Applied {
		return revocation
	}
	return receipt
}

func (s *Session) setActiveTaskTurn(turnID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.activeTaskTurnID = strings.TrimSpace(turnID)
	s.mu.Unlock()
}

func (s *Session) clearActiveTaskTurn(turnID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.activeTaskTurnID == strings.TrimSpace(turnID) {
		s.activeTaskTurnID = ""
	}
	s.mu.Unlock()
}

func (s *Session) abortTaskCompletionForActiveTurn(ctx context.Context, reason string) TaskInterruptReceipt {
	if s == nil {
		return TaskInterruptReceipt{}
	}
	s.mu.RLock()
	turnID := s.activeTaskTurnID
	s.mu.RUnlock()
	return s.abortTaskCompletionForTurn(ctx, turnID, reason)
}

func (s *Session) abortTaskCompletionForTurn(ctx context.Context, turnID, reason string) TaskInterruptReceipt {
	if s == nil || strings.TrimSpace(turnID) == "" {
		return TaskInterruptReceipt{}
	}
	revoker, ok := s.model.(TaskCompletionRevoker)
	if !ok {
		return TaskInterruptReceipt{}
	}
	runtimeCtx := WithTaskTurnContext(s.taskRuntimeContext(ctx), turnID)
	return revoker.AbortTaskCompletion(runtimeCtx, reason)
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

// persistTaskState records interruption changes outside a model turn. It
// reloads the revision on conflict because a cancelling turn may still be
// flushing its last tool event when the user interrupts it.
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

// maxTaskCompletionContinuations prevents an uncooperative model from burning
// an unlimited turn budget by repeatedly ignoring the same GapPacket.
const maxTaskCompletionContinuations = 3

// streamTaskAwareAnswer keeps one durable user turn open while the controller
// feeds unmet completion conditions back to the model. Intermediate responses
// and all tool observations stay available to the next model call, but only a
// controller-approved final response is committed by askThread.
func (s *Session) streamTaskAwareAnswer(ctx context.Context, view []*schema.Message, onChunk func(string) error, emit EventEmitter) (*schema.Message, error) {
	currentView := append([]*schema.Message(nil), view...)
	runtime, taskAware := s.model.(TaskRuntime)
	for attempt := 0; ; attempt++ {
		// A run can become active mid-ReAct after task_plan returns. Suppress
		// assistant content while that is true: otherwise a model could show a
		// premature "done" sentence just before the controller rejects it.
		// Tool cards and reasoning remain live. Once a run was active in this
		// model response, its assistant text is released only after the stream
		// ends and the controller accepts the completion state.
		var heldChunks []string
		suppressTaskContent := taskAware && runtime.TaskCompletionGate(ctx).Active
		attemptEmit := emit
		if taskAware && emit != nil {
			attemptEmit = func(event TurnEvent) {
				if event.Kind == TurnEventChunk {
					if runtime.TaskCompletionGate(ctx).Active {
						suppressTaskContent = true
					}
					if suppressTaskContent {
						heldChunks = append(heldChunks, event.Chunk)
						return
					}
				}
				emit(event)
			}
		}
		attemptOnChunk := onChunk
		if taskAware && onChunk != nil {
			attemptOnChunk = func(chunk string) error {
				if runtime.TaskCompletionGate(ctx).Active {
					suppressTaskContent = true
				}
				if suppressTaskContent {
					// streamAnswer emits TurnEventChunk before invoking onChunk. When
					// an emitter exists it already retained this exact chunk above.
					if emit == nil {
						heldChunks = append(heldChunks, chunk)
					}
					return nil
				}
				return onChunk(chunk)
			}
		}

		answer, err := s.streamAnswer(ctx, currentView, attemptOnChunk, attemptEmit)
		if err != nil {
			return answer, err
		}

		if !taskAware {
			return answer, nil
		}
		gate := runtime.TaskCompletionGate(ctx)
		if !gate.Active || gate.Complete {
			// task_complete can grant permission before final text streams. The
			// held path only covers a premature attempt, so flush it once the
			// gate agrees this response is deliverable.
			for _, chunk := range heldChunks {
				if emit != nil {
					emit(TurnEvent{Kind: TurnEventChunk, Chunk: chunk})
				}
				if onChunk != nil {
					if err := onChunk(chunk); err != nil {
						return answer, fmt.Errorf("write response chunk: %w", err)
					}
				}
			}
			return answer, nil
		}

		gap := strings.TrimSpace(gate.Gap)
		if gap == "" {
			gap = "The active task is not complete. Inspect the task runtime state, repair every listed gap, run the required proofs, call task_complete, and do not provide a final delivery yet."
		}
		if emit != nil {
			gateCopy := gate
			emit(TurnEvent{Kind: TurnEventTaskGate, TaskGate: &gateCopy})
		}
		if attempt+1 >= maxTaskCompletionContinuations {
			summary := strings.TrimSpace(gate.Summary)
			if summary == "" {
				summary = "controller rejected completion"
			}
			return answer, fmt.Errorf("%w: %s", ErrTaskCompletionUnresolved, summary)
		}

		// Do not commit this synthetic user message. It is a same-run control
		// packet, not a new user turn or a replacement for the original request.
		currentView = append(currentView, answer, schema.UserMessage(gap))
	}
}
