package tui

import (
	"fmt"
	"strings"
	"unicode"

	"eino-local-assistant/internal/chat"

	"github.com/charmbracelet/lipgloss"
)

const (
	taskPaneRows       = 6
	taskPaneMaxGapRows = 2
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
	if !hasTaskStatus(sessionTaskStatus(m.deps.Session)) {
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
	status := sessionTaskStatus(m.deps.Session)
	if !hasTaskStatus(status) {
		return renderUnavailableTaskPane(m.width)
	}
	return renderTaskPane(m.width, status)
}

func renderTaskPane(width int, status chat.TaskRunStatus) string {
	width = max(20, width)
	rows := []string{
		taskPaneTitleStyle.Render(taskPaneTruncate(taskPaneHeader(status), width)),
		renderTaskPaneField("Goal", taskPaneGoal(status), width, taskPaneValueStyle),
		renderTaskPaneField("Scope", taskPaneScope(status), width, taskPaneValueStyle),
	}
	label, value := taskPaneActivity(status)
	rows = append(rows, renderTaskPaneField(label, value, width, taskPaneValueStyle))

	gaps := taskPaneGaps(status)
	if len(gaps) == 0 {
		rows = append(rows, renderTaskPaneField("Status", taskPaneFootnote(status), width, taskPaneLabelStyle))
	} else {
		for _, gap := range gaps[:min(len(gaps), taskPaneMaxGapRows)] {
			rows = append(rows, renderTaskPaneField("Gap", gap, width, taskPaneGapStyle))
		}
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
	if status.PlanRequired {
		parts = append(parts, "plan required")
	} else if status.Tasks > 0 {
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
	if status.PlanRequired {
		return "workspace changes need a task plan"
	}
	return "task goal was not recorded"
}

func taskPaneScope(status chat.TaskRunStatus) string {
	if status.PlanRequired {
		return "task plan required"
	}
	parts := make([]string, 0, 2)
	if status.Requirements > 0 {
		parts = append(parts, fmt.Sprintf("%d requirements", status.Requirements))
	}
	if status.Scenarios > 0 {
		parts = append(parts, fmt.Sprintf("%d scenarios", status.Scenarios))
	}
	if len(parts) == 0 && status.Tasks > 0 {
		parts = append(parts, fmt.Sprintf("%d tasks", status.Tasks))
	}
	if len(parts) == 0 {
		return "no plan details"
	}
	return strings.Join(parts, " · ")
}

func taskPaneActivity(status chat.TaskRunStatus) (string, string) {
	switch status.State {
	case "active":
		if status.PlanRequired {
			return "Next", "create a task plan before continuing"
		}
		if active := taskPaneCompact(status.ActiveTask); active != "" {
			return "Current", active
		}
		return "Next", "select the next planned task"
	case "complete":
		return "Result", "all task evidence is accepted"
	case "interrupted":
		return "Resume", "send the next request to replan unfinished work"
	case "recovery_error":
		return "Recovery", "inspect the saved task state before continuing"
	default:
		return "State", taskPaneState(status.State)
	}
}

func taskPaneFootnote(status chat.TaskRunStatus) string {
	switch status.State {
	case "complete":
		return "ready for delivery"
	case "interrupted":
		return "completed evidence remains saved"
	case "active":
		return "waiting for the next verified step"
	default:
		return "task state is available"
	}
}

func taskPaneGaps(status chat.TaskRunStatus) []string {
	gaps := make([]string, 0, min(len(status.Gaps), taskPaneMaxGapRows))
	for _, gap := range status.Gaps {
		if compact := taskPaneCompact(gap); compact != "" {
			gaps = append(gaps, compact)
		}
		if len(gaps) == taskPaneMaxGapRows {
			break
		}
	}
	return gaps
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
