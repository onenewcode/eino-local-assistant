package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"eino-local-assistant/internal/config"

	claude "github.com/cloudwego/eino-ext/components/model/claude"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// NewAnthropicModel creates a native Anthropic Messages API model through
// Eino's Claude adapter. The adapter converts Eino messages and tool calls to
// the provider's wire format without leaking it into the agent loop.
func NewAnthropicModel(ctx context.Context, cfg config.ModelConfig) (model.ToolCallingChatModel, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate model configuration: %w", err)
	}

	baseURL := cfg.BaseURL
	var additionalRequestFields map[string]any
	if cfg.ReasoningEffort != "" {
		additionalRequestFields = map[string]any{
			"output_config.effort": cfg.ReasoningEffort,
		}
	}
	client, err := claude.NewChatModel(ctx, &claude.Config{
		APIKey:                  cfg.APIKey,
		BaseURL:                 &baseURL,
		Model:                   cfg.Name,
		MaxTokens:               cfg.Context.MaxOutputTokens,
		RequestTimeout:          time.Duration(cfg.TimeoutSeconds) * time.Second,
		AdditionalRequestFields: additionalRequestFields,
	})
	if err != nil {
		return nil, fmt.Errorf("create Anthropic Messages chat model: %w", err)
	}

	return &anthropicModel{delegate: client}, nil
}

// anthropicModel applies the small amount of canonical-message normalization
// that the generic Eino ledger needs before entering the Claude adapter.
// The adapter itself owns Anthropic HTTP, SSE, tools, and usage conversion.
type anthropicModel struct {
	delegate model.ToolCallingChatModel
}

var _ model.ToolCallingChatModel = (*anthropicModel)(nil)

func (m *anthropicModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.delegate.Generate(ctx, normalizeAnthropicMessages(input), opts...)
}

func (m *anthropicModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.delegate.Stream(ctx, normalizeAnthropicMessages(input), opts...)
}

func (m *anthropicModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.delegate.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &anthropicModel{delegate: bound}, nil
}

func (m *anthropicModel) GetType() string {
	if typer, ok := m.delegate.(components.Typer); ok {
		return typer.GetType()
	}
	return "Anthropic"
}

func (m *anthropicModel) IsCallbacksEnabled() bool {
	return components.IsCallbacksEnabled(m.delegate)
}

func normalizeAnthropicMessages(input []*schema.Message) []*schema.Message {
	system := make([]*schema.Message, 0)
	messages := make([]*schema.Message, 0, len(input))

	for _, message := range input {
		if message == nil {
			continue
		}
		messageCopy := *message
		if messageCopy.Role == schema.Tool && len(messageCopy.UserInputMultiContent) == 0 && len(messageCopy.MultiContent) == 0 {
			// Route all plain tool output through the adapter's tool-result path.
			// In particular, an empty Content value must not become plain user text.
			messageCopy.UserInputMultiContent = []schema.MessageInputPart{{
				Type: schema.ChatMessagePartTypeText,
				Text: messageCopy.Content,
			}}
		}
		if messageCopy.Role == schema.Assistant && len(messageCopy.ToolCalls) > 0 {
			messageCopy.ToolCalls = append([]schema.ToolCall(nil), messageCopy.ToolCalls...)
			for i := range messageCopy.ToolCalls {
				arguments := messageCopy.ToolCalls[i].Function.Arguments
				if !isJSONObject(arguments) {
					// Historical OpenAI-compatible endpoints may have accepted an
					// arbitrary string (or omitted arguments) here. Anthropic
					// requires tool input objects.
					messageCopy.ToolCalls[i].Function.Arguments = "{}"
				}
			}
		}
		if messageCopy.Role == schema.System {
			system = append(system, &messageCopy)
			continue
		}
		messages = append(messages, &messageCopy)
	}

	return append(system, messages...)
}

func isJSONObject(value string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &object); err != nil {
		return false
	}
	return object != nil && strings.HasPrefix(strings.TrimSpace(value), "{")
}
