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

// cmdGoal reports only the session's UI-safe task projection. It never asks
// the model or consults the completion gate.
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
	fmt.Fprintf(&b, "  Scope: requirements=%d scenarios=%d tasks=%d\n", status.Requirements, status.Scenarios, status.Tasks)
	fmt.Fprintf(&b, "  Progress: done=%d/%d\n", status.DoneTasks, status.Tasks)
	fmt.Fprintf(&b, "  Active task: %s\n", goalCommandValue(status.ActiveTask, "none"))
	fmt.Fprintf(&b, "  PlanRequired: %t\n", status.PlanRequired)

	gaps := taskPaneGaps(status)
	if len(gaps) == 0 {
		b.WriteString("  Gaps: none")
		return b.String()
	}
	b.WriteString("  Gaps:\n")
	for _, gap := range gaps {
		fmt.Fprintf(&b, "    - %s\n", taskPaneTruncate(gap, goalCommandMaxValueWidth))
	}
	return strings.TrimRight(b.String(), "\n")
}

func goalCommandValue(value, fallback string) string {
	if compact := taskPaneCompact(value); compact != "" {
		return taskPaneTruncate(compact, goalCommandMaxValueWidth)
	}
	return fallback
}
