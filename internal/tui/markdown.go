package tui

import (
	"strings"
	"sync"
	"unicode"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
)

// Markdown rendering for completed assistant messages (Claude/Codex-style).
// Streaming chunks stay plain to avoid flicker and expensive re-parses.

var (
	mdMu               sync.Mutex
	mdRenderer         *glamour.TermRenderer
	mdWidthUsed        int
	mdPreserveNewlines bool

	// Tiny last-hit cache: completed turns re-render the whole transcript often.
	mdCacheKey string
	mdCacheOut string
)

func markdownWordWrapWidth(width int) int {
	if width < 20 {
		width = 20
	}
	// Leave room for the "• " prefix.
	w := width - 2
	if w < 16 {
		w = 16
	}
	return w
}

func markdownRenderer(width int, preserveNewlines bool) *glamour.TermRenderer {
	w := markdownWordWrapWidth(width)

	mdMu.Lock()
	defer mdMu.Unlock()

	if mdRenderer != nil && mdPreserveNewlines == preserveNewlines && absInt(w-mdWidthUsed) < 8 {
		return mdRenderer
	}
	// Prefer dark style for typical coding terminals; AutoStyle also works but
	// can flip mid-session when COLORFGBG is unset.
	options := []glamour.TermRendererOption{
		glamour.WithStandardStyle(styles.DarkStyle),
		glamour.WithWordWrap(w),
	}
	if preserveNewlines {
		options = append(options, glamour.WithPreservedNewLines())
	}
	r, err := glamour.NewTermRenderer(options...)
	if err != nil {
		// Fallback to auto.
		fallbackOptions := []glamour.TermRendererOption{
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(w),
		}
		if preserveNewlines {
			fallbackOptions = append(fallbackOptions, glamour.WithPreservedNewLines())
		}
		r, err = glamour.NewTermRenderer(fallbackOptions...)
		if err != nil {
			return mdRenderer
		}
	}
	mdRenderer = r
	mdWidthUsed = w
	mdPreserveNewlines = preserveNewlines
	mdCacheKey = ""
	mdCacheOut = ""
	return mdRenderer
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// renderAssistantMarkdown renders completed assistant text as terminal markdown.
// Falls back to plain text if glamour fails or content is empty.
func renderAssistantMarkdown(text string, width int) string {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return assistantStyle.Render("• ")
	}

	preserveNewlines := containsCJKOutsideCode(text)
	renderText := text
	if preserveNewlines {
		renderText = wrapCJKMarkdown(text, max(8, markdownWordWrapWidth(width)-2))
	}
	cacheKey := itoa(width) + "\x00" + text
	mdMu.Lock()
	if cacheKey == mdCacheKey && mdCacheOut != "" {
		out := mdCacheOut
		mdMu.Unlock()
		return out
	}
	mdMu.Unlock()

	r := markdownRenderer(width, preserveNewlines)
	if r == nil {
		return renderAssistantPlain(text)
	}
	out, err := r.Render(renderText)
	if err != nil {
		return renderAssistantPlain(text)
	}
	out = strings.TrimSpace(out) // glamour often adds leading/trailing blank lines
	if out == "" {
		return renderAssistantPlain(text)
	}
	lines := strings.Split(out, "\n")
	var b strings.Builder
	b.WriteString(assistantStyle.Render("• "))
	b.WriteString(lines[0])
	for _, line := range lines[1:] {
		b.WriteByte('\n')
		b.WriteString(line)
	}
	rendered := b.String()

	mdMu.Lock()
	mdCacheKey = cacheKey
	mdCacheOut = rendered
	mdMu.Unlock()
	return rendered
}

// containsCJKOutsideCode identifies prose that glamour's word wrapper cannot
// break reliably because it treats a run of CJK characters as one word.
func containsCJKOutsideCode(text string) bool {
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		if markdownFenceLine(line) {
			inFence = !inFence
			continue
		}
		if !inFence && containsCJK(line) {
			return true
		}
	}
	return false
}

func wrapCJKMarkdown(text string, width int) string {
	if width < 1 {
		return text
	}
	inFence := false
	lines := strings.Split(text, "\n")
	wrapped := make([]string, 0, len(lines))
	for _, line := range lines {
		if markdownFenceLine(line) {
			wrapped = append(wrapped, line)
			inFence = !inFence
			continue
		}
		if inFence || !containsCJK(line) || lipgloss.Width(line) <= width {
			wrapped = append(wrapped, line)
			continue
		}
		wrapped = append(wrapped, wrapCJKMarkdownLine(line, width)...)
	}
	return strings.Join(wrapped, "\n")
}

func markdownFenceLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

func containsCJK(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
}

func wrapCJKMarkdownLine(line string, width int) []string {
	prefixEnd := markdownLinePrefixEnd(line)
	prefix := line[:prefixEnd]
	body := line[prefixEnd:]
	available := width - lipgloss.Width(prefix)
	if available < 4 {
		prefix = ""
		body = line
		available = width
	}
	parts := wrapCellText(body, available)
	if len(parts) <= 1 {
		return []string{line}
	}
	continuation := strings.Repeat(" ", lipgloss.Width(prefix))
	result := make([]string, 0, len(parts))
	result = append(result, prefix+parts[0])
	for _, part := range parts[1:] {
		result = append(result, continuation+part)
	}
	return result
}

func markdownLinePrefixEnd(line string) int {
	end := 0
	for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
		end++
	}
	markerStart := end
	if end < len(line) && (line[end] == '>' || line[end] == '#') {
		for end < len(line) && line[end] == '#' {
			end++
		}
		if line[markerStart] == '>' {
			end++
		}
		if end < len(line) && (line[end] == ' ' || line[end] == '\t') {
			end++
			return end
		}
		return markerStart
	}
	if end < len(line) && (line[end] == '-' || line[end] == '+' || line[end] == '*') {
		end++
		if end < len(line) && (line[end] == ' ' || line[end] == '\t') {
			return end + 1
		}
	}
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	if end > markerStart && end < len(line) && (line[end] == '.' || line[end] == ')') {
		end++
		if end < len(line) && (line[end] == ' ' || line[end] == '\t') {
			return end + 1
		}
	}
	if end > markerStart {
		return markerStart
	}
	return markerStart
}

func wrapCellText(text string, width int) []string {
	if width < 1 {
		return []string{text}
	}
	var lines []string
	var current strings.Builder
	currentWidth := 0
	flush := func() {
		lines = append(lines, strings.TrimRight(current.String(), " \t"))
		current.Reset()
		currentWidth = 0
	}
	for _, r := range text {
		runeWidth := lipgloss.Width(string(r))
		if current.Len() > 0 && currentWidth+runeWidth > width {
			flush()
		}
		if current.Len() == 0 && (r == ' ' || r == '\t') {
			continue
		}
		current.WriteRune(r)
		currentWidth += runeWidth
	}
	if current.Len() > 0 || len(lines) == 0 {
		flush()
	}
	return lines
}

func renderAssistantPlain(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return assistantStyle.Render("• ")
	}
	var b strings.Builder
	b.WriteString(assistantStyle.Render("• "))
	b.WriteString(assistantStyle.Render(lines[0]))
	for _, line := range lines[1:] {
		b.WriteByte('\n')
		b.WriteString(assistantStyle.Render("  " + line))
	}
	return b.String()
}

// renderAssistant chooses plain while streaming; completed messages always go
// through glamour so code fences / lists match Claude/Codex readability.
func renderAssistant(text string, width int, streaming bool) string {
	if streaming {
		return renderAssistantPlain(text)
	}
	return renderAssistantMarkdown(text, width)
}
