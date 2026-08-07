package usage

import (
	"fmt"

	"eino-local-assistant/internal/store"
)

// APIUsage is the presentation-relevant portion of a session's durable usage.
// CallCount includes calls that omitted provider usage; their missing counts are
// represented by StatusIncomplete rather than local estimates.
//
// Struct fields match eino schema.TokenUsage (PromptTokens, CompletionTokens,
// CachedTokens, TotalTokens). User-facing "input" is the cache-miss portion:
// max(0, PromptTokens - CachedTokens) — never labeled "prompt".
type APIUsage struct {
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	TotalTokens      int
	CallCount        int
	CostUSD          float64
	Status           store.UsageStatus
}

func (v APIUsage) emptyExact() bool {
	return v.CallCount == 0 && v.PromptTokens == 0 && v.CompletionTokens == 0 &&
		v.TotalTokens == 0 && v.CachedTokens == 0
}

// UncachedInputTokens is the non-cache-hit share of provider input.
// Aligns with product "输入" (cache miss), not the full eino PromptTokens total.
func (v APIUsage) UncachedInputTokens() int {
	in := v.PromptTokens - v.CachedTokens
	if in < 0 {
		return 0
	}
	return in
}

// APIUsageFromMeta adapts the durable thread projection for a display surface.
func APIUsageFromMeta(meta store.ThreadMeta) APIUsage {
	return APIUsage{
		PromptTokens:     meta.PromptTokens,
		CompletionTokens: meta.CompletionTokens,
		CachedTokens:     meta.CachedTokens,
		TotalTokens:      meta.TotalTokens,
		CallCount:        meta.ModelCallCount,
		CostUSD:          meta.CostUSD,
		Status:           meta.UsageStatus,
	}
}

// FormatAPIUsage reports provider-derived API accounting for UI surfaces.
// Labels: input (uncached), completion, cached, total, calls.
// Never prints "prompt=" — PromptTokens remains the internal eino field only.
func FormatAPIUsage(v APIUsage) string {
	status := normalizedStatus(v.Status)
	if status == store.UsageStatusUnavailable {
		return "API usage: unavailable"
	}
	if status == store.UsageStatusExact && v.emptyExact() {
		return fmt.Sprintf("API usage (%s): none yet", status)
	}
	return fmt.Sprintf("API usage (%s): input=%s completion=%s cached=%s total=%s calls=%d",
		status,
		FormatTokens(v.UncachedInputTokens()),
		FormatTokens(v.CompletionTokens),
		FormatTokens(v.CachedTokens),
		FormatTokens(v.TotalTokens),
		v.CallCount,
	)
}

// FormatCostEstimate makes clear that configured local pricing is not an API
// invoice. An incomplete token basis is marked accordingly; unavailable old
// metadata has no cost display at all.
func FormatCostEstimate(costUSD float64, status store.UsageStatus) string {
	status = normalizedStatus(status)
	if status == store.UsageStatusUnavailable {
		return "cost~=unavailable"
	}
	suffix := ""
	if status != store.UsageStatusExact {
		suffix = " (" + string(status) + ")"
	}
	return "cost~=" + FormatUSD(costUSD) + suffix
}

// FormatContextSnapshot reports the last provider-derived primary prompt
// measurement (ContextSnapshot.PromptTokens = full window occupancy).
// Independent of planner estimate and of the uncached-input bill split.
func FormatContextSnapshot(snapshot *store.ContextSnapshot) string {
	if snapshot == nil {
		return "context=unknown"
	}
	if snapshot.WindowTokens <= 0 {
		return "context=" + FormatTokens(snapshot.PromptTokens)
	}
	pct := snapshot.PromptTokens * 100 / snapshot.WindowTokens
	return fmt.Sprintf("context=%s/%s (%d%%)",
		FormatTokens(snapshot.PromptTokens),
		FormatTokens(snapshot.WindowTokens),
		pct,
	)
}

// FormatCompactContextSnapshot is the status-bar form of the last measured
// provider request. Returns "" when no trustworthy measurement exists so
// callers can omit the fragment instead of showing a placeholder.
// Uses full PromptTokens for window occupancy (not uncached input).
func FormatCompactContextSnapshot(snapshot *store.ContextSnapshot) string {
	if snapshot == nil {
		return ""
	}
	if snapshot.WindowTokens <= 0 {
		return "ctx=" + FormatTokens(snapshot.PromptTokens)
	}
	pct := snapshot.PromptTokens * 100 / snapshot.WindowTokens
	return fmt.Sprintf("ctx=%s/%s (%d%%)",
		FormatTokens(snapshot.PromptTokens),
		FormatTokens(snapshot.WindowTokens),
		pct,
	)
}

// FormatCompactEstimatedContext is the status-bar form of a locally planned
// request. The approximate marker distinguishes it from provider usage.
func FormatCompactEstimatedContext(tokens, windowTokens int) string {
	if windowTokens <= 0 {
		return "ctx≈" + FormatTokens(tokens)
	}
	pct := tokens * 100 / windowTokens
	return fmt.Sprintf("ctx≈%s/%s (%d%%)",
		FormatTokens(tokens),
		FormatTokens(windowTokens),
		pct,
	)
}

func normalizedStatus(status store.UsageStatus) store.UsageStatus {
	switch status {
	case store.UsageStatusExact, store.UsageStatusIncomplete, store.UsageStatusUnavailable:
		return status
	default:
		// Old materialized metadata lacks one-record-per-call accounting, so its
		// totals cannot be honestly rendered as current API usage.
		return store.UsageStatusUnavailable
	}
}
