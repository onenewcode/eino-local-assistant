package tui

import (
	"strings"
	"testing"
)

func TestFormatToolCardPrettyJSON(t *testing.T) {
	card := formatToolCard("get_current_time", `{"datetime":"2026-07-14","tz":"UTC"}`, "ok")
	if !strings.HasPrefix(card, "get_current_time\n") {
		t.Fatalf("header: %q", card)
	}
	if !strings.Contains(card, "  ⎿  {") {
		t.Fatalf("expected indented JSON start: %q", card)
	}
	if !strings.Contains(card, `"datetime"`) {
		t.Fatalf("expected pretty field: %q", card)
	}
}

func TestFormatToolCardRunningArgsOnHeader(t *testing.T) {
	run := formatToolCard("get_current_time", `{"timezone":"UTC"}`, "run")
	if !strings.Contains(run, "running…") {
		t.Fatalf("run card body: %q", run)
	}
	// Args should appear on the header line.
	first := strings.SplitN(run, "\n", 2)[0]
	if !strings.Contains(first, "timezone=") {
		t.Fatalf("expected arg summary on header: %q", first)
	}
	errCard := formatToolCard("t", "boom", "err")
	if !strings.Contains(errCard, "  ✗  boom") {
		t.Fatalf("err card: %q", errCard)
	}
}

func TestFormatToolCardClampsRawOutputForDisplay(t *testing.T) {
	raw := strings.Repeat("x", toolBodyMaxRunes+200)
	card := formatToolCard("large_output", raw, "ok")
	if strings.Contains(card, raw) {
		t.Fatalf("tool card should not render the full raw payload")
	}
	if !strings.Contains(card, "…") {
		t.Fatalf("clamped tool card should mark omitted output: %q", card)
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
	if !strings.Contains(mm.lines[startIdx].text, "datetime") {
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
	next, _ = mm.Update(turnToolEndMsg{turnID: 1, tool: "search", callID: "call-b", output: "result B"})
	mm = next.(*model)
	next, _ = mm.Update(turnToolEndMsg{turnID: 1, tool: "search", callID: "call-a", output: "result A"})
	mm = next.(*model)

	var cardA, cardB string
	for _, line := range mm.lines {
		if line.kind != lineTool {
			continue
		}
		if strings.Contains(line.text, "result A") {
			cardA = line.text
		}
		if strings.Contains(line.text, "result B") {
			cardB = line.text
		}
	}
	if cardA == "" || cardB == "" || strings.Contains(cardA, "result B") || strings.Contains(cardB, "result A") {
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
