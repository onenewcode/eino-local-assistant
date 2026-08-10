package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/mcpoauth"

	"golang.org/x/oauth2"
)

func TestLoginMCPServerStoresCredentialEnablesRuntimeAndRedactsToken(t *testing.T) {
	configPath := writeMCPListConfig(t, `
[mcp]

[[mcp.servers]]
name = "remote"
type = "streamable_http"
url = "https://mcp.example.test/v1"
`)
	store := &recordingMCPOAuthStore{}
	var stdout, stderr bytes.Buffer
	deps := mcpOAuthCommandDeps{
		login: func(ctx context.Context, endpoint string, options mcpoauth.LoginOptions) (*oauth2.Token, error) {
			if endpoint != "https://mcp.example.test/v1" {
				t.Fatalf("OAuth endpoint = %q", endpoint)
			}
			if err := options.AuthorizationURL(ctx, "https://authorize.example.test/?state=temporary"); err != nil {
				t.Fatalf("AuthorizationURL() error = %v", err)
			}
			return &oauth2.Token{AccessToken: "access-token-should-not-print"}, nil
		},
		newStore: func() mcpOAuthCredentialStore { return store },
		openBrowser: func(string) error {
			t.Fatal("--no-browser login attempted to open a browser")
			return nil
		},
	}
	if err := loginMCPServer(context.Background(), configPath, " remote ", time.Minute, true, &stdout, &stderr, deps); err != nil {
		t.Fatalf("loginMCPServer() error = %v", err)
	}
	if store.savedServer != "remote" || store.savedEndpoint != "https://mcp.example.test/v1" || store.savedToken == nil || store.savedToken.AccessToken != "access-token-should-not-print" {
		t.Fatalf("saved OAuth credential = %#v", store)
	}
	updated, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.MCP.Servers) != 1 || !updated.MCP.Servers[0].OAuth {
		t.Fatalf("OAuth config after login = %#v", updated.MCP.Servers)
	}
	output := stdout.String() + stderr.String()
	if !strings.Contains(output, "https://authorize.example.test/") || !strings.Contains(output, `Logged in to MCP server "remote".`) || strings.Contains(output, "access-token-should-not-print") {
		t.Fatalf("login output = %q", output)
	}
}

func TestLoginMCPServerRemovesFreshCredentialWhenConfigUpdateFails(t *testing.T) {
	target := writeMCPListConfig(t, `
[mcp]

[[mcp.servers]]
name = "remote"
type = "streamable_http"
url = "https://mcp.example.test/v1"
`)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.Symlink(target, configPath); err != nil {
		t.Fatal(err)
	}
	store := &recordingMCPOAuthStore{}
	deps := mcpOAuthCommandDeps{
		login: func(context.Context, string, mcpoauth.LoginOptions) (*oauth2.Token, error) {
			return &oauth2.Token{AccessToken: "not-printed"}, nil
		},
		newStore: func() mcpOAuthCredentialStore { return store },
	}
	err := loginMCPServer(context.Background(), configPath, "remote", time.Minute, true, &bytes.Buffer{}, &bytes.Buffer{}, deps)
	if err == nil || !strings.Contains(err.Error(), "symbolic-link") || store.deleteCalls != 1 {
		t.Fatalf("login error=%v delete_calls=%d", err, store.deleteCalls)
	}
}

func TestMCPLoginAndLogoutRejectIncorrectServerKinds(t *testing.T) {
	for name, extra := range map[string]string{
		"local": `
[mcp]

[[mcp.servers]]
name = "server"
command = "mcp-server"
`,
		"bearer": `
[mcp]

[[mcp.servers]]
name = "server"
type = "streamable_http"
url = "https://mcp.example.test"
bearer_token_env_var = "EINO_MCP_TOKEN"
`,
	} {
		t.Run(name, func(t *testing.T) {
			configPath := writeMCPListConfig(t, extra)
			deps := mcpOAuthCommandDeps{newStore: func() mcpOAuthCredentialStore { return &recordingMCPOAuthStore{} }}
			loginErr := loginMCPServer(context.Background(), configPath, "server", time.Minute, true, &bytes.Buffer{}, &bytes.Buffer{}, deps)
			if loginErr == nil {
				t.Fatal("login unexpectedly succeeded")
			}
			if !strings.Contains(loginErr.Error(), "does not support OAuth") && !strings.Contains(loginErr.Error(), "bearer_token_env_var") {
				t.Fatalf("login error = %v", loginErr)
			}
			logoutErr := logoutMCPServer(configPath, "server", &bytes.Buffer{}, deps)
			if logoutErr == nil {
				t.Fatal("logout unexpectedly succeeded")
			}
			if !strings.Contains(logoutErr.Error(), "does not support OAuth") && !strings.Contains(logoutErr.Error(), "bearer_token_env_var") {
				t.Fatalf("logout error = %v", logoutErr)
			}
		})
	}
}

func TestLogoutMCPServerClearsStoredCredentialAndIsIdempotent(t *testing.T) {
	configPath := writeMCPListConfig(t, `
[mcp]

[[mcp.servers]]
name = "remote"
type = "streamable_http"
url = "https://mcp.example.test/v1"
oauth = true
`)
	store := &recordingMCPOAuthStore{}
	deps := mcpOAuthCommandDeps{newStore: func() mcpOAuthCredentialStore { return store }}
	var stdout bytes.Buffer
	if err := logoutMCPServer(configPath, "remote", &stdout, deps); err != nil {
		t.Fatalf("logoutMCPServer() error = %v", err)
	}
	updated, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MCP.Servers[0].OAuth || store.deleteCalls != 1 || strings.Contains(stdout.String(), "access") {
		t.Fatalf("logout state = config=%#v store=%#v output=%q", updated.MCP.Servers[0], store, stdout.String())
	}
	store.deleteErr = mcpoauth.ErrNotFound
	stdout.Reset()
	if err := logoutMCPServer(configPath, "remote", &stdout, deps); err != nil {
		t.Fatalf("idempotent logout error = %v", err)
	}
	if !strings.Contains(stdout.String(), "No stored OAuth credential") || store.deleteCalls != 2 {
		t.Fatalf("idempotent logout = store=%#v output=%q", store, stdout.String())
	}
}

func TestMCPLoginAndLogoutCommandHelpAndArguments(t *testing.T) {
	for _, verb := range []string{"login", "logout"} {
		stdout, _, err := executeMCPCommandForTest("mcp", verb, "--help")
		if err != nil || !strings.Contains(stdout, "OAuth") || !strings.Contains(stdout, "<name>") {
			t.Fatalf("mcp %s help = %q, err=%v", verb, stdout, err)
		}
		for _, args := range [][]string{{"mcp", verb}, {"mcp", verb, "first", "second"}, {"mcp", verb, " "}} {
			if _, _, commandErr := executeMCPCommandForTest(args...); commandErr == nil {
				t.Fatalf("mcp %s %q should reject invalid arguments", verb, args)
			}
		}
	}
}

type recordingMCPOAuthStore struct {
	savedServer   string
	savedEndpoint string
	savedToken    *oauth2.Token
	deleteCalls   int
	saveErr       error
	deleteErr     error
}

func (s *recordingMCPOAuthStore) Save(serverName, endpoint string, token *oauth2.Token) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.savedServer = serverName
	s.savedEndpoint = endpoint
	s.savedToken = token
	return nil
}

func (s *recordingMCPOAuthStore) Delete(string) error {
	s.deleteCalls++
	return s.deleteErr
}

var _ mcpOAuthCredentialStore = (*recordingMCPOAuthStore)(nil)
