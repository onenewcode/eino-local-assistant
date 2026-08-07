package tui

import (
	"errors"
	"testing"
)

func TestTurnErrorPausesQueueForManualContinuation(t *testing.T) {
	m := newTestModel(t)
	m.queue = []string{"queued follow-up"}
	m.finishTurn(errors.New("provider failed"))
	if !m.queuePaused || len(m.queue) != 1 {
		t.Fatalf("turn error should keep queue paused: paused=%v queue=%#v", m.queuePaused, m.queue)
	}
	if !hasLineContaining(m.lines, lineError, "provider failed") {
		t.Fatalf("turn error missing: %#v", m.lines)
	}
}
