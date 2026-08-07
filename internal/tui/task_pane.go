package tui

import (
	"fmt"
	"strings"
	"unicode"

	"eino-local-assistant/internal/chat"

	"github.com/charmbracelet/lipgloss"
)

const (
	taskPaneRows     = 7
	taskPaneMaxItems = 5
)

// sessionTaskStatus reads only the UI-safe task projection exposed by Session.
func sessionTaskStatus(session *chat.Session) chat.TaskRunStatus {
	if session == nil {
		return chat.TaskRunStatus{}
	}
	return session.TaskStatus()
}

func hasTaskStatus(status chat.TaskRunStatus) bool {
	return status.Available && strings.TrimSpace(status.State) != ""
}

func (m *model) toggleTaskPane() bool {
	if m.taskPaneOpen {
		m.taskPaneOpen = false
		return true
	}
	if !hasTaskStatus(sessionTaskStatus(m.activeSession())) {
		return false
	}
	m.taskPaneOpen = true
	return true
}

func (m *model) closeTaskPane() {
	if !m.taskPaneOpen {
		return
	}
	m.taskPaneOpen = false
	m.layout()
}

// taskPaneRows stays fixed so updates to a running task do not move the
// composer or viewport while the panel is open.
func (m *model) taskPaneHeight() int {
	if m == nil || !m.taskPaneOpen {
		return 0
	}
	return taskPaneRows
}

func (m *model) taskPaneView() string {
	status := sessionTaskStatus(m.activeSession())
	if !hasTaskStatus(status) {
		return renderUnavailableTaskPane(m.width)
	}
	return renderTaskPane(m.width, status)
}

func renderTaskPane(width int, status chat.TaskRunStatus) string {
	width = max(20, width)
	rows := []string{taskPaneTitleStyle.Render(taskPaneTruncate(taskPaneHeader(status), width))}
	if len(status.Items) == 0 {
		label, value := taskPaneActivity(status)
		rows = append(rows, renderTaskPaneField(label, value, width, taskPaneValueStyle))
		return taskPaneWithRows(rows)
	}
	for _, item := range status.Items[:min(len(status.Items), taskPaneMaxItems)] {
		rows = append(rows, renderTaskPaneItem(item, width))
	}
	if extra := len(status.Items) - taskPaneMaxItems; extra > 0 {
		rows = append(rows, taskPaneLabelStyle.Render(taskPaneTruncate(fmt.Sprintf("  +%d more via /goal", extra), width)))
	}
	return taskPaneWithRows(rows)
}

func renderUnavailableTaskPane(width int) string {
	width = max(20, width)
	rows := []string{
		taskPaneTitleStyle.Render(taskPaneTruncate("Tasks · unavailable · ctrl+t hide", width)),
		renderTaskPaneField("Status", "no task progress is available for this session", width, taskPaneLabelStyle),
	}
	return taskPaneWithRows(rows)
}

func taskPaneWithRows(rows []string) string {
	for len(rows) < taskPaneRows {
		rows = append(rows, "")
	}
	return strings.Join(rows[:taskPaneRows], "\n")
}

func taskPaneHeader(status chat.TaskRunStatus) string {
	parts := []string{"Tasks", taskPaneState(status.State)}
	if status.Tasks > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", status.DoneTasks, status.Tasks))
	}
	parts = append(parts, "ctrl+t hide")
	return strings.Join(parts, " · ")
}

func taskPaneState(state string) string {
	state = strings.TrimSpace(state)
	switch state {
	case "recovery_error":
		return "recovery error"
	case "":
		return "unavailable"
	default:
		return strings.ReplaceAll(state, "_", " ")
	}
}

func taskPaneGoal(status chat.TaskRunStatus) string {
	if goal := taskPaneCompact(status.Goal); goal != "" {
		return goal
	}
	return "plan note was not recorded"
}

func taskPaneActivity(status chat.TaskRunStatus) (string, string) {
	switch status.State {
	case "active":
		if len(status.ActiveTasks) > 0 {
			return "Current", strings.Join(status.ActiveTasks, ", ")
		}
		return "Next", "update the plan when progress changes"
	case "interrupted":
		return "Resume", "send the next request to refresh the checklist"
	case "recovery_error":
		return "Recovery", "inspect the saved plan state before continuing"
	default:
		return "State", taskPaneState(status.State)
	}
}

func renderTaskPaneItem(item chat.TaskListItem, width int) string {
	state := taskPaneState(item.State)
	marker := "□"
	style := taskPaneValueStyle
	switch item.State {
	case "done":
		marker = "✔"
		style = taskPaneLabelStyle.Strikethrough(true)
	case "working":
		style = taskPaneTitleStyle
	}
	text := fmt.Sprintf("  %s %s", marker, taskPaneCompact(item.Goal))
	if id := taskPaneCompact(item.ID); id != "" && taskPaneCompact(item.Goal) == "" {
		text = fmt.Sprintf("  %s %s", marker, id)
	}
	suffix := " · " + state
	return style.Render(taskPaneTruncate(text, max(1, width-lipgloss.Width(suffix)))) + taskPaneLabelStyle.Render(suffix)
}

func renderTaskPaneField(label, value string, width int, valueStyle lipgloss.Style) string {
	prefix := "  " + label + ": "
	budget := max(1, width-lipgloss.Width(prefix))
	return taskPaneLabelStyle.Render(prefix) + valueStyle.Render(taskPaneTruncate(value, budget))
}

func taskPaneCompact(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.Join(strings.Fields(text), " ")
}

func taskPaneTruncate(text string, width int) string {
	text = taskPaneCompact(text)
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
