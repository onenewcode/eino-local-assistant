package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/store"

	tea "github.com/charmbracelet/bubbletea"
)

func TestForkPickOpensSearchableActiveSessionPicker(t *testing.T) {
	ctx := context.Background()
	m, threadStore, current := newSessionPickerTestModel(t)
	target := newSessionPickerTestSession(t, threadStore, "Release branch")
	archived := newSessionPickerTestSession(t, threadStore, "Archived branch")
	state, err := threadStore.LoadThread(ctx, archived.ID())
	if err != nil {
		t.Fatalf("LoadThread archived: %v", err)
	}
	if _, err := threadStore.ArchiveThread(ctx, archived.ID(), state.Revision); err != nil {
		t.Fatalf("ArchiveThread: %v", err)
	}
	m.textarea.SetValue("draft must survive fork picker")
	viewportHeight := m.viewport.Height

	next, _ := m.submit("/fork --pick")
	m = next.(*model)
	if !m.sessionPickerOpen() || m.sessionPicker.intent != sessionPickerFork || m.activeSession() != current || m.textarea.Value() != "draft must survive fork picker" {
		t.Fatalf("fork picker changed state: picker=%#v active=%p draft=%q", m.sessionPicker, m.activeSession(), m.textarea.Value())
	}
	rows := m.sessionPickerRows()
	if len(rows) != 2 {
		t.Fatalf("fork picker rows = %#v, want current and target", rows)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ID] = true
	}
	if !seen[current.ID()] || !seen[target.ID()] || seen[archived.ID()] {
		t.Fatalf("fork picker scope = %#v", rows)
	}
	if m.viewport.Height >= viewportHeight {
		t.Fatalf("fork picker did not reserve viewport height: before=%d after=%d", viewportHeight, m.viewport.Height)
	}
	view := m.View()
	for _, want := range []string{"Fork Session", "Release branch", "enter fork"} {
		if !strings.Contains(view, want) {
			t.Fatalf("fork picker view missing %q:\n%s", want, view)
		}
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("release")})
	m = next.(*model)
	if rows = m.sessionPickerRows(); len(rows) != 1 || rows[0].ID != target.ID() {
		t.Fatalf("fork picker search rows = %#v, want target %q", rows, target.ID())
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*model)
	if m.sessionPickerOpen() || m.activeSession() != current || m.textarea.Value() != "draft must survive fork picker" || m.viewport.Height != viewportHeight {
		t.Fatalf("fork picker cancel changed state: picker=%v active=%p draft=%q height=%d", m.sessionPickerOpen(), m.activeSession(), m.textarea.Value(), m.viewport.Height)
	}
}

func TestForkPickerConfirmationUsesSelectedSource(t *testing.T) {
	ctx := context.Background()
	m, threadStore, current := newSessionPickerTestModel(t)
	target := newSessionPickerTestSession(t, threadStore, "Investigation branch")
	if err := target.Ask(ctx, "persist target parent", nil); err != nil {
		t.Fatalf("target Ask: %v", err)
	}
	m.textarea.SetValue("draft stays after fork picker confirmation")

	next, _ := m.submit("/fork --pick")
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("investigation")})
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	child := m.activeSession()
	if child == nil || child.ID() == current.ID() || child.ID() == target.ID() || m.sessionPickerOpen() {
		t.Fatalf("fork picker confirmation did not activate a child: active=%#v picker=%v", child, m.sessionPickerOpen())
	}
	meta, err := threadStore.LoadThreadMeta(ctx, child.ID())
	if err != nil {
		t.Fatalf("LoadThreadMeta child: %v", err)
	}
	if meta.ParentID != target.ID() || m.textarea.Value() != "draft stays after fork picker confirmation" {
		t.Fatalf("fork picker child/draft = parent=%q draft=%q", meta.ParentID, m.textarea.Value())
	}
}

func TestForkPickerFailureLeavesPickerOpen(t *testing.T) {
	m, threadStore, current := newSessionPickerTestModel(t)
	target := newSessionPickerTestSession(t, threadStore, "Empty parent")
	m.textarea.SetValue("draft must survive fork failure")
	m.sessionPicker = &sessionPickerState{intent: sessionPickerFork, entries: []store.ThreadMeta{{ID: target.ID(), Title: target.Title()}}}
	m.layout()
	m.refreshViewport()

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if !m.sessionPickerOpen() || m.activeSession() != current || m.textarea.Value() != "draft must survive fork failure" {
		t.Fatalf("failed fork picker changed state: picker=%v active=%p draft=%q", m.sessionPickerOpen(), m.activeSession(), m.textarea.Value())
	}
	if !hasLineContaining(m.lines, lineError, store.ErrForkNoCommittedTurn.Error()) {
		t.Fatalf("fork picker failure missing durable error: %#v", m.lines)
	}
}
