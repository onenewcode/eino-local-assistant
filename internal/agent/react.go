package agent

import (
	"context"
	"fmt"
	"unicode/utf8"

	"eino-local-assistant/internal/chat"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	cbtemplate "github.com/cloudwego/eino/utils/callbacks"
)

// MaxStep is the ReAct graph step budget (model ↔ tools loops).
// Exported so the CLI can surface it in /status without hardcoding.
const MaxStep = 8

// ReActModel adapts Eino's ReAct agent to the local chat.Model stream interface.
// Tool calls are handled inside the agent; callers only see the final assistant stream.
type ReActModel struct {
	agent *react.Agent
}

// NewReActModel builds a streaming chat model backed by Eino ReAct + tools.
func NewReActModel(ctx context.Context, chatModel model.ToolCallingChatModel, tools []tool.BaseTool) (*ReActModel, error) {
	if chatModel == nil {
		return nil, fmt.Errorf("chat model is required")
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("at least one tool is required")
	}

	ag, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: tools,
			UnknownToolsHandler: func(_ context.Context, name, _ string) (string, error) {
				return fmt.Sprintf("error: unknown tool %q", name), nil
			},
		},
		// Model -> Tools -> Model is a few graph steps; keep headroom without unbounded loops.
		MaxStep: MaxStep,
	})
	if err != nil {
		return nil, fmt.Errorf("create ReAct agent: %w", err)
	}

	return &ReActModel{agent: ag}, nil
}

// Stream runs one ReAct turn and returns the final assistant message stream.
func (m *ReActModel) Stream(ctx context.Context, messages []*schema.Message) (chat.Stream, error) {
	return m.StreamWithEvents(ctx, messages, nil)
}

// StreamWithEvents runs one ReAct turn and emits tool lifecycle events when emit is set.
func (m *ReActModel) StreamWithEvents(ctx context.Context, messages []*schema.Message, emit chat.EventEmitter) (chat.Stream, error) {
	var opts []agent.AgentOption
	if emit != nil {
		opts = append(opts, agent.WithComposeOptions(compose.WithCallbacks(toolEventCallback(emit))))
	}

	stream, err := m.agent.Stream(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

func toolEventCallback(emit chat.EventEmitter) callbacks.Handler {
	return react.BuildAgentCallback(nil, &cbtemplate.ToolCallbackHandler{
		OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *tool.CallbackInput) context.Context {
			name := ""
			if info != nil {
				name = info.Name
			}
			args := ""
			if input != nil {
				args = input.ArgumentsInJSON
			}
			// Preserve the complete observation for the session recorder. Rendering
			// is deliberately bounded later in the TUI, not at this provenance edge.
			emit(chat.TurnEvent{
				Kind:       chat.TurnEventToolStart,
				Tool:       name,
				ToolCallID: compose.GetToolCallID(ctx),
				Input:      args,
			})
			return ctx
		},
		OnEnd: func(ctx context.Context, info *callbacks.RunInfo, output *tool.CallbackOutput) context.Context {
			name := ""
			if info != nil {
				name = info.Name
			}
			response := ""
			if output != nil {
				response = output.Response
			}
			emit(chat.TurnEvent{
				Kind:       chat.TurnEventToolEnd,
				Tool:       name,
				ToolCallID: compose.GetToolCallID(ctx),
				Output:     response,
			})
			return ctx
		},
		OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			name := ""
			if info != nil {
				name = info.Name
			}
			emit(chat.TurnEvent{
				Kind:       chat.TurnEventToolError,
				Tool:       name,
				ToolCallID: compose.GetToolCallID(ctx),
				Err:        err,
			})
			return ctx
		},
	})
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	if limit < 1 {
		return ""
	}
	return string(runes[:limit-1]) + "…"
}
