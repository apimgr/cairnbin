package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/pastebin/src/config"
)

func pagesServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.Web.SiteTitle = "Test Paste"
	cfg.Web.Theme = "dark"
	s := newMinimalServer(cfg)
	tmpl, err := s.buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	s.templates = tmpl
	return s
}

func TestPageContentOverride(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Pages.Terms.Content = "# Custom Terms Marker"
	s.cfg.Server.Pages.About.Content = "Extra about marker"

	get := func(h http.HandlerFunc) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		h(w, r)
		return w.Body.String()
	}
	terms := get(s.handleTerms)
	if !strings.Contains(terms, "Custom Terms Marker") || strings.Contains(terms, "Acceptable use") {
		t.Error("terms content must replace the default body")
	}
	about := get(s.handleAbout)
	if !strings.Contains(about, "Extra about marker") || !strings.Contains(about, "Test Paste") {
		t.Error("about content must be appended to the default body")
	}
}

func TestServerPagesAPI(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Pages.Contact.Enabled = true
	s.cfg.Server.Pages.Contact.Captcha = "none"

	for name, h := range map[string]http.HandlerFunc{
		"about": s.handleAboutAPI, "privacy": s.handlePrivacyAPI,
		"help": s.handleHelpAPI, "terms": s.handleTermsAPI,
	} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok": true`) {
			t.Errorf("%s: status=%d body=%q", name, w.Code, w.Body.String())
		}
	}

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.handleContactAPI(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		return w
	}
	if w := post(`{bad`); w.Code != http.StatusBadRequest {
		t.Errorf("bad json status=%d, want 400", w.Code)
	}
	if w := post(`{"name":"a"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "VALIDATION_FAILED") {
		t.Errorf("missing fields: %d %q", w.Code, w.Body.String())
	}
	full := `{"name":"a","email":"a@example.com","subject":"s","message":"m"}`
	if w := post(full); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no delivery channel status=%d, want 503", w.Code)
	}
	s.cfg.Server.Pages.Contact.Enabled = false
	if w := post(full); w.Code != http.StatusNotFound {
		t.Errorf("disabled status=%d, want 404", w.Code)
	}
}

func TestExpandFooterVars(t *testing.T) {
	s := pagesServer(t)
	s.version = "1.2.3"
	s.buildDate = "July 18, 2026 at 10:00:00 UTC"
	got := s.expandFooterVars("<p>{project_org}/{project_name} {project_version} {build_datetime} [{onion_address}][{i2p_address}] {current_year}</p>")
	for _, want := range []string{"apimgr/pastebin", "1.2.3", "July 18, 2026 at 10:00:00 UTC", "[][]"} {
		if !strings.Contains(got, want) {
			t.Errorf("expanded footer %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "{current_year}") {
		t.Error("{current_year} not expanded")
	}
	if s.expandFooterVars("") != "" {
		t.Error("empty footer must stay empty")
	}
}
