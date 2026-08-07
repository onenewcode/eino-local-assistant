package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/logging"
	"eino-local-assistant/internal/runtimeguard"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	cbtemplate "github.com/cloudwego/eino/utils/callbacks"
)

const (
	// DefaultMaxModelSteps bounds tool-enabled model decisions in one turn.
	// A tools-disabled final response is reserved separately when the last
	// permitted decision requests tools.
	DefaultMaxModelSteps = 8

	finalModelInstruction = "Tool-planning budget is exhausted. Use the observations already present and answer the user now. Do not request tools."
	finalModelFallback    = "Tool-planning budget reached before a final answer was produced from the available evidence."
)

// ReActOptions configures a ReAct model. Zero values use the package defaults;
// negative values are rejected.
type ReActOptions struct {
	// MaxModelSteps bounds tool-enabled model decisions in one ReAct turn.
	// A response with multiple parallel tool calls still consumes one step.
	MaxModelSteps int
	// Admission is the private full-window request policy. A zero window leaves
	// admission disabled for standalone embeddings that manage their own limit.
	Admission contextbuild.AdmissionPolicy
	// EnableSteer opts this ReAct implementation into the explicit
	// chat.TurnSteerModel contract. The production default is enabled so the
	// formal runtime constructor does not leave Session.Steer unreachable.
	EnableSteer bool
	// DisableSteer is the explicit compatibility escape hatch for an embedding
	// that must not expose steer. It takes precedence over EnableSteer.
	DisableSteer bool
	// TaskController enables the optional autonomous-task runtime. It owns
	// controller state while chat.Session owns the durable conversation ledger.
	TaskController *TaskController
}

// DefaultReActOptions returns the options used by NewReActModel.
func DefaultReActOptions() ReActOptions {
	return ReActOptions{MaxModelSteps: DefaultMaxModelSteps, EnableSteer: true}
}

// ReActModel drives a bounded tool-calling loop over Eino model and tool
// components. Product model steps deliberately do not depend on Eino Pregel
// graph-node scheduling.
type ReActModel struct {
	toolModel           model.ToolCallingChatModel
	finalModel          model.ToolCallingChatModel
	toolsNode           *compose.ToolsNode
	executableToolNames map[string]struct{}
	maxModelSteps       int
	admission           contextbuild.AdmissionPolicy
	taskController      *TaskController
	steer               *turnSteerRegistry
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

	steer := (*turnSteerRegistry)(nil)
	if opts.EnableSteer {
		steer = newTurnSteerRegistry()
	}
	toolInfos := make([]*schema.ToolInfo, 0, len(tools))
	executableToolNames := make(map[string]struct{}, len(tools))
	for index, base := range tools {
		info, infoErr := base.Info(ctx)
		if infoErr != nil {
			return nil, fmt.Errorf("get tool info at index %d: %w", index, infoErr)
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("tool info at index %d has no name", index)
		}
		toolInfos = append(toolInfos, info)
		executableToolNames[info.Name] = struct{}{}
	}
	admission := opts.Admission
	if admission.Enabled() {
		admission = contextbuild.NewAdmissionPolicy(admission.WindowTokens, toolInfos)
	}
	toolModel, err := chatModel.WithTools(toolInfos)
	if err != nil {
		return nil, fmt.Errorf("bind tool-calling model: %w", err)
	}
	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
		Tools: tools,
		UnknownToolsHandler: func(_ context.Context, name, _ string) (string, error) {
			return fmt.Sprintf("error: unknown tool %q", name), nil
		},
		// Eino's Claude adapter emits an empty argument string for a streamed
		// tool_use whose input is {}. Inferred Go tools still require JSON.
		ToolArgumentsHandler: normalizeToolArguments,
	})
	if err != nil {
		return nil, fmt.Errorf("create tools node: %w", err)
	}

	return &ReActModel{
		toolModel: toolModel,
		// The base model is retained unbound for a forced final response. This
		// avoids requiring every ToolCallingChatModel implementation to support
		// WithTools(nil) as an unbind operation.
		finalModel:          chatModel,
		toolsNode:           toolsNode,
		executableToolNames: executableToolNames,
		maxModelSteps:       opts.MaxModelSteps,
		admission:           admission,
		taskController:      opts.TaskController,
		steer:               steer,
	}, nil
}

func normalizeReActOptions(opts ReActOptions) (ReActOptions, error) {
	if opts.MaxModelSteps < 0 {
		return ReActOptions{}, fmt.Errorf("max model steps must be >= 0")
	}
	if opts.Admission.WindowTokens < 0 {
		return ReActOptions{}, fmt.Errorf("admission window tokens must be >= 0")
	}
	if opts.MaxModelSteps == 0 {
		opts.MaxModelSteps = DefaultReActOptions().MaxModelSteps
	}
	if opts.DisableSteer {
		opts.EnableSteer = false
	} else {
		// A zero-value ReActOptions is used by the production runtime's explicit
		// options literal; keep the capability reachable unless it is disabled.
		opts.EnableSteer = true
	}
	return opts, nil
}

// MaxModelSteps returns the effective tool-enabled model-decision budget.
func (m *ReActModel) MaxModelSteps() int {
	if m == nil {
		return 0
	}
	return m.maxModelSteps
}

// AdmissionPolicy returns the locally enforced full-window safety policy.
func (m *ReActModel) AdmissionPolicy() contextbuild.AdmissionPolicy {
	if m == nil {
		return contextbuild.AdmissionPolicy{}
	}
	return m.admission
}

// RegisterTurnSteer opts one durable turn into this model's turn-local
// mailbox. A ReActModel only accepts registration when EnableSteer was true.
func (m *ReActModel) RegisterTurnSteer(turnID string, mailbox chat.TurnSteerMailbox) error {
	if m == nil || m.steer == nil {
		return chat.ErrSteerUnsupported
	}
	return m.steer.register(turnID, mailbox)
}

// UnregisterTurnSteer removes the model-side lookup after a turn settles.
func (m *ReActModel) UnregisterTurnSteer(turnID string) {
	if m == nil || m.steer == nil {
		return
	}
	m.steer.unregister(turnID)
}

func normalizeToolArguments(_ context.Context, _ string, arguments string) (string, error) {
	if strings.TrimSpace(arguments) == "" {
		return "{}", nil
	}
	return arguments, nil
}

// Stream runs one bounded ReAct turn and returns the final assistant stream.
func (m *ReActModel) Stream(ctx context.Context, messages []*schema.Message) (chat.Stream, error) {
	stream, _, err := m.runTurn(ctx, messages, nil)
	return stream, err
}

// ReasoningEventsFromStreams marks ReActModel as a chat.ReasoningEventSource:
// runTurn emits TurnEventReasoning for every model call before returning the
// final stream.
func (m *ReActModel) ReasoningEventsFromStreams() {}

// StreamWithEvents runs one ReAct turn and emits tool lifecycle events when emit is set.
func (m *ReActModel) StreamWithEvents(ctx context.Context, messages []*schema.Message, emit chat.EventEmitter) (chat.Stream, <-chan struct{}, error) {
	if emit == nil {
		stream, err := m.Stream(ctx, messages)
		return stream, nil, err
	}
	return m.runTurn(ctx, messages, emit)
}

func (m *ReActModel) runTurn(ctx context.Context, messages []*schema.Message, emit chat.EventEmitter) (chat.Stream, <-chan struct{}, error) {
	if m == nil || m.toolModel == nil || m.finalModel == nil || m.toolsNode == nil {
		return nil, nil, errors.New("ReAct model is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = logging.With(ctx, "component", "agent")
	logging.DebugContext(ctx, "react turn started",
		"max_model_steps", m.maxModelSteps,
		"history_messages", len(messages),
	)

	toolContext := ctx
	if emit != nil || m.taskController != nil {
		toolContext = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{}, toolEventCallback(emit, m.taskController))
	}
	// Keep controller state out of the accumulated history. Each request gets a
	// fresh packet after the preceding tool batch has updated the DAG.
	history := append([]*schema.Message(nil), messages...)
	modelCallNumber := 0
	for step := 0; step < m.maxModelSteps; step++ {
		if err := runtimeguard.AcquireModelStep(toolContext); err != nil {
			if errors.Is(err, runtimeguard.ErrModelStepBudgetExceeded) {
				break
			}
			return nil, nil, fmt.Errorf("acquire model step: %w", err)
		}
		history = m.rewriteMessages(toolContext, history)
		requestHistory := m.messagesWithTaskPacket(toolContext, history)
		if err := contextbuild.CheckRequestAdmission(requestHistory, m.admission); err != nil {
			logging.WarnContext(toolContext, "model request admission rejected",
				"step", step+1,
				"err", err,
			)
			return nil, nil, fmt.Errorf("admit model request: %w", err)
		}
		modelCallNumber++
		stepStarted := time.Now()
		logging.InfoContext(toolContext, "model step started",
			"step", modelCallNumber,
			"kind", "tool_enabled",
			"request_messages", len(requestHistory),
		)
		// Session owns durable model-call IDs. Keeping this empty lets its
		// turn-scoped usage tracker allocate IDs across task continuations.
		response, presentation, err := m.inspectModelStream(toolContext, m.toolModel, requestHistory, "", emit)
		if err != nil {
			logging.ErrorContext(toolContext, "model step failed",
				"step", modelCallNumber,
				"duration_ms", logging.DurationMillis(stepStarted),
				"err", err,
			)
			return nil, nil, err
		}
		response = withToolCallIDs(toolContext, response, modelCallNumber)
		toolCalls := 0
		if response != nil {
			toolCalls = len(response.ToolCalls)
		}
		logging.InfoContext(toolContext, "model step completed",
			"step", modelCallNumber,
			"kind", "tool_enabled",
			"tool_calls", toolCalls,
			"duration_ms", logging.DurationMillis(stepStarted),
		)
		if len(response.ToolCalls) == 0 {
			return presentation, completedEventStream(), nil
		}
		presentation.Close()

		history = append(history, response)
		admissions, err := m.admitToolBatch(toolContext, response)
		if err != nil {
			return nil, nil, err
		}
		batchStarted := time.Now()
		logging.InfoContext(toolContext, "tool batch started",
			"step", modelCallNumber,
			"tool_calls", toolCalls,
		)
		results, err := m.invokeAdmittedToolBatch(toolContext, response, admissions, emit)
		if err != nil {
			logging.ErrorContext(toolContext, "tool batch failed",
				"step", modelCallNumber,
				"duration_ms", logging.DurationMillis(batchStarted),
				"err", err,
			)
			return nil, nil, fmt.Errorf("run tool batch: %w", err)
		}
		// The session recorder persists large outputs as artifacts before it
		// exposes their bounded projection. Do not let a persistence failure
		// fall back to the raw tool output in the next model request.
		if err := chat.ToolResultProjectionFailure(toolContext); err != nil {
			logging.ErrorContext(toolContext, "tool result projection failed",
				"step", modelCallNumber,
				"err", err,
			)
			return nil, nil, fmt.Errorf("persist tool lifecycle: %w", err)
		}
		logging.InfoContext(toolContext, "tool batch completed",
			"step", modelCallNumber,
			"results", len(results),
			"duration_ms", logging.DurationMillis(batchStarted),
		)
		history = append(history, projectToolResultMessages(toolContext, results)...)
		// A tool batch is an active transaction. Its results are durable before
		// the next model call, but compaction cannot safely run in the middle of
		// it; the next admission check fails the turn rather than rerunning tools.
	}

	// The final fallback also sees the current controller state, but the
	// ephemeral packet is not retained if this turn later continues.
	history = withFinalModelInstruction(m.messagesWithTaskPacket(toolContext, history))
	history = m.rewriteMessages(toolContext, history)
	if err := contextbuild.CheckRequestAdmission(history, m.admission.WithoutTools()); err != nil {
		return nil, nil, fmt.Errorf("admit final model request: %w", err)
	}
	if err := runtimeguard.AcquireFinalResponse(toolContext); err != nil {
		return nil, nil, fmt.Errorf("acquire final response: %w", err)
	}
	modelCallNumber++
	finalStarted := time.Now()
	logging.InfoContext(toolContext, "model step started",
		"step", modelCallNumber,
		"kind", "final",
		"request_messages", len(history),
	)
	stream, done, err := newFinalModelResponseStream(toolContext, m.finalModel, history, "", emit)
	if err != nil {
		logging.ErrorContext(toolContext, "model step failed",
			"step", modelCallNumber,
			"kind", "final",
			"duration_ms", logging.DurationMillis(finalStarted),
			"err", err,
		)
		return nil, nil, err
	}
	logging.InfoContext(toolContext, "model step stream opened",
		"step", modelCallNumber,
		"kind", "final",
		"duration_ms", logging.DurationMillis(finalStarted),
	)
	return stream, done, nil
}

func completedEventStream() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

// projectToolResultMessages substitutes the session's durable preview for a
// large raw tool result in the same ReAct turn. The full result remains in the
// artifact ledger and can be read deliberately through read_artifact.
func projectToolResultMessages(ctx context.Context, results []*schema.Message) []*schema.Message {
	return chat.ProjectToolResultsForModel(ctx, results)
}

func (m *ReActModel) rewriteMessages(ctx context.Context, messages []*schema.Message) []*schema.Message {
	if m == nil || m.steer == nil {
		return messages
	}
	return m.steer.rewrite(ctx, messages)
}

func (m *ReActModel) inspectModelStream(ctx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, callID string, emit chat.EventEmitter) (*schema.Message, *schema.StreamReader[*schema.Message], error) {
	stream, err := chatModel.Stream(ctx, messages)
	if err != nil {
		return nil, nil, fmt.Errorf("run model: %w", err)
	}
	if stream == nil {
		return nil, nil, errors.New("model returned no stream")
	}
	// A tools-enabled response must be fully classified before its content can
	// reach Session. Some providers emit ordinary text and only later emit
	// tool_calls; a speculative text chunk cannot be withdrawn if the response
	// turns out to be a tool-planning step. The tools-disabled forced-final path
	// below is safe to forward live because later tool calls are not actionable.
	copies := stream.Copy(2)
	response, err := collectModelResponse(copies[0], callID, emit)
	if err != nil {
		copies[1].Close()
		return nil, nil, err
	}
	return response, copies[1], nil
}

// newFinalModelResponseStream leaves a known tools-disabled response live for
// Session while collecting its complete provider response as it is consumed.
// Tool-planning requests still use inspectModelStream because their complete
// response is needed before a batch can be admitted.
func newFinalModelResponseStream(ctx context.Context, chatModel model.BaseChatModel, messages []*schema.Message, callID string, emit chat.EventEmitter) (chat.Stream, <-chan struct{}, error) {
	stream, err := chatModel.Stream(ctx, messages)
	if err != nil {
		return nil, nil, fmt.Errorf("run model: %w", err)
	}
	if stream == nil {
		return nil, nil, errors.New("model returned no stream")
	}
	final := &finalModelResponseStream{
		stream: stream,
		callID: callID,
		emit:   emit,
		done:   make(chan struct{}),
	}
	return final, final.done, nil
}

type toolBatchAdmission struct {
	executable   bool
	arguments    string
	admission    runtimeguard.ToolAdmission
	denialReason string
}

func (m *ReActModel) admitToolBatch(ctx context.Context, response *schema.Message) ([]toolBatchAdmission, error) {
	if response == nil {
		return nil, errors.New("tool-call response is required")
	}
	decisions := make([]toolBatchAdmission, len(response.ToolCalls))
	calls := make([]runtimeguard.ToolCall, 0, len(response.ToolCalls))
	indexes := make([]int, 0, len(response.ToolCalls))
	for index, call := range response.ToolCalls {
		arguments, err := normalizeToolArguments(ctx, call.Function.Name, call.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("normalize tool arguments for %q: %w", call.Function.Name, err)
		}
		decisions[index].arguments = arguments
		if _, executable := m.executableToolNames[call.Function.Name]; !executable {
			continue
		}
		calls = append(calls, runtimeguard.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: arguments,
		})
		indexes = append(indexes, index)
		decisions[index].executable = true
	}
	// Keep update_plan sequential when mixed with other tools so the checklist
	// snapshot and shell/patch side effects stay ordered in one model turn.
	if len(response.ToolCalls) > 1 && containsTaskControlCall(response.ToolCalls) {
		for index := range decisions {
			decisions[index].executable = true
			decisions[index].denialReason = "requires_sequential_execution"
		}
		return decisions, nil
	}
	if len(calls) == 0 {
		return decisions, nil
	}

	admissions, err := runtimeguard.AdmitToolBatch(ctx, calls)
	if err != nil {
		return nil, fmt.Errorf("admit tool batch: %w", err)
	}
	if len(admissions) != len(indexes) {
		return nil, errors.New("runtime guard returned an incomplete tool admission batch")
	}
	for index, admission := range admissions {
		decisions[indexes[index]].admission = admission
	}
	return decisions, nil
}

func containsTaskControlCall(calls []schema.ToolCall) bool {
	for _, call := range calls {
		if isTaskControlTool(call.Function.Name) {
			return true
		}
	}
	return false
}

// invokeAdmittedToolBatch sends only registered calls that passed runtime
// admission to ToolsNode. BaseTool implementations supplied outside the
// registry do not necessarily call runtimeguard.StartToolCall themselves, so
// this is the final execution boundary. Unknown calls receive a bounded
// synthetic result here rather than becoming an unbudgeted node dispatch.
func (m *ReActModel) invokeAdmittedToolBatch(ctx context.Context, response *schema.Message, decisions []toolBatchAdmission, emit chat.EventEmitter) ([]*schema.Message, error) {
	if response == nil {
		return nil, errors.New("tool-call response is required")
	}
	if len(decisions) != len(response.ToolCalls) {
		return nil, errors.New("tool admission batch does not match tool-call response")
	}

	permittedCalls := make([]schema.ToolCall, 0, len(response.ToolCalls))
	deniedResults := make([]*schema.Message, len(response.ToolCalls))
	for index, call := range response.ToolCalls {
		decision := decisions[index]
		if decision.denialReason != "" {
			result := sequentialRetryToolResult(call, decision.denialReason)
			deniedResults[index] = result
			emitDeniedToolLifecycle(emit, call, decision.arguments, result.Content)
			continue
		}
		if !decision.executable {
			result := unknownToolResult(call)
			deniedResults[index] = result
			emitDeniedToolLifecycle(emit, call, decision.arguments, result.Content)
			continue
		}
		if !decision.admission.Allowed {
			result := deniedToolResult(call, decision.admission.Reason)
			deniedResults[index] = result
			emitDeniedToolLifecycle(emit, call, decision.arguments, result.Content)
			continue
		}
		permittedCalls = append(permittedCalls, call)
	}

	permittedResults := make([]*schema.Message, 0, len(permittedCalls))
	if len(permittedCalls) > 0 {
		permittedResponse := *response
		permittedResponse.ToolCalls = permittedCalls
		var err error
		permittedResults, err = m.toolsNode.Invoke(ctx, &permittedResponse)
		if err != nil {
			return nil, err
		}
	}

	results := make([]*schema.Message, 0, len(response.ToolCalls))
	permittedIndex := 0
	for index := range response.ToolCalls {
		if denied := deniedResults[index]; denied != nil {
			results = append(results, denied)
			continue
		}
		if permittedIndex >= len(permittedResults) {
			return nil, errors.New("tools node returned an incomplete permitted tool-result batch")
		}
		results = append(results, permittedResults[permittedIndex])
		permittedIndex++
	}
	if permittedIndex != len(permittedResults) {
		return nil, errors.New("tools node returned an oversized permitted tool-result batch")
	}
	return results, nil
}

func sequentialRetryToolResult(call schema.ToolCall, reason string) *schema.Message {
	return schema.ToolMessage(
		fmt.Sprintf(`{"denied":true,"reason":%q,"retry_next_model_call":true}`, reason),
		call.ID,
		schema.WithToolName(call.Function.Name),
	)
}

func deniedToolResult(call schema.ToolCall, reason string) *schema.Message {
	if strings.TrimSpace(reason) == "" {
		reason = runtimeguard.DenialReason(runtimeguard.ErrToolCallNotAdmitted)
	}
	return schema.ToolMessage(
		fmt.Sprintf(`{"denied":true,"reason":%q,"stop_retrying":true}`, reason),
		call.ID,
		schema.WithToolName(call.Function.Name),
	)
}

func unknownToolResult(call schema.ToolCall) *schema.Message {
	return deniedToolResult(call, "unknown_tool")
}

func emitDeniedToolLifecycle(emit chat.EventEmitter, call schema.ToolCall, arguments, output string) {
	if emit == nil {
		return
	}
	emit(chat.TurnEvent{
		Kind:       chat.TurnEventToolStart,
		Tool:       call.Function.Name,
		ToolCallID: call.ID,
		Input:      arguments,
	})
	emit(chat.TurnEvent{
		Kind:       chat.TurnEventToolEnd,
		Tool:       call.Function.Name,
		ToolCallID: call.ID,
		Output:     output,
	})
}

func collectModelResponse(stream *schema.StreamReader[*schema.Message], callID string, emit chat.EventEmitter) (*schema.Message, error) {
	if stream == nil {
		return nil, errors.New("model stream is required")
	}
	defer stream.Close()

	chunks := make([]*schema.Message, 0)
	var streamErr error
	for {
		message, err := stream.Recv()
		if message != nil {
			chunks = append(chunks, message)
			if emit != nil {
				if reasoning := chat.DisplayReasoningContent(message); reasoning != "" {
					emit(chat.TurnEvent{Kind: chat.TurnEventReasoning, Chunk: reasoning})
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

	return finalizeModelResponse(chunks, streamErr, callID, emit)
}

// finalizeModelResponse preserves the model-call accounting contract for both
// eagerly collected tool-planning responses and the live final response.
func finalizeModelResponse(chunks []*schema.Message, streamErr error, callID string, emit chat.EventEmitter) (*schema.Message, error) {
	message, concatErr := concatModelResponse(chunks)
	turn, available := usageFromChunks(chunks)
	if concatErr == nil {
		turn, available = usage.FromMessageUsage(message)
	}
	if streamErr != nil && !available {
		emitModelUsage(emit, callID, usage.Turn{}, false)
	} else {
		emitModelUsage(emit, callID, turn, available)
	}
	if concatErr != nil {
		return nil, fmt.Errorf("combine model stream: %w", concatErr)
	}
	if streamErr != nil {
		return nil, fmt.Errorf("read model stream: %w", streamErr)
	}
	return message, nil
}

func concatModelResponse(chunks []*schema.Message) (*schema.Message, error) {
	if len(chunks) == 0 {
		return schema.AssistantMessage("", nil), nil
	}
	message, err := schema.ConcatMessages(chunks)
	if err != nil {
		return nil, err
	}
	if message.Role == "" {
		message.Role = schema.Assistant
	}
	return message, nil
}

func withToolCallIDs(ctx context.Context, message *schema.Message, modelCallNumber int) *schema.Message {
	if message == nil || len(message.ToolCalls) == 0 {
		return message
	}
	copyMessage := *message
	copyMessage.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
	for index := range copyMessage.ToolCalls {
		if strings.TrimSpace(copyMessage.ToolCalls[index].ID) == "" {
			if id, ok := chat.NextLocalToolCallID(ctx); ok {
				copyMessage.ToolCalls[index].ID = id
			} else {
				copyMessage.ToolCalls[index].ID = fmt.Sprintf("local-tool-call-%d-%d", modelCallNumber, index+1)
			}
		}
	}
	return &copyMessage
}

func withFinalModelInstruction(messages []*schema.Message) []*schema.Message {
	insertAt := 0
	for insertAt < len(messages) && messages[insertAt] != nil && messages[insertAt].Role == schema.System {
		insertAt++
	}
	withInstruction := make([]*schema.Message, 0, len(messages)+1)
	withInstruction = append(withInstruction, messages[:insertAt]...)
	withInstruction = append(withInstruction, schema.SystemMessage(finalModelInstruction))
	withInstruction = append(withInstruction, messages[insertAt:]...)
	return withInstruction
}

func hasFinalText(message *schema.Message) bool {
	return message != nil && strings.TrimSpace(message.Content) != ""
}

type finalModelResponseStream struct {
	stream *schema.StreamReader[*schema.Message]
	callID string
	emit   chat.EventEmitter
	done   chan struct{}

	mu              sync.Mutex
	chunks          []*schema.Message
	response        *schema.Message
	terminalErr     error
	finalized       bool
	fallbackPending bool
	fallbackSent    bool

	finishOnce sync.Once
	closeOnce  sync.Once
}

// Recv forwards final text as it arrives. It records every raw provider chunk
// before removing any unexpected tool calls from the user-visible stream.
func (s *finalModelResponseStream) Recv() (*schema.Message, error) {
	if fallback, ok := s.nextFallback(); ok {
		return fallback, nil
	}
	if finished, err := s.terminalResult(); finished {
		return nil, err
	}

	message, streamErr := s.stream.Recv()
	if message != nil {
		s.record(message)
		if s.emit != nil {
			if reasoning := chat.DisplayReasoningContent(message); reasoning != "" {
				s.emit(chat.TurnEvent{Kind: chat.TurnEventReasoning, Chunk: reasoning})
			}
		}
		message = withoutToolCalls(message)
	}

	switch {
	case errors.Is(streamErr, io.EOF):
		response, err := s.finish(nil)
		s.closeSource()
		if err != nil {
			return message, err
		}
		if hasFinalText(response) {
			return message, streamErr
		}
		s.setFallbackPending()
		if message != nil {
			// Eino normally returns EOF on a separate receive. Preserve a rare
			// terminal metadata chunk before presenting the fallback next.
			return message, nil
		}
		fallback, _ := s.nextFallback()
		return fallback, nil
	case streamErr != nil:
		_, err := s.finish(streamErr)
		s.closeSource()
		return message, err
	default:
		return message, nil
	}
}

func (s *finalModelResponseStream) Close() {
	s.closeSource()
	_, _ = s.finish(context.Canceled)
}

func (s *finalModelResponseStream) record(message *schema.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalized {
		return
	}
	s.chunks = append(s.chunks, message)
}

func (s *finalModelResponseStream) finish(streamErr error) (*schema.Message, error) {
	s.finishOnce.Do(func() {
		s.mu.Lock()
		chunks := append([]*schema.Message(nil), s.chunks...)
		s.mu.Unlock()

		response, err := finalizeModelResponse(chunks, streamErr, s.callID, s.emit)

		s.mu.Lock()
		s.response = response
		s.terminalErr = err
		s.finalized = true
		s.mu.Unlock()
		close(s.done)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.response, s.terminalErr
}

func (s *finalModelResponseStream) closeSource() {
	s.closeOnce.Do(func() {
		s.stream.Close()
	})
}

func (s *finalModelResponseStream) terminalResult() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.finalized {
		return false, nil
	}
	if s.terminalErr != nil {
		return true, s.terminalErr
	}
	if s.fallbackPending && !s.fallbackSent {
		return false, nil
	}
	return true, io.EOF
}

func (s *finalModelResponseStream) setFallbackPending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fallbackPending = true
}

func (s *finalModelResponseStream) nextFallback() (*schema.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fallbackPending || s.fallbackSent {
		return nil, false
	}
	s.fallbackSent = true
	return schema.AssistantMessage(finalModelFallback, nil), true
}

func withoutToolCalls(message *schema.Message) *schema.Message {
	if message == nil || len(message.ToolCalls) == 0 {
		return message
	}
	copyMessage := *message
	copyMessage.ToolCalls = nil
	return &copyMessage
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
				if emit != nil {
					if reasoning := chat.DisplayReasoningContent(message); reasoning != "" {
						emit(chat.TurnEvent{
							Kind:  chat.TurnEventReasoning,
							Chunk: reasoning,
						})
					}
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
	emitTaskStatus := func(ctx context.Context) {
		if emit == nil || taskController == nil {
			return
		}
		status := taskController.TaskExecutionStatus(ctx)
		emit(chat.TurnEvent{Kind: chat.TurnEventTaskStatus, TaskStatus: &status})
	}
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
			callID := compose.GetToolCallID(ctx)
			// High-signal diagnostics only: never mirror argument bodies into
			// process logs (session ledger remains the full provenance store).
			logging.InfoContext(ctx, "tool started",
				"tool", name,
				"tool_call_id", callID,
				"input_bytes", len(args),
			)
			// Preserve the complete observation for the session recorder. Rendering
			// is deliberately bounded later in the TUI, not at this provenance edge.
			if emit != nil {
				emit(chat.TurnEvent{
					Kind:       chat.TurnEventToolStart,
					Tool:       name,
					ToolCallID: callID,
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
			callID := compose.GetToolCallID(ctx)
			logging.InfoContext(ctx, "tool completed",
				"tool", name,
				"tool_call_id", callID,
				"output_bytes", len(response),
			)
			if emit != nil {
				emit(chat.TurnEvent{
					Kind:       chat.TurnEventToolEnd,
					Tool:       name,
					ToolCallID: callID,
					Output:     response,
				})
			}
			emitTaskStatus(ctx)
			return ctx
		},
		OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			name := ""
			if info != nil {
				name = info.Name
			}
			callID := compose.GetToolCallID(ctx)
			logging.WarnContext(ctx, "tool error",
				"tool", name,
				"tool_call_id", callID,
				"err", err,
			)
			if emit != nil {
				emit(chat.TurnEvent{
					Kind:       chat.TurnEventToolError,
					Tool:       name,
					ToolCallID: callID,
					Err:        err,
				})
			}
			emitTaskStatus(ctx)
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

// TaskExecutionStatus and InterruptTask satisfy the optional chat.TaskRuntime
// contract without making chat depend on agent.
func (m *ReActModel) TaskExecutionStatus(ctx context.Context) chat.TaskRunStatus {
	if m == nil || m.taskController == nil {
		return chat.TaskRunStatus{}
	}
	return m.taskController.TaskExecutionStatus(ctx)
}

func (m *ReActModel) InterruptTask(ctx context.Context, reason string) chat.TaskInterruptReceipt {
	if m == nil || m.taskController == nil {
		return chat.TaskInterruptReceipt{Summary: "plan runtime is unavailable"}
	}
	return m.taskController.InterruptTask(ctx, reason)
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
