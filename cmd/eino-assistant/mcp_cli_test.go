package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestMCPListTextRedactsEnvironmentValues(t *testing.T) {
	configPath := writeMCPListConfig(t, `[mcp]

[[mcp.servers]]
name = "local-tools"
command = "does-not-exist"
args = ["--stdio", "two words"]
working_dir = `+strconv.Quote(t.TempDir())+`

[mcp.servers.env]
Z_TOKEN = "super-secret"
A_FLAG = "enabled"

[[mcp.servers]]
name = "helper"
command = "./helper"
`)
	stdout, err := listMCPServersForTest(configPath, false)
	if err != nil {
		t.Fatalf("mcp list failed: %v", err)
	}
	for _, want := range []string{"NAME", "TRANSPORT", "local-tools", "stdio", "does-not-exist", `"two words"`, "A_FLAG,Z_TOKEN", "helper"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("mcp list missing %q:\n%s", want, stdout)
		}
	}
	for _, secret := range []string{"super-secret", "enabled"} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("mcp list leaked environment value %q:\n%s", secret, stdout)
		}
	}
}

func TestMCPListJSONMatchesCodexStyleShape(t *testing.T) {
	configPath := writeMCPListConfig(t, `[mcp]

[[mcp.servers]]
name = "local-tools"
command = "npx"
args = ["-y", "@example/server"]

[mcp.servers.env]
TOKEN = "do-not-print"

[[mcp.servers]]
name = "helper"
command = "./helper"
enabled = false
`)
	stdout, err := listMCPServersForTest(configPath, true)
	if err != nil {
		t.Fatalf("mcp list --json failed: %v", err)
	}
	if strings.Contains(stdout, "do-not-print") {
		t.Fatalf("mcp list JSON leaked environment value: %s", stdout)
	}
	var entries []mcpServerEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("decode MCP list JSON: %v\n%s", err, stdout)
	}
	if len(entries) != 2 || entries[0].Name != "local-tools" || !entries[0].Enabled {
		t.Fatalf("MCP entries = %+v", entries)
	}
	if entries[1].Name != "helper" || entries[1].Enabled {
		t.Fatalf("disabled MCP entry = %+v", entries[1])
	}
	if entries[0].Transport.Type != "stdio" || entries[0].Transport.Command != "npx" || strings.Join(entries[0].Transport.Args, " ") != "-y @example/server" {
		t.Fatalf("first transport = %+v", entries[0].Transport)
	}
	if len(entries[0].Transport.EnvVars) != 1 || entries[0].Transport.EnvVars[0] != "TOKEN" {
		t.Fatalf("first env vars = %#v", entries[0].Transport.EnvVars)
	}
	if entries[1].Transport.Args == nil || len(entries[1].Transport.Args) != 0 {
		t.Fatalf("empty args = %#v, want non-nil empty array", entries[1].Transport.Args)
	}
}

func TestMCPListEmptyAndHelp(t *testing.T) {
	configPath := writeMCPListConfig(t, "")
	stdout, err := listMCPServersForTest(configPath, false)
	if err != nil || strings.TrimSpace(stdout) != "No configured MCP servers." {
		t.Fatalf("empty text list = %q, err=%v", stdout, err)
	}
	stdout, err = listMCPServersForTest(configPath, true)
	if err != nil || strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("empty JSON list = %q, err=%v", stdout, err)
	}
	stdout, _, err = executeMCPCommandForTest("mcp", "--help")
	if err != nil || !strings.Contains(stdout, "Inspect configured MCP servers") || !strings.Contains(stdout, "list") || !strings.Contains(stdout, "get") {
		t.Fatalf("mcp help = %q, err=%v", stdout, err)
	}
	stdout, _, err = executeMCPCommandForTest("mcp", "list", "--help")
	if err != nil || !strings.Contains(stdout, "--json") || !strings.Contains(stdout, "without starting them") {
		t.Fatalf("mcp list help = %q, err=%v", stdout, err)
	}
	if _, _, err := executeMCPCommandForTest("mcp", "list", "extra"); err == nil {
		t.Fatal("mcp list should reject positional arguments")
	}
}

func TestMCPGetShowsOneServerWithoutEnvironmentValues(t *testing.T) {
	configPath := writeMCPListConfig(t, `[mcp]

[[mcp.servers]]
name = "local-tools"
command = "npx"
args = ["-y", "@example/server"]
working_dir = `+strconv.Quote(t.TempDir())+`
enabled = false

[mcp.servers.env]
API_TOKEN = "super-secret"
LOG_LEVEL = "debug"

[[mcp.servers]]
name = "other"
command = "other-command"
`)
	stdout, err := getMCPServerForTest(configPath, " local-tools ", false)
	if err != nil {
		t.Fatalf("mcp get failed: %v", err)
	}
	for _, want := range []string{"local-tools", "enabled: false", "transport: stdio", "command: npx", `args: "-y" "@example/server"`, "env_vars: API_TOKEN,LOG_LEVEL"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("mcp get missing %q:\n%s", want, stdout)
		}
	}
	for _, secret := range []string{"super-secret", "debug", "other-command"} {
		if strings.Contains(stdout, secret) {
			t.Fatalf("mcp get leaked or included %q:\n%s", secret, stdout)
		}
	}

	jsonOutput, err := getMCPServerForTest(configPath, "local-tools", true)
	if err != nil {
		t.Fatalf("mcp get --json failed: %v", err)
	}
	if strings.Contains(jsonOutput, "super-secret") {
		t.Fatalf("mcp get JSON leaked environment value: %s", jsonOutput)
	}
	var entry mcpServerEntry
	if err := json.Unmarshal([]byte(jsonOutput), &entry); err != nil {
		t.Fatalf("decode mcp get JSON: %v\n%s", err, jsonOutput)
	}
	if entry.Name != "local-tools" || entry.Enabled || entry.Transport.EnvVars[0] != "API_TOKEN" || entry.Transport.EnvVars[1] != "LOG_LEVEL" {
		t.Fatalf("MCP get entry = %+v", entry)
	}
}

func TestMCPGetRejectsMissingAndUnknownNames(t *testing.T) {
	configPath := writeMCPListConfig(t, `[mcp]

[[mcp.servers]]
name = "local-tools"
command = "mcp-server"
`)
	if _, err := getMCPServerForTest(configPath, "missing", false); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unknown get error = %v", err)
	}
	stdout, _, err := executeMCPCommandForTest("mcp", "get", "--help")
	if err != nil || !strings.Contains(stdout, "Show one configured MCP server") || !strings.Contains(stdout, "--json") || !strings.Contains(stdout, "without starting") {
		t.Fatalf("mcp get help = %q, err=%v", stdout, err)
	}
	for _, args := range [][]string{{"mcp", "get"}, {"mcp", "get", "first", "second"}, {"mcp", "get", " "}} {
		if _, _, err := executeMCPCommandForTest(args...); err == nil {
			t.Fatalf("mcp get %q should reject invalid arguments", args)
		}
	}
}

func writeMCPListConfig(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := `approval_policy = "on-request"

[model]
provider = "openai"
base_url = "http://localhost:8080/v1"
api_key = "test-key"
name = "test-model"
timeout_seconds = 5

[model.context]
window_tokens = 32000
` + extra
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func listMCPServersForTest(configPath string, jsonOutput bool) (string, error) {
	var stdout bytes.Buffer
	err := listMCPServers(configPath, jsonOutput, &stdout)
	return stdout.String(), err
}

func getMCPServerForTest(configPath, name string, jsonOutput bool) (string, error) {
	var stdout bytes.Buffer
	err := getMCPServer(configPath, name, jsonOutput, &stdout)
	return stdout.String(), err
}

func executeMCPCommandForTest(args ...string) (stdout, stderr string, err error) {
	root := &cobra.Command{Use: appName}
	root.AddCommand(newMCPCommand(&rootOptions{}))
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}
