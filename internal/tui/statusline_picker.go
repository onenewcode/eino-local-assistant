package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxStatusLinePickerRows = 7

type statusLinePickerState struct {
	query    string
	selected int
	draft    StatusLineConfig
}

type statusLinePickerRow struct {
	field string
	label string
}

var statusLinePickerFieldRows = []statusLinePickerRow{
	{field: statusFieldModelWithReasoning, label: "model-with-reasoning"},
	{field: statusFieldContextUsed, label: "context-used"},
	{field: statusFieldUsedTokens, label: "used-tokens"},
	{field: statusFieldTaskProgress, label: "task-progress"},
	{field: statusFieldActivity, label: "activity"},
	{field: statusFieldMode, label: "mode"},
}

func (m *model) statusLinePickerOpen() bool {
	return m.statusLinePicker != nil
}

func (m *model) openStatusLinePicker() (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "status-line picker unavailable while busy; retry when idle")
		return m, nil
	}
	if m.hasPendingApproval() {
		m.appendLine(lineError, "status-line picker unavailable while approval is pending; retry after it is resolved")
		return m, nil
	}
	if m.sideQuestions > 0 {
		m.appendLine(lineError, "status-line picker unavailable while a side question is running; retry after it finishes")
		return m, nil
	}
	if m.deps.SaveStatusLineConfig == nil {
		m.appendLine(lineError, "status-line persistence is unavailable in this TUI")
		return m, nil
	}
	m.statusLinePicker = &statusLinePickerState{draft: copyStatusLineConfig(m.deps.StatusLine)}
	m.clearSlashMenu()
	m.clearBacktrack()
	m.layout()
	m.refreshViewport()
	return m, nil
}

func (m *model) closeStatusLinePicker() {
	m.statusLinePicker = nil
	m.layout()
	m.refreshViewport()
}

func copyStatusLineConfig(config StatusLineConfig) StatusLineConfig {
	return StatusLineConfig{
		Fields: append([]string(nil), config.Fields...),
	}
}

func (m *model) statusLinePickerRows() []statusLinePickerRow {
	if !m.statusLinePickerOpen() {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(m.statusLinePicker.query))
	rows := make([]statusLinePickerRow, 0, len(statusLinePickerFieldRows))
	for _, row := range statusLinePickerFieldRows {
		if query == "" || strings.Contains(strings.ToLower(row.label), query) {
			rows = append(rows, row)
		}
	}
	return rows
}

func (m *model) selectedStatusLinePickerRow() (statusLinePickerRow, bool) {
	rows := m.statusLinePickerRows()
	if len(rows) == 0 {
		return statusLinePickerRow{}, false
	}
	selected := m.statusLinePicker.selected
	if selected < 0 || selected >= len(rows) {
		selected = 0
		m.statusLinePicker.selected = selected
	}
	return rows[selected], true
}

func (m *model) handleStatusLinePickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.statusLinePickerOpen() {
		return m, nil
	}
	switch {
	case msg.Type == tea.KeyEsc:
		m.closeStatusLinePicker()
		return m, nil
	case msg.Type == tea.KeyEnter:
		return m.saveStatusLinePickerDraft()
	case msg.Type == tea.KeySpace || statusLinePickerKeyRune(msg, ' '):
		m.toggleSelectedStatusLinePickerRow()
		return m, nil
	case msg.Type == tea.KeyUp || msg.Type == tea.KeyLeft || statusLinePickerKeyRune(msg, 'k') || statusLinePickerKeyRune(msg, 'h'):
		m.moveStatusLinePickerSelection(-1)
		return m, nil
	case msg.Type == tea.KeyDown || msg.Type == tea.KeyRight || statusLinePickerKeyRune(msg, 'j') || statusLinePickerKeyRune(msg, 'l'):
		m.moveStatusLinePickerSelection(1)
		return m, nil
	case msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete:
		m.statusLinePicker.query = trimLastRune(m.statusLinePicker.query)
		m.statusLinePicker.selected = 0
		m.layout()
		m.refreshViewport()
		return m, nil
	case msg.Type == tea.KeyRunes && !msg.Alt && len(msg.Runes) > 0:
		m.statusLinePicker.query += string(msg.Runes)
		m.statusLinePicker.selected = 0
		m.layout()
		m.refreshViewport()
		return m, nil
	default:
		return m, nil
	}
}

func statusLinePickerKeyRune(msg tea.KeyMsg, want rune) bool {
	return msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && !msg.Alt && msg.Runes[0] == want
}

func trimLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}

func (m *model) moveStatusLinePickerSelection(delta int) {
	rows := m.statusLinePickerRows()
	if len(rows) == 0 || delta == 0 {
		return
	}
	selected := m.statusLinePicker.selected + delta
	if selected < 0 {
		selected = len(rows) - 1
	}
	if selected >= len(rows) {
		selected = 0
	}
	m.statusLinePicker.selected = selected
	m.refreshViewport()
}

func (m *model) toggleSelectedStatusLinePickerRow() {
	row, ok := m.selectedStatusLinePickerRow()
	if !ok {
		return
	}
	if containsStatusLineField(m.statusLinePicker.draft.Fields, row.field) {
		if len(m.statusLinePicker.draft.Fields) == 1 {
			m.appendLine(lineError, "status line must retain at least one field")
			return
		}
		m.statusLinePicker.draft.Fields = withoutStatusLineField(m.statusLinePicker.draft.Fields, row.field)
	} else {
		m.statusLinePicker.draft.Fields = append(m.statusLinePicker.draft.Fields, row.field)
	}
	m.refreshViewport()
}

func containsStatusLineField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

func withoutStatusLineField(fields []string, hidden string) []string {
	visible := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != hidden {
			visible = append(visible, field)
		}
	}
	return visible
}

func (m *model) saveStatusLinePickerDraft() (tea.Model, tea.Cmd) {
	if !m.statusLinePickerOpen() {
		return m, nil
	}
	draft := normalizeStatusLineConfig(m.statusLinePicker.draft)
	if m.deps.SaveStatusLineConfig == nil {
		m.appendLine(lineError, "status-line persistence is unavailable in this TUI")
		return m, nil
	}
	if err := m.deps.SaveStatusLineConfig(draft); err != nil {
		m.appendLine(lineError, "save status-line settings: "+err.Error())
		return m, nil
	}
	m.deps.StatusLine = draft
	m.closeStatusLinePicker()
	m.appendLine(lineSystem, "status-line settings saved")
	m.appendLine(lineSep, "")
	return m, nil
}

func (m *model) statusLinePickerView() string {
	if !m.statusLinePickerOpen() {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	rows := m.statusLinePickerRows()
	selected := m.statusLinePicker.selected
	if selected < 0 || selected >= len(rows) {
		selected = 0
	}
	lines := []string{
		statusLinePickerTitleStyle.Render("Configure Status Line"),
		statusLinePickerSearchStyle.Render("  Type to search: " + m.statusLinePicker.query),
	}
	start, end := statusLinePickerVisibleRange(len(rows), selected)
	for i := start; i < end; i++ {
		row := rows[i]
		checked := containsStatusLineField(m.statusLinePicker.draft.Fields, row.field)
		marker := "[ ]"
		if checked {
			marker = "[x]"
		}
		line := "  " + marker + " " + row.label
		line = truncateStatusLinePickerText(line, width)
		if i == selected {
			line = statusLinePickerSelectedStyle.Render(line)
		} else {
			line = statusLinePickerRowStyle.Render(line)
		}
		lines = append(lines, line)
	}
	if len(rows) == 0 {
		lines = append(lines, statusLinePickerSearchStyle.Render("  No matching fields"))
	}
	lines = append(lines,
		statusLinePickerFooterStyle.Render("  space toggle · enter save · esc cancel"),
	)
	return strings.Join(lines, "\n")
}

func statusLinePickerVisibleRange(total, selected int) (int, int) {
	if total <= maxStatusLinePickerRows {
		return 0, total
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	start := selected - maxStatusLinePickerRows/2
	if start < 0 {
		start = 0
	}
	end := start + maxStatusLinePickerRows
	if end > total {
		end = total
		start = end - maxStatusLinePickerRows
	}
	return start, end
}

func truncateStatusLinePickerText(value string, width int) string {
	if width < 20 {
		width = 20
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	if width <= 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
}

func (m *model) statusLinePickerHeight() int {
	if !m.statusLinePickerOpen() {
		return 0
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	return lipgloss.Height(m.statusLinePickerView())
}
