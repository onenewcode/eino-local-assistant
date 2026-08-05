package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/usage"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEmitFromTurnEventPreservesRawToolPayload(t *testing.T) {
	ch := make(chan tea.Msg, 2)
	emit := emitFromTurnEvent(context.Background(), 17, 23, ch)

	input := strings.Repeat("i", toolBodyMaxRunes+200)
	output := strings.Repeat("o", toolBodyMaxRunes+200)
	emit(chat.TurnEvent{Kind: chat.TurnEventToolStart, Tool: "read_file", Input: input})
	emit(chat.TurnEvent{Kind: chat.TurnEventToolEnd, Tool: "read_file", Output: output})

	start, ok := (<-ch).(turnToolStartMsg)
	if !ok {
		t.Fatal("first event should be a tool-start message")
	}
	if start.sessionGeneration != 23 {
		t.Fatalf("tool-start generation = %d, want 23", start.sessionGeneration)
	}
	if start.input != input {
		t.Fatalf("tool input was truncated: got %d want %d runes", len([]rune(start.input)), len([]rune(input)))
	}
	end, ok := (<-ch).(turnToolEndMsg)
	if !ok {
		t.Fatal("second event should be a tool-end message")
	}
	if end.sessionGeneration != 23 {
		t.Fatalf("tool-end generation = %d, want 23", end.sessionGeneration)
	}
	if end.output != output {
		t.Fatalf("tool output was truncated: got %d want %d runes", len([]rune(end.output)), len([]rune(output)))
	}
}

func TestEmitFromTurnEventForwardsModelUsage(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	emit := emitFromTurnEvent(context.Background(), 17, 23, ch)
	emit(chat.TurnEvent{
		Kind: chat.TurnEventModelUsage,
		ModelUsage: &chat.ModelUsageEvent{
			CallID:    "call-1",
			Operation: chat.ModelUsageOperationAgent,
			Usage: usage.Turn{
				PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CachedTokens: 4,
			},
			Available: true,
		},
	})

	got, ok := (<-ch).(turnUsageMsg)
	if !ok {
		t.Fatal("model usage should become a turnUsageMsg")
	}
	if got.turnID != 17 || got.sessionGeneration != 23 || got.usage.CallID != "call-1" || got.usage.Usage.TotalTokens != 12 || got.usage.Usage.CachedTokens != 4 {
		t.Fatalf("usage msg=%+v", got)
	}
}

func TestEmitFromTurnEventForwardsReasoning(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	emit := emitFromTurnEvent(context.Background(), 9, 23, ch)
	emit(chat.TurnEvent{Kind: chat.TurnEventReasoning, Chunk: "ponder"})
	got, ok := (<-ch).(turnReasoningMsg)
	if !ok {
		t.Fatal("reasoning should become turnReasoningMsg")
	}
	if got.turnID != 9 || got.sessionGeneration != 23 || got.chunk != "ponder" {
		t.Fatalf("got %+v", got)
	}
}

func TestEmitFromTurnEventForwardsSteerConsumption(t *testing.T) {
	ch := make(chan tea.Msg, 1)
	emit := emitFromTurnEvent(context.Background(), 9, 23, ch)
	emit(chat.TurnEvent{
		Kind:          chat.TurnEventSteerConsumed,
		SteerSequence: 4,
		SteerContent:  "redirect",
	})
	got, ok := (<-ch).(turnSteerConsumedMsg)
	if !ok {
		t.Fatal("steer consumption should become a turnSteerConsumedMsg")
	}
	if got.turnID != 9 || got.sessionGeneration != 23 || got.sequence != 4 || got.content != "redirect" {
		t.Fatalf("got %+v", got)
	}
}

func TestEmitFromTurnEventUnblocksOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan tea.Msg)
	emit := emitFromTurnEvent(ctx, 17, 23, ch)
	done := make(chan struct{})
	go func() {
		emit(chat.TurnEvent{Kind: chat.TurnEventChunk, Chunk: "blocked"})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled event emitter remained blocked")
	}
}
