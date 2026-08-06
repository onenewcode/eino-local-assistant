package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/runtimeguard"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/schema"
)

func TestNewSessionRequiresThreadStore(t *testing.T) {
	_, err := NewSession(&scriptedModel{}, "system instructions", SessionOptions{})
	if err == nil || !strings.Contains(err.Error(), "thread store is required") {
		t.Fatalf("NewSession error = %v, want required thread store", err)
	}
}

func TestTurnTerminationUsesStableRuntimeDeadlineReason(t *testing.T) {
	ctx, cancel, err := runtimeguard.WithTurnContext(context.Background(), runtimeguard.TurnOptions{Timeout: time.Nanosecond})
	if err != nil {
		t.Fatalf("WithTurnContext() error = %v", err)
	}
	defer cancel()
	<-ctx.Done()

	if got := turnTerminationReason(ctx, context.DeadlineExceeded); got != runtimeguard.TurnTimeoutReason {
		t.Fatalf("turnTerminationReason() = %q, want %q", got, runtimeguard.TurnTimeoutReason)
	}
	if err := turnTerminationError(ctx, context.DeadlineExceeded); !errors.Is(err, runtimeguard.ErrTurnDeadlineExceeded) {
		t.Fatalf("turnTerminationError() = %v, want runtime deadline sentinel", err)
	}
}

func TestSessionAskAggregatesStreamAndCommitsCompleteTurn(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("Hello, ", nil)},
		{message: schema.AssistantMessage("world!", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var output strings.Builder
	err = session.Ask(context.Background(), "say hello", func(chunk string) error {
		_, err := output.WriteString(chunk)
		return err
	})
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}

	if got, want := output.String(), "Hello, world!"; got != want {
		t.Errorf("streamed output = %q, want %q", got, want)
	}
	if !stream.closed {
		t.Error("Ask() did not close the response stream")
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "say hello"},
		{role: schema.Assistant, content: "Hello, world!"},
	})
	assertMessages(t, model.requests[0], []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "say hello"},
	})
}

func TestSessionAskEmitsReasoningAndStripsOnCommit(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: &schema.Message{Role: schema.Assistant, ReasoningContent: "step ", Content: ""}},
		{message: &schema.Message{Role: schema.Assistant, ReasoningContent: "one", Content: "Hello"}},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var reasoning []string
	var chunks []string
	err = session.AskWithEvents(context.Background(), "hi", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	}, func(ev TurnEvent) {
		switch ev.Kind {
		case TurnEventReasoning:
			reasoning = append(reasoning, ev.Chunk)
		case TurnEventChunk:
			// also observed via onChunk
		}
	})
	if err != nil {
		t.Fatalf("AskWithEvents: %v", err)
	}
	if got, want := strings.Join(reasoning, ""), "step one"; got != want {
		t.Fatalf("reasoning events = %q, want %q", got, want)
	}
	if got, want := strings.Join(chunks, ""), "Hello"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	for _, msg := range session.Transcript() {
		if msg != nil && msg.ReasoningContent != "" {
			t.Fatalf("committed transcript must strip ReasoningContent: %#v", msg)
		}
	}

	// Second turn must not re-send prior reasoning into the model prompt.
	stream2 := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("ok", nil)},
	}}
	model.streams = []Stream{stream2}
	if err := session.Ask(context.Background(), "again", nil); err != nil {
		t.Fatalf("second Ask: %v", err)
	}
	if len(model.requests) < 2 {
		t.Fatalf("expected second model request, got %d", len(model.requests))
	}
	for _, msg := range model.requests[1] {
		if msg != nil && msg.ReasoningContent != "" {
			t.Fatalf("follow-up prompt must not include ReasoningContent: %#v", msg)
		}
	}
}

func TestStripReasoningForStorage(t *testing.T) {
	if got := stripReasoningForStorage(nil); got != nil {
		t.Fatalf("nil in => %v", got)
	}
	plain := schema.AssistantMessage("hi", nil)
	if got := stripReasoningForStorage(plain); got != plain {
		t.Fatalf("no reasoning should return same pointer")
	}
	withRC := &schema.Message{
		Role:             schema.Assistant,
		Content:          "hi",
		ReasoningContent: "think",
		Extra: map[string]any{
			extraKeyClaudeThinking:         "think",
			extraKeyClaudeThinkingSign:     "sig",
			extraKeyOpenAIReasoningContent: "think",
			"keep-me":                      "ok",
		},
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "step"}},
			{Type: schema.ChatMessagePartTypeText, Text: "hi"},
		},
	}
	stripped := stripReasoningForStorage(withRC)
	if stripped == withRC {
		t.Fatal("expected copy when stripping")
	}
	if stripped.ReasoningContent != "" || stripped.Content != "hi" {
		t.Fatalf("stripped = %#v", stripped)
	}
	if stripped.Extra["keep-me"] != "ok" {
		t.Fatalf("non-reasoning extra lost: %#v", stripped.Extra)
	}
	for _, key := range []string{extraKeyClaudeThinking, extraKeyClaudeThinkingSign, extraKeyOpenAIReasoningContent} {
		if _, ok := stripped.Extra[key]; ok {
			t.Fatalf("reasoning extra %q must be removed", key)
		}
	}
	if len(stripped.AssistantGenMultiContent) != 1 || stripped.AssistantGenMultiContent[0].Type != schema.ChatMessagePartTypeText {
		t.Fatalf("reasoning multi-parts not stripped: %#v", stripped.AssistantGenMultiContent)
	}
	if withRC.ReasoningContent != "think" || withRC.Extra[extraKeyClaudeThinking] != "think" {
		t.Fatal("original must be unchanged")
	}
}

func TestDisplayReasoningContentNormalizesProviderFields(t *testing.T) {
	cases := []struct {
		name string
		msg  *schema.Message
		want string
	}{
		{
			name: "reasoning content",
			msg:  &schema.Message{ReasoningContent: "summary"},
			want: "summary",
		},
		{
			name: "openai extra",
			msg:  &schema.Message{Extra: map[string]any{extraKeyOpenAIReasoningContent: "openai"}},
			want: "openai",
		},
		{
			name: "claude extra",
			msg:  &schema.Message{Extra: map[string]any{extraKeyClaudeThinking: "claude"}},
			want: "claude",
		},
		{
			name: "multi content",
			msg: &schema.Message{AssistantGenMultiContent: []schema.MessageOutputPart{
				{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "one"}},
				{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "two"}},
			}},
			want: "onetwo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayReasoningContent(tc.msg); got != tc.want {
				t.Fatalf("DisplayReasoningContent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSessionStripsProviderExtraThinkingOnCommit(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: &schema.Message{
			Role:             schema.Assistant,
			Content:          "Hello",
			ReasoningContent: "private",
			Extra: map[string]any{
				extraKeyClaudeThinking:     "private",
				extraKeyClaudeThinkingSign: "sig-1",
			},
		}},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := session.Ask(context.Background(), "hi", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	for _, msg := range session.Transcript() {
		if msg == nil || msg.Role != schema.Assistant {
			continue
		}
		if msg.ReasoningContent != "" {
			t.Fatalf("ReasoningContent leaked: %#v", msg)
		}
		if msg.Extra != nil {
			if _, ok := msg.Extra[extraKeyClaudeThinking]; ok {
				t.Fatalf("claude thinking extra leaked: %#v", msg.Extra)
			}
		}
	}

	stream2 := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("ok", nil)},
	}}
	model.streams = []Stream{stream2}
	if err := session.Ask(context.Background(), "again", nil); err != nil {
		t.Fatalf("second Ask: %v", err)
	}
	for _, msg := range model.requests[1] {
		if msg == nil {
			continue
		}
		if msg.ReasoningContent != "" {
			t.Fatalf("follow-up prompt ReasoningContent: %#v", msg)
		}
		if msg.Extra != nil {
			if _, ok := msg.Extra[extraKeyClaudeThinking]; ok {
				t.Fatalf("follow-up prompt thinking extra: %#v", msg.Extra)
			}
		}
	}
}

// reasoningSourceModel is EventAware and claims to emit reasoning events so
// streamAnswer must not re-emit ReasoningContent from the final stream.
type reasoningSourceModel struct {
	scriptedModel
}

func (m *reasoningSourceModel) ReasoningEventsFromStreams() {}

func (m *reasoningSourceModel) StreamWithEvents(ctx context.Context, messages []*schema.Message, emit EventEmitter) (Stream, <-chan struct{}, error) {
	// Simulate ReAct: emit reasoning from the sidecar, not from final Recv only.
	if emit != nil {
		emit(TurnEvent{Kind: TurnEventReasoning, Chunk: "from-source"})
	}
	stream, err := m.Stream(ctx, messages)
	done := make(chan struct{})
	close(done)
	return stream, done, err
}

func TestSessionSkipsReasoningEmitWhenModelIsReasoningEventSource(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: &schema.Message{Role: schema.Assistant, ReasoningContent: "from-final-stream", Content: "hi"}},
	}}
	model := &reasoningSourceModel{scriptedModel: scriptedModel{streams: []Stream{stream}}}
	session, err := NewSession(model, "system", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var reasoning []string
	if err := session.AskWithEvents(context.Background(), "hi", nil, func(ev TurnEvent) {
		if ev.Kind == TurnEventReasoning {
			reasoning = append(reasoning, ev.Chunk)
		}
	}); err != nil {
		t.Fatalf("AskWithEvents: %v", err)
	}
	if got, want := strings.Join(reasoning, "|"), "from-source"; got != want {
		t.Fatalf("reasoning = %q, want only sidecar emit %q (no final-stream duplicate)", got, want)
	}
}

func TestSessionAskSendsPriorCompleteTurnsAsContext(t *testing.T) {
	firstStream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("first answer", nil)},
	}}
	secondStream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("second answer", nil)},
	}}
	model := &scriptedModel{streams: []Stream{firstStream, secondStream}}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := session.Ask(context.Background(), "first question", nil); err != nil {
		t.Fatalf("first Ask() error = %v", err)
	}
	if err := session.Ask(context.Background(), "second question", nil); err != nil {
		t.Fatalf("second Ask() error = %v", err)
	}

	if got, want := len(model.requests), 2; got != want {
		t.Fatalf("model calls = %d, want %d", got, want)
	}
	assertMessages(t, model.requests[1], []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "first question"},
		{role: schema.Assistant, content: "first answer"},
		{role: schema.User, content: "second question"},
	})
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "first question"},
		{role: schema.Assistant, content: "first answer"},
		{role: schema.User, content: "second question"},
		{role: schema.Assistant, content: "second answer"},
	})
}

func TestSessionAskRollsBackOnStreamFailure(t *testing.T) {
	wantErr := errors.New("connection dropped")
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial reply", nil)},
		{err: wantErr},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var output strings.Builder
	err = session.Ask(context.Background(), "question", func(chunk string) error {
		_, err := output.WriteString(chunk)
		return err
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask() error = %v, want wrapped %v", err, wantErr)
	}
	if got, want := output.String(), "partial reply"; got != want {
		t.Errorf("streamed output = %q, want %q", got, want)
	}
	if !stream.closed {
		t.Error("Ask() did not close the failed response stream")
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
	if summary := session.UsageSummary(); summary.Status != store.UsageStatusIncomplete || summary.ModelCallCount != 1 || summary.TotalTokens != 0 {
		t.Fatalf("partial failed stream usage = %+v, want one unavailable call", summary)
	}
}

func TestSessionPreservesProviderUsageFromFinalErroredChunk(t *testing.T) {
	wantErr := errors.New("connection dropped")
	stream := &scriptedStream{events: []streamEvent{{
		message: assistantWithProviderUsage("partial reply", 10, 2),
		err:     wantErr,
	}}}
	session, err := NewSession(&scriptedModel{streams: []Stream{stream}}, "system instructions", SessionOptions{
		Store: newDurableThreadStore(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = session.Ask(context.Background(), "question", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask error = %v, want wrapped %v", err, wantErr)
	}
	if summary := session.UsageSummary(); summary.Status != store.UsageStatusExact || summary.ModelCallCount != 1 || summary.PromptTokens != 10 || summary.CompletionTokens != 2 || summary.TotalTokens != 12 {
		t.Fatalf("errored final chunk usage = %+v", summary)
	}
}

func TestSessionRecordsProviderUsageForIncompleteToolCallResponse(t *testing.T) {
	answer := assistantWithProviderUsage("", 10, 2)
	answer.ToolCalls = []schema.ToolCall{{
		ID:   "tool-1",
		Type: "function",
		Function: schema.FunctionCall{
			Name:      "get_current_time",
			Arguments: "{}",
		},
	}}
	session, err := NewSession(&scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: answer}}},
	}}, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	err = session.Ask(context.Background(), "question", nil)
	if err == nil || !strings.Contains(err.Error(), "incomplete tool call response") {
		t.Fatalf("Ask error = %v, want incomplete tool call response", err)
	}
	if summary := session.UsageSummary(); summary.Status != store.UsageStatusExact || summary.ModelCallCount != 1 || summary.PromptTokens != 10 || summary.CompletionTokens != 2 || summary.TotalTokens != 12 {
		t.Fatalf("incomplete tool-call usage = %+v", summary)
	}
}

func TestSessionPreservesUsageWhenResponseChunksCannotConcat(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: assistantWithProviderUsage("partial", 10, 2)},
		{message: schema.ToolMessage("malformed stream", "tool-1")},
	}}
	session, err := NewSession(&scriptedModel{streams: []Stream{stream}}, "system instructions", SessionOptions{
		Store: newDurableThreadStore(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = session.Ask(context.Background(), "question", nil)
	if err == nil || !strings.Contains(err.Error(), "combine response stream") {
		t.Fatalf("Ask error = %v, want combine response error", err)
	}
	if summary := session.UsageSummary(); summary.Status != store.UsageStatusExact || summary.ModelCallCount != 1 || summary.TotalTokens != 12 {
		t.Fatalf("malformed stream usage = %+v", summary)
	}
}

func TestSessionAskRollsBackOnChunkCallbackFailure(t *testing.T) {
	wantErr := errors.New("terminal write failed")
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial reply", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "question", func(string) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask() error = %v, want wrapped %v", err, wantErr)
	}
	if !stream.closed {
		t.Error("Ask() did not close the response stream after callback failure")
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionAskRejectsEmptyInputWithoutCallingModel(t *testing.T) {
	model := &scriptedModel{}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), " \t\n ", nil)
	if !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("Ask() error = %v, want %v", err, ErrEmptyInput)
	}
	if got := len(model.requests); got != 0 {
		t.Errorf("model calls = %d, want 0", got)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionAskRollsBackWhenStreamCannotStart(t *testing.T) {
	wantErr := errors.New("model unavailable")
	model := &scriptedModel{err: wantErr}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "question", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask() error = %v, want wrapped %v", err, wantErr)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionCancellationClosesBlockedStream(t *testing.T) {
	stream := &blockingStream{closed: make(chan struct{})}
	started := make(chan struct{})
	model := &scriptedModel{
		streams: []Stream{stream},
		beforeStream: func() {
			close(started)
		},
	}
	session, err := NewSession(model, "system instructions", SessionOptions{Store: newDurableThreadStore(t)})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- session.Ask(ctx, "question", nil)
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Ask error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled blocked stream did not return")
	}
}

func TestSessionCancellationAtEOFDoesNotCommitPartialTurn(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial reply", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	st := newDurableThreadStore(t)
	session, err := NewSession(model, "system instructions", SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = session.Ask(ctx, "question", func(string) error {
		// A provider can acknowledge Stream.Close by returning EOF. The turn must
		// still be terminally cancelled rather than committed as a partial answer.
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Ask error = %v, want context.Canceled", err)
	}
	state, err := st.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != 1 {
		t.Fatalf("cancelled EOF turn committed %d messages", state.Meta.MessageCount)
	}
	groups, err := st.LoadTurnGroups(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Cancelled == nil || groups[0].Committed != nil {
		t.Fatalf("cancelled EOF lifecycle = %#v", groups)
	}
}

func TestSessionPersistsSuccessfulTurnBeforeMemoryCommit(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("ok", nil)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	st := newDurableThreadStore(t)
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store: st,
		ID:    "sess-1",
		Title: "t",
		Now:   func() time.Time { return time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	if err := session.Ask(context.Background(), "hi", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}

	state, err := st.LoadThread(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != 3 {
		t.Fatalf("message count = %d, want 3", state.Meta.MessageCount)
	}
	groups, err := st.LoadTurnGroups(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Started == nil || groups[0].Committed == nil {
		t.Fatalf("completed lifecycle = %#v", groups)
	}
	assertMessages(t, groups[0].Committed.Messages, []messageExpectation{
		{role: schema.User, content: "hi"},
		{role: schema.Assistant, content: "ok"},
	})
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "hi"},
		{role: schema.Assistant, content: "ok"},
	})
}

func TestSessionDoesNotPersistFailedTurn(t *testing.T) {
	wantErr := errors.New("boom")
	stream := &scriptedStream{events: []streamEvent{
		{message: schema.AssistantMessage("partial", nil)},
		{err: wantErr},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	st := newDurableThreadStore(t)
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store: st,
		ID:    "sess-fail",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "q", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Ask error = %v", err)
	}
	state, err := st.LoadThread(context.Background(), "sess-fail")
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != 1 {
		t.Fatalf("message count = %d, want only the system message", state.Meta.MessageCount)
	}
	groups, err := st.LoadTurnGroups(context.Background(), "sess-fail")
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Committed != nil || groups[0].Failed == nil {
		t.Fatalf("failed lifecycle = %#v", groups)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
	})
}

func TestSessionFinalResponseValidationFailsBeforeCommitAndKeepsUsage(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: assistantWithProviderUsage(`{"wrong":true}`, 11, 3)},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	st := newDurableThreadStore(t)
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store: st,
		ID:    "sess-schema-fail",
		FinalResponseValidator: func(string) error {
			return errors.New("required property answer is missing")
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "q", nil)
	if err == nil || !strings.Contains(err.Error(), "final response validation") {
		t.Fatalf("Ask error = %v, want final response validation failure", err)
	}
	state, err := st.LoadThread(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != 1 {
		t.Fatalf("message count = %d, want only the system message", state.Meta.MessageCount)
	}
	groups, err := st.LoadTurnGroups(context.Background(), session.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Committed != nil || groups[0].Failed == nil {
		t.Fatalf("failed lifecycle = %#v", groups)
	}
	usage := session.UsageSummary()
	if usage.PromptTokens != 11 || usage.CompletionTokens != 3 || usage.ModelCallCount != 1 {
		t.Fatalf("usage = %+v, want provider usage retained", usage)
	}
}

func TestSessionRejectsIncompleteToolCallFinalAnswer(t *testing.T) {
	// Simulates a ReAct END that still carries tool_calls (text-then-tool_calls
	// mis-detection). Must not commit; otherwise the next turn 400s forever.
	stream := &scriptedStream{events: []streamEvent{
		{message: &schema.Message{
			Role:    schema.Assistant,
			Content: "好的，我帮你看一下现在的时间！",
			ToolCalls: []schema.ToolCall{{
				ID:   "call_time_1",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "get_current_time",
					Arguments: `{}`,
				},
			}},
		}},
	}}
	model := &scriptedModel{streams: []Stream{stream}}
	st := newDurableThreadStore(t)
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store: st,
		ID:    "sess-tool-incomplete",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	err = session.Ask(context.Background(), "现在时间", nil)
	if err == nil || !strings.Contains(err.Error(), "incomplete tool call response") {
		t.Fatalf("Ask error = %v, want incomplete tool call response", err)
	}
	state, err := st.LoadThread(context.Background(), "sess-tool-incomplete")
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.MessageCount != 1 {
		t.Fatalf("message count = %d, want only system (no polluted commit)", state.Meta.MessageCount)
	}
	groups, err := st.LoadTurnGroups(context.Background(), "sess-tool-incomplete")
	if err != nil {
		t.Fatalf("LoadTurnGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Committed != nil || groups[0].Failed == nil {
		t.Fatalf("lifecycle = %#v, want failed uncommitted turn", groups)
	}
}

func TestTurnGroupMessagesStripsDanglingToolCalls(t *testing.T) {
	// Polluted commit: final assistant still has tool_calls and no tool lifecycle.
	// Prompt reconstruction must strip tool_calls so follow-ups can recover.
	group := store.TurnGroup{
		TurnID: "turn-polluted",
		Started: &store.TurnStart{
			TurnID: "turn-polluted",
			Input:  "现在时间",
		},
		Committed: &store.TurnCommit{
			TurnID: "turn-polluted",
			Messages: []*schema.Message{
				schema.UserMessage("现在时间"),
				{
					Role:    schema.Assistant,
					Content: "好的，我帮你看一下现在的时间！",
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
	}
	msgs := turnGroupMessages(group)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2: %#v", len(msgs), msgs)
	}
	if msgs[1] == nil || msgs[1].Role != schema.Assistant {
		t.Fatalf("assistant missing: %#v", msgs)
	}
	if len(msgs[1].ToolCalls) != 0 {
		t.Fatalf("dangling tool_calls must be stripped: %#v", msgs[1].ToolCalls)
	}
	if msgs[1].Content != "好的，我帮你看一下现在的时间！" {
		t.Fatalf("content = %q", msgs[1].Content)
	}
}

func TestTurnGroupMessagesNormalizesReplayedToolArguments(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "blank", input: "", want: "{}"},
		{name: "whitespace", input: " \t\n", want: "{}"},
		{name: "malformed", input: "not-json", want: "{}"},
		{name: "array", input: "[]", want: "{}"},
		{name: "object preserved", input: ` { "timezone": "UTC" } `, want: ` { "timezone": "UTC" } `},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			group := store.TurnGroup{
				TurnID: "turn-replay-tool",
				Started: &store.TurnStart{
					TurnID: "turn-replay-tool",
					Input:  "现在时间",
				},
				Tools: []store.ToolGroup{{
					Started: &store.ToolStarted{
						TurnID:     "turn-replay-tool",
						ToolCallID: "call-time",
						ToolName:   "get_current_time",
						Input:      tt.input,
					},
					Completed: &store.ToolCompleted{
						TurnID:     "turn-replay-tool",
						ToolCallID: "call-time",
						ToolName:   "get_current_time",
						Output:     "tool output",
					},
				}},
				Committed: &store.TurnCommit{
					TurnID: "turn-replay-tool",
					Messages: []*schema.Message{
						schema.UserMessage("现在时间"),
						schema.AssistantMessage("现在是下午。", nil),
					},
				},
			}

			messages := turnGroupMessages(group)
			if len(messages) < 2 || messages[1] == nil || len(messages[1].ToolCalls) != 1 {
				t.Fatalf("replayed tool call missing: %#v", messages)
			}
			if got := messages[1].ToolCalls[0].Function.Arguments; got != tt.want {
				t.Errorf("replayed arguments = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionRebasesUsageWhenThreadRevisionChanges(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{
		{message: assistantWithProviderUsage("ok", 10, 2)},
	}}
	st := newDurableThreadStore(t)
	var concurrentWriteErr error
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store: st,
		ID:    "sess-disk",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	model.beforeStream = func() {
		state, loadErr := st.LoadThread(context.Background(), session.ID())
		if loadErr != nil {
			concurrentWriteErr = loadErr
			return
		}
		_, concurrentWriteErr = st.SetThreadTitle(context.Background(), session.ID(), state.Revision, "external writer")
	}

	err = session.Ask(context.Background(), "hi", nil)
	if concurrentWriteErr != nil {
		t.Fatalf("concurrent thread write: %v", concurrentWriteErr)
	}
	if err != nil {
		t.Fatalf("Ask error = %v", err)
	}
	groups, loadErr := st.LoadTurnGroups(context.Background(), session.ID())
	if loadErr != nil {
		t.Fatalf("LoadTurnGroups: %v", loadErr)
	}
	if len(groups) != 1 || groups[0].Failed != nil || groups[0].Committed == nil {
		t.Fatalf("rebased turn = %#v", groups)
	}
	if summary := session.UsageSummary(); summary.PromptTokens != 10 || summary.CompletionTokens != 2 || summary.ModelCallCount != 1 || summary.Status != store.UsageStatusExact {
		t.Fatalf("rebased usage summary = %+v", summary)
	}
	resumedModel := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("recovered", nil)}}},
	}}
	resumed, openErr := OpenSession(resumedModel, st, session.ID(), SessionOptions{Store: st})
	if openErr != nil {
		t.Fatalf("OpenSession after rebase: %v", openErr)
	}
	if askErr := resumed.Ask(context.Background(), "retry", nil); askErr != nil {
		t.Fatalf("Ask after rebase: %v", askErr)
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "system instructions"},
		{role: schema.User, content: "hi"},
		{role: schema.Assistant, content: "ok"},
	})
}

func TestSessionRecordsUsageFromResponseMeta(t *testing.T) {
	answer := schema.AssistantMessage("ok", nil)
	answer.ResponseMeta = &schema.ResponseMeta{
		Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	stream := &scriptedStream{events: []streamEvent{{message: answer}}}
	model := &scriptedModel{streams: []Stream{stream}}
	session, err := NewSession(model, "system instructions", SessionOptions{
		Store:   newDurableThreadStore(t),
		Pricing: usage.Pricing{InputPerMillion: 1, OutputPerMillion: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	summary := session.UsageSummary()
	if summary.PromptTokens != 10 || summary.CompletionTokens != 5 || summary.TotalTokens != 15 ||
		summary.Status != store.UsageStatusExact || summary.CostUSD <= 0 {
		t.Fatalf("usage summary=%+v", summary)
	}
}

func TestSessionDoesNotEstimateMissingProviderUsage(t *testing.T) {
	stream := &scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("unreported", nil)}}}
	session, err := NewSession(&scriptedModel{streams: []Stream{stream}}, "system instructions", SessionOptions{
		Store: newDurableThreadStore(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Ask(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	summary := session.UsageSummary()
	if summary.Status != store.UsageStatusIncomplete || summary.TotalTokens != 0 || summary.ModelCallCount != 1 {
		t.Fatalf("usage summary = %+v, want one unavailable call without estimated tokens", summary)
	}
	if session.ContextStatus().MeasuredKnown {
		t.Fatal("missing provider usage must not create an exact context snapshot")
	}
}

func TestSessionSendsBudgetedViewNotFullTranscript(t *testing.T) {
	ctx := context.Background()
	st := newDurableThreadStore(t)
	seedModel := &scriptedModel{streams: make([]Stream, 0, 8)}
	for range 8 {
		seedModel.streams = append(seedModel.streams, &scriptedStream{events: []streamEvent{
			{message: schema.AssistantMessage(strings.Repeat("a", 160), nil)},
		}})
	}
	cfg := contextbuild.Config{
		WindowTokens:              500,
		MaxOutputTokens:           100,
		AutoCompactTriggerPercent: 75,
		PostCompactTargetPercent:  45,
		KeepRecentTurns:           2,
	}
	seed, err := NewSession(seedModel, "system instructions", SessionOptions{
		Store:   st,
		Context: cfg,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		input := fmt.Sprintf("old-%02d %s", i, strings.Repeat("u", 160))
		if err := seed.Ask(ctx, input, nil); err != nil {
			t.Fatalf("seed Ask(%d): %v", i, err)
		}
	}

	// Reopening forces the next request to be assembled from durable turn groups,
	// rather than from a mutable flat message slice.
	model := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("ok", nil)}}},
	}}
	session, err := OpenSession(model, st, seed.ID(), SessionOptions{
		Store: st,
		Context: contextbuild.Config{
			WindowTokens:              500,
			MaxOutputTokens:           100,
			AutoCompactTriggerPercent: 75,
			PostCompactTargetPercent:  45,
			KeepRecentTurns:           2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := session.Ask(ctx, "latest question", nil); err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if len(model.requests) != 1 {
		t.Fatalf("model calls=%d", len(model.requests))
	}
	// The durable planner omits whole old turn groups, never a hand-mutated
	// message prefix, before sending the request to the model.
	if len(model.requests[0]) >= 18 {
		t.Fatalf("model received all durable messages (%d); want bounded view", len(model.requests[0]))
	}
	if model.requests[0][0].Role != schema.System {
		t.Fatalf("first role=%s", model.requests[0][0].Role)
	}
	status := session.ContextStatus()
	if status.OmittedTurnGroups == 0 || status.CurrentTokens > status.BudgetTokens {
		t.Fatalf("bounded prompt status = %+v", status)
	}
}

func TestOpenSessionLoadsTranscript(t *testing.T) {
	st := newDurableThreadStore(t)
	ctx := context.Background()
	seedModel := &scriptedModel{streams: []Stream{
		&scriptedStream{events: []streamEvent{{message: schema.AssistantMessage("a1", nil)}}},
	}}
	seed, err := NewSession(seedModel, "sys", SessionOptions{
		Store: st,
		ID:    "open-1",
		Title: "loaded",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := seed.Ask(ctx, "u1", nil); err != nil {
		t.Fatalf("seed Ask: %v", err)
	}

	model := &scriptedModel{}
	session, err := OpenSession(model, st, "open-1", SessionOptions{Store: st})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if session.ID() != "open-1" || session.Title() != "loaded" {
		t.Errorf("id/title = %q / %q", session.ID(), session.Title())
	}
	assertMessages(t, session.Transcript(), []messageExpectation{
		{role: schema.System, content: "sys"},
		{role: schema.User, content: "u1"},
		{role: schema.Assistant, content: "a1"},
	})
}

type scriptedModel struct {
	streams      []Stream
	err          error
	beforeStream func()
	requests     [][]*schema.Message
}

func (m *scriptedModel) Stream(_ context.Context, messages []*schema.Message) (Stream, error) {
	m.requests = append(m.requests, append([]*schema.Message(nil), messages...))
	if m.beforeStream != nil {
		m.beforeStream()
	}
	if m.err != nil {
		return nil, m.err
	}
	if len(m.streams) == 0 {
		return nil, errors.New("unexpected model call")
	}

	stream := m.streams[0]
	m.streams = m.streams[1:]
	return stream, nil
}

type streamEvent struct {
	message *schema.Message
	err     error
}

type scriptedStream struct {
	events []streamEvent
	next   int
	closed bool
}

func (s *scriptedStream) Recv() (*schema.Message, error) {
	if s.next >= len(s.events) {
		return nil, io.EOF
	}

	event := s.events[s.next]
	s.next++
	return event.message, event.err
}

func (s *scriptedStream) Close() {
	s.closed = true
}

type blockingStream struct {
	closed chan struct{}
	once   sync.Once
}

func (s *blockingStream) Recv() (*schema.Message, error) {
	<-s.closed
	return nil, context.Canceled
}

func (s *blockingStream) Close() {
	s.once.Do(func() { close(s.closed) })
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
		if got[i] == nil {
			t.Errorf("message %d = nil", i)
			continue
		}
		if got[i].Role != expected.role {
			t.Errorf("message %d role = %q, want %q", i, got[i].Role, expected.role)
		}
		if got[i].Content != expected.content {
			t.Errorf("message %d content = %q, want %q", i, got[i].Content, expected.content)
		}
	}
}

func newDurableThreadStore(t *testing.T) *store.ThreadStore {
	t.Helper()
	st, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	return st
}
