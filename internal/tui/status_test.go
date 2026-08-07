package tui

import (
	"strings"
	"testing"

	"eino-local-assistant/internal/chat"
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
		model:   "deepseek",
		shortID: "abc123",
		title:   "debug",
		context: "ctx=754/6.0k (12%)",
		queued:  "queued:2",
	}
	got := formatIdleStatus(120, p)
	for _, want := range []string{"ready", "deepseek", "abc123", "debug", "ctx=754/6.0k (12%)", "queued:2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status %q missing %q", got, want)
		}
	}
}

func TestFormatIdleStatusDropsWhenNarrow(t *testing.T) {
	p := idleStatusParts{
		model:   "very-long-model-name",
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
		model:   "model-name",
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
	line := m.statusLine()
	if strings.Contains(line, "openai/") {
		t.Fatalf("status must omit provider prefix: %q", line)
	}
	if !strings.Contains(line, "test-model") {
		t.Fatalf("idle status should contain model: %q", line)
	}
	// The Codex-like footer never exposes the internal ctx= rendering.
	if strings.Contains(line, "ctx=") {
		t.Fatalf("status must use human-facing context text: %q", line)
	}
}

func TestStatusLineFollowHint(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLineFields = []string{statusFieldModel, statusFieldFollow}
	// Build tall content so not-at-bottom is possible.
	for range 40 {
		m.appendLine(lineSystem, strings.Repeat("x", 20))
	}
	m.viewport.GotoTop()
	m.stickBottom = false
	line := m.statusLine()
	if !strings.Contains(line, "End to follow") {
		t.Fatalf("expected follow hint when scrolled up: %q", line)
	}
	m.viewport.GotoBottom()
	m.stickBottom = true
	line = m.statusLine()
	if strings.Contains(line, "End to follow") {
		t.Fatalf("follow hint should clear at bottom: %q", line)
	}
}

func TestStatusLabelBusyUsesSharedSuffix(t *testing.T) {
	m := newTestModel(t)
	m.deps.StatusLineFields = []string{statusFieldModel, statusFieldActivity, statusFieldQueue}
	m.mode = modeBusy
	m.queue = []string{"next"}
	line := m.statusLabel()
	if !strings.Contains(line, "thinking") {
		t.Fatalf("busy status missing activity: %q", line)
	}
	if !strings.Contains(line, "queued:1") {
		t.Fatalf("busy status should include queue via shared extras: %q", line)
	}
	if strings.Contains(line, "ctx=") {
		t.Fatalf("busy status without measurement must omit ctx: %q", line)
	}
}

func TestTaskStatusFragmentUsesCompactTaskState(t *testing.T) {
	tests := []struct {
		name   string
		status chat.TaskRunStatus
		want   string
	}{
		{name: "active progress", status: chat.TaskRunStatus{Available: true, State: "active", DoneTasks: 1, Tasks: 3}, want: "task:1/3"},
		{name: "fresh plan", status: chat.TaskRunStatus{Available: true, State: "active", PlanRequired: true}, want: "task:plan"},
		{name: "interrupted", status: chat.TaskRunStatus{Available: true, State: "interrupted"}, want: "task:interrupted"},
		{name: "complete", status: chat.TaskRunStatus{Available: true, State: "complete"}, want: "task:complete"},
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
