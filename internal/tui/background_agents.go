package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	maxBackgroundAgents         = 4
	maxRetainedBackgroundAgents = 16
	maxBackgroundAgentResult    = 64 * 1024
)

type backgroundAgentState string

const (
	backgroundAgentWorking    backgroundAgentState = "working"
	backgroundAgentCancelling backgroundAgentState = "cancelling"
	backgroundAgentCompleted  backgroundAgentState = "completed"
	backgroundAgentFailed     backgroundAgentState = "failed"
	backgroundAgentCancelled  backgroundAgentState = "cancelled"
)

type backgroundAgentTask struct {
	id                string
	prompt            string
	sessionID         string
	sessionGeneration uint64
	startedAt         time.Time
	finishedAt        time.Time
	state             backgroundAgentState
	cancel            context.CancelFunc
	answer            string
	answerTruncated   bool
	failure           string
}

func (m *model) cmdAgent(prompt string) (tea.Model, tea.Cmd) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		m.appendLine(lineError, "usage: /agent <analysis task>")
		return m, nil
	}
	callback := m.deps.BackgroundAgent
	if callback == nil {
		m.appendLine(lineError, "background agent unavailable: callback is not configured")
		return m, nil
	}
	if m.activeBackgroundAgents() >= maxBackgroundAgents {
		m.appendLine(lineError, fmt.Sprintf("background agent limit reached (%d active); wait or /agents cancel <id>", maxBackgroundAgents))
		return m, nil
	}
	session, generation := m.activeSessionSnapshot()
	if session == nil {
		m.appendLine(lineError, "background agent unavailable: session is unavailable")
		return m, nil
	}

	ctx, cancel := m.backgroundAgentContext()
	id := m.nextBackgroundAgentID()
	task := &backgroundAgentTask{
		id:                id,
		prompt:            prompt,
		sessionID:         session.ID(),
		sessionGeneration: generation,
		startedAt:         time.Now(),
		state:             backgroundAgentWorking,
		cancel:            cancel,
	}
	if m.backgroundAgents == nil {
		m.backgroundAgents = make(map[string]*backgroundAgentTask)
	}
	m.backgroundAgents[id] = task
	m.backgroundAgentOrder = append(m.backgroundAgentOrder, id)
	m.trimBackgroundAgents()
	m.appendLine(lineSystem, fmt.Sprintf("background agent %s started (read-only, no tools): %s", id, backgroundAgentPreview(prompt)))
	m.appendLine(lineSep, "")

	return m, func() tea.Msg {
		answer, err := callback(ctx, session, prompt)
		return backgroundAgentDoneMsg{
			id:                id,
			sessionID:         session.ID(),
			sessionGeneration: generation,
			answer:            answer,
			err:               err,
		}
	}
}

func (m *model) cmdAgents(arg string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(strings.TrimSpace(arg))
	if len(fields) == 0 {
		m.appendLine(lineSystem, renderBackgroundAgents(m))
		m.appendLine(lineSep, "")
		return m, nil
	}
	if len(fields) != 2 {
		m.appendLine(lineError, "usage: /agents [show <id>|cancel <id>]")
		return m, nil
	}
	task := m.backgroundAgents[fields[1]]
	if task == nil {
		m.appendLine(lineError, fmt.Sprintf("background agent %q was not found", fields[1]))
		return m, nil
	}
	switch strings.ToLower(fields[0]) {
	case "show":
		m.appendLine(lineSystem, renderBackgroundAgent(task))
		m.appendLine(lineSep, "")
		return m, nil
	case "cancel":
		if !task.active() {
			m.appendLine(lineError, fmt.Sprintf("background agent %s is already %s", task.id, task.state))
			return m, nil
		}
		task.state = backgroundAgentCancelling
		if task.cancel != nil {
			task.cancel()
		}
		m.appendLine(lineSystem, fmt.Sprintf("background agent %s cancellation requested", task.id))
		m.appendLine(lineSep, "")
		return m, nil
	default:
		m.appendLine(lineError, "usage: /agents [show <id>|cancel <id>]")
		return m, nil
	}
}

func (m *model) finishBackgroundAgent(msg backgroundAgentDoneMsg) {
	task := m.backgroundAgents[msg.id]
	// Commands are process-local, but bind completion to the task snapshot so a
	// stale or malformed message can never settle a differently sourced task.
	if task == nil || !task.active() || msg.sessionID != task.sessionID || msg.sessionGeneration != task.sessionGeneration {
		return
	}
	if task.cancel != nil {
		task.cancel()
		task.cancel = nil
	}
	task.finishedAt = time.Now()
	if task.state == backgroundAgentCancelling {
		task.state = backgroundAgentCancelled
		task.answer = ""
		task.failure = ""
	} else if msg.err != nil {
		task.state = backgroundAgentFailed
		task.failure = sanitizeSkillsError(msg.err)
	} else if strings.TrimSpace(msg.answer) == "" {
		task.state = backgroundAgentFailed
		task.failure = "empty analysis result"
	} else {
		payload := sanitizeDiffPayload(msg.answer, maxBackgroundAgentResult)
		task.state = backgroundAgentCompleted
		task.answer = payload.text
		task.answerTruncated = payload.truncated
	}
	m.trimBackgroundAgents()

	current, generation := m.activeSessionSnapshot()
	if current == nil || current.ID() != task.sessionID || generation != task.sessionGeneration {
		return
	}
	switch task.state {
	case backgroundAgentCompleted:
		m.appendSideLine(fmt.Sprintf("[%s] completed; use /agents show %s to inspect the display-only result", task.id, task.id))
	case backgroundAgentFailed:
		m.appendSideLine(fmt.Sprintf("[%s] failed: %s", task.id, task.failure))
	case backgroundAgentCancelled:
		m.appendSideLine(fmt.Sprintf("[%s] cancelled", task.id))
	}
}

func (m *model) backgroundAgentContext() (context.Context, context.CancelFunc) {
	parent := m.processCtx()
	if timeout := m.deps.TurnOptions.Timeout; timeout > 0 {
		return context.WithTimeout(parent, timeout)
	}
	return context.WithCancel(parent)
}

func (m *model) nextBackgroundAgentID() string {
	m.backgroundAgentNextID++
	if m.backgroundAgentNextID == 0 {
		m.backgroundAgentNextID++
	}
	return fmt.Sprintf("agent-%d", m.backgroundAgentNextID)
}

func (m *model) activeBackgroundAgents() int {
	active := 0
	for _, task := range m.backgroundAgents {
		if task != nil && task.active() {
			active++
		}
	}
	return active
}

func (m *model) cancelBackgroundAgents() {
	for _, task := range m.backgroundAgents {
		if task == nil || !task.active() {
			continue
		}
		task.state = backgroundAgentCancelling
		if task.cancel != nil {
			task.cancel()
		}
	}
}

func (m *model) trimBackgroundAgents() {
	for len(m.backgroundAgents) > maxRetainedBackgroundAgents {
		removed := false
		for index, id := range m.backgroundAgentOrder {
			task := m.backgroundAgents[id]
			if task == nil || task.active() {
				continue
			}
			delete(m.backgroundAgents, id)
			m.backgroundAgentOrder = append(m.backgroundAgentOrder[:index], m.backgroundAgentOrder[index+1:]...)
			removed = true
			break
		}
		if !removed {
			return
		}
	}
}

func (task *backgroundAgentTask) active() bool {
	return task != nil && (task.state == backgroundAgentWorking || task.state == backgroundAgentCancelling)
}

func renderBackgroundAgents(m *model) string {
	if m == nil || m.deps.BackgroundAgent == nil {
		return "Background agents\n  unavailable: no background analysis runtime is configured"
	}
	if len(m.backgroundAgents) == 0 {
		return "Background agents (0)\n  (none; use /agent <analysis task> to start one)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Background agents (%d; %d active, limit %d)\n", len(m.backgroundAgents), m.activeBackgroundAgents(), maxBackgroundAgents)
	for _, id := range m.backgroundAgentOrder {
		task := m.backgroundAgents[id]
		if task == nil {
			continue
		}
		fmt.Fprintf(&b, "  %s [%s] %s\n", task.id, task.state, backgroundAgentPreview(task.prompt))
	}
	b.WriteString("Use /agents show <id> to inspect a finished result, or /agents cancel <id> to stop an active agent.")
	return strings.TrimRight(b.String(), "\n")
}

func renderBackgroundAgent(task *backgroundAgentTask) string {
	if task == nil {
		return "Background agent\n  unavailable"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Background agent: %s\nState: %s\nTask: %s\n", task.id, task.state, backgroundAgentPreview(task.prompt))
	if task.active() {
		b.WriteString("Result: not available until the agent reaches a terminal state")
		return b.String()
	}
	switch task.state {
	case backgroundAgentCompleted:
		b.WriteString("Result (display-only; not sent to the parent model):\n\n")
		b.WriteString(task.answer)
		if task.answerTruncated {
			b.WriteString("\n\nNotice: result truncated after 65536 bytes.")
		}
	case backgroundAgentFailed:
		b.WriteString("Failure: ")
		b.WriteString(task.failure)
	case backgroundAgentCancelled:
		b.WriteString("Result: cancelled before a result was accepted")
	}
	return strings.TrimRight(b.String(), "\n")
}

func backgroundAgentPreview(prompt string) string {
	return taskPaneTruncate(prompt, 120)
}
