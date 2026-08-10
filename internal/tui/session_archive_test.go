package tui

import (
	"context"
	"strings"
	"testing"

	"eino-local-assistant/internal/store"
)

func TestSessionArchiveLifecycleUsesNamesAndArchivedList(t *testing.T) {
	ctx := context.Background()
	m, threadStore, active := newSessionPickerTestModel(t)
	completed := newSessionPickerTestSession(t, threadStore, "Completed investigation")

	next, _ := m.submit("/archive Completed investigation")
	m = next.(*model)
	state, err := threadStore.LoadThread(ctx, completed.ID())
	if err != nil {
		t.Fatalf("LoadThread after archive: %v", err)
	}
	if state.Meta.ArchivedAt == nil || !hasLineContaining(m.lines, lineSystem, "archived session "+completed.ID()) {
		t.Fatalf("archive did not persist or confirm: state=%#v lines=%#v", state.Meta, m.lines)
	}
	activeList, err := threadStore.ListThreads(ctx)
	if err != nil {
		t.Fatalf("ListThreads after archive: %v", err)
	}
	for _, meta := range activeList {
		if meta.ID == completed.ID() {
			t.Fatalf("archived session remained active: %#v", activeList)
		}
	}

	next, _ = m.submit("/sessions --archived")
	m = next.(*model)
	if !hasLineContaining(m.lines, lineSystem, "Archived sessions (most recent first)") ||
		!hasLineContaining(m.lines, lineSystem, completed.ID()) ||
		!hasLineContaining(m.lines, lineSystem, "/unarchive <id-or-name>") {
		t.Fatalf("archived session list missing lifecycle details: %#v", m.lines)
	}

	next, _ = m.submit("/unarchive Completed investigation")
	m = next.(*model)
	state, err = threadStore.LoadThread(ctx, completed.ID())
	if err != nil {
		t.Fatalf("LoadThread after unarchive: %v", err)
	}
	if state.Meta.ArchivedAt != nil || !hasLineContaining(m.lines, lineSystem, "unarchived session "+completed.ID()) {
		t.Fatalf("unarchive did not persist or confirm: state=%#v lines=%#v", state.Meta, m.lines)
	}
	if m.activeSession() != active {
		t.Fatal("archive lifecycle changed the active TUI session")
	}
}

func TestSessionArchiveRejectsActiveAndLiveTargets(t *testing.T) {
	ctx := context.Background()
	m, threadStore, active := newSessionPickerTestModel(t)
	m.textarea.SetValue("draft must survive archive errors")
	next, _ := m.submit("/archive Active session")
	m = next.(*model)
	if !hasLineContaining(m.lines, lineError, "cannot archive the active session") || m.activeSession() != active || m.textarea.Value() != "draft must survive archive errors" {
		t.Fatalf("active archive rejection changed state: lines=%#v active=%p draft=%q", m.lines, m.activeSession(), m.textarea.Value())
	}

	live := newSessionPickerTestSession(t, threadStore, "Live elsewhere")
	state, err := threadStore.LoadThread(ctx, live.ID())
	if err != nil {
		t.Fatalf("LoadThread live: %v", err)
	}
	if _, err := threadStore.StartTurn(ctx, live.ID(), state.Revision, store.TurnStart{TurnID: "live-turn", Input: "work"}); err != nil {
		t.Fatalf("StartTurn live: %v", err)
	}
	next, _ = m.submit("/archive " + live.ID())
	m = next.(*model)
	if !hasLineContaining(m.lines, lineError, "cannot change archive state while a turn is active") {
		t.Fatalf("live archive safety error missing: %#v", m.lines)
	}
	state, err = threadStore.LoadThread(ctx, live.ID())
	if err != nil {
		t.Fatalf("LoadThread live after reject: %v", err)
	}
	if state.Meta.ArchivedAt != nil || !strings.Contains(m.textarea.Value(), "draft must survive") {
		t.Fatalf("live archive rejection changed durable or composer state: meta=%#v draft=%q", state.Meta, m.textarea.Value())
	}
}
