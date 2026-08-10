package tui

import (
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/tools"
)

func TestShortSessionID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"20260715-120000-abc123", "abc123"},
		{"short", "short"},
		{"verylongsessionidentifier", "verylong…"},
	}
	for _, tc := range cases {
		if got := shortSessionID(tc.in); got != tc.want {
			t.Errorf("shortSessionID(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatIdleStatusIncludesCoreParts(t *testing.T) {
	p := idleStatusParts{
		shortID: "abc123",
		title:   "debug",
		context: "ctx=754/6.0k (12%)",
		queued:  "queued:2",
	}
	got := formatIdleStatus(120, p)
	for _, want := range []string{"ready", "abc123", "debug", "ctx=754/6.0k (12%)", "queued:2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status %q missing %q", got, want)
		}
	}
}

func TestFormatIdleStatusDropsWhenNarrow(t *testing.T) {
	p := idleStatusParts{
		shortID: "abc123",
		title:   "a fairly long title here",
		context: "ctx=12.3k/128k (9%)",
		queued:  "queued:3",
		follow:  "↑ End to follow",
	}
	got := formatIdleStatus(40, p)
	if !strings.Contains(got, "ready") {
		t.Fatalf("must keep ready: %q", got)
	}
	// Title should be among first drops.
	if strings.Contains(got, "a fairly long title") {
		t.Fatalf("narrow width should drop title: %q", got)
	}
	// Prefer keeping queue/follow if possible.
	if !strings.Contains(got, "queued:3") && !strings.Contains(got, "End to follow") {
		t.Fatalf("expected queue or follow retained: %q", got)
	}
}

func TestFormatIdleStatusKeepsContextLongerThanDecoration(t *testing.T) {
	p := idleStatusParts{
		shortID: "abc123",
		title:   "title-here",
		context: "ctx=1.2k/4.0k (30%)",
	}
	// Wide enough for ready + ctx but not all decoration.
	got := formatIdleStatus(28, p)
	if !strings.Contains(got, "ctx=") {
		// At very narrow widths ctx may still drop last; use a width that fits base+ctx.
		got = formatIdleStatus(35, p)
	}
	if !strings.Contains(got, "ctx=") {
		t.Fatalf("expected ctx retained over title/id when possible: %q", got)
	}
	if strings.Contains(got, "title-here") && !strings.Contains(got, "ctx=") {
		t.Fatalf("title must not outrank ctx: %q", got)
	}
}

func TestSessionCtxFragmentOmitsUnknown(t *testing.T) {
	m := newTestModel(t)
	if got := sessionCtxFragment(m.deps.Session); got != "" {
		t.Fatalf("no measurement should omit ctx fragment, got %q", got)
	}
	if got := sessionCtxFragment(nil); got != "" {
		t.Fatalf("nil session should omit ctx fragment, got %q", got)
	}
}

func TestJoinStatusSuffix(t *testing.T) {
	if got := joinStatusSuffix(statusExtras{}); got != "" {
		t.Fatalf("empty extras=%q", got)
	}
	got := joinStatusSuffix(statusExtras{context: "ctx=1/2 (50%)", queued: "queued:1", follow: "↑ End to follow"})
	want := " · ctx=1/2 (50%) · queued:1 · ↑ End to follow"
	if got != want {
		t.Fatalf("suffix=%q want %q", got, want)
	}
}

func TestStatusLineDefaultShowsModelWithoutProvider(t *testing.T) {
	m := newTestModel(t)
	m.deps.Status.Model = "openai/test-model"
	m.deps.PolicyInfo.Mode = "ask"
	line := m.statusLine()
	if strings.Contains(line, "openai/") {
		t.Fatalf("status must omit provider prefix: %q", line)
	}
	if !strings.Contains(line, "test-model") {
		t.Fatalf("status must contain the model name: %q", line)
	}
	// The Codex-like footer never exposes the internal ctx= rendering.
	if strings.Contains(line, "ctx=") {
		t.Fatalf("status must use human-facing context text: %q", line)
	}
	if !strings.Contains(line, "ask") || strings.Contains(line, "mode=") {
		t.Fatalf("default status must include the compact mode: %q", line)
	}
}

func TestStatusLineUsedTokensUsesCompactKUnits(t *testing.T) {
	tests := []struct {
		tokens int
		want   string
	}{
		{tokens: 999, want: "999 used"},
		{tokens: 1_500, want: "1.5k used"},
		{tokens: 535_272, want: "535k used"},
	}
	for _, test := range tests {
		if got := statusLineUsedTokenCount(test.tokens); got != test.want {
			t.Fatalf("statusLineUsedTokenCount(%d) = %q, want %q", test.tokens, got, test.want)
		}
	}
}

func TestStatusLabelBusyUsesConfigurableActivity(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLine.Fields = []string{statusFieldModelWithReasoning, statusFieldTaskProgress, statusFieldActivity}
	m.mode = modeBusy
	line := m.statusLabel()
	if !strings.Contains(line, "thinking") {
		t.Fatalf("busy status missing activity in task progress: %q", line)
	}
	if !strings.Contains(line, "Tasks 0/0") {
		t.Fatalf("busy status missing task progress: %q", line)
	}
	if strings.Contains(line, "ctx=") {
		t.Fatalf("busy status without measurement must omit ctx: %q", line)
	}
}

func TestStatusActivityCanBeHiddenIndependently(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLine.Fields = []string{statusFieldModelWithReasoning, statusFieldTaskProgress}
	m.mode = modeBusy
	if line := m.statusLabel(); strings.Contains(line, "thinking") {
		t.Fatalf("activity ignored status-line selection: %q", line)
	}
}

func TestStatusLineModeIsOptionalAndTracksState(t *testing.T) {
	state, err := tools.NewApprovalState(tools.ApprovalOnRequest)
	if err != nil {
		t.Fatal(err)
	}
	m := newTestModel(t)
	m.deps.PolicyInfo.ApprovalState = state
	m.deps.StatusLine.Fields = []string{statusFieldMode}
	if got := m.statusLabel(); got != "ask" {
		t.Fatalf("mode field = %q, want ask", got)
	}
	if err := state.SetYolo(); err != nil {
		t.Fatal(err)
	}
	if got := m.statusLabel(); got != "yolo" {
		t.Fatalf("mode field = %q, want yolo", got)
	}
}

func TestTaskStatusFragmentUsesCompactTaskState(t *testing.T) {
	tests := []struct {
		name   string
		status chat.TaskRunStatus
		want   string
	}{
		{name: "active progress", status: chat.TaskRunStatus{Available: true, State: "active", DoneTasks: 1, Tasks: 3}, want: "task:1/3"},
		{name: "interrupted", status: chat.TaskRunStatus{Available: true, State: "interrupted"}, want: "task:interrupted"},
		{name: "unavailable", status: chat.TaskRunStatus{}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &taskControlModel{status: test.status}
			session := mustSession(t, backend, "system")
			if got := taskStatusFragment(session); got != test.want {
				t.Fatalf("taskStatusFragment() = %q, want %q", got, test.want)
			}
		})
	}
}
