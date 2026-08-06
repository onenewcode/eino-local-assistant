package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const maxModelPickerRows = 6

func isModelPickerToggleKey(msg tea.KeyMsg) bool {
	return msg.Type == tea.KeyRunes && msg.Alt && len(msg.Runes) == 1 && strings.EqualFold(string(msg.Runes), "p")
}

func (m *model) modelPickerOpen() bool {
	return len(m.modelPickerItems) > 0
}

func (m *model) openModelPicker() (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "model picker unavailable while busy; retry when idle")
		return m, nil
	}
	if m.hasPendingApproval() {
		m.appendLine(lineError, "model picker unavailable while approval is pending; retry after it is resolved")
		return m, nil
	}
	if m.sideQuestions > 0 {
		m.appendLine(lineError, "model picker unavailable while a side question is running; retry after it finishes")
		return m, nil
	}
	if len(m.deps.ModelCatalog) == 0 {
		m.appendLine(lineError, "model picker unavailable: no configured catalog; use /model <name>")
		return m, nil
	}
	if m.deps.SwitchModel == nil {
		m.appendLine(lineError, "model picker unavailable: runtime callback is not configured")
		return m, nil
	}
	items := copyModelCatalogEntries(m.deps.ModelCatalog)
	m.modelPickerItems = items
	m.modelPickerSel = modelPickerSelection(items, m.currentModelIdentity())
	m.clearSlashMenu()
	m.clearBacktrack()
	m.layout()
	m.refreshViewport()
	return m, nil
}

func (m *model) closeModelPicker() {
	m.modelPickerItems = nil
	m.modelPickerSel = 0
	m.layout()
	m.refreshViewport()
}

func (m *model) handleModelPickerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.modelPickerOpen() {
		return m, nil
	}
	switch {
	case msg.Type == tea.KeyEsc || isModelPickerToggleKey(msg):
		m.closeModelPicker()
		return m, nil
	case msg.Type == tea.KeyUp || modelPickerKeyRune(msg, 'k'):
		m.modelPickerSel = moveModelPickerSelection(m.modelPickerItems, m.modelPickerSel, -1)
		m.refreshViewport()
		return m, nil
	case msg.Type == tea.KeyDown || modelPickerKeyRune(msg, 'j'):
		m.modelPickerSel = moveModelPickerSelection(m.modelPickerItems, m.modelPickerSel, 1)
		m.refreshViewport()
		return m, nil
	case msg.Type == tea.KeyEnter:
		entry, ok := selectedModelPickerEntry(m.modelPickerItems, m.modelPickerSel)
		if !ok {
			m.appendLine(lineError, "model picker: no selectable model is configured")
			return m, nil
		}
		return m.applyModelName(entry.CanonicalName)
	default:
		return m, nil
	}
}

func modelPickerKeyRune(msg tea.KeyMsg, want rune) bool {
	return msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && !msg.Alt && msg.Runes[0] == want
}

func copyModelCatalogEntries(entries []ModelCatalogEntry) []ModelCatalogEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]ModelCatalogEntry, len(entries))
	copy(out, entries)
	for i := range out {
		out[i].CanonicalName = strings.TrimSpace(out[i].CanonicalName)
		out[i].DisplayName = strings.TrimSpace(out[i].DisplayName)
		if out[i].DisplayName == "" {
			out[i].DisplayName = out[i].CanonicalName
		}
		out[i].Lifecycle = strings.ToLower(strings.TrimSpace(out[i].Lifecycle))
		if out[i].Lifecycle == "" {
			out[i].Lifecycle = "active"
		}
		out[i].Provenance = strings.TrimSpace(out[i].Provenance)
		out[i].Description = strings.TrimSpace(out[i].Description)
		out[i].Aliases = append([]string(nil), out[i].Aliases...)
		out[i].Capabilities.ReasoningEfforts = append([]string(nil), out[i].Capabilities.ReasoningEfforts...)
		out[i].Capabilities.InputModalities = append([]string(nil), out[i].Capabilities.InputModalities...)
	}
	return out
}

func modelPickerSelection(entries []ModelCatalogEntry, current string) int {
	for i, entry := range entries {
		if !modelPickerEntrySelectable(entry) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(entry.CanonicalName), strings.TrimSpace(current)) {
			return i
		}
	}
	for i, entry := range entries {
		if modelPickerEntrySelectable(entry) {
			return i
		}
	}
	return 0
}

func moveModelPickerSelection(entries []ModelCatalogEntry, selected, delta int) int {
	if len(entries) == 0 || delta == 0 {
		return selected
	}
	if selected < 0 || selected >= len(entries) {
		selected = modelPickerSelection(entries, "")
	}
	for steps := 0; steps < len(entries); steps++ {
		selected += delta
		if selected < 0 {
			selected = len(entries) - 1
		}
		if selected >= len(entries) {
			selected = 0
		}
		if modelPickerEntrySelectable(entries[selected]) {
			return selected
		}
	}
	return selected
}

func selectedModelPickerEntry(entries []ModelCatalogEntry, selected int) (ModelCatalogEntry, bool) {
	if selected < 0 || selected >= len(entries) || !modelPickerEntrySelectable(entries[selected]) {
		return ModelCatalogEntry{}, false
	}
	return entries[selected], true
}

func modelPickerEntrySelectable(entry ModelCatalogEntry) bool {
	return strings.ToLower(strings.TrimSpace(entry.Lifecycle)) != "retired" && strings.TrimSpace(entry.CanonicalName) != ""
}

func (m *model) applyModelName(name string) (tea.Model, tea.Cmd) {
	if !m.modelSwitchAvailable() {
		return m, nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		m.appendLine(lineError, "usage: /model <name>")
		return m, nil
	}
	session, _ := m.activeSessionSnapshot()
	if session == nil {
		m.appendLine(lineError, "model switch failed: session is unavailable")
		return m, nil
	}
	if m.deps.SwitchModel == nil {
		m.appendLine(lineError, "model switch unavailable: runtime callback is not configured")
		return m, nil
	}
	result, err := m.deps.SwitchModel(m.processCtx(), session, name)
	if err != nil {
		m.appendLine(lineError, "model switch failed: "+err.Error())
		return m, nil
	}
	// The callback commits the same Session pointer. Only replace the display
	// and future-session snapshot; no session-generation or transient reset is
	// allowed for an in-place model change.
	m.deps.Status = result.Status
	m.deps.SessionOpts = result.SessionOpts
	if m.modelPickerOpen() {
		m.closeModelPicker()
	}
	displayName := strings.TrimSpace(result.Status.ModelDisplayName)
	if displayName == "" {
		displayName = modelCatalogDisplayName(m.deps.ModelCatalog, name)
	}
	canonical := strings.TrimSpace(result.Status.Model)
	if canonical == "" {
		canonical = strings.TrimSpace(result.SessionOpts.ModelName)
	}
	if displayName == "" {
		displayName = canonical
	}
	if displayName != "" && canonical != "" && !strings.EqualFold(displayName, canonical) {
		displayName += " (" + canonical + ")"
	}
	if displayName == "" {
		displayName = "provider default"
	}
	m.appendLine(lineSystem, "model switched to "+displayName)
	m.appendLine(lineSep, "")
	return m, nil
}

func (m *model) modelSwitchAvailable() bool {
	if m.mode != modeIdle {
		m.appendLine(lineError, "model switch unavailable while busy; retry when idle")
		return false
	}
	if m.hasPendingApproval() {
		m.appendLine(lineError, "model switch unavailable while approval is pending; retry after it is resolved")
		return false
	}
	if m.sideQuestions > 0 {
		m.appendLine(lineError, "model switch unavailable while a side question is running; retry after it finishes")
		return false
	}
	return true
}

func modelCatalogDisplayName(entries []ModelCatalogEntry, canonical string) string {
	canonical = strings.TrimSpace(canonical)
	for _, entry := range entries {
		if strings.EqualFold(strings.TrimSpace(entry.CanonicalName), canonical) {
			label := strings.TrimSpace(entry.DisplayName)
			if label != "" && !strings.EqualFold(label, canonical) {
				return label
			}
		}
	}
	return ""
}

func modelPickerVisibleRange(entries []ModelCatalogEntry, selected int) (int, int) {
	if len(entries) <= maxModelPickerRows {
		return 0, len(entries)
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= len(entries) {
		selected = len(entries) - 1
	}
	start := selected - maxModelPickerRows/2
	if start < 0 {
		start = 0
	}
	if start+maxModelPickerRows > len(entries) {
		start = len(entries) - maxModelPickerRows
	}
	return start, start + maxModelPickerRows
}

func renderModelPicker(width int, entries []ModelCatalogEntry, selected int, current string) string {
	if len(entries) == 0 {
		return ""
	}
	if width < 32 {
		width = 32
	}
	if selected < 0 || selected >= len(entries) {
		selected = 0
	}
	start, end := modelPickerVisibleRange(entries, selected)
	lines := []string{modelPickerTitleStyle.Render(fmt.Sprintf("Models · configured catalog (%d/%d)", selected+1, len(entries)))}
	for i := start; i < end; i++ {
		entry := entries[i]
		label := strings.TrimSpace(entry.DisplayName)
		if label == "" {
			label = entry.CanonicalName
		}
		if entry.Lifecycle == "deprecated" {
			label += " [deprecated]"
		}
		if entry.Lifecycle == "retired" {
			label += " [retired]"
		}
		marker := "  "
		if i == selected && modelPickerEntrySelectable(entry) {
			marker = "> "
		}
		currentMark := ""
		if strings.EqualFold(strings.TrimSpace(entry.CanonicalName), strings.TrimSpace(current)) {
			currentMark = "  current"
		}
		labelLine := marker + label
		if entry.CanonicalName != "" && !strings.EqualFold(label, entry.CanonicalName) {
			labelLine += "  " + entry.CanonicalName
		}
		labelLine += currentMark
		labelLine = truncateModelPickerText(labelLine, width-2)
		if i == selected && modelPickerEntrySelectable(entry) {
			lines = append(lines, modelPickerSelectedStyle.Render(labelLine))
		} else if !modelPickerEntrySelectable(entry) {
			lines = append(lines, modelPickerDisabledStyle.Render(labelLine))
		} else {
			lines = append(lines, modelPickerRowStyle.Render(labelLine))
		}
		meta := ""
		if entry.Provenance != "" {
			meta = "source=" + entry.Provenance
		}
		if len(entry.Aliases) > 0 {
			meta = appendPickerMeta(meta, "alias="+strings.Join(entry.Aliases, "/"))
		}
		meta = appendPickerMeta(meta, modelPickerCapabilitySummary(entry))
		if entry.Description != "" {
			meta = appendPickerMeta(meta, entry.Description)
		}
		if meta == "" {
			meta = "declared capabilities: unknown"
		}
		for _, metaLine := range modelPickerMetaLines(meta, width-4) {
			lines = append(lines, modelPickerMetaStyle.Render("  "+metaLine))
		}
	}
	if start > 0 || end < len(entries) {
		lines = append(lines, modelPickerMetaStyle.Render(fmt.Sprintf("  showing %d-%d; use up/down for more", start+1, end)))
	}
	lines = append(lines, modelPickerFooterStyle.Render("  enter apply · esc cancel · alt+p close"))
	return strings.Join(lines, "\n")
}

func appendPickerMeta(current, next string) string {
	if current == "" {
		return next
	}
	return current + " · " + next
}

func modelPickerMetaLines(meta string, width int) []string {
	if width < 4 {
		width = 4
	}
	parts := strings.Split(meta, " · ")
	lines := make([]string, 0, len(parts))
	current := ""
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if current == "" {
			current = truncateModelPickerText(part, width)
			continue
		}
		candidate := current + " · " + part
		if lipgloss.Width(candidate) <= width {
			current = candidate
			continue
		}
		lines = append(lines, current)
		current = truncateModelPickerText(part, width)
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func modelPickerCapabilitySummary(entry ModelCatalogEntry) string {
	caps := entry.Capabilities
	parts := make([]string, 0, 6)
	if caps.ContextWindowTokens > 0 {
		parts = append(parts, "ctx="+formatPickerTokens(caps.ContextWindowTokens))
	}
	if caps.MaxOutputTokens > 0 {
		parts = append(parts, "out="+formatPickerTokens(caps.MaxOutputTokens))
	}
	if caps.SupportsReasoning != nil {
		parts = append(parts, "reasoning="+yesNo(*caps.SupportsReasoning))
	}
	if len(caps.ReasoningEfforts) > 0 {
		parts = append(parts, "effort="+strings.Join(caps.ReasoningEfforts, "/"))
	}
	if len(caps.InputModalities) > 0 {
		parts = append(parts, "input="+strings.Join(caps.InputModalities, "/"))
	}
	if caps.SupportsTools != nil {
		parts = append(parts, "tools="+yesNo(*caps.SupportsTools))
	}
	if caps.SupportsStreaming != nil {
		parts = append(parts, "stream="+yesNo(*caps.SupportsStreaming))
	}
	return strings.Join(parts, " · ")
}

func formatPickerTokens(tokens int) string {
	if tokens >= 1_000_000 && tokens%1_000_000 == 0 {
		return strconv.Itoa(tokens/1_000_000) + "m"
	}
	if tokens >= 1_000 && tokens%1_000 == 0 {
		return strconv.Itoa(tokens/1_000) + "k"
	}
	return strconv.Itoa(tokens)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func truncateModelPickerText(value string, width int) string {
	if width < 4 {
		return "..."
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"...") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}
