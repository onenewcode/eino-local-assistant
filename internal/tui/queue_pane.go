package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxQueuePaneRows = 5

func isQueuePaneToggleKey(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyRunes && msg.Alt && len(msg.Runes) == 1 && strings.EqualFold(string(msg.Runes), "q")
}

func queuePaneKeyRune(msg tea.KeyMsg, want rune) bool {
	return msg.Type == tea.KeyRunes && !msg.Alt && len(msg.Runes) == 1 && msg.Runes[0] == want
}

func (m *model) queuePaneVisible() bool {
	return len(m.queue) > 0
}

func (m *model) normalizeQueuePaneSelection() {
	if len(m.queue) == 0 {
		m.queuePaneFocused = false
		m.queuePaneSelected = 0
		return
	}
	if m.queuePaneSelected < 0 {
		m.queuePaneSelected = 0
	}
	if m.queuePaneSelected >= len(m.queue) {
		m.queuePaneSelected = len(m.queue) - 1
	}
}

func (m *model) openQueuePane() {
	if !m.queuePaneVisible() {
		return
	}
	m.queuePaneFocused = true
	m.normalizeQueuePaneSelection()
	m.clearSlashMenu()
	m.clearBacktrack()
	m.textarea.Blur()
	m.layout()
	m.refreshViewport()
}

func (m *model) closeQueuePane() {
	m.queuePaneFocused = false
	m.normalizeQueuePaneSelection()
	m.textarea.Focus()
	m.syncSlashMenu()
	m.layout()
	m.refreshViewport()
}

func (m *model) handleQueuePaneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyEsc:
		// Busy Esc is handled before this pane so it keeps the interrupt contract.
		m.closeQueuePane()
		return m, nil
	case msg.Type == tea.KeyUp || queuePaneKeyRune(msg, 'k'):
		if m.queuePaneSelected > 0 {
			m.queuePaneSelected--
		}
		return m, nil
	case msg.Type == tea.KeyDown || queuePaneKeyRune(msg, 'j'):
		if m.queuePaneSelected < len(m.queue)-1 {
			m.queuePaneSelected++
		}
		return m, nil
	case msg.Type == tea.KeyEnter:
		return m.sendSelectedQueuedFollowUp()
	case queuePaneKeyRune(msg, 'x'):
		return m.cancelSelectedQueuedFollowUp()
	default:
		return m, nil
	}
}

func (m *model) cancelSelectedQueuedFollowUp() (tea.Model, tea.Cmd) {
	m.normalizeQueuePaneSelection()
	if len(m.queue) == 0 {
		return m, nil
	}
	index := m.queuePaneSelected + 1
	var dropped string
	m.queue, dropped, _ = dropQueuedFollowUp(m.queue, index)
	if len(m.queue) == 0 {
		m.queuePaused = false
	}
	m.normalizeQueuePaneSelection()
	// Keep pane cancellation observable without treating the prompt as sent.
	m.appendLine(lineSystem, fmt.Sprintf("queue cancelled (%d): %s", index, queuePreview(dropped)))
	m.appendLine(lineSep, "")
	if !m.queuePaneFocused {
		m.textarea.Focus()
	}
	m.layout()
	m.refreshViewport()
	return m, nil
}

// sendSelectedQueuedFollowUp makes the selected item the next item to drain.
// Sending during a turn deliberately cancels that turn first; the existing
// completion path then promotes the selected message through the normal submit path.
func (m *model) sendSelectedQueuedFollowUp() (tea.Model, tea.Cmd) {
	m.normalizeQueuePaneSelection()
	if len(m.queue) == 0 {
		return m, nil
	}
	if m.mode == modeCompacting {
		m.appendLine(lineError, "send now unavailable while context compaction is running; wait for it to finish")
		return m, nil
	}
	m.queue, _ = promoteQueuedFollowUp(m.queue, m.queuePaneSelected+1)
	m.queuePaused = false
	m.closeQueuePane()
	if m.mode == modeBusy {
		m.interruptTurn("send queued prompt now")
		m.cancelActiveTask("user sent a queued prompt now")
		m.showInterruptRequested()
		return m, nil
	}
	if m.mode == modeIdle {
		return m, m.drainQueue()
	}
	m.appendLine(lineError, "send now unavailable while another operation is running")
	return m, nil
}

func (m *model) queuePaneHeight() int {
	if !m.queuePaneVisible() {
		return 0
	}
	return lipgloss.Height(m.queuePaneView())
}

func (m *model) queuePaneView() string {
	return renderQueuePane(m.width, m.queue, m.queuePaused, m.queuePaneFocused, m.queuePaneSelected)
}

func renderQueuePane(width int, queue []string, paused, focused bool, selected int) string {
	if len(queue) == 0 {
		return ""
	}
	if width < 20 {
		width = 20
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= len(queue) {
		selected = len(queue) - 1
	}

	header := fmt.Sprintf("Queued messages (%d)", len(queue))
	if paused {
		header += " [paused]"
	}
	if focused {
		header += " | managing"
	} else {
		header += " | alt+q manage"
	}
	rows := []string{queuePaneTitleStyle.Render(queuePaneTruncate(header, width))}
	windowSelected := selected
	if !focused {
		windowSelected = 0
	}
	start, end := queuePaneWindow(len(queue), windowSelected)
	for i := start; i < end; i++ {
		prefix := fmt.Sprintf("  %d. ", i+1)
		style := queuePaneRowStyle
		if focused && i == selected {
			prefix = fmt.Sprintf("> %d. ", i+1)
			style = queuePaneSelectedStyle
		}
		rows = append(rows, style.Render(prefix+queuePaneTruncate(queuePreview(queue[i]), width-lipgloss.Width(prefix))))
	}
	if len(queue) > maxQueuePaneRows {
		rows = append(rows, queuePaneMetaStyle.Render(fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(queue))))
	}
	if focused {
		rows = append(rows, queuePaneMetaStyle.Render("  enter send now | x cancel | alt+q composer"))
	}
	return strings.Join(rows, "\n")
}

// queuePaneWindow keeps the selected row visible while capping vertical chrome.
func queuePaneWindow(total, selected int) (start, end int) {
	if total <= maxQueuePaneRows {
		return 0, total
	}
	if selected < maxQueuePaneRows {
		return 0, maxQueuePaneRows
	}
	end = min(total, selected+1)
	start = end - maxQueuePaneRows
	return start, end
}

func queuePaneTruncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	var b strings.Builder
	for _, r := range text {
		if lipgloss.Width(b.String())+lipgloss.Width(string(r))+3 > width {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "..."
}
