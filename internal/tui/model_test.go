package tui

import (
	"context"
	"io"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/schema"
)

func TestSubmitHelpAndClear(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	if err := session.Ask(context.Background(), "remember this", nil); err != nil {
		t.Fatalf("seed ask: %v", err)
	}
	if got := len(session.Transcript()); got < 3 {
		t.Fatalf("seed transcript len=%d want >=3", got)
	}

	m := newModel(Deps{Ctx: context.Background(), Session: session, Status: StatusInfo{Model: "test-model", Tools: []string{"get_current_time"}}})
	m.appendLine(lineUser, "remember this")
	m.queue = []string{"follow-up"}

	next, cmd := m.submit("/help")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("help should not start a turn")
	}
	if !hasLineContaining(mm.lines, lineSystem, "Commands:") {
		t.Fatalf("help text missing: %#v", mm.lines)
	}

	next, _ = mm.submit("/clear")
	mm = next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "context cleared") {
		t.Fatalf("clear confirmation missing: %#v", mm.lines)
	}
	if len(mm.queue) != 0 {
		t.Fatalf("queue should be cleared, got %#v", mm.queue)
	}
	transcript := mm.deps.Session.Transcript()
	if len(transcript) != 1 || transcript[0].Role != schema.System || transcript[0].Content != "system" {
		t.Fatalf("transcript after clear = %#v", transcript)
	}
	for _, line := range mm.lines {
		if line.kind == lineUser {
			t.Fatalf("user transcript should be gone after clear: %#v", mm.lines)
		}
	}
}

func TestSubmitStatus(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session, Status: StatusInfo{Model: "deepseek", Tools: []string{"get_current_time"}}})
	next, _ := m.submit("/status")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "model=deepseek") {
		t.Fatalf("status missing model: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "get_current_time") {
		t.Fatalf("status missing tools: %#v", mm.lines)
	}
}

func TestChunkAndToolEventsUpdateTranscript(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	m.turnID = 1
	m.mode = modeBusy
	m.width = 80
	m.layout()

	next, _ := m.Update(turnToolStartMsg{turnID: 1, tool: "get_current_time", input: "{}"})
	mm := next.(*model)
	next, _ = mm.Update(turnToolEndMsg{turnID: 1, tool: "get_current_time", output: `{"datetime":"2026-07-14"}`})
	mm = next.(*model)
	next, _ = mm.Update(turnChunkMsg{turnID: 1, chunk: "Hello"})
	mm = next.(*model)
	next, _ = mm.Update(turnChunkMsg{turnID: 1, chunk: " world"})
	mm = next.(*model)

	if !hasLineContaining(mm.lines, lineTool, "get_current_time") {
		t.Fatalf("tool line missing: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineAssistant, "Hello world") {
		t.Fatalf("assistant stream missing: %#v", mm.lines)
	}
}

func TestTurnCompletionDrainsBufferedStreamEvents(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	m.turnID = 1
	m.mode = modeBusy
	m.events = make(chan tea.Msg, 1)
	m.events <- turnChunkMsg{turnID: 1, chunk: "fast reply"}
	close(m.events)
	m.turnDone = make(chan turnDoneMsg, 1)
	m.turnDone <- turnDoneMsg{turnID: 1}

	// This models the scheduler choosing the ready completion before the
	// buffered chunk. The model must defer finish until the display queue drains.
	next, cmd := m.Update(turnDoneMsg{turnID: 1})
	mm := next.(*model)
	if mm.mode != modeBusy || mm.pendingTurnDone == nil {
		t.Fatalf("turn finished before buffered events drained: mode=%v pending=%#v", mm.mode, mm.pendingTurnDone)
	}
	msg := cmd()
	next, cmd = mm.Update(msg)
	mm = next.(*model)
	if !hasLineContaining(mm.lines, lineAssistant, "fast reply") {
		t.Fatalf("buffered response chunk was dropped: %#v", mm.lines)
	}
	msg = cmd()
	next, _ = mm.Update(msg)
	mm = next.(*model)
	if mm.mode != modeIdle || mm.pendingTurnDone != nil {
		t.Fatalf("turn did not finish after draining events: mode=%v pending=%#v", mm.mode, mm.pendingTurnDone)
	}
}

func TestStaleTurnEventsAreIgnored(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	m.turnID = 2
	before := len(m.lines)
	next, _ := m.Update(turnChunkMsg{turnID: 1, chunk: "stale"})
	mm := next.(*model)
	if len(mm.lines) != before {
		t.Fatalf("stale chunk should be ignored")
	}
}

func hasLineContaining(lines []transcriptLine, kind lineKind, substr string) bool {
	for _, line := range lines {
		if line.kind == kind && strings.Contains(line.text, substr) {
			return true
		}
	}
	return false
}

func mustSession(t *testing.T, model chat.Model, system string) *chat.Session {
	t.Helper()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	session, err := chat.NewSession(model, system, chat.SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

type staticModel struct{}

func (staticModel) Stream(_ context.Context, _ []*schema.Message) (chat.Stream, error) {
	return &staticStream{}, nil
}

type staticStream struct{}

func (s *staticStream) Recv() (*schema.Message, error) {
	return nil, io.EOF
}

func (s *staticStream) Close() {}
