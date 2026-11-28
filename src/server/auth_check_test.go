package server

import (
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
)

func TestAuthCheckRejectsMissingAndInvalidBearer(t *testing.T) {
	s := New(&stubDB{}, config.DefaultConfig(), nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())
	for _, authorization := range []string{"", "Basic abc", "Bearer invalid"} {
		r := httptest.NewRequest("GET", "/api/v1/server/auth/check", nil)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		if w.Code != 401 || (authorization != "" && strings.Contains(w.Body.String(), authorization)) {
			t.Errorf("authorization %q: status=%d response=%q", authorization, w.Code, w.Body.String())
		}
	}
}

func TestInvalidBearerLockoutBlocksValidCredentialByIP(t *testing.T) {
	valid := "operator-secret"
	s := &Server{authFailureLimiter: newRateLimiter(2, time.Minute), operatorTokenHash: sha256.Sum256([]byte(valid))}
	handler := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
		r.Header.Set("Authorization", "Bearer invalid-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("invalid request %d status=%d, want request to reach route", i+1, w.Code)
		}
	}
	anonymous := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
	anonymousResult := httptest.NewRecorder()
	handler.ServeHTTP(anonymousResult, anonymous)
	if anonymousResult.Code != http.StatusTooManyRequests || anonymousResult.Header().Get("Retry-After") == "" {
		t.Fatalf("anonymous request during lockout status=%d retry=%q", anonymousResult.Code, anonymousResult.Header().Get("Retry-After"))
	}
	r := httptest.NewRequest(http.MethodGet, "/api/v1/pastes", nil)
	r.Header.Set("Authorization", "Bearer "+valid)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Fatalf("valid credential during lockout status=%d retry=%q body=%s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
}

func TestRequireOperatorTokenRejectsOwnerTokenWithForbidden(t *testing.T) {
	s, ownerToken := ownerTokenTestServer(t)
	s.operatorTokenHash = sha256.Sum256([]byte("operator-token"))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/server/schedulers/task/enable", nil)
	request.Header.Set("Authorization", "Bearer "+ownerToken)
	response := httptest.NewRecorder()
	s.requireOperatorToken(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("owner token reached operator-only handler")
	})).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("owner token status=%d, want %d; body=%s", response.Code, http.StatusForbidden, response.Body.String())
	}
	if got := response.Header().Get("WWW-Authenticate"); got != `Bearer error="insufficient_scope"` {
		t.Fatalf("WWW-Authenticate=%q, want insufficient_scope", got)
	}
}

func TestAuthCheckReturnsOperatorKindWithoutToken(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Token = "operator-secret-value"
	s := New(&stubDB{}, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())
	r := httptest.NewRequest("GET", "/api/v1/server/auth/check", nil)
	r.Header.Set("Authorization", "Bearer "+cfg.Server.Token)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"kind"`) || !strings.Contains(w.Body.String(), `"operator"`) || strings.Contains(w.Body.String(), cfg.Server.Token) {
		t.Fatalf("operator check status=%d response=%q", w.Code, w.Body.String())
	}
}
