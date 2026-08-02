package usage

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestFromMessageUsage(t *testing.T) {
	msg := &schema.Message{
		Role: schema.Assistant,
		ResponseMeta: &schema.ResponseMeta{
			Usage: &schema.TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 20,
				TotalTokens:      120,
			},
		},
	}
	turn, ok := FromMessageUsage(msg)
	if !ok || turn.PromptTokens != 100 || turn.CompletionTokens != 20 {
		t.Fatalf("turn=%+v ok=%v", turn, ok)
	}
}

func TestFromTokenUsageKeepsExplicitZeroReport(t *testing.T) {
	turn, ok := FromTokenUsage(&schema.TokenUsage{})
	if !ok || turn != (Turn{}) {
		t.Fatalf("turn=%+v ok=%v", turn, ok)
	}
}

func TestEstimateAndCost(t *testing.T) {
	msgs := []*schema.Message{
		schema.SystemMessage("hello world"),
		schema.UserMessage("abcdefg hijklmn"),
	}
	n := EstimateMessages(msgs)
	if n <= 0 {
		t.Fatalf("estimate=%d", n)
	}
	cost := CostUSD(1_000_000, 1_000_000, Pricing{InputPerMillion: 1, OutputPerMillion: 2})
	if cost != 3 {
		t.Fatalf("cost=%v want 3", cost)
	}
}

func TestFormatHelpers(t *testing.T) {
	if FormatTokens(500) != "500" {
		t.Fatalf("FormatTokens(500)=%s", FormatTokens(500))
	}
	if FormatUSD(0.0012) != "$0.0012" {
		t.Fatalf("FormatUSD=%s", FormatUSD(0.0012))
	}
}
