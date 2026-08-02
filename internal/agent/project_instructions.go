package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"eino-local-assistant/internal/usage"
)

const agentsFile = "AGENTS.md"

// ProjectInstructions is the loaded workspace-root AGENTS.md block.
// Soft project guidance only — not hard permissions and not long-term memory.
type ProjectInstructions struct {
	// Path is the absolute AGENTS.md path when found.
	Path string
	// Text is the (possibly truncated) markdown body.
	Text string
	// Tokens is a local estimate of Text size.
	Tokens int
	// Truncated is true when the file exceeded the budget.
	Truncated bool
	// Found is false when AGENTS.md is missing.
	Found bool
}

// LoadProjectInstructions reads workspace-root AGENTS.md and caps it by maxTokens.
// Missing file returns Found=false without error.
func LoadProjectInstructions(workspaceRoot string, maxTokens int) (ProjectInstructions, error) {
	ws := strings.TrimSpace(workspaceRoot)
	if ws == "" {
		return ProjectInstructions{}, errors.New("workspace root is required")
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return ProjectInstructions{}, err
	}
	if maxTokens <= 0 {
		maxTokens = 8000
	}
	path := filepath.Join(abs, agentsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProjectInstructions{Found: false}, nil
		}
		return ProjectInstructions{}, fmt.Errorf("read AGENTS.md: %w", err)
	}
	text := string(data)
	// Strip UTF-8 BOM if present.
	text = strings.TrimPrefix(text, "\ufeff")
	truncated := false
	const notice = "\n\n…(AGENTS.md truncated for context budget)\n"
	if usage.EstimateText(text) > maxTokens {
		// Keep the head; binary-search a rune cut that fits with the notice.
		runes := []rune(text)
		lo, hi := 0, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			candidate := string(runes[:mid]) + notice
			if usage.EstimateText(candidate) <= maxTokens {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		if lo < 1 {
			lo = 1
		}
		// Prefer snapping to a newline in the kept head.
		kept := string(runes[:lo])
		if i := strings.LastIndex(kept, "\n"); i > 32 {
			kept = kept[:i]
		}
		text = kept + notice
		truncated = true
	}
	return ProjectInstructions{
		Path:      path,
		Text:      text,
		Tokens:    usage.EstimateText(text),
		Truncated: truncated,
		Found:     true,
	}, nil
}

// FormatProjectInstructionsBlock returns the system-prompt section for AGENTS.md.
func FormatProjectInstructionsBlock(b ProjectInstructions) string {
	if !b.Found || strings.TrimSpace(b.Text) == "" {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Project instructions (AGENTS.md)\n\n")
	if b.Truncated {
		sb.WriteString("_Note: AGENTS.md was truncated to fit the context budget._\n\n")
	}
	sb.WriteString(strings.TrimSpace(b.Text))
	sb.WriteString("\n")
	return sb.String()
}
