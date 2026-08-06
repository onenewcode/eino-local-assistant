package store

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cloudwego/eino/schema"
)

const (
	// SessionJournalFormatVersion is the on-disk format used by ThreadStore. Version 5
	// stores each active session as one date-partitioned JSONL file.
	SessionJournalFormatVersion = 5

	// MaxArtifactBytes bounds full payload retention for one artifact. Larger
	// inputs are retained as digest plus head/tail metadata.
	MaxArtifactBytes int64 = 4 << 20
	// MaxThreadArtifactBytes bounds aggregate payload retention for one thread;
	// inputs beyond it still produce truncated metadata artifacts.
	MaxThreadArtifactBytes int64 = 64 << 20
)

var (
	// ErrRevisionConflict means a writer used a stale thread revision.
	ErrRevisionConflict = errors.New("thread revision conflict")
	// ErrThreadLocked means another process currently owns the thread lock.
	ErrThreadLocked = errors.New("thread is locked by another writer")
	// ErrJournalCorrupt means a non-tail journal record failed integrity checks.
	ErrJournalCorrupt = errors.New("thread journal is corrupt")
	// ErrInvalidThreadLifecycle means an event violates the turn/tool state
	// machine even when its hash and revision chain are otherwise valid.
	ErrInvalidThreadLifecycle = errors.New("invalid thread lifecycle")
	// ErrUsageRecordConflict means one model-call ID was reused with different
	// immutable accounting data.
	ErrUsageRecordConflict = errors.New("model usage record conflict")
	// ErrCompactionOperationNotPending means an identity-bound compaction was
	// terminalized or replaced before a late result could reconcile it.
	ErrCompactionOperationNotPending = errors.New("compaction operation is not pending")
	// ErrForkActiveTurn means a source thread still owns an unfinished turn.
	ErrForkActiveTurn = errors.New("thread fork source has an active turn")
	// ErrForkPendingCompaction means a source thread has an unfinished
	// compaction operation whose derived state is not safe to fork.
	ErrForkPendingCompaction = errors.New("thread fork source has a pending compaction")
	// ErrForkNoCommittedTurn means the source has no complete committed prefix.
	ErrForkNoCommittedTurn = errors.New("thread fork source has no committed turn")
	// ErrForkInvalidBoundary means the requested turn is not a complete commit.
	ErrForkInvalidBoundary = errors.New("invalid thread fork boundary")
	// ErrForkUnsupportedState means v1 cannot prove a derived state is safe to
	// carry into the child ledger.
	ErrForkUnsupportedState = errors.New("thread fork source has unsupported derived state")
	// ErrForkSourceChanged means the source was not stable for the full fork.
	ErrForkSourceChanged = errors.New("thread fork source changed during fork")
	// ErrForkDestinationExists means the requested child ID is already present.
	ErrForkDestinationExists = errors.New("thread fork destination already exists")
	// ErrModelChangeActiveTurn means a model identity cannot change while a
	// durable turn still owns the thread.
	ErrModelChangeActiveTurn = errors.New("cannot change thread model while a turn is active")
	// ErrModelChangePendingCompaction means a model identity cannot change while
	// a compaction operation may still reconcile provider usage or a checkpoint.
	ErrModelChangePendingCompaction = errors.New("cannot change thread model while compaction is pending")
)

// EventKind is a durable journal event category.
type EventKind string

const (
	EventThreadCreated EventKind = "thread.created"
	EventTurnStarted   EventKind = "turn.started"
	EventToolStarted   EventKind = "tool.started"
	EventToolCompleted EventKind = "tool.completed"
	EventTurnCommitted EventKind = "turn.committed"
	EventTurnCancelled EventKind = "turn.cancelled"
	EventTurnFailed    EventKind = "turn.failed"
	EventTitleChanged  EventKind = "title.changed"
	EventModelChanged  EventKind = "model.changed"
	EventUsageRecorded EventKind = "usage.recorded"
	// EventTaskStateUpdated records the latest recoverable autonomous-task
	// controller snapshot. The immutable tool and turn events remain the source
	// of evidence; this is the compact execution-state projection used on resume.
	EventTaskStateUpdated EventKind = "task.state.updated"
	// EventContextCompactionStarted records a durable operation boundary before
	// a compactor provider call can incur usage.
	EventContextCompactionStarted EventKind = "context.compaction.started"
	EventContextCompacted         EventKind = "context.compacted"
	// EventContextCompactionFailed records an unsuccessful compaction without
	// changing the active checkpoint or raw transcript.
	EventContextCompactionFailed EventKind = "context.compaction.failed"
	// EventContextCheckpointReset drops an incompatible active checkpoint while
	// retaining its immutable journal/file record and all raw sources.
	EventContextCheckpointReset EventKind = "context.checkpoint.reset"
)

// ThreadEvent is one hash-chained entry in a session JSONL ledger. Fields marked with
// json:"-" are derived from the compact on-disk envelope during replay.
type ThreadEvent struct {
	Version          int             `json:"format_version"`
	Sequence         uint64          `json:"seq"`
	ID               string          `json:"event_id"`
	ThreadID         string          `json:"-"`
	Timestamp        time.Time       `json:"timestamp"`
	Kind             EventKind       `json:"kind"`
	TurnID           string          `json:"turn_id,omitempty"`
	CorrelationID    string          `json:"-"`
	ExpectedRevision uint64          `json:"-"`
	Revision         uint64          `json:"-"`
	Payload          json.RawMessage `json:"payload"`
	PayloadHash      string          `json:"-"`
	PreviousHash     string          `json:"previous_hash,omitempty"`
	Hash             string          `json:"hash"`
}

// ThreadState is the materialized, recoverable state of a thread.
type ThreadState struct {
	FormatVersion             int                  `json:"format_version"`
	ID                        string               `json:"id"`
	Revision                  uint64               `json:"revision"`
	HeadSequence              uint64               `json:"head_sequence"`
	LastHash                  string               `json:"last_hash,omitempty"`
	ActiveCheckpointID        string               `json:"active_checkpoint_id,omitempty"`
	SystemPrompt              string               `json:"system_prompt,omitempty"`
	AutoCompactionPaused      bool                 `json:"auto_compaction_paused,omitempty"`
	AutoCompactionPauseReason string               `json:"auto_compaction_pause_reason,omitempty"`
	LowGainStreak             uint64               `json:"low_gain_streak,omitempty"`
	PendingCompaction         *CompactionOperation `json:"pending_compaction,omitempty"`
	LastCompaction            *CompactionOutcome   `json:"last_compaction,omitempty"`
	TaskState                 json.RawMessage      `json:"task_state,omitempty"`
	CreatedAt                 time.Time            `json:"created_at"`
	UpdatedAt                 time.Time            `json:"updated_at"`
	Meta                      ThreadMeta           `json:"meta"`

	// recordedUsage is rebuilt from the journal and prevents duplicate call IDs
	// from being counted twice during replay.
	recordedUsage map[string]ModelUsage
	// recordedCheckpointIDs is rebuilt from context.compacted events and rejects
	// duplicate IDs within the JSONL ledger.
	recordedCheckpointIDs map[string]struct{}
	// recordedCompactionOperationIDs is rebuilt from compaction lifecycle events
	// so a later transaction cannot reuse accounting correlation data.
	recordedCompactionOperationIDs map[string]struct{}
}

// ModelChange is the full durable model selection payload for a model.changed
// event. An empty Model means the caller selected the provider default
// identity; effort-only API calls resolve their retained model before writing
// this payload.
type ModelChange struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// UsageOperation identifies the product operation that made a model request.
type UsageOperation string

const (
	// UsageOperationAgent is one model call made while answering an agent turn.
	UsageOperationAgent UsageOperation = "agent"
	// UsageOperationCompaction is one model call used to summarize context.
	UsageOperationCompaction UsageOperation = "compaction"
)

// ContextSnapshot is the last exact primary-model context measurement. A nil
// ThreadMeta.LastContext means that no trustworthy measurement is available.
type ContextSnapshot struct {
	PromptTokens int `json:"prompt_tokens"`
	BudgetTokens int `json:"budget_tokens,omitempty"`
}

// CompactionOutcomeStatus classifies the last completed compaction lifecycle
// transaction without pretending a failed attempt changed the active view.
type CompactionOutcomeStatus string

const (
	CompactionOutcomeSucceeded       CompactionOutcomeStatus = "succeeded"
	CompactionOutcomeFailed          CompactionOutcomeStatus = "failed"
	CompactionOutcomeCancelled       CompactionOutcomeStatus = "cancelled"
	CompactionOutcomeCheckpointReset CompactionOutcomeStatus = "checkpoint_reset"
)

// CompactionFailureReasonLowGain marks a syntactically valid checkpoint that
// did not free enough capacity. Automatic low-gain failures may retry until
// the configured streak limit is reached.
const CompactionFailureReasonLowGain = "low_gain"

// CompactionFailureReasonStale marks a cancelled automatic operation whose
// frozen candidate lost a CAS race. It closes the durable operation without
// latching automatic compaction as failed.
const CompactionFailureReasonStale = "stale"

// CompactionOutcome is a durable, user-visible summary of one compaction
// result. Detailed model usage remains in usage.recorded events linked by
// OperationID.
type CompactionOutcome struct {
	Status       CompactionOutcomeStatus `json:"status"`
	OperationID  string                  `json:"operation_id,omitempty"`
	CheckpointID string                  `json:"checkpoint_id,omitempty"`
	Automatic    bool                    `json:"automatic,omitempty"`
	Reason       string                  `json:"reason,omitempty"`
	At           time.Time               `json:"at"`
}

// CompactionOperation identifies a started compaction that has not yet reached
// a durable success or failure event. Resume requires explicit recovery before
// another compaction can spend provider tokens on the same thread.
type CompactionOperation struct {
	OperationID string    `json:"operation_id"`
	Automatic   bool      `json:"automatic,omitempty"`
	StartedAt   time.Time `json:"started_at"`
}

// ModelUsage is the durable accounting record for one completed model API
// call. CallID is unique within a thread and makes RecordUsage idempotent.
// Token values are trusted only when HasProviderUsage is true.
type ModelUsage struct {
	CallID    string         `json:"call_id"`
	TurnID    string         `json:"turn_id,omitempty"`
	Operation UsageOperation `json:"operation"`
	// OperationID correlates compaction usage calls with one compaction
	// transaction. Agent usage deliberately leaves it empty.
	OperationID         string  `json:"operation_id,omitempty"`
	HasProviderUsage    bool    `json:"has_provider_usage"`
	PromptTokens        int     `json:"prompt_tokens,omitempty"`
	CompletionTokens    int     `json:"completion_tokens,omitempty"`
	TotalTokens         int     `json:"total_tokens,omitempty"`
	CachedTokens        int     `json:"cached_tokens,omitempty"`
	ReasoningTokens     int     `json:"reasoning_tokens,omitempty"`
	ContextBudgetTokens int     `json:"context_budget_tokens,omitempty"`
	CostUSD             float64 `json:"cost_usd,omitempty"`
}

// TurnStart records an input accepted for one agent turn.
type TurnStart struct {
	TurnID string `json:"turn_id"`
	Input  string `json:"input"`
}

// ToolStarted records a tool call before it executes.
type ToolStarted struct {
	TurnID     string `json:"turn_id"`
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	// Input is used only while the current turn is running. Raw tool
	// arguments are intentionally not durable telemetry; replay can rebuild a
	// portable empty object for the tool-call/result pair.
	Input string `json:"-"`
}

// ToolCompleted records the terminal result of a tool call. Large output can
// be stored once as an artifact and referenced here.
type ToolCompleted struct {
	TurnID     string       `json:"turn_id"`
	ToolCallID string       `json:"tool_call_id"`
	ToolName   string       `json:"tool_name"`
	Output     string       `json:"output,omitempty"`
	Artifact   *ArtifactRef `json:"artifact,omitempty"`
}

// TurnCommit atomically commits all visible messages for a completed turn.
type TurnCommit struct {
	TurnID   string            `json:"turn_id"`
	Messages []*schema.Message `json:"messages"`
}

// TurnCancel records a cancelled, uncommitted turn.
type TurnCancel struct {
	TurnID string `json:"turn_id"`
	Reason string `json:"reason,omitempty"`
}

// TurnFailure records a failed, uncommitted turn.
type TurnFailure struct {
	TurnID string `json:"turn_id"`
	Error  string `json:"error"`
}

// TurnFinish terminally closes one known active turn without relying on a
// caller's stale revision. It is used after a model call has already ended.
type TurnFinish struct {
	TurnID    string
	Cancelled bool
	Reason    string
}

// ArtifactInput is immutable content addressed by its SHA-256 digest.
type ArtifactInput struct {
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"-"`
}

// ArtifactRef is a stable reference stored in events and checkpoints.
type ArtifactRef struct {
	ID           string `json:"id"`
	SHA256       string `json:"sha256"`
	Digest       string `json:"digest"`
	Kind         string `json:"kind"`
	MediaType    string `json:"media_type"`
	Size         int64  `json:"size"`
	OriginalSize int64  `json:"original_size"`
	StoredSize   int64  `json:"stored_size"`
	Truncated    bool   `json:"truncated,omitempty"`
	Head         []byte `json:"head,omitempty"`
	Tail         []byte `json:"tail,omitempty"`
	// Data is retained with non-truncated evidence in the same JSONL event.
	Data []byte `json:"data,omitempty"`
}

// ArtifactRead is one bounded range from a retained artifact. For a truncated
// Ref, Offset and HasMore address the virtual head/omission-marker/tail
// excerpt; Ref.Truncated means bytes outside that excerpt are unavailable.
type ArtifactRead struct {
	Ref     ArtifactRef `json:"ref"`
	Offset  int64       `json:"offset"`
	Data    []byte      `json:"data"`
	HasMore bool        `json:"has_more"`
}

// MessageRef identifies one message in a prior event for context projections.
type MessageRef struct {
	EventID      string `json:"event_id"`
	MessageIndex int    `json:"message_index"`
}

// CheckpointInput contains one immutable context projection. SourceEventIDs
// describe only the direct raw events newly covered by this checkpoint;
// ParentID links older coverage without copying it into each new checkpoint.
// Payload carries the bounded model-facing structured handoff.
type CheckpointInput struct {
	ID             string          `json:"id,omitempty"`
	ParentID       string          `json:"parent_id,omitempty"`
	Kind           string          `json:"kind,omitempty"`
	WindowNumber   uint64          `json:"window_number,omitempty"`
	MessageRefs    []MessageRef    `json:"message_refs,omitempty"`
	Summary        *ArtifactRef    `json:"summary,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	SourceEventIDs []string        `json:"source_event_ids,omitempty"`
	SourceHash     string          `json:"source_hash,omitempty"`
	Focus          string          `json:"focus,omitempty"`
	BeforeTokens   int             `json:"before_tokens,omitempty"`
	AfterTokens    int             `json:"after_tokens,omitempty"`
	Automatic      bool            `json:"automatic,omitempty"`
	// LowGain is ignored on write. Kept only so older callers/fixtures compile;
	// anti-thrash streak is owned exclusively by compaction failure events.
	LowGain         bool   `json:"low_gain,omitempty"`
	AutoPaused      bool   `json:"auto_paused,omitempty"`
	AutoPauseReason string `json:"auto_pause_reason,omitempty"`
	OperationID     string `json:"operation_id,omitempty"`
}

// Checkpoint is an immutable checkpoint persisted in its corresponding
// context.compacted journal event. SourceEventIDs are direct
// coverage only; callers resolve ParentID lineage to recover the full source
// manifest without placing it in the model-visible payload.
type Checkpoint struct {
	ID             string          `json:"id"`
	ThreadID       string          `json:"thread_id"`
	Revision       uint64          `json:"revision"`
	Sequence       uint64          `json:"sequence"`
	CreatedAt      time.Time       `json:"created_at"`
	ParentID       string          `json:"parent_id,omitempty"`
	Kind           string          `json:"kind,omitempty"`
	WindowNumber   uint64          `json:"window_number,omitempty"`
	MessageRefs    []MessageRef    `json:"message_refs,omitempty"`
	Summary        *ArtifactRef    `json:"summary,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	SourceEventIDs []string        `json:"source_event_ids,omitempty"`
	SourceHash     string          `json:"source_hash,omitempty"`
	Focus          string          `json:"focus,omitempty"`
	BeforeTokens   int             `json:"before_tokens,omitempty"`
	AfterTokens    int             `json:"after_tokens,omitempty"`
	Automatic      bool            `json:"automatic,omitempty"`
	// LowGain is historical journal metadata only. New checkpoints always write
	// false; successful installs always clear LowGainStreak.
	LowGain         bool   `json:"low_gain,omitempty"`
	AutoPaused      bool   `json:"auto_paused,omitempty"`
	AutoPauseReason string `json:"auto_pause_reason,omitempty"`
	OperationID     string `json:"operation_id,omitempty"`
	Hash            string `json:"hash"`
}

// DefaultMaxLowGainAttempts is consecutive automatic low-gain failures before
// auto-compaction pauses when MaxLowGainAttempts is unset or zero.
const DefaultMaxLowGainAttempts = 2

// CompactionFailure appends an unsuccessful compaction result. It is separate
// from a checkpoint commit so failed model output can never alter the active
// model-visible work view.
//
// For automatic failures, the store materializes AutoPaused / AutoPauseReason
// and ResultingLowGainStreak under the write lock from the current
// LowGainStreak and MaxLowGainAttempts. Callers should not precompute those
// fields for automatic outcomes; only manual failures preserve an explicit
// AutoPaused snapshot.
type CompactionFailure struct {
	OperationID     string `json:"operation_id,omitempty"`
	Automatic       bool   `json:"automatic,omitempty"`
	Cancelled       bool   `json:"cancelled,omitempty"`
	Reason          string `json:"reason,omitempty"`
	AutoPaused      bool   `json:"auto_paused,omitempty"`
	AutoPauseReason string `json:"auto_pause_reason,omitempty"`
	// ResultingLowGainStreak is the absolute projected streak after this
	// automatic non-stale failure (previous+1 for low_gain, 0 for hard fails).
	// Nil means leave streak unchanged (stale/manual) or apply a legacy
	// reason-based fallback when replaying older journals.
	ResultingLowGainStreak *uint64 `json:"resulting_low_gain_streak,omitempty"`
	// MaxLowGainAttempts is write-time policy only. It is not journaled; the
	// resulting AutoPaused flag and ResultingLowGainStreak are. Zero means
	// DefaultMaxLowGainAttempts.
	MaxLowGainAttempts int `json:"-"`
}

// CompactionStart opens a durable compaction operation before provider usage
// is recorded. OperationID must match its terminal checkpoint or failure.
type CompactionStart struct {
	OperationID string `json:"operation_id"`
	Automatic   bool   `json:"automatic,omitempty"`
}

// CheckpointSchemaReset records an intentional active-pointer reset when a
// session encounters an incompatible checkpoint schema on resume.
type CheckpointSchemaReset struct {
	OperationID     string `json:"operation_id,omitempty"`
	CheckpointID    string `json:"checkpoint_id"`
	Reason          string `json:"reason"`
	AutoPaused      bool   `json:"auto_paused,omitempty"`
	AutoPauseReason string `json:"auto_pause_reason,omitempty"`
}

// ToolGroup associates tool start and completion records reconstructed from a
// thread journal.
type ToolGroup struct {
	ToolCallID string         `json:"tool_call_id"`
	Started    *ToolStarted   `json:"started,omitempty"`
	Completed  *ToolCompleted `json:"completed,omitempty"`
	EventIDs   []string       `json:"event_ids"`
}

// TurnGroup reconstructs the durable lifecycle for a single agent turn.
type TurnGroup struct {
	TurnID         string       `json:"turn_id"`
	Started        *TurnStart   `json:"started,omitempty"`
	Tools          []ToolGroup  `json:"tools,omitempty"`
	Usages         []ModelUsage `json:"usages,omitempty"`
	Committed      *TurnCommit  `json:"committed,omitempty"`
	Cancelled      *TurnCancel  `json:"cancelled,omitempty"`
	Failed         *TurnFailure `json:"failed,omitempty"`
	SourceEventIDs []string     `json:"source_event_ids"`
}
