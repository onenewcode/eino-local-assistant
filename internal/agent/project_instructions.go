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
	Found bool
	// Sources contains the selected instruction file from each discovered
	// directory, in workspace-root-first order.
	Sources []ProjectInstructionSource
	// StartDirOutsideWorkspace is true when the requested start directory is
	// outside the workspace and discovery fell back to the workspace root.
	StartDirOutsideWorkspace bool
	maxTokens                int
}

// ProjectInstructionSource describes one selected project instruction file.
// Tokens is the number of tokens included in the formatted aggregate block,
// rather than the size of the unbounded file contents.
type ProjectInstructionSource struct {
	// Path is the absolute path at which the candidate was discovered.
	Path string
	// Title is the stable workspace-relative title used in the prompt.
	Title string
	// Text is the normalized, untruncated instruction body.
	Text string
	// Tokens is the source's contribution to the aggregate formatted block.
	Tokens int
	// Truncated reports that the source could not be included in full. A source
	// with zero Tokens was discovered after the aggregate budget was exhausted.
	Truncated bool
}

// LoadProjectInstructions reads one workspace-root instruction file and caps
// its complete formatted rules block by maxTokens. AGENTS.override.md takes
// precedence over AGENTS.md; the files are alternatives and are never
// concatenated. Missing files return Found=false without error. This legacy
// entry point keeps fallback filenames disabled.
func LoadProjectInstructions(workspaceRoot string, maxTokens int) (ProjectInstructions, error) {
	return LoadProjectInstructionsAt(workspaceRoot, workspaceRoot, maxTokens)
}

// LoadProjectInstructionsWithFallbacks is the root-only form of
// LoadProjectInstructionsAtWithFallbacks.
func LoadProjectInstructionsWithFallbacks(workspaceRoot string, maxTokens int, fallbackFilenames []string) (ProjectInstructions, error) {
	return LoadProjectInstructionsAtWithFallbacks(workspaceRoot, workspaceRoot, maxTokens, fallbackFilenames)
}

// LoadProjectInstructionsAt reads project instruction files from workspaceRoot
// through startDir, inclusive, and caps the aggregate formatted block by
// maxTokens. If startDir is outside workspaceRoot, only workspaceRoot is
// inspected and StartDirOutsideWorkspace records that compatibility fallback.
// The legacy entry point keeps fallback filenames disabled.
func LoadProjectInstructionsAt(workspaceRoot, startDir string, maxTokens int) (ProjectInstructions, error) {
	return LoadProjectInstructionsAtWithFallbacks(workspaceRoot, startDir, maxTokens, nil)
}

// LoadProjectInstructionsAtWithFallbacks is the configurable project-layer
// loader. Each discovered directory tries the canonical AGENTS candidates
// first, then fallbackFilenames in order, selecting at most one non-empty
// regular file. Fallback names must be basenames; callers normally obtain the
// validated list from config.RulesConfig before passing it through LayerOptions.
func LoadProjectInstructionsAtWithFallbacks(workspaceRoot, startDir string, maxTokens int, fallbackFilenames []string) (ProjectInstructions, error) {
	ws := strings.TrimSpace(workspaceRoot)
	if ws == "" {
		return ProjectInstructions{}, errors.New("workspace root is required")
	}
	fallbackFilenames, err := normalizeProjectInstructionFallbackFilenames(fallbackFilenames)
	if err != nil {
		return ProjectInstructions{}, err
	}
	abs, err := filepath.Abs(ws)
	if err != nil {
		return ProjectInstructions{}, err
	}
	abs = canonicalProjectInstructionPath(abs)
	if maxTokens <= 0 {
		maxTokens = 8000
	}

	start := strings.TrimSpace(startDir)
	if start == "" {
		start = abs
	}
	startAbs, err := filepath.Abs(start)
	if err != nil {
		return ProjectInstructions{}, err
	}
	startAbs = canonicalProjectInstructionPath(startAbs)

	directories, outside := projectInstructionDirectories(abs, startAbs)
	sources := make([]ProjectInstructionSource, 0, len(directories))
	for _, directory := range directories {
		path, text, found, err := readProjectInstructionsFileWithFallbacks(directory, fallbackFilenames)
		if err != nil {
			return ProjectInstructions{}, err
		}
		if !found {
			continue
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return ProjectInstructions{}, err
		}
		sources = append(sources, ProjectInstructionSource{
			Path:  path,
			Title: filepath.ToSlash(rel),
			Text:  text,
		})
	}
	if len(sources) == 0 {
		return ProjectInstructions{
			Found:                    false,
			StartDirOutsideWorkspace: outside,
			maxTokens:                maxTokens,
		}, nil
	}

	sources, block := fitProjectInstructionSources(sources, maxTokens)
	first := sources[0]
	var combined strings.Builder
	for i, source := range sources {
		if i > 0 {
			combined.WriteString("\n\n")
		}
		combined.WriteString(source.Text)
	}
	return ProjectInstructions{
		Path:                     first.Path,
		Text:                     combined.String(),
		Tokens:                   usage.EstimateText(block),
		Truncated:                anyProjectInstructionSourceTruncated(sources),
		Found:                    true,
		Sources:                  sources,
		StartDirOutsideWorkspace: outside,
		maxTokens:                maxTokens,
	}, nil
}

// canonicalProjectInstructionPath keeps directory containment comparisons
// consistent when callers pass a symlinked workspace or startup directory.
// A missing path is left cleaned so the existing missing-file behavior stays
// non-fatal.
func canonicalProjectInstructionPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func projectInstructionDirectories(workspaceRoot, startDir string) ([]string, bool) {
	rel, err := filepath.Rel(workspaceRoot, startDir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return []string{workspaceRoot}, true
	}
	if rel == "." {
		return []string{workspaceRoot}, false
	}

	directories := []string{workspaceRoot}
	current := workspaceRoot
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		directories = append(directories, current)
	}
	return directories, false
}

func readProjectInstructionsFileWithFallbacks(workspaceRoot string, fallbackFilenames []string) (string, string, bool, error) {
	names := make([]string, 0, 2+len(fallbackFilenames))
	seen := make(map[string]struct{}, 2+len(fallbackFilenames))
	for _, name := range append([]string{agentsOverrideFile, agentsFile}, fallbackFilenames...) {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, name := range names {
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

func normalizeProjectInstructionFallbackFilenames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, errors.New("project instruction fallback filenames must not contain empty entries")
		}
		if name == "." || name == ".." || strings.ContainsRune(name, '\x00') || strings.ContainsAny(name, "/\\:\r\n") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
			return nil, fmt.Errorf("project instruction fallback filename %q must be a basename without path separators", raw)
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized, nil
}

// FormatProjectInstructionsBlock returns the system-prompt section for the
// selected AGENTS instruction file.
func FormatProjectInstructionsBlock(b ProjectInstructions) string {
	if len(b.Sources) > 0 {
		if b.maxTokens > 0 {
			_, block := fitProjectInstructionSources(b.Sources, b.maxTokens)
			return block
		}
		var sb strings.Builder
		for _, source := range b.Sources {
			if source.Text == "" {
				continue
			}
			sb.WriteString(renderProjectInstructionsBlockWithNote(source.Title, strings.TrimSpace(source.Text), source.Truncated))
		}
		return sb.String()
	}
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
	return fitProjectInstructionsBlockTitle(instructionFilename(path), body, maxTokens)
}

func fitProjectInstructionsBlockTitle(title, body string, maxTokens int) (string, bool) {
	name := title
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

func fitProjectInstructionSources(sources []ProjectInstructionSource, maxTokens int) ([]ProjectInstructionSource, string) {
	if maxTokens <= 0 {
		maxTokens = 8000
	}
	result := append([]ProjectInstructionSource(nil), sources...)
	var block strings.Builder
	used := 0
	for i := range result {
		if used >= maxTokens {
			result[i].Tokens = 0
			result[i].Truncated = true
			continue
		}
		formatted, truncated := fitProjectInstructionsBlockTitle(result[i].Title, result[i].Text, maxTokens-used)
		result[i].Tokens = usage.EstimateText(formatted)
		result[i].Truncated = truncated
		if result[i].Tokens == 0 {
			continue
		}
		block.WriteString(formatted)
		used += result[i].Tokens
	}
	return result, block.String()
}

func anyProjectInstructionSourceTruncated(sources []ProjectInstructionSource) bool {
	for _, source := range sources {
		if source.Truncated {
			return true
		}
	}
	return false
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
