package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func TestApplyContentStickiness(t *testing.T) {
	vp := viewport.New(40, 5)
	// Tall content so scrolling is possible.
	base := strings.Repeat("line\n", 40)
	stick := true
	applyContent(&vp, &stick, base)
	if !vp.AtBottom() {
		t.Fatalf("sticky init should pin to bottom")
	}
	if !stick {
		t.Fatalf("stickBottom should remain true at bottom")
	}

	// Scroll up manually and clear stickiness.
	vp.PageUp()
	if vp.AtBottom() {
		t.Fatalf("expected not at bottom after PageUp")
	}
	stick = false
	prevY := vp.YOffset

	// Append more content without stick — YOffset should stay put.
	applyContent(&vp, &stick, base+"extra\n")
	if stick {
		t.Fatalf("stickBottom should stay false when not at bottom")
	}
	if vp.YOffset != prevY {
		t.Fatalf("YOffset = %d, want preserved %d", vp.YOffset, prevY)
	}

	// Sticky path forces bottom.
	stick = true
	applyContent(&vp, &stick, base+"extra\nmore\n")
	if !vp.AtBottom() || !stick {
		t.Fatalf("sticky refresh should GotoBottom")
	}
}

func TestIsViewportScrollMsg(t *testing.T) {
	km := transcriptKeyMap()
	if !isViewportScrollMsg(tea.KeyMsg{Type: tea.KeyPgUp}, km) {
		t.Fatalf("pgup should be a scroll key")
	}
	if !isViewportScrollMsg(tea.KeyMsg{Type: tea.KeyPgDown}, km) {
		t.Fatalf("pgdown should be a scroll key")
	}
	if isViewportScrollMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}, km) {
		t.Fatalf("j must stay with the composer, not scroll")
	}
	wheel := tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}
	if !isViewportScrollMsg(wheel, km) {
		t.Fatalf("mouse wheel should scroll")
	}
}
