package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/tools"
)

func TestDiffCommandRendersSnapshotWithoutSessionMutation(t *testing.T) {
	called := false
	session := mustSession(t, &staticModel{}, "system")
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: session,
		WorkspaceDiff: func(ctx context.Context) (string, error) {
			called = true
			if ctx == nil {
				t.Fatal("diff callback received nil context")
			}
			return "diff --git a/README.md b/README.md\n+changed\n", nil
		},
	})
	m.queue = []string{"follow-up"}
	beforeTranscript := len(session.Transcript())
	beforeQueue := append([]string(nil), m.queue...)

	next, cmd := m.submit("/diff")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("/diff must not start a model turn: %#v", cmd)
	}
	if !called {
		t.Fatal("/diff did not call the injected read-only callback")
	}
	if len(session.Transcript()) != beforeTranscript {
		t.Fatalf("/diff changed session transcript: before=%d after=%d", beforeTranscript, len(session.Transcript()))
	}
	if !reflect.DeepEqual(mm.queue, beforeQueue) {
		t.Fatalf("/diff changed queue: got %#v want %#v", mm.queue, beforeQueue)
	}
	for _, want := range []string{
		"Scope: tracked changes against HEAD (staged + unstaged) plus non-ignored untracked files; ignored omitted",
		"diff --git a/README.md b/README.md",
		"+changed",
	} {
		if !hasLineContaining(mm.lines, lineSystem, want) {
			t.Fatalf("/diff output missing %q: %#v", want, mm.lines)
		}
	}
}

func TestDiffCommandDistinguishesEmptySnapshot(t *testing.T) {
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		WorkspaceDiff: func(context.Context) (string, error) {
			return "", nil
		},
	})

	next, cmd := m.submit("/diff")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("empty /diff must not start a model turn: %#v", cmd)
	}
	if !hasLineContaining(mm.lines, lineSystem, "No workspace changes.") {
		t.Fatalf("empty /diff output missing: %#v", mm.lines)
	}
	if hasLineContaining(mm.lines, lineError, "diff unavailable") {
		t.Fatalf("empty /diff must not be reported as an error: %#v", mm.lines)
	}
}

func TestDiffCommandReportsGitFailureWithoutLeakingControlBytes(t *testing.T) {
	m := newModel(Deps{
		Ctx:     context.Background(),
		Session: mustSession(t, &staticModel{}, "system"),
		WorkspaceDiff: func(context.Context) (string, error) {
			return "", errors.New("workspace is not a Git repository\x1b[31m")
		},
	})

	next, cmd := m.submit("/diff")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("failed /diff must not start a model turn: %#v", cmd)
	}
	if !hasLineContaining(mm.lines, lineError, "diff unavailable: workspace is not a Git repository?[31m") {
		t.Fatalf("Git error missing or unsanitized: %#v", mm.lines)
	}
	for _, line := range mm.lines {
		if line.kind == lineError && strings.ContainsAny(line.text, "\x00\x1b\t\r\n") {
			t.Fatalf("control bytes leaked into Git error: %q", line.text)
		}
	}
}

func TestDiffCommandSanitizesNonTextAndCapsOutput(t *testing.T) {
	raw := "diff --git a/file b/file\x00\x1b[31m\n\t" + strings.Repeat("x", diffCommandMaxBytes)
	output := renderDiffCommand(raw)
	if strings.ContainsAny(output, "\x00\x1b\t\r") {
		t.Fatalf("unsafe bytes leaked into /diff output: %q", output[:min(len(output), 200)])
	}
	if !strings.Contains(output, "non-text diff output was sanitized") {
		t.Fatalf("missing non-text notice: %q", output[:min(len(output), 200)])
	}
	if !strings.Contains(output, "diff output truncated after 131072 bytes") {
		t.Fatalf("missing truncation notice")
	}
	payload := sanitizeDiffPayload(raw, diffCommandMaxBytes)
	if len(payload.text) > diffCommandMaxBytes || !payload.truncated || !payload.nonText {
		t.Fatalf("unexpected sanitized payload: len=%d truncated=%v nonText=%v", len(payload.text), payload.truncated, payload.nonText)
	}
}

func TestDiffCommandRunsImmediatelyWithoutChangingBusyOperation(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode mode
	}{
		{name: "busy", mode: modeBusy},
		{name: "compacting", mode: modeCompacting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.mode = tc.mode
			m.turnID = 7
			m.queue = []string{"retained"}
			cancelled := false
			m.turnCancel = func() { cancelled = true }
			m.deps.WorkspaceDiff = func(context.Context) (string, error) {
				return "diff --git a/file b/file\n+change", nil
			}
			beforeQueue := append([]string(nil), m.queue...)

			next, cmd := m.queueWhileBusy("/diff")
			mm := next.(*model)
			if cmd != nil || mm.mode != tc.mode || mm.turnID != 7 {
				t.Fatalf("/diff changed active operation: mode=%s turn=%d cmd=%v", modeName(mm.mode), mm.turnID, cmd)
			}
			if cancelled {
				t.Fatal("/diff must not cancel the active operation")
			}
			if !reflect.DeepEqual(mm.queue, beforeQueue) {
				t.Fatalf("/diff changed queue: got %#v want %#v", mm.queue, beforeQueue)
			}
			if !hasLineContaining(mm.lines, lineSystem, "diff --git a/file b/file") {
				t.Fatalf("busy /diff output missing: %#v", mm.lines)
			}
		})
	}
}

func TestDiffCommandRunsImmediatelyWhileApprovalIsPending(t *testing.T) {
	m := newTestModel(t)
	m.pendingApproval = &approvalRequestMsg{Request: tools.ApprovalRequest{Tool: "run_command"}}
	m.queue = []string{"retained"}
	m.deps.WorkspaceDiff = func(context.Context) (string, error) {
		return "", nil
	}

	next, cmd := m.queueWhileBusy("/diff")
	mm := next.(*model)
	if cmd != nil || mm.pendingApproval == nil {
		t.Fatalf("pending approval was changed by /diff: cmd=%v approval=%v", cmd, mm.pendingApproval)
	}
	if !hasLineContaining(mm.lines, lineSystem, "No workspace changes.") {
		t.Fatalf("pending approval /diff output missing: %#v", mm.lines)
	}
	if !reflect.DeepEqual(mm.queue, []string{"retained"}) {
		t.Fatalf("pending approval /diff changed queue: %#v", mm.queue)
	}
}

func TestDiffCommandRejectsArgumentsWithStableUsage(t *testing.T) {
	m := newTestModel(t)
	m.deps.WorkspaceDiff = func(context.Context) (string, error) {
		t.Fatal("invalid /diff must not call callback")
		return "", nil
	}

	next, cmd := m.submit("/diff extra")
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("invalid /diff must not start a model turn: %#v", cmd)
	}
	if !hasLineContaining(mm.lines, lineError, diffCommandUsage) {
		t.Fatalf("missing /diff usage: %#v", mm.lines)
	}
}
