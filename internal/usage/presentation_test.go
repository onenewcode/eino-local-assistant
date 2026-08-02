package usage

import (
	"strings"
	"testing"

	"eino-local-assistant/internal/store"
)

func TestFormatAPIUsageStates(t *testing.T) {
	cases := []struct {
		name string
		in   APIUsage
		want []string
		ban  []string
	}{
		{
			name: "exact no cache",
			in: APIUsage{
				PromptTokens: 1200, CompletionTokens: 34, TotalTokens: 1234, CallCount: 2, Status: store.UsageStatusExact,
			},
			// uncached input = full prompt when cached=0
			want: []string{"API usage (exact)", "input=1.2k", "completion=34", "cached=0", "total=1.2k", "calls=2"},
			ban:  []string{"prompt="},
		},
		{
			name: "incomplete includes missing call",
			in:   APIUsage{CallCount: 2, Status: store.UsageStatusIncomplete},
			want: []string{"API usage (incomplete)", "input=0", "completion=0", "cached=0", "total=0", "calls=2"},
			ban:  []string{"prompt="},
		},
		{
			name: "old metadata is unavailable rather than guessed",
			in:   APIUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, Status: store.UsageStatusUnavailable},
			want: []string{"API usage: unavailable"},
		},
		{
			name: "input is cache-miss only",
			// PromptTokens (eino full) 30692, Cached 29824 → input 868
			in: APIUsage{
				PromptTokens:     30692,
				CachedTokens:     29824,
				CompletionTokens: 477,
				TotalTokens:      31169,
				CallCount:        1,
				Status:           store.UsageStatusExact,
			},
			want: []string{"input=868", "completion=477", "cached=29.8k", "total=31.2k", "calls=1"},
			ban:  []string{"prompt=", "input=30.7k"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatAPIUsage(tc.in)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("FormatAPIUsage(%+v)=%q, missing %q", tc.in, got, want)
				}
			}
			for _, ban := range tc.ban {
				if strings.Contains(got, ban) {
					t.Fatalf("FormatAPIUsage(%+v)=%q, must not contain %q", tc.in, got, ban)
				}
			}
		})
	}
}

func TestUncachedInputTokensClampsNegative(t *testing.T) {
	v := APIUsage{PromptTokens: 10, CachedTokens: 20}
	if got := v.UncachedInputTokens(); got != 0 {
		t.Fatalf("UncachedInputTokens=%d, want 0", got)
	}
}

func TestFormatContextSnapshotSeparatesUnknownAndActual(t *testing.T) {
	if got := FormatContextSnapshot(nil); got != "context=unknown" {
		t.Fatalf("unknown context=%q", got)
	}
	snapshot := &store.ContextSnapshot{PromptTokens: 125, BudgetTokens: 100}
	if got := FormatContextSnapshot(snapshot); got != "context=125/100 (125%)" {
		t.Fatalf("context=%q", got)
	}
	if got := FormatCompactContextSnapshot(snapshot); got != "ctx=125/100 (125%)" {
		t.Fatalf("compact context=%q", got)
	}
	if got := FormatCompactContextSnapshot(nil); got != "" {
		t.Fatalf("unknown compact should omit fragment, got %q", got)
	}
	if got := FormatCompactContextSnapshot(&store.ContextSnapshot{PromptTokens: 754}); got != "ctx=754" {
		t.Fatalf("no-budget compact=%q", got)
	}
}

func TestFormatCostEstimateCarriesUsageConfidence(t *testing.T) {
	if got := FormatCostEstimate(0.0123, store.UsageStatusExact); got != "cost~=$0.012" {
		t.Fatalf("exact cost=%q", got)
	}
	if got := FormatCostEstimate(0.0123, store.UsageStatusIncomplete); got != "cost~=$0.012 (incomplete)" {
		t.Fatalf("incomplete cost=%q", got)
	}
}
