package store

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/cloudwego/eino/schema"
)

const (
	// ThreadFormatVersion is the on-disk format used by ThreadStore.
	ThreadFormatVersion = 2

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
)

// EventKind is a durable journal event category.
type EventKind string

const (
	EventThreadCreated    EventKind = "thread.created"
	EventTurnStarted      EventKind = "turn.started"
	EventToolStarted      EventKind = "tool.started"
	EventToolCompleted    EventKind = "tool.completed"
	EventTurnCommitted    EventKind = "turn.committed"
	EventTurnCancelled    EventKind = "turn.cancelled"
	EventTurnFailed       EventKind = "turn.failed"
	EventTitleChanged     EventKind = "title.changed"
	EventContextCompacted EventKind = "context.compacted"
)

// ThreadEvent is one hash-chained entry in journal.jsonl. Payload is preserved
// verbatim so future schema additions do not require rewriting old journals.
type ThreadEvent struct {
	Version          int             `json:"format_version"`
	Sequence         uint64          `json:"seq"`
	ID               string          `json:"event_id"`
	ThreadID         string          `json:"thread_id"`
	Timestamp        time.Time       `json:"timestamp"`
	Kind             EventKind       `json:"kind"`
	TurnID           string          `json:"turn_id"`
	CorrelationID    string          `json:"correlation_id"`
	ExpectedRevision uint64          `json:"expected_revision"`
	Revision         uint64          `json:"revision"`
	Payload          json.RawMessage `json:"payload"`
	PayloadHash      string          `json:"payload_hash"`
	PreviousHash     string          `json:"previous_hash"`
	Hash             string          `json:"hash"`
}

// ThreadState is the materialized, recoverable state of a thread.
type ThreadState struct {
	FormatVersion        int        `json:"format_version"`
	ID                   string     `json:"id"`
	Revision             uint64     `json:"revision"`
	HeadSequence         uint64     `json:"head_sequence"`
	LastHash             string     `json:"last_hash,omitempty"`
	ActiveCheckpointID   string     `json:"active_checkpoint_id,omitempty"`
	SystemPrompt         string     `json:"system_prompt,omitempty"`
	AutoCompactionPaused bool       `json:"auto_compaction_paused,omitempty"`
	LowGainStreak        uint64     `json:"low_gain_streak,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	Meta                 ThreadMeta `json:"meta"`
}

// UsageDelta is atomically recorded with a committed turn.
type UsageDelta struct {
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	Estimated        bool    `json:"estimated,omitempty"`
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
	Input      string `json:"input,omitempty"`
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
	Usage    UsageDelta        `json:"usage,omitempty"`
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
	LowGain        bool            `json:"low_gain,omitempty"`
	AutoPaused     bool            `json:"auto_paused,omitempty"`
}

// Checkpoint is an immutable checkpoint persisted both in a file and in the
// corresponding context.compacted journal event. SourceEventIDs are direct
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
	LowGain        bool            `json:"low_gain,omitempty"`
	AutoPaused     bool            `json:"auto_paused,omitempty"`
	Hash           string          `json:"hash"`
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
	Committed      *TurnCommit  `json:"committed,omitempty"`
	Cancelled      *TurnCancel  `json:"cancelled,omitempty"`
	Failed         *TurnFailure `json:"failed,omitempty"`
	SourceEventIDs []string     `json:"source_event_ids"`
}
