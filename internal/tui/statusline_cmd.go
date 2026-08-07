package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const statusLineCommandUsage = "usage: /statusline"

// cmdStatusLine opens the same persistent picker used by Codex. Its state is
// deliberately not parsed from shell-like arguments: the searchable list
// exposes all supported fields and shows the result before it is committed.
func (m *model) cmdStatusLine(arg string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(arg) != "" {
		m.appendLine(lineError, statusLineCommandUsage)
		return m, nil
	}
	return m.openStatusLinePicker()
}
