package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/runtimeguard"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/tools"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestNewReActModelWithOptionsConfiguresMaxModelSteps(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	model, err := NewReActModelWithOptions(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool}, ReActOptions{MaxModelSteps: 3})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions() error = %v", err)
	}
	if got, want := model.MaxModelSteps(), 3; got != want {
		t.Errorf("MaxModelSteps() = %d, want %d", got, want)
	}
}

func TestNewReActModelPreservesDefaultMaxModelSteps(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	model, err := NewReActModel(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool})
	if err != nil {
		t.Fatalf("NewReActModel() error = %v", err)
	}
	if got, want := model.MaxModelSteps(), DefaultReActOptions().MaxModelSteps; got != want {
		t.Errorf("MaxModelSteps() = %d, want %d", got, want)
	}
	if got, want := DefaultMaxModelSteps, DefaultReActOptions().MaxModelSteps; got != want {
		t.Errorf("DefaultMaxModelSteps = %d, want %d", got, want)
	}
	if model.steer == nil {
		t.Fatal("production ReAct constructor did not opt into explicit steer")
	}
}

func TestReActModelCanExplicitlyDisableSteer(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}
	model, err := NewReActModelWithOptions(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool}, ReActOptions{
		DisableSteer: true,
	})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions() error = %v", err)
	}
	if model.steer != nil {
		t.Fatal("explicitly disabled ReAct steer capability was initialized")
	}
	if err := model.RegisterTurnSteer("turn-1", nil); !errors.Is(err, chat.ErrSteerUnsupported) {
		t.Fatalf("RegisterTurnSteer() error = %v, want ErrSteerUnsupported", err)
	}
}

func TestNewReActModelWithOptionsDefaultsZeroAndRejectsNegativeMaxModelSteps(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	model, err := NewReActModelWithOptions(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool}, ReActOptions{})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions() error = %v", err)
	}
	if got, want := model.MaxModelSteps(), DefaultMaxModelSteps; got != want {
		t.Errorf("MaxModelSteps() = %d, want %d", got, want)
	}

	_, err = NewReActModelWithOptions(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool}, ReActOptions{MaxModelSteps: -1})
	if err == nil {
		t.Fatal("NewReActModelWithOptions() error = nil, want invalid max step error")
	}
	_, err = NewReActModelWithOptions(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool}, ReActOptions{Admission: contextbuild.NewAdmissionPolicy(-1, nil)})
	if err == nil {
		t.Fatal("NewReActModelWithOptions() error = nil, want invalid prompt budget")
	}
}

func TestReActModelBlocksOverBudgetRequestBeforeProvider(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}
	fake := &scriptedToolModel{}
	reactModel, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{timeTool}, ReActOptions{
		Admission: contextbuild.NewAdmissionPolicy(1, nil),
	})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions() error = %v", err)
	}
	_, err = reactModel.Stream(context.Background(), []*schema.Message{schema.UserMessage("request that cannot fit")})
	if !errors.Is(err, contextbuild.ErrRequestAdmissionExceeded) {
		t.Fatalf("Stream() error = %v, want prompt budget admission failure", err)
	}
	if fake.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 after local admission rejection", fake.calls)
	}
}

func TestReActModelCallsGetCurrentTimeBeforeAnswering(t *testing.T) {
	fixed := time.Date(2026, 7, 14, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	timeTool, err := tools.NewGetCurrentTime(func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	fake := &scriptedToolModel{responses: []modelResponse{
		{
			message: &schema.Message{
				Role: schema.Assistant,
				ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 1,
					TotalTokens:      11,
				}},
				ToolCalls: []schema.ToolCall{{
					ID:   "call_time_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "get_current_time",
						Arguments: `{}`,
					},
				}},
			},
		},
		{message: &schema.Message{
			Role:    schema.Assistant,
			Content: "现在是 2026-07-14 15:30:00（CST，UTC+08:00）。",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				PromptTokens:     20,
				CompletionTokens: 5,
				TotalTokens:      25,
			}},
		}},
	}}

	reactModel, err := NewReActModel(context.Background(), fake, []tool.BaseTool{timeTool})
	if err != nil {
		t.Fatalf("NewReActModel() error = %v", err)
	}

	session, err := chat.NewSession(reactModel, "system prompt", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	var output strings.Builder
	var events []chat.TurnEvent
	if err := session.AskWithEvents(context.Background(), "今天是什么时间", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}, func(ev chat.TurnEvent) {
		if ev.Kind != chat.TurnEventChunk {
			events = append(events, ev)
		}
	}); err != nil {
		t.Fatalf("AskWithEvents() error = %v", err)
	}

	if got, want := output.String(), "现在是 2026-07-14 15:30:00（CST，UTC+08:00）。"; got != want {
		t.Errorf("streamed output = %q, want %q", got, want)
	}
	if got, want := fake.calls, 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	summary := session.UsageSummary()
	if summary.PromptTokens != 30 || summary.CompletionTokens != 6 || summary.TotalTokens != 36 || summary.ModelCallCount != 2 || summary.Status != store.UsageStatusExact {
		t.Fatalf("usage summary = %+v, want 30/6/36 across two exact calls", summary)
	}
	contextStatus := session.ContextStatus()
	if !contextStatus.MeasuredKnown || contextStatus.MeasuredTokens != 20 {
		t.Fatalf("context snapshot = %+v, want final model prompt tokens 20", contextStatus)
	}

	second := fake.requests[1]
	foundToolResult := false
	for _, msg := range second {
		if msg.Role == schema.Tool && strings.Contains(msg.Content, "2026-07-14 15:30:00") {
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Fatalf("second model request missing tool result, messages=%#v", second)
	}

	// Tool callbacks should surface start/end for get_current_time when the graph runs.
	// Depending on Eino callback wiring this may be empty if callbacks are not invoked
	// for scripted models; assert best-effort without failing the core loop.
	for _, ev := range events {
		if ev.Kind == chat.TurnEventToolStart && ev.Tool != "get_current_time" {
			t.Errorf("unexpected tool start: %#v", ev)
		}
	}

	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system prompt"},
		{role: schema.User, content: "今天是什么时间"},
		{role: schema.Assistant, content: "现在是 2026-07-14 15:30:00（CST，UTC+08:00）。"},
	})
}

func TestUsageTrackingStreamCloseWaitsForCopiedUsage(t *testing.T) {
	done := make(chan struct{})
	stream := &usageTrackingStream{
		stream: schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}),
		done:   done,
	}
	closed := make(chan struct{})
	go func() {
		stream.Close()
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("Close returned before copied usage completed")
	case <-time.After(10 * time.Millisecond):
	}
	close(done)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after copied usage completed")
	}
}

func TestCollectOneModelUsageAfterStreamError(t *testing.T) {
	streamErr := errors.New("connection closed")
	tests := []struct {
		name           string
		chunks         []*schema.Message
		wantEvent      bool
		wantAvailable  bool
		wantPrompt     int
		wantCompletion int
		wantTotal      int
	}{
		{
			name: "partial assistant response is unavailable once",
			chunks: []*schema.Message{
				schema.AssistantMessage("partial ", nil),
				schema.AssistantMessage("response", nil),
			},
			wantEvent:     true,
			wantAvailable: false,
		},
		{
			name: "empty role model chunks are unavailable",
			chunks: []*schema.Message{
				{Content: "partial response"},
			},
			wantEvent:     true,
			wantAvailable: false,
		},
		{
			name: "reported usage stays exact",
			chunks: []*schema.Message{
				{
					Role:    schema.Assistant,
					Content: "partial response",
					ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
						PromptTokens:     12,
						CompletionTokens: 3,
						TotalTokens:      15,
					}},
				},
			},
			wantEvent:      true,
			wantAvailable:  true,
			wantPrompt:     12,
			wantCompletion: 3,
			wantTotal:      15,
		},
		{
			name: "tool result stream is ignored",
			chunks: []*schema.Message{
				schema.ToolMessage("partial result", "tool-call-1"),
			},
		},
		{
			name: "stream that never yielded a chunk is ignored",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []chat.TurnEvent
			observeOneModelStream(streamEndingWithError(tt.chunks, streamErr), "model-1", func(event chat.TurnEvent) {
				events = append(events, event)
			})

			if !tt.wantEvent {
				if len(events) != 0 {
					t.Fatalf("usage events = %#v, want none", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("usage events = %#v, want exactly one", events)
			}
			event := events[0]
			if event.Kind != chat.TurnEventModelUsage || event.ModelUsage == nil {
				t.Fatalf("event = %#v, want model usage", event)
			}
			if event.ModelUsage.CallID != "model-1" || event.ModelUsage.Operation != chat.ModelUsageOperationAgent {
				t.Errorf("usage identity = %#v, want model-1 agent", event.ModelUsage)
			}
			if event.ModelUsage.Available != tt.wantAvailable {
				t.Errorf("usage available = %t, want %t", event.ModelUsage.Available, tt.wantAvailable)
			}
			if event.ModelUsage.Usage.PromptTokens != tt.wantPrompt {
				t.Errorf("usage prompt tokens = %d, want %d", event.ModelUsage.Usage.PromptTokens, tt.wantPrompt)
			}
			if event.ModelUsage.Usage.CompletionTokens != tt.wantCompletion || event.ModelUsage.Usage.TotalTokens != tt.wantTotal {
				t.Errorf("usage output/total tokens = %d/%d, want %d/%d", event.ModelUsage.Usage.CompletionTokens, event.ModelUsage.Usage.TotalTokens, tt.wantCompletion, tt.wantTotal)
			}
		})
	}
}

func TestCollectOneModelUsagePreservesUsageWhenChunksCannotConcat(t *testing.T) {
	stream := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role: schema.Assistant,
			Name: "first",
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12,
			}},
		},
		{Role: schema.Assistant, Name: "second"},
	})
	var events []chat.TurnEvent
	observeOneModelStream(stream, "model-1", func(event chat.TurnEvent) {
		events = append(events, event)
	})
	if len(events) != 1 || events[0].ModelUsage == nil {
		t.Fatalf("usage events = %#v", events)
	}
	observed := events[0].ModelUsage
	if !observed.Available || observed.Usage.PromptTokens != 10 || observed.Usage.CompletionTokens != 2 || observed.Usage.TotalTokens != 12 {
		t.Fatalf("concat failure usage = %#v", observed)
	}
}

func TestReActModelRunsNoArgumentStreamToolCall(t *testing.T) {
	fixed := time.Date(2026, 7, 14, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	timeTool, err := tools.NewGetCurrentTime(func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	// Eino's Claude adapter represents a streamed tool_use input of {} as an
	// empty arguments string. The ReAct tool boundary must restore valid JSON.
	fake := &scriptedToolModel{responses: []modelResponse{
		{
			stream: []*schema.Message{{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "toolu_no_arguments",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "get_current_time",
						Arguments: "",
					},
				}},
			}},
		},
		{message: schema.AssistantMessage("工具已执行。", nil)},
	}}

	reactModel, err := NewReActModel(context.Background(), fake, []tool.BaseTool{timeTool})
	if err != nil {
		t.Fatalf("NewReActModel() error = %v", err)
	}
	session, err := chat.NewSession(reactModel, "system prompt", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	var output strings.Builder
	if err := session.Ask(context.Background(), "现在时间", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if got, want := output.String(), "工具已执行。"; got != want {
		t.Errorf("streamed output = %q, want %q", got, want)
	}
	if got, want := fake.calls, 2; got != want {
		t.Fatalf("model calls = %d, want %d (blank arguments must still execute the tool)", got, want)
	}

	foundToolResult := false
	for _, message := range fake.requests[1] {
		if message != nil && message.Role == schema.Tool && strings.Contains(message.Content, "2026-") {
			foundToolResult = true
			break
		}
	}
	if !foundToolResult {
		t.Fatalf("second model request missing tool result: %#v", fake.requests[1])
	}
}

func TestReActModelStreamsPlainAnswersWithoutTools(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	fake := &scriptedToolModel{responses: []modelResponse{
		{message: schema.AssistantMessage("使用 defer 关闭资源。", nil)},
	}}

	reactModel, err := NewReActModel(context.Background(), fake, []tool.BaseTool{timeTool})
	if err != nil {
		t.Fatalf("NewReActModel() error = %v", err)
	}

	var output strings.Builder
	session, err := chat.NewSession(reactModel, "system prompt", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if err := session.Ask(context.Background(), "一句话解释 defer", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}

	if got, want := output.String(), "使用 defer 关闭资源。"; got != want {
		t.Errorf("streamed output = %q, want %q", got, want)
	}
	if got, want := fake.calls, 1; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
}

func TestReActModelMaxModelStepsCountsToolEnabledResponses(t *testing.T) {
	executed := &recordingTool{name: "count_steps"}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: toolCallMessage("step-1", "count_steps")},
		{message: toolCallMessage("step-2", "count_steps")},
		{message: schema.AssistantMessage("final response", nil)},
	}}

	reactModel, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{executed}, ReActOptions{MaxModelSteps: 2})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(reactModel, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var output strings.Builder
	if err := session.Ask(context.Background(), "do the work", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if got, want := output.String(), "final response"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := fake.calls, 3; got != want {
		t.Fatalf("model calls = %d, want %d: two tool-enabled decisions plus one final-only call", got, want)
	}
	if got, want := executed.Calls(), 2; got != want {
		t.Fatalf("executed tools = %d, want %d", got, want)
	}
}

func TestReActModelFourParallelToolBatchesLeaveRoomForFinalAnswer(t *testing.T) {
	first := &recordingTool{name: "batch_first"}
	second := &recordingTool{name: "batch_second"}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			toolCall("batch-1a", "batch_first"),
			toolCall("batch-1b", "batch_second"),
		}}},
		{message: &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			toolCall("batch-2a", "batch_first"),
			toolCall("batch-2b", "batch_second"),
		}}},
		{message: &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			toolCall("batch-3a", "batch_first"),
			toolCall("batch-3b", "batch_second"),
		}}},
		{message: &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			toolCall("batch-4a", "batch_first"),
			toolCall("batch-4b", "batch_second"),
		}}},
		{message: schema.AssistantMessage("completed after four batches", nil)},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{first, second}, ReActOptions{MaxModelSteps: 8})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var output strings.Builder
	if err := session.Ask(context.Background(), "complete the task", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if got, want := output.String(), "completed after four batches"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got, want := first.Calls()+second.Calls(), 8; got != want {
		t.Fatalf("executed tools = %d, want %d", got, want)
	}
	if got, want := fake.calls, 5; got != want {
		t.Fatalf("model calls = %d, want %d: four tool-enabled responses plus final text", got, want)
	}
}

func TestReActModelRunsParallelToolCallsInOneModelStep(t *testing.T) {
	probe := newParallelToolProbe()
	first := &recordingTool{name: "parallel_first", onRun: probe.Run}
	second := &recordingTool{name: "parallel_second", onRun: probe.Run}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			toolCall("parallel-1", "parallel_first"),
			toolCall("parallel-2", "parallel_second"),
		}}},
		{message: schema.AssistantMessage("both tools completed", nil)},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{first, second}, ReActOptions{MaxModelSteps: 1})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var output strings.Builder
	if err := session.Ask(ctx, "run both", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if got, want := output.String(), "both tools completed"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if !probe.BothStarted() {
		t.Fatal("tool calls from one model response were not executing in parallel")
	}
	if got, want := fake.calls, 2; got != want {
		t.Fatalf("model calls = %d, want %d: one tool-enabled response consumes one step", got, want)
	}
	if got, want := first.Calls()+second.Calls(), 2; got != want {
		t.Fatalf("executed tools = %d, want %d", got, want)
	}
}

func TestReActModelProjectsLargeToolOutputIntoCurrentTurn(t *testing.T) {
	head := strings.Repeat("H", 4<<10)
	omitted := strings.Repeat("M", 16<<10)
	tail := strings.Repeat("T", 4<<10)
	largeOutput := head + omitted + tail
	executed := &recordingTool{name: "large_result", result: largeOutput}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: toolCallMessage("large-result-call", "large_result")},
		{message: schema.AssistantMessage("used bounded evidence", nil)},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{executed}, ReActOptions{MaxModelSteps: 2})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(context.Background(), "inspect the large result", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got, want := fake.calls, 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}

	var toolOutput string
	for _, message := range fake.requests[1] {
		if message != nil && message.Role == schema.Tool && message.ToolCallID == "large-result-call" {
			toolOutput = message.Content
			break
		}
	}
	if toolOutput == "" {
		t.Fatalf("second model request has no tool result: %#v", fake.requests[1])
	}
	if !strings.Contains(toolOutput, "artifact id=") || !strings.Contains(toolOutput, head) || !strings.Contains(toolOutput, tail) {
		t.Fatalf("current-turn tool projection = %q, want artifact reference with head and tail", toolOutput)
	}
	if strings.Contains(toolOutput, omitted) || strings.Contains(toolOutput, largeOutput) {
		t.Fatalf("current-turn tool projection leaked omitted output")
	}
}

func TestReActModelStopsBeforeNextModelCallWhenLargeToolArtifactPersistenceFails(t *testing.T) {
	largeOutput := strings.Repeat("untrusted-large-tool-output", 1024)
	executed := &recordingTool{name: "large_result", result: largeOutput}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: toolCallMessage("large-result-call", "large_result")},
		{message: schema.AssistantMessage("must not be requested", nil)},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{executed}, ReActOptions{MaxModelSteps: 2})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	repository := &artifactFailingRepository{ThreadRepository: newTestThreadStore(t)}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: repository})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "inspect the large result", nil)
	if err == nil || !strings.Contains(err.Error(), "artifact persistence failed") {
		t.Fatalf("Ask error = %v, want artifact persistence failure", err)
	}
	if got, want := fake.calls, 1; got != want {
		t.Fatalf("model calls = %d, want %d: raw output must not reach a second request", got, want)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("model requests = %d, want only the planning request", len(fake.requests))
	}
}

func TestReActModelUsesToolsDisabledFinalCallAndFallsBackForToolCalls(t *testing.T) {
	executed := &recordingTool{name: "final_only_tool"}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: toolCallMessage("initial-tool", "final_only_tool")},
		{message: toolCallMessage("malformed-final-tool", "final_only_tool")},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{executed}, ReActOptions{MaxModelSteps: 1})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var output strings.Builder
	if err := session.Ask(context.Background(), "use the tool", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if got, want := output.String(), finalModelFallback; got != want {
		t.Fatalf("output = %q, want final fallback %q", got, want)
	}
	if got, want := executed.Calls(), 1; got != want {
		t.Fatalf("executed tools = %d, want only the permitted planning call", got)
	}
	if got, want := fake.calls, 2; got != want {
		t.Fatalf("model calls = %d, want %d (planning plus final-only call)", got, want)
	}
	if got, want := fake.ToolBindings(), []int{1}; !equalInts(got, want) {
		t.Fatalf("WithTools argument lengths = %v, want %v", got, want)
	}
	if !containsTaskPacket(fake.requests[1], finalModelInstruction) {
		t.Fatalf("final-only request missing finalization instruction: %#v", fake.requests[1])
	}
}

func TestReActModelStreamsForcedFinalResponseBeforeProviderEOF(t *testing.T) {
	finalReader, finalWriter := schema.Pipe[*schema.Message](1)
	var closeFinalWriter sync.Once
	closeWriter := func() { closeFinalWriter.Do(finalWriter.Close) }
	t.Cleanup(closeWriter)

	fake := &delayedFinalToolModel{
		finalReader:  finalReader,
		finalStarted: make(chan struct{}),
	}
	executed := &recordingTool{name: "stream_final_tool"}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{executed}, ReActOptions{MaxModelSteps: 1})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var output strings.Builder
	var eventsMu sync.Mutex
	var usageEvents []chat.ModelUsageEvent
	firstChunk := make(chan string, 1)
	askDone := make(chan error, 1)
	go func() {
		askDone <- session.AskWithEvents(context.Background(), "run the tool", func(chunk string) error {
			_, writeErr := output.WriteString(chunk)
			select {
			case firstChunk <- chunk:
			default:
			}
			return writeErr
		}, func(event chat.TurnEvent) {
			if event.Kind != chat.TurnEventModelUsage || event.ModelUsage == nil {
				return
			}
			eventsMu.Lock()
			usageEvents = append(usageEvents, *event.ModelUsage)
			eventsMu.Unlock()
		})
	}()

	select {
	case <-fake.finalStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forced final model call")
	}
	if closed := finalWriter.Send(schema.AssistantMessage("first ", nil), nil); closed {
		t.Fatal("final provider stream closed before first chunk")
	}
	select {
	case chunk := <-firstChunk:
		if chunk != "first " {
			t.Fatalf("first final chunk = %q, want %q", chunk, "first ")
		}
	case <-time.After(time.Second):
		closeWriter()
		<-askDone
		t.Fatal("final response was buffered until provider EOF")
	}
	select {
	case err := <-askDone:
		t.Fatalf("Ask returned before final provider EOF: %v", err)
	default:
	}
	eventsMu.Lock()
	usageBeforeEOF := len(usageEvents)
	eventsMu.Unlock()
	if usageBeforeEOF != 1 {
		t.Fatalf("usage events before final EOF = %d, want planning call only", usageBeforeEOF)
	}

	if closed := finalWriter.Send(&schema.Message{
		Role:    schema.Assistant,
		Content: "response",
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6,
		}},
	}, nil); closed {
		t.Fatal("final provider stream closed before completion")
	}
	closeWriter()
	select {
	case err := <-askDone:
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for streamed final response")
	}

	if got, want := output.String(), "first response"; got != want {
		t.Fatalf("streamed output = %q, want %q", got, want)
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if got, want := len(usageEvents), 2; got != want {
		t.Fatalf("usage event count = %d, want %d", got, want)
	}
	for index, event := range usageEvents {
		if !event.Available {
			t.Fatalf("usage event %d unexpectedly unavailable: %#v", index, event)
		}
	}
	summary := session.UsageSummary()
	if summary.PromptTokens != 6 || summary.CompletionTokens != 3 || summary.TotalTokens != 9 || summary.ModelCallCount != 2 || summary.Status != store.UsageStatusExact {
		t.Fatalf("usage summary = %+v, want 6/3/9 across two exact calls", summary)
	}
}

func TestReActModelDoesNotLeakTextBeforeLaterToolCall(t *testing.T) {
	planningReader, planningWriter := schema.Pipe[*schema.Message](0)
	var closePlanningWriter sync.Once
	closeWriter := func() { closePlanningWriter.Do(planningWriter.Close) }
	t.Cleanup(closeWriter)

	fake := &delayedPlanningToolModel{
		planningReader:  planningReader,
		planningStarted: make(chan struct{}),
		finalResponse:   schema.AssistantMessage("final answer", nil),
	}
	executed := &recordingTool{name: "late_tool"}
	model, err := NewReActModel(context.Background(), fake, []tool.BaseTool{executed})
	if err != nil {
		t.Fatalf("NewReActModel: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var output strings.Builder
	chunks := make(chan string, 2)
	askDone := make(chan error, 1)
	go func() {
		askDone <- session.Ask(ctx, "use the late tool", func(chunk string) error {
			_, writeErr := output.WriteString(chunk)
			chunks <- chunk
			return writeErr
		})
	}()

	select {
	case <-fake.planningStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool-enabled model call")
	}
	// An unbuffered pipe send returns only after inspectModelStream has consumed
	// this chunk. It must still be invisible while the response could become a
	// later tool_call (as happens with Claude- and DeepSeek-style streams).
	if closed := planningWriter.Send(schema.AssistantMessage("I will check that. ", nil), nil); closed {
		t.Fatal("planning provider stream closed before its text chunk")
	}
	select {
	case chunk := <-chunks:
		t.Fatalf("leaked speculative planning text %q before tool_call", chunk)
	default:
	}
	select {
	case err := <-askDone:
		t.Fatalf("Ask returned before the tool_call: %v", err)
	default:
	}

	if closed := planningWriter.Send(&schema.Message{
		Role:      schema.Assistant,
		ToolCalls: []schema.ToolCall{toolCall("late-call", "late_tool")},
	}, nil); closed {
		t.Fatal("planning provider stream closed before its tool_call")
	}
	closeWriter()

	select {
	case err := <-askDone:
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for final response")
	}
	if got, want := output.String(), "final answer"; got != want {
		t.Fatalf("streamed output = %q, want only %q", got, want)
	}
	if got, want := executed.Calls(), 1; got != want {
		t.Fatalf("executed tool calls = %d, want %d", got, want)
	}
	if got, want := fake.Calls(), 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
}

func TestReActModelBuffersPlainToolEnabledResponseUntilComplete(t *testing.T) {
	planningReader, planningWriter := schema.Pipe[*schema.Message](0)
	var closePlanningWriter sync.Once
	closeWriter := func() { closePlanningWriter.Do(planningWriter.Close) }
	t.Cleanup(closeWriter)

	fake := &delayedPlanningToolModel{
		planningReader:  planningReader,
		planningStarted: make(chan struct{}),
	}
	model, err := NewReActModel(context.Background(), fake, []tool.BaseTool{&recordingTool{name: "unused_tool"}})
	if err != nil {
		t.Fatalf("NewReActModel: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var output strings.Builder
	chunks := make(chan string, 2)
	askDone := make(chan error, 1)
	go func() {
		askDone <- session.Ask(ctx, "give a plain answer", func(chunk string) error {
			_, writeErr := output.WriteString(chunk)
			chunks <- chunk
			return writeErr
		})
	}()

	select {
	case <-fake.planningStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tool-enabled model call")
	}
	if closed := planningWriter.Send(schema.AssistantMessage("first ", nil), nil); closed {
		t.Fatal("provider stream closed before its first answer chunk")
	}
	if closed := planningWriter.Send(schema.AssistantMessage("answer", nil), nil); closed {
		t.Fatal("provider stream closed before its second answer chunk")
	}
	// The initial request has tools bound, so it remains behind the complete
	// classification gate even when this particular response is ultimately
	// plain text. Otherwise a later tool_call could make already displayed text
	// impossible to retract.
	select {
	case chunk := <-chunks:
		t.Fatalf("plain response leaked before provider completion: %q", chunk)
	default:
	}
	select {
	case err := <-askDone:
		t.Fatalf("Ask returned before provider completion: %v", err)
	default:
	}

	closeWriter()
	select {
	case err := <-askDone:
		if err != nil {
			t.Fatalf("Ask: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for buffered plain response")
	}
	if got, want := output.String(), "first answer"; got != want {
		t.Fatalf("streamed output = %q, want %q", got, want)
	}
	if got, want := fake.Calls(), 1; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
}

func TestReActModelDoesNotRepeatForcedFinalResponseWithinTurn(t *testing.T) {
	executed := &recordingTool{name: "final_budget_tool"}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: toolCallMessage("final-budget-tool", "final_budget_tool")},
		{message: schema.AssistantMessage("first forced final", nil)},
		{message: schema.AssistantMessage("must not be requested", nil)},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{executed}, ReActOptions{MaxModelSteps: 1})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{MaxModelSteps: 1})
	if err != nil {
		t.Fatalf("WithTurnContext: %v", err)
	}
	t.Cleanup(cancel)

	stream, err := model.Stream(ctx, []*schema.Message{schema.UserMessage("finish the task")})
	if err != nil {
		t.Fatalf("first Stream() error = %v", err)
	}
	for {
		_, receiveErr := stream.Recv()
		if errors.Is(receiveErr, io.EOF) {
			break
		}
		if receiveErr != nil {
			t.Fatalf("read first Stream() = %v", receiveErr)
		}
	}
	if got, want := fake.calls, 2; got != want {
		t.Fatalf("provider calls after first forced final = %d, want %d", got, want)
	}

	_, err = model.Stream(ctx, []*schema.Message{schema.UserMessage("task controller continuation")})
	if !errors.Is(err, runtimeguard.ErrFinalResponseBudgetExceeded) {
		t.Fatalf("second Stream() error = %v, want ErrFinalResponseBudgetExceeded", err)
	}
	if got, want := fake.calls, 2; got != want {
		t.Fatalf("second forced final called provider %d times, want %d", got, want)
	}
}

func TestReActModelDeniesWholeToolBatchWhenRemainingBudgetIsInsufficient(t *testing.T) {
	// These tools deliberately do not call runtimeguard.StartToolCall. ReAct
	// itself must prevent raw BaseTool implementations from bypassing an
	// all-or-nothing batch denial.
	first := &recordingTool{name: "budget_first"}
	second := &recordingTool{name: "budget_second"}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			toolCall("budget-1", "budget_first"),
			toolCall("budget-2", "budget_second"),
		}}},
		{message: schema.AssistantMessage("batch was denied", nil)},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{first, second}, ReActOptions{MaxModelSteps: 1})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{MaxToolCalls: 1})
	if err != nil {
		t.Fatalf("WithTurnContext: %v", err)
	}
	defer cancel()
	var output strings.Builder
	var events []chat.TurnEvent
	if err := session.AskWithEvents(ctx, "run too many", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}, func(event chat.TurnEvent) {
		events = append(events, event)
	}); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	if got, want := output.String(), "batch was denied"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if got := first.Calls() + second.Calls(); got != 0 {
		t.Fatalf("tool batch ran %d calls despite insufficient total budget", got)
	}
	budget, ok := runtimeguard.FromContext(ctx)
	if !ok {
		t.Fatal("turn budget missing after Ask")
	}
	if got := budget.ToolCalls(); got != 0 {
		t.Fatalf("denied tool batch reserved %d execution slots, want 0", got)
	}

	deniedIDs := make([]string, 0, 2)
	for _, message := range fake.requests[1] {
		if message == nil || message.Role != schema.Tool {
			continue
		}
		if !strings.Contains(message.Content, `"denied":true`) || !strings.Contains(message.Content, "runtime_tool_budget_exceeded") {
			t.Fatalf("next model request has unexpected denied tool result: %#v", message)
		}
		deniedIDs = append(deniedIDs, message.ToolCallID)
	}
	if got, want := len(deniedIDs), 2; got != want {
		t.Fatalf("next model request has %d budget-denied tool results, want %d: %#v", got, want, fake.requests[1])
	}
	if deniedIDs[0] != "budget-1" || deniedIDs[1] != "budget-2" {
		t.Fatalf("budget-denied tool result order = %v, want [budget-1 budget-2]", deniedIDs)
	}

	starts, ends := 0, 0
	startedIDs, endedIDs := make([]string, 0, 2), make([]string, 0, 2)
	for _, event := range events {
		switch event.Kind {
		case chat.TurnEventToolStart:
			starts++
			startedIDs = append(startedIDs, event.ToolCallID)
		case chat.TurnEventToolEnd:
			ends++
			endedIDs = append(endedIDs, event.ToolCallID)
		}
	}
	if starts != 2 || ends != 2 {
		t.Fatalf("denied tool lifecycle start/end = %d/%d, want 2/2: %#v", starts, ends, events)
	}
	if startedIDs[0] != "budget-1" || startedIDs[1] != "budget-2" || endedIDs[0] != "budget-1" || endedIDs[1] != "budget-2" {
		t.Fatalf("denied tool lifecycle ids start=%v end=%v, want source order [budget-1 budget-2]", startedIDs, endedIDs)
	}
}

func TestReActModelRejectsUnknownToolAtTheExecutionBoundary(t *testing.T) {
	known := &recordingTool{name: "known_tool"}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			toolCall("unknown-1", "not_registered"),
			toolCall("known-1", "known_tool"),
		}}},
		{message: schema.AssistantMessage("continued after the unavailable tool", nil)},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{known}, ReActOptions{MaxModelSteps: 1})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{MaxToolCalls: 1})
	if err != nil {
		t.Fatalf("WithTurnContext: %v", err)
	}
	t.Cleanup(cancel)
	if err := session.Ask(ctx, "run the available tool", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if got, want := known.Calls(), 1; got != want {
		t.Fatalf("known tool calls = %d, want %d", got, want)
	}
	budget, ok := runtimeguard.FromContext(ctx)
	if !ok {
		t.Fatal("turn budget missing after Ask")
	}
	if got, want := budget.ToolCalls(), 1; got != want {
		t.Fatalf("tool calls = %d, want %d: only the executable call consumes the budget", got, want)
	}

	var unknownResult string
	for _, message := range fake.requests[1] {
		if message != nil && message.Role == schema.Tool && message.ToolCallID == "unknown-1" {
			unknownResult = message.Content
			break
		}
	}
	if !strings.Contains(unknownResult, `"denied":true`) || !strings.Contains(unknownResult, `"reason":"unknown_tool"`) {
		t.Fatalf("unknown tool result = %q, want bounded rejection", unknownResult)
	}
}

func TestReActSteerIsDeliveredOnlyAtNextModelBoundary(t *testing.T) {
	blockingTool := &steerBlockingTool{
		started: make(chan struct{}),
		release: make(chan struct{}),
		args:    make(chan string, 1),
	}
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "steer-tool-call",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "steer_block",
					Arguments: `{"value":"keep"}`,
				},
			}},
		}},
		{message: schema.AssistantMessage("final answer", nil)},
	}}
	model, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{blockingTool}, ReActOptions{
		EnableSteer: true,
	})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(model, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	askDone := make(chan error, 1)
	go func() { askDone <- session.Ask(context.Background(), "original", nil) }()
	select {
	case <-blockingTool.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool execution")
	}
	turnID, ok := session.ActiveTurnID()
	if !ok {
		t.Fatal("active ReAct turn did not expose a steer ID")
	}
	if err := session.Steer(context.Background(), turnID, "redirect at next decision"); err != nil {
		t.Fatalf("Steer during tool execution: %v", err)
	}
	close(blockingTool.release)
	if err := <-askDone; err != nil {
		t.Fatalf("Ask: %v", err)
	}

	select {
	case args := <-blockingTool.args:
		if args != `{"value":"keep"}` {
			t.Fatalf("tool arguments = %q, steer must not rewrite tool parameters", args)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for tool arguments")
	}
	if len(fake.requests) != 2 {
		t.Fatalf("underlying model calls = %d, want 2", len(fake.requests))
	}
	for _, message := range fake.requests[0] {
		if message != nil && message.Role == schema.User && message.Content == "redirect at next decision" {
			t.Fatal("steer was delivered before the tool finished")
		}
	}
	foundSteer := false
	for _, message := range fake.requests[1] {
		if message != nil && message.Role == schema.User && message.Content == "redirect at next decision" {
			foundSteer = true
		}
	}
	if !foundSteer {
		t.Fatalf("next model request did not contain steer: %#v", fake.requests[1])
	}
	steerCount := 0
	for _, message := range session.Transcript() {
		if message != nil && message.Role == schema.User && message.Content == "redirect at next decision" {
			steerCount++
		}
	}
	if steerCount != 1 {
		t.Fatalf("committed steer count = %d, want exactly 1", steerCount)
	}
}

func TestReActTaskCallbackCreatesPlanRequiredGateAfterUnplannedPatch(t *testing.T) {
	workspace := t.TempDir()
	patchTool, err := tools.NewApplyPatch(tools.ApplyPatchOptions{
		WorkspaceRoot: workspace,
		Approval:      tools.ApprovalNever,
	})
	if err != nil {
		t.Fatalf("NewApplyPatch: %v", err)
	}
	controller := NewTaskController()
	fake := &scriptedToolModel{responses: []modelResponse{
		{message: &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "patch-before-plan",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "apply_patch",
					Arguments: `{"operations":[{"type":"create_file","path":"changed.txt","content":"changed\n"}]}`,
				},
			}},
		}},
		{message: schema.AssistantMessage("premature delivery", nil)},
		{message: schema.AssistantMessage("still not planned", nil)},
		{message: schema.AssistantMessage("still not planned", nil)},
	}}
	reactModel, err := NewReActModelWithOptions(context.Background(), fake, []tool.BaseTool{patchTool}, ReActOptions{
		MaxModelSteps:  6,
		TaskController: controller,
	})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions: %v", err)
	}
	session, err := chat.NewSession(reactModel, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "edit the file", nil)
	if !errors.Is(err, chat.ErrTaskCompletionUnresolved) {
		t.Fatalf("Ask error = %v, want unresolved task completion", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "changed.txt")); err != nil {
		t.Fatalf("apply_patch did not run: %v", err)
	}
	status := session.TaskStatus()
	if status.State != taskRunActive || !strings.Contains(strings.Join(status.Gaps, " "), "before a task plan") {
		t.Fatalf("task status after callback = %#v", status)
	}
	if len(fake.requests) < 3 || !containsTaskPacket(fake.requests[2], "Create a fresh task_plan") {
		t.Fatalf("continuation request missing controller packet: %#v", fake.requests)
	}
}

func TestReActTaskCallbackInvalidatesProofAfterApplyPatchError(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-callback-patch-error")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}

	toolEventCallback(nil, controller).OnError(ctx, &callbacks.RunInfo{
		Component: components.ComponentOfTool,
		Name:      "apply_patch",
	}, errors.New("write second file: disk full"))
	if status := controller.TaskExecutionStatus(ctx); status.DoneTasks != 0 || len(status.Gaps) == 0 {
		t.Fatalf("apply_patch callback error must invalidate evidence: %#v", status)
	}
}

func TestReActTaskCallbackInvalidatesProofAfterShellError(t *testing.T) {
	controller := NewTaskController()
	ctx := taskTestContext(t, "task-callback-shell-error")
	if result, err := controller.SetPlan(ctx, simpleTaskPlan()); err != nil || !result.OK {
		t.Fatalf("SetPlan = %#v, %v", result, err)
	}
	if result, err := controller.StartTask(ctx, "implement"); err != nil || !result.OK {
		t.Fatalf("StartTask = %#v, %v", result, err)
	}
	controller.RecordToolResult(ctx, "shell", "proof", "", `{"command":"go test ./internal/example","exit_code":0}`)
	if result, err := controller.RecordProof(ctx, "implement", "unit", "proof"); err != nil || !result.OK {
		t.Fatalf("RecordProof = %#v, %v", result, err)
	}

	toolEventCallback(nil, controller).OnError(ctx, &callbacks.RunInfo{
		Component: components.ComponentOfTool,
		Name:      "shell",
	}, errors.New("wait command: transport lost after process start"))
	if status := controller.TaskExecutionStatus(ctx); status.DoneTasks != 0 || len(status.Gaps) == 0 {
		t.Fatalf("shell callback error must invalidate evidence: %#v", status)
	}
}

func containsTaskPacket(messages []*schema.Message, fragment string) bool {
	for _, message := range messages {
		if message != nil && message.Role == schema.System && strings.Contains(message.Content, fragment) {
			return true
		}
	}
	return false
}

func TestContentThenToolStreamToolCallCheckerFindsLaterToolCalls(t *testing.T) {
	// DeepSeek-style stream: text first, tool_calls later.
	sr := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("好的，我帮你看一下现在的时间！", nil),
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "call_time_later",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "get_current_time",
					Arguments: `{}`,
				},
			}},
		},
	})
	ok, err := contentThenToolStreamToolCallChecker(context.Background(), sr)
	if err != nil {
		t.Fatalf("checker error = %v", err)
	}
	if !ok {
		t.Fatal("checker must detect tool_calls after content chunks")
	}
}

func TestContentThenToolStreamToolCallCheckerPlainAnswer(t *testing.T) {
	sr := schema.StreamReaderFromArray([]*schema.Message{
		schema.AssistantMessage("plain answer", nil),
	})
	ok, err := contentThenToolStreamToolCallChecker(context.Background(), sr)
	if err != nil {
		t.Fatalf("checker error = %v", err)
	}
	if ok {
		t.Fatal("plain answer must not be treated as tool call")
	}
}

func TestReActModelHandlesTextThenToolCallStream(t *testing.T) {
	fixed := time.Date(2026, 7, 14, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	timeTool, err := tools.NewGetCurrentTime(func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	// First model call streams text then tool_calls across chunks (DeepSeek style).
	// Second call returns the final natural-language answer after the tool ran.
	fake := &scriptedToolModel{responses: []modelResponse{
		{
			stream: []*schema.Message{
				schema.AssistantMessage("好的，我帮你看一下现在的时间！", nil),
				{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:   "call_time_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "get_current_time",
							Arguments: `{}`,
						},
					}},
				},
			},
		},
		{message: schema.AssistantMessage("现在是 2026-07-14 15:30:00（CST，UTC+08:00）。", nil)},
	}}

	reactModel, err := NewReActModel(context.Background(), fake, []tool.BaseTool{timeTool})
	if err != nil {
		t.Fatalf("NewReActModel() error = %v", err)
	}
	session, err := chat.NewSession(reactModel, "system prompt", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	var output strings.Builder
	if err := session.Ask(context.Background(), "现在的时间", func(chunk string) error {
		_, writeErr := output.WriteString(chunk)
		return writeErr
	}); err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	if got, want := output.String(), "现在是 2026-07-14 15:30:00（CST，UTC+08:00）。"; got != want {
		t.Errorf("streamed output = %q, want %q", got, want)
	}
	if got, want := fake.calls, 2; got != want {
		t.Fatalf("model calls = %d, want %d (tools must run)", got, want)
	}
	for _, msg := range session.Transcript() {
		if msg != nil && msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			t.Fatalf("transcript must not keep open tool_calls: %#v", msg)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
	got := truncateRunes("abcdefghij", 5)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != 5 {
		t.Errorf("truncate long = %q", got)
	}
}

func TestReActEmitsReasoningFromEveryModelCall(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime: %v", err)
	}
	fake := &scriptedToolModel{responses: []modelResponse{
		{
			stream: []*schema.Message{
				{Role: schema.Assistant, ReasoningContent: "need clock"},
				{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:   "call_time_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "get_current_time",
							Arguments: `{}`,
						},
					}},
					ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
						PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6,
					}},
				},
			},
		},
		{
			stream: []*schema.Message{
				{Role: schema.Assistant, ReasoningContent: "format answer"},
				{
					Role:    schema.Assistant,
					Content: "it is noon",
					ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
						PromptTokens: 8, CompletionTokens: 2, TotalTokens: 10,
					}},
				},
			},
		},
	}}

	reactModel, err := NewReActModel(context.Background(), fake, []tool.BaseTool{timeTool})
	if err != nil {
		t.Fatalf("NewReActModel: %v", err)
	}
	session, err := chat.NewSession(reactModel, "system", chat.SessionOptions{Store: newTestThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var reasoning []string
	if err := session.AskWithEvents(context.Background(), "time?", nil, func(ev chat.TurnEvent) {
		if ev.Kind == chat.TurnEventReasoning {
			reasoning = append(reasoning, ev.Chunk)
		}
	}); err != nil {
		t.Fatalf("AskWithEvents: %v", err)
	}
	if got, want := strings.Join(reasoning, "|"), "need clock|format answer"; got != want {
		t.Fatalf("reasoning = %q, want %q", got, want)
	}
	for _, msg := range session.Transcript() {
		if msg != nil && msg.ReasoningContent != "" {
			t.Fatalf("committed message kept ReasoningContent: %#v", msg)
		}
	}
}

func newTestThreadStore(t *testing.T) *store.ThreadStore {
	t.Helper()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	return threadStore
}

type artifactFailingRepository struct {
	store.ThreadRepository
}

func (r *artifactFailingRepository) PutArtifact(context.Context, string, store.ArtifactInput) (store.ArtifactRef, error) {
	return store.ArtifactRef{}, errors.New("artifact persistence failed")
}

func streamEndingWithError(chunks []*schema.Message, err error) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](len(chunks) + 1)
	for _, chunk := range chunks {
		writer.Send(chunk, nil)
	}
	writer.Send(nil, err)
	writer.Close()
	return reader
}

type modelResponse struct {
	message *schema.Message
	// stream, when set, is returned as multi-chunk streaming output.
	// Prefer this over message when testing text-then-tool_calls providers.
	stream []*schema.Message
	err    error
}

type scriptedToolModel struct {
	responses []modelResponse
	calls     int
	requests  [][]*schema.Message

	mu           sync.Mutex
	toolBindings []int
}

type delayedFinalToolModel struct {
	finalReader  *schema.StreamReader[*schema.Message]
	finalStarted chan struct{}

	mu        sync.Mutex
	calls     int
	startOnce sync.Once
}

type delayedPlanningToolModel struct {
	planningReader  *schema.StreamReader[*schema.Message]
	planningStarted chan struct{}
	finalResponse   *schema.Message

	mu        sync.Mutex
	calls     int
	startOnce sync.Once
}

type steerBlockingTool struct {
	started     chan struct{}
	startedOnce sync.Once
	release     chan struct{}
	args        chan string
}

func (*steerBlockingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "steer_block", Desc: "block until released"}, nil
}

func (t *steerBlockingTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	t.startedOnce.Do(func() { close(t.started) })
	select {
	case t.args <- argumentsInJSON:
	default:
	}
	select {
	case <-t.release:
		return "tool result", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (m *scriptedToolModel) WithTools(toolInfos []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	// A nil slice is the explicit final-only binding used after the final
	// tool-planning step; it is valid and distinct from a non-empty binding.
	m.mu.Lock()
	m.toolBindings = append(m.toolBindings, len(toolInfos))
	m.mu.Unlock()
	return m, nil
}

func (m *scriptedToolModel) ToolBindings() []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]int(nil), m.toolBindings...)
}

func (m *scriptedToolModel) next(messages []*schema.Message) (modelResponse, error) {
	m.requests = append(m.requests, append([]*schema.Message(nil), messages...))
	if m.calls >= len(m.responses) {
		return modelResponse{}, io.EOF
	}
	resp := m.responses[m.calls]
	m.calls++
	if resp.err != nil {
		return modelResponse{}, resp.err
	}
	return resp, nil
}

func (m *scriptedToolModel) Generate(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	resp, err := m.next(messages)
	if err != nil {
		return nil, err
	}
	if resp.message != nil {
		return resp.message, nil
	}
	if len(resp.stream) > 0 {
		return schema.ConcatMessages(resp.stream)
	}
	return nil, io.EOF
}

func (m *scriptedToolModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	resp, err := m.next(messages)
	if err != nil {
		return nil, err
	}
	if len(resp.stream) > 0 {
		return schema.StreamReaderFromArray(resp.stream), nil
	}
	if resp.message != nil {
		return schema.StreamReaderFromArray([]*schema.Message{resp.message}), nil
	}
	return nil, io.EOF
}

func (m *delayedFinalToolModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *delayedPlanningToolModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (*delayedFinalToolModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("Generate is not used by this streaming test")
}

func (*delayedPlanningToolModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("Generate is not used by this streaming test")
}

func (m *delayedFinalToolModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	switch call {
	case 1:
		return schema.StreamReaderFromArray([]*schema.Message{{
			Role:      schema.Assistant,
			ToolCalls: []schema.ToolCall{toolCall("stream-final-call", "stream_final_tool")},
			ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3,
			}},
		}}), nil
	case 2:
		m.startOnce.Do(func() { close(m.finalStarted) })
		return m.finalReader, nil
	default:
		return nil, io.EOF
	}
}

func (m *delayedPlanningToolModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	switch call {
	case 1:
		m.startOnce.Do(func() { close(m.planningStarted) })
		return m.planningReader, nil
	case 2:
		return schema.StreamReaderFromArray([]*schema.Message{m.finalResponse}), nil
	default:
		return nil, io.EOF
	}
}

func (m *delayedPlanningToolModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

type recordingTool struct {
	name         string
	result       string
	runtimeGuard bool
	onRun        func(context.Context, string) error

	mu    sync.Mutex
	calls int
}

func (t *recordingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "test recording tool"}, nil
}

func (t *recordingTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	if t.runtimeGuard {
		if err := runtimeguard.StartToolCall(ctx, runtimeguard.ToolCall{
			ID:        compose.GetToolCallID(ctx),
			Name:      t.name,
			Arguments: argumentsInJSON,
		}); err != nil {
			return `{"denied":true}`, nil
		}
	}
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	if t.onRun != nil {
		if err := t.onRun(ctx, t.name); err != nil {
			return "", err
		}
	}
	if t.result != "" {
		return t.result, nil
	}
	return t.name + " completed", nil
}

func (t *recordingTool) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

type parallelToolProbe struct {
	mu          sync.Mutex
	started     int
	bothStarted chan struct{}
	once        sync.Once
}

func newParallelToolProbe() *parallelToolProbe {
	return &parallelToolProbe{bothStarted: make(chan struct{})}
}

func (p *parallelToolProbe) Run(ctx context.Context, _ string) error {
	p.mu.Lock()
	p.started++
	if p.started == 2 {
		p.once.Do(func() { close(p.bothStarted) })
	}
	p.mu.Unlock()
	select {
	case <-p.bothStarted:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *parallelToolProbe) BothStarted() bool {
	select {
	case <-p.bothStarted:
		return true
	default:
		return false
	}
}

func toolCallMessage(id, name string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{toolCall(id, name)}}
}

func toolCall(id, name string) schema.ToolCall {
	return schema.ToolCall{
		ID:   id,
		Type: "function",
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: `{}`,
		},
	}
}

func equalInts(got, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

type messageExpectation struct {
	role    schema.RoleType
	content string
}

func assertMessages(t *testing.T, got []*schema.Message, want []messageExpectation) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("messages = %d, want %d: %#v", len(got), len(want), got)
	}
	for i, expected := range want {
		if got[i].Role != expected.role {
			t.Errorf("message %d role = %q, want %q", i, got[i].Role, expected.role)
		}
		if got[i].Content != expected.content {
			t.Errorf("message %d content = %q, want %q", i, got[i].Content, expected.content)
		}
	}
}
