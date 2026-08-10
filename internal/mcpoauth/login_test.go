package mcpoauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLoginDiscoversMetadataUsesPKCEAndReceivesLoopbackCallback(t *testing.T) {
	const accessToken = "test-oauth-access-token"
	server := mcp.NewServer(&mcp.Implementation{Name: "oauth-test", Version: "1"}, nil)
	streamable := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})

	var baseURL string
	var metadata muString
	var registration muRegistration
	var tokenRequest muValues
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/mcp":
			if request.Header.Get("Authorization") != "Bearer "+accessToken {
				writer.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+baseURL+`/resource"`)
				http.Error(writer, "OAuth required", http.StatusUnauthorized)
				return
			}
			streamable.ServeHTTP(writer, request)
		case "/resource":
			writeOAuthJSON(t, writer, map[string]any{
				"resource":              baseURL + "/mcp",
				"authorization_servers": []string{baseURL},
			})
		case "/.well-known/oauth-authorization-server":
			writeOAuthJSON(t, writer, map[string]any{
				"issuer":                           baseURL,
				"authorization_endpoint":           baseURL + "/authorize",
				"token_endpoint":                   baseURL + "/token",
				"registration_endpoint":            baseURL + "/register",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/register":
			var payload struct {
				ClientName   string   `json:"client_name"`
				RedirectURIs []string `json:"redirect_uris"`
			}
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode registration: %v", err)
				http.Error(writer, "invalid registration", http.StatusBadRequest)
				return
			}
			registration.Store(payload.ClientName, payload.RedirectURIs)
			writeOAuthJSON(t, writer, map[string]any{"client_id": "eino-test-client", "token_endpoint_auth_method": "none"})
		case "/token":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
				http.Error(writer, "invalid token request", http.StatusBadRequest)
				return
			}
			tokenRequest.Store(request.Form)
			writeOAuthJSON(t, writer, map[string]any{"access_token": accessToken, "token_type": "Bearer", "expires_in": 3600})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer httpServer.Close()
	baseURL = httpServer.URL

	token, err := Login(context.Background(), baseURL+"/mcp", LoginOptions{
		AuthorizationURL: func(_ context.Context, authorizationURL string) error {
			metadata.Store(authorizationURL)
			parsed, parseErr := url.Parse(authorizationURL)
			if parseErr != nil {
				return parseErr
			}
			query := parsed.Query()
			callback, getErr := http.Get(query.Get("redirect_uri") + "?code=authorization-code&state=" + url.QueryEscape(query.Get("state")))
			if getErr != nil {
				return getErr
			}
			return callback.Body.Close()
		},
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if token.AccessToken != accessToken {
		t.Fatalf("access token = %q", token.AccessToken)
	}
	parsedAuthorizationURL, err := url.Parse(metadata.Load())
	if err != nil {
		t.Fatalf("authorization URL = %q: %v", metadata.Load(), err)
	}
	query := parsedAuthorizationURL.Query()
	if parsedAuthorizationURL.Path != "/authorize" || query.Get("resource") != baseURL+"/mcp" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" || query.Get("state") == "" {
		t.Fatalf("authorization URL = %s", metadata.Load())
	}
	clientName, redirectURIs := registration.Load()
	if clientName != "Eino Local Assistant" || len(redirectURIs) != 1 || !strings.HasPrefix(redirectURIs[0], "http://127.0.0.1:") || !strings.HasSuffix(redirectURIs[0], oauthCallbackPath) {
		t.Fatalf("dynamic registration = name=%q redirect_uris=%#v", clientName, redirectURIs)
	}
	form := tokenRequest.Load()
	if form.Get("code") != "authorization-code" || form.Get("resource") != baseURL+"/mcp" || form.Get("code_verifier") == "" || form.Get("client_id") != "eino-test-client" {
		t.Fatalf("token request = %v", form)
	}
}

func TestLoopbackCallbackRejectsMissingResponseFieldsAndStopsAtContextCancellation(t *testing.T) {
	callback, err := newLoopbackCallback(func(context.Context, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()

	missing, err := http.Get(callback.URL() + "?code=only-code")
	if err != nil {
		t.Fatal(err)
	}
	if closeErr := missing.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, fetchErr := callback.Fetch(context.Background(), &authorizationArgs); fetchErr == nil || !strings.Contains(fetchErr.Error(), "code and state") {
		t.Fatalf("Fetch(missing state) error = %v", fetchErr)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, fetchErr := callback.Fetch(cancelled, &authorizationArgs); fetchErr == nil || !errors.Is(fetchErr, context.Canceled) {
		t.Fatalf("Fetch(cancelled) error = %v", fetchErr)
	}
}

func TestOAuthHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	response, err := oauthHTTPClient(nil).Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || targetRequests.Load() != 0 {
		t.Fatalf("redirect response=%d target_requests=%d", response.StatusCode, targetRequests.Load())
	}
}

var authorizationArgs = auth.AuthorizationArgs{URL: "https://authorize.example.test"}

type muString struct {
	mu    sync.Mutex
	value string
}

func (s *muString) Store(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = value
}

func (s *muString) Load() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

type muRegistration struct {
	mu           sync.Mutex
	clientName   string
	redirectURIs []string
}

func (r *muRegistration) Store(clientName string, redirectURIs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clientName = clientName
	r.redirectURIs = append([]string(nil), redirectURIs...)
}

func (r *muRegistration) Load() (string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clientName, append([]string(nil), r.redirectURIs...)
}

type muValues struct {
	mu     sync.Mutex
	values url.Values
}

func (v *muValues) Store(values url.Values) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values = values
}

func (v *muValues) Load() url.Values {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.values
}

func writeOAuthJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode OAuth response: %v", err)
	}
}
