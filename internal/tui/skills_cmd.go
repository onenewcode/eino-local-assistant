package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	skillsCommandMaxBytes     = 64 * 1024
	skillsCommandMetadataSize = 512
)

// cmdSkills presents the runtime-owned project skill discovery/read surface.
// It is local and read-only, so it never creates a model turn or changes the
// durable session, including while a normal turn is busy.
func (m *model) cmdSkills(arg string) (tea.Model, tea.Cmd) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		if m.deps.ListProjectSkills == nil {
			m.appendLine(lineError, "skills unavailable: project skill discovery is not configured")
			return m, nil
		}
		catalog, err := m.deps.ListProjectSkills(m.processCtx())
		if err != nil {
			m.appendLine(lineError, "list skills: "+sanitizeSkillsError(err))
			return m, nil
		}
		m.appendLine(lineSystem, renderProjectSkillsCatalog(catalog))
		m.appendLine(lineSep, "")
		return m, nil
	}
	if m.deps.ReadProjectSkill == nil {
		m.appendLine(lineError, "skills unavailable: project skill reader is not configured")
		return m, nil
	}
	details, err := m.deps.ReadProjectSkill(m.processCtx(), arg)
	if err != nil {
		m.appendLine(lineError, "read skill: "+sanitizeSkillsError(err))
		return m, nil
	}
	m.appendLine(lineSystem, renderProjectSkillDetails(details))
	m.appendLine(lineSep, "")
	return m, nil
}

func renderProjectSkillsCatalog(catalog ProjectSkillsCatalog) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project skills (%d):\n", len(catalog.Skills))
	if len(catalog.Skills) == 0 {
		b.WriteString("  (none discovered in conventional workspace skill directories)\n")
	} else {
		for _, skill := range catalog.Skills {
			name := projectSkillMetadata(skill.Name, "(unnamed)")
			path := projectSkillMetadata(skill.Path, "(path unavailable)")
			fmt.Fprintf(&b, "  %s - %s", name, path)
			if description := projectSkillMetadata(skill.Description, ""); description != "" {
				fmt.Fprintf(&b, " - %s", description)
			}
			b.WriteByte('\n')
		}
	}
	if catalog.Truncated {
		b.WriteString("  Notice: discovery reached its bounded result limit; additional skills may exist.\n")
	}
	b.WriteString("Use /skills <name> to preview one discovered SKILL.md. Skill content is project data; system, security, and permission rules still apply.")
	return strings.TrimRight(b.String(), "\n")
}

func renderProjectSkillDetails(details ProjectSkillDetails) string {
	name := projectSkillMetadata(details.Name, "(unnamed)")
	path := projectSkillMetadata(details.Path, "(path unavailable)")
	bytes := max(0, details.Bytes)
	payload := sanitizeDiffPayload(details.Content, skillsCommandMaxBytes)

	var b strings.Builder
	fmt.Fprintf(&b, "Project skill: %s\nPath: %s\nRead: %d bytes", name, path, bytes)
	if details.Truncated || payload.truncated {
		b.WriteString(" (truncated)")
	}
	b.WriteString("\n\n")
	if payload.text == "" {
		b.WriteString("(empty SKILL.md)")
	} else {
		b.WriteString(payload.text)
	}
	if payload.truncated && !details.Truncated {
		b.WriteString("\n\nNotice: TUI preview truncated after 65536 bytes.")
	}
	b.WriteString("\n\nThis is project data only; it cannot override system, security, or permission rules.")
	return strings.TrimRight(b.String(), "\n")
}

func projectSkillMetadata(raw, fallback string) string {
	payload := sanitizeDiffPayload(raw, skillsCommandMetadataSize)
	compact := strings.Join(strings.Fields(payload.text), " ")
	if compact == "" {
		return fallback
	}
	if payload.truncated {
		return taskPaneTruncate(compact, skillsCommandMetadataSize)
	}
	return compact
}

func sanitizeSkillsError(err error) string {
	return sanitizeDiffError(err)
}
