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

func TestHandleLLMs_DisabledAndCustomSections(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.LLMs.Enabled = false
	s := New(&stubDB{}, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())
	r := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled llms.txt status = %d, want 404", w.Code)
	}

	cfg = config.DefaultConfig()
	cfg.Web.LLMs.IncludeEndpoints = false
	cfg.Web.LLMs.CustomSections = []string{"## Extra\nHello"}
	s = New(&stubDB{}, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())
	w = httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))
	body := w.Body.String()
	if strings.Contains(body, "## Endpoints") || !strings.Contains(body, "## Extra\nHello") {
		t.Errorf("unexpected llms body: %s", body)
	}
}

func TestHandleLLMs_IncludeSchemas(t *testing.T) {
	for _, on := range []bool{false, true} {
		cfg := config.DefaultConfig()
		cfg.Web.LLMs.IncludeSchemas = on
		s := New(&stubDB{}, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))
		if got := strings.Contains(w.Body.String(), "## Schemas"); got != on {
			t.Errorf("include_schemas=%v: Schemas section present=%v", on, got)
		}
	}
}

func TestWellKnownNamespaceContract(t *testing.T) {
	s := New(&stubDB{}, config.DefaultConfig(), nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/unknown-entry", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown entry status = %d, want 404", w.Code)
	}
	w = httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/.well-known/unknown-entry", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", w.Code)
	}
}

func TestHandleRobots_DeniedAIBot(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Robots.AIBots.Bots = map[string]string{"GPTBot": "deny"}
	s := New(&stubDB{}, cfg, nil, "1.0.0", "abc", "now", t.TempDir(), t.TempDir())
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if !strings.Contains(w.Body.String(), "User-agent: GPTBot\nDisallow: /\n") {
		t.Errorf("robots.txt missing GPTBot stanza: %s", w.Body.String())
	}
}
