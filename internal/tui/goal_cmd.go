package tui

import (
	"fmt"
	"strings"

	"eino-local-assistant/internal/chat"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	goalCommandUsage         = "usage: /goal"
	goalCommandMaxValueWidth = 160
)

// cmdGoal reports only the session's UI-safe plan checklist.
func (m *model) cmdGoal(arg string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(arg) != "" {
		m.appendLine(lineError, goalCommandUsage)
		return m, nil
	}

	status := sessionTaskStatus(m.activeSession())
	m.appendLine(lineSystem, renderGoalCommand(status))
	m.appendLine(lineSep, "")
	return m, nil
}

func renderGoalCommand(status chat.TaskRunStatus) string {
	if !hasTaskStatus(status) {
		return "Goal\n  unavailable: no autonomous task runtime is available for this session"
	}

	var b strings.Builder
	b.WriteString("Goal\n")
	fmt.Fprintf(&b, "  Goal: %s\n", goalCommandValue(taskPaneGoal(status), "task goal was not recorded"))
	fmt.Fprintf(&b, "  State: %s\n", goalCommandValue(taskPaneState(status.State), "unavailable"))
	fmt.Fprintf(&b, "  Progress: done=%d/%d\n", status.DoneTasks, status.Tasks)
	activeTasks := "none"
	if len(status.ActiveTasks) > 0 {
		activeTasks = strings.Join(status.ActiveTasks, ", ")
	}
	fmt.Fprintf(&b, "  Active: %s\n", goalCommandValue(activeTasks, "none"))
	if len(status.Items) > 0 {
		b.WriteString("  Steps:\n")
		for _, item := range status.Items {
			fmt.Fprintf(&b, "    [%s] %s\n", goalCommandValue(taskPaneState(item.State), "unknown"), goalCommandValue(item.Goal, "step text was not recorded"))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func goalCommandValue(value, fallback string) string {
	if compact := taskPaneCompact(value); compact != "" {
		return taskPaneTruncate(compact, goalCommandMaxValueWidth)
	}
	return fallback
}
