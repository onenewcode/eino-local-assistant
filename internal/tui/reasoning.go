package tui

import (
	"fmt"
	"strings"
)

// noOpenReasoning is the sentinel for model.openReasoning (no open block).
const noOpenReasoning = -1

// appendReasoningChunk streams display-only reasoning into the open transcript
// block. Reasoning never enters the session ledger.
func (m *model) appendReasoningChunk(chunk string) {
	if chunk == "" {
		return
	}
	m.streamingAssistant = false
	if idx := m.openReasoning; idx >= 0 && idx < len(m.lines) {
		line := &m.lines[idx]
		if line.kind == lineReasoning && !line.folded {
			line.text += chunk
			m.refreshViewport()
			return
		}
	}
	m.lines = append(m.lines, transcriptLine{kind: lineReasoning, text: chunk})
	m.openReasoning = len(m.lines) - 1
	m.refreshViewport()
}

// foldOpenReasoning collapses the in-flight reasoning block after its stream
// ends (discrete lines via appendLine, or turn completion). We do not fold on
// assistant content: final model calls emit reasoning and content concurrently
// from different goroutines.
//
// Fold replaces the body with a one-line summary (display-only, ephemeral).
func (m *model) foldOpenReasoning() {
	idx := m.openReasoning
	m.openReasoning = noOpenReasoning
	if idx < 0 || idx >= len(m.lines) {
		return
	}
	line := &m.lines[idx]
	if line.kind != lineReasoning || line.folded {
		return
	}
	line.text = formatFoldedReasoning(line.text)
	line.folded = true
	m.refreshViewport()
}

func formatFoldedReasoning(body string) string {
	runes := []rune(strings.TrimSpace(body))
	if len(runes) == 0 {
		return "thinking"
	}
	const maxPreview = 48
	preview := string(runes)
	if len(runes) > maxPreview {
		preview = string(runes[:maxPreview]) + "…"
	}
	preview = strings.ReplaceAll(preview, "\n", " ")
	return fmt.Sprintf("thinking · %d chars · %s", len(runes), preview)
}

func (m *model) reasoningIsStreaming(lineIndex int) bool {
	return m.openReasoning == lineIndex && lineIndex >= 0
}
