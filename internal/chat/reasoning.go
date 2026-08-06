package chat

import (
	"strings"

	"github.com/cloudwego/eino/schema"
)

// Provider adapters stash display reasoning in Message.ReasoningContent and,
// for some vendors, Extra keys. These strings mirror unexported eino-ext keys
// (openai "reasoning-content", claude "_eino_claude_thinking*"). Re-check when
// upgrading github.com/cloudwego/eino-ext/... message_extra.go.
const (
	extraKeyOpenAIReasoningContent = "reasoning-content"
	extraKeyClaudeThinking         = "_eino_claude_thinking"
	extraKeyClaudeThinkingSign     = "_eino_claude_thinking_signature"
)

// ReasoningEventSource marks models whose StreamWithEvents path already
// live-emits TurnEventReasoning for every provider model call (including
// intermediate ReAct steps). Session.streamAnswer must not re-emit reasoning
// from the final answer stream for these models.
//
// The method is a Go marker (interfaces need a method); presence of the type
// assertion is the contract, not the method body.
type ReasoningEventSource interface {
	ReasoningEventsFromStreams()
}

func isReasoningExtraKey(key string) bool {
	switch key {
	case extraKeyOpenAIReasoningContent, extraKeyClaudeThinking, extraKeyClaudeThinkingSign:
		return true
	default:
		return false
	}
}

// DisplayReasoningContent returns provider-supplied reasoning text without
// treating encrypted signatures or other metadata as visible content. Eino
// adapters use more than one field, so the UI/event bridge must normalize them
// before deciding whether a reasoning event exists.
func DisplayReasoningContent(msg *schema.Message) string {
	if msg == nil {
		return ""
	}
	if msg.ReasoningContent != "" {
		return msg.ReasoningContent
	}
	for _, key := range []string{extraKeyOpenAIReasoningContent, extraKeyClaudeThinking} {
		if value, ok := msg.Extra[key].(string); ok && value != "" {
			return value
		}
	}
	var b strings.Builder
	for _, part := range msg.AssistantGenMultiContent {
		if part.Type != schema.ChatMessagePartTypeReasoning || part.Reasoning == nil {
			continue
		}
		b.WriteString(part.Reasoning.Text)
	}
	return b.String()
}

func hasDisplayReasoning(msg *schema.Message) bool {
	if msg == nil {
		return false
	}
	if DisplayReasoningContent(msg) != "" {
		return true
	}
	if msg.Extra != nil {
		for key := range msg.Extra {
			if isReasoningExtraKey(key) {
				return true
			}
		}
	}
	for _, part := range msg.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeReasoning {
			return true
		}
	}
	return false
}

// stripReasoningForStorage clears display-only reasoning before a message
// enters the durable turn ledger or is replayed into a model prompt.
// UI already observed ReasoningContent via TurnEventReasoning.
func stripReasoningForStorage(msg *schema.Message) *schema.Message {
	if msg == nil || !hasDisplayReasoning(msg) {
		return msg
	}
	cp := *msg
	cp.ReasoningContent = ""
	if msg.Extra != nil {
		extra := make(map[string]any, len(msg.Extra))
		for key, value := range msg.Extra {
			if isReasoningExtraKey(key) {
				continue
			}
			extra[key] = value
		}
		if len(extra) == 0 {
			cp.Extra = nil
		} else {
			cp.Extra = extra
		}
	}
	if len(msg.AssistantGenMultiContent) > 0 {
		parts := make([]schema.MessageOutputPart, 0, len(msg.AssistantGenMultiContent))
		for _, part := range msg.AssistantGenMultiContent {
			if part.Type == schema.ChatMessagePartTypeReasoning {
				continue
			}
			parts = append(parts, part)
		}
		if len(parts) == 0 {
			cp.AssistantGenMultiContent = nil
		} else {
			cp.AssistantGenMultiContent = parts
		}
	}
	return &cp
}

// modelEmitsReasoningEvents reports whether the chat model already surfaces
// TurnEventReasoning for every model call (see ReasoningEventSource).
func modelEmitsReasoningEvents(m Model) bool {
	_, ok := m.(ReasoningEventSource)
	return ok
}
