package tui

import (
	"fmt"
	"strings"
	"unicode"

	"eino-local-assistant/internal/store"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxSessionPickerRows = 7

type sessionPickerIntent int

const (
	sessionPickerResume sessionPickerIntent = iota
	sessionPickerFork
)

type sessionPickerState struct {
	intent   sessionPickerIntent
	query    string
	selected int
	entries  []store.ThreadMeta
}

func (m *model) sessionPickerOpen() bool {
	return m.sessionPicker != nil
}

func (m *model) openSessionPicker() (tea.Model, tea.Cmd) {
	return m.openSessionPickerFor(sessionPickerResume)
}

func (m *model) openForkPicker() (tea.Model, tea.Cmd) {
	return m.openSessionPickerFor(sessionPickerFork)
}

func (m *model) openSessionPickerFor(intent sessionPickerIntent) (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	if m.hasPendingApproval() {
		m.appendLine(lineError, "busy: resolve the pending approval first")
		return m, nil
	}
	if m.sideQuestions > 0 {
		m.appendLine(lineError, "busy: wait for the side question to finish first")
		return m, nil
	}
	current := m.activeSession()
	if current == nil || m.deps.Store == nil {
		m.appendLine(lineError, "session picker is unavailable")
		return m, nil
	}
	list, err := m.deps.Store.ListThreads(m.processCtx())
	if err != nil {
		m.appendLine(lineError, "list sessions: "+err.Error())
		return m, nil
	}
	entries := sessionPickerCandidatesForIntent(list, current.ID(), intent)
	if len(entries) == 0 {
		message := "no other active sessions to resume"
		if intent == sessionPickerFork {
			message = "no active sessions available to fork"
		}
		m.appendLine(lineSystem, message)
		m.appendLine(lineSep, "")
		return m, nil
	}
	m.sessionPicker = &sessionPickerState{intent: intent, entries: entries}
	m.clearSlashMenu()
	m.clearBacktrack()
	m.layout()
	m.refreshViewport()
	return m, nil
}

func sessionPickerCandidates(entries []store.ThreadMeta, activeID string) []store.ThreadMeta {
	return sessionPickerCandidatesForIntent(entries, activeID, sessionPickerResume)
}

func sessionPickerCandidatesForIntent(entries []store.ThreadMeta, activeID string, intent sessionPickerIntent) []store.ThreadMeta {
	candidates := make([]store.ThreadMeta, 0, len(entries))
	for _, entry := range entries {
		entry.ID = strings.TrimSpace(entry.ID)
		if entry.ID == "" || entry.ArchivedAt != nil {
			continue
		}
		if intent == sessionPickerResume && entry.ID == activeID {
			continue
		}
		entry.Title = sessionPickerTitle(entry.Title)
		candidates = append(candidates, entry)
	}
	return candidates
}

func (m *model) closeSessionPicker() {
	m.sessionPicker = nil
	m.layout()
	m.refreshViewport()
}

func (m *model) sessionPickerRows() []store.ThreadMeta {
	if !m.sessionPickerOpen() {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(m.sessionPicker.query))
	rows := make([]store.ThreadMeta, 0, len(m.sessionPicker.entries))
	for _, entry := range m.sessionPicker.entries {
		if query == "" || strings.Contains(strings.ToLower(entry.ID), query) || strings.Contains(strings.ToLower(entry.Title), query) {
			rows = append(rows, entry)
		}
	}
	return rows
}

func (m *model) selectedSessionPickerEntry() (store.ThreadMeta, bool) {
	rows := m.sessionPickerRows()
	if len(rows) == 0 {
		return store.ThreadMeta{}, false
	}
	selected := m.sessionPicker.selected
	if selected < 0 || selected >= len(rows) {
		selected = 0
		m.sessionPicker.selected = selected
	}
	return rows[selected], true
}

func (m *model) handleSessionPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.sessionPickerOpen() {
		return m, nil
	}
	switch {
	case msg.Type == tea.KeyEsc:
		m.closeSessionPicker()
		return m, nil
	case msg.Type == tea.KeyEnter:
		entry, ok := m.selectedSessionPickerEntry()
		if !ok {
			m.appendLine(lineError, "session picker: no matching session")
			return m, nil
		}
		switch m.sessionPicker.intent {
		case sessionPickerResume:
			return m.resumeSession(entry.ID, false)
		case sessionPickerFork:
			return m.cmdFork(entry.ID)
		default:
			m.appendLine(lineError, "session picker: invalid action")
			return m, nil
		}
	case msg.Type == tea.KeyUp || sessionPickerKeyRune(msg, 'k'):
		m.moveSessionPickerSelection(-1)
		return m, nil
	case msg.Type == tea.KeyDown || sessionPickerKeyRune(msg, 'j'):
		m.moveSessionPickerSelection(1)
		return m, nil
	case msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete:
		m.sessionPicker.query = trimLastRune(m.sessionPicker.query)
		m.sessionPicker.selected = 0
		m.layout()
		m.refreshViewport()
		return m, nil
	case msg.Type == tea.KeyRunes && !msg.Alt && len(msg.Runes) > 0:
		m.sessionPicker.query += string(msg.Runes)
		m.sessionPicker.selected = 0
		m.layout()
		m.refreshViewport()
		return m, nil
	default:
		return m, nil
	}
}

func sessionPickerKeyRune(msg tea.KeyMsg, want rune) bool {
	return msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && !msg.Alt && msg.Runes[0] == want
}

func (m *model) moveSessionPickerSelection(delta int) {
	rows := m.sessionPickerRows()
	if len(rows) == 0 || delta == 0 {
		return
	}
	selected := m.sessionPicker.selected + delta
	if selected < 0 {
		selected = len(rows) - 1
	}
	if selected >= len(rows) {
		selected = 0
	}
	m.sessionPicker.selected = selected
	m.refreshViewport()
}

func (m *model) sessionPickerView() string {
	if !m.sessionPickerOpen() {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	rows := m.sessionPickerRows()
	selected := m.sessionPicker.selected
	if selected < 0 || selected >= len(rows) {
		selected = 0
	}
	title := "Resume Session"
	footerAction := "resume"
	if m.sessionPicker.intent == sessionPickerFork {
		title = "Fork Session"
		footerAction = "fork"
	}
	lines := []string{
		statusLinePickerTitleStyle.Render(fmt.Sprintf("%s · active sessions (%d)", title, len(m.sessionPicker.entries))),
		statusLinePickerSearchStyle.Render("  Type to search: " + m.sessionPicker.query),
	}
	start, end := sessionPickerVisibleRange(len(rows), selected)
	for i := start; i < end; i++ {
		entry := rows[i]
		marker := "  "
		if i == selected {
			marker = "> "
		}
		line := fmt.Sprintf("%s%s  %s  msgs=%d  %s", marker, sessionPickerTitle(entry.Title), entry.ID, entry.MessageCount, sessionPickerUpdatedAt(entry))
		line = truncateSessionPickerText(line, width)
		if i == selected {
			lines = append(lines, statusLinePickerSelectedStyle.Render(line))
		} else {
			lines = append(lines, statusLinePickerRowStyle.Render(line))
		}
	}
	if len(rows) == 0 {
		lines = append(lines, statusLinePickerSearchStyle.Render("  No matching active sessions"))
	}
	lines = append(lines, statusLinePickerFooterStyle.Render("  up/down or j/k select · enter "+footerAction+" · esc cancel"))
	return strings.Join(lines, "\n")
}

func sessionPickerTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, title)
	if title == "" {
		return "(untitled)"
	}
	return title
}

func sessionPickerUpdatedAt(entry store.ThreadMeta) string {
	if entry.UpdatedAt.IsZero() {
		return "updated=unknown"
	}
	return "updated=" + entry.UpdatedAt.Local().Format("2006-01-02 15:04")
}

func sessionPickerVisibleRange(total, selected int) (int, int) {
	if total <= maxSessionPickerRows {
		return 0, total
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	start := selected - maxSessionPickerRows/2
	if start < 0 {
		start = 0
	}
	end := start + maxSessionPickerRows
	if end > total {
		end = total
		start = end - maxSessionPickerRows
	}
	return start, end
}

func truncateSessionPickerText(value string, width int) string {
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
	limit := min(len(runes), width-1)
	return string(runes[:limit]) + "…"
}

func (m *model) sessionPickerHeight() int {
	if !m.sessionPickerOpen() {
		return 0
	}
	return lipgloss.Height(m.sessionPickerView())
}
