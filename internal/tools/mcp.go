package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"eino-local-assistant/internal/mcpoauth"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// MCPServerOptions describes one explicitly configured MCP server. An empty
// Type is kept as a stdio default for embedding callers using the older API.
type MCPServerOptions struct {
	Name              string
	Type              string
	Command           string
	Args              []string
	WorkingDir        string
	Env               map[string]string
	URL               string
	BearerTokenEnvVar string
	OAuth             bool
	OAuthTokenLoader  MCPOAuthTokenLoader
	ConnectTimeout    time.Duration
}

// MCPOAuthTokenLoader makes an OAuth-enabled embedding explicit and keeps
// system-keyring access out of callers that supply their own credential store.
type MCPOAuthTokenLoader func(serverName, endpoint string) (*oauth2.Token, error)

const (
	mcpTransportStdio          = "stdio"
	mcpTransportStreamableHTTP = "streamable_http"
)

// MCPConnectionOptions gives external tools the same runtime approval state
// as shell and apply_patch. Leaving it unset preserves the library-level
// context permission hook used by embedding callers.
type MCPConnectionOptions struct {
	Approval      ApprovalMode
	ApprovalState *ApprovalState
	Approver      Approver
	SessionAllows *SessionAllowlist
	SessionDenies *SessionDenylist
}

func (o MCPConnectionOptions) usesRuntimeApproval() bool {
	return o.Approval != "" || o.ApprovalState != nil || o.Approver != nil || o.SessionAllows != nil || o.SessionDenies != nil
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
func ConnectMCPServers(ctx context.Context, servers []MCPServerOptions, connectionOptions ...MCPConnectionOptions) (*MCPToolSet, error) {
	options := MCPConnectionOptions{}
	if len(connectionOptions) > 0 {
		options = connectionOptions[0]
	}
	set := &MCPToolSet{}
	seen := make(map[string]struct{})
	for _, opts := range servers {
		name := strings.TrimSpace(opts.Name)
		if name == "" {
			set.Close()
			return nil, errors.New("MCP server name is required")
		}
		transport, err := mcpClientTransport(opts)
		if err != nil {
			set.Close()
			return nil, fmt.Errorf("configure MCP server %q: %w", name, err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "eino-local-assistant", Version: "dev"}, nil)
		serverCtx := ctx
		cancel := func() {}
		if opts.ConnectTimeout > 0 {
			serverCtx, cancel = context.WithTimeout(ctx, opts.ConnectTimeout)
		}
		session, err := client.Connect(serverCtx, transport, nil)
		if err != nil {
			cancel()
			set.Close()
			return nil, fmt.Errorf("connect MCP server %q: %w", name, err)
		}
		set.sessions = append(set.sessions, session)
		serverInfo := MCPServerInfo{Name: name}
		cursor := ""
		for {
			result, listErr := session.ListTools(serverCtx, &mcp.ListToolsParams{Cursor: cursor})
			if listErr != nil {
				cancel()
				set.Close()
				return nil, fmt.Errorf("list MCP tools from %q: %w", name, listErr)
			}
			for _, remote := range result.Tools {
				if remote == nil || strings.TrimSpace(remote.Name) == "" {
					continue
				}
				toolName := mcpToolName(name, remote.Name)
				if _, exists := seen[toolName]; exists {
					cancel()
					set.Close()
					return nil, fmt.Errorf("duplicate MCP tool name %q", toolName)
				}
				info, infoErr := mcpToolInfo(toolName, remote)
				if infoErr != nil {
					cancel()
					set.Close()
					return nil, fmt.Errorf("decode MCP tool %q: %w", remote.Name, infoErr)
				}
				seen[toolName] = struct{}{}
				set.Tools = append(set.Tools, &mcpTool{session: session, serverName: name, remoteName: remote.Name, info: info, options: options})
				serverInfo.ToolCount++
			}
			cursor = strings.TrimSpace(result.NextCursor)
			if cursor == "" {
				break
			}
		}
		cancel()
		set.Servers = append(set.Servers, serverInfo)
	}
	return set, nil
}

func mcpClientTransport(opts MCPServerOptions) (mcp.Transport, error) {
	transport := strings.TrimSpace(opts.Type)
	if transport == "" {
		transport = mcpTransportStdio
	}
	switch transport {
	case mcpTransportStdio:
		command := strings.TrimSpace(opts.Command)
		if command == "" {
			return nil, errors.New("stdio MCP command is required")
		}
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
		return &mcp.CommandTransport{Command: cmd}, nil
	case mcpTransportStreamableHTTP:
		endpoint, err := validStreamableMCPEndpoint(opts.URL)
		if err != nil {
			return nil, err
		}
		if opts.OAuth && strings.TrimSpace(opts.BearerTokenEnvVar) != "" {
			return nil, errors.New("MCP OAuth and bearer token environment variables cannot be used together")
		}
		var client *http.Client
		var oauthHandler *storedMCPOAuthHandler
		if opts.OAuth {
			token, tokenErr := loadMCPStoredOAuthToken(opts, endpoint)
			if tokenErr != nil {
				return nil, tokenErr
			}
			if !token.Valid() {
				return nil, fmt.Errorf("MCP OAuth credential for server %q has expired; run %q", opts.Name, "eino mcp login "+opts.Name)
			}
			client = newMCPNoRedirectHTTPClient()
			oauthHandler = newStoredMCPOAuthHandler(opts.Name, token)
		}
		if envVar := strings.TrimSpace(opts.BearerTokenEnvVar); envVar != "" {
			token, ok := os.LookupEnv(envVar)
			if !ok || token == "" {
				return nil, fmt.Errorf("MCP bearer token environment variable %q is not set or is empty", envVar)
			}
			client = newMCPBearerTokenHTTPClient(token)
		}
		streamable := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client}
		if oauthHandler != nil {
			streamable.OAuthHandler = oauthHandler
		}
		return streamable, nil
	default:
		return nil, fmt.Errorf("unsupported MCP transport %q", transport)
	}
}

func loadMCPStoredOAuthToken(opts MCPServerOptions, endpoint string) (*oauth2.Token, error) {
	loader := opts.OAuthTokenLoader
	if loader == nil {
		loader = func(serverName, serverEndpoint string) (*oauth2.Token, error) {
			return mcpoauth.NewSystemStore().Load(serverName, serverEndpoint)
		}
	}
	token, err := loader(strings.TrimSpace(opts.Name), endpoint)
	if errors.Is(err, mcpoauth.ErrNotFound) || errors.Is(err, mcpoauth.ErrEndpointMismatch) {
		return nil, fmt.Errorf("MCP OAuth credential is unavailable for server %q; run %q", opts.Name, "eino mcp login "+opts.Name)
	}
	if err != nil {
		return nil, fmt.Errorf("load MCP OAuth credential for server %q: %w", opts.Name, err)
	}
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return nil, fmt.Errorf("MCP OAuth credential is invalid for server %q; run %q", opts.Name, "eino mcp login "+opts.Name)
	}
	return token, nil
}

func newMCPBearerTokenHTTPClient(token string) *http.Client {
	return &http.Client{
		Transport: bearerTokenRoundTripper{base: http.DefaultTransport, token: token},
		// A transport-level header injector would otherwise attach the token to a
		// redirect target. Remote MCP endpoints must be addressed directly.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newMCPNoRedirectHTTPClient() *http.Client {
	return &http.Client{
		Transport: http.DefaultTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type bearerTokenRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (t bearerTokenRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

// storedMCPOAuthHandler sends a saved token but never begins an interactive
// flow. Runtime 401 responses must direct the user to explicit mcp login.
type storedMCPOAuthHandler struct {
	serverName string
	token      *oauth2.Token
}

func newStoredMCPOAuthHandler(serverName string, token *oauth2.Token) *storedMCPOAuthHandler {
	tokenCopy := *token
	return &storedMCPOAuthHandler{serverName: strings.TrimSpace(serverName), token: &tokenCopy}
}

func (h *storedMCPOAuthHandler) TokenSource(context.Context) (oauth2.TokenSource, error) {
	return oauth2.StaticTokenSource(h.token), nil
}

func (h *storedMCPOAuthHandler) Authorize(_ context.Context, _ *http.Request, response *http.Response) error {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return fmt.Errorf("MCP OAuth credential was rejected for server %q; run %q", h.serverName, "eino mcp login "+h.serverName)
}

func validStreamableMCPEndpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return "", errors.New("streamable HTTP MCP URL is required")
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid streamable HTTP MCP URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("streamable HTTP MCP URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return "", errors.New("streamable HTTP MCP URL must include a host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("streamable HTTP MCP URL must not include credentials, a query, or a fragment")
	}
	return endpoint, nil
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
	serverName string
	remoteName string
	info       *schema.ToolInfo
	options    MCPConnectionOptions
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
	if err := t.authorize(ctx); err != nil {
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

func (t *mcpTool) authorize(ctx context.Context) error {
	if !t.options.usesRuntimeApproval() {
		return RequirePermission(ctx, PermissionRequest{
			Tool: t.info.Name, Action: "call", Detail: t.remoteName, Risk: RiskMedium,
		})
	}
	key := "mcp:" + t.serverName + ":" + t.remoteName
	mode := effectiveApprovalMode(t.options.Approval, t.options.ApprovalState)
	if mode == ApprovalNever || isYoloApprovalMode(mode) {
		return nil
	}
	if mode == ApprovalPlan || (t.options.SessionDenies != nil && t.options.SessionDenies.Contains(key)) {
		return fmt.Errorf("%w: %s", ErrPermissionDenied, t.info.Name)
	}
	if t.options.SessionAllows != nil && t.options.SessionAllows.Contains(key) {
		return nil
	}
	if t.options.Approver == nil {
		return fmt.Errorf("%w: %s", ErrPermissionDenied, t.info.Name)
	}
	resp, err := t.options.Approver.Request(ctx, ApprovalRequest{
		Tool:         t.info.Name,
		Command:      "MCP " + t.serverName + "." + t.remoteName,
		Reason:       "MCP tool calls can invoke external services",
		RuleID:       "mcp",
		RuleKey:      key,
		AllowSession: true,
	})
	if err != nil {
		return err
	}
	switch resp.Action {
	case ApprovalOnce:
		return nil
	case ApprovalSession:
		if t.options.SessionAllows != nil {
			t.options.SessionAllows.Allow(key)
		}
		return nil
	case ApprovalDeny:
		if t.options.SessionDenies != nil {
			t.options.SessionDenies.Deny(key)
		}
		return fmt.Errorf("%w: %s", ErrPermissionDenied, t.info.Name)
	default:
		return fmt.Errorf("%w: %s", ErrPermissionDenied, t.info.Name)
	}
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
