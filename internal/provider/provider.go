package provider

import (
	"context"
	"fmt"

	"eino-local-assistant/internal/config"

	"github.com/cloudwego/eino/components/model"
)

// NewChatModel selects the configured wire-protocol adapter while preserving
// Eino's provider-neutral ToolCallingChatModel contract for the rest of the app.
func NewChatModel(ctx context.Context, cfg config.ModelConfig) (model.ToolCallingChatModel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate model configuration: %w", err)
	}

	switch cfg.Provider {
	case "", config.ProviderOpenAI:
		return NewOpenAIModel(ctx, cfg)
	case config.ProviderAnthropic:
		return NewAnthropicModel(ctx, cfg)
	default:
		// Keep this defensive branch for callers that construct ModelConfig
		// directly instead of loading YAML through config.Load.
		return nil, fmt.Errorf("unsupported model.provider %q", cfg.Provider)
	}
}
