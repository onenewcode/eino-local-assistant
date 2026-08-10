package mcpoauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"golang.org/x/oauth2"
)

// ErrRefreshUnavailable reports an expired token that cannot be rotated with
// securely persisted client and refresh-token state.
var ErrRefreshUnavailable = errors.New("MCP OAuth credential cannot be refreshed")

// CredentialWriter persists a rotated credential in the same secure store that
// supplied it. Returning an error fails closed instead of using a token that
// could be lost after refresh-token rotation.
type CredentialWriter func(Credential) error

// NewTokenSource creates an endpoint-bound OAuth token source. It preserves
// valid legacy access tokens, but only refreshes when the client identity,
// refresh token, secure writer, and HTTP client are all present.
func NewTokenSource(credential *Credential, httpClient *http.Client, write CredentialWriter) (oauth2.TokenSource, error) {
	if credential == nil || credential.Token == nil || strings.TrimSpace(credential.Token.AccessToken) == "" {
		return nil, ErrInvalidCredential
	}
	profile := credential.Refresh
	if err := validateRefreshProfile(profile); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCredential, err)
	}
	if profile == nil || strings.TrimSpace(credential.Token.RefreshToken) == "" || write == nil {
		if credential.Token.Valid() {
			return oauth2.StaticTokenSource(copyToken(credential.Token)), nil
		}
		return nil, ErrRefreshUnavailable
	}
	if httpClient == nil {
		return nil, errors.New("MCP OAuth refresh HTTP client is unavailable")
	}

	config := oauth2.Config{
		ClientID:     profile.ClientID,
		ClientSecret: profile.ClientSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL:  profile.TokenURL,
			AuthStyle: refreshAuthStyle(profile.AuthStyle),
		},
	}
	refreshCtx := context.WithValue(context.Background(), oauth2.HTTPClient, httpClient)
	return &persistingTokenSource{
		source:     config.TokenSource(refreshCtx, copyToken(credential.Token)),
		credential: Credential{Token: copyToken(credential.Token), Refresh: copyRefreshProfile(profile)},
		write:      write,
	}, nil
}

type persistingTokenSource struct {
	mu         sync.Mutex
	source     oauth2.TokenSource
	credential Credential
	write      CredentialWriter
}

func (s *persistingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, err := s.source.Token()
	if err != nil {
		return nil, err
	}
	if tokensEqual(s.credential.Token, token) {
		return token, nil
	}
	updated := Credential{Token: copyToken(token), Refresh: copyRefreshProfile(s.credential.Refresh)}
	if err := s.write(updated); err != nil {
		return nil, fmt.Errorf("persist rotated MCP OAuth credential: %w", err)
	}
	s.credential = updated
	return token, nil
}

func RefreshProfileFromOAuthConfig(config *oauth2.Config) *RefreshProfile {
	if config == nil {
		return nil
	}
	return &RefreshProfile{
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		TokenURL:     config.Endpoint.TokenURL,
		AuthStyle:    refreshAuthStyleName(config.Endpoint.AuthStyle),
	}
}

func validateRefreshProfile(profile *RefreshProfile) error {
	if profile == nil {
		return nil
	}
	if strings.TrimSpace(profile.ClientID) == "" {
		return errors.New("MCP OAuth refresh client ID is empty")
	}
	endpoint, err := url.ParseRequestURI(strings.TrimSpace(profile.TokenURL))
	if err != nil || endpoint.Hostname() == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.Fragment != "" {
		return errors.New("MCP OAuth refresh token URL is invalid")
	}
	switch profile.AuthStyle {
	case "auto", "in_params", "in_header":
		return nil
	default:
		return errors.New("MCP OAuth refresh client authentication style is invalid")
	}
}

func refreshAuthStyle(style string) oauth2.AuthStyle {
	switch style {
	case "in_params":
		return oauth2.AuthStyleInParams
	case "in_header":
		return oauth2.AuthStyleInHeader
	default:
		return oauth2.AuthStyleAutoDetect
	}
}

func refreshAuthStyleName(style oauth2.AuthStyle) string {
	switch style {
	case oauth2.AuthStyleInParams:
		return "in_params"
	case oauth2.AuthStyleInHeader:
		return "in_header"
	default:
		return "auto"
	}
}

func tokensEqual(left, right *oauth2.Token) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.AccessToken == right.AccessToken &&
		left.TokenType == right.TokenType &&
		left.RefreshToken == right.RefreshToken &&
		left.Expiry.Equal(right.Expiry) &&
		left.ExpiresIn == right.ExpiresIn
}
