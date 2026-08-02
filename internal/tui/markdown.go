package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
)

// Markdown rendering for completed assistant messages (Claude/Codex-style).
// Streaming chunks stay plain to avoid flicker and expensive re-parses.

var (
	mdMu        sync.Mutex
	mdRenderer  *glamour.TermRenderer
	mdWidthUsed int

	// Tiny last-hit cache: completed turns re-render the whole transcript often.
	mdCacheKey string
	mdCacheOut string
)

func markdownRenderer(width int) *glamour.TermRenderer {
	if width < 20 {
		width = 20
	}
	// Leave room for the "• " prefix.
	w := width - 2
	if w < 16 {
		w = 16
	}

	mdMu.Lock()
	defer mdMu.Unlock()

	if mdRenderer != nil && absInt(w-mdWidthUsed) < 8 {
		return mdRenderer
	}
	// Prefer dark style for typical coding terminals; AutoStyle also works but
	// can flip mid-session when COLORFGBG is unset.
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(styles.DarkStyle),
		glamour.WithWordWrap(w),
	)
	if err != nil {
		// Fallback to auto.
		r, err = glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(w),
		)
		if err != nil {
			return mdRenderer
		}
	}
	mdRenderer = r
	mdWidthUsed = w
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

	cacheKey := itoa(width) + "\x00" + text
	mdMu.Lock()
	if cacheKey == mdCacheKey && mdCacheOut != "" {
		out := mdCacheOut
		mdMu.Unlock()
		return out
	}
	mdMu.Unlock()

	r := markdownRenderer(width)
	if r == nil {
		return renderAssistantPlain(text)
	}
	out, err := r.Render(text)
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
