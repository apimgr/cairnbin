package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/geoip"
)

func TestAboutPageLocalizesShippedBrandingDefaultsAndPreservesOverrides(t *testing.T) {
	cfg := config.DefaultConfig()
	applyI18nConfig(cfg)
	defer applyI18nConfig(config.DefaultConfig())
	s := newMinimalServer(cfg)
	s.templates, _ = s.buildTemplates()

	for _, lang := range []string{"en", "es", "fr", "de", "ar", "it", "ja", "zh"} {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/server/about?lang="+lang, nil)
		r.Header.Set("Accept", "text/html")
		w := httptest.NewRecorder()
		s.handleAbout(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s about page status = %d: %s", lang, w.Code, w.Body.String())
		}
		if want := i18n.Translate(lang, "about.default_tagline"); !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s about page missing localized default tagline %q", lang, want)
		}
		if want := i18n.Translate(lang, "about.default_feature_1"); !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s about page missing localized default feature %q", lang, want)
		}
		if want := i18n.Translate(lang, "about.attribution_chroma"); !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s about page missing localized Chroma attribution %q", lang, want)
		}
		if !strings.Contains(w.Body.String(), "href=\"https://github.com/apimgr/cairnbin/blob/main/LICENSE.md\">LICENSE.md</a>") {
			t.Errorf("%s about page missing third-party license link", lang)
		}
	}

	cfg.Server.Branding.Tagline = "Operator tagline"
	cfg.Server.Branding.Description = "Operator description"
	cfg.Server.Branding.Features = []string{"Operator feature"}
	s = newMinimalServer(cfg)
	s.templates, _ = s.buildTemplates()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/server/about?lang=es", nil)
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	s.handleAbout(w, r)
	for _, text := range []string{"Operator tagline", "Operator description", "Operator feature"} {
		if !strings.Contains(w.Body.String(), text) {
			t.Errorf("operator branding override %q was not rendered", text)
		}
	}
	if strings.Contains(w.Body.String(), "Compatibilidad directa con pastebin.com") {
		t.Error("operator branding override was replaced with translated defaults")
	}
}

func TestAboutPageShowsExactDBIPAttributionWhenGeoIPIsEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.GeoIP.Enabled = true
	s := newMinimalServer(cfg)
	s.geoipDB = &geoip.DB{}
	s.templates, _ = s.buildTemplates()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/server/about", nil)
	r.Header.Set("Accept", "text/html")
	s.handleAbout(w, r)
	if !strings.Contains(w.Body.String(), `<a href="https://db-ip.com/">IP Geolocation by DB-IP</a>`) {
		t.Fatal("about page is missing the required verbatim DB-IP attribution")
	}
}
