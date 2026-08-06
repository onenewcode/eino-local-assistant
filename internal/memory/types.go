// Package memory provides project-scoped semantic memory (not session resume).
package memory

import "time"

const (
	// TrustUser is an explicit user-confirmed memory.
	TrustUser Trust = "user"
	// TrustCandidate is auto-extracted and unverified.
	TrustCandidate Trust = "candidate"

	// StatusActive is visible to list/search/summary.
	StatusActive Status = "active"
	// StatusSuperseded was replaced by a newer same-key write.
	StatusSuperseded Status = "superseded"
	// StatusDeleted is a tombstone.
	StatusDeleted Status = "deleted"
)

// Trust grades how a memory may be presented in prompts.
type Trust string

// Status is the lifecycle state of one memory row.
type Status string

// Entry is one durable memory claim.
type Entry struct {
	ID              string    `json:"id"`
	Key             string    `json:"key"`
	Claim           string    `json:"claim"`
	Trust           Trust     `json:"trust"`
	Status          Status    `json:"status"`
	Version         int       `json:"version"`
	SourceEventIDs  []string  `json:"source_event_ids,omitempty"`
	SourceThreadID  string    `json:"source_thread_id,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Supersedes      string    `json:"supersedes,omitempty"`
	ExtractedFromID string    `json:"extracted_from_id,omitempty"`
}

// Meta is a compatibility-shaped snapshot of database settings and extraction state.
type Meta struct {
	SchemaVersion    int        `json:"schema_version"`
	WorkspaceRoot    string     `json:"workspace_root,omitempty"`
	UseEnabled       bool       `json:"use_enabled"`
	GenerateEnabled  bool       `json:"generate_enabled"`
	ResetGeneration  uint64     `json:"reset_generation"`
	LastConsolidate  *time.Time `json:"last_consolidate_at,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	ClaimedThreads   []string   `json:"claimed_threads,omitempty"`
	ProcessedThreads []string   `json:"processed_threads,omitempty"`
}

// SummaryBundle is the bounded text injected into the system prompt.
type SummaryBundle struct {
	Text      string
	Tokens    int
	Truncated bool
	UserCount int
	CandCount int
}

// StatusReport is a human-readable snapshot for /memory status.
type StatusReport struct {
	Root               string
	DatabasePath       string
	SchemaVersion      int
	UseEnabled         bool
	GenerateEnabled    bool
	UserActive         int
	CandidateActive    int
	LastConsolidate    *time.Time
	LastError          string
	RunningExtractions int
	FailedExtractions  int
}
