package tui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEnqueueFollowUpHelpers(t *testing.T) {
	q, ok := enqueueFollowUp(nil, "  hello  ")
	if !ok || len(q) != 1 || q[0] != "hello" {
		t.Fatalf("enqueue trimmed = %#v ok=%v", q, ok)
	}
	if got := queuePreview("a\n\tb  c"); got != "a b c" {
		t.Fatalf("preview whitespace collapse = %q", got)
	}
	long := strings.Repeat("x", 80)
	if got := queuePreview(long); !strings.HasSuffix(got, "…") {
		t.Fatalf("long preview should truncate: %q", got)
	}
	if !strings.Contains(queuedSystemLine(2, "hi"), "queued (2): hi") {
		t.Fatalf("queued system line formatting")
	}
}

func TestQueueDropRemovesOneFollowUpInFIFOOrder(t *testing.T) {
	m := newTestModel(t)
	m.queue = []string{"one", "two", "three"}

	next, cmd := m.submit("/queue drop 2")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("queue drop should not return a command: %#v", cmd)
	}
	if len(mm.queue) != 2 || mm.queue[0] != "one" || mm.queue[1] != "three" {
		t.Fatalf("queue after drop = %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue dropped (2): two") {
		t.Fatalf("drop confirmation missing: %#v", mm.lines)
	}
}

func TestQueueDropRejectsInvalidIndexWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "/queue drop", want: queueCommandUsage},
		{input: "/queue drop 0", want: queueDropIndexError},
		{input: "/queue drop nope", want: queueDropIndexError},
		{input: "/queue drop 3", want: queueDropRangeError},
		{input: "/queue drop 1 extra", want: queueCommandUsage},
	} {
		t.Run(test.input, func(t *testing.T) {
			m := newTestModel(t)
			m.queue = []string{"one", "two"}
			next, _ := m.submit(test.input)
			mm := next.(*model)
			if len(mm.queue) != 2 || mm.queue[0] != "one" || mm.queue[1] != "two" {
				t.Fatalf("invalid drop changed queue: %#v", mm.queue)
			}
			if !hasLineContaining(mm.lines, lineError, test.want) {
				t.Fatalf("error %q missing: %#v", test.want, mm.lines)
			}
		})
	}

	m := newTestModel(t)
	next, _ := m.submit("/queue drop 1")
	mm := next.(*model)
	if len(mm.queue) != 0 || !hasLineContaining(mm.lines, lineError, queueEmptyMessage) {
		t.Fatalf("empty queue drop = queue %#v lines %#v", mm.queue, mm.lines)
	}
}

func TestQueueDropRunsImmediatelyWhileBusy(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 7
	m.queue = []string{"first", "second"}
	m.textarea.SetValue("/queue drop 1")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("busy queue drop should not start another operation: %#v", cmd)
	}
	if mm.mode != modeBusy || mm.turnID != 7 {
		t.Fatalf("queue drop changed active operation: mode=%s turn=%d", modeName(mm.mode), mm.turnID)
	}
	if len(mm.queue) != 1 || mm.queue[0] != "second" {
		t.Fatalf("busy queue after drop = %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue dropped (1): first") {
		t.Fatalf("busy drop confirmation missing: %#v", mm.lines)
	}
}

func TestQueueEditReplacesHeadMiddleAndTailInPlace(t *testing.T) {
	for _, test := range []struct {
		name  string
		index int
		text  string
		want  []string
	}{
		{name: "head", index: 1, text: "edited first", want: []string{"edited first", "two", "three"}},
		{name: "middle", index: 2, text: "edited second", want: []string{"one", "edited second", "three"}},
		{name: "tail", index: 3, text: "edited third", want: []string{"one", "two", "edited third"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newTestModel(t)
			m.queue = []string{"one", "two", "three"}

			next, cmd := m.submit(fmt.Sprintf("/queue edit %d %s", test.index, test.text))
			mm := next.(*model)
			if cmd != nil {
				t.Fatalf("queue edit should not return a command: %#v", cmd)
			}
			if !reflect.DeepEqual(mm.queue, test.want) {
				t.Fatalf("queue after edit = %#v, want %#v", mm.queue, test.want)
			}
			if !hasLineContaining(mm.lines, lineSystem, fmt.Sprintf("queue edited (%d): %s", test.index, queuePreview(test.text))) {
				t.Fatalf("edit confirmation missing: %#v", mm.lines)
			}
		})
	}
}

func TestQueueEditTrimsEdgesAndPreservesInternalWhitespace(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "spaces", input: "/queue edit 2   edited   text  ", want: "edited   text"},
		{name: "tabs", input: "/queue edit 2\t edited\t\ttext \t", want: "edited\t\ttext"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newTestModel(t)
			m.queue = []string{"one", "old", "three"}

			next, cmd := m.submit(test.input)
			mm := next.(*model)
			if cmd != nil {
				t.Fatalf("queue edit should not return a command: %#v", cmd)
			}
			if got := mm.queue[1]; got != test.want {
				t.Fatalf("edited text = %q, want outer trim with internal whitespace preserved as %q", got, test.want)
			}
			if !hasLineContaining(mm.lines, lineSystem, "queue edited (2): edited text") {
				t.Fatalf("edit preview missing: %#v", mm.lines)
			}
		})
	}
}

func TestQueueEditRejectsInvalidInputWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "/queue edit", want: queueCommandUsage},
		{input: "/queue edit 0 replacement", want: queueEditIndexError},
		{input: "/queue edit nope replacement", want: queueEditIndexError},
		{input: "/queue edit 1", want: queueEditTextError},
		{input: "/queue edit 1    ", want: queueEditTextError},
		{input: "/queue edit 3 replacement", want: queueEditRangeError},
	} {
		t.Run(test.input, func(t *testing.T) {
			m := newTestModel(t)
			m.queue = []string{"one", "two"}

			next, cmd := m.submit(test.input)
			mm := next.(*model)
			if cmd != nil {
				t.Fatalf("invalid queue edit should not return a command: %#v", cmd)
			}
			if !reflect.DeepEqual(mm.queue, []string{"one", "two"}) {
				t.Fatalf("invalid edit changed queue: %#v", mm.queue)
			}
			if !hasLineContaining(mm.lines, lineError, test.want) {
				t.Fatalf("error %q missing: %#v", test.want, mm.lines)
			}
		})
	}

	m := newTestModel(t)
	next, _ := m.submit("/queue edit 1 replacement")
	mm := next.(*model)
	if len(mm.queue) != 0 || !hasLineContaining(mm.lines, lineError, queueEmptyMessage) {
		t.Fatalf("empty queue edit = queue %#v lines %#v", mm.queue, mm.lines)
	}
}

func TestQueueEditRejectsNonQueueableReplacementWithoutMutation(t *testing.T) {
	for _, replacement := range []string{"/help", "/status", "/queue clear", "/new topic", "/compact"} {
		t.Run(replacement, func(t *testing.T) {
			m := newTestModel(t)
			m.queue = []string{"one", "two"}

			next, cmd := m.submit(fmt.Sprintf("/queue edit 2 %s", replacement))
			mm := next.(*model)
			if cmd != nil {
				t.Fatalf("rejected queue edit should not return a command: %#v", cmd)
			}
			if !reflect.DeepEqual(mm.queue, []string{"one", "two"}) {
				t.Fatalf("rejected edit changed queue: %#v", mm.queue)
			}
			if !hasLineContaining(mm.lines, lineError, queueEditAdmissionError) {
				t.Fatalf("admission error missing: %#v", mm.lines)
			}
		})
	}
}

func TestQueueEditAcceptsNaturalLanguageAndUnknownSlash(t *testing.T) {
	for _, replacement := range []string{"ordinary follow-up", "/future follow-up"} {
		t.Run(replacement, func(t *testing.T) {
			// Unknown slash input follows the existing busy admission rule and remains queueable.
			m := newTestModel(t)
			m.queue = []string{"one", "two"}

			next, cmd := m.submit(fmt.Sprintf("/queue edit 2 %s", replacement))
			mm := next.(*model)
			if cmd != nil {
				t.Fatalf("accepted queue edit should not return a command: %#v", cmd)
			}
			if got := mm.queue[1]; got != replacement {
				t.Fatalf("edited replacement = %q, want %q", got, replacement)
			}
		})
	}
}

func TestQueueEditRunsImmediatelyWhileBusyWithoutInterrupting(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 7
	interrupted := false
	m.turnCancel = func() { interrupted = true }
	m.queue = []string{"first", "second"}
	m.textarea.SetValue("/queue edit 2 updated follow-up")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("busy queue edit should not start another operation: %#v", cmd)
	}
	if interrupted {
		t.Fatal("busy queue edit interrupted the active turn")
	}
	if mm.mode != modeBusy || mm.turnID != 7 {
		t.Fatalf("queue edit changed active operation: mode=%s turn=%d", modeName(mm.mode), mm.turnID)
	}
	if !reflect.DeepEqual(mm.queue, []string{"first", "updated follow-up"}) {
		t.Fatalf("busy queue after edit = %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue edited (2): updated follow-up") {
		t.Fatalf("busy edit confirmation missing: %#v", mm.lines)
	}
}

func TestQueueEditHelpText(t *testing.T) {
	help := helpText()
	for _, phrase := range []string{
		"/queue drop <1-based-index>",
		"drop one queued follow-up",
		"/queue edit <1-based-index> <new text>",
		"edit one queued follow-up in place",
		"/queue resume",
		"paused after a turn error",
	} {
		if !strings.Contains(help, phrase) {
			t.Fatalf("queue help missing %q: %s", phrase, help)
		}
	}
}

func TestEnterWhileBusyEnqueues(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 1
	m.textarea.SetValue("follow up please")

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		// queue path should not start spinner/event cmds
		t.Fatalf("enqueue should not return turn cmds, got %#v", cmd)
	}
	if mm.mode != modeBusy {
		t.Fatalf("mode should stay busy")
	}
	if mm.turnID != 1 {
		t.Fatalf("turnID should not bump on enqueue, got %d", mm.turnID)
	}
	if len(mm.queue) != 1 || mm.queue[0] != "follow up please" {
		t.Fatalf("queue = %#v", mm.queue)
	}
	if strings.TrimSpace(mm.textarea.Value()) != "" {
		t.Fatalf("composer should reset after enqueue")
	}
	if !hasLineContaining(mm.lines, lineSystem, "queued (1):") {
		t.Fatalf("missing queued system line: %#v", mm.lines)
	}
}

func TestFinishTurnDrainsQueue(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 3
	m.queue = []string{"a", "b"}
	// Avoid a real Ask: use a model that EOFs immediately.
	cmd := m.finishTurn(nil)
	if m.mode != modeBusy {
		t.Fatalf("drain of natural-language item should start a turn, mode=%s", modeName(m.mode))
	}
	if m.turnID != 4 {
		t.Fatalf("turnID = %d, want 4", m.turnID)
	}
	if len(m.queue) != 1 || m.queue[0] != "b" {
		t.Fatalf("remaining queue = %#v", m.queue)
	}
	if !hasLineContaining(m.lines, lineUser, "a") {
		t.Fatalf("drained user line missing: %#v", m.lines)
	}
	if cmd == nil {
		t.Fatalf("startTurn should return spinner/event cmds")
	}
	cancelAndWaitForTurn(t, m)
}

func TestProviderErrorPausesQueueWithoutStartingNextTurn(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 3
	m.queue = []string{"first queued", "second queued"}

	cmd := m.finishTurn(errors.New("provider unavailable"))
	if cmd != nil {
		t.Fatalf("provider error must not start the next queued turn: %#v", cmd)
	}
	if m.mode != modeIdle || m.turnID != 3 {
		t.Fatalf("provider error changed turn state: mode=%s turnID=%d", modeName(m.mode), m.turnID)
	}
	if !m.queuePaused || !reflect.DeepEqual(m.queue, []string{"first queued", "second queued"}) {
		t.Fatalf("provider error must preserve and pause queue: paused=%v queue=%#v", m.queuePaused, m.queue)
	}
	if !hasLineContaining(m.lines, lineSystem, "queue paused after turn error") ||
		!hasLineContaining(m.lines, lineSystem, "/queue resume") {
		t.Fatalf("queue pause feedback missing: %#v", m.lines)
	}
	if !hasLineContaining(m.lines, lineError, "provider unavailable") {
		t.Fatalf("provider error missing: %#v", m.lines)
	}
}

func TestDrainQueueIsNoOpWhilePaused(t *testing.T) {
	m := newTestModel(t)
	m.queuePaused = true
	m.queue = []string{"must remain"}
	m.turnID = 9

	if cmd := m.drainQueue(); cmd != nil {
		t.Fatalf("paused drain should not return a command: %#v", cmd)
	}
	if m.mode != modeIdle || m.turnID != 9 || !reflect.DeepEqual(m.queue, []string{"must remain"}) {
		t.Fatalf("paused drain changed state: mode=%s turnID=%d queue=%#v", modeName(m.mode), m.turnID, m.queue)
	}
}

func TestQueueResumeStartsHeadWhenIdle(t *testing.T) {
	m := newTestModel(t)
	m.queuePaused = true
	m.queue = []string{"first queued", "second queued"}
	m.turnID = 9

	next, cmd := m.submit("/queue resume")
	mm := next.(*model)
	if cmd == nil || mm.mode != modeBusy || mm.turnID != 10 {
		t.Fatalf("idle resume should start the head turn: mode=%s turnID=%d cmd=%v", modeName(mm.mode), mm.turnID, cmd)
	}
	if mm.queuePaused || !reflect.DeepEqual(mm.queue, []string{"second queued"}) {
		t.Fatalf("idle resume changed queue incorrectly: paused=%v queue=%#v", mm.queuePaused, mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, "queue resumed; continuing queued follow-ups") {
		t.Fatalf("resume confirmation missing: %#v", mm.lines)
	}
	cancelAndWaitForTurn(t, mm)
}

func TestQueueResumeRetainsHeadWhenRuntimeGuardPreflightFails(t *testing.T) {
	m := newTestModel(t)
	m.queuePaused = true
	m.queue = []string{"first queued", "second queued"}
	m.turnID = 12
	m.deps.TurnOptions.Timeout = -1

	next, cmd := m.submit("/queue resume")
	mm := next.(*model)
	if cmd != nil || mm.mode != modeIdle || mm.turnID != 12 {
		t.Fatalf("preflight failure should not publish a turn: mode=%s turnID=%d cmd=%v", modeName(mm.mode), mm.turnID, cmd)
	}
	if !mm.queuePaused || !reflect.DeepEqual(mm.queue, []string{"first queued", "second queued"}) {
		t.Fatalf("preflight failure lost or resumed queue: paused=%v queue=%#v", mm.queuePaused, mm.queue)
	}
	if mm.err == nil {
		t.Fatal("preflight failure should remain observable in m.err")
	}
	if !hasLineContaining(mm.lines, lineError, "configure runtime guard") ||
		!hasLineContaining(mm.lines, lineSystem, "queue paused after turn error") {
		t.Fatalf("preflight failure feedback missing: %#v", mm.lines)
	}
}

func TestQueueResumeRetainsHeadWhenSessionPreflightFails(t *testing.T) {
	m := newTestModel(t)
	m.queuePaused = true
	m.queue = []string{"first queued", "second queued"}
	m.turnID = 12
	m.deps.Session = nil

	next, cmd := m.submit("/queue resume")
	mm := next.(*model)
	if cmd != nil || mm.mode != modeIdle || mm.turnID != 12 {
		t.Fatalf("session preflight failure should not publish a turn: mode=%s turnID=%d cmd=%v", modeName(mm.mode), mm.turnID, cmd)
	}
	if !mm.queuePaused || !reflect.DeepEqual(mm.queue, []string{"first queued", "second queued"}) || mm.err == nil {
		t.Fatalf("session preflight failure changed state: paused=%v queue=%#v err=%v", mm.queuePaused, mm.queue, mm.err)
	}
	if !hasLineContaining(mm.lines, lineError, "session is unavailable") ||
		!hasLineContaining(mm.lines, lineSystem, "queue paused after turn error") {
		t.Fatalf("session preflight feedback missing: %#v", mm.lines)
	}
}

func TestQueueResumeBusyDefersAndRetainsPause(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 7
	m.queuePaused = true
	m.queue = []string{"must wait"}
	interrupted := false
	m.turnCancel = func() { interrupted = true }

	next, cmd := m.submit("/queue resume")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("busy resume must not start a concurrent turn: %#v", cmd)
	}
	if mm.mode != modeBusy || mm.turnID != 7 || !mm.queuePaused || interrupted {
		t.Fatalf("busy resume changed active operation: mode=%s turnID=%d paused=%v interrupted=%v", modeName(mm.mode), mm.turnID, mm.queuePaused, interrupted)
	}
	if !reflect.DeepEqual(mm.queue, []string{"must wait"}) {
		t.Fatalf("busy resume changed queue: %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, queueResumeBusyMessage) {
		t.Fatalf("busy resume feedback missing: %#v", mm.lines)
	}
}

func TestQueueResumeCompactingDefersAndRetainsPause(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeCompacting
	m.compactID = 4
	m.queuePaused = true
	m.queue = []string{"must wait"}

	next, cmd := m.submit("/queue resume")
	mm := next.(*model)
	if cmd != nil || mm.mode != modeCompacting || mm.compactID != 4 || !mm.queuePaused {
		t.Fatalf("compacting resume changed active operation: mode=%s compactID=%d paused=%v cmd=%v", modeName(mm.mode), mm.compactID, mm.queuePaused, cmd)
	}
	if !reflect.DeepEqual(mm.queue, []string{"must wait"}) {
		t.Fatalf("compacting resume changed queue: %#v", mm.queue)
	}
	if !hasLineContaining(mm.lines, lineSystem, queueResumeBusyMessage) {
		t.Fatalf("compacting resume feedback missing: %#v", mm.lines)
	}
}

func TestQueueResumeEmptyAndRepeatedAreSafe(t *testing.T) {
	m := newTestModel(t)
	m.queuePaused = true

	for i := 0; i < 2; i++ {
		next, cmd := m.submit("/queue resume")
		mm := next.(*model)
		if cmd != nil || mm.mode != modeIdle || mm.queuePaused || len(mm.queue) != 0 {
			t.Fatalf("empty resume %d changed state: mode=%s paused=%v queue=%#v cmd=%v", i, modeName(mm.mode), mm.queuePaused, mm.queue, cmd)
		}
		if !hasLineContaining(mm.lines, lineSystem, queueEmptyMessage) {
			t.Fatalf("empty resume %d missing stable feedback: %#v", i, mm.lines)
		}
	}
}

func TestQueueListAndStatusExposePause(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLineFields = []string{statusFieldModel, statusFieldQueue}
	m.queuePaused = true
	m.queue = []string{"one"}

	next, _ := m.submit("/queue")
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "Queue (1) [paused]") ||
		!hasLineContaining(mm.lines, lineSystem, "/queue resume") {
		t.Fatalf("queue list omitted paused state or resume hint: %#v", mm.lines)
	}

	next, _ = mm.submit("/status")
	mm = next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "queue_paused=true") ||
		!hasLineContaining(mm.lines, lineSystem, "queue_resume=/queue resume") {
		t.Fatalf("status omitted paused state or resume hint: %#v", mm.lines)
	}
	if !strings.Contains(mm.statusLabel(), "queue:paused") {
		t.Fatalf("status bar omitted paused state: %q", mm.statusLabel())
	}
}

func TestFinishTurnDrainsLocalSlashThenTurn(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 1
	m.queue = []string{"/help", "hello"}
	cmd := m.finishTurn(nil)
	if !hasLineContaining(m.lines, lineSystem, "Commands:") {
		t.Fatalf("help should have been drained: %#v", m.lines)
	}
	if m.mode != modeBusy {
		t.Fatalf("after help, hello should start a turn")
	}
	if m.turnID != 2 {
		t.Fatalf("turnID = %d, want 2", m.turnID)
	}
	if len(m.queue) != 0 {
		t.Fatalf("queue should be empty, got %#v", m.queue)
	}
	if !hasLineContaining(m.lines, lineUser, "hello") {
		t.Fatalf("hello user line missing")
	}
	if cmd == nil {
		t.Fatalf("expected turn cmds")
	}
	cancelAndWaitForTurn(t, m)
}

func TestInterruptKeepsQueue(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 5
	canceled := false
	m.turnCancel = func() { canceled = true }
	m.queue = []string{"next"}

	m.interruptTurn("interrupted")
	if !canceled {
		t.Fatalf("interrupt should cancel turn context")
	}
	if len(m.queue) != 1 || m.queue[0] != "next" {
		t.Fatalf("queue must be kept: %#v", m.queue)
	}
	// Simulate Ask returning canceled.
	cmd := m.finishTurn(context.Canceled)
	if !hasLineContaining(m.lines, lineSystem, "interrupted") {
		t.Fatalf("interrupted system line missing")
	}
	if m.mode != modeBusy {
		t.Fatalf("queued next should auto-start")
	}
	if m.turnID != 6 {
		t.Fatalf("turnID = %d, want 6", m.turnID)
	}
	if cmd == nil {
		t.Fatalf("expected drain startTurn cmds")
	}
	cancelAndWaitForTurn(t, m)
}

func TestCancellationPreservesExistingQueuePause(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 5
	m.queuePaused = true
	m.queue = []string{"must still wait"}

	cmd := m.finishTurn(context.Canceled)
	if cmd != nil || m.mode != modeIdle || !m.queuePaused || !reflect.DeepEqual(m.queue, []string{"must still wait"}) {
		t.Fatalf("cancellation bypassed existing queue pause: cmd=%v mode=%s paused=%v queue=%#v", cmd, modeName(m.mode), m.queuePaused, m.queue)
	}
	if !hasLineContaining(m.lines, lineSystem, "interrupted") {
		t.Fatalf("interruption feedback missing: %#v", m.lines)
	}
}

func TestQueueMaxRejected(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	for i := range maxQueue {
		m.queue = append(m.queue, fmt.Sprintf("item-%d", i))
	}
	m.textarea.SetValue("one-too-many")
	next, _ := m.queueWhileBusy("one-too-many")
	mm := next.(*model)
	if len(mm.queue) != maxQueue {
		t.Fatalf("queue length = %d, want %d", len(mm.queue), maxQueue)
	}
	if !hasLineContaining(mm.lines, lineError, "queue full") {
		t.Fatalf("expected queue full error: %#v", mm.lines)
	}
	if strings.TrimSpace(mm.textarea.Value()) != "one-too-many" {
		t.Fatalf("full queue must keep draft in composer, got %q", mm.textarea.Value())
	}
}

func TestToolEventsTrackCurrentTool(t *testing.T) {
	m := newTestModel(t)
	m.turnID = 1
	m.mode = modeBusy

	next, _ := m.Update(turnToolStartMsg{turnID: 1, tool: "get_current_time", input: "{}"})
	mm := next.(*model)
	if mm.currentTool != "get_current_time" {
		t.Fatalf("currentTool = %q", mm.currentTool)
	}
	next, _ = mm.Update(turnToolEndMsg{turnID: 1, tool: "get_current_time", output: "ok"})
	mm = next.(*model)
	if mm.currentTool != "" {
		t.Fatalf("currentTool should clear after end, got %q", mm.currentTool)
	}
}

func newTestModel(t *testing.T) *model {
	t.Helper()
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: session,
		Status:  StatusInfo{Model: "test-model", Tools: []string{"get_current_time"}, MaxModelSteps: 8},
	})
	m.width = 80
	m.height = 24
	m.layout()
	return m
}

func cancelAndWaitForTurn(t *testing.T, m *model) {
	t.Helper()
	if m.turnCancel != nil {
		m.turnCancel()
	}
	if m.turnDone == nil {
		return
	}
	select {
	case <-m.turnDone:
		// A completed turn is emitted only after its durable lifecycle is done.
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for durable turn to stop")
	}
}

// Ensure staticModel still satisfies chat.Model for package tests.
var _ chat.Model = staticModel{}
