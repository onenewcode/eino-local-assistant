package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wrap"
)

// Quiet dark palette — closer to Claude Code / Codex than bright accent chrome.
// Avoid filled status bars and neon borders; keep structure with dim rules.
var (
	userStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("117")).
			Bold(true)

	assistantStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

	// reasoningStyle is deliberately weaker than assistant body text.
	reasoningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Italic(true)

	toolStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("180"))

	toolBodyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("203")).
			Bold(true)

	systemStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("114"))

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("238"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("242"))

	// Soft gray border — not a "selected field" blue/purple highlight.
	composerBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1)
)

func renderUser(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return userStyle.Render("› ")
	}
	if len(lines) == 1 {
		return userStyle.Render("› ") + text
	}
	var b strings.Builder
	b.WriteString(userStyle.Render("› "))
	b.WriteString(lines[0])
	for _, line := range lines[1:] {
		b.WriteByte('\n')
		b.WriteString("  ")
		b.WriteString(line)
	}
	return b.String()
}

func renderError(text string, width int) string {
	prefix := "! "
	indent := "  "
	bodyWidth := width - lipgloss.Width(prefix)
	if bodyWidth < 16 {
		bodyWidth = 16
	}
	// Hard-wrap long tokens so provider errors (often one long line) fit the terminal.
	wrapped := wrap.String(strings.ReplaceAll(text, "\t", " "), bodyWidth)
	lines := strings.Split(wrapped, "\n")
	if len(lines) == 0 {
		return errorStyle.Render(prefix)
	}
	var b strings.Builder
	b.WriteString(errorStyle.Render(prefix))
	b.WriteString(lines[0])
	for _, line := range lines[1:] {
		b.WriteByte('\n')
		b.WriteString(indent)
		b.WriteString(line)
	}
	return b.String()
}

func renderSystem(text string) string {
	return systemStyle.Render("· ") + text
}

// renderReasoning draws model reasoning summary (display-only).
// Folded blocks are a single dim summary line; open/streaming blocks show body.
func renderReasoning(text string, folded, streaming bool) string {
	if folded {
		return reasoningStyle.Render("·· " + text)
	}
	prefix := "thinking"
	if streaming {
		prefix = "thinking…"
	}
	body := strings.TrimRight(text, "\n")
	if body == "" {
		return reasoningStyle.Render(prefix)
	}
	lines := strings.Split(body, "\n")
	var b strings.Builder
	b.WriteString(reasoningStyle.Render(prefix))
	b.WriteByte('\n')
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(reasoningStyle.Render("  " + line))
	}
	return b.String()
}

func renderSeparator(width int) string {
	if width < 8 {
		width = 8
	}
	return separatorStyle.Render(strings.Repeat("─", width))
}

// renderStatusBar is a quiet single line (no reverse-video strip).
func renderStatusBar(width int, label string) string {
	if width < 8 {
		width = 8
	}
	// Dim rule above status to separate transcript from chrome, Claude-style.
	rule := separatorStyle.Render(strings.Repeat("─", width))
	line := statusStyle.Width(width).MaxWidth(width).Render(label)
	return rule + "\n" + line
}

func renderComposer(width int, view string) string {
	if width < 12 {
		width = 12
	}
	// Outer width ≈ terminal width; leave a small margin so the box doesn't clip.
	return composerBorder.Width(width - 2).MaxWidth(width).Render(view)
}

var (
	slashMenuNameStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("114")).
				Bold(true)
	slashMenuDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))
	slashMenuSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Background(lipgloss.Color("236")).
				Bold(true)
	slashMenuSelectedDescStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("250")).
					Background(lipgloss.Color("236"))
	slashMenuCursorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("114")).
				Bold(true)

	taskPaneTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("114")).
				Bold(true)
	taskPaneLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))
	taskPaneValueStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))
	taskPaneGapStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("203"))
)

// renderSlashMenu paints the prefix-filtered command list above the composer.
// selected is clamped by the caller; out-of-range values are treated as 0.
func renderSlashMenu(width int, items []slashCommand, selected int) string {
	if len(items) == 0 {
		return ""
	}
	if width < 20 {
		width = 20
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= len(items) {
		selected = len(items) - 1
	}

	// Visible window follows the selection when the list is longer than the cap.
	start, end := slashMenuWindow(len(items), selected, maxSlashMenuRows)
	var b strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			b.WriteByte('\n')
		}
		item := items[i]
		cursor := "  "
		nameStyle := slashMenuNameStyle
		descStyle := slashMenuDescStyle
		if i == selected {
			cursor = slashMenuCursorStyle.Render("› ")
			nameStyle = slashMenuSelectedStyle
			descStyle = slashMenuSelectedDescStyle
		}
		name := nameStyle.Render(item.Name)
		// Leave room for cursor + name + gap.
		descBudget := max(0, width-lipgloss.Width(item.Name)-4)
		desc := item.Description
		if descBudget < 8 {
			desc = ""
		} else if lipgloss.Width(desc) > descBudget {
			runes := []rune(desc)
			if descBudget > 1 && len(runes) > descBudget-1 {
				desc = string(runes[:descBudget-1]) + "…"
			}
		}
		line := cursor + name
		if desc != "" {
			line += "  " + descStyle.Render(desc)
		}
		// Pad selected row background across the menu width.
		if i == selected {
			pad := width - lipgloss.Width(line)
			if pad > 0 {
				line += slashMenuSelectedDescStyle.Render(strings.Repeat(" ", pad))
			}
		}
		b.WriteString(line)
	}
	return b.String()
}

// slashMenuWindow returns [start,end) for a capped list centered on selected.
func slashMenuWindow(n, selected, maxRows int) (start, end int) {
	if n <= 0 {
		return 0, 0
	}
	if maxRows <= 0 || n <= maxRows {
		return 0, n
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= n {
		selected = n - 1
	}
	start = selected - maxRows/2
	if start < 0 {
		start = 0
	}
	end = start + maxRows
	if end > n {
		end = n
		start = end - maxRows
	}
	return start, end
}
