package tui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// transcriptKeyMap is pager-only: arrows/j/k stay with the composer.
func transcriptKeyMap() viewport.KeyMap {
	return viewport.KeyMap{
		PageUp: key.NewBinding(
			key.WithKeys("pgup"),
			key.WithHelp("pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown"),
			key.WithHelp("pgdn", "page down"),
		),
		// Intentionally empty: ctrl+b/f move the textarea cursor; ctrl+u/d
		// collide with delete-line / quit. Mouse wheel covers fine scrolling.
		HalfPageUp:   key.NewBinding(),
		HalfPageDown: key.NewBinding(),
		Up:           key.NewBinding(),
		Down:         key.NewBinding(),
		Left:         key.NewBinding(),
		Right:        key.NewBinding(),
	}
}

// isViewportScrollMsg reports keys/mouse that should scroll the transcript
// instead of editing the composer.
func isViewportScrollMsg(msg tea.Msg, km viewport.KeyMap) bool {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return key.Matches(msg, km.PageUp) ||
			key.Matches(msg, km.PageDown) ||
			key.Matches(msg, km.HalfPageUp) ||
			key.Matches(msg, km.HalfPageDown)
	case tea.MouseMsg:
		if msg.Action != tea.MouseActionPress {
			return false
		}
		return msg.Button == tea.MouseButtonWheelUp ||
			msg.Button == tea.MouseButtonWheelDown
	default:
		return false
	}
}

// applyContent keeps stick-to-bottom semantics used by mainstream agent TUIs:
// auto-follow only when the user is already at (or was sticky to) the bottom.
func applyContent(vp *viewport.Model, stickBottom *bool, content string) {
	if vp == nil || stickBottom == nil {
		return
	}
	stick := *stickBottom || vp.AtBottom()
	prevY := vp.YOffset
	vp.SetContent(content)
	if stick {
		vp.GotoBottom()
		*stickBottom = true
		return
	}
	vp.SetYOffset(prevY)
	*stickBottom = vp.AtBottom()
}
