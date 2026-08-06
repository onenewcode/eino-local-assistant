package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestClassifyShellCommandSeparatesReadWriteAndExternalEffects(t *testing.T) {
	tests := []struct {
		command string
		want    ToolImpact
	}{
		{command: "ls -la && cat AGENTS.md 2>/dev/null | head -50", want: ToolImpactReadOnly},
		{command: `git status 2>&1; echo "---"; git log --oneline -10 2>&1`, want: ToolImpactReadOnly},
		{command: `which git go 2>&1; echo "---"; go version 2>&1; git --version 2>&1`, want: ToolImpactReadOnly},
		{command: "find . -maxdepth 2 -type f", want: ToolImpactReadOnly},
		{command: "find . -maxdepth 2 -type d -not -path './.git*' -not -path './.eino*' | sort | head -50", want: ToolImpactReadOnly},
		{command: "find . -delete", want: ToolImpactWorkspaceWrite},
		{command: "sort -o changed.txt", want: ToolImpactWorkspaceWrite},
		{command: "ls && touch changed.txt", want: ToolImpactWorkspaceWrite},
		{command: "git push origin main", want: ToolImpactExternalSideEffect},
		{command: "curl https://example.test", want: ToolImpactExternalSideEffect},
		{command: "cat input.txt > output.txt", want: ToolImpactWorkspaceWrite},
		{command: "git diff --output changed.patch", want: ToolImpactWorkspaceWrite},
		{command: "ls $(pwd)", want: ToolImpactWorkspaceWrite},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			if got := ClassifyShellCommand(test.command); got != test.want {
				t.Fatalf("ClassifyShellCommand(%q) = %q, want %q", test.command, got, test.want)
			}
		})
	}
}

func TestClassifyShellCommandTreatsPathExecutablesAsWorkspaceWrite(t *testing.T) {
	for _, command := range []string{
		filepath.Join(t.TempDir(), "git") + " status",
		"./git status",
		filepath.Join(t.TempDir(), "sort") + " input.txt",
	} {
		t.Run(command, func(t *testing.T) {
			if got := ClassifyShellCommand(command); got != ToolImpactWorkspaceWrite {
				t.Fatalf("ClassifyShellCommand(%q) = %q, want %q", command, got, ToolImpactWorkspaceWrite)
			}
		})
	}
}

func TestLoadToolPolicyUsesCodexLayersAndStrictestDecision(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	workspace := t.TempDir()
	if _, err := LoadToolPolicyAt(userRoot, workspace); err != nil {
		t.Fatalf("initialize user rules: %v", err)
	}
	userRules := `
prefix_rule(
    pattern = ["git", ["status", "log"]],
    decision = "allow",
    match = ["git status --short", ["git", "log"]],
    not_match = ["git show"],
)
`
	if err := os.WriteFile(filepath.Join(userRoot, "rules", "default.rules"), []byte(userRules), 0o600); err != nil {
		t.Fatal(err)
	}
	projectRulesDir := filepath.Join(workspace, toolRulesDirectory, "rules")
	if err := os.MkdirAll(projectRulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRulesDir, "20-project.rules"), []byte(`
prefix_rule(pattern = ["git"], decision = "prompt", justification = "Confirm repository commands.")
prefix_rule(pattern = ["git", "status"], decision = "forbidden", justification = "Use the dashboard.")
`), 0o600); err != nil {
		t.Fatal(err)
	}
	trustToolPolicyProject(t, userRoot, workspace, "trusted")

	policy, err := LoadToolPolicyAt(userRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.EvaluateShell("git status --short"); got.Decision != DecisionDeny || !strings.Contains(got.Reason, "dashboard") {
		t.Fatalf("git status evaluation = %+v", got)
	}
	if got := policy.EvaluateShell("git log -1"); got.Decision != DecisionAsk || !strings.Contains(got.Reason, "Confirm") {
		t.Fatalf("git log evaluation = %+v", got)
	}
	if got := policy.EvaluateShell("pwd"); got.Decision != DecisionAllow || got.RuleID != "known-safe" {
		t.Fatalf("known-safe fallback = %+v", got)
	}
	if got := policy.EvaluateShell("find . -maxdepth 2 -type d | sort | head -50"); got.Decision != DecisionAsk || got.RuleID != "default" {
		t.Fatalf("impact-only sort must not become an authorization fallback = %+v", got)
	}
}

func TestLoadToolPolicySkipsUntrustedProjectRules(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	workspace := t.TempDir()
	if _, err := LoadToolPolicyAt(userRoot, workspace); err != nil {
		t.Fatalf("initialize user rules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userRoot, "rules", "default.rules"), []byte("# no custom allow\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectRulesDir := filepath.Join(workspace, toolRulesDirectory, "rules")
	if err := os.MkdirAll(projectRulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRulesDir, "10-project.rules"), []byte(`prefix_rule(pattern = ["project-only-command"], decision = "allow")`), 0o600); err != nil {
		t.Fatal(err)
	}
	projectConfig := "[projects." + strconv.Quote(workspace) + "]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(filepath.Join(workspace, toolPolicyConfigFile), []byte(projectConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	policy, err := LoadToolPolicyAt(userRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.EvaluateShell("project-only-command"); got.Decision != DecisionAsk {
		t.Fatalf("untrusted project rule granted authorization: %+v", got)
	}
	if lines := strings.Join(policy.SummaryLines(), "\n"); !strings.Contains(lines, "project rules: skipped") {
		t.Fatalf("untrusted project status missing: %s", lines)
	}
	if lines := strings.Join(policy.SummaryLines(), "\n"); strings.Contains(lines, "network") {
		t.Fatalf("unsupported network rules must not be advertised: %s", lines)
	}

	trustToolPolicyProject(t, userRoot, workspace, "trusted")
	policy, err = LoadToolPolicyAt(userRoot, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.EvaluateShell("project-only-command"); got.Decision != DecisionAllow {
		t.Fatalf("trusted project rule did not grant authorization: %+v", got)
	}
	if lines := strings.Join(policy.SummaryLines(), "\n"); !strings.Contains(lines, "project rules: loaded") {
		t.Fatalf("trusted project status missing: %s", lines)
	}
}

func TestLoadToolPolicyRejectsBlankUserProjectTrustLevel(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	workspace := t.TempDir()
	if _, err := LoadToolPolicyAt(userRoot, workspace); err != nil {
		t.Fatalf("initialize user rules: %v", err)
	}
	trustToolPolicyProject(t, userRoot, workspace, "")

	_, err := LoadToolPolicyAt(userRoot, workspace)
	if err == nil || !strings.Contains(err.Error(), "invalid trust_level") {
		t.Fatalf("LoadToolPolicyAt() error = %v, want invalid trust level", err)
	}
}

func TestLoadToolPolicyRejectsInvalidUserProjectTrustRecords(t *testing.T) {
	for name, document := range map[string]string{
		"relative path": "[projects.\"relative-workspace\"]\ntrust_level = \"trusted\"\n",
		"unknown level": "[projects.\"/absolute/workspace\"]\ntrust_level = \"prompt\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
			workspace := t.TempDir()
			if _, err := LoadToolPolicyAt(userRoot, workspace); err != nil {
				t.Fatalf("initialize user rules: %v", err)
			}
			if err := os.WriteFile(filepath.Join(userRoot, toolPolicyConfigFile), []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := LoadToolPolicyAt(userRoot, workspace)
			if err == nil || !strings.Contains(err.Error(), "user project trust") {
				t.Fatalf("LoadToolPolicyAt() error = %v, want invalid project trust record", err)
			}
		})
	}
}

func TestLoadToolPolicyRejectsSymlinkedUserProjectTrustConfiguration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating file symlinks requires elevated privileges on Windows")
	}
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	workspace := t.TempDir()
	if _, err := LoadToolPolicyAt(userRoot, workspace); err != nil {
		t.Fatalf("initialize user rules: %v", err)
	}
	target := filepath.Join(workspace, toolPolicyConfigFile)
	if err := os.WriteFile(target, []byte("[projects."+strconv.Quote(workspace)+"]\ntrust_level = \"trusted\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(userRoot, toolPolicyConfigFile)); err != nil {
		t.Skipf("creating file symlink: %v", err)
	}

	_, err := LoadToolPolicyAt(userRoot, workspace)
	if err == nil || !strings.Contains(err.Error(), "regular non-symlink file") {
		t.Fatalf("LoadToolPolicyAt() error = %v, want symlinked trust configuration rejection", err)
	}
}

func TestLoadToolPolicyRejectsNonRegularUserProjectTrustConfiguration(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	workspace := t.TempDir()
	if _, err := LoadToolPolicyAt(userRoot, workspace); err != nil {
		t.Fatalf("initialize user rules: %v", err)
	}
	if err := os.Mkdir(filepath.Join(userRoot, toolPolicyConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := LoadToolPolicyAt(userRoot, workspace)
	if err == nil || !strings.Contains(err.Error(), "regular non-symlink file") {
		t.Fatalf("LoadToolPolicyAt() error = %v, want non-regular trust configuration rejection", err)
	}
}

func TestLoadToolPolicySupportsHostExecutableFallbackAndRejectsCustomFields(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	if _, err := LoadToolPolicyAt(userRoot, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(userRoot, "rules", "default.rules")
	if err := os.WriteFile(path, []byte(`
host_executable(name = "git", paths = ["/usr/bin/git"])
prefix_rule(pattern = ["git", "status"], decision = "allow")
`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadToolPolicyAt(userRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.EvaluateShell("/usr/bin/git status"); got.Decision != DecisionAllow {
		t.Fatalf("host executable evaluation = %+v", got)
	}
	if got := policy.EvaluateShell("/opt/git status"); got.Decision != DecisionAsk {
		t.Fatalf("undeclared executable evaluation = %+v", got)
	}
	if err := os.WriteFile(path, []byte(`prefix_rule(pattern = ["ls"], impact = "read_only")`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToolPolicyAt(userRoot, ""); err == nil || !strings.Contains(err.Error(), "unexpected keyword argument \"impact\"") {
		t.Fatalf("custom rule field error = %v", err)
	}
}

func TestLoadToolPolicyDoesNotResolveBareRuleForUndeclaredAbsoluteExecutable(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	if _, err := LoadToolPolicyAt(userRoot, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(userRoot, "rules", "default.rules")
	if err := os.WriteFile(path, []byte(`prefix_rule(pattern = ["git", "status"], decision = "allow")`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadToolPolicyAt(userRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	spoofedGit := filepath.Join(t.TempDir(), "git")
	if matches := policy.matchingRules([]string{spoofedGit, "status"}); len(matches) != 0 {
		t.Fatalf("undeclared absolute executable matched bare rule: %#v", matches)
	}
	if got := policy.EvaluateShell(spoofedGit + " status"); got.Decision != DecisionAsk {
		t.Fatalf("undeclared absolute executable evaluation = %+v, want approval", got)
	}
	if got := ClassifyShellCommand(spoofedGit + " status"); got != ToolImpactWorkspaceWrite {
		t.Fatalf("undeclared absolute executable impact = %q, want %q", got, ToolImpactWorkspaceWrite)
	}
}

func TestLoadToolPolicyValidatesExamplesAfterHostDeclarations(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	if _, err := LoadToolPolicyAt(userRoot, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(userRoot, "rules", "default.rules")
	if err := os.WriteFile(path, []byte(`
prefix_rule(
    pattern = ["git", "status"],
    decision = "allow",
    match = [["/usr/bin/git", "status"]],
    not_match = [["/usr/bin/git", "show"]],
)
host_executable(name = "git", paths = ["/usr/bin/git"])
`), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadToolPolicyAt(userRoot, "")
	if err != nil {
		t.Fatalf("LoadToolPolicyAt() error = %v", err)
	}
	if got := policy.EvaluateShell("/usr/bin/git status"); got.Decision != DecisionAllow {
		t.Fatalf("host-resolved example rule evaluation = %+v", got)
	}
}

func TestLoadToolPolicyAccumulatesHostExecutableDeclarationsAcrossFiles(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	if _, err := LoadToolPolicyAt(userRoot, ""); err != nil {
		t.Fatal(err)
	}
	rulesDir := filepath.Join(userRoot, "rules")
	for name, document := range map[string]string{
		"10-system-git.rules": `host_executable(name = "git", paths = ["/usr/bin/git"])`,
		"20-local-git.rules":  `host_executable(name = "git", paths = ["/usr/local/bin/git"])`,
		"30-git-status.rules": `
prefix_rule(
    pattern = ["git", "status"],
    decision = "allow",
    match = [["/usr/bin/git", "status"], ["/usr/local/bin/git", "status"]],
)
`,
	} {
		if err := os.WriteFile(filepath.Join(rulesDir, name), []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	policy, err := LoadToolPolicyAt(userRoot, "")
	if err != nil {
		t.Fatalf("LoadToolPolicyAt() error = %v", err)
	}
	for _, command := range []string{"/usr/bin/git status", "/usr/local/bin/git status"} {
		if got := policy.EvaluateShell(command); got.Decision != DecisionAllow {
			t.Fatalf("%q evaluation = %+v", command, got)
		}
	}
}

func TestLoadToolPolicyRejectsUnsupportedNetworkRule(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	if _, err := LoadToolPolicyAt(userRoot, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(userRoot, "rules", "default.rules")
	if err := os.WriteFile(path, []byte(`network_rule(host = "registry.example.test", protocol = "https", decision = "allow")`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToolPolicyAt(userRoot, ""); err == nil || !strings.Contains(err.Error(), "network_rule") {
		t.Fatalf("network_rule must be rejected until an execution path enforces it: %v", err)
	}
}

func TestLoadToolPolicyInitializesZeroAuthorizationStarterButDoesNotOverwriteUserRules(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	policy, err := LoadToolPolicyAt(userRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(userRoot, "rules", "default.rules")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "intentionally grants no commands") {
		t.Fatalf("unexpected default rules content:\n%s", before)
	}
	if len(policy.shellRules) != 0 || len(policy.hostExecutables) != 0 {
		t.Fatalf("zero-authorization starter loaded policy state: shell=%d host_executables=%d", len(policy.shellRules), len(policy.hostExecutables))
	}
	if got := policy.EvaluateShell("git push origin main"); got.Decision != DecisionAsk {
		t.Fatalf("zero-authorization starter granted external command: %+v", got)
	}
	custom := []byte(`prefix_rule(pattern = ["git", "status"], decision = "allow")`)
	if err := os.WriteFile(path, custom, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToolPolicyAt(userRoot, ""); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(custom) {
		t.Fatalf("existing user rules were overwritten: %q", after)
	}
}

func TestLoadToolPolicyRejectsSymlinkedProjectRulesControlDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks requires elevated privileges on Windows")
	}
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	workspace := t.TempDir()
	if _, err := LoadToolPolicyAt(userRoot, workspace); err != nil {
		t.Fatalf("initialize user rules: %v", err)
	}
	trustToolPolicyProject(t, userRoot, workspace, "trusted")

	outside := t.TempDir()
	outsideRules := filepath.Join(outside, "rules")
	if err := os.MkdirAll(outsideRules, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideRules, "10-outside.rules"), []byte(`prefix_rule(pattern = ["outside-command"], decision = "allow")`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, toolRulesDirectory)); err != nil {
		t.Skipf("creating directory symlink: %v", err)
	}

	_, err := LoadToolPolicyAt(userRoot, workspace)
	if err == nil || !strings.Contains(err.Error(), "project tool rules control directory") || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("LoadToolPolicyAt() error = %v, want project control-directory symlink rejection", err)
	}
}

func TestLoadToolPolicyInitializesDefaultRulesAtomicallyAcrossConcurrentStarts(t *testing.T) {
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	const workers = 8
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := LoadToolPolicyAt(userRoot, "")
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent LoadToolPolicyAt() error = %v", err)
		}
	}
	contents, err := os.ReadFile(filepath.Join(userRoot, "rules", "default.rules"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != defaultToolRulesDocument {
		t.Fatal("concurrent initialization did not publish the complete embedded default.rules")
	}
}

func TestLoadToolPolicyRejectsSymlinkedRulesDirectoryBeforeInitialization(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks requires elevated privileges on Windows")
	}
	userRoot := filepath.Join(t.TempDir(), toolRulesDirectory)
	if err := os.MkdirAll(userRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(userRoot, "rules")); err != nil {
		t.Fatal(err)
	}

	_, err := LoadToolPolicyAt(userRoot, "")
	if err == nil || !strings.Contains(err.Error(), "non-symlink directory") {
		t.Fatalf("LoadToolPolicyAt() error = %v, want symlink rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "default.rules")); !os.IsNotExist(statErr) {
		t.Fatalf("symlinked rules directory received a default file: stat error = %v", statErr)
	}
}

func trustToolPolicyProject(t *testing.T, userRoot, workspace, trustLevel string) {
	t.Helper()
	document := "[projects." + strconv.Quote(workspace) + "]\ntrust_level = " + strconv.Quote(trustLevel) + "\n"
	if err := os.WriteFile(filepath.Join(userRoot, toolPolicyConfigFile), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}
