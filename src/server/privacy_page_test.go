package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

// TestRenderMarkdown verifies operator Markdown is converted and raw HTML is not
// passed through (goldmark runs without WithUnsafe).
func TestRenderMarkdown(t *testing.T) {
	if got := renderMarkdown(""); got != "" {
		t.Errorf("empty input = %q, want empty", got)
	}

	out := string(renderMarkdown("**bold** and _italic_"))
	if !strings.Contains(out, "<strong>bold</strong>") {
		t.Errorf("bold not rendered: %q", out)
	}
	if !strings.Contains(out, "<em>italic</em>") {
		t.Errorf("italic not rendered: %q", out)
	}

	list := string(renderMarkdown("- one\n- two\n"))
	if !strings.Contains(list, "<ul>") || !strings.Contains(list, "<li>one</li>") {
		t.Errorf("list not rendered: %q", list)
	}

	// Raw HTML in operator content must not be emitted as active markup.
	unsafe := string(renderMarkdown("Hello <script>alert(1)</script>"))
	if strings.Contains(unsafe, "<script>") {
		t.Errorf("raw <script> should not pass through: %q", unsafe)
	}
}

// TestHandleCCPAOptOut verifies the opt-out cookie is set and cleared and the
// handler redirects back to the anchored privacy section.
func TestHandleCCPAOptOut(t *testing.T) {
	s := newMinimalServer(&config.Config{})

	t.Run("opt out sets persistent cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/server/ccpa", strings.NewReader("choice=opt-out"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleCCPAOptOut(w, r)

		if w.Code != http.StatusSeeOther {
			t.Fatalf("status = %d, want 303", w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/server/privacy#ccpa-opt-out" {
			t.Errorf("Location = %q, want /server/privacy#ccpa-opt-out", loc)
		}
		c := findCookie(w.Result().Cookies(), "ccpa_opt_out")
		if c == nil {
			t.Fatal("ccpa_opt_out cookie not set")
		}
		if c.Value != "true" {
			t.Errorf("cookie value = %q, want true", c.Value)
		}
		if c.MaxAge <= 0 {
			t.Errorf("cookie MaxAge = %d, want positive", c.MaxAge)
		}
	})

	t.Run("opt in clears cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/server/ccpa", strings.NewReader("choice=opt-in"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.handleCCPAOptOut(w, r)

		c := findCookie(w.Result().Cookies(), "ccpa_opt_out")
		if c == nil {
			t.Fatal("ccpa_opt_out cookie not set")
		}
		if c.Value != "" {
			t.Errorf("cookie value = %q, want empty", c.Value)
		}
		if c.MaxAge >= 0 {
			t.Errorf("cookie MaxAge = %d, want negative (delete)", c.MaxAge)
		}
	})
}

// TestPrivacyPageData verifies the privacy template data reflects the CCPA
// cookie and auto-populated third-party analytics service.
func TestPrivacyPageData(t *testing.T) {
	cfg := &config.Config{}
	cfg.Web.SiteTitle = "Test Paste"
	cfg.Server.Tracking.Type = "matomo"
	cfg.Server.Tracking.ID = "1"
	cfg.Server.Tracking.URL = "https://analytics.example.com"
	s := newMinimalServer(cfg)

	t.Run("no cookie means not opted out", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/server/privacy", nil)
		data := s.privacyPageData(r)
		if data["CCPAOptedOut"].(bool) {
			t.Error("CCPAOptedOut = true, want false without cookie")
		}
		if _, ok := data["Privacy"].(*config.PrivacyConfig); !ok {
			t.Error("Privacy not a *config.PrivacyConfig")
		}
		if _, ok := data["Tracking"].(*config.TrackingConfig); !ok {
			t.Error("Tracking not a *config.TrackingConfig")
		}
		services, ok := data["ThirdPartyServices"].([]config.ThirdPartyService)
		if !ok {
			t.Fatal("ThirdPartyServices wrong type")
		}
		if len(services) != 1 || services[0].Name != "Matomo" {
			t.Errorf("ThirdPartyServices = %+v, want auto Matomo entry", services)
		}
	})

	t.Run("cookie set means opted out", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/server/privacy", nil)
		r.AddCookie(&http.Cookie{Name: "ccpa_opt_out", Value: "true"})
		data := s.privacyPageData(r)
		if !data["CCPAOptedOut"].(bool) {
			t.Error("CCPAOptedOut = false, want true with cookie")
		}
	})
}

func TestPrivacyPageLocalizesDefaultMarkdownAndPreservesOperatorOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.SiteTitle = "Test Paste"
	applyI18nConfig(cfg)
	defer applyI18nConfig(config.DefaultConfig())
	s := newMinimalServer(cfg)
	s.templates, _ = s.buildTemplates()

	r := httptest.NewRequest(http.MethodGet, "http://example.com/server/privacy?lang=es", nil)
	w := httptest.NewRecorder()
	s.handlePrivacy(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("privacy page status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Información tratada para prestar el servicio") {
		t.Error("Spanish privacy page did not render translated default data collection content")
	}

	s = newMinimalServer(cfg)
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	configDir, err := os.MkdirTemp(base, "cairnbin-privacy-test-")
	if err != nil {
		t.Fatal(err)
	}
	s.configDir = configDir
	t.Cleanup(func() { _ = os.RemoveAll(s.configDir) })
	writeOperatorPage(t, s, "privacy.md", "**Operator-authored disclosure**")
	s.templates, _ = s.buildTemplates()
	r = httptest.NewRequest(http.MethodGet, "http://example.com/server/privacy?lang=es", nil)
	w = httptest.NewRecorder()
	s.handlePrivacy(w, r)
	if !strings.Contains(w.Body.String(), "Operator-authored disclosure") {
		t.Error("privacy page did not preserve operator-authored disclosure")
	}
}

func TestPrivacyPageShowsOnlyPublicDPOContact(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Contact.Admin.Email = "admin-secret@example.test"
	s.cfg.Server.Contact.General.Email = "general@example.test"
	s.cfg.Server.Contact.DPO.Name = "Data Officer"
	s.cfg.Server.Contact.DPO.Email = "dpo@example.test"

	get := func() string {
		r := httptest.NewRequest(http.MethodGet, "/server/privacy", nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		s.handlePrivacy(w, r)
		return w.Body.String()
	}
	body := get()
	if !strings.Contains(body, "Data Officer") || !strings.Contains(body, `mailto:dpo@example.test`) {
		t.Error("privacy page did not show the configured DPO name and email")
	}
	if strings.Contains(body, "admin-secret@example.test") || strings.Contains(body, "general@example.test") {
		t.Error("privacy page exposed an address other than the configured DPO address")
	}

	s.cfg.Server.Contact.DPO.Email = ""
	body = get()
	if !strings.Contains(body, `mailto:general@example.test`) || strings.Contains(body, "admin-secret@example.test") {
		t.Error("privacy page did not use the public general address fallback safely")
	}

	s.cfg.Server.Contact.General.Email = ""
	body = get()
	if !strings.Contains(body, `href="/server/security"`) || strings.Contains(body, "mailto:") || strings.Contains(body, "admin-secret@example.test") {
		t.Error("privacy page did not link to security when no public contact address resolves")
	}
}

func TestDPOPageUsesPublicAddressFallback(t *testing.T) {
	s := pagesServer(t)
	s.cfg.Server.Contact.Admin.Email = "admin-secret@example.test"
	s.cfg.Server.Contact.General.Email = "general@example.test"
	s.cfg.Server.Contact.DPO.Name = "Data Officer"
	s.cfg.Server.Contact.DPO.Email = "dpo@example.test"

	get := func() string {
		r := httptest.NewRequest(http.MethodGet, "/server/dpo", nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		s.handleDPO(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("DPO page status = %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	body := get()
	if !strings.Contains(body, "Data Officer") || !strings.Contains(body, `mailto:dpo@example.test`) || !strings.Contains(body, `href="/server/privacy"`) {
		t.Error("DPO page did not show the configured name, email, and privacy link")
	}
	if strings.Contains(body, "admin-secret@example.test") || strings.Contains(body, "general@example.test") {
		t.Error("DPO page exposed an address other than the configured DPO address")
	}

	s.cfg.Server.Contact.DPO.Email = ""
	body = get()
	if !strings.Contains(body, `mailto:general@example.test`) || strings.Contains(body, "admin-secret@example.test") {
		t.Error("DPO page did not use the public general address fallback safely")
	}

	s.cfg.Server.Contact.General.Email = ""
	body = get()
	if !strings.Contains(body, `href="/server/security"`) || strings.Contains(body, "mailto:") || strings.Contains(body, "admin-secret@example.test") {
		t.Error("DPO page did not link to security when no public address resolves")
	}
}

// findCookie returns the named cookie from a slice, or nil.
func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}
