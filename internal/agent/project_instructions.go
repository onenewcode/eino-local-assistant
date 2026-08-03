package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"eino-local-assistant/internal/usage"
)

const (
	agentsFile         = "AGENTS.md"
	agentsOverrideFile = "AGENTS.override.md"
)

// ProjectInstructions is the selected workspace-root instruction block.
// Soft project guidance only — not hard permissions and not long-term memory.
type ProjectInstructions struct {
	// Path is the absolute selected instruction path when found.
	Path string
	// Text is the normalized markdown body.
	Text string
	// Tokens is a local estimate of the complete formatted rules block.
	Tokens int
	// Truncated is true when the file exceeded the budget.
	Truncated bool
	// Found is false when no supported instruction file exists.
	Found     bool
	maxTokens int
}

// LoadProjectInstructions reads one workspace-root instruction file and caps
// its complete formatted rules block by maxTokens. AGENTS.override.md takes
// precedence over AGENTS.md; the files are alternatives and are never
// concatenated. Missing files return Found=false without error.
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
	path, text, found, err := readProjectInstructionsFile(abs)
	if err != nil {
		return ProjectInstructions{}, err
	}
	if !found {
		return ProjectInstructions{Found: false}, nil
	}
	block, truncated := fitProjectInstructionsBlock(path, text, maxTokens)
	return ProjectInstructions{
		Path:      path,
		Text:      text,
		Tokens:    usage.EstimateText(block),
		Truncated: truncated,
		Found:     true,
		maxTokens: maxTokens,
	}, nil
}

func readProjectInstructionsFile(workspaceRoot string) (string, string, bool, error) {
	for _, name := range [...]string{agentsOverrideFile, agentsFile} {
		path := filepath.Join(workspaceRoot, name)
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", "", false, fmt.Errorf("stat %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", "", false, fmt.Errorf("read %s: %w", name, err)
		}
		text := strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff"))
		if text == "" {
			continue
		}
		return path, text, true, nil
	}
	return "", "", false, nil
}

// FormatProjectInstructionsBlock returns the system-prompt section for the
// selected AGENTS instruction file.
func FormatProjectInstructionsBlock(b ProjectInstructions) string {
	if !b.Found || strings.TrimSpace(b.Text) == "" {
		return ""
	}
	if b.maxTokens > 0 {
		block, _ := fitProjectInstructionsBlock(b.Path, b.Text, b.maxTokens)
		return block
	}
	return renderProjectInstructionsBlock(instructionFilename(b.Path), strings.TrimSpace(b.Text), b.Truncated)
}

func fitProjectInstructionsBlock(path, body string, maxTokens int) (string, bool) {
	name := instructionFilename(path)
	body = strings.TrimSpace(body)
	full := renderProjectInstructionsBlock(name, body, false)
	if usage.EstimateText(full) <= maxTokens {
		return full, false
	}

	// The ellipsis already marks truncation. Keep the shorter frame so raising
	// the budget never replaces useful rule text with an explanatory note.
	minimal := renderTruncatedProjectInstructionsBlock(name, "")
	if usage.EstimateText(minimal) <= maxTokens {
		runes := []rune(body)
		lo, hi := 0, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			candidate := renderTruncatedProjectInstructionsBlock(name, string(runes[:mid]))
			if usage.EstimateText(candidate) <= maxTokens {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return renderTruncatedProjectInstructionsBlock(name, string(runes[:lo])), true
	}

	// A positive token budget always accommodates this one-rune marker.
	return "…", true
}

func renderTruncatedProjectInstructionsBlock(name, kept string) string {
	kept = strings.TrimRight(kept, " \t\r\n")
	body := "…"
	if kept != "" {
		body = kept + "…"
	}
	return renderProjectInstructionsBlockWithNote(name, body, false)
}

func renderProjectInstructionsBlock(name, body string, truncated bool) string {
	return renderProjectInstructionsBlockWithNote(name, body, truncated)
}

func renderProjectInstructionsBlockWithNote(name, body string, withNote bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Project instructions (%s)\n\n", name)
	if withNote {
		fmt.Fprintf(&sb, "_Note: %s was truncated to fit the context budget._\n\n", name)
	}
	sb.WriteString(body)
	sb.WriteString("\n")
	return sb.String()
}

func instructionFilename(path string) string {
	name := filepath.Base(path)
	if name == "." || name == string(filepath.Separator) || name == "" {
		return agentsFile
	}
	return name
}
