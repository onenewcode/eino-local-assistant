package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"eino-local-assistant/internal/config"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
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
	if usesMaxCompletionTokens(cfg.Name) {
		modelConfig.MaxCompletionTokens = &cfg.Context.MaxOutputTokens
	} else {
		modelConfig.MaxTokens = &cfg.Context.MaxOutputTokens
	}

	client, err := openai.NewChatModel(ctx, modelConfig)
	if err != nil {
		return nil, fmt.Errorf("create OpenAI chat model: %w", err)
	}

	return client, nil
}

func usesMaxCompletionTokens(modelName string) bool {
	modelName = strings.ToLower(strings.TrimSpace(modelName))
	for _, family := range [...]string{"o1", "o3", "o4", "gpt-5"} {
		if matchesModelFamily(modelName, family) {
			return true
		}
	}
	return false
}

func matchesModelFamily(modelName, family string) bool {
	if modelName == family {
		return true
	}
	if !strings.HasPrefix(modelName, family) {
		return false
	}
	suffix := strings.TrimPrefix(modelName, family)
	return strings.HasPrefix(suffix, "-") || strings.HasPrefix(suffix, ".")
}
