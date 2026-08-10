package tools

import (
	"context"
	"os"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServerProcess(t *testing.T) {
	if os.Getenv("EINO_MCP_HELPER") != "1" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo a value"}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		Value string `json:"value"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: input.Value}}}, nil, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
}

func TestConnectMCPServersDiscoversAndCallsTool(t *testing.T) {
	set, err := ConnectMCPServers(context.Background(), []MCPServerOptions{{
		Name: "test-server", Command: os.Args[0], Args: []string{"-test.run=TestMCPServerProcess"},
		Env: map[string]string{"EINO_MCP_HELPER": "1"},
	}})
	if err != nil {
		t.Fatalf("ConnectMCPServers() error = %v", err)
	}
	defer set.Close()
	if len(set.Tools) != 1 {
		t.Fatalf("discovered tools = %d, want 1", len(set.Tools))
	}
	if len(set.Servers) != 1 || set.Servers[0].Name != "test-server" || set.Servers[0].ToolCount != 1 {
		t.Fatalf("server status = %+v", set.Servers)
	}
	info, err := set.Tools[0].Info(context.Background())
	if err != nil || info.Name != "mcp__test-server__echo" || info.ParamsOneOf == nil {
		t.Fatalf("tool info = %+v, err=%v", info, err)
	}
	allowedCtx := WithPermissionHandler(context.Background(), func(_ context.Context, request PermissionRequest) (bool, error) {
		if request.Tool != "mcp__test-server__echo" || request.Action != "call" {
			t.Errorf("permission request = %+v", request)
		}
		return true, nil
	})
	output, err := set.Tools[0].InvokableRun(allowedCtx, `{"value":"hello"}`)
	if err != nil || output != "hello" {
		t.Fatalf("MCP call output=%q err=%v", output, err)
	}
}

func TestMCPToolPermissionDenied(t *testing.T) {
	set, err := ConnectMCPServers(context.Background(), []MCPServerOptions{{
		Name: "test", Command: os.Args[0], Args: []string{"-test.run=TestMCPServerProcess"},
		Env: map[string]string{"EINO_MCP_HELPER": "1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	_, err = set.Tools[0].InvokableRun(WithPermissionHandler(context.Background(), func(context.Context, PermissionRequest) (bool, error) {
		return false, nil
	}), `{"value":"blocked"}`)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("denied call error = %v", err)
	}
}

func TestMCPNameAndResultHelpers(t *testing.T) {
	if got := mcpToolName("remote server", "read.file"); got != "mcp__remote_server__read_file" {
		t.Fatalf("mcpToolName = %q", got)
	}
	if got := mcpResultText(&mcp.CallToolResult{StructuredContent: map[string]any{"ok": true}}); got != `{"ok":true}` {
		t.Fatalf("structured result = %q", got)
	}
}
