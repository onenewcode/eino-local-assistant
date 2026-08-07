package provider

import (
	"context"
	"fmt"
	"time"

	"eino-local-assistant/internal/config"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// NewOpenAIModel creates a Chat Completions ToolCallingChatModel.
func NewOpenAIModel(ctx context.Context, cfg config.ModelConfig) (model.ToolCallingChatModel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate model configuration: %w", err)
	}

	modelConfig := &openai.ChatModelConfig{
		APIKey:  cfg.APIKey,
		Model:   cfg.Name,
		BaseURL: cfg.BaseURL,
		Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
		ByAzure: false,
	}
	if cfg.ReasoningEffort != "" {
		modelConfig.ReasoningEffort = openai.ReasoningEffortLevel(cfg.ReasoningEffort)
	}
	client, err := openai.NewChatModel(ctx, modelConfig)
	if err != nil {
		return nil, fmt.Errorf("create OpenAI chat model: %w", err)
	}

	return &openAIModel{delegate: client}, nil
}

// openAIModel keeps an explicit reference to the original unbound client.
// The Eino OpenAI adapter rejects WithTools(nil), but ReAct needs an unbound
// model for its final response after the tool-planning budget is exhausted.
// Returning the original client is also the correct OpenAI wire behavior: the
// final request omits the tools field rather than merely asking the model not
// to use an already-bound tool list.
type openAIModel struct {
	delegate model.ToolCallingChatModel
	unbound  *openAIModel
}

var _ model.ToolCallingChatModel = (*openAIModel)(nil)

func (m *openAIModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.delegate.Generate(ctx, input, opts...)
}

func (m *openAIModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.delegate.Stream(ctx, input, opts...)
}

func (m *openAIModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	base := m
	if m.unbound != nil {
		base = m.unbound
	}
	if len(tools) == 0 {
		return base, nil
	}
	bound, err := base.delegate.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &openAIModel{delegate: bound, unbound: base}, nil
}

func (m *openAIModel) GetType() string {
	if typer, ok := m.delegate.(components.Typer); ok {
		return typer.GetType()
	}
	return "OpenAI"
}

func (m *openAIModel) IsCallbacksEnabled() bool {
	return components.IsCallbacksEnabled(m.delegate)
}
