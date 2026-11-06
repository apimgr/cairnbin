package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Tests for api.go: apiClient, newAPIClient, fetchPastes, fetchPasteRaw, deletePaste

func TestNewAPIClient(t *testing.T) {
	client := newAPIClient("https://example.com", "en-US")
	if client.server != "https://example.com" {
		t.Errorf("server = %q, want %q", client.server, "https://example.com")
	}
	if client.lang != "en-US" {
		t.Errorf("lang = %q, want %q", client.lang, "en-US")
	}
}

func TestAPIClientGetSetsHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Error("User-Agent header not set")
		}
		if accept := r.Header.Get("Accept"); accept != "application/json" {
			t.Errorf("Accept = %q, want application/json", accept)
		}
		if lang := r.Header.Get("Accept-Language"); lang != "en-US" {
			t.Errorf("Accept-Language = %q, want en-US", lang)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "en-US")
	resp, err := client.get("/test")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	_ = resp.Body.Close()
}

func TestAPIClientGetWithoutLang(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Accept-Language should be empty when not set
		if lang := r.Header.Get("Accept-Language"); lang != "" {
			t.Errorf("Accept-Language = %q, want empty", lang)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newAPIClient(server.URL, "")
	resp, err := client.get("/test")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	_ = resp.Body.Close()
}

func TestFetchPastesSuccess(t *testing.T) {
	response := listResponse{
		Pastes: []PasteListItem{
			{ID: "abc123", Title: "Test", Language: "go", Views: 10},
			{ID: "def456", Title: "Another", Language: "python", Views: 5},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if !contains(r.URL.Path, "/api/v1/pastes") {
			t.Errorf("path = %q, want /api/v1/pastes", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	pastes, err := fetchPastes(server.URL, "en", 1, 50)
	if err != nil {
		t.Fatalf("fetchPastes failed: %v", err)
	}
	if len(pastes) != 2 {
		t.Errorf("len(pastes) = %d, want 2", len(pastes))
	}
	if pastes[0].ID != "abc123" {
		t.Errorf("pastes[0].ID = %q", pastes[0].ID)
	}
}

func TestFetchPastesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := fetchPastes(server.URL, "en", 1, 50)
	if err == nil {
		t.Error("fetchPastes should fail on 500")
	}
}

func TestFetchPastesInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("invalid json"))
	}))
	defer server.Close()

	_, err := fetchPastes(server.URL, "en", 1, 50)
	if err == nil {
		t.Error("fetchPastes should fail on invalid JSON")
	}
}

func TestFetchPastesNetworkError(t *testing.T) {
	// Use a closed server to simulate network error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close()

	_, err := fetchPastes(server.URL, "en", 1, 50)
	if err == nil {
		t.Error("fetchPastes should fail on network error")
	}
}

func TestFetchPasteRawSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/raw/abc123" {
			t.Errorf("path = %q, want /raw/abc123", r.URL.Path)
		}
		_, _ = w.Write([]byte("Hello World"))
	}))
	defer server.Close()

	content, err := fetchPasteRaw(server.URL, "en", "abc123")
	if err != nil {
		t.Fatalf("fetchPasteRaw failed: %v", err)
	}
	if content != "Hello World" {
		t.Errorf("content = %q, want 'Hello World'", content)
	}
}

func TestFetchPasteRawNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := fetchPasteRaw(server.URL, "en", "nonexistent")
	if err == nil {
		t.Error("fetchPasteRaw should fail on 404")
	}
	if !contains(err.Error(), "not found") {
		t.Errorf("error = %q, should mention 'not found'", err.Error())
	}
}

func TestFetchPasteRawServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := fetchPasteRaw(server.URL, "en", "abc123")
	if err == nil {
		t.Error("fetchPasteRaw should fail on 500")
	}
}

func TestFetchPasteRawLargeContent(t *testing.T) {
	largeContent := make([]byte, 10000)
	for i := range largeContent {
		largeContent[i] = 'x'
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(largeContent)
	}))
	defer server.Close()

	content, err := fetchPasteRaw(server.URL, "en", "abc123")
	if err != nil {
		t.Fatalf("fetchPasteRaw failed: %v", err)
	}
	if len(content) != 10000 {
		t.Errorf("content length = %d, want 10000", len(content))
	}
}

func TestDeletePasteSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		if !contains(r.URL.Path, "/api/v1/pastes/abc123") {
			t.Errorf("path = %q, want /api/v1/pastes/abc123", r.URL.Path)
		}
		if token := r.Header.Get("Authorization"); token != "Bearer tok_delete123" {
			t.Errorf("Authorization = %q, want Bearer tok_delete123", token)
		}
		if token := r.URL.Query().Get("token"); token != "" {
			t.Errorf("query token = %q, want none", token)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := deletePaste(server.URL, "en", "abc123", "tok_delete123")
	if err != nil {
		t.Errorf("deletePaste failed: %v", err)
	}
}

func TestDeletePasteSuccessWithOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := deletePaste(server.URL, "en", "abc123", "tok_delete123")
	if err != nil {
		t.Errorf("deletePaste should succeed with 200: %v", err)
	}
}

func TestDeletePasteNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	err := deletePaste(server.URL, "en", "abc123", "tok_wrong")
	if err == nil {
		t.Error("deletePaste should fail on 404")
	}
	if !contains(err.Error(), "not found") {
		t.Errorf("error = %q, should mention 'not found'", err.Error())
	}
}

func TestDeletePasteServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	err := deletePaste(server.URL, "en", "abc123", "tok_123")
	if err == nil {
		t.Error("deletePaste should fail on 403")
	}
}

func TestDeletePasteSetsLanguageHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lang := r.Header.Get("Accept-Language"); lang != "de" {
			t.Errorf("Accept-Language = %q, want de", lang)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := deletePaste(server.URL, "de", "abc123", "tok_123")
	if err != nil {
		t.Errorf("deletePaste failed: %v", err)
	}
}

func TestDeletePasteEmptyLanguage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Should not have Accept-Language header when empty
		if lang := r.Header.Get("Accept-Language"); lang != "" {
			t.Errorf("Accept-Language = %q, want empty", lang)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := deletePaste(server.URL, "", "abc123", "tok_123")
	if err != nil {
		t.Errorf("deletePaste failed: %v", err)
	}
}

func TestPasteListItemFields(t *testing.T) {
	now := time.Now()
	item := PasteListItem{
		ID:        "abc123",
		Title:     "Test Paste",
		Language:  "go",
		Views:     42,
		CreatedAt: now,
		ExpiresAt: now.Add(24 * time.Hour),
	}

	if item.ID != "abc123" {
		t.Errorf("ID = %q", item.ID)
	}
	if item.Title != "Test Paste" {
		t.Errorf("Title = %q", item.Title)
	}
	if item.Language != "go" {
		t.Errorf("Language = %q", item.Language)
	}
	if item.Views != 42 {
		t.Errorf("Views = %d", item.Views)
	}
}

func TestClientVersionVar(t *testing.T) {
	// clientVersion should be set (at least to "dev")
	if clientVersion == "" {
		t.Error("clientVersion should not be empty")
	}
}

func TestListResponseJSON(t *testing.T) {
	jsonData := `{
		"data": [
			{"id": "abc", "title": "Test", "language": "go", "views": 5}
		],
		"pagination": {"total": 1, "pages": 1}
	}`

	var lr listResponse
	if err := json.Unmarshal([]byte(jsonData), &lr); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(lr.Pastes) != 1 {
		t.Errorf("len(Pastes) = %d, want 1", len(lr.Pastes))
	}
	if lr.Pagination.Total != 1 {
		t.Errorf("Pagination.Total = %d", lr.Pagination.Total)
	}
}

// contains checks if substr is in s
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
