package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEmitFromTurnEventPreservesRawToolPayload(t *testing.T) {
	ch := make(chan tea.Msg, 2)
	emit := emitFromTurnEvent(context.Background(), 17, ch)

	input := strings.Repeat("i", toolBodyMaxRunes+200)
	output := strings.Repeat("o", toolBodyMaxRunes+200)
	emit(chat.TurnEvent{Kind: chat.TurnEventToolStart, Tool: "read_file", Input: input})
	emit(chat.TurnEvent{Kind: chat.TurnEventToolEnd, Tool: "read_file", Output: output})

	start, ok := (<-ch).(turnToolStartMsg)
	if !ok {
		t.Fatal("first event should be a tool-start message")
	}
	if start.input != input {
		t.Fatalf("tool input was truncated: got %d want %d runes", len([]rune(start.input)), len([]rune(input)))
	}
	end, ok := (<-ch).(turnToolEndMsg)
	if !ok {
		t.Fatal("second event should be a tool-end message")
	}
	if end.output != output {
		t.Fatalf("tool output was truncated: got %d want %d runes", len([]rune(end.output)), len([]rune(output)))
	}
}

func TestEmitFromTurnEventUnblocksOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan tea.Msg)
	emit := emitFromTurnEvent(ctx, 17, ch)
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
