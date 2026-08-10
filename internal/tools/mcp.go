package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPServerOptions describes one explicitly configured stdio MCP server.
type MCPServerOptions struct {
	Name       string
	Command    string
	Args       []string
	WorkingDir string
	Env        map[string]string
}

// MCPToolSet owns the external sessions and the Eino tools backed by them.
// Call Close when the chat/exec process is done so child servers are reaped.
type MCPToolSet struct {
	sessions []*mcp.ClientSession
	Tools    []tool.InvokableTool
	Servers  []MCPServerInfo
}

// MCPServerInfo is the discovered, read-only status of one configured server.
type MCPServerInfo struct {
	Name      string
	ToolCount int
}

// ConnectMCPServers starts configured servers and discovers their tools.
// Names are namespaced as mcp__<server>__<tool> to avoid collisions with
// built-ins and to keep the remote name private to the adapter.
func ConnectMCPServers(ctx context.Context, servers []MCPServerOptions) (*MCPToolSet, error) {
	set := &MCPToolSet{}
	seen := make(map[string]struct{})
	for _, opts := range servers {
		name := strings.TrimSpace(opts.Name)
		command := strings.TrimSpace(opts.Command)
		if name == "" || command == "" {
			set.Close()
			return nil, errors.New("MCP server name and command are required")
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "eino-local-assistant", Version: "dev"}, nil)
		cmd := exec.Command(command, opts.Args...)
		if strings.TrimSpace(opts.WorkingDir) != "" {
			cmd.Dir = opts.WorkingDir
		}
		if len(opts.Env) > 0 {
			cmd.Env = os.Environ()
			for key, value := range opts.Env {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
		}
		session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
		if err != nil {
			set.Close()
			return nil, fmt.Errorf("connect MCP server %q: %w", name, err)
		}
		set.sessions = append(set.sessions, session)
		serverInfo := MCPServerInfo{Name: name}
		cursor := ""
		for {
			result, listErr := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
			if listErr != nil {
				set.Close()
				return nil, fmt.Errorf("list MCP tools from %q: %w", name, listErr)
			}
			for _, remote := range result.Tools {
				if remote == nil || strings.TrimSpace(remote.Name) == "" {
					continue
				}
				toolName := mcpToolName(name, remote.Name)
				if _, exists := seen[toolName]; exists {
					set.Close()
					return nil, fmt.Errorf("duplicate MCP tool name %q", toolName)
				}
				info, infoErr := mcpToolInfo(toolName, remote)
				if infoErr != nil {
					set.Close()
					return nil, fmt.Errorf("decode MCP tool %q: %w", remote.Name, infoErr)
				}
				seen[toolName] = struct{}{}
				set.Tools = append(set.Tools, &mcpTool{session: session, remoteName: remote.Name, info: info})
				serverInfo.ToolCount++
			}
			cursor = strings.TrimSpace(result.NextCursor)
			if cursor == "" {
				break
			}
		}
		set.Servers = append(set.Servers, serverInfo)
	}
	return set, nil
}

// Close shuts down all external MCP sessions. It is safe to call repeatedly.
func (s *MCPToolSet) Close() error {
	if s == nil {
		return nil
	}
	var first error
	for _, session := range s.sessions {
		if err := session.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.sessions = nil
	return first
}

type mcpTool struct {
	session    *mcp.ClientSession
	remoteName string
	info       *schema.ToolInfo
}

func (t *mcpTool) Info(context.Context) (*schema.ToolInfo, error) {
	return t.info, nil
}

func (t *mcpTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	var args map[string]any
	if strings.TrimSpace(argumentsInJSON) != "" {
		if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
			return "", fmt.Errorf("decode MCP tool arguments: %w", err)
		}
	}
	if err := RequirePermission(ctx, PermissionRequest{
		Tool: t.info.Name, Action: "call", Detail: t.remoteName, Risk: RiskMedium,
	}); err != nil {
		return "", err
	}
	result, err := t.session.CallTool(ctx, &mcp.CallToolParams{Name: t.remoteName, Arguments: args})
	if err != nil {
		return "", fmt.Errorf("call MCP tool %q: %w", t.remoteName, err)
	}
	output := mcpResultText(result)
	if result.IsError {
		raw, marshalErr := json.Marshal(map[string]any{"is_error": true, "content": output})
		if marshalErr != nil {
			return "", marshalErr
		}
		return string(raw), nil
	}
	return output, nil
}

func mcpToolName(server, remote string) string {
	return "mcp__" + sanitizeMCPName(server) + "__" + sanitizeMCPName(remote)
}

func sanitizeMCPName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func mcpToolInfo(name string, remote *mcp.Tool) (*schema.ToolInfo, error) {
	info := &schema.ToolInfo{Name: name, Desc: strings.TrimSpace(remote.Description)}
	if info.Desc == "" {
		info.Desc = "Call the external MCP tool " + remote.Name
	}
	if remote.InputSchema == nil {
		return info, nil
	}
	raw, err := json.Marshal(remote.InputSchema)
	if err != nil {
		return nil, err
	}
	var input jsonschema.Schema
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&input)
	return info, nil
}

func mcpResultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
			continue
		}
		if raw, err := json.Marshal(content); err == nil {
			parts = append(parts, string(raw))
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if result.StructuredContent != nil {
		if raw, err := json.Marshal(result.StructuredContent); err == nil {
			return string(raw)
		}
	}
	return ""
}
