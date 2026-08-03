package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	cbtemplate "github.com/cloudwego/eino/utils/callbacks"
)

const (
	// DefaultMaxStep is the ReAct graph step budget used unless configured.
	DefaultMaxStep = 8
	// MaxStep preserves the legacy default for callers that only need to show a
	// static status value. New construction should use ReActOptions instead.
	MaxStep = DefaultMaxStep
)

// ReActOptions configures a ReAct model. Zero values use the package defaults;
// negative values are rejected.
type ReActOptions struct {
	// MaxStep bounds model and tool graph iterations in one ReAct turn.
	MaxStep int
	// TaskController enables the optional autonomous-task runtime. It owns
	// controller state while chat.Session owns the durable conversation ledger.
	TaskController *TaskController
}

// DefaultReActOptions returns the options used by NewReActModel.
func DefaultReActOptions() ReActOptions {
	return ReActOptions{MaxStep: DefaultMaxStep}
}

// ReActModel adapts Eino's ReAct agent to the local chat.Model stream interface.
// Tool calls are handled inside the agent; callers only see the final assistant stream.
type ReActModel struct {
	agent          *react.Agent
	maxStep        int
	taskController *TaskController
}

// NewReActModel builds a streaming chat model backed by Eino ReAct + tools.
func NewReActModel(ctx context.Context, chatModel model.ToolCallingChatModel, tools []tool.BaseTool) (*ReActModel, error) {
	return NewReActModelWithOptions(ctx, chatModel, tools, DefaultReActOptions())
}

// NewReActModelWithOptions builds a streaming chat model with explicit ReAct
// execution options.
func NewReActModelWithOptions(ctx context.Context, chatModel model.ToolCallingChatModel, tools []tool.BaseTool, opts ReActOptions) (*ReActModel, error) {
	opts, err := normalizeReActOptions(opts)
	if err != nil {
		return nil, err
	}
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
			// Eino's Claude adapter emits an empty argument string for a streamed
			// tool_use whose input is {}. Inferred Go tools still require JSON.
			ToolArgumentsHandler: normalizeToolArguments,
		},
		// DeepSeek and similar providers often stream assistant text before
		// tool_calls. Eino's default first-chunk checker would END early and
		// leave a dangling tool_calls assistant in history.
		StreamToolCallChecker: contentThenToolStreamToolCallChecker,
		// Model -> Tools -> Model is a few graph steps; keep headroom without unbounded loops.
		MaxStep: opts.MaxStep,
	})
	if err != nil {
		return nil, fmt.Errorf("create ReAct agent: %w", err)
	}

	return &ReActModel{agent: ag, maxStep: opts.MaxStep, taskController: opts.TaskController}, nil
}

func normalizeReActOptions(opts ReActOptions) (ReActOptions, error) {
	if opts.MaxStep < 0 {
		return ReActOptions{}, fmt.Errorf("max step must be >= 0")
	}
	if opts.MaxStep == 0 {
		opts.MaxStep = DefaultReActOptions().MaxStep
	}
	return opts, nil
}

// MaxSteps returns the effective ReAct graph step budget for this model.
func (m *ReActModel) MaxSteps() int {
	if m == nil {
		return 0
	}
	return m.maxStep
}

func normalizeToolArguments(_ context.Context, _ string, arguments string) (string, error) {
	if strings.TrimSpace(arguments) == "" {
		return "{}", nil
	}
	return arguments, nil
}

// Stream runs one ReAct turn and returns the final assistant message stream.
func (m *ReActModel) Stream(ctx context.Context, messages []*schema.Message) (chat.Stream, error) {
	options := make([]agent.AgentOption, 0, 1)
	if m.taskController != nil {
		// Ask() has no UI emitter, but task proof acceptance still needs the
		// immutable shell observation from the callback.
		options = append(options, agent.WithComposeOptions(compose.WithCallbacks(toolEventCallback(nil, m.taskController))))
	}
	stream, err := m.agent.Stream(ctx, m.messagesWithTaskPacket(ctx, messages), options...)
	if err != nil {
		return nil, err
	}
	return stream, nil
}

// ReasoningEventsFromStreams marks ReActModel as a chat.ReasoningEventSource:
// observeModelStreams live-emits TurnEventReasoning for every model call.
func (m *ReActModel) ReasoningEventsFromStreams() {}

// StreamWithEvents runs one ReAct turn and emits tool lifecycle events when emit is set.
func (m *ReActModel) StreamWithEvents(ctx context.Context, messages []*schema.Message, emit chat.EventEmitter) (chat.Stream, <-chan struct{}, error) {
	if emit == nil {
		stream, err := m.Stream(ctx, messages)
		return stream, nil, err
	}
	usageOption, future := react.WithMessageFuture()
	opts := []agent.AgentOption{usageOption}

	if m.taskController != nil {
		opts = append(opts, agent.WithComposeOptions(compose.WithCallbacks(toolEventCallback(emit, m.taskController))))
	} else {
		opts = append(opts, agent.WithComposeOptions(compose.WithCallbacks(toolEventCallback(emit, nil))))
	}

	stream, err := m.agent.Stream(ctx, m.messagesWithTaskPacket(ctx, messages), opts...)
	if err != nil {
		return nil, nil, err
	}

	// The agent exposes only its final stream. MessageFuture gives the observer a
	// copy of every model stream inside the ReAct loop, including tool-planning
	// calls that would otherwise disappear from session accounting and UI.
	done := observeModelStreams(future, emit)
	return &usageTrackingStream{stream: stream, done: done}, done, nil
}

type usageTrackingStream struct {
	stream *schema.StreamReader[*schema.Message]
	done   <-chan struct{}
	once   sync.Once
}

func (s *usageTrackingStream) Recv() (*schema.Message, error) {
	message, err := s.stream.Recv()
	if err != nil {
		// Do not let Session commit or fail a turn before the copied model
		// streams have published their usage observations.
		s.waitForUsage()
	}
	return message, err
}

func (s *usageTrackingStream) Close() {
	s.once.Do(func() {
		s.stream.Close()
		// An interrupted display stream can still have completed model calls in
		// the copied futures. Wait before Session closes the durable turn.
		s.waitForUsage()
	})
}

func (s *usageTrackingStream) waitForUsage() {
	if s.done == nil {
		return
	}
	<-s.done
}

// observeModelStreams drains every MessageFuture model stream: live-emits
// TurnEventReasoning deltas and records per-call usage when the stream ends.
func observeModelStreams(future react.MessageFuture, emit chat.EventEmitter) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		iterator := future.GetMessageStreams()
		var callNumber uint64
		for {
			stream, ok, err := iterator.Next()
			if err != nil || !ok {
				return
			}
			callID := fmt.Sprintf("model-%d", atomic.AddUint64(&callNumber, 1))
			observeOneModelStream(stream, callID, emit)
		}
	}()
	return done
}

func observeOneModelStream(stream *schema.StreamReader[*schema.Message], callID string, emit chat.EventEmitter) {
	if stream == nil {
		return
	}
	defer stream.Close()

	chunks := make([]*schema.Message, 0)
	hasModelChunk := false
	hasToolChunk := false
	var streamErr error
	for {
		message, err := stream.Recv()
		if message != nil {
			chunks = append(chunks, message)
			switch message.Role {
			case schema.Tool:
				hasToolChunk = true
			case schema.Assistant, "":
				hasModelChunk = true
				// Live-emit reasoning from every model call (including the final
				// one). Session skips final-stream re-emit for ReasoningEventSource.
				if emit != nil && message.ReasoningContent != "" {
					emit(chat.TurnEvent{
						Kind:  chat.TurnEventReasoning,
						Chunk: message.ReasoningContent,
					})
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			streamErr = err
			break
		}
	}
	// MessageFuture forwards tool-result streams too. Only an explicit tool
	// message identifies one; model adapters may omit Role on later chunks.
	if len(chunks) == 0 || hasToolChunk || !hasModelChunk {
		return
	}

	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		turn, available := usageFromChunks(chunks)
		emitModelUsage(emit, callID, turn, available)
		return
	}
	turn, available := usage.FromMessageUsage(message)
	if streamErr != nil && !available {
		// A partial model response proves the request started, but an errored
		// stream cannot be considered exact without a provider usage report.
		emitModelUsage(emit, callID, usage.Turn{}, false)
		return
	}
	emitModelUsage(emit, callID, turn, available)
}

func usageFromChunks(chunks []*schema.Message) (usage.Turn, bool) {
	for i := len(chunks) - 1; i >= 0; i-- {
		if turn, available := usage.FromMessageUsage(chunks[i]); available {
			return turn, true
		}
	}
	return usage.Turn{}, false
}

func emitModelUsage(emit chat.EventEmitter, callID string, turn usage.Turn, available bool) {
	if emit == nil {
		return
	}
	emit(chat.TurnEvent{
		Kind: chat.TurnEventModelUsage,
		ModelUsage: &chat.ModelUsageEvent{
			CallID:    callID,
			Operation: chat.ModelUsageOperationAgent,
			Usage:     turn,
			Available: available,
		},
	})
}

func toolEventCallback(emit chat.EventEmitter, taskController *TaskController) callbacks.Handler {
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
			if emit != nil {
				emit(chat.TurnEvent{
					Kind:       chat.TurnEventToolStart,
					Tool:       name,
					ToolCallID: compose.GetToolCallID(ctx),
					Input:      args,
				})
			}
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
			if emit != nil {
				emit(chat.TurnEvent{
					Kind:       chat.TurnEventToolEnd,
					Tool:       name,
					ToolCallID: compose.GetToolCallID(ctx),
					Output:     response,
				})
			}
			if taskController != nil {
				// The durable tool-completed event is recorded synchronously by the
				// session emitter before controller accepts it as task evidence.
				taskController.RecordToolResult(ctx, name, compose.GetToolCallID(ctx), "", response)
			}
			return ctx
		},
		OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			name := ""
			if info != nil {
				name = info.Name
			}
			if emit != nil {
				emit(chat.TurnEvent{
					Kind:       chat.TurnEventToolError,
					Tool:       name,
					ToolCallID: compose.GetToolCallID(ctx),
					Err:        err,
				})
			}
			if taskController != nil && (name == "apply_patch" || name == "shell") {
				// A patch operation can fail after an earlier edit, and an execution
				// error can arrive after a shell process started. Treat both as
				// uncertain workspace mutations before accepting old proof again.
				failure := "tool error"
				if err != nil {
					failure += ": " + err.Error()
				}
				taskController.RecordToolResult(ctx, name, compose.GetToolCallID(ctx), "", failure)
			}
			return ctx
		},
	})
}

// messagesWithTaskPacket places controller-owned task state after the durable
// system prefix. It is deliberately ephemeral: compaction and session replay
// never need to treat the packet as a user-authored transcript message.
func (m *ReActModel) messagesWithTaskPacket(ctx context.Context, messages []*schema.Message) []*schema.Message {
	if m == nil || m.taskController == nil {
		return messages
	}
	packet := strings.TrimSpace(m.taskController.ExecutionPacket(ctx))
	if packet == "" {
		return messages
	}
	insertAt := 0
	for insertAt < len(messages) && messages[insertAt] != nil && messages[insertAt].Role == schema.System {
		insertAt++
	}
	withPacket := make([]*schema.Message, 0, len(messages)+1)
	withPacket = append(withPacket, messages[:insertAt]...)
	withPacket = append(withPacket, schema.SystemMessage(packet))
	withPacket = append(withPacket, messages[insertAt:]...)
	return withPacket
}

// TaskExecutionStatus, TaskCompletionGate, and InterruptTask satisfy the
// optional chat.TaskRuntime contract without making chat depend on agent.
func (m *ReActModel) TaskExecutionStatus(ctx context.Context) chat.TaskRunStatus {
	if m == nil || m.taskController == nil {
		return chat.TaskRunStatus{}
	}
	return m.taskController.TaskExecutionStatus(ctx)
}

func (m *ReActModel) TaskCompletionGate(ctx context.Context) chat.TaskCompletionGate {
	if m == nil || m.taskController == nil {
		return chat.TaskCompletionGate{}
	}
	return m.taskController.TaskCompletionGate(ctx)
}

func (m *ReActModel) InterruptTask(ctx context.Context, reason string) chat.TaskInterruptReceipt {
	if m == nil || m.taskController == nil {
		return chat.TaskInterruptReceipt{Summary: "task runtime is unavailable"}
	}
	return m.taskController.InterruptTask(ctx, reason)
}

// AbortTaskCompletion implements chat.TaskCompletionRevoker. It is invoked by
// Session only when the turn that obtained completion approval cannot commit
// its final delivery.
func (m *ReActModel) AbortTaskCompletion(ctx context.Context, reason string) chat.TaskInterruptReceipt {
	if m == nil || m.taskController == nil {
		return chat.TaskInterruptReceipt{Summary: "task runtime is unavailable"}
	}
	return m.taskController.AbortTaskCompletion(ctx, reason)
}

// contentThenToolStreamToolCallChecker reports whether a model stream contains
// tool calls anywhere, not only in the first non-empty content chunk.
//
// The checker must close modelOutput before returning (Eino contract).
func contentThenToolStreamToolCallChecker(_ context.Context, sr *schema.StreamReader[*schema.Message]) (bool, error) {
	defer sr.Close()
	for {
		msg, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if msg != nil && len(msg.ToolCalls) > 0 {
			return true, nil
		}
	}
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
