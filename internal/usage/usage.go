// Package usage estimates tokens and converts them to cost.
package usage

import (
	"math"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
)

// Pricing is USD per 1 million tokens.
type Pricing struct {
	InputPerMillion  float64
	OutputPerMillion float64
}

// Turn holds token accounting for one model call.
type Turn struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Estimated        bool
	CostUSD          float64
}

// FromMessageUsage prefers provider-reported usage on the assistant message.
func FromMessageUsage(msg *schema.Message) (Turn, bool) {
	if msg == nil || msg.ResponseMeta == nil || msg.ResponseMeta.Usage == nil {
		return Turn{}, false
	}
	u := msg.ResponseMeta.Usage
	t := Turn{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	if t.TotalTokens == 0 {
		t.TotalTokens = t.PromptTokens + t.CompletionTokens
	}
	if t.PromptTokens == 0 && t.CompletionTokens == 0 && t.TotalTokens == 0 {
		return Turn{}, false
	}
	return t, true
}

// EstimateMessages approximates prompt tokens for a message list.
func EstimateMessages(msgs []*schema.Message) int {
	total := 0
	for _, m := range msgs {
		if m == nil {
			continue
		}
		total += EstimateText(m.Content)
		total += EstimateText(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			total += EstimateText(tc.Function.Name)
			total += EstimateText(tc.Function.Arguments)
			total += 8 // framing overhead
		}
		total += 4 // role framing
	}
	return total
}

// EstimateText uses a cheap rune-based heuristic (~4 runes/token for mixed CJK/Latin).
func EstimateText(s string) int {
	if s == "" {
		return 0
	}
	n := utf8.RuneCountInString(s)
	// Slightly conservative for CJK-heavy text.
	est := (n + 3) / 4
	if est < 1 {
		return 1
	}
	return est
}

// EstimateTurn builds usage when the provider did not return token counts.
func EstimateTurn(prompt []*schema.Message, completion *schema.Message) Turn {
	promptTok := EstimateMessages(prompt)
	compTok := 0
	if completion != nil {
		compTok = EstimateText(completion.Content) + EstimateText(completion.ReasoningContent)
	}
	return Turn{
		PromptTokens:     promptTok,
		CompletionTokens: compTok,
		TotalTokens:      promptTok + compTok,
		Estimated:        true,
	}
}

// CostUSD computes dollar cost from token counts and pricing.
func CostUSD(promptTokens, completionTokens int, p Pricing) float64 {
	if p.InputPerMillion <= 0 && p.OutputPerMillion <= 0 {
		return 0
	}
	cost := float64(promptTokens)/1_000_000*p.InputPerMillion +
		float64(completionTokens)/1_000_000*p.OutputPerMillion
	return roundCost(cost)
}

func roundCost(v float64) float64 {
	// Microdollar precision is enough for UI.
	return math.Round(v*1_000_000) / 1_000_000
}

// FormatUSD formats a cost for status lines.
func FormatUSD(v float64) string {
	if v == 0 {
		return "$0"
	}
	if v < 0.01 {
		return sprintf("$%.4f", v)
	}
	return sprintf("$%.3f", v)
}

// FormatTokens shortens large counts: 15300 -> 15.3k
func FormatTokens(n int) string {
	if n < 1000 {
		return sprintf("%d", n)
	}
	if n < 100_000 {
		return sprintf("%.1fk", float64(n)/1000)
	}
	return sprintf("%.0fk", float64(n)/1000)
}

// tiny sprintf helper to avoid fmt in hot docs — use fmt for simplicity.
func sprintf(format string, args ...any) string {
	return sprintfImpl(format, args...)
}
