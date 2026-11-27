package setup

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTestConnectionWithoutTokenUsesHealthEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server/healthz" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("health request unexpectedly included an authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"project": map[string]string{"name": "Test Paste"},
			"version": "1.2.3",
		})
	}))
	defer server.Close()

	info, err := TestConnection(server.URL, "", "v1")
	if err != nil {
		t.Fatalf("TestConnection returned error: %v", err)
	}
	if info.Name != "Test Paste" || info.Version != "1.2.3" {
		t.Fatalf("server info = %#v", info)
	}
}

func TestTestConnectionValidatesToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/healthz") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]string{"name": "Test"}, "version": "1"})
			return
		}
		if r.URL.Path != "/api/v2/server/auth/check" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"kind":"owner"}}`))
	}))
	defer server.Close()

	info, err := TestConnection(server.URL, "secret-token", "v2")
	if err != nil {
		t.Fatalf("TestConnection returned error: %v", err)
	}
	if info.TokenKind != "owner" {
		t.Fatalf("token kind = %q, want owner", info.TokenKind)
	}
}

func TestTestConnectionRejectsInvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/healthz") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]string{"name": "Test"}, "version": "1"})
			return
		}
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := TestConnection(server.URL, "invalid", "v1")
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("error = %v, want invalid-token error", err)
	}
}

func TestTestConnectionRejectsURLsWithCredentials(t *testing.T) {
	if _, err := TestConnection("https://user:secret@example.com", "", "v1"); err == nil {
		t.Fatal("expected URL credentials to be rejected")
	}
}
