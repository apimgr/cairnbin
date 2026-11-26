package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
)

func pagesServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Web.SiteTitle = "Test Paste"
	cfg.Web.Theme = "dark"
	cfg.Server.Pages.Contact.Antispam = config.DefaultConfig().Server.Pages.Contact.Antispam
	cfg.Server.Branding.Content = make(map[string]config.BrandingContent)
	s := newMinimalServer(cfg)
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	configDir, err := os.MkdirTemp(base, "cairnbin-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(configDir) })
	s.configDir = configDir
	s.installSecret = []byte("pages-api-test-installation-secret")
	tmpl, err := s.buildTemplates()
	if err != nil {
		t.Fatalf("build templates: %v", err)
	}
	s.templates = tmpl
	return s
}

func TestPageContentOverride(t *testing.T) {
	s := pagesServer(t)
	writeOperatorPage(t, s, "terms.md", "# Custom Terms Marker")
	writeOperatorPage(t, s, "about.md", "Extra about marker")

	get := func(h http.HandlerFunc) string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		h(w, r)
		return w.Body.String()
	}
	terms := get(s.handleTerms)
	if !strings.Contains(terms, "Custom Terms Marker") || strings.Contains(terms, "Acceptable use") {
		t.Error("terms file must replace the default body")
	}
	about := get(s.handleAbout)
	if !strings.Contains(about, "Extra about marker") || !strings.Contains(about, "Test Paste") {
		t.Error("about file must be appended to the default body")
	}
}

func writeOperatorPage(t *testing.T, s *Server, name, content string) {
	t.Helper()
	base := strings.TrimSuffix(name, filepath.Ext(name))
	format := map[string]string{".md": "md", ".html": "html", ".txt": "txt"}[filepath.Ext(name)]
	if format == "" {
		t.Fatalf("unsupported operator content extension %q", name)
	}
	if s.cfg.Server.Branding.Content == nil {
		s.cfg.Server.Branding.Content = make(map[string]config.BrandingContent)
	}
	if s.cfg.Server.Branding.Content == nil {
		s.cfg.Server.Branding.Content = make(map[string]config.BrandingContent)
	}
	s.cfg.Server.Branding.Content[base] = config.BrandingContent{Format: format, Body: content}
}

func TestSecurityAndDPOPageAPIs(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Pages.Contact.Enabled = true
	for name, handler := range map[string]http.HandlerFunc{
		"dpo": s.handleDPOAPI, "security": s.handleSecurityAPI,
		"security policy": s.handleSecurityPolicyAPI,
	} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok": true`) {
			t.Errorf("%s API: status=%d body=%q", name, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.handleSecurityThanksAPI(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("security thanks without a database: status=%d, want 500", w.Code)
	}
	w = httptest.NewRecorder()
	s.handleSecurityAPI(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var response struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response.Data["report_url"]; !ok {
		t.Error("enabled report form omitted report_url")
	}
	s.cfg.Server.Pages.Contact.Enabled = false
	w = httptest.NewRecorder()
	s.handleSecurityAPI(w, httptest.NewRequest(http.MethodGet, "/", nil))
	response = struct {
		Data map[string]interface{} `json:"data"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if _, ok := response.Data["report_url"]; ok {
		t.Error("disabled report form exposed report_url")
	}
}

func TestServerPagesAPI(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Pages.Contact.Enabled = true
	s.cfg.Server.Pages.Contact.Captcha = "simple"

	for name, h := range map[string]http.HandlerFunc{
		"about": s.handleAboutAPI, "privacy": s.handlePrivacyAPI, "dpo": s.handleDPOAPI,
		"help": s.handleHelpAPI, "terms": s.handleTermsAPI,
	} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok": true`) {
			t.Errorf("%s: status=%d body=%q", name, w.Code, w.Body.String())
		}
	}
	terms := httptest.NewRecorder()
	s.handleTermsAPI(terms, httptest.NewRequest(http.MethodGet, "/", nil))
	var termsResponse struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(terms.Body.Bytes(), &termsResponse); err != nil {
		t.Fatalf("decode terms API response: %v", err)
	}
	if content, _ := termsResponse.Data["content"].(string); !strings.HasPrefix(content, "## Acceptance") {
		t.Errorf("terms API built-in content = %q, want Markdown fallback", content)
	}
	if termsResponse.Data["format"] != "markdown" {
		t.Errorf("terms API format = %v, want markdown", termsResponse.Data["format"])
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
	formToken := s.issueContactFormToken(time.Now().Add(-5*time.Second), contactAntispam(s.cfg))
	full := fmt.Sprintf(`{"type":"privacy","reported_url":"","reason":"","copyrighted_work":"","good_faith":false,"accuracy":false,"signature":"","request_kind":"access","name":"a","email":"a@example.com","subject":"s","message":"m","form_token":%q,"website":"","captcha_id":%q,"captcha_answer":%q}`, formToken, challenge.Token, strconv.Itoa(a+b))
	if w := post(full); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"review_token"`) {
		t.Errorf("first contact call should return a review without delivery: %d %q", w.Code, w.Body.String())
	} else {
		var response struct {
			Data struct {
				FormToken string                 `json:"form_token"`
				Captcha   map[string]interface{} `json:"captcha"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.FormToken == "" || response.Data.Captcha["captcha_id"] == "" {
			t.Errorf("review response omitted refresh token or captcha: %s", w.Body.String())
		}
	}
	localized := httptest.NewRecorder()
	localizedToken := s.issueContactFormToken(time.Now().Add(-5*time.Second), contactAntispam(s.cfg))
	localizedPayload := fmt.Sprintf(`{"type":"privacy","reported_url":"","reason":"","copyrighted_work":"","good_faith":false,"accuracy":false,"signature":"","request_kind":"access","name":"a","email":"invalid","subject":"s","message":"m","form_token":%q,"website":""}`, localizedToken)
	localizedReq := httptest.NewRequest(http.MethodPost, "/?lang=es", strings.NewReader(localizedPayload))
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

func TestNoIndexHeaderCoversNonTemplateResponses(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.SEO.NoIndex = true
	handler := s.seoIndexMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/server/healthz", nil))
	if got := w.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Fatalf("X-Robots-Tag = %q", got)
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
			FormToken   string                 `json:"form_token"`
			SecurityURL string                 `json:"security_url"`
			AbuseEmail  string                 `json:"abuse_email"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode GET contact: %v", err)
	}
	if !response.OK || len(response.Data.Fields) < 14 || response.Data.FormToken == "" {
		t.Fatalf("unexpected contact form definition: %s", w.Body.String())
	}
	fields := make(map[string]struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		Required  bool   `json:"required"`
		MaxLength *int   `json:"max_length"`
	})
	for _, field := range response.Data.Fields {
		fields[field.Name] = field
	}
	if field := fields["name"]; field.MaxLength == nil || *field.MaxLength != 100 {
		t.Errorf("unexpected name field descriptor: %+v", field)
	}
	if field := fields["captcha"]; field.MaxLength != nil {
		t.Errorf("captcha should have null max_length: %+v", field)
	}
	if response.Data.Captcha["provider"] != "simple" || response.Data.Captcha["question"] == "" || response.Data.Captcha["captcha_id"] == "" {
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
	formToken := s.issueContactFormToken(time.Now().Add(-5*time.Second), contactAntispam(s.cfg))
	body := fmt.Sprintf(`{"type":"privacy","reported_url":"","reason":"","copyrighted_work":"","good_faith":false,"accuracy":false,"signature":"","request_kind":"access","name":" %s ","email":"a@example.com","subject":"s","message":"m","form_token":%q,"website":""}`, strings.Repeat("界", 101), formToken)
	w := httptest.NewRecorder()
	s.handleContactAPI(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode validation response: %v", err)
	}
	data, _ := response["data"].(map[string]interface{})
	details, _ := data["details"].(map[string]interface{})
	if w.Code != http.StatusBadRequest || response["error"] != "VALIDATION_FAILED" || details["field"] != "name" || details["max_length"] != float64(100) {
		t.Errorf("unexpected max-length response: status=%d body=%s", w.Code, w.Body.String())
	}
	if data["form_token"] == "" || data["captcha"] == nil {
		t.Errorf("max-length rejection omitted form token/captcha refresh: %s", w.Body.String())
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

func TestLocalizedHelpContentFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		body interface{}
		lang string
		want string
	}{
		{name: "scalar", body: "all languages", lang: "es", want: "all languages"},
		{name: "resolved language", body: map[string]interface{}{"default": "fallback", "es": "espanol"}, lang: "es", want: "espanol"},
		{name: "default", body: map[string]interface{}{"default": "fallback", "fr": "francais"}, lang: "es", want: "fallback"},
		{name: "first sorted key", body: map[string]interface{}{"zz": "last", "fr": "first"}, lang: "es", want: "first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := localizedHelpContent(tc.body, tc.lang); got != tc.want {
				t.Fatalf("localizedHelpContent() = %q, want %q", got, tc.want)
			}
		})
	}
}
