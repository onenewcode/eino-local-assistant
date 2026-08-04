package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"eino-local-assistant/internal/usage"
)

// DefaultUserInstructionsTokens is the bounded budget for the optional user
// instruction block when callers do not provide one.
const DefaultUserInstructionsTokens = 4000

// UserInstructions is the user-scoped instruction block selected from the
// configured home directory. It is soft context, not a permission boundary.
type UserInstructions struct {
	// Path is the candidate path selected when found. It intentionally keeps
	// the candidate filename even when that file is a symlink.
	Path string
	// Text is the normalized, untruncated instruction body.
	Text string
	// Tokens is the estimate of the formatted block actually included.
	Tokens int
	// Truncated reports whether the body was shortened to fit the budget.
	Truncated bool
	// Found reports whether a usable candidate was selected.
	Found     bool
	maxTokens int
}

// LoadUserInstructions selects one non-empty regular AGENTS candidate under
// root. AGENTS.override.md takes precedence over AGENTS.md. Missing, blank,
// and non-regular candidates are skipped; other stat/read errors fail fast.
func LoadUserInstructions(root string, maxTokens int) (UserInstructions, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return UserInstructions{}, errors.New("user instructions root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return UserInstructions{}, err
	}
	if maxTokens <= 0 {
		maxTokens = DefaultUserInstructionsTokens
	}

	path, text, found, err := readUserInstructionsFile(filepath.Clean(absRoot))
	if err != nil {
		return UserInstructions{}, err
	}
	if !found {
		return UserInstructions{Found: false, maxTokens: maxTokens}, nil
	}
	block, truncated := fitUserInstructionsBlock(path, text, maxTokens)
	return UserInstructions{
		Path:      path,
		Text:      text,
		Tokens:    usage.EstimateText(block),
		Truncated: truncated,
		Found:     true,
		maxTokens: maxTokens,
	}, nil
}

func readUserInstructionsFile(root string) (string, string, bool, error) {
	for _, name := range [...]string{agentsOverrideFile, agentsFile} {
		path := filepath.Join(root, name)
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

// FormatUserInstructionsBlock renders the stable user-scope prompt section.
func FormatUserInstructionsBlock(instructions UserInstructions) string {
	if !instructions.Found || strings.TrimSpace(instructions.Text) == "" {
		return ""
	}
	if instructions.maxTokens > 0 {
		block, _ := fitUserInstructionsBlock(instructions.Path, instructions.Text, instructions.maxTokens)
		return block
	}
	return renderUserInstructionsBlock(instructionFilename(instructions.Path), instructions.Text, instructions.Truncated)
}

func fitUserInstructionsBlock(path, body string, maxTokens int) (string, bool) {
	name := instructionFilename(path)
	body = strings.TrimSpace(body)
	full := renderUserInstructionsBlock(name, body, false)
	if usage.EstimateText(full) <= maxTokens {
		return full, false
	}

	minimal := renderUserInstructionsTruncatedBlock(name, "")
	if usage.EstimateText(minimal) <= maxTokens {
		runes := []rune(body)
		lo, hi := 0, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			candidate := renderUserInstructionsTruncatedBlock(name, string(runes[:mid]))
			if usage.EstimateText(candidate) <= maxTokens {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return renderUserInstructionsTruncatedBlock(name, string(runes[:lo])), true
	}
	return "…", true
}

func renderUserInstructionsTruncatedBlock(name, kept string) string {
	kept = strings.TrimRight(kept, " \t\r\n")
	body := "…"
	if kept != "" {
		body = kept + "…"
	}
	return renderUserInstructionsBlock(name, body, false)
}

func renderUserInstructionsBlock(name, body string, withNote bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## User instructions (%s)\n\n", name)
	if withNote {
		fmt.Fprintf(&b, "_Note: %s was truncated to fit the user instruction budget._\n\n", name)
	}
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}
