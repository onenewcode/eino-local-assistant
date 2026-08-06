package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

type steerSessionStub struct {
	turnID      string
	active      bool
	err         error
	gotExpected string
	gotInput    string
	steerCalls  int
}

type legacySteerSessionStub struct {
	err   error
	calls int
}

func (s *legacySteerSessionStub) Steer(_ context.Context, _, _ string) error {
	s.calls++
	return s.err
}

func (s *steerSessionStub) ActiveTurnID() (string, bool) {
	return s.turnID, s.active
}

func (s *steerSessionStub) Steer(_ context.Context, expectedTurnID, input string) error {
	s.steerCalls++
	s.gotExpected = expectedTurnID
	s.gotInput = input
	return s.err
}

func (s *steerSessionStub) SteerWithReceipt(ctx context.Context, expectedTurnID, input string) (chat.TurnSteerReceipt, error) {
	if err := s.Steer(ctx, expectedTurnID, input); err != nil {
		return chat.TurnSteerReceipt{}, err
	}
	return chat.TurnSteerReceipt{Sequence: uint64(s.steerCalls), Content: input}, nil
}

func TestSteerBusyAdmitsUsingDurableTurnIDWithoutChangingTurnState(t *testing.T) {
	m := newTestModel(t)
	stub := &steerSessionStub{turnID: "durable-turn-7", active: true}
	m.steerSession = stub
	m.mode = modeBusy
	m.turnID = 99
	m.queue = []string{"queued follow-up"}
	m.queuePaused = true
	cancelled := false
	m.turnCancel = func() { cancelled = true }
	approval := &approvalRequestMsg{}
	m.pendingApproval = approval
	m.textarea.SetValue("/steer  change   direction  ")
	m.syncComposerHeight()
	beforeLines := len(m.lines)

	next, cmd := m.queueWhileBusy("/steer  change   direction  ")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("steer should use quick admission: %#v", cmd)
	}
	if stub.steerCalls != 1 || stub.gotExpected != "durable-turn-7" {
		t.Fatalf("steer call = %d expected=%q", stub.steerCalls, stub.gotExpected)
	}
	if stub.gotInput != "change   direction" {
		t.Fatalf("steer input = %q, internal whitespace was not preserved", stub.gotInput)
	}
	if mm.mode != modeBusy || mm.turnID != 99 || cancelled || mm.pendingApproval == nil {
		t.Fatalf("steer changed active state: mode=%s turn=%d cancelled=%v approval=%v", modeName(mm.mode), mm.turnID, cancelled, mm.pendingApproval)
	}
	if mm.queuePaused != true || len(mm.queue) != 1 || mm.queue[0] != "queued follow-up" {
		t.Fatalf("steer changed queue state: queue=%v paused=%v", mm.queue, mm.queuePaused)
	}
	if mm.textarea.Value() != "" {
		t.Fatalf("successful steer did not clear composer draft: %q", mm.textarea.Value())
	}
	if mm.pendingApproval != approval {
		t.Fatalf("steer changed approval state: got=%p want=%p", mm.pendingApproval, approval)
	}
	if len(mm.lines) == beforeLines || !hasLineContaining(mm.lines, lineSystem, "steer admitted; awaiting next model call") {
		t.Fatalf("steer success feedback missing: %#v", mm.lines)
	}
	if hasLineContaining(mm.lines, lineSystem, "steer consumed") {
		t.Fatal("admission must not claim model-boundary consumption")
	}
	for _, line := range mm.lines[beforeLines:] {
		if line.kind == lineUser {
			t.Fatalf("steer must not append a user transcript line: %#v", mm.lines)
		}
	}
}

func TestSteerConsumedFeedbackIsDisplayOnlyAndSettlesLateAdmission(t *testing.T) {
	m := newTestModel(t)
	stub := &steerSessionStub{turnID: "durable-turn-7", active: true}
	m.steerSession = stub
	m.mode = modeBusy
	m.turnID = 7
	next, _ := m.queueWhileBusy("/steer redirect")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "steer admitted; awaiting next model call") {
		t.Fatalf("admission feedback missing: %#v", mm.lines)
	}
	next, _ = mm.Update(turnSteerConsumedMsg{turnID: 7, sequence: 1, content: "redirect"})
	mm = next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "steer consumed at model boundary (#1): redirect") {
		t.Fatalf("consumed feedback missing: %#v", mm.lines)
	}
	mm.turnSteerAdmitted = 2
	mm.finishTurn(nil)
	if !hasLineContaining(mm.lines, lineSystem, "steer committed: 1 consumed input(s)") {
		t.Fatalf("successful steer settlement missing: %#v", mm.lines)
	}
	if !hasLineContaining(mm.lines, lineSystem, "steer discarded: 1 admitted input(s) were not consumed") {
		t.Fatalf("late steer settlement missing: %#v", mm.lines)
	}
	for _, line := range mm.lines {
		if line.kind == lineUser && strings.Contains(line.text, "redirect") {
			t.Fatalf("steer feedback entered user transcript: %#v", mm.lines)
		}
	}
}

func TestSteerFailureSettlementReportsNotCommitted(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnSteerAdmitted = 2
	m.turnSteerConsumed = map[uint64]struct{}{1: {}}
	m.finishTurn(errors.New("provider failed"))
	if !hasLineContaining(m.lines, lineSystem, "steer discarded: 2 admitted input(s); turn not committed") {
		t.Fatalf("failed steer settlement missing: %#v", m.lines)
	}
	if hasLineContaining(m.lines, lineSystem, "steer committed:") {
		t.Fatalf("failed turn must not report committed steer input: %#v", m.lines)
	}
}

func TestSteerEmptyTextReportsUsage(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	stub := &steerSessionStub{turnID: "turn", active: true}
	m.steerSession = stub
	next, cmd := m.submit("/steer   ")
	mm := next.(*model)
	if cmd != nil || stub.steerCalls != 0 || !hasLineContaining(mm.lines, lineError, "usage: /steer <text>") {
		t.Fatalf("empty steer: cmd=%v calls=%d lines=%#v", cmd, stub.steerCalls, mm.lines)
	}
}

func TestSteerExplicitFailuresNeverFallBackToQueueOrTurn(t *testing.T) {
	tests := []struct {
		name string
		mode mode
		stub *steerSessionStub
		want string
	}{
		{name: "idle", mode: modeIdle, stub: &steerSessionStub{turnID: "turn", active: true}, want: "regular turn is not running"},
		{name: "compacting", mode: modeCompacting, stub: &steerSessionStub{turnID: "turn", active: true}, want: "regular turn is not running"},
		{name: "no active", mode: modeBusy, stub: &steerSessionStub{}, want: "no active steerable turn"},
		{name: "unsupported", mode: modeBusy, stub: &steerSessionStub{turnID: "turn", active: false}, want: "no active steerable turn"},
		{name: "mismatch", mode: modeBusy, stub: &steerSessionStub{turnID: "turn", active: true, err: errors.New("steer turn ID mismatch")}, want: "steer turn ID mismatch"},
		{name: "core error", mode: modeBusy, stub: &steerSessionStub{turnID: "turn", active: true, err: errors.New("arbitrary failure")}, want: "arbitrary failure"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.steerSession = tc.stub
			m.mode = tc.mode
			m.turnID = 12
			m.queue = []string{"keep me"}
			m.queuePaused = true
			approval := &approvalRequestMsg{}
			m.pendingApproval = approval
			draft := "/steer  correction  "
			m.textarea.SetValue(draft)
			m.syncComposerHeight()
			next, cmd := m.queueWhileBusy(strings.TrimSpace(draft))
			mm := next.(*model)
			if cmd != nil || mm.mode != tc.mode || mm.turnID != 12 || len(mm.queue) != 1 || mm.queue[0] != "keep me" || !mm.queuePaused {
				t.Fatalf("failure changed state: cmd=%v mode=%s turn=%d queue=%v paused=%v", cmd, modeName(mm.mode), mm.turnID, mm.queue, mm.queuePaused)
			}
			if mm.textarea.Value() != draft {
				t.Fatalf("failed steer changed composer draft: got=%q want=%q", mm.textarea.Value(), draft)
			}
			if mm.pendingApproval != approval {
				t.Fatalf("failed steer changed approval state: got=%p want=%p", mm.pendingApproval, approval)
			}
			if tc.stub.steerCalls > 0 && tc.name != "core error" && tc.name != "mismatch" {
				t.Fatalf("unexpected steer call count=%d", tc.stub.steerCalls)
			}
			if !hasLineContaining(mm.lines, lineError, tc.want) {
				t.Fatalf("missing %q in lines: %#v", tc.want, mm.lines)
			}
			for _, line := range mm.lines {
				if line.kind == lineUser && strings.Contains(line.text, "correction") {
					t.Fatalf("steer entered user transcript: %#v", mm.lines)
				}
			}
		})
	}
}

func TestSteerIsNeverQueueableOrQueueEditable(t *testing.T) {
	if got := classifyBusyInput("/steer text"); got != busyInputSteer {
		t.Fatalf("busy steer disposition=%v, want steer", got)
	}
	if isQueueableInput("/steer text") {
		t.Fatal("steer must not be queueable")
	}
	m := newTestModel(t)
	m.queue = []string{"old"}
	next, _ := m.submit("/queue edit 1 /steer text")
	mm := next.(*model)
	if mm.queue[0] != "old" || !hasLineContaining(mm.lines, lineError, "queue edit rejected") {
		t.Fatalf("queue edit accepted steer: queue=%v lines=%#v", mm.queue, mm.lines)
	}
}

func TestLegacySteerAdmissionDoesNotInventReceipt(t *testing.T) {
	m := newTestModel(t)
	legacy := &legacySteerSessionStub{}
	receipt, err := m.admitSteer(legacy, "durable-turn-7", "redirect")
	if err != nil {
		t.Fatalf("legacy admission: %v", err)
	}
	if receipt != (chat.TurnSteerReceipt{}) {
		t.Fatalf("legacy admission returned guessed receipt: %#v", receipt)
	}
	if legacy.calls != 1 {
		t.Fatalf("legacy steer calls = %d, want 1", legacy.calls)
	}
}

func TestIdleSteerRejectedByUpdateRetainsComposerDraft(t *testing.T) {
	m := newTestModel(t)
	draft := "/steer  correction  "
	setComposer(m, draft)
	beforeLines := len(m.lines)
	beforeHistory := len(m.inputHist.entries)
	beforeTurnID := m.turnID

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("rejected idle steer should not return a command: %#v", cmd)
	}
	if mm.textarea.Value() != draft {
		t.Fatalf("rejected idle steer changed composer draft: got=%q want=%q", mm.textarea.Value(), draft)
	}
	if mm.mode != modeIdle || mm.turnID != beforeTurnID || mm.turnCancel != nil || mm.turnDone != nil {
		t.Fatalf("rejected idle steer started a turn: mode=%s turn=%d cancel=%v done=%v", modeName(mm.mode), mm.turnID, mm.turnCancel != nil, mm.turnDone != nil)
	}
	if len(mm.inputHist.entries) != beforeHistory+1 || mm.inputHist.entries[len(mm.inputHist.entries)-1] != strings.TrimSpace(draft) {
		t.Fatalf("idle steer changed input history unexpectedly: %#v", mm.inputHist.entries)
	}
	if len(mm.lines) <= beforeLines || !hasLineContaining(mm.lines, lineError, "regular turn is not running") {
		t.Fatalf("rejected idle steer feedback missing: %#v", mm.lines)
	}
}

func TestInterruptedTurnRestoresUnconsumedSteersInAdmissionOrder(t *testing.T) {
	m := newTestModel(t)
	stub := &steerSessionStub{turnID: "durable-turn-7", active: true}
	m.steerSession = stub
	m.mode = modeBusy
	m.turnID = 7

	for _, input := range []string{"first", "second", "third"} {
		next, cmd := m.queueWhileBusy("/steer " + input)
		if cmd != nil {
			t.Fatalf("steer admission returned command: %#v", cmd)
		}
		m = next.(*model)
	}
	// Simulate a draft typed while the turn was stopping. It must survive the
	// recovery and remain ahead of restored steer text.
	m.textarea.SetValue("existing draft")
	m.syncComposerHeight()
	next, _ := m.Update(turnSteerConsumedMsg{turnID: 7, sequence: 2, content: "second"})
	m = next.(*model)
	m.finishTurn(context.Canceled)

	if got := m.textarea.Value(); got != "existing draft\nfirst\nthird" {
		t.Fatalf("restored composer = %q, want existing draft followed by pending steers", got)
	}
	if len(m.queue) != 0 || m.inputHist.browsing() || len(m.inputHist.entries) != 0 {
		t.Fatalf("restored steer changed queue/history: queue=%v history=%#v", m.queue, m.inputHist.entries)
	}
	if !hasLineContaining(m.lines, lineSystem, "steer restored to composer: 2 uncommitted input(s)") {
		t.Fatalf("restore feedback missing: %#v", m.lines)
	}
}

func TestInterruptedTurnDoesNotRestoreConsumedSteer(t *testing.T) {
	m := newTestModel(t)
	stub := &steerSessionStub{turnID: "durable-turn-7", active: true}
	m.steerSession = stub
	m.mode = modeBusy
	m.turnID = 7
	next, _ := m.queueWhileBusy("/steer consumed")
	m = next.(*model)
	m.textarea.SetValue("draft")
	next, _ = m.Update(turnSteerConsumedMsg{turnID: 7, sequence: 1, content: "consumed"})
	m = next.(*model)
	m.finishTurn(context.Canceled)

	if got := m.textarea.Value(); got != "draft" {
		t.Fatalf("consumed steer was restored: %q", got)
	}
	if hasLineContaining(m.lines, lineSystem, "steer restored to composer") {
		t.Fatalf("consumed steer produced restore feedback: %#v", m.lines)
	}
}

func TestSteerRestoreOnlyRunsForCancellation(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "success", err: nil},
		{name: "failure", err: errors.New("provider failed")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			stub := &steerSessionStub{turnID: "durable-turn-7", active: true}
			m.steerSession = stub
			m.mode = modeBusy
			m.turnID = 7
			next, _ := m.queueWhileBusy("/steer keep this")
			m = next.(*model)
			m.textarea.SetValue("draft")
			m.finishTurn(tc.err)
			if got := m.textarea.Value(); got != "draft" {
				t.Fatalf("non-cancel %s restored steer into composer: %q", tc.name, got)
			}
			if hasLineContaining(m.lines, lineSystem, "steer restored to composer") {
				t.Fatalf("non-cancel %s produced restore feedback: %#v", tc.name, m.lines)
			}
		})
	}
}

func TestInterruptedSteerRestoreKeepsNormalQueueDrain(t *testing.T) {
	m := newTestModel(t)
	stub := &steerSessionStub{turnID: "durable-turn-7", active: true}
	m.steerSession = stub
	m.mode = modeBusy
	m.turnID = 7
	next, _ := m.queueWhileBusy("/steer keep composing")
	m = next.(*model)
	m.queue = []string{"queued follow-up"}
	m.finishTurn(context.Canceled)
	if m.mode != modeBusy || len(m.queue) != 0 {
		t.Fatalf("normal queue did not drain after cancellation: mode=%s queue=%v", modeName(m.mode), m.queue)
	}
	if got := m.textarea.Value(); got != "keep composing" {
		t.Fatalf("restored composer changed while queued turn started: %q", got)
	}
	cancelAndWaitForTurn(t, m)
}
