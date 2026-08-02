package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Run starts the Bubble Tea UI until the user quits or ctx is canceled.
// sessionID is the active session at exit (for a Codex-style resume hint after
// the alt screen is torn down).
func Run(ctx context.Context, deps Deps) (sessionID string, err error) {
	if deps.Session == nil {
		return "", fmt.Errorf("tui requires a session")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	deps.Ctx = ctx

	p := tea.NewProgram(
		newModel(deps),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithContext(ctx),
	)
	final, err := p.Run()
	return sessionIDAtExit(final, deps), err
}

// sessionIDAtExit prefers the live model session (may have switched via /new
// or /resume) and falls back to the deps session if the final model is missing.
func sessionIDAtExit(final tea.Model, deps Deps) string {
	if m, ok := final.(*model); ok && m != nil && m.deps.Session != nil {
		return m.deps.Session.ID()
	}
	if deps.Session != nil {
		return deps.Session.ID()
	}
	return ""
}

// FormatResumeHint builds the one-line command printed after TUI exit so the
// user can re-enter the session from the shell scrollback (Codex-style).
func FormatResumeHint(appName, sessionID string) string {
	appName = strings.TrimSpace(appName)
	sessionID = strings.TrimSpace(sessionID)
	if appName == "" || sessionID == "" {
		return ""
	}
	return appName + " resume " + sessionID
}
