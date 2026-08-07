package tui

import (
	"strings"
	"testing"
)

func TestRenderErrorWrapsLongProviderMessage(t *testing.T) {
	long := "start response stream: [NodeRunError] error, status code: 400, status: 400 Bad Request, message: An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)\n------------------------\nnode path: [chat]"
	out := renderError(long, 40)
	lines := strings.Split(out, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected multi-line wrap, got %d lines: %q", len(lines), out)
	}
	for i, line := range lines[1:] {
		// Continuation lines should start with two spaces (possibly after ANSI).
		if !strings.Contains(line, "  ") {
			t.Fatalf("continuation line %d not indented: %q", i+1, line)
		}
	}
}

func TestFormatToolCardUsesCompactJSONSummary(t *testing.T) {
	card := formatToolCard("get_current_time", `{"datetime":"2026-07-14","tz":"UTC"}`, "ok")
	if card != "get_current_time  datetime=2026-07-14" {
		t.Fatalf("card: %q", card)
	}
	if strings.Contains(card, "\n") || strings.Contains(card, `"tz"`) {
		t.Fatalf("default card must not dump JSON: %q", card)
	}
}

func TestFormatToolCardRunningArgsOnHeader(t *testing.T) {
	run := formatToolCard("get_current_time", `{"timezone":"UTC"}`, "run")
	if !strings.Contains(run, "running…") {
		t.Fatalf("run card: %q", run)
	}
	if !strings.Contains(run, "timezone=") || strings.Contains(run, "\n") {
		t.Fatalf("expected compact running summary: %q", run)
	}
	errCard := formatToolCard("t", "boom", "err")
	if !strings.Contains(errCard, "failed: boom") || strings.Contains(errCard, "\n") {
		t.Fatalf("err card: %q", errCard)
	}
}

func TestFormatToolCardSummarizesShellWithoutSandboxJSON(t *testing.T) {
	card := formatToolCardWithInput("shell", `{"command":"ls -la /workspace"}`, `{"cancelled":false,"command":"ls -la /workspace","decision":"allow","denied":false,"duration_ms":14,"exit_code":0,"impact":"read_only","sandbox":{"enforced":true}}`, "ok")
	for _, want := range []string{"shell", "ls -la /workspace", "exit 0", "14ms", "read-only"} {
		if !strings.Contains(card, want) {
			t.Fatalf("shell card %q missing %q", card, want)
		}
	}
	if strings.Contains(card, "sandbox") || strings.Contains(card, "\n") || strings.Contains(card, `"`) {
		t.Fatalf("shell card leaked raw result JSON: %q", card)
	}

	errCard := formatToolCardWithInput("shell", `{"command":"rm -rf tmp"}`, "permission denied\nmore diagnostic data", "err")
	if !strings.Contains(errCard, "rm -rf tmp · failed: permission denied more diagnostic data") || strings.Contains(errCard, "\n") {
		t.Fatalf("shell error card: %q", errCard)
	}
}

func TestFormatToolCardExplainsUpdatePlanResult(t *testing.T) {
	card := formatToolCardWithInput("update_plan", `{"plan":[{"step":"inspect","status":"completed"}]}`, `{"ok":true,"run_state":"active","complete":false,"message":"Plan updated","display_hint":"Plan updated"}`, "ok")
	for _, want := range []string{"update_plan", "accepted", "Plan updated"} {
		if !strings.Contains(card, want) {
			t.Fatalf("update_plan card missing %q: %q", want, card)
		}
	}
}

func TestUpdateOpenToolCardInPlace(t *testing.T) {
	m := newTestModel(t)
	m.turnID = 1
	m.mode = modeBusy

	next, _ := m.Update(turnToolStartMsg{turnID: 1, tool: "get_current_time", callID: "call-time", input: `{"timezone":"UTC"}`})
	mm := next.(*model)
	startIdx, ok := mm.openToolCards["call-time"]
	if !ok || startIdx < 0 || startIdx >= len(mm.lines) {
		t.Fatalf("openToolCards=%#v lines=%d", mm.openToolCards, len(mm.lines))
	}
	if mm.lines[startIdx].kind != lineTool || !strings.Contains(mm.lines[startIdx].text, "get_current_time") {
		t.Fatalf("open card bad: %#v", mm.lines[startIdx])
	}

	next, _ = mm.Update(turnToolEndMsg{turnID: 1, tool: "get_current_time", callID: "call-time", output: `{"datetime":"2026-07-14"}`})
	mm = next.(*model)
	toolLines := 0
	for _, l := range mm.lines {
		if l.kind == lineTool {
			toolLines++
		}
	}
	if toolLines != 1 {
		t.Fatalf("want 1 tool card after end, got %d: %#v", toolLines, mm.lines)
	}
	if len(mm.openToolCards) != 0 {
		t.Fatalf("open tool cards should clear, got %#v", mm.openToolCards)
	}
	if !strings.Contains(mm.lines[startIdx].text, "datetime=2026-07-14") || strings.Contains(mm.lines[startIdx].text, "\n") {
		t.Fatalf("result missing: %q", mm.lines[startIdx].text)
	}
}

func TestToolCardsUseCallIDForReverseCompletion(t *testing.T) {
	m := newTestModel(t)
	m.turnID = 1
	m.mode = modeBusy

	next, _ := m.Update(turnToolStartMsg{turnID: 1, tool: "search", callID: "call-a", input: "A"})
	mm := next.(*model)
	next, _ = mm.Update(turnToolStartMsg{turnID: 1, tool: "search", callID: "call-b", input: "B"})
	mm = next.(*model)
	next, _ = mm.Update(turnToolEndMsg{turnID: 1, tool: "search", callID: "call-b", output: `{"status":"B"}`})
	mm = next.(*model)
	next, _ = mm.Update(turnToolEndMsg{turnID: 1, tool: "search", callID: "call-a", output: `{"status":"A"}`})
	mm = next.(*model)

	var cardA, cardB string
	for _, line := range mm.lines {
		if line.kind != lineTool {
			continue
		}
		if strings.Contains(line.text, "status=A") {
			cardA = line.text
		}
		if strings.Contains(line.text, "status=B") {
			cardB = line.text
		}
	}
	if cardA == "" || cardB == "" || strings.Contains(cardA, "status=B") || strings.Contains(cardB, "status=A") {
		t.Fatalf("reverse completion cards were mixed: %#v", mm.lines)
	}
}

func TestRenderAssistantMarkdownFallsBack(t *testing.T) {
	out := renderAssistant("hello", 80, true)
	if !strings.Contains(out, "hello") {
		t.Fatalf("plain stream: %q", out)
	}
	out = renderAssistant("use `fmt` and **bold**", 80, false)
	if !strings.Contains(out, "fmt") {
		t.Fatalf("markdown render lost content: %q", out)
	}
}

func TestRenderComposerHasBorder(t *testing.T) {
	// Rounded border uses box-drawing characters.
	out := renderComposer(40, "› hello")
	if !strings.Contains(out, "╭") && !strings.Contains(out, "┌") {
		// lipgloss rounded border typically uses ╭╮╰╯
		t.Fatalf("expected bordered composer, got %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("composer content missing: %q", out)
	}
}

func TestRenderStatusBarWidth(t *testing.T) {
	out := renderStatusBar(30, "ready · model")
	if !strings.Contains(out, "ready") {
		t.Fatalf("status bar: %q", out)
	}
}
