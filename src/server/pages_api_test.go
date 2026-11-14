package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
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
	s.cfg.Server.Pages.Contact.Captcha = "simple"

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
	challenge, err := s.generateSimpleCaptcha()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(challenge.Question)
	a, _ := strconv.Atoi(parts[2])
	b, _ := strconv.Atoi(strings.TrimSuffix(parts[4], "?"))
	full := fmt.Sprintf(`{"name":"a","email":"a@example.com","subject":"s","message":"m","captcha_token":%q,"captcha":%q}`, challenge.Token, strconv.Itoa(a+b))
	if w := post(full); w.Code != http.StatusServiceUnavailable {
		t.Errorf("no delivery channel status=%d, want 503", w.Code)
	}
	localized := httptest.NewRecorder()
	localizedReq := httptest.NewRequest(http.MethodPost, "/?lang=es", strings.NewReader(`{"name":"a","email":"invalid","subject":"s","message":"m"}`))
	s.handleContactAPI(localized, localizedReq)
	var localizedBody map[string]interface{}
	if err := json.Unmarshal(localized.Body.Bytes(), &localizedBody); err != nil {
		t.Fatalf("localized validation response is not JSON: %v", err)
	}
	if localized.Code != http.StatusBadRequest || localizedBody["error"] != "VALIDATION_FAILED" || localizedBody["message"] != i18n.Translate("es", "errors.invalid_email") {
		t.Errorf("localized validation response changed its contract or message: status=%d body=%q", localized.Code, localized.Body.String())
	}
	s.cfg.Server.Pages.Contact.Enabled = false
	if w := post(full); w.Code != http.StatusNotFound {
		t.Errorf("disabled status=%d, want 404", w.Code)
	}
}

func TestContactAPIGetFormDefinition(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Pages.Contact.Enabled = true
	s.cfg.Server.Pages.Contact.Captcha = "simple"
	w := httptest.NewRecorder()
	s.handleContactAPIGet(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET contact status=%d body=%q", w.Code, w.Body.String())
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			Fields []struct {
				Name      string `json:"name"`
				Type      string `json:"type"`
				Required  bool   `json:"required"`
				MaxLength *int   `json:"max_length"`
			} `json:"fields"`
			Captcha     map[string]interface{} `json:"captcha"`
			SecurityURL string                 `json:"security_url"`
			AbuseEmail  string                 `json:"abuse_email"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET contact: %v", err)
	}
	if !response.OK || len(response.Data.Fields) != 5 {
		t.Fatalf("unexpected contact form definition: %s", w.Body.String())
	}
	if response.Data.Fields[0].Name != "name" || response.Data.Fields[0].MaxLength == nil || *response.Data.Fields[0].MaxLength != 100 {
		t.Errorf("unexpected first field descriptor: %+v", response.Data.Fields[0])
	}
	if response.Data.Fields[4].Name != "captcha" || response.Data.Fields[4].MaxLength != nil {
		t.Errorf("captcha should have null max_length: %+v", response.Data.Fields[4])
	}
	if response.Data.Captcha["provider"] != "simple" || response.Data.Captcha["question"] == "" || response.Data.Captcha["challenge_id"] == "" {
		t.Errorf("missing simple captcha challenge descriptor: %#v", response.Data.Captcha)
	}
	if _, exists := response.Data.Captcha["recipient"]; exists || strings.Contains(w.Body.String(), "admin@example.com") {
		t.Errorf("contact API must not expose recipient details: %s", w.Body.String())
	}

	s.cfg.Server.Pages.Contact.Enabled = false
	disabled := httptest.NewRecorder()
	s.handleContactAPIGet(disabled, httptest.NewRequest(http.MethodGet, "/", nil))
	if disabled.Code != http.StatusNotFound {
		t.Errorf("disabled GET contact status=%d, want 404", disabled.Code)
	}
}

func TestContactAPIMaxLengthsCountUnicodeRunes(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Pages.Contact.Enabled = true
	s.cfg.Server.Pages.Contact.Captcha = "none"
	// Trimming precedes length validation; a multi-byte character counts once.
	body := `{"name":" ` + strings.Repeat("界", 101) + ` ","email":"a@example.com","subject":"s","message":"m"}`
	w := httptest.NewRecorder()
	s.handleContactAPI(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode validation response: %v", err)
	}
	details, _ := response["details"].(map[string]interface{})
	if w.Code != http.StatusBadRequest || response["error"] != "VALIDATION_FAILED" || details["field"] != "name" || details["max_length"] != float64(100) {
		t.Errorf("unexpected max-length response: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestExpandFooterVars(t *testing.T) {
	s := pagesServer(t)
	s.version = "1.2.3"
	s.buildDate = "July 18, 2026 at 10:00:00 UTC"
	got := s.expandFooterVars("<p>{project_org}/{project_name} {project_version} {build_datetime} [{onion_address}][{i2p_address}] {current_year}</p>")
	for _, want := range []string{"apimgr/cairnbin", "1.2.3", "July 18, 2026 at 10:00:00 UTC", "[][]"} {
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

func TestConsentBannerServerRendered(t *testing.T) {
	s := pagesServer(t)
	get := func(cookie string) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept", "text/html")
		if cookie != "" {
			addConsentCookie(r, cookie)
		}
		w := httptest.NewRecorder()
		s.handleAbout(w, r)
		return w.Body.String()
	}
	first := get("")
	for _, want := range []string{`id="cookie-consent"`, `action="/server/consent"`, `value="accept"`, `value="decline"`} {
		if !strings.Contains(first, want) {
			t.Errorf("first visit must render %s without JS", want)
		}
	}
	if strings.Contains(first, "<noscript>\n<form class=\"cookie-banner") {
		t.Error("banner must not be hidden inside noscript")
	}
	if got := get(`{"essential":true}`); strings.Contains(got, `id="cookie-consent"`) {
		t.Error("banner must be omitted once a cookie_consent cookie exists")
	}
}

func TestCheckTrackingAllowed(t *testing.T) {
	s := pagesServer(t)
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if s.checkTrackingAllowed(r) {
		t.Error("no cookie must not allow tracking")
	}
	addConsentCookie(r, `{"essential":true,"analytics":true}`)
	if s.checkTrackingAllowed(r) {
		t.Error("consent without configured tracking must not allow tracking")
	}
}

// TestCheckTrackingAllowedHonorsGPC verifies Sec-GPC overrides stored analytics consent.
func TestCheckTrackingAllowedHonorsGPC(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Tracking = config.TrackingConfig{Type: "simple"}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	addConsentCookie(r, `{"essential":true,"analytics":true}`)
	if !s.checkTrackingAllowed(r) {
		t.Fatal("consent + configured tracking must allow tracking")
	}
	r = r.WithContext(context.WithValue(r.Context(), privacyOptOutKey{}, true))
	if s.checkTrackingAllowed(r) {
		t.Error("Sec-GPC opt-out must disable tracking despite consent")
	}
}

func TestSitemapConfig(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Web.Robots.Allow = []string{"/"}
	s.cfg.Server.SEO.Sitemap = config.SitemapConfig{MaxURLs: 50000, IncludeImages: true}
	w := httptest.NewRecorder()
	s.handleSitemap(w, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "<urlset") || !strings.Contains(body, "og-image.png") {
		t.Errorf("always-on sitemap/include_images not honored: status=%d body=%s", w.Code, body)
	}
	s.cfg.Server.SEO.Sitemap.MaxURLs = 2
	w = httptest.NewRecorder()
	s.handleSitemap(w, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "sitemapindex") {
		t.Errorf("small max_urls must split static pages into child files: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHelpPageAPIFirstThenCompat(t *testing.T) {
	s := pagesServer(t)
	r := httptest.NewRequest(http.MethodGet, "/server/help", nil)
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.handleHelp(w, r)
	body := w.Body.String()
	api := strings.Index(body, `id="api"`)
	compat := strings.Index(body, `id="compat"`)
	if api < 0 || compat < 0 || api > compat {
		t.Fatalf("REST API section must precede Compatibility (api=%d compat=%d)", api, compat)
	}
	for _, want := range []string{"/api/v1/pastes", `id="compat-subdomains"`, `id="compat-hastebin"`} {
		if !strings.Contains(body, want) {
			t.Errorf("help page missing %q", want)
		}
	}
	if strings.Contains(body, "{{.APIBase}}") {
		t.Error("APIBase not expanded")
	}
}
