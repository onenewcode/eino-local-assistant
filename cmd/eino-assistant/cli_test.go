package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/store"
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
			for _, want := range []string{"Usage:", "chat", "resume", "sessions", "version"} {
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
		{[]string{"help", "resume"}, "Resume a previously saved session"},
		{[]string{"resume", "-h"}, "Resume a previously saved session"},
		{[]string{"help", "sessions"}, "List saved sessions"},
		{[]string{"sessions", "-h"}, "List saved sessions"},
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
	configPath := filepath.Join(t.TempDir(), "config.yml")
	config := "model:\n" +
		"  base_url: https://api.example.test/v1\n" +
		"  api_key: test-key\n" +
		"  name: test-model\n" +
		"  timeout_seconds: 60\n" +
		"assistant:\n" +
		"  system_prompt: system\n" +
		"storage:\n" +
		"  data_dir: " + dataDir + "\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	stdout, _, err := executeForTest("--config", configPath, "sessions")
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if !strings.Contains(stdout, "20260715-120000-abc123") || !strings.Contains(stdout, "v2 ledger") {
		t.Fatalf("sessions output omitted v2 thread:\n%s", stdout)
	}
}
