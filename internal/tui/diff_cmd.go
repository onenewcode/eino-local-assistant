package tui

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	diffCommandUsage      = "usage: /diff"
	diffCommandMaxBytes   = 128 * 1024
	diffCommandMaxError   = 256
	diffCommandHeader     = "Diff\n  Scope: tracked changes against HEAD (staged + unstaged) plus non-ignored untracked files; ignored omitted\n"
	diffCommandNonTextMsg = "  Notice: non-text diff output was sanitized; control bytes were replaced\n"
)

// cmdDiff displays one callback-provided Git snapshot. The callback is the
// only component allowed to cross the Git process boundary.
func (m *model) cmdDiff(arg string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(arg) != "" {
		m.appendLine(lineError, diffCommandUsage)
		return m, nil
	}
	if m.deps.WorkspaceDiff == nil {
		m.appendLine(lineError, "diff unavailable: workspace diff callback is not configured")
		return m, nil
	}

	diff, err := m.deps.WorkspaceDiff(m.processCtx())
	if err != nil {
		m.appendLine(lineError, "diff unavailable: "+sanitizeDiffError(err))
		return m, nil
	}
	m.appendLine(lineSystem, renderDiffCommand(diff))
	m.appendLine(lineSep, "")
	return m, nil
}

type sanitizedDiffPayload struct {
	text      string
	truncated bool
	nonText   bool
}

// renderDiffCommand keeps the viewer local and bounded even when an embedding
// callback returns malformed or unexpectedly large output.
func renderDiffCommand(raw string) string {
	payload := sanitizeDiffPayload(raw, diffCommandMaxBytes)
	if payload.text == "" && !payload.nonText && !payload.truncated {
		return diffCommandHeader + "  No workspace changes."
	}

	var b strings.Builder
	b.WriteString(diffCommandHeader)
	if payload.nonText {
		b.WriteString(diffCommandNonTextMsg)
	}
	b.WriteString(payload.text)
	if payload.truncated {
		if b.Len() > 0 && !strings.HasSuffix(payload.text, "\n") {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "  Notice: diff output truncated after %d bytes", diffCommandMaxBytes)
	}
	return strings.TrimRight(b.String(), "\n")
}

func sanitizeDiffPayload(raw string, limit int) sanitizedDiffPayload {
	if limit <= 0 {
		return sanitizedDiffPayload{truncated: raw != ""}
	}
	truncated := len(raw) > limit
	if truncated {
		raw = raw[:limit]
	}

	var b strings.Builder
	write := func(value string) bool {
		if b.Len()+len(value) > limit {
			return false
		}
		b.WriteString(value)
		return true
	}

	nonText := false
	for len(raw) > 0 {
		r, size := utf8.DecodeRuneInString(raw)
		if r == utf8.RuneError && size == 1 {
			nonText = true
			if !write("?") {
				truncated = true
				break
			}
			raw = raw[1:]
			continue
		}
		raw = raw[size:]

		value := string(r)
		switch r {
		case '\n':
		case '\r':
			// Normalize CRLF and standalone CR so output cannot move the cursor.
			if strings.HasPrefix(raw, "\n") {
				raw = raw[1:]
			}
			value = "\n"
		case '\t':
			nonText = true
			value = "    "
		default:
			if unicode.IsControl(r) {
				nonText = true
				value = "?"
			}
		}
		if !write(value) {
			truncated = true
			break
		}
	}

	return sanitizedDiffPayload{text: b.String(), truncated: truncated, nonText: nonText}
}

func sanitizeDiffError(err error) string {
	if err == nil {
		return "Git command failed"
	}
	value := strings.TrimSpace(err.Error())
	if value == "" {
		return "Git command failed"
	}

	var b strings.Builder
	for len(value) > 0 && b.Len() < diffCommandMaxError {
		r, size := utf8.DecodeRuneInString(value)
		if r == utf8.RuneError && size == 1 {
			r = '?'
			size = 1
		}
		value = value[size:]
		if r == '\n' || r == '\r' || unicode.IsSpace(r) {
			r = ' '
		} else if unicode.IsControl(r) {
			r = '?'
		}
		encoded := string(r)
		if b.Len()+len(encoded) > diffCommandMaxError {
			break
		}
		b.WriteString(encoded)
	}
	if b.Len() == 0 {
		return "Git command failed"
	}
	if len(value) > 0 {
		b.WriteString("...")
	}
	return b.String()
}
