package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"eino-local-assistant/internal/usage"
)

func TestLoadProjectInstructionsMissing(t *testing.T) {
	t.Parallel()
	b, err := LoadProjectInstructions(t.TempDir(), 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if b.Found {
		t.Fatal("expected not found")
	}
	if FormatProjectInstructionsBlock(b) != "" {
		t.Fatal("expected empty block")
	}
}

func TestLoadProjectInstructionsTruncate(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	body := strings.Repeat("rule line about builds and tests\n", 500)
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadProjectInstructions(ws, 50)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || !b.Truncated {
		t.Fatalf("found=%v truncated=%v tokens=%d", b.Found, b.Truncated, b.Tokens)
	}
	if filepath.Base(b.Path) != agentsFile {
		t.Fatalf("path=%q, want selected filename provenance", b.Path)
	}
	block := FormatProjectInstructionsBlock(b)
	if !strings.Contains(block, "Project instructions") {
		t.Fatalf("block: %q", block)
	}
	if got := usage.EstimateText(block); got > 50 || b.Tokens != got {
		t.Fatalf("block tokens=%d bundle tokens=%d, want <= 50", got, b.Tokens)
	}
}

func TestLoadProjectInstructionsTruncateWithinTinyBudget(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	body := strings.Repeat("project instruction ", 20)
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadProjectInstructions(ws, 1)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || !b.Truncated {
		t.Fatalf("found=%v truncated=%v tokens=%d", b.Found, b.Truncated, b.Tokens)
	}
	block := FormatProjectInstructionsBlock(b)
	if block != "…" {
		t.Fatalf("block = %q, want deterministic tiny-budget marker", block)
	}
	if got := usage.EstimateText(block); got != 1 || b.Tokens != got {
		t.Fatalf("block tokens=%d bundle tokens=%d, want 1", got, b.Tokens)
	}
}

func TestLoadProjectInstructionsFormattedBlockBoundaryBudgets(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	body := "run focused tests before the full suite"
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	full := renderProjectInstructionsBlock(agentsFile, body, false)
	fullTokens := usage.EstimateText(full)

	exact, err := LoadProjectInstructions(ws, fullTokens)
	if err != nil {
		t.Fatalf("LoadProjectInstructions exact: %v", err)
	}
	if exact.Truncated || exact.Tokens != fullTokens || FormatProjectInstructionsBlock(exact) != full {
		t.Fatalf("exact-budget bundle=%+v block=%q", exact, FormatProjectInstructionsBlock(exact))
	}

	truncated, err := LoadProjectInstructions(ws, fullTokens-1)
	if err != nil {
		t.Fatalf("LoadProjectInstructions below boundary: %v", err)
	}
	block := FormatProjectInstructionsBlock(truncated)
	if !truncated.Truncated || usage.EstimateText(block) > fullTokens-1 || truncated.Tokens != usage.EstimateText(block) {
		t.Fatalf("below-boundary bundle=%+v block=%q", truncated, block)
	}
}

func TestLoadProjectInstructionsDoesNotTradeRulesForTruncationNote(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	body := strings.Repeat("important rule content ", 20)
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	withNoteTokens := usage.EstimateText(renderProjectInstructionsBlockWithNote(agentsFile, "…", true))

	b, err := LoadProjectInstructions(ws, withNoteTokens)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	block := FormatProjectInstructionsBlock(b)
	if strings.Contains(block, "_Note:") {
		t.Fatalf("block traded rule text for redundant note: %q", block)
	}
	if !strings.Contains(block, body[:40]) {
		t.Fatalf("block retained too little useful rule text at note threshold: %q", block)
	}
	if got := usage.EstimateText(block); got > withNoteTokens || b.Tokens != got {
		t.Fatalf("block tokens=%d bundle tokens=%d, want <= %d", got, b.Tokens, withNoteTokens)
	}
}

func TestLoadProjectInstructionsBudgetGrowthNeverDropsRules(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	body := strings.Repeat("rule0123456789", 40)
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	header := "## Project instructions (AGENTS.md)\n\n"
	fullTokens := usage.EstimateText(renderProjectInstructionsBlock(agentsFile, body, false))
	previousKept := 0

	for maxTokens := 1; maxTokens <= fullTokens; maxTokens++ {
		b, err := LoadProjectInstructions(ws, maxTokens)
		if err != nil {
			t.Fatalf("LoadProjectInstructions(%d): %v", maxTokens, err)
		}
		block := FormatProjectInstructionsBlock(b)
		gotTokens := usage.EstimateText(block)
		if gotTokens > maxTokens || b.Tokens != gotTokens {
			t.Fatalf("budget=%d block tokens=%d bundle tokens=%d block=%q", maxTokens, gotTokens, b.Tokens, block)
		}

		kept := 0
		switch {
		case block == "…":
		case !strings.HasPrefix(block, header):
			t.Fatalf("budget=%d block lost selected filename: %q", maxTokens, block)
		case !b.Truncated:
			if block != header+body+"\n" {
				t.Fatalf("budget=%d untruncated block=%q", maxTokens, block)
			}
			kept = len(body)
		default:
			retained := strings.TrimSuffix(strings.TrimPrefix(block, header), "…\n")
			if retained == strings.TrimPrefix(block, header) || !strings.HasPrefix(body, retained) {
				t.Fatalf("budget=%d invalid truncated body: %q", maxTokens, block)
			}
			kept = len(retained)
		}
		if kept < previousKept {
			t.Fatalf("budget=%d retained rule bytes fell from %d to %d", maxTokens, previousKept, kept)
		}
		previousKept = kept
	}
}

func TestLoadProjectInstructionsNormalizesBodyBeforeBudgeting(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	body := "keep this visible"
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("\n\t  "+body+"  \n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	maxTokens := usage.EstimateText(renderProjectInstructionsBlock(agentsFile, body, false))

	b, err := LoadProjectInstructions(ws, maxTokens)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if b.Text != body || b.Truncated {
		t.Fatalf("bundle=%+v, want normalized untruncated body", b)
	}
	block := FormatProjectInstructionsBlock(b)
	if !strings.HasSuffix(block, body+"\n") || usage.EstimateText(block) > maxTokens {
		t.Fatalf("block=%q tokens=%d max=%d", block, usage.EstimateText(block), maxTokens)
	}
}

func TestLoadProjectInstructionsTruncatedOverrideUsesSelectedFilename(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("shared instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	override := strings.Repeat("local override instruction\n", 100)
	if err := os.WriteFile(filepath.Join(ws, agentsOverrideFile), []byte(override), 0o644); err != nil {
		t.Fatal(err)
	}

	const maxTokens = 50
	b, err := LoadProjectInstructions(ws, maxTokens)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || !b.Truncated || filepath.Base(b.Path) != agentsOverrideFile {
		t.Fatalf("bundle = %+v", b)
	}
	block := FormatProjectInstructionsBlock(b)
	if got := usage.EstimateText(block); got > maxTokens || b.Tokens != got {
		t.Fatalf("block tokens=%d bundle tokens=%d, want <= %d", got, b.Tokens, maxTokens)
	}
	if !strings.Contains(block, "## Project instructions (AGENTS.override.md)") ||
		strings.Contains(block, "shared instructions") || !strings.HasSuffix(block, "…\n") {
		t.Fatalf("block = %q", block)
	}
}

func TestFormatProjectInstructionsBlockManualBundleCompatibility(t *testing.T) {
	t.Parallel()
	b := ProjectInstructions{
		Path:      filepath.Join("workspace", agentsOverrideFile),
		Text:      "  manual override rule  \n",
		Tokens:    123,
		Truncated: true,
		Found:     true,
	}
	want := "## Project instructions (AGENTS.override.md)\n\n" +
		"_Note: AGENTS.override.md was truncated to fit the context budget._\n\n" +
		"manual override rule\n"
	if got := FormatProjectInstructionsBlock(b); got != want {
		t.Fatalf("FormatProjectInstructionsBlock() = %q, want %q", got, want)
	}
}

func TestLoadProjectInstructionsPrefersRootOverride(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("shared instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, agentsOverrideFile), []byte("local override"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadProjectInstructions(ws, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || b.Text != "local override" || filepath.Base(b.Path) != agentsOverrideFile {
		t.Fatalf("bundle = %+v", b)
	}
	block := FormatProjectInstructionsBlock(b)
	if !strings.Contains(block, "Project instructions (AGENTS.override.md)") ||
		strings.Contains(block, "shared instructions") {
		t.Fatalf("block = %q", block)
	}
}

func TestLoadProjectInstructionsEmptyOverrideFallsBackToBase(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("shared instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, agentsOverrideFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadProjectInstructions(ws, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || b.Text != "shared instructions" || filepath.Base(b.Path) != agentsFile {
		t.Fatalf("bundle = %+v", b)
	}
	if block := FormatProjectInstructionsBlock(b); !strings.Contains(block, "shared instructions") {
		t.Fatalf("block = %q", block)
	}
}

func TestLoadProjectInstructionsBlankOverrideFallsBackToBase(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("shared instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, agentsOverrideFile), []byte("\xef\xbb\xbf \n\t"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadProjectInstructions(ws, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || b.Text != "shared instructions" || filepath.Base(b.Path) != agentsFile {
		t.Fatalf("bundle = %+v", b)
	}
}

func TestLoadProjectInstructionsAllowsRegularFileSymlink(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("shared instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside-workspace.md")
	if err := os.WriteFile(target, []byte("linked override"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(ws, agentsOverrideFile)); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("create symlink: %v", err)
		}
		t.Fatal(err)
	}

	b, err := LoadProjectInstructions(ws, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || b.Text != "linked override" || filepath.Base(b.Path) != agentsOverrideFile {
		t.Fatalf("bundle = %+v", b)
	}
}

func TestLoadProjectInstructionsSkipsNonRegularOverride(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("shared instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ws, agentsOverrideFile), 0o755); err != nil {
		t.Fatal(err)
	}

	b, err := LoadProjectInstructions(ws, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || b.Text != "shared instructions" || filepath.Base(b.Path) != agentsFile {
		t.Fatalf("bundle = %+v", b)
	}
}
