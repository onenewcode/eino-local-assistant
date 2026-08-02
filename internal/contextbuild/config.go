package contextbuild

// Config controls the context planner and compactor.
type Config struct {
	// ModelContextTokens is the provider context capacity. Zero uses 32000.
	ModelContextTokens int
	// OutputReserveTokens leaves room for the next response. Zero uses 4096.
	OutputReserveTokens int
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
	// MaxLowGainAttempts pauses automatic compaction after repeated low gain.
	// Zero uses 2.
	MaxLowGainAttempts int
	// LowGainThresholdPercent is the minimum useful release. Zero uses 15.
	LowGainThresholdPercent int
}

// DefaultConfig returns the product budget defaults.
func DefaultConfig() Config {
	return Config{
		ModelContextTokens:        32_000,
		OutputReserveTokens:       4_096,
		KeepRecentTurns:           12,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
		SummaryMaxTokens:          2_048,
		MaxLowGainAttempts:        2,
		LowGainThresholdPercent:   15,
	}
}

// Normalize fills omitted configuration values with product defaults.
func (c Config) Normalize() Config {
	out := c
	defaults := DefaultConfig()
	if out.ModelContextTokens <= 0 {
		out.ModelContextTokens = defaults.ModelContextTokens
	}
	if out.OutputReserveTokens <= 0 {
		out.OutputReserveTokens = defaults.OutputReserveTokens
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
	if out.MaxLowGainAttempts <= 0 {
		out.MaxLowGainAttempts = defaults.MaxLowGainAttempts
	}
	if out.LowGainThresholdPercent <= 0 {
		out.LowGainThresholdPercent = defaults.LowGainThresholdPercent
	}
	return out
}

// UsableInputTokens reports model capacity after the output reserve.
func (c Config) UsableInputTokens() int {
	c = c.Normalize()
	return c.ModelContextTokens - c.OutputReserveTokens
}
