package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/store"

	"github.com/cloudwego/eino/schema"
)

func executeForTest(args ...string) (stdout, stderr string, err error) {
	root := newRootCommand()
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestRootHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"-h"},
		{"--help"},
		{"help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			stdout, _, err := executeForTest(args...)
			if err != nil {
				t.Fatalf("execute(%v): %v", args, err)
			}
			for _, want := range []string{"Usage:", "chat", "exec", "resume", "sessions", "mcp", "completion", "init", "export", "doctor", "version"} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("help missing %q:\n%s", want, stdout)
				}
			}
		})
	}
}

func TestCommandHelp(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"help", "chat"}, "Start a new interactive chat"},
		{[]string{"chat", "-h"}, "Start a new interactive chat"},
		{[]string{"help", "exec"}, "Run one durable assistant turn without a TTY"},
		{[]string{"exec", "-h"}, "Run one durable assistant turn without a TTY"},
		{[]string{"help", "exec", "resume"}, "Open the explicitly named durable session"},
		{[]string{"exec", "resume", "-h"}, "Open the explicitly named durable session"},
		{[]string{"help", "resume"}, "Resume a previously saved session"},
		{[]string{"resume", "-h"}, "Resume a previously saved session"},
		{[]string{"help", "sessions"}, "List saved sessions"},
		{[]string{"sessions", "-h"}, "List saved sessions"},
		{[]string{"help", "completion"}, "Generate a completion script"},
		{[]string{"completion", "-h"}, "Generate a completion script"},
		{[]string{"help", "init"}, "Create an AGENTS.md project instruction file"},
		{[]string{"init", "-h"}, "Create an AGENTS.md project instruction file"},
		{[]string{"help", "export"}, "Export the complete visible transcript"},
		{[]string{"export", "-h"}, "Export the complete visible transcript"},
		{[]string{"help", "doctor"}, "Check local configuration, workspace"},
		{[]string{"doctor", "-h"}, "Check local configuration, workspace"},
		{[]string{"help", "mcp"}, "Manage configured MCP servers"},
		{[]string{"mcp", "list", "-h"}, "output the configured servers as JSON"},
		{[]string{"mcp", "get", "-h"}, "Show one configured MCP server"},
		{[]string{"mcp", "add", "-h"}, "Add one stdio MCP server"},
		{[]string{"mcp", "enable", "-h"}, "Enable one configured MCP server"},
		{[]string{"mcp", "disable", "-h"}, "Disable one configured MCP server"},
		{[]string{"mcp", "remove", "-h"}, "Remove one configured MCP server"},
		{[]string{"version", "-h"}, "Print version information"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			stdout, _, err := executeForTest(tc.args...)
			if err != nil {
				t.Fatalf("execute(%v): %v", tc.args, err)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Fatalf("expected %q in:\n%s", tc.want, stdout)
			}
		})
	}
}

func TestCompletionGeneratesSupportedShells(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			stdout, _, err := executeForTest("completion", shell)
			if err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
			if strings.TrimSpace(stdout) == "" {
				t.Fatalf("completion %s returned empty output", shell)
			}
		})
	}
	_, _, err := executeForTest("completion", "tcsh")
	if err == nil || !strings.Contains(err.Error(), "unsupported shell") {
		t.Fatalf("completion tcsh error = %v", err)
	}
}

func TestInteractiveModelSelectionHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--help"},
		{"chat", "--help"},
		{"new", "--help"},
		{"resume", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			stdout, _, err := executeForTest(args...)
			if err != nil {
				t.Fatalf("execute(%v): %v", args, err)
			}
			for _, want := range []string{"-m", "--model", "startup-only", "--yolo", "DANGEROUS"} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("help for %v missing %q:\n%s", args, want, stdout)
				}
			}
		})
	}
}

func TestInteractiveModelFlagWiresSessionStart(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want sessionStart
	}{
		{name: "bare", args: []string{"--model", "bare-model"}, want: sessionStart{modelName: "bare-model"}},
		{name: "bare yolo", args: []string{"--yolo"}, want: sessionStart{yolo: true}},
		{name: "root yolo before chat", args: []string{"--yolo", "chat"}, want: sessionStart{yolo: true}},
		{name: "chat", args: []string{"chat", "--model", "chat-model"}, want: sessionStart{modelName: "chat-model"}},
		{name: "chat yolo", args: []string{"chat", "--yolo"}, want: sessionStart{yolo: true}},
		{name: "new alias", args: []string{"new", "-m", "alias-model"}, want: sessionStart{modelName: "alias-model"}},
		{name: "new alias yolo", args: []string{"new", "--yolo"}, want: sessionStart{yolo: true}},
		{name: "resume", args: []string{"resume", "saved-session", "-m", "resume-model"}, want: sessionStart{resumeID: "saved-session", modelName: "resume-model"}},
		{name: "resume yolo", args: []string{"resume", "saved-session", "--yolo"}, want: sessionStart{resumeID: "saved-session", yolo: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got sessionStart
			var gotConfigPath string
			called := false
			root := newRootCommandWithDeps(commandDeps{
				interactive: func(configPath string, start sessionStart, _ io.Writer) error {
					called = true
					gotConfigPath = configPath
					got = start
					return nil
				},
			})
			root.SetArgs(tc.args)
			if err := root.Execute(); err != nil {
				t.Fatalf("execute(%v): %v", tc.args, err)
			}
			if !called {
				t.Fatal("interactive runner was not called")
			}
			if got != tc.want {
				t.Fatalf("session start = %#v, want %#v", got, tc.want)
			}
			wantConfigPath, err := config.UserConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			if gotConfigPath != wantConfigPath {
				t.Fatalf("config path = %q, want %q", gotConfigPath, wantConfigPath)
			}
		})
	}
}

func TestConfigFlagIsRejected(t *testing.T) {
	t.Parallel()
	_, _, err := executeForTest("--config", "project-config.toml", "version")
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --config") {
		t.Fatalf("error = %v, want unknown --config flag", err)
	}
}

func TestYoloIsRejectedForHeadlessAndInformationalCommands(t *testing.T) {
	for _, args := range [][]string{
		{"--yolo", "exec", "inspect"},
		{"--yolo", "sessions"},
		{"--yolo", "version"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCommandWithDeps(commandDeps{})
			root.SetArgs(args)
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "only supported for interactive") {
				t.Fatalf("execute(%v) error = %v, want explicit interactive-only rejection", args, err)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	t.Parallel()
	stdout, _, err := executeForTest("version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	got := strings.TrimSpace(stdout)
	if !strings.HasPrefix(got, appName+" ") {
		t.Fatalf("version output = %q", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	t.Parallel()
	_, _, err := executeForTest("nope")
	if err == nil {
		t.Fatal("expected error for unknown command")
	}
}

func TestResumeRequiresID(t *testing.T) {
	t.Parallel()
	_, _, err := executeForTest("resume")
	if err == nil {
		t.Fatal("expected error when resume id is missing")
	}
}

func TestExecResumeRequiresExplicitIDOrLastAndDocumentsRecovery(t *testing.T) {
	t.Parallel()
	_, _, err := executeForTest("exec", "resume")
	if err == nil || !strings.Contains(err.Error(), "session id is required") {
		t.Fatalf("error=%v, want explicit session-id-or-last error", err)
	}

	stdout, _, err := executeForTest("exec", "resume", "--help")
	if err != nil {
		t.Fatalf("exec resume --help: %v", err)
	}
	for _, want := range []string{"--recover", "--last", "--ephemeral", "-m", "--model", "--reasoning-effort", "-o", "--output-last-message", "stable identity", "storage.data_dir", "temporary snapshot", "[PROMPT]"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("headless resume help missing %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "--json") || !strings.Contains(stdout, "stream-json") {
		t.Fatalf("exec resume help omitted JSONL alias documentation:\n%s", stdout)
	}
}

func TestResumeRecoveryHelpExplainsPendingCompaction(t *testing.T) {
	t.Parallel()
	stdout, _, err := executeForTest("resume", "--help")
	if err != nil {
		t.Fatalf("resume --help: %v", err)
	}
	if !strings.Contains(stdout, "pending compaction") {
		t.Fatalf("resume recovery help omitted pending compaction:\n%s", stdout)
	}
}

func TestSessionsListsV2ThreadStore(t *testing.T) {
	dataDir := t.TempDir()
	threadStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	_, err = threadStore.CreateThread(context.Background(), store.ThreadMeta{
		ID:        "20260715-120000-abc123",
		Title:     "v2 ledger",
		CreatedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	}, "system")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	state, err := threadStore.LoadThread(context.Background(), "20260715-120000-abc123")
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	state, err = threadStore.StartTurn(context.Background(), state.ID, state.Revision, store.TurnStart{
		TurnID: "cli-usage",
		Input:  "seed",
	})
	if err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	state, err = threadStore.RecordUsage(context.Background(), state.ID, store.ModelUsage{
		CallID:              "cli-usage-model-1",
		TurnID:              "cli-usage",
		Operation:           store.UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        1000,
		CompletionTokens:    30,
		TotalTokens:         1030,
		ContextWindowTokens: 4000,
		CostUSD:             0.01,
	})
	if err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}
	state, err = threadStore.RecordUsage(context.Background(), state.ID, store.ModelUsage{
		CallID:              "cli-usage-model-2",
		TurnID:              "cli-usage",
		Operation:           store.UsageOperationAgent,
		HasProviderUsage:    true,
		PromptTokens:        200,
		CompletionTokens:    4,
		TotalTokens:         204,
		ContextWindowTokens: 4000,
		CostUSD:             0.0023,
	})
	if err != nil {
		t.Fatalf("RecordUsage second call: %v", err)
	}
	_, err = threadStore.CommitTurn(context.Background(), state.ID, state.Revision, store.TurnCommit{
		TurnID: "cli-usage",
		Messages: []*schema.Message{
			schema.UserMessage("seed"),
			schema.AssistantMessage("done", nil),
		},
	})
	if err != nil {
		t.Fatalf("CommitTurn: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	config := "[model]\n" +
		"base_url = \"https://api.example.test/v1\"\n" +
		"api_key = \"test-key\"\n" +
		"name = \"test-model\"\n" +
		"timeout_seconds = 60\n" +
		"[model.context]\n" +
		"window_tokens = 32000\n" +
		"[storage]\n" +
		"data_dir = \"" + dataDir + "\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var stdout bytes.Buffer
	err = listSessions(configPath, &stdout)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, "20260715-120000-abc123") || !strings.Contains(output, "v2 ledger") {
		t.Fatalf("sessions output omitted v2 thread:\n%s", output)
	}
	for _, want := range []string{
		"API USAGE", "CONTEXT", "COST~",
		"API usage (exact): input=1.2k completion=34 cached=0 total=1.2k calls=2",
		"context=200/4.0k (5%)", "cost~=$0.012",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("sessions output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "\tTOKENS\t") {
		t.Fatalf("sessions should not label API usage as TOKENS:\n%s", output)
	}
}

func TestSessionsJSONOutputIsMachineReadable(t *testing.T) {
	helpCommand := newSessionsCommand(&rootOptions{})
	var help bytes.Buffer
	helpCommand.SetOut(&help)
	helpCommand.SetArgs([]string{"--help"})
	if err := helpCommand.Execute(); err != nil || !strings.Contains(help.String(), "--output-format") {
		t.Fatalf("sessions help = %q, err=%v", help.String(), err)
	}

	dataDir := t.TempDir()
	threadStore, err := store.NewThreadStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threadStore.CreateThread(context.Background(), store.ThreadMeta{ID: "session-json", Title: "machine readable"}, "secret system prompt"); err != nil {
		t.Fatal(err)
	}
	configPath := writeSessionsConfig(t, dataDir)

	command := newSessionsCommand(&rootOptions{configPath: configPath})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--output-format", "JSON"})
	if err := command.Execute(); err != nil {
		t.Fatalf("sessions --output-format json: %v", err)
	}
	var sessions []store.ThreadMeta
	if err := json.Unmarshal(stdout.Bytes(), &sessions); err != nil {
		t.Fatalf("decode sessions JSON %q: %v", stdout.String(), err)
	}
	if len(sessions) != 1 || sessions[0].ID != "session-json" || sessions[0].Title != "machine readable" {
		t.Fatalf("sessions JSON = %+v", sessions)
	}
	if strings.Contains(stdout.String(), "secret system prompt") || strings.Contains(stdout.String(), "test-key") {
		t.Fatalf("sessions JSON leaked transcript content: %s", stdout.String())
	}

	emptyCommand := newSessionsCommand(&rootOptions{configPath: writeSessionsConfig(t, t.TempDir())})
	stdout.Reset()
	emptyCommand.SetOut(&stdout)
	emptyCommand.SetArgs([]string{"--output-format", "json"})
	if err := emptyCommand.Execute(); err != nil || stdout.String() != "[]\n" {
		t.Fatalf("empty sessions JSON = %q, err=%v", stdout.String(), err)
	}
	for _, raw := range []string{"", "text", " JSON "} {
		if _, err := normalizeSessionsOutputFormat(raw); err != nil {
			t.Fatalf("normalizeSessionsOutputFormat(%q): %v", raw, err)
		}
	}
	if _, err := normalizeSessionsOutputFormat("yaml"); err == nil || !strings.Contains(err.Error(), "text or json") {
		t.Fatalf("normalizeSessionsOutputFormat(yaml) error = %v", err)
	}
}

func writeSessionsConfig(t *testing.T, dataDir string) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configContents := "[model]\n" +
		"base_url = \"https://api.example.test/v1\"\n" +
		"api_key = \"test-key\"\n" +
		"name = \"test-model\"\n" +
		"timeout_seconds = 60\n" +
		"[model.context]\n" +
		"window_tokens = 32000\n" +
		"[storage]\n" +
		"data_dir = \"" + dataDir + "\"\n"
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}
