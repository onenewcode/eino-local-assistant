package tui

import (
	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"
)

func sessionAPIUsage(session *chat.Session) usage.APIUsage {
	if session == nil {
		return usage.APIUsage{Status: store.UsageStatusUnavailable}
	}
	summary := session.UsageSummary()
	return usage.APIUsage{
		PromptTokens:     summary.PromptTokens,
		CompletionTokens: summary.CompletionTokens,
		CachedTokens:     summary.CachedTokens,
		TotalTokens:      summary.TotalTokens,
		CallCount:        summary.ModelCallCount,
		CostUSD:          summary.CostUSD,
		Status:           summary.Status,
	}
}

func sessionContextSnapshot(session *chat.Session) *store.ContextSnapshot {
	if session == nil {
		return nil
	}
	status := session.ContextStatus()
	if !status.MeasuredKnown {
		return nil
	}
	budget := status.MeasuredWindowTokens
	if budget == 0 {
		budget = session.ContextConfig().WindowTokens
	}
	return &store.ContextSnapshot{PromptTokens: status.MeasuredTokens, WindowTokens: budget}
}
