package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const reviewCommandMaxBytes = 128 * 1024

func (m *model) cmdReview(arg string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(arg) != "" {
		m.appendLine(lineError, "usage: /review")
		return m, nil
	}
	if m.mode != modeIdle {
		m.appendLine(lineError, "review unavailable: finish the current operation first")
		return m, nil
	}
	if m.hasPendingApproval() {
		m.appendLine(lineError, "review unavailable: resolve the pending approval first")
		return m, nil
	}
	if m.sideQuestions > 0 {
		m.appendLine(lineError, "review unavailable: wait for the side question to finish first")
		return m, nil
	}
	if m.reviewInFlight {
		m.appendLine(lineError, "review already running")
		return m, nil
	}
	if m.deps.WorkspaceDiff == nil {
		m.appendLine(lineError, "review unavailable: workspace diff callback is not configured")
		return m, nil
	}
	if m.deps.WorkspaceReview == nil {
		m.appendLine(lineError, "review unavailable: callback is not configured")
		return m, nil
	}
	session, generation := m.activeSessionSnapshot()
	if session == nil {
		m.appendLine(lineError, "review unavailable: session is unavailable")
		return m, nil
	}
	m.reviewNextID++
	requestID := m.reviewNextID
	m.reviewInFlight = true
	ctx := m.processCtx()
	return m, func() tea.Msg {
		diff, err := m.deps.WorkspaceDiff(ctx)
		if err != nil {
			return reviewDoneMsg{requestID: requestID, sessionID: session.ID(), sessionGeneration: generation, err: err}
		}
		if strings.TrimSpace(diff) == "" {
			return reviewDoneMsg{requestID: requestID, sessionID: session.ID(), sessionGeneration: generation, answer: "No workspace changes to review"}
		}
		answer, err := m.deps.WorkspaceReview(ctx, diff)
		return reviewDoneMsg{requestID: requestID, sessionID: session.ID(), sessionGeneration: generation, answer: answer, err: err}
	}
}

func (m *model) finishReview(msg reviewDoneMsg) {
	if msg.err != nil {
		m.appendLine(lineError, "review error: "+sanitizeDiffError(msg.err))
		return
	}
	payload := sanitizeDiffPayload(strings.TrimSpace(msg.answer), reviewCommandMaxBytes)
	if payload.text == "" {
		m.appendLine(lineError, "review error: empty result")
		return
	}
	m.appendLine(lineReview, "Review\n"+payload.text)
	if payload.truncated {
		m.appendLine(lineSystem, fmt.Sprintf("review output truncated after %d bytes", reviewCommandMaxBytes))
	}
}
