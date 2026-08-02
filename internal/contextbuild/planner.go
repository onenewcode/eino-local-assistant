package contextbuild

import (
	"errors"
	"fmt"
	"strings"

	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

// ContextPlanner builds a bounded prompt from semantically complete turn groups
// and tool transactions.
type ContextPlanner struct {
	Config Config
}

// NewContextPlanner retains the caller configuration. Zero values are resolved
// when planning; keeping the raw values lets ValidateConfig reject negatives.
func NewContextPlanner(cfg Config) *ContextPlanner {
	return &ContextPlanner{Config: cfg}
}

// PlanContext is a convenience wrapper for one-off planning.
func PlanContext(input PlannerInput, cfg Config) (PromptPlan, error) {
	return NewContextPlanner(cfg).Plan(input)
}

// PromptBudgetTokens returns the input capacity after reserving output space.
func (p *ContextPlanner) PromptBudgetTokens() int {
	cfg := p.normalizedConfig()
	return cfg.WindowTokens - cfg.MaxOutputTokens
}

// AutoCompactTriggerTokens is the soft-watermark threshold for planning an
// asynchronous compaction at a turn boundary.
func (p *ContextPlanner) AutoCompactTriggerTokens() int {
	cfg := p.normalizedConfig()
	return percentTokens(p.PromptBudgetTokens(), cfg.AutoCompactTriggerPercent)
}

// PostCompactTargetTokens is the desired prompt size after a successful
// compaction. It gives the caller a stable target for anti-thrashing logic.
func (p *ContextPlanner) PostCompactTargetTokens() int {
	cfg := p.normalizedConfig()
	return percentTokens(p.PromptBudgetTokens(), cfg.PostCompactTargetPercent)
}

// ValidateConfig reports invalid planner settings.
func (p *ContextPlanner) ValidateConfig() error {
	if p == nil {
		return errors.New("context planner is required")
	}
	cfg := p.Config
	if cfg.WindowTokens < 0 {
		return errors.New("context window tokens must be >= 0")
	}
	if cfg.MaxOutputTokens < 0 {
		return errors.New("max output tokens must be >= 0")
	}
	normalized := cfg.Normalize()
	if normalized.MaxOutputTokens >= normalized.WindowTokens {
		return errors.New("max output tokens must be smaller than context window tokens")
	}
	if cfg.AutoCompactTriggerPercent < 0 || cfg.AutoCompactTriggerPercent > 100 {
		return errors.New("auto compact trigger percent must be between 0 and 100")
	}
	if cfg.PostCompactTargetPercent < 0 || cfg.PostCompactTargetPercent > 100 {
		return errors.New("post compact target percent must be between 0 and 100")
	}
	if cfg.SummaryMaxTokens < 0 {
		return errors.New("summary max tokens must be >= 0")
	}
	if cfg.LowGainThresholdPercent < 0 || cfg.LowGainThresholdPercent > 100 {
		return errors.New("low gain threshold percent must be between 0 and 100")
	}
	return nil
}

// Plan builds a model-ready prompt. Immutable and active content must fit as a
// unit; older groups are deterministically omitted oldest-first, but a group is
// never split. Returned messages are copies of all source message structs.
func (p *ContextPlanner) Plan(input PlannerInput) (PromptPlan, error) {
	if err := p.ValidateConfig(); err != nil {
		return PromptPlan{}, err
	}
	for _, group := range input.TurnGroups {
		if err := group.validate(); err != nil {
			return PromptPlan{}, err
		}
	}
	for _, artifact := range input.Artifacts {
		if err := artifact.validate(); err != nil {
			return PromptPlan{}, err
		}
	}
	if input.Checkpoint != nil {
		if err := input.Checkpoint.Validate(); err != nil {
			return PromptPlan{}, fmt.Errorf("validate checkpoint: %w", err)
		}
	}

	budget := p.PromptBudgetTokens()
	trigger := p.AutoCompactTriggerTokens()
	target := p.PostCompactTargetTokens()
	plan := PromptPlan{
		BudgetTokens:  budget,
		TriggerTokens: trigger,
		TargetTokens:  target,
	}

	prefix := append(cloneMsgs(input.ImmutableMessages), cloneMsgs(input.ActiveMessages)...)
	current := cloneMsgs(input.CurrentMessages)
	mandatory := append(cloneMsgs(prefix), current...)
	immutableTokens := usage.EstimateMessages(mandatory)
	if immutableTokens > budget {
		return PromptPlan{}, &ImmutableOverBudgetError{Tokens: immutableTokens, Budget: budget}
	}

	// OriginalTokens measures the full candidate set before any planner fallback.
	plan.OriginalTokens = immutableTokens
	if input.Checkpoint != nil {
		plan.OriginalTokens += input.Checkpoint.EstimatedTokens()
	}
	for _, artifact := range input.Artifacts {
		plan.OriginalTokens += artifact.EstimatedTokens()
	}
	for _, group := range input.TurnGroups {
		plan.OriginalTokens += group.EstimatedTokens()
	}

	messages := prefix
	used := immutableTokens

	if input.Checkpoint != nil {
		checkpointMessage := schema.SystemMessage(input.Checkpoint.PromptText())
		checkpointTokens := usage.EstimateMessages([]*schema.Message{checkpointMessage})
		if used+checkpointTokens <= budget {
			messages = append(messages, checkpointMessage)
			used += checkpointTokens
		} else {
			plan.Fallbacks = append(plan.Fallbacks, PlanFallback{
				Kind:    "checkpoint_omitted",
				Details: "derived checkpoint did not fit after immutable context",
			})
		}
	}

	// Required artifact digests are treated as active context. Optional artifacts
	// are admitted later only when capacity remains.
	for _, artifact := range input.Artifacts {
		if !artifact.Required {
			continue
		}
		artifactMessage := schema.UserMessage(artifact.PromptText())
		artifactTokens := usage.EstimateMessages([]*schema.Message{artifactMessage})
		if used+artifactTokens > budget {
			return PromptPlan{}, fmt.Errorf("%w: artifact %q", ErrRequiredGroupOverBudget, artifact.ID)
		}
		messages = append(messages, artifactMessage)
		used += artifactTokens
		plan.IncludedArtifacts = append(plan.IncludedArtifacts, artifact)
	}

	// Select a contiguous historical suffix newest-first. Once a group does not
	// fit, all older optional groups are omitted; otherwise a small old group
	// could displace more relevant recent context.
	selected := make([]TurnGroup, 0, len(input.TurnGroups))
	omitted := make([]string, 0)
	cut := false
	for i := len(input.TurnGroups) - 1; i >= 0; i-- {
		group := input.TurnGroups[i]
		groupTokens := group.EstimatedTokens()
		if cut || used+groupTokens > budget {
			if group.Required {
				return PromptPlan{}, fmt.Errorf("%w: group %q", ErrRequiredGroupOverBudget, group.ID)
			}
			omitted = append(omitted, group.ID)
			cut = true
			continue
		}
		selected = append(selected, group)
		used += groupTokens
	}
	for i := len(selected) - 1; i >= 0; i-- {
		group := selected[i]
		messages = append(messages, group.promptMessages()...)
		plan.IncludedGroupIDs = append(plan.IncludedGroupIDs, group.ID)
	}
	// omitted was discovered newest-to-oldest; callers expect source order.
	for i := len(omitted) - 1; i >= 0; i-- {
		plan.OmittedGroupIDs = append(plan.OmittedGroupIDs, omitted[i])
	}

	for _, artifact := range input.Artifacts {
		if artifact.Required {
			continue
		}
		artifactMessage := schema.UserMessage(artifact.PromptText())
		artifactTokens := usage.EstimateMessages([]*schema.Message{artifactMessage})
		if used+artifactTokens > budget {
			plan.Fallbacks = append(plan.Fallbacks, PlanFallback{
				Kind:    "artifact_omitted",
				Details: "artifact " + artifact.ID + " did not fit",
			})
			continue
		}
		messages = append(messages, artifactMessage)
		used += artifactTokens
		plan.IncludedArtifacts = append(plan.IncludedArtifacts, artifact)
	}

	// Current request content must remain last so tool/model role ordering stays
	// conventional even after historical groups and artifact digests are added.
	messages = append(messages, current...)
	plan.Messages = cloneMsgs(messages)
	plan.ResultTokens = usage.EstimateMessages(plan.Messages)
	plan.ShouldCompact = plan.OriginalTokens >= trigger && (len(plan.OmittedGroupIDs) > 0 || plan.OriginalTokens > target)
	if len(plan.OmittedGroupIDs) > 0 {
		plan.Fallbacks = append(plan.Fallbacks, PlanFallback{
			Kind:    "complete_groups_omitted",
			Details: strings.Join(plan.OmittedGroupIDs, ","),
		})
	}
	return plan, nil
}

func (p *ContextPlanner) normalizedConfig() Config {
	if p == nil {
		return DefaultConfig()
	}
	return p.Config.Normalize()
}

func percentTokens(tokens, percent int) int {
	if tokens <= 0 || percent <= 0 {
		return 0
	}
	return tokens * percent / 100
}
