package contextbuild

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

var (
	// ErrImmutableOverBudget means the system instructions and active task alone
	// cannot fit into the model input budget. Dropping prior turn groups cannot fix it.
	ErrImmutableOverBudget = errors.New("immutable context exceeds prompt budget")
	// ErrRequiredGroupOverBudget means a group explicitly marked Required cannot
	// fit without splitting a turn or tool transaction.
	ErrRequiredGroupOverBudget = errors.New("required context group exceeds prompt budget")
	// ErrRequestAdmissionExceeded means an outbound model request was blocked
	// before calling the provider because its conservative local estimate exceeds
	// the fixed full-window safety ceiling.
	ErrRequestAdmissionExceeded = errors.New("model request exceeds context safety ceiling")
)

// RequestAdmissionExceededError records the local estimate used to reject one
// outbound request. It is intentionally an estimate: provider token accounting
// remains the source of truth for reported usage.
type RequestAdmissionExceededError struct {
	EstimatedTokens int
	CeilingTokens   int
}

func (e *RequestAdmissionExceededError) Error() string {
	return fmt.Sprintf("%s: estimated %d tokens exceeds %d-token ceiling", ErrRequestAdmissionExceeded, e.EstimatedTokens, e.CeilingTokens)
}

func (e *RequestAdmissionExceededError) Unwrap() error {
	return ErrRequestAdmissionExceeded
}

// AdmissionPolicy estimates all material that reaches a model request: prompt
// messages, serialized tool schemas, protocol framing, and a tokenizer guard.
// It is deliberately local and conservative; provider usage remains the
// observable source of truth after a request completes.
type AdmissionPolicy struct {
	WindowTokens     int
	ToolSchemaTokens int
}

const (
	admissionBaseFramingTokens    = 32
	admissionMessageFramingTokens = 8
	admissionToolFramingTokens    = 16
	admissionGuardPercent         = 10
)

// NewAdmissionPolicy creates an immutable policy for one model binding. Tool
// schemas are serialized once because providers include them in every
// tool-enabled request even though they are not regular chat messages.
func NewAdmissionPolicy(windowTokens int, tools []*schema.ToolInfo) AdmissionPolicy {
	policy := AdmissionPolicy{WindowTokens: windowTokens}
	if len(tools) == 0 {
		return policy
	}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		encoded, err := json.Marshal(tool)
		if err != nil {
			// The provider will still validate the schema. Count a conservative
			// fixed fallback instead of allowing a marshal anomaly to disable
			// local safety admission.
			policy.ToolSchemaTokens += admissionToolFramingTokens * 4
			continue
		}
		policy.ToolSchemaTokens += usage.EstimateText(string(encoded)) + admissionToolFramingTokens
	}
	return policy
}

// WithoutTools returns the same window policy for an unbound final response.
func (p AdmissionPolicy) WithoutTools() AdmissionPolicy {
	p.ToolSchemaTokens = 0
	return p
}

// Enabled reports whether an embedding configured a physical context window.
func (p AdmissionPolicy) Enabled() bool { return p.WindowTokens > 0 }

// CeilingTokens returns the fixed local request-admission ceiling.
func (p AdmissionPolicy) CeilingTokens() int {
	if !p.Enabled() {
		return 0
	}
	return percentTokens(p.WindowTokens, requestAdmissionCeilingPercent)
}

// EstimateRequestTokens includes non-message material that generic message
// counters cannot observe. The guard covers provider-specific serialization
// and the fact that the local tokenizer is intentionally only an estimate.
func (p AdmissionPolicy) EstimateRequestTokens(messages []*schema.Message) int {
	raw := usage.EstimateMessages(messages) + p.ToolSchemaTokens + admissionBaseFramingTokens
	for _, message := range messages {
		if message != nil {
			raw += admissionMessageFramingTokens
		}
	}
	guard := max(admissionBaseFramingTokens, raw*admissionGuardPercent/100)
	return raw + guard
}

// MaxResponseTokens computes the required Anthropic Messages API value. This
// is a transport constraint, not a public response-length preference.
func (p AdmissionPolicy) MaxResponseTokens(messages []*schema.Message) int {
	remaining := p.CeilingTokens() - p.EstimateRequestTokens(messages)
	if remaining < 1 {
		return 1
	}
	return remaining
}

// CheckRequestAdmission rejects an outbound request before it can reach a
// provider. A disabled policy preserves the standalone component's explicit
// no-window mode.
func CheckRequestAdmission(messages []*schema.Message, policy AdmissionPolicy) error {
	if !policy.Enabled() {
		return nil
	}
	estimatedTokens := policy.EstimateRequestTokens(messages)
	ceilingTokens := policy.CeilingTokens()
	if estimatedTokens <= ceilingTokens {
		return nil
	}
	return &RequestAdmissionExceededError{
		EstimatedTokens: estimatedTokens,
		CeilingTokens:   ceilingTokens,
	}
}

// Confidence states how directly a checkpoint claim is supported by its sources.
type Confidence string

const (
	ConfidenceObserved Confidence = "observed"
	ConfidenceInferred Confidence = "inferred"
	ConfidenceUnknown  Confidence = "unknown"
)

func (c Confidence) valid() bool {
	return c == ConfidenceObserved || c == ConfidenceInferred || c == ConfidenceUnknown
}

// ArtifactRef keeps large or re-readable source material outside the hot prompt
// while retaining enough provenance to request it again.
type ArtifactRef struct {
	ID             string     `json:"id"`
	URI            string     `json:"uri,omitempty"`
	Digest         string     `json:"digest,omitempty"`
	ContentHash    string     `json:"content_hash,omitempty"`
	SourceEventIDs []string   `json:"source_event_ids,omitempty"`
	Confidence     Confidence `json:"confidence,omitempty"`
	TokenEstimate  int        `json:"token_estimate,omitempty"`
	Required       bool       `json:"required,omitempty"`
}

// PromptText is intentionally data-only: callers decide which role to use when
// placing an artifact into a model prompt.
func (a ArtifactRef) PromptText() string {
	var b strings.Builder
	b.WriteString("[artifact")
	if a.ID != "" {
		b.WriteString(" id=")
		b.WriteString(a.ID)
	}
	if a.URI != "" {
		b.WriteString(" uri=")
		b.WriteString(a.URI)
	}
	if a.ContentHash != "" {
		b.WriteString(" hash=")
		b.WriteString(a.ContentHash)
	}
	b.WriteString("]")
	if a.Digest != "" {
		b.WriteString("\n")
		b.WriteString(a.Digest)
	}
	return b.String()
}

// EstimatedTokens takes the larger of a declared estimate and a stable local
// estimate. An optimistic caller estimate must never make the planner exceed
// its hard input budget.
func (a ArtifactRef) EstimatedTokens() int {
	// Artifacts are rendered as user messages, so include the role/message
	// framing in the admission estimate instead of only counting plain text.
	return max(a.TokenEstimate, usage.EstimateMessages([]*schema.Message{schema.UserMessage(a.PromptText())}))
}

func (a ArtifactRef) validate() error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("artifact id is required")
	}
	if a.TokenEstimate < 0 {
		return fmt.Errorf("artifact %q has a negative token estimate", a.ID)
	}
	if a.Confidence != "" && !a.Confidence.valid() {
		return fmt.Errorf("artifact %q has invalid confidence %q", a.ID, a.Confidence)
	}
	return nil
}

// TurnGroup is the smallest planner unit. Messages and artifacts in one group
// are kept or omitted together so a tool result cannot become orphaned from its
// assistant tool call, nor can a turn be split halfway through a transaction.
// Groups are ordered oldest to newest when passed to ContextPlanner.
type TurnGroup struct {
	ID string `json:"id"`
	// StartSequence is a local ledger cursor. It never enters a compactor
	// request or prompt, but lets resume seek directly to an uncovered tail.
	StartSequence  uint64            `json:"-"`
	SourceEventIDs []string          `json:"source_event_ids,omitempty"`
	Messages       []*schema.Message `json:"messages,omitempty"`
	Artifacts      []ArtifactRef     `json:"artifacts,omitempty"`
	TokenEstimate  int               `json:"token_estimate,omitempty"`
	Required       bool              `json:"required,omitempty"`

	// derivedCheckpoint marks an in-memory recursive merge input. It is never
	// serialized to a provider, and prevents a second merge-of-merges from
	// severing interior raw-event provenance.
	derivedCheckpoint bool
	// visibleCheckpointEventIDs is the subset of a synthetic checkpoint's direct
	// events that its merge prompt actually exposes. It remains in memory so a
	// final merge cannot cite an arbitrary event from the larger cold manifest.
	visibleCheckpointEventIDs []string
}

// EffectiveSourceEventIDs returns explicit event IDs, falling back to the
// group ID so callers can adopt grouping before their event store ships.
func (g TurnGroup) EffectiveSourceEventIDs() []string {
	if len(g.SourceEventIDs) > 0 {
		return uniqueNonEmpty(g.SourceEventIDs)
	}
	if strings.TrimSpace(g.ID) == "" {
		return nil
	}
	return []string{g.ID}
}

func (g TurnGroup) validate() error {
	if strings.TrimSpace(g.ID) == "" {
		return errors.New("turn group id is required")
	}
	if g.TokenEstimate < 0 {
		return fmt.Errorf("turn group %q has a negative token estimate", g.ID)
	}
	if len(g.Messages) == 0 && len(g.Artifacts) == 0 {
		return fmt.Errorf("turn group %q is empty", g.ID)
	}
	for i, message := range g.Messages {
		if message == nil {
			return fmt.Errorf("turn group %q message %d is nil", g.ID, i)
		}
	}
	for _, artifact := range g.Artifacts {
		if err := artifact.validate(); err != nil {
			return fmt.Errorf("turn group %q: %w", g.ID, err)
		}
	}
	if len(g.EffectiveSourceEventIDs()) == 0 {
		return fmt.Errorf("turn group %q has no source event ids", g.ID)
	}
	return nil
}

// EstimatedTokens counts the exact prompt pieces the planner can render and
// treats an explicit estimate as an upper-bound hint, never a reason to
// undercount a real group.
func (g TurnGroup) EstimatedTokens() int {
	tokens := usage.EstimateMessages(g.Messages)
	for _, artifact := range g.Artifacts {
		tokens += artifact.EstimatedTokens()
	}
	return max(g.TokenEstimate, tokens)
}

func (g TurnGroup) promptMessages() []*schema.Message {
	messages := cloneMsgs(g.Messages)
	for _, artifact := range g.Artifacts {
		messages = append(messages, schema.UserMessage(artifact.PromptText()))
	}
	return messages
}

// PlannerInput separates immutable instructions, active work, derived state,
// complete historical groups, and optional artifact references. All fields are
// copied before planning; ContextPlanner never mutates source messages.
type PlannerInput struct {
	ImmutableMessages []*schema.Message
	ActiveMessages    []*schema.Message
	// CurrentMessages are appended last and treated as immutable for the current
	// request. Use this for the current user prompt so historical groups retain
	// chronological placement before it.
	CurrentMessages []*schema.Message
	Checkpoint      *Checkpoint
	TurnGroups      []TurnGroup
	Artifacts       []ArtifactRef
}

// PlanFallback records a deterministic degradation taken to stay within the
// prompt budget. It is observable rather than silently lossy.
type PlanFallback struct {
	Kind    string
	Details string
}

// PromptPlan is a pure, model-ready view plus its provenance and capacity
// decisions. Turn groups are never partially included.
type PromptPlan struct {
	Messages          []*schema.Message
	CeilingTokens     int
	TriggerTokens     int
	TargetTokens      int
	OriginalTokens    int
	ResultTokens      int
	ShouldCompact     bool
	IncludedGroupIDs  []string
	OmittedGroupIDs   []string
	IncludedArtifacts []ArtifactRef
	Fallbacks         []PlanFallback
}

// ImmutableOverBudgetError carries the observed size for callers that want to
// render a helpful recovery message while still supporting errors.Is.
type ImmutableOverBudgetError struct {
	Tokens int
	Budget int
}

func (e *ImmutableOverBudgetError) Error() string {
	return fmt.Sprintf("%s: %d tokens exceeds %d-token budget", ErrImmutableOverBudget, e.Tokens, e.Budget)
}

func (e *ImmutableOverBudgetError) Unwrap() error {
	return ErrImmutableOverBudget
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func cloneMsgs(messages []*schema.Message) []*schema.Message {
	cloned := make([]*schema.Message, 0, len(messages))
	for _, message := range messages {
		if message == nil {
			continue
		}
		clonedMessage := *message
		cloned = append(cloned, &clonedMessage)
	}
	return cloned
}
