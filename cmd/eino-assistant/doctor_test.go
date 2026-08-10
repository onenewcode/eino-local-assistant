package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsLocalConfigurationWithoutSecretsOrNetwork(t *testing.T) {
	dataDir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configContents := "[model]\n" +
		"base_url = \"http://test-key@127.0.0.1:1234/v1?secret=hidden\"\n" +
		"api_key = \"test-key\"\n" +
		"name = \"test-model\"\n" +
		"timeout_seconds = 60\n" +
		"[model.context]\n" +
		"window_tokens = 32000\n" +
		"[storage]\n" +
		"data_dir = \"" + dataDir + "\"\n" +
		"[mcp]\n" +
		"\n[[mcp.servers]]\n" +
		"name = \"remote\"\n" +
		"type = \"streamable_http\"\n" +
		"url = \"https://mcp.example.test/mcp\"\n" +
		"oauth = true\n"
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runDoctor(configPath, &output); err != nil {
		t.Fatalf("runDoctor: %v", err)
	}
	for _, want := range []string{"Doctor", "config: ok", "model: ok (test-model", "http://127.0.0.1:1234", "workspace: ok", "storage: ok", "OAuth configured (credential not checked)", "result: ok"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, output.String())
		}
	}
	for _, secret := range []string{"test-key", "secret=hidden", "/v1"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("doctor output leaked %q:\n%s", secret, output.String())
		}
	}
}

func TestDoctorRejectsInvalidConfigAndMissingMCPBearerEnvironment(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "invalid.toml")
	if err := os.WriteFile(invalid, []byte("[model]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDoctor(invalid, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "doctor config") {
		t.Fatalf("invalid doctor config error = %v", err)
	}

	t.Setenv("EINO_DOCTOR_BEARER", "")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configContents := "[model]\n" +
		"base_url = \"https://api.example.test/v1\"\n" +
		"api_key = \"test-key\"\n" +
		"name = \"test-model\"\n" +
		"timeout_seconds = 60\n" +
		"[model.context]\n" +
		"window_tokens = 32000\n" +
		"[storage]\n" +
		"data_dir = \"" + t.TempDir() + "\"\n" +
		"[mcp]\n" +
		"\n[[mcp.servers]]\n" +
		"name = \"remote\"\n" +
		"type = \"streamable_http\"\n" +
		"url = \"https://mcp.example.test/mcp\"\n" +
		"bearer_token_env_var = \"EINO_DOCTOR_BEARER\"\n"
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDoctor(configPath, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "EINO_DOCTOR_BEARER") {
		t.Fatalf("missing bearer environment error = %v", err)
	}
}

func TestDoctorLeavesPendingStorageUncreatedAndChecksStdioCommands(t *testing.T) {
	root := t.TempDir()
	pendingStorage := filepath.Join(root, "future", "sessions")
	configPath := filepath.Join(root, "config.toml")
	configContents := "[model]\n" +
		"base_url = \"https://api.example.test/v1\"\n" +
		"api_key = \"test-key\"\n" +
		"name = \"test-model\"\n" +
		"timeout_seconds = 60\n" +
		"[model.context]\n" +
		"window_tokens = 32000\n" +
		"[storage]\n" +
		"data_dir = \"" + pendingStorage + "\"\n"
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runDoctor(configPath, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "storage: pending") {
		t.Fatalf("pending storage output = %s", output.String())
	}
	if _, err := os.Stat(pendingStorage); !os.IsNotExist(err) {
		t.Fatalf("doctor created pending storage: %v", err)
	}

	configContents += "[mcp]\n\n[[mcp.servers]]\nname = \"missing-command\"\ncommand = \"eino-doctor-command-does-not-exist\"\n"
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDoctor(configPath, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "command") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing stdio command error = %v", err)
	}
}
