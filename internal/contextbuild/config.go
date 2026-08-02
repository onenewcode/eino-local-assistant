package contextbuild

// Config controls the context planner and compactor.
type Config struct {
	// WindowTokens is the provider context capacity. Zero uses 32000.
	WindowTokens int
	// MaxOutputTokens limits the next response and is reserved from the prompt.
	// Zero uses 4096 for direct programmatic callers.
	MaxOutputTokens int
	// KeepRecentTurns is the complete turn-group hot window. Zero uses 12.
	KeepRecentTurns int
	// AutoCompactTriggerPercent starts automatic compaction at this percentage
	// of usable input capacity. Zero uses 75.
	AutoCompactTriggerPercent int
	// PostCompactTargetPercent is the desired utilization after a checkpoint.
	// Zero uses 45.
	PostCompactTargetPercent int
	// SummaryMaxTokens caps the model-visible structured checkpoint. Zero uses 2048.
	SummaryMaxTokens int
	// LowGainThresholdPercent is the minimum useful release for installing a
	// checkpoint. Zero uses 15. How many automatic low-gain failures are
	// tolerated before pause is session/runtime policy, not planner budget.
	LowGainThresholdPercent int
}

// DefaultConfig returns the product budget defaults.
func DefaultConfig() Config {
	return Config{
		WindowTokens:              32_000,
		MaxOutputTokens:           4_096,
		KeepRecentTurns:           12,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
		SummaryMaxTokens:          2_048,
		LowGainThresholdPercent:   15,
	}
}

// Normalize fills omitted configuration values with product defaults.
func (c Config) Normalize() Config {
	out := c
	defaults := DefaultConfig()
	if out.WindowTokens <= 0 {
		out.WindowTokens = defaults.WindowTokens
	}
	if out.MaxOutputTokens <= 0 {
		out.MaxOutputTokens = defaults.MaxOutputTokens
	}
	if out.KeepRecentTurns <= 0 {
		out.KeepRecentTurns = defaults.KeepRecentTurns
	}
	if out.AutoCompactTriggerPercent <= 0 {
		out.AutoCompactTriggerPercent = defaults.AutoCompactTriggerPercent
	}
	if out.PostCompactTargetPercent <= 0 {
		out.PostCompactTargetPercent = defaults.PostCompactTargetPercent
	}
	if out.SummaryMaxTokens <= 0 {
		out.SummaryMaxTokens = defaults.SummaryMaxTokens
	}
	if out.LowGainThresholdPercent <= 0 {
		out.LowGainThresholdPercent = defaults.LowGainThresholdPercent
	}
	return out
}

// UsableInputTokens reports model capacity after the maximum response budget.
func (c Config) UsableInputTokens() int {
	c = c.Normalize()
	return c.WindowTokens - c.MaxOutputTokens
}
