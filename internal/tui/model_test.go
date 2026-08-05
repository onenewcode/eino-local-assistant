package tui

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/runtimeguard"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

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

func TestSubmitRulesReportsCapturedMetadata(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: session,
		RulesReport: func() string {
			return "Rules\nuser source path=/home/tester/.eino-assistant/AGENTS.md\nproject source=none"
		},
	})
	next, _ := m.submit("/rules")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "project source=none") {
		t.Fatalf("rules report missing: %#v", mm.lines)
	}
}

func TestSubmitRulesRejectsArguments(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	next, _ := m.submit("/rules now")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineError, "usage: /rules") {
		t.Fatalf("rules arg error missing: %#v", mm.lines)
	}
}

func TestSubmitRulesWithoutCallbackExplainsUnavailableMetadata(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	next, _ := m.submit("/rules")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "runtime callback is not configured") {
		t.Fatalf("unavailable report missing: %#v", mm.lines)
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

func TestReasoningEventsStreamThenFold(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{Ctx: context.Background(), Session: session})
	m.turnID = 1
	m.mode = modeBusy
	m.width = 80
	m.layout()

	next, _ := m.Update(turnReasoningMsg{turnID: 1, chunk: "consider "})
	mm := next.(*model)
	next, _ = mm.Update(turnReasoningMsg{turnID: 1, chunk: "options"})
	mm = next.(*model)
	if mm.openReasoning < 0 {
		t.Fatal("expected open reasoning block")
	}
	if !hasLineContaining(mm.lines, lineReasoning, "consider options") {
		t.Fatalf("streaming reasoning missing: %#v", mm.lines)
	}

	// Content must not fold: final model calls interleave reasoning/content.
	next, _ = mm.Update(turnChunkMsg{turnID: 1, chunk: "done"})
	mm = next.(*model)
	if mm.openReasoning < 0 {
		t.Fatal("reasoning must stay open across assistant content")
	}
	if !hasLineContaining(mm.lines, lineAssistant, "done") {
		t.Fatalf("assistant missing: %#v", mm.lines)
	}
	// Streaming indicator tracks the open index, not "last line only".
	if !mm.reasoningIsStreaming(mm.openReasoning) {
		t.Fatal("open reasoning should still count as streaming after content")
	}

	// Tool start folds via appendLine before the card.
	next, _ = mm.Update(turnToolStartMsg{turnID: 1, tool: "search", callID: "c1", input: "{}"})
	mm = next.(*model)
	if mm.openReasoning != noOpenReasoning {
		t.Fatal("reasoning should fold on tool start")
	}
	var folded *transcriptLine
	for i := range mm.lines {
		if mm.lines[i].kind == lineReasoning {
			folded = &mm.lines[i]
			break
		}
	}
	if folded == nil {
		t.Fatalf("reasoning line missing after fold: %#v", mm.lines)
	}
	if !folded.folded || !strings.Contains(folded.text, "thinking") {
		t.Fatalf("expected folded summary, got %#v", *folded)
	}
	// Fold is lossy by design (ephemeral UI); body is not retained.
	if strings.Contains(folded.text, "consider options") && !strings.Contains(folded.text, "chars") {
		t.Fatalf("expected summary form, got %#v", *folded)
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

func TestTurnEventsRejectOldSessionGenerationWithSameTurnID(t *testing.T) {
	m := newTestModel(t)
	m.turnID = 7
	oldGeneration := m.sessionGeneration
	child := mustSession(t, &staticModel{}, "child")
	m.replaceSession(child)
	m.lines = []transcriptLine{{kind: lineSystem, text: "child transcript"}}
	m.mode = modeBusy
	m.turnUsage = usage.APIUsage{Status: store.UsageStatusExact}
	m.turnUsageCallIDs = make(map[string]struct{})
	beforeLines := append([]transcriptLine(nil), m.lines...)

	staleMessages := []tea.Msg{
		turnChunkMsg{turnID: 7, sessionGeneration: oldGeneration, chunk: "old chunk"},
		turnReasoningMsg{turnID: 7, sessionGeneration: oldGeneration, chunk: "old reasoning"},
		turnToolStartMsg{turnID: 7, sessionGeneration: oldGeneration, tool: "old-tool", callID: "old-call", input: "{}"},
		turnToolEndMsg{turnID: 7, sessionGeneration: oldGeneration, tool: "old-tool", callID: "old-call", output: "old output"},
		turnToolErrorMsg{turnID: 7, sessionGeneration: oldGeneration, tool: "old-tool", callID: "old-call", err: errors.New("old error")},
		turnUsageMsg{turnID: 7, sessionGeneration: oldGeneration, usage: chat.ModelUsageEvent{CallID: "old-usage", Available: true}},
		turnTaskGateMsg{turnID: 7, sessionGeneration: oldGeneration, gate: chat.TaskCompletionGate{Summary: "old gate"}},
		turnDoneMsg{turnID: 7, sessionGeneration: oldGeneration, err: errors.New("old done")},
	}
	for _, msg := range staleMessages {
		next, _ := m.Update(msg)
		m = next.(*model)
	}

	if !reflect.DeepEqual(m.lines, beforeLines) {
		t.Fatalf("old session events polluted child transcript: %#v", m.lines)
	}
	if m.turnUsageSeen || m.pendingTurnDone != nil || m.currentTool != "" || len(m.openToolCards) != 0 {
		t.Fatalf("old session events changed child turn state: usage=%v pending=%#v tool=%q cards=%v", m.turnUsageSeen, m.pendingTurnDone, m.currentTool, m.openToolCards)
	}

	next, _ := m.Update(turnChunkMsg{turnID: 7, sessionGeneration: m.sessionGeneration, chunk: "child reply"})
	m = next.(*model)
	if !hasLineContaining(m.lines, lineAssistant, "child reply") {
		t.Fatalf("current session event was not displayed: %#v", m.lines)
	}
}

func TestTurnUsageAccumulatorAggregatesCallsWithoutDoubleCounting(t *testing.T) {
	m := newTestModel(t)
	m.turnUsageCallIDs = make(map[string]struct{})
	m.turnUsage.Status = store.UsageStatusExact

	m.addTurnUsage(chat.ModelUsageEvent{
		CallID:    "call-1",
		Usage:     usage.Turn{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11, CachedTokens: 2, CostUSD: 1.25},
		Available: true,
	})
	m.addTurnUsage(chat.ModelUsageEvent{
		CallID:    "call-2",
		Usage:     usage.Turn{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25, CachedTokens: 4, CostUSD: 2.5},
		Available: true,
	})
	// Replayed event notifications must not inflate the visual round summary.
	m.addTurnUsage(chat.ModelUsageEvent{
		CallID:    "call-1",
		Usage:     usage.Turn{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11, CachedTokens: 2, CostUSD: 1.25},
		Available: true,
	})

	if !m.turnUsageSeen || m.turnUsage.CallCount != 2 || m.turnUsage.PromptTokens != 30 ||
		m.turnUsage.CompletionTokens != 6 || m.turnUsage.CachedTokens != 6 || m.turnUsage.TotalTokens != 36 || m.turnUsage.CostUSD != 3.75 {
		t.Fatalf("turn usage=%+v seen=%v", m.turnUsage, m.turnUsageSeen)
	}
	line := formatTurnUsageLine(m.turnUsage, m.turnUsageSeen)
	// input = uncached = PromptTokens - CachedTokens = 30 - 6 = 24
	for _, want := range []string{"API usage (exact)", "input=24", "completion=6", "cached=6", "total=36", "calls=2", "turn cost~=$3.750"} {
		if !strings.Contains(line, want) {
			t.Fatalf("round usage line %q missing %q", line, want)
		}
	}
	if strings.Contains(line, "prompt=") {
		t.Fatalf("turn footer must not label prompt=: %q", line)
	}
	if strings.Contains(line, "context=") {
		t.Fatalf("turn footer must not repeat context (global status bar owns ctx): %q", line)
	}
}

func TestFinishTurnShowsUsageAfterError(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnUsage = usage.APIUsage{
		PromptTokens:     10,
		CompletionTokens: 2,
		CachedTokens:     0,
		TotalTokens:      12,
		CallCount:        1,
		CostUSD:          0.25,
		Status:           store.UsageStatusExact,
	}
	m.turnUsageSeen = true

	m.finishTurn(errors.New("stream failed"))
	if !hasLineContaining(m.lines, lineError, "stream failed") {
		t.Fatalf("turn error missing: %#v", m.lines)
	}
	if !hasLineContaining(m.lines, lineUsage, "API usage (exact)") || !hasLineContaining(m.lines, lineUsage, "turn cost~=$0.250") {
		t.Fatalf("usage after error missing: %#v", m.lines)
	}
	if hasLineContaining(m.lines, lineUsage, "context=") {
		t.Fatalf("turn footer must not include context=: %#v", m.lines)
	}
}

func TestFinishTurnShowsStableRuntimeDeadlineReason(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy

	m.finishTurn(runtimeguard.ErrTurnDeadlineExceeded)
	if !hasLineContaining(m.lines, lineSystem, runtimeguard.TurnTimeoutReason) {
		t.Fatalf("runtime deadline reason missing: %#v", m.lines)
	}
	if hasLineContaining(m.lines, lineError, "deadline") {
		t.Fatalf("runtime deadline must not render as an opaque error: %#v", m.lines)
	}
}

func TestFinishTurnHidesUsageWhenConfigured(t *testing.T) {
	m := newTestModel(t)
	m.deps.HideTurnUsage = true
	m.mode = modeBusy
	m.turnUsage = usage.APIUsage{
		PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CallCount: 1,
		CostUSD: 0.25, Status: store.UsageStatusExact,
	}
	m.turnUsageSeen = true

	m.finishTurn(nil)
	if hasLineContaining(m.lines, lineUsage, "API usage") || hasLineContaining(m.lines, lineSystem, "API usage") {
		t.Fatalf("usage footer must be hidden when HideTurnUsage: %#v", m.lines)
	}
}

func TestUsageCommandTogglesDisplayOnlyFooter(t *testing.T) {
	m := newTestModel(t)
	if m.deps.HideTurnUsage {
		t.Fatal("default must show turn usage footer")
	}
	next, _ := m.submit("/usage off")
	mm := next.(*model)
	if !mm.deps.HideTurnUsage {
		t.Fatal("/usage off should hide footer")
	}
	if !hasLineContaining(mm.lines, lineSystem, "turn usage footer: off") {
		t.Fatalf("expected off confirmation: %#v", mm.lines)
	}
	next, _ = mm.submit("/usage")
	mm = next.(*model)
	if mm.deps.HideTurnUsage {
		t.Fatal("/usage (toggle) should re-enable footer")
	}
	if !hasLineContaining(mm.lines, lineSystem, "turn usage footer: on") {
		t.Fatalf("expected on confirmation: %#v", mm.lines)
	}
	next, _ = mm.submit("/usage on")
	mm = next.(*model)
	if mm.deps.HideTurnUsage {
		t.Fatal("/usage on must keep footer enabled")
	}
}

func TestTurnUsageFooterNeverEntersSessionTranscript(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnUsage = usage.APIUsage{
		PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CallCount: 1,
		CostUSD: 0.25, Status: store.UsageStatusExact,
	}
	m.turnUsageSeen = true
	before := append([]*schema.Message(nil), m.deps.Session.Transcript()...)

	m.finishTurn(nil)
	if !hasLineContaining(m.lines, lineUsage, "API usage (exact)") {
		t.Fatalf("expected display footer: %#v", m.lines)
	}
	after := m.deps.Session.Transcript()
	if len(after) != len(before) {
		t.Fatalf("session transcript length changed %d -> %d; usage chrome must not persist", len(before), len(after))
	}
	for i, msg := range after {
		if msg == nil {
			continue
		}
		if strings.Contains(msg.Content, "API usage") || strings.Contains(msg.Content, "turn cost~=") {
			t.Fatalf("session message %d contains usage chrome (would reach the model): %q", i, msg.Content)
		}
		if i < len(before) && before[i] != nil && msg.Content != before[i].Content {
			t.Fatalf("session transcript mutated at %d", i)
		}
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
