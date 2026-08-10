package mcpoauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestStoreCredentialRoundTripAndVersionOneCompatibility(t *testing.T) {
	backend := newMemoryBackend()
	store := NewStore(backend)
	credential := Credential{
		Token:   &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)},
		Refresh: &RefreshProfile{ClientID: "client", ClientSecret: "secret", TokenURL: "https://issuer.example.test/token", AuthStyle: "in_header"},
	}
	if err := store.SaveCredential("remote", "https://mcp.example.test", credential); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadCredential("remote", "https://mcp.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token.AccessToken != "access" || loaded.Token.RefreshToken != "refresh" || loaded.Refresh == nil || loaded.Refresh.ClientID != "client" || loaded.Refresh.ClientSecret != "secret" {
		t.Fatalf("loaded credential = %#v", loaded)
	}
	loaded.Token.AccessToken = "changed"
	loaded.Refresh.ClientID = "changed"
	again, err := store.LoadCredential("remote", "https://mcp.example.test")
	if err != nil || again.Token.AccessToken != "access" || again.Refresh.ClientID != "client" {
		t.Fatalf("credential copy = %#v, err=%v", again, err)
	}

	legacyPayload, err := json.Marshal(storedCredential{Version: 1, Endpoint: "https://mcp.example.test", Token: &oauth2.Token{AccessToken: "legacy"}})
	if err != nil {
		t.Fatal(err)
	}
	backend.values[keyringService+"/"+credentialKey("legacy")] = string(legacyPayload)
	legacy, err := store.LoadCredential("legacy", "https://mcp.example.test")
	if err != nil || legacy.Token.AccessToken != "legacy" || legacy.Refresh != nil {
		t.Fatalf("legacy credential = %#v, err=%v", legacy, err)
	}
}

func TestNewTokenSourceRefreshesAndPersistsRotation(t *testing.T) {
	var requests muRefreshRequests
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "POST required", http.StatusMethodNotAllowed)
			return
		}
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
			http.Error(writer, "invalid form", http.StatusBadRequest)
			return
		}
		requests.Store(request.Form, request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()

	credential := Credential{
		Token:   &oauth2.Token{AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Hour)},
		Refresh: &RefreshProfile{ClientID: "client", ClientSecret: "secret", TokenURL: server.URL, AuthStyle: "in_params"},
	}
	var writes []Credential
	var writesMu sync.Mutex
	source, err := NewTokenSource(&credential, server.Client(), func(updated Credential) error {
		writesMu.Lock()
		defer writesMu.Unlock()
		writes = append(writes, updated)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := source.Token()
	if err != nil || token.AccessToken != "new-access" || token.RefreshToken != "new-refresh" {
		t.Fatalf("Token() = %#v, err=%v", token, err)
	}
	form, authorization := requests.Load()
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "old-refresh" || form.Get("client_id") != "client" || form.Get("client_secret") != "secret" || authorization != "" {
		t.Fatalf("refresh request form=%v authorization=%q", form, authorization)
	}
	writesMu.Lock()
	defer writesMu.Unlock()
	if len(writes) != 1 || writes[0].Token.AccessToken != "new-access" || writes[0].Token.RefreshToken != "new-refresh" || writes[0].Refresh.ClientSecret != "secret" {
		t.Fatalf("persisted rotations = %#v", writes)
	}
	if _, err := source.Token(); err != nil || len(writes) != 1 {
		t.Fatalf("cached Token() err=%v writes=%d", err, len(writes))
	}
}

func TestNewTokenSourceFailsClosedWhenRefreshCannotPersist(t *testing.T) {
	expired := Credential{Token: &oauth2.Token{AccessToken: "access", Expiry: time.Now().Add(-time.Hour)}}
	if _, err := NewTokenSource(&expired, http.DefaultClient, nil); !errors.Is(err, ErrRefreshUnavailable) {
		t.Fatalf("legacy expired source error = %v", err)
	}
	profiled := Credential{
		Token:   &oauth2.Token{AccessToken: "access", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Hour)},
		Refresh: &RefreshProfile{ClientID: "client", TokenURL: "https://issuer.example.test/token", AuthStyle: "in_params"},
	}
	if _, err := NewTokenSource(&profiled, http.DefaultClient, nil); !errors.Is(err, ErrRefreshUnavailable) {
		t.Fatalf("unpersistable refresh source error = %v", err)
	}
	if _, err := NewTokenSource(&Credential{Token: &oauth2.Token{AccessToken: "access"}, Refresh: &RefreshProfile{ClientID: "", TokenURL: "https://issuer.example.test/token", AuthStyle: "auto"}}, http.DefaultClient, func(Credential) error { return nil }); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("invalid profile source error = %v", err)
	}
}

func TestNewTokenSourceFailsClosedWhenRotationWriteFails(t *testing.T) {
	var tokenRequests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		tokenRequests++
		if err := request.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
			http.Error(writer, "invalid form", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"rotated-access","refresh_token":"rotated-refresh","token_type":"Bearer","expires_in":3600}`))
	}))
	defer server.Close()

	credential := Credential{
		Token:   &oauth2.Token{AccessToken: "expired-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Hour)},
		Refresh: &RefreshProfile{ClientID: "client", TokenURL: server.URL, AuthStyle: "in_params"},
	}
	source, err := NewTokenSource(&credential, server.Client(), func(Credential) error {
		return errors.New("keyring write failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	if token, tokenErr := source.Token(); token != nil || tokenErr == nil || !strings.Contains(tokenErr.Error(), "keyring write failed") {
		t.Fatalf("Token() = %#v, err=%v", token, tokenErr)
	}
	if tokenRequests != 1 {
		t.Fatalf("token requests = %d, want 1", tokenRequests)
	}
}

type muRefreshRequests struct {
	mu            sync.Mutex
	form          url.Values
	authorization string
}

func (r *muRefreshRequests) Store(form url.Values, authorization string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.form = make(url.Values, len(form))
	for key, values := range form {
		r.form[key] = append([]string(nil), values...)
	}
	r.authorization = authorization
}

func (r *muRefreshRequests) Load() (url.Values, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.form, r.authorization
}
