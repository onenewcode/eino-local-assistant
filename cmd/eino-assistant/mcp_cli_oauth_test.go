package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
			options.OnRefreshProfile(&mcpoauth.RefreshProfile{ClientID: "client-id", ClientSecret: "client-secret-should-not-print", TokenURL: "https://issuer.example.test/token", AuthStyle: "in_params"})
			return &oauth2.Token{AccessToken: "access-token-should-not-print", RefreshToken: "refresh-token-should-not-print"}, nil
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
	if store.savedServer != "remote" || store.savedEndpoint != "https://mcp.example.test/v1" || store.savedToken == nil || store.savedToken.AccessToken != "access-token-should-not-print" || store.savedCredential.Refresh == nil || store.savedCredential.Refresh.ClientSecret != "client-secret-should-not-print" {
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
	if !strings.Contains(output, "https://authorize.example.test/") || !strings.Contains(output, `Logged in to MCP server "remote".`) || strings.Contains(output, "access-token-should-not-print") || strings.Contains(output, "refresh-token-should-not-print") || strings.Contains(output, "client-secret-should-not-print") {
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

func TestMCPAuthStatusReportsOnlyRedactedLocalKeyringState(t *testing.T) {
	configPath := writeMCPListConfig(t, `
[mcp]

[[mcp.servers]]
name = "available"
type = "streamable_http"
url = "https://available.example.test/mcp"
oauth = true

[[mcp.servers]]
name = "expired"
type = "streamable_http"
url = "https://expired.example.test/mcp"
oauth = true

[[mcp.servers]]
name = "missing"
type = "streamable_http"
url = "https://missing.example.test/mcp"
oauth = true

[[mcp.servers]]
name = "mismatch"
type = "streamable_http"
url = "https://mismatch.example.test/mcp"
oauth = true

[[mcp.servers]]
name = "invalid"
type = "streamable_http"
url = "https://invalid.example.test/mcp"
oauth = true

[[mcp.servers]]
name = "not-configured"
type = "streamable_http"
url = "https://anonymous.example.test/mcp"
`)
	store := &statusMCPOAuthStore{results: map[string]mcpOAuthStatusResult{
		"available": {token: &oauth2.Token{AccessToken: "never-print-available", Expiry: time.Now().Add(time.Hour)}},
		"expired":   {token: &oauth2.Token{AccessToken: "never-print-expired", Expiry: time.Now().Add(-time.Hour)}},
		"missing":   {err: mcpoauth.ErrNotFound},
		"mismatch":  {err: mcpoauth.ErrEndpointMismatch},
		"invalid":   {err: mcpoauth.ErrInvalidCredential},
	}}
	deps := mcpOAuthCommandDeps{newStore: func() mcpOAuthCredentialStore { return store }}

	entries, err := mcpOAuthStatusEntries(configPath, deps)
	if err != nil {
		t.Fatal(err)
	}
	if got := mapMCPAuthStatuses(entries); !reflect.DeepEqual(got, map[string]string{
		"available": "available", "expired": "expired", "missing": "missing", "mismatch": "endpoint_mismatch", "invalid": "invalid",
	}) {
		t.Fatalf("OAuth status = %#v", got)
	}
	if entries[0].ExpiresAt == nil || entries[1].ExpiresAt == nil || len(store.loads) != 5 {
		t.Fatalf("OAuth status details = entries=%#v loads=%#v", entries, store.loads)
	}

	var textOutput bytes.Buffer
	if err := listMCPAuthStatus(configPath, false, &textOutput, deps); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "available", "expired", "missing", "endpoint_mismatch", "invalid"} {
		if !strings.Contains(textOutput.String(), want) {
			t.Fatalf("auth list missing %q:\n%s", want, textOutput.String())
		}
	}
	if strings.Contains(textOutput.String(), "never-print") {
		t.Fatalf("auth list leaked credential: %s", textOutput.String())
	}

	var jsonOutput bytes.Buffer
	if err := listMCPAuthStatus(configPath, true, &jsonOutput, deps); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(jsonOutput.String(), "never-print") {
		t.Fatalf("auth list JSON leaked credential: %s", jsonOutput.String())
	}
	var jsonEntries []mcpOAuthStatusEntry
	if err := json.Unmarshal(jsonOutput.Bytes(), &jsonEntries); err != nil || len(jsonEntries) != 5 {
		t.Fatalf("auth list JSON = %s, err=%v", jsonOutput.String(), err)
	}

	var getOutput bytes.Buffer
	if err := getMCPAuthStatus(configPath, "not-configured", false, &getOutput, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(getOutput.String(), "not_configured") || strings.Contains(getOutput.String(), "never-print") {
		t.Fatalf("not-configured OAuth get = %q", getOutput.String())
	}
}

func TestMCPAuthStatusHandlesKeyringFailuresAndCommandHelp(t *testing.T) {
	configPath := writeMCPListConfig(t, `
[mcp]

[[mcp.servers]]
name = "remote"
type = "streamable_http"
url = "https://mcp.example.test/mcp"
oauth = true

[[mcp.servers]]
name = "local"
command = "mcp-server"
`)
	deps := mcpOAuthCommandDeps{newStore: func() mcpOAuthCredentialStore {
		return &statusMCPOAuthStore{results: map[string]mcpOAuthStatusResult{"remote": {err: errors.New("keyring unavailable")}}}
	}}
	entry, err := mcpOAuthStatusForServer(configPath, "remote", deps)
	if err != nil || entry.Status != "keyring_unavailable" {
		t.Fatalf("keyring status = %#v, err=%v", entry, err)
	}
	if _, err := mcpOAuthStatusForServer(configPath, "local", deps); err == nil || !strings.Contains(err.Error(), "does not support OAuth") {
		t.Fatalf("local auth status error = %v", err)
	}
	if _, err := mcpOAuthStatusForServer(configPath, "missing", deps); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing auth status error = %v", err)
	}
	for _, args := range [][]string{{"mcp", "auth", "--help"}, {"mcp", "auth", "list", "--help"}, {"mcp", "auth", "get", "--help"}} {
		stdout, _, commandErr := executeMCPCommandForTest(args...)
		if commandErr != nil || !strings.Contains(stdout, "OAuth") {
			t.Fatalf("%v help = %q, err=%v", args, stdout, commandErr)
		}
	}
	for _, args := range [][]string{{"mcp", "auth", "list", "extra"}, {"mcp", "auth", "get"}, {"mcp", "auth", "get", "one", "two"}} {
		if _, _, commandErr := executeMCPCommandForTest(args...); commandErr == nil {
			t.Fatalf("%v should reject invalid arguments", args)
		}
	}
}

type recordingMCPOAuthStore struct {
	savedServer     string
	savedEndpoint   string
	savedToken      *oauth2.Token
	savedCredential mcpoauth.Credential
	loadToken       *oauth2.Token
	loadErr         error
	deleteCalls     int
	saveErr         error
	deleteErr       error
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

func (s *recordingMCPOAuthStore) SaveCredential(serverName, endpoint string, credential mcpoauth.Credential) error {
	s.savedCredential = credential
	return s.Save(serverName, endpoint, credential.Token)
}

func (s *recordingMCPOAuthStore) Load(string, string) (*oauth2.Token, error) {
	return s.loadToken, s.loadErr
}

func (s *recordingMCPOAuthStore) Delete(string) error {
	s.deleteCalls++
	return s.deleteErr
}

var _ mcpOAuthCredentialStore = (*recordingMCPOAuthStore)(nil)

type mcpOAuthStatusResult struct {
	token *oauth2.Token
	err   error
}

type statusMCPOAuthStore struct {
	results map[string]mcpOAuthStatusResult
	loads   []string
}

func (s *statusMCPOAuthStore) Save(string, string, *oauth2.Token) error {
	return errors.New("Save should not be called while inspecting OAuth status")
}

func (s *statusMCPOAuthStore) SaveCredential(string, string, mcpoauth.Credential) error {
	return errors.New("SaveCredential should not be called while inspecting OAuth status")
}

func (s *statusMCPOAuthStore) Load(serverName, endpoint string) (*oauth2.Token, error) {
	s.loads = append(s.loads, serverName+"@"+endpoint)
	result, found := s.results[serverName]
	if !found {
		return nil, mcpoauth.ErrNotFound
	}
	return result.token, result.err
}

func (s *statusMCPOAuthStore) Delete(string) error {
	return errors.New("Delete should not be called while inspecting OAuth status")
}

var _ mcpOAuthCredentialStore = (*statusMCPOAuthStore)(nil)

func mapMCPAuthStatuses(entries []mcpOAuthStatusEntry) map[string]string {
	statuses := make(map[string]string, len(entries))
	for _, entry := range entries {
		statuses[entry.Name] = entry.Status
	}
	return statuses
}
