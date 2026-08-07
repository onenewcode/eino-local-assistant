package tui

import (
	"fmt"
	"strings"

	"eino-local-assistant/internal/chat"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wrap"
)

// taskStatusFingerprint identifies a display-safe plan snapshot. It excludes
// controller-only observations, so repeated lifecycle callbacks do not flood
// the transcript with identical cards.
func taskStatusFingerprint(status chat.TaskRunStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%t|%s|%s|%d|%d|%s", status.Available, status.State, status.Goal, status.DoneTasks, status.Tasks, strings.Join(status.ActiveTasks, ","))
	for _, item := range status.Items {
		fmt.Fprintf(&b, "\n%s|%s|%s", item.ID, item.Goal, item.State)
	}
	return b.String()
}

// renderUpdatedPlan follows the transcript-card convention used by coding
// agent CLIs: a complete, readable checklist replaces terse status prose.
func renderUpdatedPlan(width int, status chat.TaskRunStatus) string {
	width = max(20, width)
	var rows []string
	rows = append(rows, taskPlanTitleStyle.Render("• Updated Plan"))
	if len(status.Items) == 0 {
		rows = append(rows, taskPlanMutedStyle.Render("  no task nodes are available yet"))
		return strings.Join(rows, "\n")
	}
	for _, item := range status.Items {
		rows = append(rows, renderPlanItem(width, item)...)
	}
	return strings.Join(rows, "\n")
}

func renderPlanItem(width int, item chat.TaskListItem) []string {
	// Codex PlanUpdateCell: completed = ✔ struck, in_progress = emphasized □, pending = dim □.
	marker := "□"
	style := taskPlanPendingStyle
	switch item.State {
	case "done", "completed":
		marker = "✔"
		style = taskPlanDoneStyle
	case "working", "in_progress":
		style = taskPlanWorkingStyle
	}
	step := taskPaneCompact(item.Goal)
	if step == "" {
		step = taskPaneCompact(item.ID)
	}
	primary := fmt.Sprintf("  %s %s", marker, step)
	return renderPlanWrapped(primary, "    ", width, style)
}

func renderPlanWrapped(value, continuation string, width int, style lipgloss.Style) []string {
	value = taskPaneCompact(value)
	if value == "" {
		return nil
	}
	// Reserve the continuation indent before wrapping so every subsequent line
	// remains within the terminal width as well.
	wrapWidth := max(1, width-lipgloss.Width(continuation))
	lines := strings.Split(wrap.String(value, wrapWidth), "\n")
	for index, line := range lines {
		if index > 0 {
			lines[index] = continuation + line
		}
		lines[index] = style.Render(lines[index])
	}
	return lines
}
