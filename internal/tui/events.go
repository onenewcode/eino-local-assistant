package tui

import (
	"context"
	"time"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

type turnChunkMsg struct {
	turnID int
	chunk  string
}

type turnToolStartMsg struct {
	turnID int
	tool   string
	callID string
	input  string
}

type turnToolEndMsg struct {
	turnID int
	tool   string
	callID string
	output string
}

type turnToolErrorMsg struct {
	turnID int
	tool   string
	callID string
	err    error
}

type turnDoneMsg struct {
	turnID int
	err    error
}

// compactDoneMsg returns the outcome of a dedicated manual or automatic
// compaction operation to the Bubble Tea state machine.
type compactDoneMsg struct {
	compactID int
	automatic bool
	result    chat.CompactionResult
	err       error
}

type statusTickMsg time.Time

func waitTurnEvent(events <-chan tea.Msg, done <-chan turnDoneMsg, pending *turnDoneMsg) tea.Cmd {
	return func() tea.Msg {
		// Completion is published only after events is closed. If a select saw it
		// first, drain every already-buffered display event before releasing busy
		// state, otherwise a fast successful stream can disappear from the TUI.
		if pending != nil {
			if events == nil {
				return *pending
			}
			msg, ok := <-events
			if !ok {
				return *pending
			}
			return msg
		}
		for events != nil || done != nil {
			select {
			case msg := <-done:
				return msg
			case msg, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				return msg
			}
		}
		return nil
	}
}

// emitFromTurnEvent bridges chat events into the Bubble Tea event channel.
// Tool payloads stay verbatim here: persistence can retain raw evidence while
// formatToolCard applies the separate, display-only visual cap.
// Sends preserve all events while a turn is active, but cancellation releases
// a blocked producer so the durable turn can publish its completion signal.
func emitFromTurnEvent(ctx context.Context, turnID int, ch chan<- tea.Msg) chat.EventEmitter {
	return func(ev chat.TurnEvent) {
		var msg tea.Msg
		switch ev.Kind {
		case chat.TurnEventChunk:
			msg = turnChunkMsg{turnID: turnID, chunk: ev.Chunk}
		case chat.TurnEventToolStart:
			msg = turnToolStartMsg{turnID: turnID, tool: ev.Tool, callID: ev.ToolCallID, input: ev.Input}
		case chat.TurnEventToolEnd:
			msg = turnToolEndMsg{turnID: turnID, tool: ev.Tool, callID: ev.ToolCallID, output: ev.Output}
		case chat.TurnEventToolError:
			msg = turnToolErrorMsg{turnID: turnID, tool: ev.Tool, callID: ev.ToolCallID, err: ev.Err}
		default:
			return
		}
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}
}
