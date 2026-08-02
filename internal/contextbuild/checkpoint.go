package contextbuild

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

// CheckpointSchemaVersion is bumped only when persisted checkpoint JSON becomes
// incompatible. Strict parsing prevents a model from silently inventing fields.
const CheckpointSchemaVersion = 1

// SourceRange identifies the ordered evidence anchors for a checkpoint. Full
// source coverage is retained in the durable checkpoint lineage; keeping only
// bounded anchors here prevents the model-visible handoff from growing forever.
type SourceRange struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	ContentHash string   `json:"content_hash"`
	EventIDs    []string `json:"event_ids"`
}

// CheckpointItem is a sourced claim or next action.
type CheckpointItem struct {
	Text           string     `json:"text"`
	SourceEventIDs []string   `json:"source_event_ids"`
	Confidence     Confidence `json:"confidence"`
}

// CheckpointDecision records both the decision and why it was chosen.
type CheckpointDecision struct {
	Decision       string     `json:"decision"`
	Reason         string     `json:"reason"`
	SourceEventIDs []string   `json:"source_event_ids"`
	Confidence     Confidence `json:"confidence"`
}

// CheckpointAttempt preserves failed and successful exploration so the next
// agent does not repeat a disproven approach.
type CheckpointAttempt struct {
	Text           string     `json:"text"`
	Result         string     `json:"result"`
	SourceEventIDs []string   `json:"source_event_ids"`
	Confidence     Confidence `json:"confidence"`
}

// CheckpointFileArtifact references a changed file, tool artifact, log, or
// other evidence that can be re-read outside the compact prompt.
type CheckpointFileArtifact struct {
	Ref            string     `json:"ref"`
	Description    string     `json:"description"`
	SourceEventIDs []string   `json:"source_event_ids"`
	Confidence     Confidence `json:"confidence"`
}

// Checkpoint is a structured, derived handoff state. It is never a substitute
// for the source event stream: every material claim points to bounded source
// event anchors, while the ledger lineage retains the complete manifest.
type Checkpoint struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	Focus         string `json:"focus,omitempty"`

	SourceRange    SourceRange `json:"source_range"`
	SourceEventIDs []string    `json:"source_event_ids"`
	SourceHash     string      `json:"source_hash"`

	TaskGoal           string                   `json:"task_goal"`
	Constraints        []CheckpointItem         `json:"constraints"`
	ConfirmedFacts     []CheckpointItem         `json:"confirmed_facts"`
	Decisions          []CheckpointDecision     `json:"decisions"`
	AttemptsAndResults []CheckpointAttempt      `json:"attempts_and_results"`
	FilesOrArtifacts   []CheckpointFileArtifact `json:"files_or_artifacts"`
	OpenQuestions      []CheckpointItem         `json:"open_questions"`
	NextActions        []CheckpointItem         `json:"next_actions"`
}

// EstimatedTokens measures the exact derived representation that is placed in
// a prompt by ContextPlanner.
func (c Checkpoint) EstimatedTokens() int {
	// ContextPlanner installs checkpoints as one system message, so include the
	// same role framing in the summary cap rather than undercounting by four.
	return usage.EstimateMessages([]*schema.Message{schema.SystemMessage(c.PromptText())})
}

// PromptText makes the derived status explicit so models do not confuse it
// with raw conversation. JSON keeps field boundaries resilient to formatting.
func (c Checkpoint) PromptText() string {
	data, err := json.Marshal(c)
	if err != nil {
		return "[invalid structured checkpoint]"
	}
	return "Structured checkpoint (derived from source events; re-read sources when uncertain):\n" + string(data)
}

// ParseCheckpointJSON accepts exactly one strict JSON object and validates all
// required content before it is ever installed into a session.
func ParseCheckpointJSON(data []byte) (Checkpoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var checkpoint Checkpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("decode checkpoint: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Checkpoint{}, errors.New("checkpoint must contain one JSON object")
		}
		return Checkpoint{}, fmt.Errorf("decode checkpoint tail: %w", err)
	}
	if err := checkpoint.Validate(); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

// Validate checks schema shape, provenance, confidence labels, and required
// handoff sections. Empty sections must be represented by an explicit sourced
// unknown item rather than silently omitted.
func (c Checkpoint) Validate() error {
	if c.SchemaVersion != CheckpointSchemaVersion {
		return fmt.Errorf("unsupported checkpoint schema version %d", c.SchemaVersion)
	}
	if strings.TrimSpace(c.TaskGoal) == "" {
		return errors.New("checkpoint task_goal is required")
	}
	ids := uniqueNonEmpty(c.SourceEventIDs)
	if len(ids) == 0 || len(ids) != len(c.SourceEventIDs) {
		return errors.New("checkpoint source_event_ids must be non-empty and unique")
	}
	if !isSHA256(c.SourceHash) {
		return errors.New("checkpoint source_hash must be a sha256 hex digest")
	}
	if strings.TrimSpace(c.SourceRange.From) == "" || strings.TrimSpace(c.SourceRange.To) == "" {
		return errors.New("checkpoint source_range from and to are required")
	}
	if !isSHA256(c.SourceRange.ContentHash) {
		return errors.New("checkpoint source_range content_hash must be a sha256 hex digest")
	}
	if c.SourceRange.ContentHash != c.SourceHash {
		return errors.New("checkpoint source_range content_hash must equal source_hash")
	}
	rangeIDs := uniqueNonEmpty(c.SourceRange.EventIDs)
	if len(rangeIDs) != len(c.SourceRange.EventIDs) || !sameStrings(ids, rangeIDs) {
		return errors.New("checkpoint source_range event_ids must match source_event_ids")
	}
	if c.SourceRange.From != ids[0] || c.SourceRange.To != ids[len(ids)-1] {
		return errors.New("checkpoint source_range boundaries must match source_event_ids")
	}

	return c.validateClaims(nil)
}

func (c Checkpoint) validateClaims(allowed map[string]struct{}) error {
	if err := validateItems("constraints", c.Constraints, allowed); err != nil {
		return err
	}
	if err := validateItems("confirmed_facts", c.ConfirmedFacts, allowed); err != nil {
		return err
	}
	if err := validateDecisions(c.Decisions, allowed); err != nil {
		return err
	}
	if err := validateAttempts(c.AttemptsAndResults, allowed); err != nil {
		return err
	}
	if err := validateFiles(c.FilesOrArtifacts, allowed); err != nil {
		return err
	}
	if err := validateItems("open_questions", c.OpenQuestions, allowed); err != nil {
		return err
	}
	return validateItems("next_actions", c.NextActions, allowed)
}

// ValidateForSource verifies that a checkpoint describes exactly the expected
// source identity, including a recomputed content hash where source groups are
// available.
func (c Checkpoint) ValidateForSource(eventIDs []string, sourceHash string, allowedEventIDs ...[]string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	eventIDs = uniqueNonEmpty(eventIDs)
	if len(eventIDs) == 0 {
		return errors.New("expected source event ids are required")
	}
	if !sameStrings(c.SourceEventIDs, eventIDs) {
		return errors.New("checkpoint source_event_ids do not match compaction source")
	}
	if c.SourceHash != sourceHash {
		return errors.New("checkpoint source_hash does not match compaction source")
	}
	allowed := eventIDs
	if len(allowedEventIDs) > 0 && len(allowedEventIDs[0]) > 0 {
		allowed = uniqueNonEmpty(allowedEventIDs[0])
	}
	if len(allowed) == 0 {
		return errors.New("checkpoint claim source scope is required")
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	return c.validateClaims(allowedSet)
}

// HashTurnGroups returns a stable SHA-256 hash of ordered group IDs, source
// event IDs, messages, and artifact metadata. It is suitable for checkpoint
// provenance, not for cryptographic authentication of untrusted storage.
func HashTurnGroups(groups []TurnGroup) (string, error) {
	hash := sha256.New()
	for _, group := range groups {
		if err := group.validate(); err != nil {
			return "", err
		}
		if _, err := hash.Write([]byte(group.ID + "\n")); err != nil {
			return "", err
		}
		for _, id := range group.EffectiveSourceEventIDs() {
			if _, err := hash.Write([]byte(id + "\n")); err != nil {
				return "", err
			}
		}
		for _, message := range group.Messages {
			encoded, err := json.Marshal(message)
			if err != nil {
				return "", fmt.Errorf("marshal source message in %q: %w", group.ID, err)
			}
			if _, err := hash.Write(encoded); err != nil {
				return "", err
			}
			if _, err := hash.Write([]byte("\n")); err != nil {
				return "", err
			}
		}
		for _, artifact := range group.Artifacts {
			encoded, err := json.Marshal(artifact)
			if err != nil {
				return "", fmt.Errorf("marshal source artifact in %q: %w", group.ID, err)
			}
			if _, err := hash.Write(encoded); err != nil {
				return "", err
			}
			if _, err := hash.Write([]byte("\n")); err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// HashSourceEventIDs is used by deterministic fallback when raw source content
// is unavailable but event identity must still be preserved.
func HashSourceEventIDs(eventIDs []string) string {
	hash := sha256.New()
	for _, id := range uniqueNonEmpty(eventIDs) {
		_, _ = hash.Write([]byte(id))
		_, _ = hash.Write([]byte("\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validateItems(name string, items []CheckpointItem, allowed map[string]struct{}) error {
	if len(items) == 0 {
		return fmt.Errorf("checkpoint %s is required", name)
	}
	for i, item := range items {
		if strings.TrimSpace(item.Text) == "" {
			return fmt.Errorf("checkpoint %s[%d] text is required", name, i)
		}
		if err := validateReferences(item.SourceEventIDs, item.Confidence, allowed); err != nil {
			return fmt.Errorf("checkpoint %s[%d]: %w", name, i, err)
		}
	}
	return nil
}

func validateDecisions(items []CheckpointDecision, allowed map[string]struct{}) error {
	if len(items) == 0 {
		return errors.New("checkpoint decisions is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Decision) == "" || strings.TrimSpace(item.Reason) == "" {
			return fmt.Errorf("checkpoint decisions[%d] decision and reason are required", i)
		}
		if err := validateReferences(item.SourceEventIDs, item.Confidence, allowed); err != nil {
			return fmt.Errorf("checkpoint decisions[%d]: %w", i, err)
		}
	}
	return nil
}

func validateAttempts(items []CheckpointAttempt, allowed map[string]struct{}) error {
	if len(items) == 0 {
		return errors.New("checkpoint attempts_and_results is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Text) == "" || strings.TrimSpace(item.Result) == "" {
			return fmt.Errorf("checkpoint attempts_and_results[%d] text and result are required", i)
		}
		if err := validateReferences(item.SourceEventIDs, item.Confidence, allowed); err != nil {
			return fmt.Errorf("checkpoint attempts_and_results[%d]: %w", i, err)
		}
	}
	return nil
}

func validateFiles(items []CheckpointFileArtifact, allowed map[string]struct{}) error {
	if len(items) == 0 {
		return errors.New("checkpoint files_or_artifacts is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Ref) == "" || strings.TrimSpace(item.Description) == "" {
			return fmt.Errorf("checkpoint files_or_artifacts[%d] ref and description are required", i)
		}
		if err := validateReferences(item.SourceEventIDs, item.Confidence, allowed); err != nil {
			return fmt.Errorf("checkpoint files_or_artifacts[%d]: %w", i, err)
		}
	}
	return nil
}

func validateReferences(ids []string, confidence Confidence, allowed map[string]struct{}) error {
	ids = uniqueNonEmpty(ids)
	if len(ids) == 0 {
		return errors.New("source_event_ids are required")
	}
	if !confidence.valid() {
		return fmt.Errorf("invalid confidence %q", confidence)
	}
	for _, id := range ids {
		if allowed != nil {
			if _, ok := allowed[id]; !ok {
				return fmt.Errorf("unknown source event id %q", id)
			}
		}
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
