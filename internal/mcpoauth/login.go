package mcpoauth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

const oauthCallbackPath = "/mcp-oauth/callback"

// LoginOptions controls the interactive part of an OAuth authorization-code
// flow. AuthorizationURL must present the URL to the person who invoked login.
type LoginOptions struct {
	AuthorizationURL func(context.Context, string) error
	HTTPClient       *http.Client
}

// Login acquires one MCP OAuth access token through metadata discovery,
// dynamic client registration, PKCE, and a loopback callback. It does not
// persist the token; callers must save it only after the full flow succeeds.
func Login(ctx context.Context, endpoint string, options LoginOptions) (*oauth2.Token, error) {
	endpoint, err := validateEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if options.AuthorizationURL == nil {
		return nil, errors.New("OAuth authorization URL handler is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	callback, err := newLoopbackCallback(options.AuthorizationURL)
	if err != nil {
		return nil, err
	}
	defer callback.Close()

	httpClient := oauthHTTPClient(options.HTTPClient)
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				ClientName:   "Eino Local Assistant",
				RedirectURIs: []string{callback.URL()},
			},
		},
		RedirectURL:              callback.URL(),
		AuthorizationCodeFetcher: callback.Fetch,
		Client:                   httpClient,
		// Refresh requests need durable client-registration state and a token
		// rotation policy. Do not request one until that lifecycle is implemented.
		RequestRefreshToken: false,
	})
	if err != nil {
		return nil, fmt.Errorf("configure MCP OAuth authorization: %w", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "eino-local-assistant", Version: "dev"}, nil)
	session, connectErr := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:     endpoint,
		HTTPClient:   httpClient,
		OAuthHandler: handler,
	}, nil)
	if session != nil {
		defer session.Close()
	}

	token, tokenErr := tokenFromHandler(ctx, handler)
	if tokenErr == nil {
		return token, nil
	}
	if connectErr != nil {
		return nil, fmt.Errorf("authorize MCP endpoint: %w", connectErr)
	}
	return nil, tokenErr
}

func tokenFromHandler(ctx context.Context, handler *auth.AuthorizationCodeHandler) (*oauth2.Token, error) {
	source, err := handler.TokenSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("read MCP OAuth token source: %w", err)
	}
	if source == nil {
		return nil, errors.New("MCP endpoint did not request OAuth authorization")
	}
	token, err := source.Token()
	if err != nil {
		return nil, fmt.Errorf("read MCP OAuth access token: %w", err)
	}
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return nil, errors.New("MCP OAuth authorization returned an empty access token")
	}
	return token, nil
}

func validateEndpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil {
		return "", fmt.Errorf("invalid MCP OAuth endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("MCP OAuth endpoint must use http or https")
	}
	if parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("MCP OAuth endpoint must be an absolute URL without credentials, query, or fragment")
	}
	return endpoint, nil
}

func oauthHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	// Neither MCP bearer requests nor OAuth metadata/token requests should be
	// redirected to a different endpoint implicitly.
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}

type loopbackCallback struct {
	listener net.Listener
	server   *http.Server
	results  chan callbackResult
	serveErr chan error
	report   func(context.Context, string) error
	close    sync.Once
}

type callbackResult struct {
	result *auth.AuthorizationResult
	err    error
}

func newLoopbackCallback(report func(context.Context, string) error) (*loopbackCallback, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen for MCP OAuth callback: %w", err)
	}
	callback := &loopbackCallback{
		listener: listener,
		results:  make(chan callbackResult, 1),
		serveErr: make(chan error, 1),
		report:   report,
	}
	mux := http.NewServeMux()
	mux.HandleFunc(oauthCallbackPath, callback.handle)
	callback.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := callback.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			callback.serveErr <- serveErr
		}
	}()
	return callback, nil
}

func (c *loopbackCallback) URL() string {
	return "http://" + c.listener.Addr().String() + oauthCallbackPath
}

func (c *loopbackCallback) Fetch(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
	if args == nil || strings.TrimSpace(args.URL) == "" {
		return nil, errors.New("OAuth authorization URL is empty")
	}
	if err := c.report(ctx, args.URL); err != nil {
		return nil, err
	}
	select {
	case result := <-c.results:
		return result.result, result.err
	case serveErr := <-c.serveErr:
		return nil, fmt.Errorf("serve MCP OAuth callback: %w", serveErr)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *loopbackCallback) handle(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !isLoopbackRemote(request.RemoteAddr) {
		http.Error(writer, "loopback callback required", http.StatusForbidden)
		return
	}

	values := request.URL.Query()
	result := callbackResult{result: &auth.AuthorizationResult{
		Code:  values.Get("code"),
		State: values.Get("state"),
		Iss:   values.Get("iss"),
	}}
	if values.Get("error") != "" {
		result.err = errors.New("OAuth authorization was denied or failed")
	} else if result.result.Code == "" || result.result.State == "" {
		result.err = errors.New("OAuth callback did not contain a code and state")
	}
	select {
	case c.results <- result:
	default:
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte("<!doctype html><title>MCP OAuth complete</title><p>You can close this window and return to Eino.</p>"))
}

func (c *loopbackCallback) Close() {
	if c == nil {
		return
	}
	c.close.Do(func() {
		_ = c.server.Close()
	})
}

func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
