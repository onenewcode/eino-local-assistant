package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStatusLineCommandOpensCodexStylePickerAndSavesDraft(t *testing.T) {
	m := newTestModel(t)
	m.deps.Status.Model = "openai/gpt-5.6-terra"
	m.deps.Status.ReasoningEffort = "xhigh"
	var saved StatusLineConfig
	m.deps.SaveStatusLineConfig = func(config StatusLineConfig) error {
		saved = copyStatusLineConfig(config)
		return nil
	}

	next, cmd := m.submit("/statusline")
	m = next.(*model)
	if cmd != nil {
		t.Fatalf("/statusline should not start a turn: %v", cmd)
	}
	if !m.statusLinePickerOpen() {
		t.Fatal("/statusline did not open the picker")
	}
	view := m.View()
	for _, want := range []string{
		"Configure Status Line", "Type to search",
		"model-with-reasoning", "context-used", "used-tokens", "task-progress", "activity", "mode",
		"gpt-5.6-terra xhigh", "Context 0% used", "0 used", "Tasks 0/0",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "openai/gpt-5.6-terra") {
		t.Fatalf("picker preview leaked provider prefix:\n%s", view)
	}
	if strings.Count(view, "gpt-5.6-terra xhigh") != 1 {
		t.Fatalf("picker should render one live status line below the composer:\n%s", view)
	}
	if strings.Contains(view, "Preview") {
		t.Fatalf("picker must not render a second inline preview:\n%s", view)
	}
	if strings.Index(view, "gpt-5.6-terra xhigh") < strings.Index(view, "Message the assistant") {
		t.Fatalf("live status line must render below the composer:\n%s", view)
	}

	for range 4 {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(*model)
	}
	// Activity is selected last. Remove it from the private draft before
	// committing it with Enter.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(*model)
	if !m.statusLinePickerOpen() {
		t.Fatal("draft changes must not apply before Enter")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if m.statusLinePickerOpen() {
		t.Fatal("successful save left picker open")
	}
	want := StatusLineConfig{
		Fields: []string{statusFieldModelWithReasoning, statusFieldContextUsed, statusFieldUsedTokens, statusFieldTaskProgress, statusFieldMode},
	}
	if !reflect.DeepEqual(m.deps.StatusLine, want) || !reflect.DeepEqual(saved, want) {
		t.Fatalf("saved status line = %#v, callback=%#v, want=%#v", m.deps.StatusLine, saved, want)
	}
}

func TestStatusLinePickerSearchAndCancelDiscardDraft(t *testing.T) {
	m := newTestModel(t)
	m.deps.SaveStatusLineConfig = func(StatusLineConfig) error { return nil }
	committed := copyStatusLineConfig(m.deps.StatusLine)

	next, _ := m.submit("/statusline")
	m = next.(*model)
	for _, r := range "used" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(*model)
	}
	view := m.View()
	if !strings.Contains(view, "used-tokens") || strings.Contains(view, "activity") {
		t.Fatalf("search did not filter picker rows:\n%s", view)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*model)
	if m.statusLinePickerOpen() {
		t.Fatal("esc did not close picker")
	}
	if !reflect.DeepEqual(m.deps.StatusLine, committed) {
		t.Fatalf("esc applied draft: got %#v want %#v", m.deps.StatusLine, committed)
	}
}

func TestStatusLinePickerFailedSaveKeepsDraftAndCommittedSettings(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLine = StatusLineConfig{Fields: []string{statusFieldModelWithReasoning}}
	m.deps.SaveStatusLineConfig = func(StatusLineConfig) error { return errors.New("read-only config") }

	next, _ := m.submit("/statusline")
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if !m.statusLinePickerOpen() {
		t.Fatal("failed save should keep picker open")
	}
	if want := []string{statusFieldModelWithReasoning}; !reflect.DeepEqual(m.deps.StatusLine.Fields, want) {
		t.Fatalf("failed save applied fields = %#v, want %#v", m.deps.StatusLine.Fields, want)
	}
	if !hasLineContaining(m.lines, lineError, "save status-line settings: read-only config") {
		t.Fatalf("save failure missing from transcript: %#v", m.lines)
	}
}

func TestStatusLinePickerCombinesModelAndReasoning(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLine = StatusLineConfig{
		Fields: []string{
			statusFieldModelWithReasoning,
			statusFieldContextUsed,
			statusFieldUsedTokens,
			statusFieldTaskProgress,
		},
	}
	m.deps.StatusLine = normalizeStatusLineConfig(m.deps.StatusLine)
	if want := []string{statusFieldModelWithReasoning, statusFieldContextUsed, statusFieldUsedTokens, statusFieldTaskProgress}; !reflect.DeepEqual(m.deps.StatusLine.Fields, want) {
		t.Fatalf("combined model field = %#v, want %#v", m.deps.StatusLine.Fields, want)
	}
	m.deps.Status.ReasoningEffort = "high"
	segments := m.statusLineSegments()
	if len(segments) == 0 || segments[0].field != statusFieldModelWithReasoning || segments[0].text != "test-model high" {
		t.Fatalf("combined model/reasoning segment = %#v", segments)
	}
	m.deps.Status.ReasoningEffort = ""
	segments = m.statusLineSegments()
	if len(segments) == 0 || segments[0].text != "test-model medium" {
		t.Fatalf("omitted reasoning effort must render medium = %#v", segments)
	}
	if strings.Contains(segments[0].text, "default") {
		t.Fatalf("status line must not render a default placeholder = %#v", segments)
	}
}

func TestStatusLineCommandRejectsTextArguments(t *testing.T) {
	m := newTestModel(t)
	next, _ := m.submit("/statusline set model")
	m = next.(*model)
	if m.statusLinePickerOpen() || !hasLineContaining(m.lines, lineError, statusLineCommandUsage) {
		t.Fatalf("text argument should be rejected: %#v", m.lines)
	}
}
