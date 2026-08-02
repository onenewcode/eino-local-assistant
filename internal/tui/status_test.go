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
		tokens:  "1.2k",
		cost:    "$0.01",
		ctx:     "ctx=40%",
		queued:  "queued:2",
	}
	got := formatIdleStatus(120, p)
	for _, want := range []string{"ready", "deepseek", "abc123", "debug", "1.2k", "$0.01", "ctx=40%", "queued:2"} {
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
		tokens:  "12.3k",
		cost:    "$1.23",
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
	// Prefer keeping cost/queue/follow if possible.
	if !strings.Contains(got, "$1.23") && !strings.Contains(got, "queued:3") {
		t.Fatalf("expected cost or queue retained: %q", got)
	}
}

func TestStatusLineIdleContainsModelAndID(t *testing.T) {
	m := newTestModel(t)
	// Session from newTestModel has an id only if store is set; without store, ID may be empty.
	// Status model name still comes from deps.
	line := m.statusLine()
	if !strings.Contains(line, "ready") {
		t.Fatalf("idle status should contain ready: %q", line)
	}
	if !strings.Contains(line, "test-model") {
		t.Fatalf("idle status should contain model: %q", line)
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
