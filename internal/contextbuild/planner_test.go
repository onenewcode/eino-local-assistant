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
	if cfg.WindowTokens != 32_000 {
		t.Fatalf("WindowTokens = %d, want 32000", cfg.WindowTokens)
	}
	if cfg.AutoCompactTriggerTokens() != 27_200 || cfg.RequestAdmissionCeilingTokens() != 30_400 || cfg.PostCompactTargetTokens() != 16_000 {
		t.Fatalf("fixed policy = trigger %d ceiling %d target %d", cfg.AutoCompactTriggerTokens(), cfg.RequestAdmissionCeilingTokens(), cfg.PostCompactTargetTokens())
	}
	if cfg.KeepRecentTurnGroups() != 12 || cfg.CheckpointTargetTokens() != 2_000 || cfg.MinimumCompactionGainPercent() != 20 {
		t.Fatalf("fixed compaction policy is inconsistent: %+v", cfg)
	}
}

func TestConfigNormalizeTreatsZeroAsProductDefault(t *testing.T) {
	got := (Config{}).Normalize()
	want := DefaultConfig()
	if got != want {
		t.Fatalf("zero-value Normalize = %+v, want defaults %+v", got, want)
	}
}

func TestContextPlannerRejectsNegativeWindow(t *testing.T) {
	planner := NewContextPlanner(Config{WindowTokens: -1})
	if err := planner.ValidateConfig(); err == nil {
		t.Fatal("ValidateConfig() error = nil, want an invalid budget error")
	}
}

func TestAdmissionPolicyCountsBoundToolSchemas(t *testing.T) {
	messages := []*schema.Message{schema.SystemMessage("system"), schema.UserMessage("inspect this")}
	tool := &schema.ToolInfo{
		Name: "read_large_evidence",
		Desc: strings.Repeat("schema description ", 80),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"artifact_id": {Type: schema.String, Desc: "Content-addressed artifact identifier.", Required: true},
		}),
	}
	withoutTools := NewAdmissionPolicy(32_000, nil)
	withTools := NewAdmissionPolicy(32_000, []*schema.ToolInfo{tool})

	if withTools.ToolSchemaTokens <= 0 {
		t.Fatalf("tool schema estimate = %d, want positive", withTools.ToolSchemaTokens)
	}
	if withTools.EstimateRequestTokens(messages) <= withoutTools.EstimateRequestTokens(messages) {
		t.Fatalf("tool-enabled request estimate = %d, want greater than no-tools estimate %d", withTools.EstimateRequestTokens(messages), withoutTools.EstimateRequestTokens(messages))
	}
	if withTools.MaxResponseTokens(messages) >= withoutTools.MaxResponseTokens(messages) {
		t.Fatalf("tool-enabled remaining output = %d, want less than no-tools remaining output %d", withTools.MaxResponseTokens(messages), withoutTools.MaxResponseTokens(messages))
	}
}

func TestContextPlannerKeepsCompleteRecentSuffixAndCurrentLast(t *testing.T) {
	planner := NewContextPlanner(Config{
		WindowTokens: 900,
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
		WindowTokens: 400,
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
	planner := NewContextPlanner(Config{WindowTokens: 2_000})
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
