package server

import (
	"net/http/httptest"
	"strings"
	"testing"

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
