package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

type sideQuestionContextKey struct{}

func TestSideQuestionUsageAndUnavailable(t *testing.T) {
	m := newTestModel(t)

	next, cmd := m.submit("/btw")
	mm := next.(*model)
	if cmd != nil {
		t.Fatal("empty side question should not start a command")
	}
	if !hasLineContaining(mm.lines, lineError, "usage: /btw <question>") {
		t.Fatalf("usage missing: %#v", mm.lines)
	}

	next, cmd = mm.submit("/side explain this")
	mm = next.(*model)
	if cmd != nil {
		t.Fatal("unconfigured side question should not start a command")
	}
	if !hasSideLineContaining(mm, "[side] side unavailable") {
		t.Fatalf("unavailable result missing: %#v", mm.sideLines)
	}
}

func TestSideQuestionCallbackUsesCurrentSession(t *testing.T) {
	m := newTestModel(t)
	first := m.deps.Session
	second := mustSession(t, &staticModel{}, "second")
	root := context.WithValue(context.Background(), sideQuestionContextKey{}, "ctx")
	m.deps.Ctx = root
	m.mode = modeBusy
	m.turnID = 17
	m.queue = []string{"queued"}
	cancelCalls := 0
	m.turnCancel = func() { cancelCalls++ }

	var gotCtx context.Context
	var gotSession *chat.Session
	var gotQuestion string
	m.deps.SideQuestion = func(ctx context.Context, session *chat.Session, question string) (string, error) {
		gotCtx = ctx
		gotSession = session
		gotQuestion = question
		return "current answer", nil
	}

	mainLineCount := len(m.lines)
	firstTranscriptCount := len(first.Transcript())
	secondTranscriptCount := len(second.Transcript())
	next, cmd := m.submit("/side current session?")
	mm := next.(*model)
	if cmd == nil {
		t.Fatal("configured side question should return a command")
	}
	if mm.mode != modeBusy || mm.turnID != 17 || !reflect.DeepEqual(mm.queue, []string{"queued"}) {
		t.Fatalf("side question changed turn state: mode=%s turnID=%d queue=%v", modeName(mm.mode), mm.turnID, mm.queue)
	}
	if mm.turnCancel == nil {
		t.Fatal("side question cleared turn cancellation")
	}

	mm.replaceSession(second)
	raw := cmd()
	msg, ok := raw.(sideQuestionDoneMsg)
	if !ok {
		t.Fatalf("command returned %T, want sideQuestionDoneMsg", raw)
	}
	next, _ = mm.Update(msg)
	mm = next.(*model)
	if gotCtx != root || gotSession != second || gotQuestion != "current session?" {
		t.Fatalf("callback args: ctx=%v session=%p question=%q", gotCtx, gotSession, gotQuestion)
	}
	if mm.mode != modeBusy || mm.turnID != 17 || !reflect.DeepEqual(mm.queue, []string{"queued"}) {
		t.Fatalf("side result changed turn state: mode=%s turnID=%d queue=%v", modeName(mm.mode), mm.turnID, mm.queue)
	}
	if len(mm.lines) != mainLineCount {
		t.Fatalf("side output entered main transcript: before=%d after=%d", mainLineCount, len(mm.lines))
	}
	if len(first.Transcript()) != firstTranscriptCount || len(second.Transcript()) != secondTranscriptCount {
		t.Fatalf("side output changed session transcript")
	}
	if mm.deps.Session.ID() != second.ID() {
		t.Fatalf("active session changed: got %q want %q", mm.deps.Session.ID(), second.ID())
	}
	if !hasSideLineContaining(mm, "[side] answer: current answer") {
		t.Fatalf("side answer missing: %#v", mm.sideLines)
	}

	mm.turnCancel()
	if cancelCalls != 1 {
		t.Fatalf("turn cancellation callback was replaced or called early: %d", cancelCalls)
	}
}

func TestSideQuestionErrorAndStaleResult(t *testing.T) {
	m := newTestModel(t)
	m.deps.SideQuestion = func(context.Context, *chat.Session, string) (string, error) {
		return "", errors.New("temporary failure")
	}
	next, cmd := m.submit("/btw fail")
	mm := next.(*model)
	raw := cmd()
	msg, ok := raw.(sideQuestionDoneMsg)
	if !ok {
		t.Fatalf("command returned %T, want sideQuestionDoneMsg", raw)
	}
	next, _ = mm.Update(msg)
	mm = next.(*model)
	if mm.sideQuestions != 0 {
		t.Fatalf("error result left pending side questions: %d", mm.sideQuestions)
	}
	if !hasSideLineContaining(mm, "[btw] side error: temporary failure") {
		t.Fatalf("side error missing: %#v", mm.sideLines)
	}

	oldID := mm.deps.Session.ID()
	second := mustSession(t, &staticModel{}, "second")
	mm.deps.SideQuestion = func(context.Context, *chat.Session, string) (string, error) {
		return "old answer", nil
	}
	next, cmd = mm.submit("/btw old")
	mm = next.(*model)
	raw = cmd()
	msg, ok = raw.(sideQuestionDoneMsg)
	if !ok {
		t.Fatalf("second command returned %T, want sideQuestionDoneMsg", raw)
	}
	mm.replaceSession(second)
	before := len(mm.sideLines)
	next, _ = mm.Update(msg)
	mm = next.(*model)
	if len(mm.sideLines) != before || hasSideLineContaining(mm, "old answer") {
		t.Fatalf("stale side result was displayed: %#v", mm.sideLines)
	}
	if oldID == second.ID() || mm.deps.Session.ID() != second.ID() {
		t.Fatalf("session switch was not preserved: old=%q current=%q", oldID, mm.deps.Session.ID())
	}
	if mm.sideQuestions != 0 {
		t.Fatalf("stale result left pending side questions: %d", mm.sideQuestions)
	}
}

func TestSideQuestionStaleResultDoesNotConsumeCurrentPendingRequest(t *testing.T) {
	m := newTestModel(t)
	started := make(chan struct{})
	release := make(chan struct{})
	m.deps.SideQuestion = func(_ context.Context, _ *chat.Session, question string) (string, error) {
		if question == "old" {
			close(started)
			<-release
		}
		return question + " answer", nil
	}

	_, oldCmd := m.submit("/btw old")
	oldResult := make(chan tea.Msg, 1)
	go func() { oldResult <- oldCmd() }()
	<-started

	second := mustSession(t, &staticModel{}, "second")
	m.replaceSession(second)
	_, newCmd := m.submit("/btw new")
	newMsg := newCmd().(sideQuestionDoneMsg)
	if m.sideQuestions != 2 {
		t.Fatalf("pending side questions after session switch: %d", m.sideQuestions)
	}

	close(release)
	oldMsg := (<-oldResult).(sideQuestionDoneMsg)
	next, _ := m.Update(oldMsg)
	m = next.(*model)
	if m.sideQuestions != 1 {
		t.Fatalf("stale result consumed current request: %d", m.sideQuestions)
	}
	if hasSideLineContaining(m, "old answer") {
		t.Fatalf("stale answer was displayed: %#v", m.sideLines)
	}

	next, _ = m.Update(newMsg)
	m = next.(*model)
	if m.sideQuestions != 0 {
		t.Fatalf("current result left pending side questions: %d", m.sideQuestions)
	}
	if !hasSideLineContaining(m, "[btw] answer: new answer") {
		t.Fatalf("current answer missing: %#v", m.sideLines)
	}
}

func TestSessionSwitchResetsPendingSideQuestions(t *testing.T) {
	tests := []struct {
		name          string
		switchSession func(*testing.T, *model) *model
	}{
		{
			name: "clear",
			switchSession: func(_ *testing.T, m *model) *model {
				next, _ := m.cmdClear()
				return next.(*model)
			},
		},
		{
			name: "new",
			switchSession: func(_ *testing.T, m *model) *model {
				next, _ := m.cmdNew("new session")
				return next.(*model)
			},
		},
		{
			name: "resume",
			switchSession: func(t *testing.T, m *model) *model {
				target, err := chat.NewSession(&staticModel{}, "target", chat.SessionOptions{
					Store: m.activeSession().Store(),
				})
				if err != nil {
					t.Fatalf("create resume target: %v", err)
				}
				next, _ := m.cmdResume(target.ID())
				return next.(*model)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t)
			started := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseOld := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseOld()
			m.deps.SideQuestion = func(_ context.Context, _ *chat.Session, question string) (string, error) {
				if question == "old" {
					close(started)
					<-release
				}
				return question + " answer", nil
			}

			_, oldCmd := m.submit("/btw old")
			oldResult := make(chan tea.Msg, 1)
			go func() { oldResult <- oldCmd() }()
			<-started

			m = tt.switchSession(t, m)
			if m.sideQuestions != 0 || len(m.sideQuestionPending) != 0 {
				t.Fatalf("pending side state after /%s: count=%d pending=%v", tt.name, m.sideQuestions, m.sideQuestionPending)
			}

			_, newCmd := m.submit("/btw new")
			rawNewMsg := newCmd()
			newMsg, ok := rawNewMsg.(sideQuestionDoneMsg)
			if !ok {
				t.Fatalf("new command returned %T, want sideQuestionDoneMsg", rawNewMsg)
			}
			if m.sideQuestions != 1 {
				t.Fatalf("new side question count=%d, want 1", m.sideQuestions)
			}

			releaseOld()
			oldMsg := (<-oldResult).(sideQuestionDoneMsg)
			next, _ := m.Update(oldMsg)
			m = next.(*model)
			if m.sideQuestions != 1 {
				t.Fatalf("stale side result changed current count to %d", m.sideQuestions)
			}
			if hasSideLineContaining(m, "old answer") {
				t.Fatalf("stale answer was displayed: %#v", m.sideLines)
			}

			next, _ = m.Update(newMsg)
			m = next.(*model)
			if m.sideQuestions != 0 {
				t.Fatalf("current side result left pending count=%d", m.sideQuestions)
			}
			if !hasSideLineContaining(m, "[btw] answer: new answer") {
				t.Fatalf("current answer missing: %#v", m.sideLines)
			}
		})
	}
}

func TestSideQuestionDropsResultAcrossNewAndResume(t *testing.T) {
	m := newTestModel(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	m.deps.SideQuestion = func(context.Context, *chat.Session, string) (string, error) {
		startedOnce.Do(func() { close(started) })
		<-release
		return "old answer", nil
	}

	_, cmd := m.submit("/btw while switching")
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	<-started
	oldID := m.activeSession().ID()
	if next, _ := m.submit("/new"); next.(*model).activeSession().ID() == oldID {
		t.Fatal("/new did not switch the active session")
	} else {
		m = next.(*model)
	}
	close(release)
	msg := <-result
	before := len(m.sideLines)
	next, _ := m.Update(msg)
	m = next.(*model)
	if len(m.sideLines) != before || hasSideLineContaining(m, "old answer") {
		t.Fatalf("side result crossed /new boundary: %#v", m.sideLines)
	}

	// Resume the original ID twice. ID-only filtering would accept the result
	// from the first instance after the second resume; generation filtering must
	// still discard it.
	next, _ = m.submit("/resume " + oldID)
	m = next.(*model)
	if m.activeSession().ID() != oldID {
		t.Fatalf("resume id = %q, want %q", m.activeSession().ID(), oldID)
	}
	_, cmd = m.submit("/btw same id")
	msg = cmd().(sideQuestionDoneMsg)
	next, _ = m.submit("/resume " + oldID)
	m = next.(*model)
	before = len(m.sideLines)
	next, _ = m.Update(msg)
	m = next.(*model)
	if len(m.sideLines) != before || hasSideLineContaining(m, "old answer") {
		t.Fatalf("side result crossed same-ID /resume boundary: %#v", m.sideLines)
	}
}

func TestSideQuestionEmptyAnswerIsVisibleError(t *testing.T) {
	m := newTestModel(t)
	m.deps.SideQuestion = func(context.Context, *chat.Session, string) (string, error) {
		return " \t\n", nil
	}

	_, cmd := m.submit("/btw no answer")
	next, _ := m.Update(cmd().(sideQuestionDoneMsg))
	m = next.(*model)
	if !hasSideLineContaining(m, "[btw] side error: empty answer") {
		t.Fatalf("empty answer error missing: %#v", m.sideLines)
	}
	if hasSideLineContaining(m, "[btw] answer:") {
		t.Fatalf("empty answer rendered as success: %#v", m.sideLines)
	}
}

func TestSideQuestionRequestsRunConcurrently(t *testing.T) {
	m := newTestModel(t)
	started := make(chan string, 2)
	release := make(chan struct{})
	m.deps.SideQuestion = func(_ context.Context, _ *chat.Session, question string) (string, error) {
		started <- question
		<-release
		return "answer " + question, nil
	}

	_, firstCmd := m.submit("/btw first")
	_, secondCmd := m.submit("/side second")
	results := make(chan tea.Msg, 2)
	go func() { results <- firstCmd() }()
	go func() { results <- secondCmd() }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("side questions did not start concurrently")
		}
	}
	close(release)
	for range 2 {
		msg := <-results
		next, _ := m.Update(msg)
		m = next.(*model)
	}
	if m.sideQuestions != 0 {
		t.Fatalf("concurrent side questions left pending: %d", m.sideQuestions)
	}
	if !hasSideLineContaining(m, "[btw] answer: answer first") || !hasSideLineContaining(m, "[side] answer: answer second") {
		t.Fatalf("concurrent side answers missing: %#v", m.sideLines)
	}
}

func hasSideLineContaining(m *model, substr string) bool {
	for _, line := range m.sideLines {
		if line.kind == lineSide && strings.Contains(line.text, substr) {
			return true
		}
	}
	return false
}
