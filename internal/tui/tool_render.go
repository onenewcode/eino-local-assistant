package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// toolHeaderArgMaxRunes caps the one-line arg summary on the tool header.
const toolHeaderArgMaxRunes = 48

// formatToolCard builds the default compact tool transcript (without its glyph).
// phase is "run" | "ok" | "err". Completion callers should preserve the
// original input with formatToolCardWithInput so the finished line remains useful.
func formatToolCard(name, detail, phase string) string {
	return formatToolCardWithInput(name, "", detail, phase)
}

func formatToolCardWithInput(name, input, detail, phase string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}

	parts := make([]string, 0, 4)
	switch phase {
	case "run":
		if summary := compactToolInput(name, detail); summary != "" {
			parts = append(parts, summary)
		}
		parts = append(parts, "running…")
	case "err":
		if summary := compactToolInput(name, input); summary != "" {
			parts = append(parts, summary)
		}
		parts = append(parts, "failed: "+compactToolText(detail))
	default:
		if summary := compactToolInput(name, input); summary != "" {
			parts = append(parts, summary)
		}
		parts = append(parts, compactToolResult(detail))
	}

	return name + "  " + strings.Join(parts, " · ")
}

// compactToolArgs produces a single-line summary for the tool header.
func compactToolArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Prefer key=value for small JSON objects.
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		var obj map[string]any
		if json.Unmarshal([]byte(raw), &obj) == nil && len(obj) > 0 {
			parts := make([]string, 0, len(obj))
			for k, v := range obj {
				parts = append(parts, fmt.Sprintf("%s=%v", k, v))
				if len(parts) >= 3 {
					break
				}
			}
			// Map iteration order is random; sort for stability.
			if len(parts) > 1 {
				// simple insertion sort (tiny n)
				for i := 1; i < len(parts); i++ {
					j := i
					for j > 0 && parts[j] < parts[j-1] {
						parts[j], parts[j-1] = parts[j-1], parts[j]
						j--
					}
				}
			}
			raw = strings.Join(parts, " ")
		}
	}
	raw = strings.Join(strings.Fields(raw), " ")
	if utf8.RuneCountInString(raw) > toolHeaderArgMaxRunes {
		runes := []rune(raw)
		raw = string(runes[:toolHeaderArgMaxRunes-1]) + "…"
	}
	return raw
}

func compactToolInput(name, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if name == "shell" {
		var input struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(raw), &input) == nil && strings.TrimSpace(input.Command) != "" {
			return compactToolText(input.Command)
		}
	}
	return compactToolArgs(raw)
}

func compactToolResult(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "done"
	}
	var result map[string]any
	if json.Unmarshal([]byte(raw), &result) != nil {
		return "done"
	}

	if boolField(result, "denied") {
		return "denied" + resultMetadata(result, false)
	}
	if boolField(result, "cancelled") {
		return "cancelled" + resultMetadata(result, false)
	}
	if exitCode, ok := integerField(result, "exit_code"); ok {
		return fmt.Sprintf("exit %d", exitCode) + resultMetadata(result, true)
	}

	for _, key := range []string{"datetime", "status", "count", "matches", "path"} {
		if value, ok := scalarField(result, key); ok {
			return key + "=" + compactToolText(value)
		}
	}
	return "done"
}

func resultMetadata(result map[string]any, includeDuration bool) string {
	parts := make([]string, 0, 2)
	if includeDuration {
		if duration, ok := integerField(result, "duration_ms"); ok && duration >= 0 {
			parts = append(parts, fmt.Sprintf("%dms", duration))
		}
	}
	if impact, ok := scalarField(result, "impact"); ok {
		parts = append(parts, strings.ReplaceAll(impact, "_", "-"))
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

func boolField(result map[string]any, key string) bool {
	value, ok := result[key].(bool)
	return ok && value
}

func integerField(result map[string]any, key string) (int, bool) {
	value, ok := result[key]
	if !ok {
		return 0, false
	}
	float, ok := value.(float64)
	if !ok || float != float64(int(float)) {
		return 0, false
	}
	return int(float), true
}

func scalarField(result map[string]any, key string) (string, bool) {
	value, ok := result[key]
	if !ok {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), strings.TrimSpace(typed) != ""
	case float64:
		return fmt.Sprintf("%v", typed), true
	case bool:
		return fmt.Sprintf("%t", typed), true
	default:
		return "", false
	}
}

func compactToolText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "error"
	}
	if utf8.RuneCountInString(value) <= toolHeaderArgMaxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:toolHeaderArgMaxRunes-1]) + "…"
}

// renderToolCard styles a compact tool card for the viewport.
func renderToolCard(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	headerStyle := toolStyle.Bold(true)
	// Claude/Codex-ish call glyph.
	b.WriteString(headerStyle.Render("⚙ "))
	// Split name vs arg summary on first double-space if present.
	header := lines[0]
	if i := strings.Index(header, "  "); i > 0 {
		b.WriteString(headerStyle.Render(header[:i]))
		b.WriteString(toolBodyStyle.Render(header[i:]))
	} else {
		b.WriteString(headerStyle.Render(header))
	}
	return b.String()
}
