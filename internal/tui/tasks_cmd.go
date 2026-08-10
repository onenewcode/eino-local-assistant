package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	tasksCommandUsage        = "usage: /tasks"
	tasksCommandMaxToolWidth = 80
)

// cmdTasks reports the TUI's bounded foreground/resource boundary. It never
// asks the model, invokes a tool, or changes the active turn or queue.
func (m *model) cmdTasks(arg string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(arg) != "" {
		m.appendLine(lineError, tasksCommandUsage)
		return m, nil
	}

	m.appendLine(lineSystem, renderTasksCommand(m))
	m.appendLine(lineSep, "")
	return m, nil
}

func renderTasksCommand(m *model) string {
	if m == nil {
		return "Tasks\n  Foreground turn: idle\n  Current tool: none\n  Queued follow-ups: 0\n  Goal/checklist: unavailable via /goal\n  Background resources: unavailable (no background shell/subagent runtime)"
	}

	tool := taskPaneCompact(m.currentTool)
	if tool == "" && m.pendingApproval != nil {
		tool = taskPaneCompact(m.pendingApproval.Request.Tool)
	}
	if tool == "" {
		tool = "none"
	} else {
		tool = taskPaneTruncate(tool, tasksCommandMaxToolWidth)
	}

	goalChecklist := "unavailable via /goal"
	if hasTaskStatus(sessionTaskStatus(m.activeSession())) {
		goalChecklist = "available via /goal and Ctrl+T"
	}

	var b strings.Builder
	b.WriteString("Tasks\n")
	fmt.Fprintf(&b, "  Foreground turn: %s\n", tasksForegroundState(m))
	fmt.Fprintf(&b, "  Current tool: %s\n", tool)
	fmt.Fprintf(&b, "  Queued follow-ups: %d\n", len(m.queue))
	fmt.Fprintf(&b, "  Goal/checklist: %s\n", goalChecklist)
	b.WriteString("  ")
	b.WriteString(backgroundAgentTaskSummary(m))
	return b.String()
}

func backgroundAgentTaskSummary(m *model) string {
	if m == nil || m.deps.BackgroundAgent == nil {
		return "Background resources: unavailable (no background shell/subagent runtime)"
	}
	return fmt.Sprintf("Background analysis agents: %d active, %d retained (limit %d; manage with /agents)", m.activeBackgroundAgents(), len(m.backgroundAgents), maxBackgroundAgents)
}

func tasksForegroundState(m *model) string {
	if m == nil {
		return "idle"
	}
	if m.hasPendingApproval() {
		return "awaiting approval"
	}
	if m.mode == modeCompacting {
		return "compacting"
	}
	if m.mode == modeBusy {
		if m.interruptFeedbackShown {
			return "stopping"
		}
		return "working"
	}
	return "idle"
}
