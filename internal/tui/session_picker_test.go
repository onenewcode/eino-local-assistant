package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"

	tea "github.com/charmbracelet/bubbletea"
)

func TestResumeWithoutIDOpensSearchableActiveSessionPicker(t *testing.T) {
	m, threadStore, active := newSessionPickerTestModel(t)
	first := newSessionPickerTestSession(t, threadStore, "Incident response")
	second := newSessionPickerTestSession(t, threadStore, "Release checklist")
	archived := newSessionPickerTestSession(t, threadStore, "Archived investigation")
	state, err := threadStore.LoadThread(context.Background(), archived.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if _, err := threadStore.ArchiveThread(context.Background(), archived.ID(), state.Revision); err != nil {
		t.Fatalf("ArchiveThread: %v", err)
	}
	m.textarea.SetValue("draft that must survive the picker")
	viewportHeight := m.viewport.Height

	next, _ := m.cmdResume("")
	m = next.(*model)
	if !m.sessionPickerOpen() {
		t.Fatal("/resume without an ID did not open the session picker")
	}
	if m.textarea.Value() != "draft that must survive the picker" {
		t.Fatalf("opening picker changed composer draft: %q", m.textarea.Value())
	}
	if m.activeSession() != active {
		t.Fatal("opening picker changed the active session")
	}
	if m.viewport.Height >= viewportHeight {
		t.Fatalf("picker did not reserve viewport height: before=%d after=%d", viewportHeight, m.viewport.Height)
	}
	rows := m.sessionPickerRows()
	if len(rows) != 2 {
		t.Fatalf("picker rows = %#v, want two active non-current sessions", rows)
	}
	for _, row := range rows {
		if row.ID == active.ID() || row.ID == archived.ID() {
			t.Fatalf("non-selectable session appeared in picker: %#v", row)
		}
	}
	rowIDs := map[string]bool{}
	for _, row := range rows {
		rowIDs[row.ID] = true
	}
	if !rowIDs[first.ID()] || !rowIDs[second.ID()] {
		t.Fatalf("picker omitted an active candidate: %#v", rows)
	}
	view := m.View()
	for _, want := range []string{"Resume Session", "Incident response", "Release checklist", "msgs=", "updated="} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("release")})
	m = next.(*model)
	rows = m.sessionPickerRows()
	if len(rows) != 1 || rows[0].ID != second.ID() {
		t.Fatalf("search rows = %#v, want release session %q", rows, second.ID())
	}
	for range "release" {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(*model)
	}
	if rows = m.sessionPickerRows(); len(rows) != 2 {
		t.Fatalf("clearing search did not restore rows: %#v", rows)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(first.ID())})
	m = next.(*model)
	if rows = m.sessionPickerRows(); len(rows) != 1 || rows[0].ID != first.ID() {
		t.Fatalf("ID search rows = %#v, want incident session %q", rows, first.ID())
	}
	for range first.ID() {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(*model)
	}

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*model)
	if m.sessionPickerOpen() {
		t.Fatal("Esc did not close the session picker")
	}
	if m.activeSession() != active || m.textarea.Value() != "draft that must survive the picker" {
		t.Fatalf("Esc changed session or draft: active=%p draft=%q", m.activeSession(), m.textarea.Value())
	}
	if m.viewport.Height != viewportHeight {
		t.Fatalf("closing picker did not restore viewport height: got=%d want=%d", m.viewport.Height, viewportHeight)
	}
}

func TestSessionPickerNavigationAndConfirmationUseResumePath(t *testing.T) {
	m, threadStore, _ := newSessionPickerTestModel(t)
	first := newSessionPickerTestSession(t, threadStore, "First target")
	second := newSessionPickerTestSession(t, threadStore, "Second target")
	m.sessionPicker = &sessionPickerState{entries: []store.ThreadMeta{
		{ID: first.ID(), Title: first.Title()},
		{ID: second.ID(), Title: second.Title()},
	}}
	m.textarea.SetValue("draft that must survive confirmation")
	wantStatus := StatusInfo{Model: "openai/second-target", ReasoningEffort: "high"}
	wantOpts := chat.SessionOptions{Store: threadStore, ModelName: "second-target", ReasoningEffort: "high"}
	var gotID string
	var gotRecover bool
	m.deps.OpenSession = func(_ context.Context, id string, recoverInterrupted bool) (SessionOpenResult, error) {
		gotID = id
		gotRecover = recoverInterrupted
		return SessionOpenResult{Session: second, Status: wantStatus, SessionOpts: wantOpts}, nil
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(*model)
	if m.sessionPicker.selected != 1 {
		t.Fatalf("j selection = %d, want second row", m.sessionPicker.selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(*model)
	if m.sessionPicker.selected != 0 {
		t.Fatalf("down wrap selection = %d, want first row", m.sessionPicker.selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(*model)
	if m.sessionPicker.selected != 1 {
		t.Fatalf("up wrap selection = %d, want second row", m.sessionPicker.selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = next.(*model)
	if m.sessionPicker.selected != 0 {
		t.Fatalf("k selection = %d, want first row", m.sessionPicker.selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if gotID != second.ID() || gotRecover {
		t.Fatalf("resume callback arguments = id=%q recover=%v", gotID, gotRecover)
	}
	if m.activeSession() != second || m.sessionPickerOpen() {
		t.Fatalf("successful picker confirmation did not replace and close: active=%p picker=%v", m.activeSession(), m.sessionPickerOpen())
	}
	if m.deps.Status.Model != wantStatus.Model || m.deps.Status.ReasoningEffort != wantStatus.ReasoningEffort ||
		m.deps.SessionOpts.ModelName != wantOpts.ModelName || m.deps.SessionOpts.ReasoningEffort != wantOpts.ReasoningEffort {
		t.Fatalf("picker did not install runtime snapshot: status=%#v opts=%#v", m.deps.Status, m.deps.SessionOpts)
	}
	if m.textarea.Value() != "draft that must survive confirmation" {
		t.Fatalf("successful picker confirmation changed composer draft: %q", m.textarea.Value())
	}
}

func TestSessionPickerFailureAndCancelKeepCurrentSession(t *testing.T) {
	m, threadStore, active := newSessionPickerTestModel(t)
	newSessionPickerTestSession(t, threadStore, "Candidate")
	draft := "draft that must survive failure"
	m.textarea.SetValue(draft)

	next, _ := m.cmdResume("")
	m = next.(*model)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*model)
	if m.activeSession() != active || m.textarea.Value() != draft {
		t.Fatalf("cancel changed session or draft: active=%p draft=%q", m.activeSession(), m.textarea.Value())
	}

	next, _ = m.cmdResume("")
	m = next.(*model)
	m.deps.OpenSession = func(context.Context, string, bool) (SessionOpenResult, error) {
		return SessionOpenResult{}, errors.New("target is no longer available")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*model)
	if !m.sessionPickerOpen() {
		t.Fatal("failed session open closed the picker")
	}
	if m.activeSession() != active || m.textarea.Value() != draft {
		t.Fatalf("failed session open changed session or draft: active=%p draft=%q", m.activeSession(), m.textarea.Value())
	}
	if !hasLineContaining(m.lines, lineError, "target is no longer available") {
		t.Fatalf("failed session open feedback missing: %#v", m.lines)
	}
}

func TestResumeByIDDoesNotOpenSessionPicker(t *testing.T) {
	m, threadStore, _ := newSessionPickerTestModel(t)
	target := newSessionPickerTestSession(t, threadStore, "Direct target")
	m.deps.OpenSession = func(context.Context, string, bool) (SessionOpenResult, error) {
		return SessionOpenResult{Session: target}, nil
	}

	next, _ := m.cmdResume(target.ID())
	m = next.(*model)
	if m.sessionPickerOpen() || m.activeSession() != target {
		t.Fatalf("direct resume did not preserve direct behavior: picker=%v active=%p", m.sessionPickerOpen(), m.activeSession())
	}
}

func TestResumeLastSelectsNewestActiveSessionWithoutPicker(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		recover bool
	}{
		{name: "normal", input: "--last"},
		{name: "explicit recovery", input: "--last --recover", recover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, threadStore, active := newSessionPickerTestModel(t)
			target := newSessionPickerTestSession(t, threadStore, "Newest target")
			list := &sessionPickerListRepository{
				ThreadRepository: threadStore,
				entries: []store.ThreadMeta{
					{ID: target.ID(), Title: target.Title()},
					{ID: active.ID(), Title: active.Title()},
				},
			}
			m.deps.Store = list
			m.textarea.SetValue("draft that must survive --last")
			var gotID string
			var gotRecover bool
			m.deps.OpenSession = func(_ context.Context, id string, recoverInterrupted bool) (SessionOpenResult, error) {
				gotID = id
				gotRecover = recoverInterrupted
				return SessionOpenResult{Session: target}, nil
			}

			next, _ := m.cmdResume(tc.input)
			m = next.(*model)
			if list.calls != 1 || gotID != target.ID() || gotRecover != tc.recover {
				t.Fatalf("--last selection = calls=%d id=%q recover=%v", list.calls, gotID, gotRecover)
			}
			if m.activeSession() != target || m.sessionPickerOpen() {
				t.Fatalf("--last did not resume newest target without picker: active=%p picker=%v", m.activeSession(), m.sessionPickerOpen())
			}
			if m.textarea.Value() != "draft that must survive --last" {
				t.Fatalf("--last changed composer draft: %q", m.textarea.Value())
			}
		})
	}
}

func TestResumeLastDoesNotReopenCurrentSession(t *testing.T) {
	m, threadStore, active := newSessionPickerTestModel(t)
	target := newSessionPickerTestSession(t, threadStore, "Older target")
	list := &sessionPickerListRepository{
		ThreadRepository: threadStore,
		entries: []store.ThreadMeta{
			{ID: active.ID(), Title: active.Title()},
			{ID: target.ID(), Title: target.Title()},
		},
	}
	m.deps.Store = list
	m.textarea.SetValue("draft must remain on no-op")
	m.deps.OpenSession = func(context.Context, string, bool) (SessionOpenResult, error) {
		t.Fatal("--last reopened the already active session")
		return SessionOpenResult{}, nil
	}

	next, _ := m.cmdResume("--last")
	m = next.(*model)
	if list.calls != 1 || m.activeSession() != active || m.sessionPickerOpen() {
		t.Fatalf("current latest changed state: calls=%d active=%p picker=%v", list.calls, m.activeSession(), m.sessionPickerOpen())
	}
	if m.textarea.Value() != "draft must remain on no-op" || !hasLineContaining(m.lines, lineSystem, "latest active session is already open") {
		t.Fatalf("current latest did not preserve draft or report no-op: draft=%q lines=%#v", m.textarea.Value(), m.lines)
	}
}

func TestResumeLastParsingAndListFailureAreSafe(t *testing.T) {
	for _, tc := range []struct {
		input       string
		wantID      string
		wantLast    bool
		wantRecover bool
		wantOK      bool
	}{
		{input: "--last", wantLast: true, wantOK: true},
		{input: "--last --recover", wantLast: true, wantRecover: true, wantOK: true},
		{input: "target --recover", wantID: "target", wantRecover: true, wantOK: true},
		{input: "exact display name", wantID: "exact display name", wantOK: true},
		{input: "exact display name --recover", wantID: "exact display name", wantRecover: true, wantOK: true},
		{input: "--recover", wantOK: false},
		{input: "--recover --last", wantOK: false},
		{input: "--last unexpected", wantOK: false},
	} {
		id, last, recoverInterrupted, ok := parseResumeArgs(tc.input)
		if id != tc.wantID || last != tc.wantLast || recoverInterrupted != tc.wantRecover || ok != tc.wantOK {
			t.Fatalf("parseResumeArgs(%q) = id=%q last=%v recover=%v ok=%v", tc.input, id, last, recoverInterrupted, ok)
		}
	}

	m, threadStore, active := newSessionPickerTestModel(t)
	m.deps.Store = &sessionPickerListRepository{ThreadRepository: threadStore, err: errors.New("metadata unavailable")}
	next, _ := m.cmdResume("--last")
	m = next.(*model)
	if m.activeSession() != active || m.sessionPickerOpen() || !hasLineContaining(m.lines, lineError, "metadata unavailable") {
		t.Fatalf("--last list failure changed state or hid error: active=%p picker=%v lines=%#v", m.activeSession(), m.sessionPickerOpen(), m.lines)
	}
}

func TestSessionPickerRequiresAnotherActiveSession(t *testing.T) {
	m, _, active := newSessionPickerTestModel(t)
	next, _ := m.cmdResume("")
	m = next.(*model)
	if m.sessionPickerOpen() || m.activeSession() != active {
		t.Fatalf("empty picker changed session state: picker=%v active=%p", m.sessionPickerOpen(), m.activeSession())
	}
	if !hasLineContaining(m.lines, lineSystem, "no other active sessions to resume") {
		t.Fatalf("empty picker feedback missing: %#v", m.lines)
	}
}

func TestSessionPickerRespectsSessionMutationAdmission(t *testing.T) {
	cases := []struct {
		name          string
		mode          mode
		pending       bool
		sideQuestions int
	}{
		{name: "busy", mode: modeBusy},
		{name: "compacting", mode: modeCompacting},
		{name: "pending approval", mode: modeIdle, pending: true},
		{name: "side question", mode: modeIdle, sideQuestions: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, threadStore, active := newSessionPickerTestModel(t)
			newSessionPickerTestSession(t, threadStore, "Candidate")
			m.mode = tc.mode
			m.sideQuestions = tc.sideQuestions
			if tc.pending {
				m.pendingApproval = &approvalRequestMsg{}
			}
			m.textarea.SetValue("draft must remain")

			next, _ := m.cmdResume("")
			m = next.(*model)
			if m.sessionPickerOpen() || m.activeSession() != active || m.textarea.Value() != "draft must remain" {
				t.Fatalf("rejected picker changed state: picker=%v active=%p draft=%q", m.sessionPickerOpen(), m.activeSession(), m.textarea.Value())
			}
			if !hasLineContaining(m.lines, lineError, "busy:") {
				t.Fatalf("admission feedback missing: %#v", m.lines)
			}
		})
	}
}

func newSessionPickerTestModel(t *testing.T) (*model, *store.ThreadStore, *chat.Session) {
	t.Helper()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	active := newSessionPickerTestSession(t, threadStore, "Active session")
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: active,
		Store:   threadStore,
		Status:  StatusInfo{Model: "test-model"},
	})
	m.width = 80
	m.height = 24
	m.layout()
	return m, threadStore, active
}

func newSessionPickerTestSession(t *testing.T, threadStore *store.ThreadStore, title string) *chat.Session {
	t.Helper()
	session, err := chat.NewSession(&staticModel{}, "system", chat.SessionOptions{Store: threadStore, Title: title})
	if err != nil {
		t.Fatalf("NewSession(%q): %v", title, err)
	}
	return session
}

type sessionPickerListRepository struct {
	store.ThreadRepository
	entries []store.ThreadMeta
	err     error
	calls   int
}

func (r *sessionPickerListRepository) ListThreads(context.Context) ([]store.ThreadMeta, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return append([]store.ThreadMeta(nil), r.entries...), nil
}
