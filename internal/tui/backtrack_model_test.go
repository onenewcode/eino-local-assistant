package tui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/schema"
)

func TestBacktrackDoubleEscLoadsPromptsAndNavigates(t *testing.T) {
	m, _, _ := newBacktrackModel(t, 3)
	closedHeight := m.viewport.Height

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*model)
	if cmd != nil || m.backtrackState.mode != backtrackArmed {
		t.Fatalf("first Esc: state=%#v cmd=%v", m.backtrackState, cmd)
	}
	if !strings.Contains(m.statusLabel(), "backtrack: armed") || !strings.Contains(m.View(), "edit or submit to cancel") {
		t.Fatalf("armed chrome missing: status=%q view=%q", m.statusLabel(), m.View())
	}

	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*model)
	if cmd != nil || m.backtrackState.mode != backtrackSelecting {
		t.Fatalf("second Esc: state=%#v cmd=%v", m.backtrackState, cmd)
	}
	if len(m.backtrackState.prompts) != 3 || m.backtrackState.selected != 2 {
		t.Fatalf("loaded prompts=%#v selected=%d", m.backtrackState.prompts, m.backtrackState.selected)
	}
	if m.backtrackState.prompts[0].Text != "prompt 1" || m.backtrackState.prompts[1].Text != "prompt 2" || m.backtrackState.prompts[2].Text != "prompt 3" {
		t.Fatalf("loaded prompts=%#v", m.backtrackState.prompts)
	}
	if m.viewport.Height >= closedHeight || !strings.Contains(m.statusLabel(), "backtrack: selecting") || !strings.Contains(m.View(), "Backtrack") {
		t.Fatalf("selector chrome/layout missing: height=%d closed=%d status=%q view=%q", m.viewport.Height, closedHeight, m.statusLabel(), m.View())
	}

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'k'}},
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyUp},
		{Type: tea.KeyDown},
	} {
		next, _ = m.Update(key)
		m = next.(*model)
	}
	if m.backtrackState.selected != 2 {
		t.Fatalf("selection after jk/up/down = %d, want 2", m.backtrackState.selected)
	}
}

func TestBacktrackForksBeforeSelectedPromptAndRefillsComposer(t *testing.T) {
	m, source, _ := newBacktrackModel(t, 3)
	sourceTranscript := source.Transcript()
	sourceID := source.ID()
	enterBacktrackSelection(t, m)
	selected, ok := selectedBacktrackPrompt(m.backtrackState)
	if !ok {
		t.Fatal("backtrack prompt was not selected")
	}
	m.sideLines = []transcriptLine{{kind: lineSide, text: "old side"}}
	m.queue = []string{"old queued"}
	m.taskPaneOpen = true

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if cmd != nil {
		t.Fatalf("successful backtrack returned command: %v", cmd)
	}
	child := m.activeSession()
	if child == nil || child == source || child.ID() == sourceID {
		t.Fatalf("active session after backtrack = %#v", child)
	}
	if m.backtrackState.mode != backtrackInactive || m.textarea.Value() != selected.Text {
		t.Fatalf("backtrack state/composer = %#v/%q", m.backtrackState, m.textarea.Value())
	}
	if m.queue != nil || m.sideLines != nil || m.taskPaneOpen {
		t.Fatalf("session transient state crossed fork: queue=%v side=%v task=%v", m.queue, m.sideLines, m.taskPaneOpen)
	}
	if !reflect.DeepEqual(sourceTranscript, source.Transcript()) {
		t.Fatal("source transcript changed")
	}
	meta, err := child.Store().LoadThreadMeta(context.Background(), child.ID())
	if err != nil {
		t.Fatalf("LoadThreadMeta child: %v", err)
	}
	if meta.ForkBoundaryTurnID != selected.BoundaryTurnID {
		t.Fatalf("child boundary=%q want %q", meta.ForkBoundaryTurnID, selected.BoundaryTurnID)
	}
	for _, message := range child.Transcript() {
		if message != nil && strings.TrimSpace(message.Content) == selected.Text {
			t.Fatalf("selected prompt was written to child transcript: %#v", child.Transcript())
		}
	}
}

func TestBacktrackSingleTurnForksBeforeFirstWithEmptyChild(t *testing.T) {
	m, source, threadStore := newBacktrackModel(t, 1)
	sourceTranscript := source.Transcript()
	sourceState, err := threadStore.LoadThread(context.Background(), source.ID())
	if err != nil {
		t.Fatalf("LoadThread source before fork: %v", err)
	}

	enterBacktrackSelection(t, m)
	selected, ok := selectedBacktrackPrompt(m.backtrackState)
	if !ok {
		t.Fatal("single-turn prompt was not selected")
	}
	if !selected.BeforeFirst || selected.BoundaryTurnID != "" {
		t.Fatalf("single-turn selection = %#v, want explicit before-first boundary", selected)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if cmd != nil {
		t.Fatalf("single-turn backtrack returned command: %v", cmd)
	}
	child := m.activeSession()
	if child == nil || child == source {
		t.Fatalf("active session after single-turn backtrack = %#v", child)
	}
	if m.textarea.Value() != selected.Text {
		t.Fatalf("composer = %q, want selected prompt %q", m.textarea.Value(), selected.Text)
	}
	childTranscript := child.Transcript()
	if len(childTranscript) != 1 || childTranscript[0] == nil || childTranscript[0].Role != schema.System {
		t.Fatalf("child transcript = %#v, want only system prompt", childTranscript)
	}
	if strings.TrimSpace(childTranscript[0].Content) != "system" {
		t.Fatalf("child system prompt = %q, want system", childTranscript[0].Content)
	}
	groups, err := threadStore.LoadTurnGroups(context.Background(), child.ID())
	if err != nil {
		t.Fatalf("LoadTurnGroups child: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("child turn groups = %#v, want empty", groups)
	}
	meta, err := threadStore.LoadThreadMeta(context.Background(), child.ID())
	if err != nil {
		t.Fatalf("LoadThreadMeta child: %v", err)
	}
	if meta.ParentID != source.ID() || meta.ForkBoundaryTurnID != "" {
		t.Fatalf("child provenance = %#v, want source parent and empty boundary", meta)
	}
	if !reflect.DeepEqual(sourceTranscript, source.Transcript()) {
		t.Fatal("single-turn backtrack changed source transcript")
	}
	sourceStateAfter, err := threadStore.LoadThread(context.Background(), source.ID())
	if err != nil {
		t.Fatalf("LoadThread source after fork: %v", err)
	}
	if !reflect.DeepEqual(sourceStateAfter, sourceState) {
		t.Fatalf("single-turn backtrack changed source state:\nbefore=%#v\nafter=%#v", sourceState, sourceStateAfter)
	}
}

func TestBacktrackForkFailureKeepsSourceAndPrompt(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	repository := &failingChildOpenRepository{ThreadRepository: threadStore, fork: threadStore}
	source, err := chat.NewSession(&transcriptSeedModel{responses: []string{"answer 1", "answer 2", "answer 3"}}, "system", chat.SessionOptions{Store: repository})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for i := 1; i <= 3; i++ {
		if err := source.Ask(ctx, "prompt "+itoa(i), nil); err != nil {
			t.Fatalf("seed Ask %d: %v", i, err)
		}
	}
	m := newModel(Deps{Ctx: ctx, Session: source, Store: repository})
	m.width = 80
	m.height = 24
	m.layout()
	enterBacktrackSelection(t, m)
	selected, ok := selectedBacktrackPrompt(m.backtrackState)
	if !ok {
		t.Fatal("backtrack prompt was not selected")
	}
	sourceTranscript := source.Transcript()

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if cmd != nil {
		t.Fatalf("failed backtrack returned command: %v", cmd)
	}
	if m.activeSession() != source {
		t.Fatal("fork failure changed the active source session")
	}
	if m.textarea.Value() != selected.Text || m.backtrackState.mode != backtrackInactive {
		t.Fatalf("failed fork state/composer = %#v/%q", m.backtrackState, m.textarea.Value())
	}
	if !reflect.DeepEqual(sourceTranscript, source.Transcript()) {
		t.Fatal("failed fork changed source transcript")
	}
	if !hasLineContaining(m.lines, lineError, "backtrack: load thread transcript: child open failed") {
		t.Fatalf("fork failure missing: %#v", m.lines)
	}
}

func TestBacktrackBeforeFirstForkFailureKeepsSourceAndPrompt(t *testing.T) {
	ctx := context.Background()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	repository := &failingChildOpenRepository{ThreadRepository: threadStore, fork: threadStore}
	source, err := chat.NewSession(&transcriptSeedModel{responses: []string{"answer"}}, "system", chat.SessionOptions{Store: repository})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if err := source.Ask(ctx, "first prompt", nil); err != nil {
		t.Fatalf("seed Ask: %v", err)
	}
	sourceTranscript := source.Transcript()
	m := newModel(Deps{Ctx: ctx, Session: source, Store: repository})
	m.width = 80
	m.height = 24
	m.layout()
	enterBacktrackSelection(t, m)
	selected, ok := selectedBacktrackPrompt(m.backtrackState)
	if !ok || !selected.BeforeFirst {
		t.Fatalf("selected prompt = %#v, want before-first prompt", selected)
	}

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if cmd != nil {
		t.Fatalf("failed before-first backtrack returned command: %v", cmd)
	}
	if m.activeSession() != source {
		t.Fatal("before-first fork failure changed the active source session")
	}
	if m.textarea.Value() != selected.Text || m.backtrackState.mode != backtrackInactive {
		t.Fatalf("failed before-first state/composer = %#v/%q", m.backtrackState, m.textarea.Value())
	}
	if !reflect.DeepEqual(sourceTranscript, source.Transcript()) {
		t.Fatal("before-first fork failure changed source transcript")
	}
	if !hasLineContaining(m.lines, lineError, "backtrack: load thread transcript: child open failed") {
		t.Fatalf("before-first fork failure missing: %#v", m.lines)
	}
}

func TestBacktrackRejectsBusyApprovalSideAndNoHistory(t *testing.T) {
	t.Run("busy Esc still interrupts", func(t *testing.T) {
		m := newTestModel(t)
		m.mode = modeBusy
		cancelled := false
		m.turnCancel = func() { cancelled = true }
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(*model)
		if !cancelled || m.backtrackState.mode != backtrackInactive {
			t.Fatalf("busy Esc cancelled=%v state=%#v", cancelled, m.backtrackState)
		}
	})

	t.Run("compacting Esc still interrupts", func(t *testing.T) {
		m := newTestModel(t)
		m.mode = modeCompacting
		cancelled := false
		m.compactCancel = func() { cancelled = true }
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(*model)
		if !cancelled || m.backtrackState.mode != backtrackInactive {
			t.Fatalf("compacting Esc cancelled=%v state=%#v", cancelled, m.backtrackState)
		}
	})

	t.Run("approval owns Esc", func(t *testing.T) {
		m := newTestModel(t)
		m.pendingApproval = &approvalRequestMsg{ID: "approval"}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(*model)
		if m.backtrackState.mode != backtrackInactive || m.pendingApproval != nil {
			t.Fatalf("approval Esc state=%#v pending=%v", m.backtrackState, m.pendingApproval != nil)
		}
	})

	t.Run("side question blocks arming", func(t *testing.T) {
		m := newTestModel(t)
		m.sideQuestions = 1
		m.queue = []string{"must stay"}
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = next.(*model)
		if m.backtrackState.mode != backtrackInactive || len(m.queue) != 1 || m.sideQuestions != 1 {
			t.Fatalf("side Esc state=%#v queue=%v side=%d", m.backtrackState, m.queue, m.sideQuestions)
		}
		if !hasLineContaining(m.lines, lineError, "wait for the side question") {
			t.Fatalf("side rejection missing: %#v", m.lines)
		}
	})

}

func TestBacktrackEscDismissesSlashMenuWithoutArming(t *testing.T) {
	m := newTestModel(t)
	m.textarea.SetValue("/s")
	m.syncSlashMenu()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*model)
	if m.slashMenuOpen() || m.backtrackState.mode != backtrackInactive {
		t.Fatalf("slash dismiss state: menu=%v backtrack=%#v", m.slashMenuOpen(), m.backtrackState)
	}
}

func enterBacktrackSelection(t *testing.T, m *model) {
	t.Helper()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	next, _ = next.(*model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	m2 := next.(*model)
	if m2.backtrackState.mode != backtrackSelecting {
		t.Fatalf("backtrack state=%#v, want selecting", m2.backtrackState)
	}
}

func newBacktrackModel(t *testing.T, turns int) (*model, *chat.Session, *store.ThreadStore) {
	t.Helper()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	responses := make([]string, turns)
	for i := range responses {
		responses[i] = "answer " + itoa(i+1)
	}
	session, err := chat.NewSession(&transcriptSeedModel{responses: responses}, "system", chat.SessionOptions{Store: threadStore})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	for i := 1; i <= turns; i++ {
		if err := session.Ask(context.Background(), "prompt "+itoa(i), nil); err != nil {
			t.Fatalf("seed Ask %d: %v", i, err)
		}
	}
	m := newModel(Deps{Ctx: context.Background(), Session: session, Store: threadStore, Status: StatusInfo{Model: "test-model"}})
	m.width = 80
	m.height = 24
	m.layout()
	return m, session, threadStore
}
