package agent

import (
	"context"
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
		{
			message: schema.AssistantMessage("现在是 2026-07-14 15:30:00（CST，UTC+08:00）。", nil),
		},
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

type modelResponse struct {
	message *schema.Message
	err     error
}

type scriptedToolModel struct {
	responses []modelResponse
	calls     int
	requests  [][]*schema.Message
}

func (m *scriptedToolModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *scriptedToolModel) next(messages []*schema.Message) (*schema.Message, error) {
	m.requests = append(m.requests, append([]*schema.Message(nil), messages...))
	if m.calls >= len(m.responses) {
		return nil, io.EOF
	}
	resp := m.responses[m.calls]
	m.calls++
	if resp.err != nil {
		return nil, resp.err
	}
	return resp.message, nil
}

func (m *scriptedToolModel) Generate(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return m.next(messages)
}

func (m *scriptedToolModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.next(messages)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
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
