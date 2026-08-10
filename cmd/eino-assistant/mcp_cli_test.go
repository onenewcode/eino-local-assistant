package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func TestMCPListAndGetShowStreamableHTTPEndpointsWithoutProcessFields(t *testing.T) {
	configPath := writeMCPListConfig(t, `[mcp]

[[mcp.servers]]
name = "remote-tools"
type = "streamable_http"
url = "https://mcp.example.test/v1/tools"
enabled = false
`)
	jsonOutput, err := listMCPServersForTest(configPath, true)
	if err != nil {
		t.Fatalf("mcp list --json failed: %v", err)
	}
	if strings.Contains(jsonOutput, `"command"`) || strings.Contains(jsonOutput, `"args"`) || strings.Contains(jsonOutput, `"env_vars"`) {
		t.Fatalf("remote MCP JSON included stdio fields: %s", jsonOutput)
	}
	var entries []mcpServerEntry
	if err := json.Unmarshal([]byte(jsonOutput), &entries); err != nil {
		t.Fatalf("decode MCP list JSON: %v\n%s", err, jsonOutput)
	}
	if len(entries) != 1 || entries[0].Name != "remote-tools" || entries[0].Enabled || entries[0].Transport.Type != "streamable_http" || entries[0].Transport.URL != "https://mcp.example.test/v1/tools" {
		t.Fatalf("remote MCP entry = %+v", entries)
	}

	textOutput, err := getMCPServerForTest(configPath, "remote-tools", false)
	if err != nil {
		t.Fatalf("mcp get failed: %v", err)
	}
	for _, want := range []string{"remote-tools", "enabled: false", "transport: streamable_http", "url: https://mcp.example.test/v1/tools"} {
		if !strings.Contains(textOutput, want) {
			t.Fatalf("mcp get missing %q:\n%s", want, textOutput)
		}
	}
	if strings.Contains(textOutput, "command:") || strings.Contains(textOutput, "env_vars:") {
		t.Fatalf("remote MCP text included stdio fields:\n%s", textOutput)
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
	if err != nil || !strings.Contains(stdout, "Manage configured MCP servers") || !strings.Contains(stdout, "list") || !strings.Contains(stdout, "get") || !strings.Contains(stdout, "add") || !strings.Contains(stdout, "enable") || !strings.Contains(stdout, "disable") || !strings.Contains(stdout, "remove") {
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

func TestMCPAddWritesStdioServerWithoutStartingItOrPrintingSecrets(t *testing.T) {
	configPath := writeMCPListConfig(t, `# retain this configuration comment
`)
	stdout, _, err := executeMCPCommandWithConfigForTest(configPath, "mcp", "add", " local-tools ", "--env", "Z_TOKEN=super-secret", "--env", "A_FLAG=enabled", "--", "does-not-exist", "--stdio", "two words")
	if err != nil {
		t.Fatalf("mcp add error = %v", err)
	}
	if strings.TrimSpace(stdout) != `Added MCP server "local-tools".` || strings.Contains(stdout, "super-secret") {
		t.Fatalf("mcp add output = %q", stdout)
	}
	jsonOutput, err := listMCPServersForTest(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jsonOutput, "super-secret") || strings.Contains(jsonOutput, `"A_FLAG":"enabled"`) {
		t.Fatalf("MCP list exposed environment value: %s", jsonOutput)
	}
	var entries []mcpServerEntry
	if err := json.Unmarshal([]byte(jsonOutput), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "local-tools" || !entries[0].Enabled || entries[0].Transport.Command != "does-not-exist" || !reflect.DeepEqual(entries[0].Transport.Args, []string{"--stdio", "two words"}) || !reflect.DeepEqual(entries[0].Transport.EnvVars, []string{"A_FLAG", "Z_TOKEN"}) {
		t.Fatalf("added MCP list entry = %+v", entries)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# retain this configuration comment") || !strings.Contains(string(data), "Z_TOKEN = \"super-secret\"") {
		t.Fatalf("added config =\n%s", data)
	}
}

func TestMCPAddRejectsInvalidCommandShapeAndEnvironment(t *testing.T) {
	stdout, _, err := executeMCPCommandForTest("mcp", "add", "--help")
	if err != nil || !strings.Contains(stdout, "Add one stdio MCP server") || !strings.Contains(stdout, "--env") || !strings.Contains(stdout, "--url") || !strings.Contains(stdout, "Use -- before a stdio command") {
		t.Fatalf("mcp add help = %q, err=%v", stdout, err)
	}
	for _, args := range [][]string{
		{"mcp", "add"},
		{"mcp", "add", "name", "command"},
		{"mcp", "add", "name", "--"},
		{"mcp", "add", " ", "--", "command"},
		{"mcp", "add", "name", "--env", "NOT_AN_ASSIGNMENT", "--", "command"},
		{"mcp", "add", "name", "--env", "TOKEN=first", "--env", "TOKEN=second", "--", "command"},
	} {
		if _, _, err := executeMCPCommandForTest(args...); err == nil {
			t.Fatalf("mcp add %q should reject invalid input", args)
		}
	}
}

func TestMCPAddWritesStreamableHTTPEndpointWithoutConnecting(t *testing.T) {
	configPath := writeMCPListConfig(t, "# retain this configuration comment\n")
	stdout, _, err := executeMCPCommandWithConfigForTest(configPath, "mcp", "add", " remote-tools ", "--url", "https://does-not-resolve.invalid/mcp")
	if err != nil {
		t.Fatalf("mcp add --url error = %v", err)
	}
	if strings.TrimSpace(stdout) != `Added MCP server "remote-tools".` {
		t.Fatalf("mcp add --url output = %q", stdout)
	}
	jsonOutput, err := listMCPServersForTest(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	var entries []mcpServerEntry
	if err := json.Unmarshal([]byte(jsonOutput), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Transport.Type != "streamable_http" || entries[0].Transport.URL != "https://does-not-resolve.invalid/mcp" {
		t.Fatalf("added remote MCP entry = %+v", entries)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# retain this configuration comment") || !strings.Contains(string(data), `type = "streamable_http"`) || !strings.Contains(string(data), `url = "https://does-not-resolve.invalid/mcp"`) || strings.Contains(string(data), "command =") {
		t.Fatalf("added remote MCP config =\n%s", data)
	}
	for _, args := range [][]string{
		{"mcp", "add", "remote", "--url", "https://example.test/mcp", "--env", "TOKEN=secret"},
		{"mcp", "add", "remote", "--url", "https://example.test/mcp", "--", "command"},
		{"mcp", "add", "remote", "--url", "file:///tmp/mcp"},
	} {
		if _, _, commandErr := executeMCPCommandForTest(args...); commandErr == nil {
			t.Fatalf("mcp add %q should reject invalid remote input", args)
		}
	}
}

func TestMCPEnableAndDisableUpdateFutureRuntimeConfiguration(t *testing.T) {
	configPath := writeMCPListConfig(t, `[mcp]

[[mcp.servers]]
name = "local-tools"
command = "does-not-exist"
`)
	stdout, _, err := executeMCPCommandWithConfigForTest(configPath, "mcp", "disable", " local-tools ")
	if err != nil {
		t.Fatalf("mcp disable error = %v", err)
	}
	if strings.TrimSpace(stdout) != `Disabled MCP server "local-tools".` {
		t.Fatalf("mcp disable output = %q", stdout)
	}
	jsonOutput, err := listMCPServersForTest(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	var entries []mcpServerEntry
	if err := json.Unmarshal([]byte(jsonOutput), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Enabled {
		t.Fatalf("disabled MCP list entry = %+v", entries)
	}
	stdout, _, err = executeMCPCommandWithConfigForTest(configPath, "mcp", "enable", "local-tools")
	if err != nil {
		t.Fatalf("mcp enable error = %v", err)
	}
	if strings.TrimSpace(stdout) != `Enabled MCP server "local-tools".` {
		t.Fatalf("mcp enable output = %q", stdout)
	}
	jsonOutput, err = listMCPServersForTest(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(jsonOutput), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Enabled || entries[0].Transport.Command != "does-not-exist" {
		t.Fatalf("enabled MCP list entry = %+v", entries)
	}
}

func TestMCPEnableAndDisableRejectInvalidNamesAndDocumentNoProcessEffects(t *testing.T) {
	for _, verb := range []string{"enable", "disable"} {
		stdout, _, err := executeMCPCommandForTest("mcp", verb, "--help")
		if err != nil || !strings.Contains(stdout, "configured MCP server") || !strings.Contains(stdout, "without starting or stopping") {
			t.Fatalf("mcp %s help = %q, err=%v", verb, stdout, err)
		}
		for _, args := range [][]string{{"mcp", verb}, {"mcp", verb, "first", "second"}, {"mcp", verb, " "}} {
			if _, _, err := executeMCPCommandForTest(args...); err == nil {
				t.Fatalf("mcp %s %q should reject invalid arguments", verb, args)
			}
		}
	}
}

func TestMCPRemoveUpdatesConfigurationWithoutStartingServer(t *testing.T) {
	configPath := writeMCPListConfig(t, `[mcp]

[[mcp.servers]]
name = "remove-me"
command = "does-not-exist"

[mcp.servers.env]
TOKEN = "secret"

[[mcp.servers]]
name = "keep-me"
command = "keep-command"
`)
	stdout, _, err := executeMCPCommandWithConfigForTest(configPath, "mcp", "remove", " remove-me ")
	if err != nil {
		t.Fatalf("mcp remove error = %v", err)
	}
	if strings.TrimSpace(stdout) != `Removed MCP server "remove-me".` {
		t.Fatalf("mcp remove output = %q", stdout)
	}
	jsonOutput, err := listMCPServersForTest(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	var entries []mcpServerEntry
	if err := json.Unmarshal([]byte(jsonOutput), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "keep-me" {
		t.Fatalf("MCP list after remove = %+v", entries)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "does-not-exist") {
		t.Fatalf("removed MCP configuration remains:\n%s", data)
	}
}

func TestMCPRemoveRejectsInvalidNamesAndDocumentsCommand(t *testing.T) {
	stdout, _, err := executeMCPCommandForTest("mcp", "remove", "--help")
	if err != nil || !strings.Contains(stdout, "Remove one configured MCP server") || !strings.Contains(stdout, "without starting") {
		t.Fatalf("mcp remove help = %q, err=%v", stdout, err)
	}
	for _, args := range [][]string{{"mcp", "remove"}, {"mcp", "remove", "first", "second"}, {"mcp", "remove", " "}} {
		if _, _, err := executeMCPCommandForTest(args...); err == nil {
			t.Fatalf("mcp remove %q should reject invalid arguments", args)
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
	return executeMCPCommandWithConfigForTest("", args...)
}

func executeMCPCommandWithConfigForTest(configPath string, args ...string) (stdout, stderr string, err error) {
	root := &cobra.Command{Use: appName}
	root.AddCommand(newMCPCommand(&rootOptions{configPath: configPath}))
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}
