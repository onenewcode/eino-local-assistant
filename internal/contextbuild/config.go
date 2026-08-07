package contextbuild

// The context policy is intentionally product-owned. A configured model only
// contributes its physical context window; users do not tune a second set of
// competing thresholds or a response reservation.
const (
	defaultWindowTokens            = 32_000
	autoCompactThresholdPercent    = 85
	requestAdmissionCeilingPercent = 95
	postCompactTargetPercent       = 50
	keepRecentTurnGroups           = 12
	checkpointTargetTokens         = 2_000
	minimumCompactionGainPercent   = 20
)

// Config controls the context planner and compactor. WindowTokens is the
// model's complete physical context window. Zero is useful to embedders and
// resolves to the internal default; normal configuration requires it.
type Config struct {
	WindowTokens int
}

// DefaultConfig returns the product policy with its default model window.
func DefaultConfig() Config {
	return Config{WindowTokens: defaultWindowTokens}
}

// Normalize fills an omitted window for direct programmatic callers.
func (c Config) Normalize() Config {
	if c.WindowTokens <= 0 {
		c.WindowTokens = defaultWindowTokens
	}
	return c
}

// AutoCompactTriggerTokens returns the fixed 85% full-window watermark.
func (c Config) AutoCompactTriggerTokens() int {
	c = c.Normalize()
	return percentTokens(c.WindowTokens, autoCompactThresholdPercent)
}

// RequestAdmissionCeilingTokens returns the fixed 95% full-window safety
// ceiling. The remaining headroom absorbs provider protocol differences and
// allows every provider to complete a response safely.
func (c Config) RequestAdmissionCeilingTokens() int {
	c = c.Normalize()
	return percentTokens(c.WindowTokens, requestAdmissionCeilingPercent)
}

// PostCompactTargetTokens returns the fixed maximum desired context size after
// a successful checkpoint.
func (c Config) PostCompactTargetTokens() int {
	c = c.Normalize()
	return percentTokens(c.WindowTokens, postCompactTargetPercent)
}

// KeepRecentTurnGroups is the fixed hot-tail size retained outside a
// checkpoint. Complete tool transactions are never split.
func (c Config) KeepRecentTurnGroups() int { return keepRecentTurnGroups }

// CheckpointTargetTokens is the fixed maximum model-visible checkpoint size.
func (c Config) CheckpointTargetTokens() int { return checkpointTargetTokens }

// MinimumCompactionGainPercent is the fixed minimum useful reduction.
func (c Config) MinimumCompactionGainPercent() int { return minimumCompactionGainPercent }
