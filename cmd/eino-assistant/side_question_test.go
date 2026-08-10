package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type recordingSideQuestionModel struct {
	response       *schema.Message
	generateErr    error
	requests       [][]*schema.Message
	optionCounts   []int
	withToolsCalls int
}

func (m *recordingSideQuestionModel) Generate(_ context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.requests = append(m.requests, append([]*schema.Message(nil), messages...))
	m.optionCounts = append(m.optionCounts, len(opts))
	return m.response, m.generateErr
}

func (m *recordingSideQuestionModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{}), nil
}

func (m *recordingSideQuestionModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.withToolsCalls++
	return m, nil
}

func newSideQuestionTestSession(t *testing.T, prompt string, history string) (*chat.Session, *store.ThreadStore) {
	t.Helper()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := chat.NewSession(&localExecModel{stream: &localExecStream{
		messages: []*schema.Message{schema.AssistantMessage(history, nil)},
	}}, prompt, chat.SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(history) != "" {
		if err := session.Ask(context.Background(), "recorded history", nil); err != nil {
			t.Fatal(err)
		}
	}
	return session, threadStore
}

func TestCommandRuntimeSideQuestionCallbackUsesProvidedSession(t *testing.T) {
	model := &recordingSideQuestionModel{
		response: &schema.Message{
			Role:             schema.Assistant,
			Content:          "  visible answer  ",
			ReasoningContent: "hidden reasoning",
			ToolCalls:        []schema.ToolCall{{ID: "ignored-tool-call"}},
		},
	}
	initialSession, _ := newSideQuestionTestSession(t, "initial frozen prompt", "initial history")
	currentSession, currentStore := newSideQuestionTestSession(t, "current frozen prompt", "current history")
	runtime := &commandRuntime{session: initialSession, chatModel: model}
	callback := runtime.sideQuestion
	beforeTranscript := currentSession.Transcript()
	beforeUsage := currentSession.UsageSummary()
	beforeState, err := currentStore.LoadThread(context.Background(), currentSession.ID())
	if err != nil {
		t.Fatal(err)
	}

	answer, err := callback(context.Background(), currentSession, "What is the current side question?")
	if err != nil {
		t.Fatalf("side question callback: %v", err)
	}
	if answer != "visible answer" {
		t.Fatalf("answer = %q, want visible answer", answer)
	}
	if len(model.requests) != 1 || len(model.optionCounts) != 1 || model.optionCounts[0] != 0 {
		t.Fatalf("Generate calls = %d, option counts = %v, want one call with no options", len(model.requests), model.optionCounts)
	}
	if model.withToolsCalls != 0 {
		t.Fatalf("WithTools calls = %d, want 0", model.withToolsCalls)
	}
	requestText := sideQuestionRequestText(model.requests[0])
	for _, fragment := range []string{
		"current frozen prompt",
		"current history",
		"What is the current side question?",
		"reference-only",
		"Do not modify files",
		"Do not request escalation",
		"Do not call tools or",
	} {
		if !strings.Contains(requestText, fragment) {
			t.Fatalf("request missing %q:\n%s", fragment, requestText)
		}
	}
	if strings.Contains(requestText, "initial frozen prompt") || strings.Contains(requestText, "initial history") {
		t.Fatalf("request used runtime's initial session:\n%s", requestText)
	}
	for _, message := range model.requests[0] {
		if message == nil {
			continue
		}
		if message.Role == schema.Tool || len(message.ToolCalls) != 0 {
			t.Fatalf("side question request contains tool data: %#v", message)
		}
	}
	if got := currentSession.Transcript(); !reflect.DeepEqual(got, beforeTranscript) {
		t.Fatalf("transcript changed:\nbefore=%#v\nafter=%#v", beforeTranscript, got)
	}
	if got := currentSession.UsageSummary(); !reflect.DeepEqual(got, beforeUsage) {
		t.Fatalf("usage changed:\nbefore=%#v\nafter=%#v", beforeUsage, got)
	}
	afterState, err := currentStore.LoadThread(context.Background(), currentSession.ID())
	if err != nil {
		t.Fatal(err)
	}
	if afterState.Revision != beforeState.Revision || afterState.Meta.MessageCount != beforeState.Meta.MessageCount {
		t.Fatalf("thread changed: before revision/messages=%d/%d after=%d/%d", beforeState.Revision, beforeState.Meta.MessageCount, afterState.Revision, afterState.Meta.MessageCount)
	}
}

func TestCommandRuntimeSideQuestionValidation(t *testing.T) {
	model := &recordingSideQuestionModel{response: schema.AssistantMessage("answer", nil)}
	session, _ := newSideQuestionTestSession(t, "frozen prompt", "")
	tests := []struct {
		name     string
		runtime  *commandRuntime
		session  *chat.Session
		question string
		wantErr  error
	}{
		{name: "nil session", runtime: &commandRuntime{chatModel: model}, wantErr: errSideQuestionSessionUnavailable, question: "question"},
		{name: "nil runtime", session: session, wantErr: errSideQuestionModelUnavailable, question: "question"},
		{name: "nil model", runtime: &commandRuntime{}, session: session, wantErr: errSideQuestionModelUnavailable, question: "question"},
		{name: "empty question", runtime: &commandRuntime{chatModel: model}, session: session, wantErr: errSideQuestionEmpty, question: " \t\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.runtime.sideQuestion(context.Background(), test.session, test.question)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}

	emptyModel := &recordingSideQuestionModel{response: &schema.Message{Role: schema.Assistant, ReasoningContent: "only hidden text"}}
	if _, err := (&commandRuntime{chatModel: emptyModel}).sideQuestion(context.Background(), session, "question"); !errors.Is(err, errSideQuestionResponseEmpty) {
		t.Fatalf("empty response error = %v, want %v", err, errSideQuestionResponseEmpty)
	}
}

func TestCommandRuntimeBackgroundAgentUsesFrozenReferenceWithoutTools(t *testing.T) {
	model := &recordingSideQuestionModel{response: schema.AssistantMessage("  analysis finding  ", nil)}
	session, threadStore := newSideQuestionTestSession(t, "frozen prompt", "recorded history")
	runtime := &commandRuntime{chatModel: model}
	beforeTranscript := session.Transcript()
	beforeUsage := session.UsageSummary()
	beforeState, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatal(err)
	}

	answer, err := runtime.backgroundAgent(context.Background(), session, "inspect the failure boundary")
	if err != nil {
		t.Fatalf("backgroundAgent: %v", err)
	}
	if answer != "analysis finding" || len(model.requests) != 1 || model.withToolsCalls != 0 {
		t.Fatalf("answer=%q requests=%d withTools=%d", answer, len(model.requests), model.withToolsCalls)
	}
	requestText := sideQuestionRequestText(model.requests[0])
	for _, fragment := range []string{
		"background read-only analysis subagent",
		"frozen prompt",
		"recorded history",
		"inspect the failure boundary",
		"reference-only",
		"Do not modify files",
		"Do not call tools or further subagents",
	} {
		if !strings.Contains(requestText, fragment) {
			t.Fatalf("background request missing %q:\n%s", fragment, requestText)
		}
	}
	for _, message := range model.requests[0] {
		if message != nil && (message.Role == schema.Tool || len(message.ToolCalls) != 0) {
			t.Fatalf("background request contains tool data: %#v", message)
		}
	}
	if !reflect.DeepEqual(session.Transcript(), beforeTranscript) || !reflect.DeepEqual(session.UsageSummary(), beforeUsage) {
		t.Fatal("background agent changed session transcript or usage")
	}
	afterState, err := threadStore.LoadThread(context.Background(), session.ID())
	if err != nil || afterState.Revision != beforeState.Revision || afterState.Meta.MessageCount != beforeState.Meta.MessageCount {
		t.Fatalf("background agent changed thread: before=%#v after=%#v err=%v", beforeState, afterState, err)
	}
}

func TestCommandRuntimeBackgroundAgentValidation(t *testing.T) {
	model := &recordingSideQuestionModel{response: schema.AssistantMessage("answer", nil)}
	session, _ := newSideQuestionTestSession(t, "frozen prompt", "")
	for _, test := range []struct {
		name    string
		runtime *commandRuntime
		session *chat.Session
		task    string
		wantErr error
	}{
		{name: "nil session", runtime: &commandRuntime{chatModel: model}, task: "task", wantErr: errBackgroundAgentSessionUnavailable},
		{name: "nil runtime", session: session, task: "task", wantErr: errBackgroundAgentModelUnavailable},
		{name: "nil model", runtime: &commandRuntime{}, session: session, task: "task", wantErr: errBackgroundAgentModelUnavailable},
		{name: "empty task", runtime: &commandRuntime{chatModel: model}, session: session, task: " \t\n", wantErr: errBackgroundAgentEmpty},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.runtime.backgroundAgent(context.Background(), test.session, test.task)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
		})
	}

	emptyModel := &recordingSideQuestionModel{response: schema.AssistantMessage("", nil)}
	if _, err := (&commandRuntime{chatModel: emptyModel}).backgroundAgent(context.Background(), session, "task"); !errors.Is(err, errBackgroundAgentResponseEmpty) {
		t.Fatalf("empty response error = %v", err)
	}
}

func sideQuestionRequestText(messages []*schema.Message) string {
	var parts []string
	for _, message := range messages {
		if message == nil {
			continue
		}
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

var _ model.ToolCallingChatModel = (*recordingSideQuestionModel)(nil)
