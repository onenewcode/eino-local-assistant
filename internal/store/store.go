// Package store persists revisioned agent threads to local storage.
package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudwego/eino/schema"
)

// UsageStatus describes how completely a thread's cumulative usage can be
// trusted. Unavailable journals predate one-record-per-call accounting.
type UsageStatus string

const (
	UsageStatusExact       UsageStatus = "exact"
	UsageStatusIncomplete  UsageStatus = "incomplete"
	UsageStatusUnavailable UsageStatus = "unavailable"
)

// ThreadMeta is list/detail metadata for one durable agent thread.
type ThreadMeta struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Model     string    `json:"model,omitempty"`
	// ReasoningEffort is the requested opaque effort value, not provider
	// confirmation of the value actually applied to a request.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	MessageCount    int    `json:"message_count"`
	// ParentID, ForkBoundaryTurnID, and ForkSourceHash are populated only for
	// source-preserving children. ForkSourceHash is the source journal hash at
	// the boundary event.
	ParentID           string `json:"parent_id,omitempty"`
	ForkBoundaryTurnID string `json:"fork_boundary_turn_id,omitempty"`
	ForkSourceHash     string `json:"fork_source_hash,omitempty"`

	// Cumulative token/cost accounting, sourced only from usage.recorded events.
	PromptTokens     int              `json:"prompt_tokens,omitempty"`
	CompletionTokens int              `json:"completion_tokens,omitempty"`
	TotalTokens      int              `json:"total_tokens,omitempty"`
	CachedTokens     int              `json:"cached_tokens,omitempty"`
	ReasoningTokens  int              `json:"reasoning_tokens,omitempty"`
	ModelCallCount   int              `json:"model_call_count,omitempty"`
	CostUSD          float64          `json:"cost_usd,omitempty"`
	UsageStatus      UsageStatus      `json:"usage_status"`
	LastContext      *ContextSnapshot `json:"last_context,omitempty"`
}

// ForkResult identifies the newly published child and the exact source prefix
// it contains. LastTurnID is empty for a child forked before the first turn.
// ChildState is the journal-derived state before any later child mutation.
type ForkResult struct {
	SourceID   string      `json:"source_id"`
	ChildID    string      `json:"child_id"`
	LastTurnID string      `json:"last_turn_id"`
	SourceHash string      `json:"source_hash"`
	ChildState ThreadState `json:"child_state"`
}

// ThreadRepository is the complete durable v2 thread API.
type ThreadRepository interface {
	CreateThread(ctx context.Context, meta ThreadMeta, systemPrompt string) (ThreadState, error)
	DeleteThread(ctx context.Context, id string) error
	ListThreads(ctx context.Context) ([]ThreadMeta, error)
	LoadThreadMeta(ctx context.Context, id string) (ThreadMeta, error)
	LoadThread(ctx context.Context, id string) (ThreadState, error)
	LoadThreadTranscript(ctx context.Context, id string, limit int) (ThreadState, []*schema.Message, error)
	LoadCheckpoint(ctx context.Context, id, checkpointID string) (Checkpoint, error)
	LoadCheckpointLineage(ctx context.Context, id, checkpointID string) ([]Checkpoint, error)
	LoadCompactionUsage(ctx context.Context, id, operationID string) ([]ModelUsage, error)
	LoadTurnGroups(ctx context.Context, id string) ([]TurnGroup, error)
	LoadRecentMessages(ctx context.Context, id string, limit int) ([]*schema.Message, error)
	LoadMessagesPage(ctx context.Context, id string, before, limit int) ([]*schema.Message, bool, error)
	RecoverInterruptedTurn(ctx context.Context, id string, expectedRevision uint64, reason string) (ThreadState, bool, error)

	StartTurn(ctx context.Context, id string, expectedRevision uint64, input TurnStart) (ThreadState, error)
	ToolStarted(ctx context.Context, id string, expectedRevision uint64, input ToolStarted) (ThreadState, error)
	ToolCompleted(ctx context.Context, id string, expectedRevision uint64, input ToolCompleted) (ThreadState, error)
	RecordUsage(ctx context.Context, id string, input ModelUsage) (ThreadState, error)
	FinishTurn(ctx context.Context, id string, input TurnFinish) (ThreadState, error)
	CommitTurn(ctx context.Context, id string, expectedRevision uint64, input TurnCommit) (ThreadState, error)
	CancelTurn(ctx context.Context, id string, expectedRevision uint64, input TurnCancel) (ThreadState, error)
	FailTurn(ctx context.Context, id string, expectedRevision uint64, input TurnFailure) (ThreadState, error)
	SetThreadTitle(ctx context.Context, id string, expectedRevision uint64, title string) (ThreadState, error)
	PutArtifact(ctx context.Context, id string, input ArtifactInput) (ArtifactRef, error)
	ReadArtifact(ctx context.Context, id, artifactID string, offset, limit int64) (ArtifactRead, error)
	StartCompaction(ctx context.Context, id string, expectedRevision uint64, input CompactionStart) (ThreadState, error)
	CommitCheckpoint(ctx context.Context, id string, expectedRevision uint64, input CheckpointInput) (Checkpoint, ThreadState, error)
	RecordCompactionFailure(ctx context.Context, id string, expectedRevision uint64, input CompactionFailure) (ThreadState, error)
	FinishCompaction(ctx context.Context, id string, input CompactionFailure) (ThreadState, error)
	ResetIncompatibleCheckpoint(ctx context.Context, id string, expectedRevision uint64, input CheckpointSchemaReset) (ThreadState, error)
}

// ThreadModelRepository is an optional extension for idle model replacement.
// It is deliberately separate from ThreadRepository so older repositories and
// test fakes remain source-compatible while they adopt the new contract.
type ThreadModelRepository interface {
	SetThreadModel(ctx context.Context, id string, expectedRevision uint64, model string) (ThreadState, error)
}

// ThreadModelBindingRepository is an optional extension for atomically
// replacing a thread's model and requested reasoning effort. It is separate
// from ThreadModelRepository so older repositories and test fakes keep their
// source-compatible model-only contract.
type ThreadModelBindingRepository interface {
	// SetThreadModelBinding records one model.changed event containing the full
	// model selection tuple. An empty model retains the current model identity,
	// which allows an effort-only mutation; an empty reasoning effort clears the
	// requested value and restores provider-default semantics.
	SetThreadModelBinding(ctx context.Context, id string, expectedRevision uint64, model, reasoningEffort string) (ThreadState, error)
}

// ThreadForkRepository is the optional v1 source-preserving fork extension.
// It remains separate from ThreadRepository so existing runtime fakes do not
// need to implement fork semantics before they consume them.
type ThreadForkRepository interface {
	ForkThread(ctx context.Context, sourceID, childID, lastTurnID string) (ForkResult, error)
}

// ThreadForkBeforeFirstRepository is the optional source-preserving fork
// extension for publishing a child with an empty committed prefix. It remains
// separate so callers can opt into the explicit before-first boundary without
// treating ForkThread's empty lastTurnID as a sentinel.
type ThreadForkBeforeFirstRepository interface {
	ForkThreadBeforeFirstTurn(ctx context.Context, sourceID, childID string) (ForkResult, error)
}

// TaskStateRepository is an optional extension for runtimes that need a
// compact, recoverable execution-state projection alongside the normal thread
// ledger. It is separate from ThreadRepository so existing callers and test
// fakes do not gain an orchestration dependency.
type TaskStateRepository interface {
	LoadTaskState(ctx context.Context, id string) (json.RawMessage, error)
	UpdateTaskState(ctx context.Context, id string, expectedRevision uint64, turnID string, input TaskStateUpdate) (ThreadState, error)
}

// TaskStateRecovery is the ledger metadata needed to safely restore an opaque
// task-controller snapshot. The snapshot schema remains owned by the runtime;
// store only reports whether tool lifecycle events appeared after it.
type TaskStateRecovery struct {
	Snapshot                             json.RawMessage
	SnapshotTurnID                       string
	SnapshotTurnCommitted                bool
	PotentiallyMutatingToolAfterSnapshot bool
}

// TaskStateRecoveryRepository is an optional stronger recovery extension.
// Runtimes that only need an opaque snapshot can continue using
// TaskStateRepository unchanged.
type TaskStateRecoveryRepository interface {
	TaskStateRepository
	LoadTaskStateRecovery(ctx context.Context, id string) (TaskStateRecovery, error)
}

type threadAccessContextKey struct{}

// WithThreadAccess scopes a tool invocation to its active thread ledger. The
// context never grants access to another thread merely by changing an artifact
// ID supplied by the model.
func WithThreadAccess(ctx context.Context, repo ThreadRepository, threadID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, threadAccessContextKey{}, ThreadAccess{Repository: repo, ThreadID: threadID})
}

// ThreadAccess identifies the one ledger a tool may inspect during a turn.
type ThreadAccess struct {
	Repository ThreadRepository
	ThreadID   string
}

// ThreadAccessFromContext returns the ledger scope installed by chat.Session.
func ThreadAccessFromContext(ctx context.Context) (ThreadAccess, bool) {
	if ctx == nil {
		return ThreadAccess{}, false
	}
	access, ok := ctx.Value(threadAccessContextKey{}).(ThreadAccess)
	if !ok || access.Repository == nil || access.ThreadID == "" {
		return ThreadAccess{}, false
	}
	return access, true
}
