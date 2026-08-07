package tui

import (
	"context"
	"time"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

type turnChunkMsg struct {
	turnID            int
	sessionGeneration uint64
	chunk             string
}

// turnReasoningMsg is display-only model reasoning (ReasoningContent).
// It is never written to the session ledger.
type turnReasoningMsg struct {
	turnID            int
	sessionGeneration uint64
	chunk             string
}

// turnSteerConsumedMsg is display-only feedback from a model-call boundary.
// The steer text is not a user transcript line.
type turnSteerConsumedMsg struct {
	turnID            int
	sessionGeneration uint64
	sequence          uint64
	content           string
}

type turnToolStartMsg struct {
	turnID            int
	sessionGeneration uint64
	tool              string
	callID            string
	input             string
}

type turnToolEndMsg struct {
	turnID            int
	sessionGeneration uint64
	tool              string
	callID            string
	output            string
}

type turnToolErrorMsg struct {
	turnID            int
	sessionGeneration uint64
	tool              string
	callID            string
	err               error
}

// turnUsageMsg carries one completed provider call for the turn footer line.
// Durable accounting stays in the session ledger; context occupancy is on the
// global status bar (not repeated in this footer).
type turnUsageMsg struct {
	turnID            int
	sessionGeneration uint64
	usage             chat.ModelUsageEvent
}

// turnTaskStatusMsg is a fresh plan projection emitted after a durable tool
// result has changed the checklist.
type turnTaskStatusMsg struct {
	turnID            int
	sessionGeneration uint64
	status            chat.TaskRunStatus
}

type turnDoneMsg struct {
	turnID            int
	sessionGeneration uint64
	err               error
}

// sideQuestionDoneMsg is display-only and never enters the main turn stream.
type sideQuestionDoneMsg struct {
	requestID         uint64
	label             string
	sessionID         string
	sessionGeneration uint64
	answer            string
	err               error
	unavailable       bool
}

type reviewDoneMsg struct {
	requestID         uint64
	sessionID         string
	sessionGeneration uint64
	answer            string
	err               error
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
func emitFromTurnEvent(ctx context.Context, turnID int, sessionGeneration uint64, ch chan<- tea.Msg) chat.EventEmitter {
	return func(ev chat.TurnEvent) {
		var msg tea.Msg
		switch ev.Kind {
		case chat.TurnEventChunk:
			msg = turnChunkMsg{turnID: turnID, sessionGeneration: sessionGeneration, chunk: ev.Chunk}
		case chat.TurnEventReasoning:
			msg = turnReasoningMsg{turnID: turnID, sessionGeneration: sessionGeneration, chunk: ev.Chunk}
		case chat.TurnEventSteerConsumed:
			msg = turnSteerConsumedMsg{
				turnID:            turnID,
				sessionGeneration: sessionGeneration,
				sequence:          ev.SteerSequence,
				content:           ev.SteerContent,
			}
		case chat.TurnEventToolStart:
			msg = turnToolStartMsg{turnID: turnID, sessionGeneration: sessionGeneration, tool: ev.Tool, callID: ev.ToolCallID, input: ev.Input}
		case chat.TurnEventToolEnd:
			msg = turnToolEndMsg{turnID: turnID, sessionGeneration: sessionGeneration, tool: ev.Tool, callID: ev.ToolCallID, output: ev.Output}
		case chat.TurnEventToolError:
			msg = turnToolErrorMsg{turnID: turnID, sessionGeneration: sessionGeneration, tool: ev.Tool, callID: ev.ToolCallID, err: ev.Err}
		case chat.TurnEventModelUsage:
			if ev.ModelUsage == nil {
				return
			}
			// The producer may reuse its event object after Emit returns.
			msg = turnUsageMsg{turnID: turnID, sessionGeneration: sessionGeneration, usage: *ev.ModelUsage}
		case chat.TurnEventTaskStatus:
			if ev.TaskStatus == nil {
				return
			}
			msg = turnTaskStatusMsg{turnID: turnID, sessionGeneration: sessionGeneration, status: *ev.TaskStatus}
		default:
			return
		}
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}
}
