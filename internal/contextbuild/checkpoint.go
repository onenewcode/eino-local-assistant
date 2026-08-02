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
// incompatible. Version two separates newly covered raw evidence from the
// parent checkpoint used to carry older work forward.
const CheckpointSchemaVersion = 2

const (
	// SourceRefEvent cites one bounded direct-event anchor.
	SourceRefEvent = "event"
	// SourceRefCheckpoint cites the one parent checkpoint carried into a merge.
	SourceRefCheckpoint = "checkpoint"
)

// SourceRef is an auditable claim reference. Checkpoint references resolve
// through the durable parent lineage; event references are bounded hot anchors.
type SourceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// DirectSourceRange identifies the ordered, bounded event anchors for the raw
// turn groups newly covered by this checkpoint. The cold ledger retains every
// direct event ID; this hot form intentionally stays bounded.
type DirectSourceRange struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	ContentHash string   `json:"content_hash"`
	EventIDs    []string `json:"event_ids"`
}

// ParentCheckpointRef binds a merged checkpoint to the exact persisted parent
// handoff rather than copying parent anchors into the new direct-source scope.
type ParentCheckpointRef struct {
	ID          string `json:"id"`
	Hash        string `json:"hash"`
	LineageHash string `json:"lineage_hash"`
}

// CheckpointProvenance is the model-visible provenance contract. DirectSource
// describes only newly summarized raw groups; Parent is nullable and points to
// the existing handoff when present; LineageHash binds both without conflating
// their hashes.
type CheckpointProvenance struct {
	DirectSource DirectSourceRange    `json:"direct_source"`
	Parent       *ParentCheckpointRef `json:"parent"`
	LineageHash  string               `json:"lineage_hash"`
}

// CheckpointItem is a sourced claim or next action.
type CheckpointItem struct {
	Text       string      `json:"text"`
	SourceRefs []SourceRef `json:"source_refs"`
	Confidence Confidence  `json:"confidence"`
}

// CheckpointDecision records both the decision and why it was chosen.
type CheckpointDecision struct {
	Decision   string      `json:"decision"`
	Reason     string      `json:"reason"`
	SourceRefs []SourceRef `json:"source_refs"`
	Confidence Confidence  `json:"confidence"`
}

// CheckpointAttempt preserves failed and successful exploration so the next
// agent does not repeat a disproven approach.
type CheckpointAttempt struct {
	Text       string      `json:"text"`
	Result     string      `json:"result"`
	SourceRefs []SourceRef `json:"source_refs"`
	Confidence Confidence  `json:"confidence"`
}

// CheckpointFileArtifact references a changed file, tool artifact, log, or
// other evidence that can be re-read outside the compact prompt.
type CheckpointFileArtifact struct {
	Ref         string      `json:"ref"`
	Description string      `json:"description"`
	SourceRefs  []SourceRef `json:"source_refs"`
	Confidence  Confidence  `json:"confidence"`
}

// Checkpoint is a structured, derived handoff state. It is never a substitute
// for the source event stream: every material claim points either to a bounded
// new-event anchor or to the verified parent checkpoint lineage.
type Checkpoint struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id,omitempty"`
	// StorageHash is injected from the durable store after loading. It is never
	// model-visible, but lets a child bind itself to the exact parent payload.
	StorageHash string `json:"-"`
	Trigger     string `json:"trigger,omitempty"`
	Focus       string `json:"focus,omitempty"`

	Provenance CheckpointProvenance `json:"provenance"`

	TaskGoal           string                   `json:"task_goal"`
	Constraints        []CheckpointItem         `json:"constraints"`
	ConfirmedFacts     []CheckpointItem         `json:"confirmed_facts"`
	Decisions          []CheckpointDecision     `json:"decisions"`
	AttemptsAndResults []CheckpointAttempt      `json:"attempts_and_results"`
	FilesOrArtifacts   []CheckpointFileArtifact `json:"files_or_artifacts"`
	OpenQuestions      []CheckpointItem         `json:"open_questions"`
	NextActions        []CheckpointItem         `json:"next_actions"`

	// directSourceScope is injected only after the cold ledger has verified a
	// full direct-source manifest. It lets recursive merges cite a direct event
	// that is outside the bounded model-visible anchor list without serializing
	// that complete manifest into the checkpoint payload.
	directSourceScope []string
}

// legacyV1Checkpoint mirrors the prior strict payload solely for safe resume
// recognition. It is not converted into v2 or used for new compaction.
type legacyV1Checkpoint struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id,omitempty"`
	Trigger       string `json:"trigger,omitempty"`
	Focus         string `json:"focus,omitempty"`

	SourceRange    legacyV1SourceRange `json:"source_range"`
	SourceEventIDs []string            `json:"source_event_ids"`
	SourceHash     string              `json:"source_hash"`

	TaskGoal           string                       `json:"task_goal"`
	Constraints        []legacyV1CheckpointItem     `json:"constraints"`
	ConfirmedFacts     []legacyV1CheckpointItem     `json:"confirmed_facts"`
	Decisions          []legacyV1CheckpointDecision `json:"decisions"`
	AttemptsAndResults []legacyV1CheckpointAttempt  `json:"attempts_and_results"`
	FilesOrArtifacts   []legacyV1CheckpointFile     `json:"files_or_artifacts"`
	OpenQuestions      []legacyV1CheckpointItem     `json:"open_questions"`
	NextActions        []legacyV1CheckpointItem     `json:"next_actions"`
}

type legacyV1SourceRange struct {
	From        string   `json:"from"`
	To          string   `json:"to"`
	ContentHash string   `json:"content_hash"`
	EventIDs    []string `json:"event_ids"`
}

type legacyV1CheckpointItem struct {
	Text           string     `json:"text"`
	SourceEventIDs []string   `json:"source_event_ids"`
	Confidence     Confidence `json:"confidence"`
}

type legacyV1CheckpointDecision struct {
	Decision       string     `json:"decision"`
	Reason         string     `json:"reason"`
	SourceEventIDs []string   `json:"source_event_ids"`
	Confidence     Confidence `json:"confidence"`
}

type legacyV1CheckpointAttempt struct {
	Text           string     `json:"text"`
	Result         string     `json:"result"`
	SourceEventIDs []string   `json:"source_event_ids"`
	Confidence     Confidence `json:"confidence"`
}

type legacyV1CheckpointFile struct {
	Ref            string     `json:"ref"`
	Description    string     `json:"description"`
	SourceEventIDs []string   `json:"source_event_ids"`
	Confidence     Confidence `json:"confidence"`
}

// EstimatedTokens measures the exact derived representation that is placed in
// a prompt by ContextPlanner.
func (c Checkpoint) EstimatedTokens() int {
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

// DirectEvidenceEventIDs returns the bounded model-visible raw anchors.
func (c Checkpoint) DirectEvidenceEventIDs() []string {
	return append([]string(nil), c.Provenance.DirectSource.EventIDs...)
}

// DirectSourceHash returns the canonical hash for only the newly covered raw
// source groups, never the parent checkpoint's anchors.
func (c Checkpoint) DirectSourceHash() string {
	return c.Provenance.DirectSource.ContentHash
}

// ParentRef returns a defensive copy of the checkpoint parent binding.
func (c Checkpoint) ParentRef() *ParentCheckpointRef {
	if c.Provenance.Parent == nil {
		return nil
	}
	parent := *c.Provenance.Parent
	return &parent
}

// ParseCheckpointJSON accepts exactly one strict JSON object and validates all
// required content using the bounded model-visible event anchors. Callers with
// a verified cold direct-source manifest should use ParseCheckpointJSONForSource
// so recursive checkpoints can safely cite interior direct events as well.
func ParseCheckpointJSON(data []byte) (Checkpoint, error) {
	checkpoint, err := decodeCheckpointJSON(data)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := checkpoint.Validate(); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

// ParseCheckpointJSONForSource parses a checkpoint and validates it against
// the exact cold direct-source manifest and parent binding selected by the
// caller. The manifest remains non-serialized while the returned checkpoint is
// in memory.
func ParseCheckpointJSONForSource(data []byte, expected CheckpointProvenance, directSourceEventIDs []string) (Checkpoint, error) {
	return ParseCheckpointJSONForSourceWithClaimScope(data, expected, directSourceEventIDs, directSourceEventIDs)
}

// ParseCheckpointJSONForSourceWithClaimScope parses a checkpoint against its
// complete cold direct-source manifest while restricting event claims to IDs
// that were actually visible to the compactor request.
func ParseCheckpointJSONForSourceWithClaimScope(data []byte, expected CheckpointProvenance, directSourceEventIDs, claimSourceEventIDs []string) (Checkpoint, error) {
	checkpoint, err := decodeCheckpointJSON(data)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := checkpoint.ValidateForSourceWithClaimScope(expected, directSourceEventIDs, claimSourceEventIDs); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint.withDirectSourceScope(directSourceEventIDs), nil
}

func decodeCheckpointJSON(data []byte) (Checkpoint, error) {
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
	return checkpoint, nil
}

// CheckpointSchemaVersionFromJSON extracts a persisted schema version without
// requiring it to satisfy the current strict schema. Resume uses it to reset
// intentionally incompatible v1 active checkpoints without treating history as
// corrupt.
func CheckpointSchemaVersionFromJSON(data []byte) (int, error) {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return 0, fmt.Errorf("decode checkpoint schema version: %w", err)
	}
	return header.SchemaVersion, nil
}

// ValidateLegacyV1CheckpointJSON recognizes only the strict v1 shape that was
// emitted by the prior checkpoint implementation. A bare schema_version field
// is not enough evidence to reset an active checkpoint on resume.
func ValidateLegacyV1CheckpointJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var checkpoint legacyV1Checkpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return fmt.Errorf("decode legacy v1 checkpoint: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("legacy v1 checkpoint must contain one JSON object")
		}
		return fmt.Errorf("decode legacy v1 checkpoint tail: %w", err)
	}
	return checkpoint.validate()
}

func (c legacyV1Checkpoint) validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("unsupported legacy checkpoint schema version %d", c.SchemaVersion)
	}
	if strings.TrimSpace(c.TaskGoal) == "" {
		return errors.New("legacy checkpoint task_goal is required")
	}
	ids, err := canonicalDirectSourceEventIDs(c.SourceEventIDs)
	if err != nil {
		return fmt.Errorf("legacy checkpoint source_event_ids: %w", err)
	}
	if !isSHA256(c.SourceHash) {
		return errors.New("legacy checkpoint source_hash must be a sha256 hex digest")
	}
	if strings.TrimSpace(c.SourceRange.From) == "" || strings.TrimSpace(c.SourceRange.To) == "" || !isSHA256(c.SourceRange.ContentHash) {
		return errors.New("legacy checkpoint source_range is invalid")
	}
	if c.SourceRange.ContentHash != c.SourceHash || !sameStrings(c.SourceRange.EventIDs, ids) || c.SourceRange.From != ids[0] || c.SourceRange.To != ids[len(ids)-1] {
		return errors.New("legacy checkpoint source_range does not match source_event_ids")
	}
	// V1 retained only hot anchors at the top level, while its generated claims
	// could cite inherited cold-lineage events. Match the old strict parser's
	// shape validation here: recognize a valid legacy payload for reset without
	// pretending its bounded anchors were the complete claim scope.
	var allowed map[string]struct{}
	if err := validateLegacyItems("constraints", c.Constraints, allowed); err != nil {
		return err
	}
	if err := validateLegacyItems("confirmed_facts", c.ConfirmedFacts, allowed); err != nil {
		return err
	}
	if err := validateLegacyDecisions(c.Decisions, allowed); err != nil {
		return err
	}
	if err := validateLegacyAttempts(c.AttemptsAndResults, allowed); err != nil {
		return err
	}
	if err := validateLegacyFiles(c.FilesOrArtifacts, allowed); err != nil {
		return err
	}
	if err := validateLegacyItems("open_questions", c.OpenQuestions, allowed); err != nil {
		return err
	}
	return validateLegacyItems("next_actions", c.NextActions, allowed)
}

func validateLegacyItems(name string, items []legacyV1CheckpointItem, allowed map[string]struct{}) error {
	if len(items) == 0 {
		return fmt.Errorf("legacy checkpoint %s is required", name)
	}
	for i, item := range items {
		if strings.TrimSpace(item.Text) == "" {
			return fmt.Errorf("legacy checkpoint %s[%d] text is required", name, i)
		}
		if err := validateLegacyReferences(item.SourceEventIDs, item.Confidence, allowed); err != nil {
			return fmt.Errorf("legacy checkpoint %s[%d]: %w", name, i, err)
		}
	}
	return nil
}

func validateLegacyDecisions(items []legacyV1CheckpointDecision, allowed map[string]struct{}) error {
	if len(items) == 0 {
		return errors.New("legacy checkpoint decisions is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Decision) == "" || strings.TrimSpace(item.Reason) == "" {
			return fmt.Errorf("legacy checkpoint decisions[%d] decision and reason are required", i)
		}
		if err := validateLegacyReferences(item.SourceEventIDs, item.Confidence, allowed); err != nil {
			return fmt.Errorf("legacy checkpoint decisions[%d]: %w", i, err)
		}
	}
	return nil
}

func validateLegacyAttempts(items []legacyV1CheckpointAttempt, allowed map[string]struct{}) error {
	if len(items) == 0 {
		return errors.New("legacy checkpoint attempts_and_results is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Text) == "" || strings.TrimSpace(item.Result) == "" {
			return fmt.Errorf("legacy checkpoint attempts_and_results[%d] text and result are required", i)
		}
		if err := validateLegacyReferences(item.SourceEventIDs, item.Confidence, allowed); err != nil {
			return fmt.Errorf("legacy checkpoint attempts_and_results[%d]: %w", i, err)
		}
	}
	return nil
}

func validateLegacyFiles(items []legacyV1CheckpointFile, allowed map[string]struct{}) error {
	if len(items) == 0 {
		return errors.New("legacy checkpoint files_or_artifacts is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Ref) == "" || strings.TrimSpace(item.Description) == "" {
			return fmt.Errorf("legacy checkpoint files_or_artifacts[%d] ref and description are required", i)
		}
		if err := validateLegacyReferences(item.SourceEventIDs, item.Confidence, allowed); err != nil {
			return fmt.Errorf("legacy checkpoint files_or_artifacts[%d]: %w", i, err)
		}
	}
	return nil
}

func validateLegacyReferences(eventIDs []string, confidence Confidence, allowed map[string]struct{}) error {
	eventIDs = uniqueNonEmpty(eventIDs)
	if len(eventIDs) == 0 {
		return errors.New("legacy checkpoint source_event_ids are required")
	}
	if !confidence.valid() {
		return fmt.Errorf("invalid confidence %q", confidence)
	}
	for _, id := range eventIDs {
		if allowed != nil {
			if _, ok := allowed[id]; !ok {
				return fmt.Errorf("unknown legacy source event id %q", id)
			}
		}
	}
	return nil
}

// Validate checks schema shape, provenance, confidence labels, and required
// handoff sections. Empty sections must be represented by an explicit sourced
// unknown item rather than silently omitted.
func (c Checkpoint) Validate() error {
	if err := c.validateShape(); err != nil {
		return err
	}
	return c.validateClaims(sourceRefSet(c.claimSourceScope()), c.Provenance.Parent)
}

func (c Checkpoint) validateShape() error {
	if c.SchemaVersion != CheckpointSchemaVersion {
		return fmt.Errorf("unsupported checkpoint schema version %d", c.SchemaVersion)
	}
	if strings.TrimSpace(c.TaskGoal) == "" {
		return errors.New("checkpoint task_goal is required")
	}
	if err := validateProvenance(c.Provenance); err != nil {
		return err
	}
	return c.validateClaims(nil, c.Provenance.Parent)
}

// ValidateForSource verifies that a checkpoint describes exactly the expected
// direct raw identity and, where present, the exact parent handoff.
func (c Checkpoint) ValidateForSource(expected CheckpointProvenance) error {
	return c.ValidateForSourceWithDirectSourceEventIDs(expected, expected.DirectSource.EventIDs)
}

// ValidateForSourceWithDirectSourceEventIDs verifies an expected provenance
// against its complete cold source manifest. The checkpoint JSON continues to
// contain only bounded anchors, while source_refs may cite any direct event in
// this verified manifest.
func (c Checkpoint) ValidateForSourceWithDirectSourceEventIDs(expected CheckpointProvenance, directSourceEventIDs []string) error {
	return c.ValidateForSourceWithClaimScope(expected, directSourceEventIDs, directSourceEventIDs)
}

// ValidateForSourceWithClaimScope verifies a checkpoint against its complete
// cold direct-source manifest and a narrower set of event IDs exposed to the
// compactor. The latter prevents a recursive merge from inventing an interior
// raw event that never appeared in its synthetic checkpoint inputs.
func (c Checkpoint) ValidateForSourceWithClaimScope(expected CheckpointProvenance, directSourceEventIDs, claimSourceEventIDs []string) error {
	if err := c.validateShape(); err != nil {
		return err
	}
	canonicalExpected, err := CheckpointProvenanceForSource(directSourceEventIDs, expected.DirectSource.ContentHash, expected.Parent)
	if err != nil {
		return fmt.Errorf("validate expected checkpoint provenance: %w", err)
	}
	if !sameProvenance(expected, canonicalExpected) {
		return errors.New("expected checkpoint provenance does not match direct source manifest")
	}
	if !sameProvenance(c.Provenance, expected) {
		return errors.New("checkpoint provenance does not match compaction source")
	}
	claimSourceEventIDs = uniqueNonEmpty(claimSourceEventIDs)
	if len(claimSourceEventIDs) == 0 {
		return errors.New("checkpoint claim source event ids are required")
	}
	directSources := sourceRefSet(directSourceEventIDs)
	for _, id := range claimSourceEventIDs {
		if _, ok := directSources[id]; !ok {
			return fmt.Errorf("checkpoint claim source event id %q is outside the direct source manifest", id)
		}
	}
	return c.validateClaims(sourceRefSet(claimSourceEventIDs), expected.Parent)
}

// CheckpointProvenanceForSource constructs the model-visible bounded
// provenance for one complete, ordered direct-source manifest.
func CheckpointProvenanceForSource(directSourceEventIDs []string, directSourceHash string, parent *ParentCheckpointRef) (CheckpointProvenance, error) {
	ids, err := canonicalDirectSourceEventIDs(directSourceEventIDs)
	if err != nil {
		return CheckpointProvenance{}, err
	}
	directSourceHash = strings.TrimSpace(directSourceHash)
	if !isSHA256(directSourceHash) {
		return CheckpointProvenance{}, errors.New("checkpoint direct source hash must be a sha256 hex digest")
	}
	if parent != nil {
		parentCopy := *parent
		parent = &parentCopy
	}
	anchors := checkpointEvidenceRefs(ids)
	provenance := CheckpointProvenance{
		DirectSource: DirectSourceRange{
			From:        anchors[0],
			To:          anchors[len(anchors)-1],
			ContentHash: directSourceHash,
			EventIDs:    anchors,
		},
		Parent:      parent,
		LineageHash: HashCheckpointLineage(parent, directSourceHash),
	}
	if err := validateProvenance(provenance); err != nil {
		return CheckpointProvenance{}, err
	}
	return provenance, nil
}

func (c Checkpoint) withDirectSourceScope(directSourceEventIDs []string) Checkpoint {
	c.directSourceScope = append([]string(nil), uniqueNonEmpty(directSourceEventIDs)...)
	return c
}

func (c Checkpoint) claimSourceScope() []string {
	if len(c.directSourceScope) > 0 {
		return c.directSourceScope
	}
	return c.Provenance.DirectSource.EventIDs
}

// modelVisibleDirectEventIDs returns direct events serialized into a checkpoint
// message, either as bounded provenance anchors or as explicit event claims.
// Recursive merge validation uses this set instead of the full cold manifest.
func (c Checkpoint) modelVisibleDirectEventIDs() []string {
	ids := append([]string(nil), c.Provenance.DirectSource.EventIDs...)
	appendRefs := func(refs []SourceRef) {
		for _, ref := range refs {
			if ref.Kind == SourceRefEvent {
				ids = append(ids, ref.ID)
			}
		}
	}
	for _, item := range c.Constraints {
		appendRefs(item.SourceRefs)
	}
	for _, item := range c.ConfirmedFacts {
		appendRefs(item.SourceRefs)
	}
	for _, item := range c.Decisions {
		appendRefs(item.SourceRefs)
	}
	for _, item := range c.AttemptsAndResults {
		appendRefs(item.SourceRefs)
	}
	for _, item := range c.FilesOrArtifacts {
		appendRefs(item.SourceRefs)
	}
	for _, item := range c.OpenQuestions {
		appendRefs(item.SourceRefs)
	}
	for _, item := range c.NextActions {
		appendRefs(item.SourceRefs)
	}
	return uniqueNonEmpty(ids)
}

func canonicalDirectSourceEventIDs(eventIDs []string) ([]string, error) {
	ids := uniqueNonEmpty(eventIDs)
	if len(ids) == 0 || len(ids) != len(eventIDs) {
		return nil, errors.New("checkpoint direct source event ids must be non-empty and unique")
	}
	return ids, nil
}

func sourceRefSet(eventIDs []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(eventIDs))
	for _, id := range uniqueNonEmpty(eventIDs) {
		allowed[id] = struct{}{}
	}
	return allowed
}

func validateProvenance(provenance CheckpointProvenance) error {
	ids := uniqueNonEmpty(provenance.DirectSource.EventIDs)
	if len(ids) == 0 || len(ids) != len(provenance.DirectSource.EventIDs) {
		return errors.New("checkpoint direct_source.event_ids must be non-empty and unique")
	}
	if strings.TrimSpace(provenance.DirectSource.From) == "" || strings.TrimSpace(provenance.DirectSource.To) == "" {
		return errors.New("checkpoint direct_source boundaries are required")
	}
	if !isSHA256(provenance.DirectSource.ContentHash) {
		return errors.New("checkpoint direct_source.content_hash must be a sha256 hex digest")
	}
	if provenance.DirectSource.From != ids[0] || provenance.DirectSource.To != ids[len(ids)-1] {
		return errors.New("checkpoint direct_source boundaries must match event_ids")
	}
	if provenance.Parent != nil {
		if strings.TrimSpace(provenance.Parent.ID) == "" || !isSHA256(provenance.Parent.Hash) || !isSHA256(provenance.Parent.LineageHash) {
			return errors.New("checkpoint parent id, hash, and lineage_hash are required")
		}
	}
	if !isSHA256(provenance.LineageHash) {
		return errors.New("checkpoint lineage_hash must be a sha256 hex digest")
	}
	if provenance.LineageHash != HashCheckpointLineage(provenance.Parent, provenance.DirectSource.ContentHash) {
		return errors.New("checkpoint lineage_hash does not match parent and direct source")
	}
	return nil
}

func sameProvenance(a, b CheckpointProvenance) bool {
	if a.DirectSource.From != b.DirectSource.From || a.DirectSource.To != b.DirectSource.To ||
		a.DirectSource.ContentHash != b.DirectSource.ContentHash || !sameStrings(a.DirectSource.EventIDs, b.DirectSource.EventIDs) ||
		a.LineageHash != b.LineageHash {
		return false
	}
	if a.Parent == nil || b.Parent == nil {
		return a.Parent == nil && b.Parent == nil
	}
	return *a.Parent == *b.Parent
}

// HashCheckpointLineage binds the parent handoff (when present) to newly
// covered raw evidence. The delimiter makes the canonical inputs unambiguous.
func HashCheckpointLineage(parent *ParentCheckpointRef, directSourceHash string) string {
	hash := sha256.New()
	if parent != nil {
		_, _ = hash.Write([]byte(parent.ID))
		_, _ = hash.Write([]byte("\n"))
		_, _ = hash.Write([]byte(parent.Hash))
		_, _ = hash.Write([]byte("\n"))
		_, _ = hash.Write([]byte(parent.LineageHash))
		_, _ = hash.Write([]byte("\n"))
	}
	_, _ = hash.Write([]byte(directSourceHash))
	return hex.EncodeToString(hash.Sum(nil))
}

func (c Checkpoint) validateClaims(allowedEvents map[string]struct{}, parent *ParentCheckpointRef) error {
	if err := validateItems("constraints", c.Constraints, allowedEvents, parent); err != nil {
		return err
	}
	if err := validateItems("confirmed_facts", c.ConfirmedFacts, allowedEvents, parent); err != nil {
		return err
	}
	if err := validateDecisions(c.Decisions, allowedEvents, parent); err != nil {
		return err
	}
	if err := validateAttempts(c.AttemptsAndResults, allowedEvents, parent); err != nil {
		return err
	}
	if err := validateFiles(c.FilesOrArtifacts, allowedEvents, parent); err != nil {
		return err
	}
	if err := validateItems("open_questions", c.OpenQuestions, allowedEvents, parent); err != nil {
		return err
	}
	return validateItems("next_actions", c.NextActions, allowedEvents, parent)
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

func validateItems(name string, items []CheckpointItem, allowedEvents map[string]struct{}, parent *ParentCheckpointRef) error {
	if len(items) == 0 {
		return fmt.Errorf("checkpoint %s is required", name)
	}
	for i, item := range items {
		if strings.TrimSpace(item.Text) == "" {
			return fmt.Errorf("checkpoint %s[%d] text is required", name, i)
		}
		if err := validateReferences(item.SourceRefs, item.Confidence, allowedEvents, parent); err != nil {
			return fmt.Errorf("checkpoint %s[%d]: %w", name, i, err)
		}
	}
	return nil
}

func validateDecisions(items []CheckpointDecision, allowedEvents map[string]struct{}, parent *ParentCheckpointRef) error {
	if len(items) == 0 {
		return errors.New("checkpoint decisions is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Decision) == "" || strings.TrimSpace(item.Reason) == "" {
			return fmt.Errorf("checkpoint decisions[%d] decision and reason are required", i)
		}
		if err := validateReferences(item.SourceRefs, item.Confidence, allowedEvents, parent); err != nil {
			return fmt.Errorf("checkpoint decisions[%d]: %w", i, err)
		}
	}
	return nil
}

func validateAttempts(items []CheckpointAttempt, allowedEvents map[string]struct{}, parent *ParentCheckpointRef) error {
	if len(items) == 0 {
		return errors.New("checkpoint attempts_and_results is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Text) == "" || strings.TrimSpace(item.Result) == "" {
			return fmt.Errorf("checkpoint attempts_and_results[%d] text and result are required", i)
		}
		if err := validateReferences(item.SourceRefs, item.Confidence, allowedEvents, parent); err != nil {
			return fmt.Errorf("checkpoint attempts_and_results[%d]: %w", i, err)
		}
	}
	return nil
}

func validateFiles(items []CheckpointFileArtifact, allowedEvents map[string]struct{}, parent *ParentCheckpointRef) error {
	if len(items) == 0 {
		return errors.New("checkpoint files_or_artifacts is required")
	}
	for i, item := range items {
		if strings.TrimSpace(item.Ref) == "" || strings.TrimSpace(item.Description) == "" {
			return fmt.Errorf("checkpoint files_or_artifacts[%d] ref and description are required", i)
		}
		if err := validateReferences(item.SourceRefs, item.Confidence, allowedEvents, parent); err != nil {
			return fmt.Errorf("checkpoint files_or_artifacts[%d]: %w", i, err)
		}
	}
	return nil
}

func validateReferences(refs []SourceRef, confidence Confidence, allowedEvents map[string]struct{}, parent *ParentCheckpointRef) error {
	if len(refs) == 0 {
		return errors.New("source_refs are required")
	}
	if !confidence.valid() {
		return fmt.Errorf("invalid confidence %q", confidence)
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		kind := strings.TrimSpace(ref.Kind)
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			return errors.New("source ref id is required")
		}
		key := kind + ":" + id
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate source ref %q", key)
		}
		seen[key] = struct{}{}
		switch kind {
		case SourceRefEvent:
			if allowedEvents != nil {
				if _, ok := allowedEvents[id]; !ok {
					return fmt.Errorf("unknown direct source event id %q", id)
				}
			}
		case SourceRefCheckpoint:
			if parent == nil || id != parent.ID {
				return fmt.Errorf("unknown parent checkpoint id %q", id)
			}
		default:
			return fmt.Errorf("invalid source ref kind %q", kind)
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
