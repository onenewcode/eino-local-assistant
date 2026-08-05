package tui

import (
	"strings"
	"testing"

	"eino-local-assistant/internal/store"

	"github.com/charmbracelet/lipgloss"
	"github.com/cloudwego/eino/schema"
)

func TestBuildBacktrackPrompts(t *testing.T) {
	tests := []struct {
		name   string
		groups []store.TurnGroup
		want   []backtrackPrompt
	}{
		{name: "empty"},
		{
			name: "committed history includes the first visible prompt",
			groups: []store.TurnGroup{
				{TurnID: "one", Committed: &store.TurnCommit{Messages: []*schema.Message{schema.UserMessage("first")}}},
				{TurnID: "pending"},
				{TurnID: "two", Started: &store.TurnStart{Input: "fallback"}, Committed: &store.TurnCommit{Messages: []*schema.Message{schema.UserMessage("second")}}},
				{TurnID: "three", Started: &store.TurnStart{Input: "third"}, Committed: &store.TurnCommit{}},
			},
			want: []backtrackPrompt{
				{TurnID: "one", BeforeFirst: true, Text: "first"},
				{TurnID: "two", BoundaryTurnID: "one", Text: "second"},
				{TurnID: "three", BoundaryTurnID: "two", Text: "third"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildBacktrackPrompts(tt.groups)
			if len(got) != len(tt.want) {
				t.Fatalf("prompt count = %d, want %d: %#v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("prompt[%d] = %#v, want %#v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestBacktrackStateSelection(t *testing.T) {
	prompts := []backtrackPrompt{{TurnID: "one"}, {TurnID: "two"}, {TurnID: "three"}}
	state := newBacktrackState(prompts)
	if state.selected != 2 || state.mode != backtrackArmed {
		t.Fatalf("new state = %#v, want latest armed selection", state)
	}
	for _, tt := range []struct {
		name  string
		delta int
		want  int
	}{
		{name: "down clamps", delta: 4, want: 2},
		{name: "up moves", delta: -1, want: 1},
		{name: "up clamps", delta: -8, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state = moveBacktrackSelection(state, tt.delta)
			if state.selected != tt.want {
				t.Fatalf("selected = %d, want %d", state.selected, tt.want)
			}
		})
	}
	if _, ok := selectedBacktrackPrompt(backtrackState{}); ok {
		t.Fatal("empty state should have no selected prompt")
	}
	if got := moveBacktrackSelection(backtrackState{}, 1); got.selected != 0 {
		t.Fatalf("empty move selected = %d, want 0", got.selected)
	}
}

func TestBacktrackOverlayTruncatesAndCapsHeight(t *testing.T) {
	state := newBacktrackState([]backtrackPrompt{{Text: "第一行\n第二行 " + strings.Repeat("很长", 40)}})
	view := renderBacktrackOverlay(24, state)
	if !strings.Contains(view, "Backtrack") || !strings.Contains(view, "Enter select") || !strings.Contains(view, "›") {
		t.Fatalf("overlay is missing title, shortcut, or selection:\n%s", view)
	}
	if !strings.Contains(view, "…") || strings.Contains(view, strings.Repeat("很长", 40)) || len([]rune(view)) > 300 {
		t.Fatalf("prompt was not safely compacted/truncated:\n%s", view)
	}
	if got, want := backtrackOverlayHeight(state), lipgloss.Height(view); got != want {
		t.Fatalf("height = %d, rendered rows = %d", got, want)
	}
	for _, line := range strings.Split(renderBacktrackOverlay(20, state), "\n") {
		if got := lipgloss.Width(line); got > 20 {
			t.Fatalf("narrow overlay line width = %d, want <= 20: %q", got, line)
		}
	}
	if got := backtrackOverlayHeight(state); got > backtrackOverlayRows {
		t.Fatalf("height = %d, exceeds cap %d", got, backtrackOverlayRows)
	}
	many := make([]backtrackPrompt, backtrackMaxVisiblePrompts+3)
	for i := range many {
		many[i].Text = "prompt"
	}
	manyState := newBacktrackState(many)
	manyView := renderBacktrackOverlay(80, manyState)
	if got, want := backtrackOverlayHeight(manyState), backtrackOverlayRows; got != want {
		t.Fatalf("many-prompt height = %d, want cap %d", got, want)
	}
	if got, want := lipgloss.Height(manyView), backtrackOverlayRows; got != want {
		t.Fatalf("many-prompt rendered rows = %d, want %d", got, want)
	}
	empty := newBacktrackState(nil)
	if _, ok := selectedBacktrackPrompt(empty); ok {
		t.Fatal("empty state should have no selected prompt")
	}
	if got, want := backtrackOverlayHeight(empty), lipgloss.Height(renderBacktrackOverlay(20, empty)); got != want {
		t.Fatalf("empty height = %d, rendered rows = %d", got, want)
	}
	if got := renderBacktrackOverlay(0, backtrackState{}); got != "" {
		t.Fatalf("inactive empty overlay = %q, want empty", got)
	}
}
