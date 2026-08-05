package tui

import (
	"strings"
	"unicode"

	"eino-local-assistant/internal/store"

	"github.com/charmbracelet/lipgloss"
	"github.com/cloudwego/eino/schema"
)

type backtrackMode uint8

const (
	backtrackInactive backtrackMode = iota
	backtrackArmed
	backtrackSelecting
)

// Aliases keep the state names descriptive at call sites that prefer the
// mode-prefixed spelling.
const (
	backtrackModeInactive  = backtrackInactive
	backtrackModeArmed     = backtrackArmed
	backtrackModeSelecting = backtrackSelecting
)

type backtrackPrompt struct {
	TurnID         string
	BoundaryTurnID string
	BeforeFirst    bool
	Text           string
}

type backtrackState struct {
	mode     backtrackMode
	prompts  []backtrackPrompt
	selected int
}

const (
	backtrackMaxVisiblePrompts = 6
	backtrackOverlayRows       = 2 + backtrackMaxVisiblePrompts
	backtrackEmptyRows         = 3
)

func buildBacktrackPrompts(groups []store.TurnGroup) []backtrackPrompt {
	prompts := make([]backtrackPrompt, 0)
	lastCommitted := ""
	hasCommittedBoundary := false
	for _, group := range groups {
		if group.Committed == nil {
			continue
		}
		if text := committedUserPrompt(group); text != "" {
			prompts = append(prompts, backtrackPrompt{
				TurnID:         group.TurnID,
				BoundaryTurnID: lastCommitted,
				BeforeFirst:    !hasCommittedBoundary,
				Text:           text,
			})
		}
		lastCommitted = group.TurnID
		hasCommittedBoundary = true
	}
	return prompts
}

func committedUserPrompt(group store.TurnGroup) string {
	for _, message := range group.Committed.Messages {
		if message != nil && message.Role == schema.User {
			if text := strings.TrimSpace(message.Content); text != "" {
				return text
			}
		}
	}
	if group.Started != nil {
		return strings.TrimSpace(group.Started.Input)
	}
	return ""
}

func newBacktrackState(prompts []backtrackPrompt) backtrackState {
	state := backtrackState{mode: backtrackArmed, prompts: append([]backtrackPrompt(nil), prompts...)}
	if len(state.prompts) == 0 {
		return state
	}
	state.selected = len(state.prompts) - 1
	return state
}

func selectedBacktrackPrompt(state backtrackState) (backtrackPrompt, bool) {
	if len(state.prompts) == 0 || state.selected < 0 || state.selected >= len(state.prompts) {
		return backtrackPrompt{}, false
	}
	return state.prompts[state.selected], true
}

func moveBacktrackSelection(state backtrackState, delta int) backtrackState {
	if len(state.prompts) == 0 {
		state.selected = 0
		return state
	}
	state.selected = max(0, min(len(state.prompts)-1, state.selected+delta))
	return state
}

func backtrackOverlayHeight(state backtrackState) int {
	if state.mode == backtrackInactive {
		return 0
	}
	if len(state.prompts) == 0 {
		return backtrackEmptyRows
	}
	return min(backtrackOverlayRows, len(state.prompts)+2)
}

func renderBacktrackOverlay(width int, state backtrackState) string {
	if state.mode == backtrackInactive {
		return ""
	}
	width = max(20, width)
	selected := state.selected
	if selected < 0 {
		selected = 0
	}
	if selected >= len(state.prompts) {
		selected = len(state.prompts) - 1
	}

	rows := []string{backtrackTitleStyle.Render(backtrackTruncate("Backtrack · choose a prompt", width))}
	if len(state.prompts) == 0 {
		rows = append(rows, backtrackEmptyStyle.Render(backtrackTruncate("  no previous committed prompts", width)))
	} else {
		start := max(0, selected-backtrackMaxVisiblePrompts+1)
		end := min(len(state.prompts), start+backtrackMaxVisiblePrompts)
		for i := start; i < end; i++ {
			prefix := "  "
			style := backtrackPromptStyle
			if i == selected {
				prefix = "› "
				style = backtrackSelectedStyle
			}
			line := prefix + backtrackTruncate(state.prompts[i].Text, max(1, width-lipgloss.Width(prefix)))
			rows = append(rows, style.Render(line))
		}
	}
	rows = append(rows, backtrackHelpStyle.Render(backtrackTruncate("  ↑/↓ move · Enter select · Esc cancel", width)))
	return strings.Join(rows, "\n")
}

func backtrackTruncate(text string, width int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if width <= 0 || text == "" {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	limit := width - lipgloss.Width("…")
	var b strings.Builder
	used := 0
	for _, r := range text {
		runeWidth := lipgloss.Width(string(r))
		if used+runeWidth > limit {
			break
		}
		b.WriteRune(r)
		used += runeWidth
	}
	return b.String() + "…"
}

var (
	backtrackTitleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true)
	backtrackPromptStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	backtrackSelectedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Background(lipgloss.Color("236")).Bold(true)
	backtrackEmptyStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	backtrackHelpStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))
)
