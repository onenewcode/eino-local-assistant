package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eino-local-assistant/internal/mcpoauth"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func TestMCPServerProcess(t *testing.T) {
	if os.Getenv("EINO_MCP_HELPER") != "1" {
		return
	}
	if err := newMCPServerForTest().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		t.Fatal(err)
	}
}

func newMCPServerForTest() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "Echo a value"}, func(_ context.Context, _ *mcp.CallToolRequest, input struct {
		Value string `json:"value"`
	}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: input.Value}}}, nil, nil
	})
	return server
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

func TestConnectStreamableHTTPMCPServerDiscoversCallsAndClosesSession(t *testing.T) {
	const token = "test-bearer-token"
	t.Setenv("EINO_TEST_MCP_BEARER_TOKEN", token)
	server := newMCPServerForTest()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
	var deleteRequests atomic.Int32
	var missingAuthorization atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			missingAuthorization.Add(1)
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodDelete {
			deleteRequests.Add(1)
		}
		handler.ServeHTTP(w, request)
	}))
	defer httpServer.Close()

	set, err := ConnectMCPServers(context.Background(), []MCPServerOptions{{
		Name:              "remote",
		Type:              mcpTransportStreamableHTTP,
		URL:               httpServer.URL,
		BearerTokenEnvVar: "EINO_TEST_MCP_BEARER_TOKEN",
		ConnectTimeout:    time.Second,
	}})
	if err != nil {
		t.Fatalf("ConnectMCPServers() error = %v", err)
	}
	if len(set.Tools) != 1 || len(set.Servers) != 1 || set.Servers[0].Name != "remote" {
		set.Close()
		t.Fatalf("remote MCP discovery = tools=%d servers=%+v", len(set.Tools), set.Servers)
	}
	allowedCtx := WithPermissionHandler(context.Background(), func(context.Context, PermissionRequest) (bool, error) {
		return true, nil
	})
	output, err := set.Tools[0].InvokableRun(allowedCtx, `{"value":"remote hello"}`)
	if err != nil || output != "remote hello" {
		set.Close()
		t.Fatalf("remote MCP call output=%q err=%v", output, err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("close remote MCP session: %v", err)
	}
	if deleteRequests.Load() != 1 {
		t.Fatalf("remote session DELETE requests = %d, want 1", deleteRequests.Load())
	}
	if missingAuthorization.Load() != 0 {
		t.Fatalf("remote requests without bearer token = %d", missingAuthorization.Load())
	}
}

func TestMCPClientTransportRejectsInvalidStreamableHTTPEndpoints(t *testing.T) {
	for _, endpoint := range []string{"", "file:///tmp/mcp", "https://token@example.test/mcp", "https://example.test/mcp?token=secret"} {
		_, err := mcpClientTransport(MCPServerOptions{Type: mcpTransportStreamableHTTP, URL: endpoint})
		if err == nil {
			t.Fatalf("mcpClientTransport(%q) succeeded", endpoint)
		}
	}
}

func TestMCPClientTransportRejectsMissingBearerToken(t *testing.T) {
	t.Setenv("EINO_MCP_MISSING_TOKEN", "")
	_, err := mcpClientTransport(MCPServerOptions{
		Type:              mcpTransportStreamableHTTP,
		URL:               "https://mcp.example.test",
		BearerTokenEnvVar: "EINO_MCP_MISSING_TOKEN",
	})
	if err == nil || !strings.Contains(err.Error(), "EINO_MCP_MISSING_TOKEN") {
		t.Fatalf("missing bearer token error = %v", err)
	}
}

func TestConnectStreamableHTTPMCPServerUsesStoredOAuthCredential(t *testing.T) {
	const token = "test-oauth-token"
	server := newMCPServerForTest()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})
	var missingAuthorization atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+token {
			missingAuthorization.Add(1)
			http.Error(writer, "missing OAuth token", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()

	set, err := ConnectMCPServers(context.Background(), []MCPServerOptions{{
		Name: "remote-oauth", Type: mcpTransportStreamableHTTP, URL: httpServer.URL, OAuth: true, ConnectTimeout: time.Second,
		OAuthTokenLoader: func(serverName, endpoint string) (*oauth2.Token, error) {
			if serverName != "remote-oauth" || endpoint != httpServer.URL {
				t.Fatalf("OAuth token load = server=%q endpoint=%q", serverName, endpoint)
			}
			return &oauth2.Token{AccessToken: token, Expiry: time.Now().Add(time.Hour)}, nil
		},
	}})
	if err != nil {
		t.Fatalf("ConnectMCPServers(OAuth) error = %v", err)
	}
	defer set.Close()
	if len(set.Tools) != 1 || missingAuthorization.Load() != 0 {
		t.Fatalf("OAuth MCP set = tools=%d missing_authorization=%d", len(set.Tools), missingAuthorization.Load())
	}
}

func TestMCPClientTransportOAuthFailsClosedWithoutBrowserFallback(t *testing.T) {
	for name, options := range map[string]MCPServerOptions{
		"missing credential": {
			Name: "remote", Type: mcpTransportStreamableHTTP, URL: "https://mcp.example.test", OAuth: true,
			OAuthTokenLoader: func(string, string) (*oauth2.Token, error) {
				return nil, mcpoauth.ErrNotFound
			},
		},
		"expired credential": {
			Name: "remote", Type: mcpTransportStreamableHTTP, URL: "https://mcp.example.test", OAuth: true,
			OAuthTokenLoader: func(string, string) (*oauth2.Token, error) {
				return &oauth2.Token{AccessToken: "expired", Expiry: time.Now().Add(-time.Hour)}, nil
			},
		},
		"conflicting bearer": {
			Name: "remote", Type: mcpTransportStreamableHTTP, URL: "https://mcp.example.test", OAuth: true, BearerTokenEnvVar: "EINO_MCP_TOKEN",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := mcpClientTransport(options)
			if err == nil {
				t.Fatalf("mcpClientTransport() error = %v", err)
			}
			if name != "conflicting bearer" && !strings.Contains(err.Error(), "eino mcp login remote") {
				t.Fatalf("OAuth credential error = %v", err)
			}
			if name == "conflicting bearer" && !strings.Contains(err.Error(), "cannot be used together") {
				t.Fatalf("conflicting auth error = %v", err)
			}
		})
	}
}

func TestConnectMCPServerOAuthRejectionRequiresExplicitRelogin(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer stale-token" {
			t.Errorf("Authorization header = %q", request.Header.Get("Authorization"))
		}
		http.Error(writer, "expired", http.StatusUnauthorized)
	}))
	defer httpServer.Close()

	_, err := ConnectMCPServers(context.Background(), []MCPServerOptions{{
		Name: "remote", Type: mcpTransportStreamableHTTP, URL: httpServer.URL, OAuth: true,
		OAuthTokenLoader: func(string, string) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "stale-token", Expiry: time.Now().Add(time.Hour)}, nil
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "eino mcp login remote") {
		t.Fatalf("rejected OAuth connection error = %v", err)
	}
}

func TestMCPBearerTokenClientDoesNotFollowRedirects(t *testing.T) {
	const token = "test-bearer-token"
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	var sourceAuthorization atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") == "Bearer "+token {
			sourceAuthorization.Add(1)
		}
		http.Redirect(w, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	response, err := newMCPBearerTokenHTTPClient(token).Get(source.URL)
	if err != nil {
		t.Fatalf("GET redirect source: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || sourceAuthorization.Load() != 1 || targetRequests.Load() != 0 {
		t.Fatalf("redirect response=%d source_auth=%d target_requests=%d", response.StatusCode, sourceAuthorization.Load(), targetRequests.Load())
	}
}

func TestMCPOAuthHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	response, err := newMCPNoRedirectHTTPClient().Get(source.URL)
	if err != nil {
		t.Fatalf("GET redirect source: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || targetRequests.Load() != 0 {
		t.Fatalf("redirect response=%d target_requests=%d", response.StatusCode, targetRequests.Load())
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

func TestMCPToolRuntimeApprovalFailsClosedWithoutApprover(t *testing.T) {
	set, err := ConnectMCPServers(context.Background(), []MCPServerOptions{{
		Name: "test", Command: os.Args[0], Args: []string{"-test.run=TestMCPServerProcess"},
		Env: map[string]string{"EINO_MCP_HELPER": "1"},
	}}, MCPConnectionOptions{Approval: ApprovalOnRequest})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	_, err = set.Tools[0].InvokableRun(context.Background(), `{"value":"blocked"}`)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("runtime approval error = %v, want permission denied", err)
	}
}

func TestMCPToolRuntimeApprovalSessionAllow(t *testing.T) {
	allows := NewSessionAllowlist()
	approver := &recordingApprover{responses: []ApprovalAction{ApprovalSession}}
	set, err := ConnectMCPServers(context.Background(), []MCPServerOptions{{
		Name: "test", Command: os.Args[0], Args: []string{"-test.run=TestMCPServerProcess"},
		Env: map[string]string{"EINO_MCP_HELPER": "1"},
	}}, MCPConnectionOptions{
		Approval:      ApprovalOnRequest,
		Approver:      approver,
		SessionAllows: allows,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	for _, value := range []string{"first", "second"} {
		output, callErr := set.Tools[0].InvokableRun(context.Background(), `{"value":"`+value+`"}`)
		if callErr != nil || output != value {
			t.Fatalf("MCP call output=%q err=%v", output, callErr)
		}
	}
	requests := approver.Requests()
	if len(requests) != 1 {
		t.Fatalf("approval requests = %d, want 1", len(requests))
	}
	if request := requests[0]; request.Command != "MCP test.echo" || request.RuleKey != "mcp:test:echo" || !request.AllowSession {
		t.Fatalf("approval request = %+v", request)
	}
	if !allows.Contains("mcp:test:echo") {
		t.Fatal("session allow was not retained")
	}
}

func TestMCPToolRuntimeApprovalStateOverridesStaticMode(t *testing.T) {
	state, err := NewApprovalState(ApprovalNever)
	if err != nil {
		t.Fatal(err)
	}
	set, err := ConnectMCPServers(context.Background(), []MCPServerOptions{{
		Name: "test", Command: os.Args[0], Args: []string{"-test.run=TestMCPServerProcess"},
		Env: map[string]string{"EINO_MCP_HELPER": "1"},
	}}, MCPConnectionOptions{Approval: ApprovalOnRequest, ApprovalState: state})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	output, err := set.Tools[0].InvokableRun(context.Background(), `{"value":"allowed"}`)
	if err != nil || output != "allowed" {
		t.Fatalf("MCP call output=%q err=%v", output, err)
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
