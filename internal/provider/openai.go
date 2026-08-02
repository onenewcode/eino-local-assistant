package provider

import (
	"context"
	"fmt"
	"time"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/config"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
)

// OpenAIModel adapts Eino's OpenAI-compatible model to the local chat session.
type OpenAIModel struct {
	client *openai.ChatModel
}

// NewOpenAIModel creates an OpenAI Chat Completions-compatible Eino client.
func NewOpenAIModel(ctx context.Context, cfg config.ModelConfig) (*OpenAIModel, error) {
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

	return &OpenAIModel{client: client}, nil
}

// Stream starts a response stream for the supplied Eino message history.
func (m *OpenAIModel) Stream(ctx context.Context, history []*schema.Message) (chat.Stream, error) {
	stream, err := m.client.Stream(ctx, history)
	if err != nil {
		return nil, err
	}
	return stream, nil
}
