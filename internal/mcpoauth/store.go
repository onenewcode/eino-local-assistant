// Package mcpoauth persists remote MCP OAuth access tokens in the OS keyring.
package mcpoauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

const keyringService = "eino-local-assistant/mcp-oauth"

var (
	// ErrNotFound reports that no stored OAuth credential exists for a server.
	ErrNotFound = errors.New("MCP OAuth credential is not stored")
	// ErrEndpointMismatch reports that a saved token belongs to an older server URL.
	ErrEndpointMismatch = errors.New("MCP OAuth credential belongs to a different endpoint")
	// ErrInvalidCredential reports an unreadable or structurally invalid keyring value.
	ErrInvalidCredential = errors.New("MCP OAuth credential is invalid")
)

// SecretBackend is the narrow OS-keyring contract needed by Store.
type SecretBackend interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

// Store owns OAuth credentials for configured remote MCP servers.
type Store struct {
	backend SecretBackend
}

// NewSystemStore uses the current operating system's keyring. It never
// downgrades to a regular file when the keyring is unavailable.
func NewSystemStore() *Store {
	return NewStore(systemKeyring{})
}

// NewStore constructs a Store with a supplied backend, mainly for tests.
func NewStore(backend SecretBackend) *Store {
	return &Store{backend: backend}
}

// Save writes one token after validating that it is bound to a configured
// server endpoint. The keyring receives the serialized secret, never TOML.
func (s *Store) Save(serverName, endpoint string, token *oauth2.Token) error {
	if err := validateServer(serverName, endpoint); err != nil {
		return err
	}
	if s == nil || s.backend == nil {
		return errors.New("MCP OAuth keyring is unavailable")
	}
	if token == nil || strings.TrimSpace(token.AccessToken) == "" {
		return errors.New("MCP OAuth access token is empty")
	}
	payload, err := json.Marshal(storedCredential{Version: 1, Endpoint: strings.TrimSpace(endpoint), Token: token})
	if err != nil {
		return fmt.Errorf("encode MCP OAuth credential: %w", err)
	}
	if err := s.backend.Set(keyringService, credentialKey(serverName), string(payload)); err != nil {
		return fmt.Errorf("store MCP OAuth credential: %w", err)
	}
	return nil
}

// Load returns a copied token only when its stored endpoint matches exactly.
// This prevents a credential retained under a reused server name from being
// sent to a newly configured endpoint.
func (s *Store) Load(serverName, endpoint string) (*oauth2.Token, error) {
	if err := validateServer(serverName, endpoint); err != nil {
		return nil, err
	}
	if s == nil || s.backend == nil {
		return nil, errors.New("MCP OAuth keyring is unavailable")
	}
	payload, err := s.backend.Get(keyringService, credentialKey(serverName))
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load MCP OAuth credential: %w", err)
	}
	var stored storedCredential
	if err := json.Unmarshal([]byte(payload), &stored); err != nil {
		return nil, fmt.Errorf("%w: decode stored value: %v", ErrInvalidCredential, err)
	}
	if stored.Version != 1 || stored.Token == nil || strings.TrimSpace(stored.Token.AccessToken) == "" {
		return nil, ErrInvalidCredential
	}
	if stored.Endpoint != strings.TrimSpace(endpoint) {
		return nil, ErrEndpointMismatch
	}
	tokenCopy := *stored.Token
	return &tokenCopy, nil
}

// Delete removes one server's stored credential. A missing credential remains
// distinguishable so a CLI can report a meaningful logout result.
func (s *Store) Delete(serverName string) error {
	if strings.TrimSpace(serverName) == "" {
		return errors.New("MCP server name is required")
	}
	if s == nil || s.backend == nil {
		return errors.New("MCP OAuth keyring is unavailable")
	}
	if err := s.backend.Delete(keyringService, credentialKey(serverName)); errors.Is(err, keyring.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("delete MCP OAuth credential: %w", err)
	}
	return nil
}

type storedCredential struct {
	Version  int           `json:"version"`
	Endpoint string        `json:"endpoint"`
	Token    *oauth2.Token `json:"token"`
}

type systemKeyring struct{}

func (systemKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (systemKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (systemKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

func validateServer(serverName, endpoint string) error {
	if strings.TrimSpace(serverName) == "" {
		return errors.New("MCP server name is required")
	}
	if strings.TrimSpace(endpoint) == "" {
		return errors.New("MCP server endpoint is required")
	}
	return nil
}

func credentialKey(serverName string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(serverName)))
	return hex.EncodeToString(digest[:])
}
