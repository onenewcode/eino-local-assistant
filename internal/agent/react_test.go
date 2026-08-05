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
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/tools"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestNewReActModelWithOptionsConfiguresMaxStep(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	model, err := NewReActModelWithOptions(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool}, ReActOptions{MaxStep: 3})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions() error = %v", err)
	}
	if got, want := model.MaxSteps(), 3; got != want {
		t.Errorf("MaxSteps() = %d, want %d", got, want)
	}
}

func TestNewReActModelPreservesDefaultMaxStep(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	model, err := NewReActModel(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool})
	if err != nil {
		t.Fatalf("NewReActModel() error = %v", err)
	}
	if got, want := model.MaxSteps(), DefaultReActOptions().MaxStep; got != want {
		t.Errorf("MaxSteps() = %d, want %d", got, want)
	}
	if got, want := MaxStep, DefaultMaxStep; got != want {
		t.Errorf("MaxStep = %d, want %d", got, want)
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

func TestNewReActModelWithOptionsDefaultsZeroAndRejectsNegativeMaxStep(t *testing.T) {
	timeTool, err := tools.NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	model, err := NewReActModelWithOptions(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool}, ReActOptions{})
	if err != nil {
		t.Fatalf("NewReActModelWithOptions() error = %v", err)
	}
	if got, want := model.MaxSteps(), DefaultMaxStep; got != want {
		t.Errorf("MaxSteps() = %d, want %d", got, want)
	}

	_, err = NewReActModelWithOptions(context.Background(), &scriptedToolModel{}, []tool.BaseTool{timeTool}, ReActOptions{MaxStep: -1})
	if err == nil {
		t.Fatal("NewReActModelWithOptions() error = nil, want invalid max step error")
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
		MaxStep:        6,
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

func (m *scriptedToolModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
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
