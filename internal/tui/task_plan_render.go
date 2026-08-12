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
	fmt.Fprintf(&b, "%t|%s|%s|%d|%d|%d|%d|%t|%s|%s", status.Available, status.State, status.Goal, status.Requirements, status.Scenarios, status.DoneTasks, status.Tasks, status.PlanRequired, strings.Join(status.ActiveTasks, ","), strings.Join(status.Gaps, "\x1f"))
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
	rows = append(rows, renderTaskPlanSummary(width, status)...)
	if len(status.Items) == 0 {
		rows = append(rows, taskPlanMutedStyle.Render("  no task nodes are available yet"))
		return strings.Join(rows, "\n")
	}
	for _, item := range status.Items {
		rows = append(rows, renderPlanItem(width, item)...)
	}
	return strings.Join(rows, "\n")
}

// renderTaskPlanSummary keeps the run's state and actionable next step visible
// even before the controller has materialized a DAG node or when every node is
// otherwise unchanged. The gap is already a display-safe controller message.
func renderTaskPlanSummary(width int, status chat.TaskRunStatus) []string {
	state := taskPaneState(status.State)
	if status.Tasks > 0 {
		state = fmt.Sprintf("%s · %d/%d", state, status.DoneTasks, status.Tasks)
	}
	rows := renderPlanWrapped("  State: "+state, "    ", width, taskPlanMutedStyle)
	if gap := taskPaneGaps(status); len(gap) > 0 {
		rows = append(rows, renderPlanWrapped("  Next: "+gap[0], "       ", width, taskPlanBlockedStyle)...)
	}
	return rows
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
