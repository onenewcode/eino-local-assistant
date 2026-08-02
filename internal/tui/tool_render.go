package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// toolBodyMaxLines caps how many result/arg lines are shown under a tool call.
const toolBodyMaxLines = 10

// toolBodyMaxRunes caps a single displayed tool body (after pretty-print).
const toolBodyMaxRunes = 800

// toolHeaderArgMaxRunes caps the one-line arg summary on the tool header.
const toolHeaderArgMaxRunes = 48

// formatToolCard builds a multi-line tool transcript body (without the leading glyph).
// phase is "run" | "ok" | "err".
//
//	get_current_time  timezone=UTC
//	  ⎿  {
//	       "datetime": "…"
//	     }
func formatToolCard(name, detail, phase string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}

	header := name
	body := strings.TrimSpace(detail)

	switch phase {
	case "run":
		// Args on the header (Claude/Codex call line); body is running marker.
		if body != "" {
			if sum := compactToolArgs(body); sum != "" {
				header = name + "  " + sum
			}
		}
		body = "running…"
	case "err":
		if body == "" {
			body = "error"
		}
	default: // ok
		if body == "" {
			body = "done"
		} else {
			body = prettyToolText(body)
		}
	}

	body = clampToolBody(body)
	return header + "\n" + indentToolBody(body, phase)
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

func prettyToolText(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if (strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				return string(b)
			}
		}
	}
	return s
}

func clampToolBody(s string) string {
	if s == "" {
		return s
	}
	if utf8.RuneCountInString(s) > toolBodyMaxRunes {
		runes := []rune(s)
		s = string(runes[:toolBodyMaxRunes-1]) + "…"
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= toolBodyMaxLines {
		return s
	}
	kept := lines[:toolBodyMaxLines-1]
	omitted := len(lines) - (toolBodyMaxLines - 1)
	kept = append(kept, fmt.Sprintf("… +%d lines", omitted))
	return strings.Join(kept, "\n")
}

func indentToolBody(body, phase string) string {
	marker := "  ⎿  "
	if phase == "err" {
		marker = "  ✗  "
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = marker + line
			continue
		}
		lines[i] = "     " + line
	}
	return strings.Join(lines, "\n")
}

// renderToolCard styles a multi-line tool card for the viewport.
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
	for _, line := range lines[1:] {
		b.WriteByte('\n')
		trim := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trim, "✗") {
			b.WriteString(errorStyle.Render(line))
		} else {
			b.WriteString(toolBodyStyle.Render(line))
		}
	}
	return b.String()
}
