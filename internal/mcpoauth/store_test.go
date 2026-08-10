package mcpoauth

import (
	"errors"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"golang.org/x/oauth2"
)

func TestStoreSaveLoadDeleteAndEndpointBinding(t *testing.T) {
	backend := newMemoryBackend()
	store := NewStore(backend)
	token := &oauth2.Token{AccessToken: "access-token", RefreshToken: "refresh-token", Expiry: time.Now().Add(time.Hour)}
	if err := store.Save("remote", "https://mcp.example.test", token); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if len(backend.values) != 1 {
		t.Fatalf("stored keyring values = %#v", backend.values)
	}
	for key := range backend.values {
		if key == keyringService+"/remote" {
			t.Fatalf("credential key exposed the MCP server name: %q", key)
		}
	}
	loaded, err := store.Load("remote", "https://mcp.example.test")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.AccessToken != "access-token" || loaded.RefreshToken != "refresh-token" || loaded == token {
		t.Fatalf("loaded token = %#v", loaded)
	}
	if _, err := store.Load("remote", "https://other.example.test"); !errors.Is(err, ErrEndpointMismatch) {
		t.Fatalf("Load(endpoint mismatch) error = %v", err)
	}
	if err := store.Delete("remote"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Load("remote", "https://mcp.example.test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load(after Delete) error = %v", err)
	}
}

func TestStoreRejectsInvalidInputAndBackendErrors(t *testing.T) {
	store := NewStore(newMemoryBackend())
	for _, input := range []struct {
		name     string
		endpoint string
		token    *oauth2.Token
	}{
		{"", "https://mcp.example.test", &oauth2.Token{AccessToken: "token"}},
		{"remote", "", &oauth2.Token{AccessToken: "token"}},
		{"remote", "https://mcp.example.test", nil},
		{"remote", "https://mcp.example.test", &oauth2.Token{}},
	} {
		if err := store.Save(input.name, input.endpoint, input.token); err == nil {
			t.Fatalf("Save(%#v) succeeded", input)
		}
	}
	if err := store.Delete(""); err == nil {
		t.Fatal("Delete(empty name) succeeded")
	}
	broken := NewStore(&memoryBackend{getErr: errors.New("keyring unavailable")})
	if _, err := broken.Load("remote", "https://mcp.example.test"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Load(broken keyring) error = %v", err)
	}
}

type memoryBackend struct {
	values map[string]string
	getErr error
}

func newMemoryBackend() *memoryBackend {
	return &memoryBackend{values: make(map[string]string)}
}

func (b *memoryBackend) Get(service, user string) (string, error) {
	if b.getErr != nil {
		return "", b.getErr
	}
	value, ok := b.values[service+"/"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}

func (b *memoryBackend) Set(service, user, password string) error {
	b.values[service+"/"+user] = password
	return nil
}

func (b *memoryBackend) Delete(service, user string) error {
	key := service + "/" + user
	if _, ok := b.values[key]; !ok {
		return keyring.ErrNotFound
	}
	delete(b.values, key)
	return nil
}
