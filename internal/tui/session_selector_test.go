package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
)

func TestResumeByExactSessionNameUsesCanonicalID(t *testing.T) {
	m, threadStore, _ := newSessionPickerTestModel(t)
	target := newSessionPickerTestSession(t, threadStore, "Release planning")
	var gotID string
	m.deps.OpenSession = func(_ context.Context, id string, _ bool) (SessionOpenResult, error) {
		gotID = id
		return SessionOpenResult{Session: target}, nil
	}

	next, _ := m.cmdResume("Release planning")
	m = next.(*model)
	if gotID != target.ID() || m.activeSession() != target || m.sessionPickerOpen() {
		t.Fatalf("exact name resume = id=%q active=%p picker=%v", gotID, m.activeSession(), m.sessionPickerOpen())
	}
}

func TestResumeSelectorKeepsIDPrecedenceAndRejectsAmbiguousOrArchivedNames(t *testing.T) {
	ctx := context.Background()
	m, threadStore, _ := newSessionPickerTestModel(t)
	idWinner, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{
		Store: threadStore,
		ID:    "selector",
		Title: "ID takes precedence",
	})
	if err != nil {
		t.Fatalf("create ID winner: %v", err)
	}
	newSessionPickerTestSession(t, threadStore, "selector")
	duplicateOne := newSessionPickerTestSession(t, threadStore, "duplicate name")
	duplicateTwo := newSessionPickerTestSession(t, threadStore, "duplicate name")
	archived := newSessionPickerTestSession(t, threadStore, "archived name")
	archivedState, err := threadStore.LoadThread(ctx, archived.ID())
	if err != nil {
		t.Fatalf("load archived candidate: %v", err)
	}
	if _, err := threadStore.ArchiveThread(ctx, archived.ID(), archivedState.Revision); err != nil {
		t.Fatalf("archive candidate: %v", err)
	}

	if got, err := m.resolveResumeSelector("selector"); err != nil || got != idWinner.ID() {
		t.Fatalf("ID precedence = %q, %v; want %q", got, err, idWinner.ID())
	}
	if _, err := m.resolveResumeSelector("duplicate name"); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), duplicateOne.ID()) ||
		!strings.Contains(err.Error(), duplicateTwo.ID()) {
		t.Fatalf("ambiguous name error = %v", err)
	}
	if _, err := m.resolveResumeSelector("archived name"); err == nil || !strings.Contains(err.Error(), "no active session") {
		t.Fatalf("archived name error = %v", err)
	}
}
