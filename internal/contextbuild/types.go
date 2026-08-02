package contextbuild

import (
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
)

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
	ID             string            `json:"id"`
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
	BudgetTokens      int
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
