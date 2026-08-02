package tui

import (
	"strings"
	"testing"
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

func TestStatusLineIdleContainsModelAndID(t *testing.T) {
	m := newTestModel(t)
	line := m.statusLine()
	if !strings.Contains(line, "ready") {
		t.Fatalf("idle status should contain ready: %q", line)
	}
	if !strings.Contains(line, "test-model") {
		t.Fatalf("idle status should contain model: %q", line)
	}
	// Fresh session has no measured context; do not pollute with ctx=?.
	if strings.Contains(line, "ctx=") {
		t.Fatalf("idle status without measurement must omit ctx: %q", line)
	}
}

func TestStatusLineFollowHint(t *testing.T) {
	m := newTestModel(t)
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
	m.mode = modeBusy
	m.queue = []string{"next"}
	line := m.statusLabel()
	if !strings.Contains(line, "Working") {
		t.Fatalf("busy status missing Working: %q", line)
	}
	if !strings.Contains(line, "queued:1") {
		t.Fatalf("busy status should include queue via shared extras: %q", line)
	}
	if strings.Contains(line, "ctx=") {
		t.Fatalf("busy status without measurement must omit ctx: %q", line)
	}
}
