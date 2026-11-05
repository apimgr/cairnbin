package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

func TestHandleSitemap_XML(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil)
	r.Host = "paste.example.com"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("/sitemap.xml status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Errorf("Content-Type = %q, want application/xml", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<urlset") {
		t.Errorf("body missing <urlset>: %s", body)
	}
	// Homepage present with priority 1.0.
	if !strings.Contains(body, "<priority>1.0</priority>") {
		t.Errorf("body missing homepage priority 1.0")
	}
	// A public page must be listed.
	if !strings.Contains(body, "/server/about") {
		t.Errorf("body missing /server/about")
	}
	// Never expose /api/* endpoints.
	if strings.Contains(body, "/api/") {
		t.Errorf("sitemap must not include /api/ endpoints: %s", body)
	}
}

func TestHandleRobots_ReferencesSitemap(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	r := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
	r.Host = "paste.example.com"
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("/robots.txt status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Sitemap: ") || !strings.Contains(w.Body.String(), "/sitemap.xml") {
		t.Errorf("robots.txt missing Sitemap reference: %s", w.Body.String())
	}
}

func TestHandleLLMs_WellKnownAndAlias(t *testing.T) {
	db := &stubDB{}
	cfg := config.DefaultConfig()
	s := New(db, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())

	for _, path := range []string{"/.well-known/llms.txt", "/llms.txt"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "paste.example.com"
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
			t.Errorf("%s Content-Type = %q, want text/plain", path, ct)
		}
		body := w.Body.String()
		for _, want := range []string{"## API", "Base URL:", "## Endpoints", "## Capabilities", "## Contact"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body missing section %q", path, want)
			}
		}
		// Metrics endpoint is never advertised.
		if strings.Contains(body, "/metrics") {
			t.Errorf("%s must not advertise /metrics", path)
		}
	}
}
