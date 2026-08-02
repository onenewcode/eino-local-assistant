package provider

import (
	"context"
	"fmt"
	"time"

	"eino-local-assistant/internal/config"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

// NewOpenAIModel creates an OpenAI Chat Completions-compatible ToolCallingChatModel.
func NewOpenAIModel(ctx context.Context, cfg config.ModelConfig) (model.ToolCallingChatModel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate model configuration: %w", err)
	}

	client, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  cfg.APIKey,
		Model:   cfg.Name,
		BaseURL: cfg.BaseURL,
		Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		ByAzure: false,
	})
	if err != nil {
		return nil, fmt.Errorf("create OpenAI-compatible chat model: %w", err)
	}

	return client, nil
}
