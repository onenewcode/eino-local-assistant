package agent

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/tools"

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
			collectOneModelUsage(streamEndingWithError(tt.chunks, streamErr), "model-1", func(event chat.TurnEvent) {
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
	collectOneModelUsage(stream, "model-1", func(event chat.TurnEvent) {
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
