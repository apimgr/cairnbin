package handler

// Internal tests for unexported handler functions.
// These live in package handler (not handler_test) so they can access unexported symbols.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/model"
)

// ─── Internal helpers ─────────────────────────────────────────────────────────

func newInternalTestDB(t *testing.T) database.DB {
	t.Helper()
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cairnbin-int-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	db, err := database.NewDatabase("sqlite", filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// createMinimalPaste inserts a paste into db and returns its ID.
func createMinimalPaste(t *testing.T, db database.DB) string {
	t.Helper()
	id, err := generateID()
	if err != nil {
		t.Fatalf("generateID: %v", err)
	}
	p := &model.Paste{
		ID:       id,
		Title:    "test",
		Content:  "internal test content",
		Language: "text",
	}
	if err := db.CreatePaste(p); err != nil {
		t.Fatalf("CreatePaste: %v", err)
	}
	return id
}

// ─── extractToken ─────────────────────────────────────────────────────────────

func TestExtractToken(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*http.Request)
		want  string
	}{
		{
			name: "bearer",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer tok_testtoken")
			},
			want: "tok_testtoken",
		},
		{
			name: "bare_auth",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "tok_baretoken")
			},
			want: "tok_baretoken",
		},
		{
			name: "x_api_token",
			setup: func(r *http.Request) {
				r.Header.Set("X-Api-Token", "tok_apitoken")
			},
			want: "tok_apitoken",
		},
		{
			name: "x_token",
			setup: func(r *http.Request) {
				r.Header.Set("X-Token", "tok_xtoken")
			},
			want: "tok_xtoken",
		},
		{
			name: "x_delete_token",
			setup: func(r *http.Request) {
				r.Header.Set("X-Delete-Token", "tok_deletetoken")
			},
			want: "tok_deletetoken",
		},
		{
			name: "query_param",
			setup: func(r *http.Request) {
				r.URL.RawQuery = "token=tok_queryparam"
			},
			want: "tok_queryparam",
		},
		{
			name: "json_body",
			setup: func(r *http.Request) {
				r.Header.Set("Content-Type", "application/json")
				r.Body = io.NopCloser(strings.NewReader(`{"token":"tok_jsonbody"}`))
			},
			want: "tok_jsonbody",
		},
		{
			name:  "empty",
			setup: func(r *http.Request) {},
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			tc.setup(r)
			got := extractToken(r)
			if got != tc.want {
				t.Errorf("extractToken: got %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── httpErrCode ──────────────────────────────────────────────────────────────

func TestHTTPErrCode(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "BAD_REQUEST"},
		{http.StatusUnauthorized, "UNAUTHORIZED"},
		{http.StatusForbidden, "FORBIDDEN"},
		{http.StatusNotFound, "NOT_FOUND"},
		{http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
		{http.StatusConflict, "CONFLICT"},
		{http.StatusGone, "GONE"},
		{http.StatusTooManyRequests, "RATE_LIMITED"},
		{http.StatusServiceUnavailable, "MAINTENANCE"},
		{http.StatusInternalServerError, "SERVER_ERROR"},
		{http.StatusTeapot, "SERVER_ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := httpErrCode(tc.status)
			if got != tc.want {
				t.Errorf("httpErrCode(%d): got %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// ─── mapAPIErrorCodeToHTTPStatus ──────────────────────────────────────────────

func TestMapAPIErrorCodeToHTTPStatus(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"BAD_REQUEST", http.StatusBadRequest},
		{"VALIDATION_FAILED", http.StatusBadRequest},
		{"UNAUTHORIZED", http.StatusUnauthorized},
		{"TOKEN_EXPIRED", http.StatusUnauthorized},
		{"TOKEN_INVALID", http.StatusUnauthorized},
		{"FORBIDDEN", http.StatusForbidden},
		{"ACCOUNT_LOCKED", http.StatusForbidden},
		{"CSRF_FAILED", http.StatusForbidden},
		{"NOT_FOUND", http.StatusNotFound},
		{"METHOD_NOT_ALLOWED", http.StatusMethodNotAllowed},
		{"CONFLICT", http.StatusConflict},
		{"GONE", http.StatusGone},
		{"RATE_LIMITED", http.StatusTooManyRequests},
		{"MAINTENANCE", http.StatusServiceUnavailable},
		{"UNKNOWN_CODE", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			got := mapAPIErrorCodeToHTTPStatus(tc.code)
			if got != tc.want {
				t.Errorf("mapAPIErrorCodeToHTTPStatus(%q): got %d, want %d", tc.code, got, tc.want)
			}
		})
	}
}

// ─── sendAPIError ─────────────────────────────────────────────────────────────

func TestSendAPIError(t *testing.T) {
	cases := []struct {
		code       string
		message    string
		wantStatus int
	}{
		{"NOT_FOUND", "paste not found", http.StatusNotFound},
		{"BAD_REQUEST", "invalid input", http.StatusBadRequest},
		{"UNAUTHORIZED", "token required", http.StatusUnauthorized},
		{"RATE_LIMITED", "slow down", http.StatusTooManyRequests},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
			sendAPIError(rr, req, tc.code, tc.message)
			if rr.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d", rr.Code, tc.wantStatus)
			}
			body := rr.Body.String()
			if !strings.Contains(body, tc.code) {
				t.Errorf("body missing error code %q: %s", tc.code, body)
			}
		})
	}
}

// ─── PasteURL ─────────────────────────────────────────────────────────────────

func TestPasteURL(t *testing.T) {
	db := newInternalTestDB(t)

	t.Run("with_base_url", func(t *testing.T) {
		h := &PasteHandler{db: db, baseURL: "https://example.com"}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "ignored.host"
		got := h.PasteURL(r, "abc12345")
		want := "https://example.com/abc12345"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("http", func(t *testing.T) {
		h := &PasteHandler{db: db}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "localhost:8080"
		got := h.PasteURL(r, "abc12345")
		if !strings.HasPrefix(got, "http://") {
			t.Errorf("expected http:// prefix, got %q", got)
		}
		if !strings.Contains(got, "abc12345") {
			t.Errorf("expected id in URL, got %q", got)
		}
	})

	// X-Forwarded-Proto without a trusted resolver must NOT be honoured (PART 12
	// proxy-spoofing guard). The bare connection scheme is used instead.
	t.Run("forwarded_proto_ignored_without_resolver", func(t *testing.T) {
		h := &PasteHandler{db: db}
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "paste.example.com"
		r.Header.Set("X-Forwarded-Proto", "https")
		got := h.PasteURL(r, "abc12345")
		if !strings.HasPrefix(got, "http://") {
			t.Errorf("expected http:// (bare connection, header untrusted), got %q", got)
		}
	})

	t.Run("https_via_trusted_resolver", func(t *testing.T) {
		h := &PasteHandler{db: db}
		h.SetBaseURLResolver(func(*http.Request) string { return "https://paste.example.com" })
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "paste.example.com"
		got := h.PasteURL(r, "abc12345")
		if !strings.HasPrefix(got, "https://") {
			t.Errorf("expected https:// from trusted resolver, got %q", got)
		}
	})

	// When the server injects its canonical resolver (PART 12), PasteURL must
	// delegate to it — never rebuild a simplified scheme+Host that ignores the
	// configured-DOMAIN fallback behind a Host-stripping reverse proxy.
	t.Run("delegates_to_injected_resolver", func(t *testing.T) {
		h := &PasteHandler{db: db}
		h.SetBaseURLResolver(func(*http.Request) string { return "https://pb.pste.us" })
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "127.0.2.1:8443"
		got := h.PasteURL(r, "abc12345")
		want := "https://pb.pste.us/abc12345"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

// ─── loadLivePaste ────────────────────────────────────────────────────────────

func TestLoadLivePaste(t *testing.T) {
	db := newInternalTestDB(t)
	h := &PasteHandler{db: db}

	t.Run("not_found", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/notexist", nil)
		p, err := h.loadLivePaste(rr, req, "notexist")
		if p != nil {
			t.Error("expected nil paste for not found")
		}
		if err != nil {
			t.Error("expected nil error for not found")
		}
		if rr.Code != http.StatusNotFound {
			t.Errorf("status: got %d, want 404", rr.Code)
		}
	})

	t.Run("expired", func(t *testing.T) {
		id, err := generateID()
		if err != nil {
			t.Fatalf("generateID: %v", err)
		}
		past := time.Now().Add(-time.Hour)
		expired := &model.Paste{
			ID:        id,
			Title:     "expired",
			Content:   "expired content",
			Language:  "text",
			ExpiresAt: &past,
		}
		if err := db.CreatePaste(expired); err != nil {
			t.Fatalf("CreatePaste: %v", err)
		}

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id, nil)
		p, err := h.loadLivePaste(rr, req, id)
		if p != nil {
			t.Error("expected nil paste for expired")
		}
		if err != nil {
			t.Error("expected nil error for expired")
		}
		if rr.Code != http.StatusGone {
			t.Errorf("status: got %d, want 410", rr.Code)
		}
	})

	t.Run("found", func(t *testing.T) {
		id := createMinimalPaste(t, db)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/pastes/"+id, nil)
		p, err := h.loadLivePaste(rr, req, id)
		if err != nil {
			t.Fatalf("loadLivePaste: %v", err)
		}
		if p == nil {
			t.Fatal("expected paste, got nil")
		}
		if p.ID != id {
			t.Errorf("ID: got %q, want %q", p.ID, id)
		}
	})
}

// TestSendAPIErrorDetails verifies the structured `details` object appears in
// the canonical envelope only when provided.
func TestSendAPIErrorDetails(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pastes", nil)
	r.Header.Set("Accept", "application/json")

	rec := httptest.NewRecorder()
	sendAPIErrorDetails(rec, r, "VALIDATION_FAILED", "content is required",
		map[string]interface{}{"field": "content", "rule": "required"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "VALIDATION_FAILED" || body["ok"] != false {
		t.Errorf("envelope = %v", body)
	}
	details, _ := body["details"].(map[string]interface{})
	if details["field"] != "content" || details["rule"] != "required" {
		t.Errorf("details = %v, want field=content rule=required", body["details"])
	}

	rec = httptest.NewRecorder()
	sendAPIError(rec, r, "NOT_FOUND", "paste not found")
	var plain map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &plain)
	if _, has := plain["details"]; has {
		t.Errorf("details must be omitted when empty: %v", plain)
	}
}
