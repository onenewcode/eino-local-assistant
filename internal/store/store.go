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
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Model        string    `json:"model,omitempty"`
	MessageCount int       `json:"message_count"`

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
