package contextbuild

import (
	"errors"
	"strings"
	"testing"

	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

func TestDefaultConfigIncludesPlanningDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ModelContextTokens != 32_000 {
		t.Fatalf("ModelContextTokens = %d, want 32000", cfg.ModelContextTokens)
	}
	if cfg.OutputReserveTokens != 4_096 {
		t.Fatalf("OutputReserveTokens = %d, want 4096", cfg.OutputReserveTokens)
	}
	if cfg.AutoCompactTriggerPercent != 75 || cfg.PostCompactTargetPercent != 45 {
		t.Fatalf("trigger/target = %d/%d", cfg.AutoCompactTriggerPercent, cfg.PostCompactTargetPercent)
	}
	if cfg.SummaryMaxTokens != 2_048 || cfg.KeepRecentTurns != 12 {
		t.Fatalf("summary/recent = %d/%d", cfg.SummaryMaxTokens, cfg.KeepRecentTurns)
	}
	if cfg.LowGainThresholdPercent != 15 || cfg.MaxLowGainAttempts != 2 {
		t.Fatalf("low gain settings = %d/%d", cfg.LowGainThresholdPercent, cfg.MaxLowGainAttempts)
	}
}

func TestConfigNormalizeTreatsZeroAsProductDefault(t *testing.T) {
	got := (Config{}).Normalize()
	want := DefaultConfig()
	if got != want {
		t.Fatalf("zero-value Normalize = %+v, want defaults %+v", got, want)
	}
}

func TestContextPlannerKeepsCompleteRecentSuffixAndCurrentLast(t *testing.T) {
	planner := NewContextPlanner(Config{
		ModelContextTokens:        1_000,
		OutputReserveTokens:       100,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
		SummaryMaxTokens:          256,
	})

	toolIndex := 0
	groups := []TurnGroup{
		testTurnGroup("old", "event-old", 300),
		{
			ID:             "tool-turn",
			SourceEventIDs: []string{"event-tool"},
			TokenEstimate:  300,
			Messages: []*schema.Message{
				{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
					Index: &toolIndex,
					ID:    "call-1",
					Type:  "function",
					Function: schema.FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"a.go"}`,
					},
				}}},
				{Role: schema.Tool, ToolCallID: "call-1", ToolName: "read_file", Content: strings.Repeat("t", 1_000)},
			},
		},
		testTurnGroup("new", "event-new", 300),
	}

	plan, err := planner.Plan(PlannerInput{
		ImmutableMessages: []*schema.Message{schema.SystemMessage("system")},
		CurrentMessages:   []*schema.Message{schema.UserMessage("LATEST")},
		TurnGroups:        groups,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got, want := strings.Join(plan.IncludedGroupIDs, ","), "tool-turn,new"; got != want {
		t.Fatalf("included groups = %q, want %q", got, want)
	}
	if got, want := strings.Join(plan.OmittedGroupIDs, ","), "old"; got != want {
		t.Fatalf("omitted groups = %q, want %q", got, want)
	}
	if plan.Messages[len(plan.Messages)-1].Content != "LATEST" {
		t.Fatalf("current message must remain last: %#v", plan.Messages[len(plan.Messages)-1])
	}
	var assistantTool, toolResult bool
	for _, message := range plan.Messages {
		if message.Role == schema.Assistant && len(message.ToolCalls) == 1 && message.ToolCalls[0].ID == "call-1" {
			assistantTool = true
		}
		if message.Role == schema.Tool && message.ToolCallID == "call-1" {
			toolResult = true
		}
	}
	if !assistantTool || !toolResult {
		t.Fatalf("tool group was split or omitted: %#v", plan.Messages)
	}
	if !plan.ShouldCompact {
		t.Fatalf("expected compaction signal: %+v", plan)
	}
}

func TestArtifactEstimateIncludesRenderedMessageFraming(t *testing.T) {
	artifact := ArtifactRef{ID: "artifact-1", Digest: "evidence"}
	want := usage.EstimateMessages([]*schema.Message{schema.UserMessage(artifact.PromptText())})
	if got := artifact.EstimatedTokens(); got != want {
		t.Fatalf("artifact estimate = %d, want rendered message estimate %d", got, want)
	}
}

func TestContextPlannerRejectsImmutableOverBudget(t *testing.T) {
	planner := NewContextPlanner(Config{
		ModelContextTokens:  400,
		OutputReserveTokens: 100,
	})
	_, err := planner.Plan(PlannerInput{
		ImmutableMessages: []*schema.Message{schema.SystemMessage(strings.Repeat("s", 2_000))},
		CurrentMessages:   []*schema.Message{schema.UserMessage("latest")},
	})
	if !errors.Is(err, ErrImmutableOverBudget) {
		t.Fatalf("Plan error = %v, want ErrImmutableOverBudget", err)
	}
}

func TestContextPlannerArtifactReferencesStayOutsideSourceMessages(t *testing.T) {
	planner := NewContextPlanner(Config{ModelContextTokens: 2_000, OutputReserveTokens: 200})
	plan, err := planner.Plan(PlannerInput{
		ImmutableMessages: []*schema.Message{schema.SystemMessage("system")},
		CurrentMessages:   []*schema.Message{schema.UserMessage("question")},
		Artifacts: []ArtifactRef{{
			ID:             "log-1",
			URI:            "artifact://log-1",
			Digest:         "failing test names",
			SourceEventIDs: []string{"event-log"},
			Confidence:     ConfidenceObserved,
			Required:       true,
		}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.IncludedArtifacts) != 1 || plan.IncludedArtifacts[0].ID != "log-1" {
		t.Fatalf("included artifacts = %#v", plan.IncludedArtifacts)
	}
	if !strings.Contains(plan.Messages[1].Content, "artifact://log-1") {
		t.Fatalf("artifact reference missing from prompt: %#v", plan.Messages)
	}
}

func testTurnGroup(id, eventID string, tokenEstimate int) TurnGroup {
	return TurnGroup{
		ID:             id,
		SourceEventIDs: []string{eventID},
		TokenEstimate:  tokenEstimate,
		Messages:       []*schema.Message{schema.UserMessage(strings.Repeat(id+" ", 300))},
	}
}
