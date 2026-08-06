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

func TestLoadProjectInstructionsAtRootOnlyCompatibility(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("root instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	legacy, err := LoadProjectInstructions(ws, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	at, err := LoadProjectInstructionsAt(ws, ws, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructionsAt: %v", err)
	}
	if legacy.Path != at.Path || legacy.Text != at.Text || legacy.Tokens != at.Tokens ||
		legacy.Truncated != at.Truncated || legacy.Found != at.Found ||
		FormatProjectInstructionsBlock(legacy) != FormatProjectInstructionsBlock(at) {
		t.Fatalf("legacy=%+v at=%+v\nlegacy block=%q\nat block=%q", legacy, at,
			FormatProjectInstructionsBlock(legacy), FormatProjectInstructionsBlock(at))
	}
	if len(at.Sources) != 1 || at.Sources[0].Title != agentsFile || at.StartDirOutsideWorkspace {
		t.Fatalf("sources=%+v outside=%v", at.Sources, at.StartDirOutsideWorkspace)
	}
}

func TestLoadProjectInstructionsAtOrdersRootToStartDirAndSelectsPerDirectory(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	middle := filepath.Join(ws, "packages", "core")
	start := filepath.Join(middle, "cmd")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(ws, agentsFile):               "root",
		filepath.Join(ws, "packages", agentsFile):   "packages base",
		filepath.Join(middle, agentsOverrideFile):   "core override",
		filepath.Join(start, agentsOverrideFile):    "",
		filepath.Join(start, agentsFile):            "cmd base",
		filepath.Join(ws, "packages", "ignored.md"): "not selected",
		filepath.Join(ws, "outside", agentsFile):    "not discovered",
	}
	for path, body := range files {
		if filepath.Base(filepath.Dir(path)) == "outside" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	b, err := LoadProjectInstructionsAt(ws, start, 8000)
	if err != nil {
		t.Fatalf("LoadProjectInstructionsAt: %v", err)
	}
	if got, want := len(b.Sources), 4; got != want {
		t.Fatalf("source count=%d, want %d: %+v", got, want, b.Sources)
	}
	wantTitles := []string{agentsFile, "packages/AGENTS.md", "packages/core/AGENTS.override.md", "packages/core/cmd/AGENTS.md"}
	wantTexts := []string{"root", "packages base", "core override", "cmd base"}
	for i, source := range b.Sources {
		if source.Title != wantTitles[i] || source.Text != wantTexts[i] || source.Tokens <= 0 {
			t.Errorf("source[%d]=%+v, want title=%q text=%q and positive tokens", i, source, wantTitles[i], wantTexts[i])
		}
	}
	block := FormatProjectInstructionsBlock(b)
	previous := -1
	for _, want := range wantTexts {
		at := strings.Index(block, want)
		if at <= previous {
			t.Fatalf("block order for %q is invalid: %q", want, block)
		}
		previous = at
	}
	if strings.Contains(block, "not discovered") || strings.Contains(block, "not selected") {
		t.Fatalf("unexpected content in block: %q", block)
	}
}

func TestLoadProjectInstructionsAtOutsideStartFallsBackToRoot(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	outside := t.TempDir()
	inside := filepath.Join(ws, "nested")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("root only"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, agentsFile), []byte("must not load"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadProjectInstructionsAt(ws, outside, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructionsAt: %v", err)
	}
	if !b.StartDirOutsideWorkspace || len(b.Sources) != 1 || b.Sources[0].Text != "root only" {
		t.Fatalf("bundle=%+v", b)
	}
	if strings.Contains(FormatProjectInstructionsBlock(b), "must not load") {
		t.Fatal("outside start fallback discovered a descendant")
	}
}

func TestLoadProjectInstructionsAtCanonicalizesSymlinkedStartDir(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("root only"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, agentsFile), []byte("linked outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	start := filepath.Join(ws, "linked-start")
	if err := os.Symlink(outside, start); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("create symlink: %v", err)
		}
		t.Fatal(err)
	}

	b, err := LoadProjectInstructionsAt(ws, start, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructionsAt: %v", err)
	}
	if !b.StartDirOutsideWorkspace || len(b.Sources) != 1 || b.Sources[0].Text != "root only" {
		t.Fatalf("bundle=%+v", b)
	}
}

func TestLoadProjectInstructionsAtAllowsSymlinkAndSkipsNonRegular(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	child := filepath.Join(ws, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "linked-agents.md")
	if err := os.WriteFile(target, []byte("linked instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(child, agentsOverrideFile)); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("create symlink: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ws, agentsOverrideFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("root base"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadProjectInstructionsAt(ws, child, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructionsAt: %v", err)
	}
	if len(b.Sources) != 2 || b.Sources[0].Text != "root base" || b.Sources[1].Text != "linked instructions" {
		t.Fatalf("sources=%+v", b.Sources)
	}
	canonicalChild, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatal(err)
	}
	if b.Sources[1].Path != filepath.Join(canonicalChild, agentsOverrideFile) {
		t.Fatalf("symlink provenance path=%q", b.Sources[1].Path)
	}
}

func TestLoadProjectInstructionsAtAggregatesBudgetRootFirst(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	child := filepath.Join(ws, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte(strings.Repeat("root rule ", 40)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, agentsFile), []byte("child rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootFull := renderProjectInstructionsBlock(agentsFile, strings.Repeat("root rule ", 40), false)
	maxTokens := usage.EstimateText(rootFull[:len(rootFull)/2])
	if maxTokens >= usage.EstimateText(rootFull) {
		maxTokens = usage.EstimateText(rootFull) - 1
	}
	if maxTokens <= usage.EstimateText(renderTruncatedProjectInstructionsBlock(agentsFile, "")) {
		maxTokens = usage.EstimateText(renderTruncatedProjectInstructionsBlock(agentsFile, "")) + 5
	}

	b, err := LoadProjectInstructionsAt(ws, child, maxTokens)
	if err != nil {
		t.Fatalf("LoadProjectInstructionsAt: %v", err)
	}
	block := FormatProjectInstructionsBlock(b)
	if usage.EstimateText(block) > maxTokens || b.Tokens != usage.EstimateText(block) {
		t.Fatalf("block tokens=%d bundle tokens=%d max=%d", usage.EstimateText(block), b.Tokens, maxTokens)
	}
	if len(b.Sources) != 2 || !b.Sources[0].Truncated || b.Sources[0].Tokens <= 0 {
		t.Fatalf("sources=%+v", b.Sources)
	}
	if b.Sources[1].Tokens != 0 || !b.Sources[1].Truncated || strings.Contains(block, "child rule") {
		t.Fatalf("expected root-first exhaustion, sources=%+v block=%q", b.Sources, block)
	}
}

func TestLoadProjectInstructionsAtReadErrorsFailFast(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based read error is not portable on Windows")
	}
	ws := t.TempDir()
	path := filepath.Join(ws, agentsFile)
	if err := os.WriteFile(path, []byte("private instructions"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	_, err := LoadProjectInstructionsAt(ws, ws, 100)
	if err == nil {
		t.Skip("test process can read mode-000 files")
	}
	if !strings.Contains(err.Error(), "read AGENTS.md") {
		t.Fatalf("error=%v, want read failure", err)
	}
}

func TestLoadProjectInstructionsDefaultKeepsCanonicalCandidatesOnly(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, agentsFile), []byte("canonical instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "CLAUDE.md"), []byte("fallback instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := LoadProjectInstructions(ws, 100)
	if err != nil {
		t.Fatalf("LoadProjectInstructions: %v", err)
	}
	if !b.Found || b.Text != "canonical instructions" || filepath.Base(b.Path) != agentsFile {
		t.Fatalf("bundle=%+v, want canonical candidate", b)
	}
}

func TestLoadProjectInstructionsFallbackOnlyAfterCanonicalCandidatesAreInvalid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		writeFiles func(t *testing.T, workspace string)
	}{
		{
			name: "missing",
			writeFiles: func(t *testing.T, workspace string) {
				writeInstructionFile(t, workspace, "CLAUDE.md", "fallback instructions")
			},
		},
		{
			name: "blank",
			writeFiles: func(t *testing.T, workspace string) {
				writeInstructionFile(t, workspace, agentsOverrideFile, "\xef\xbb\xbf \n\t")
				writeInstructionFile(t, workspace, agentsFile, "\n\t")
				writeInstructionFile(t, workspace, "CLAUDE.md", "fallback instructions")
			},
		},
		{
			name: "nonregular",
			writeFiles: func(t *testing.T, workspace string) {
				if err := os.Mkdir(filepath.Join(workspace, agentsOverrideFile), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(workspace, agentsFile), 0o755); err != nil {
					t.Fatal(err)
				}
				writeInstructionFile(t, workspace, "CLAUDE.md", "fallback instructions")
			},
		},
		{
			name: "canonical-valid",
			writeFiles: func(t *testing.T, workspace string) {
				writeInstructionFile(t, workspace, agentsFile, "canonical instructions")
				writeInstructionFile(t, workspace, "CLAUDE.md", "fallback instructions")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			tc.writeFiles(t, workspace)
			bundle, err := LoadProjectInstructionsWithFallbacks(workspace, 100, []string{"CLAUDE.md"})
			if err != nil {
				t.Fatalf("LoadProjectInstructionsWithFallbacks: %v", err)
			}
			if !bundle.Found {
				t.Fatalf("bundle=%+v, want selected instructions", bundle)
			}
			wantText, wantFile := "fallback instructions", "CLAUDE.md"
			if tc.name == "canonical-valid" {
				wantText, wantFile = "canonical instructions", agentsFile
			}
			if bundle.Text != wantText || filepath.Base(bundle.Path) != wantFile {
				t.Fatalf("bundle=%+v, want text=%q file=%q", bundle, wantText, wantFile)
			}
		})
	}
}

func TestLoadProjectInstructionsFallbackOrderAndDirectoryCardinality(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	writeInstructionFile(t, ws, "CLAUDE.md", "claude fallback")
	writeInstructionFile(t, ws, "CONVENTIONS.md", "conventions fallback")

	b, err := LoadProjectInstructionsAtWithFallbacks(ws, ws, 100, []string{"CLAUDE.md", "CONVENTIONS.md", "CLAUDE.md"})
	if err != nil {
		t.Fatalf("LoadProjectInstructionsAtWithFallbacks: %v", err)
	}
	if len(b.Sources) != 1 || b.Sources[0].Text != "claude fallback" || filepath.Base(b.Sources[0].Path) != "CLAUDE.md" {
		t.Fatalf("sources=%+v, want first fallback only", b.Sources)
	}

	b, err = LoadProjectInstructionsAtWithFallbacks(ws, ws, 100, []string{"CONVENTIONS.md", "CLAUDE.md"})
	if err != nil {
		t.Fatalf("LoadProjectInstructionsAtWithFallbacks reversed: %v", err)
	}
	if len(b.Sources) != 1 || b.Sources[0].Text != "conventions fallback" || filepath.Base(b.Sources[0].Path) != "CONVENTIONS.md" {
		t.Fatalf("sources=%+v, want configured fallback order", b.Sources)
	}
}

func TestLoadProjectInstructionsFallbackAllowsRegularSymlink(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	target := filepath.Join(t.TempDir(), "linked-project-doc.md")
	if err := os.WriteFile(target, []byte("linked fallback"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(ws, "CLAUDE.md")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("create symlink: %v", err)
		}
		t.Fatal(err)
	}

	b, err := LoadProjectInstructionsWithFallbacks(ws, 100, []string{"CLAUDE.md"})
	if err != nil {
		t.Fatalf("LoadProjectInstructionsWithFallbacks: %v", err)
	}
	canonicalWorkspace, err := filepath.EvalSymlinks(ws)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Found || b.Text != "linked fallback" || b.Path != filepath.Join(canonicalWorkspace, "CLAUDE.md") {
		t.Fatalf("bundle=%+v, want fallback symlink provenance", b)
	}
}

func TestLoadProjectInstructionsFallbackRejectsNonBasenames(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	for _, name := range []string{"", " \t", "../escape.md", "nested/escape.md", filepath.Join(string(filepath.Separator), "escape.md"), `..\escape.md`, `C:\escape.md`, ".", ".."} {
		name := name
		t.Run(name, func(t *testing.T) {
			_, err := LoadProjectInstructionsWithFallbacks(ws, 100, []string{name})
			if err == nil || (!strings.Contains(err.Error(), "basename") && !strings.Contains(err.Error(), "empty entries")) {
				t.Fatalf("error=%v, want fallback filename validation", err)
			}
		})
	}
}

func writeInstructionFile(t *testing.T, workspace, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workspace, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
