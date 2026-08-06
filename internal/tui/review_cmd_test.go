package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReviewCommandEmptyAndNormalAreDisplayOnly(t *testing.T) {
	session := mustSession(t, &staticModel{}, "system")
	beforeTranscript := session.Transcript()
	beforeUsage := session.UsageSummary()
	modelCalls := 0
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: session,
		WorkspaceDiff: func(context.Context) (string, error) {
			return "diff --git a/a.go b/a.go\n+changed", nil
		},
		WorkspaceReview: func(context.Context, string) (string, error) {
			modelCalls++
			return "finding: inspect changed line", nil
		},
	})
	next, cmd := m.submit("/review")
	if cmd == nil {
		t.Fatal("review should run asynchronously")
	}
	msg := cmd().(reviewDoneMsg)
	next, _ = next.(*model).Update(msg)
	m = next.(*model)
	if modelCalls != 1 || !hasLineContaining(m.lines, lineReview, "finding: inspect changed line") {
		t.Fatalf("review result missing: calls=%d lines=%#v", modelCalls, m.lines)
	}
	if !reflect.DeepEqual(session.Transcript(), beforeTranscript) || session.UsageSummary() != beforeUsage {
		t.Fatal("review changed transcript or usage")
	}

	modelCalls = 0
	m = newModel(Deps{Ctx: context.Background(), Session: session,
		WorkspaceDiff:   func(context.Context) (string, error) { return "", nil },
		WorkspaceReview: func(context.Context, string) (string, error) { modelCalls++; return "unexpected", nil },
	})
	next, cmd = m.submit("/review")
	msg = cmd().(reviewDoneMsg)
	next, _ = next.(*model).Update(msg)
	m = next.(*model)
	if modelCalls != 0 || !hasLineContaining(m.lines, lineReview, "No workspace changes to review") {
		t.Fatalf("empty review behavior incorrect: calls=%d lines=%#v", modelCalls, m.lines)
	}
}

func TestReviewCommandAdmissionErrorsAndStaleResult(t *testing.T) {
	m := newModel(Deps{Ctx: context.Background(), Session: mustSession(t, &staticModel{}, "system"),
		WorkspaceDiff:   func(context.Context) (string, error) { return "diff", nil },
		WorkspaceReview: func(context.Context, string) (string, error) { return "answer", nil }})
	m.mode = modeBusy
	next, cmd := m.submit("/review")
	if cmd != nil || len(m.queue) != 0 || !hasLineContaining(next.(*model).lines, lineError, "finish the current operation") {
		t.Fatal("busy review was queued or started")
	}
	m.mode = modeIdle
	m.pendingApproval = &approvalRequestMsg{}
	next, cmd = m.submit("/review")
	if cmd != nil || !hasLineContaining(next.(*model).lines, lineError, "pending approval") {
		t.Fatal("approval review was not rejected")
	}
	m.pendingApproval = nil
	m.sideQuestions = 1
	next, cmd = m.submit("/review")
	if cmd != nil || !hasLineContaining(next.(*model).lines, lineError, "side question") {
		t.Fatal("side-question review was not rejected")
	}
	m.sideQuestions = 0
	_, cmd = m.submit("/review")
	if cmd == nil {
		t.Fatal("review did not start")
	}
	next, _ = m.submit("/review")
	if !hasLineContaining(next.(*model).lines, lineError, "already running") {
		t.Fatal("duplicate review was not rejected")
	}
	old := cmd().(reviewDoneMsg)
	second := mustSession(t, &staticModel{}, "second")
	m.replaceSession(second)
	before := len(m.lines)
	next, _ = m.Update(old)
	if len(next.(*model).lines) != before || m.reviewInFlight {
		t.Fatal("stale review result changed current session")
	}
}

func TestReviewCommandSanitizesErrorsAndOutput(t *testing.T) {
	m := newModel(Deps{Ctx: context.Background(), Session: mustSession(t, &staticModel{}, "system"),
		WorkspaceDiff:   func(context.Context) (string, error) { return "diff", nil },
		WorkspaceReview: func(context.Context, string) (string, error) { return "bad\x1b[2J\xff", nil }})
	next, cmd := m.submit("/review")
	next, _ = next.(*model).Update(cmd().(reviewDoneMsg))
	if got := next.(*model).lines[len(next.(*model).lines)-1].text; strings.ContainsAny(got, "\x1b\xff") {
		t.Fatalf("review output contains unsafe bytes: %q", got)
	}
	m = newModel(Deps{Ctx: context.Background(), Session: mustSession(t, &staticModel{}, "system"),
		WorkspaceDiff:   func(context.Context) (string, error) { return "diff", errors.New("git failed\x1b[31m") },
		WorkspaceReview: func(context.Context, string) (string, error) { t.Fatal("model called after Git error"); return "", nil }})
	next, cmd = m.submit("/review")
	next, _ = next.(*model).Update(cmd().(reviewDoneMsg))
	if hasLineContaining(next.(*model).lines, lineError, "\x1b") {
		t.Fatal("Git error leaked control byte")
	}
	m = newModel(Deps{Ctx: context.Background(), Session: mustSession(t, &staticModel{}, "system"),
		WorkspaceDiff:   func(context.Context) (string, error) { return "diff", nil },
		WorkspaceReview: func(context.Context, string) (string, error) { return "", errors.New("provider unavailable") }})
	next, cmd = m.submit("/review")
	next, _ = next.(*model).Update(cmd().(reviewDoneMsg))
	if !hasLineContaining(next.(*model).lines, lineError, "review error: provider unavailable") {
		t.Fatal("model error was not displayed")
	}

	m = newModel(Deps{Ctx: context.Background(), Session: mustSession(t, &staticModel{}, "system"),
		WorkspaceDiff: func(context.Context) (string, error) { return "diff", nil },
		WorkspaceReview: func(context.Context, string) (string, error) {
			return strings.Repeat("x", reviewCommandMaxBytes+10), nil
		}})
	next, cmd = m.submit("/review")
	next, _ = next.(*model).Update(cmd().(reviewDoneMsg))
	if !hasLineContaining(next.(*model).lines, lineSystem, "review output truncated") {
		t.Fatal("review truncation was not displayed")
	}
}
