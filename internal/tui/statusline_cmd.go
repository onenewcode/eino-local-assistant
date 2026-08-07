package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const statusLineCommandUsage = "usage: /statusline [show <field>|hide <field>|set <fields...>|reset]"

func (m *model) cmdStatusLine(arg string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(arg)))
	if len(fields) == 0 {
		m.appendLine(lineSystem, statusLineReport(m.deps.StatusLineFields))
		m.appendLine(lineSep, "")
		return m, nil
	}

	switch fields[0] {
	case "reset":
		if len(fields) != 1 {
			m.appendLine(lineError, statusLineCommandUsage)
			return m, nil
		}
		return m.saveStatusLineFields(defaultStatusLineFields)
	case "set":
		if len(fields) < 2 {
			m.appendLine(lineError, statusLineCommandUsage)
			return m, nil
		}
		selection, err := parseStatusLineFields(strings.TrimSpace(arg[len(fields[0]):]))
		if err != nil {
			m.appendLine(lineError, err.Error())
			return m, nil
		}
		return m.saveStatusLineFields(selection)
	case "show", "hide":
		if len(fields) != 2 {
			m.appendLine(lineError, statusLineCommandUsage)
			return m, nil
		}
		field := fields[1]
		if _, ok := statusLineFieldSet[field]; !ok {
			m.appendLine(lineError, "unknown status-line field: "+field)
			return m, nil
		}
		selection := append([]string(nil), m.deps.StatusLineFields...)
		if fields[0] == "show" {
			if containsStatusLineField(selection, field) {
				m.appendLine(lineSystem, "status-line field already shown: "+field)
				return m, nil
			}
			selection = append(selection, field)
		} else {
			selection = withoutStatusLineField(selection, field)
			if len(selection) == 0 {
				m.appendLine(lineError, "status line must retain at least one field")
				return m, nil
			}
		}
		return m.saveStatusLineFields(selection)
	default:
		m.appendLine(lineError, statusLineCommandUsage)
		return m, nil
	}
}

func parseStatusLineFields(raw string) ([]string, error) {
	parts := strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(parts) == 0 {
		return nil, errors.New(statusLineCommandUsage)
	}
	seen := make(map[string]struct{}, len(parts))
	for _, field := range parts {
		if _, ok := statusLineFieldSet[field]; !ok {
			return nil, errors.New("unknown status-line field: " + field)
		}
		if _, duplicate := seen[field]; duplicate {
			return nil, errors.New("duplicate status-line field: " + field)
		}
		seen[field] = struct{}{}
	}
	return parts, nil
}

func containsStatusLineField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

func withoutStatusLineField(fields []string, hidden string) []string {
	visible := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != hidden {
			visible = append(visible, field)
		}
	}
	return visible
}

func (m *model) saveStatusLineFields(fields []string) (tea.Model, tea.Cmd) {
	fields = normalizeStatusLineFields(fields)
	if m.deps.SaveStatusLineFields == nil {
		m.appendLine(lineError, "status-line persistence is unavailable in this TUI")
		return m, nil
	}
	if err := m.deps.SaveStatusLineFields(fields); err != nil {
		m.appendLine(lineError, "save status-line settings: "+err.Error())
		return m, nil
	}
	m.deps.StatusLineFields = append([]string(nil), fields...)
	m.appendLine(lineSystem, "status-line fields saved: "+strings.Join(fields, ", "))
	m.appendLine(lineSep, "")
	return m, nil
}

func statusLineReport(fields []string) string {
	return strings.Join([]string{
		"Status line fields (persistent): " + strings.Join(fields, ", "),
		"Available: model, effort, context, activity, session, title, policy, task, queue, follow",
		"Use /statusline show <field>, /statusline hide <field>, /statusline set <fields...>, or /statusline reset.",
	}, "\n")
}
