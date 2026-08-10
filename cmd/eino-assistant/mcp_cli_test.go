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
`)
	stdout, err := listMCPServersForTest(configPath, true)
	if err != nil {
		t.Fatalf("mcp list --json failed: %v", err)
	}
	if strings.Contains(stdout, "do-not-print") {
		t.Fatalf("mcp list JSON leaked environment value: %s", stdout)
	}
	var entries []mcpListEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("decode MCP list JSON: %v\n%s", err, stdout)
	}
	if len(entries) != 2 || entries[0].Name != "local-tools" || !entries[0].Enabled {
		t.Fatalf("MCP entries = %+v", entries)
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
	if err != nil || !strings.Contains(stdout, "Inspect configured MCP servers") || !strings.Contains(stdout, "list") {
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
