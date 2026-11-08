package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
)

// TestRenderTrackingScript verifies each analytics platform emits its expected
// embed and that disabled/unknown tracking yields nothing (PART 31).
func TestRenderTrackingScript(t *testing.T) {
	cases := []struct {
		name    string
		track   config.TrackingConfig
		want    []string
		wantErr bool
	}{
		{"disabled empty", config.TrackingConfig{}, nil, false},
		{"disabled none", config.TrackingConfig{Type: "none"}, nil, false},
		{"unknown type", config.TrackingConfig{Type: "bogus", ID: "x"}, nil, false},
		{"google ga4", config.TrackingConfig{Type: "google", ID: "G-ABC123"},
			[]string{"googletagmanager.com/gtag/js?id=G-ABC123", "gtag('config', 'G-ABC123')"}, false},
		{"google legacy ua", config.TrackingConfig{Type: "google", ID: "UA-123-1"},
			[]string{"google-analytics.com/analytics.js", "ga('create', 'UA-123-1'"}, false},
		{"matomo", config.TrackingConfig{Type: "matomo", ID: "3", URL: "https://m.example.com"},
			[]string{"m.example.com/", "matomo.php", "setSiteId', '3'"}, false},
		{"piwik", config.TrackingConfig{Type: "piwik", ID: "7", URL: "https://p.example.com/"},
			[]string{"p.example.com/", "matomo.js", "setSiteId', '7'"}, false},
		{"owa", config.TrackingConfig{Type: "owa", ID: "site1", URL: "https://owa.example.com"},
			[]string{"owa.example.com/", "setSiteId', 'site1'"}, false},
		{"fathom cloud", config.TrackingConfig{Type: "fathom", ID: "ABCDEF"},
			[]string{"cdn.usefathom.com/script.js", `data-site="ABCDEF"`}, false},
		{"plausible cloud", config.TrackingConfig{Type: "plausible", ID: "example.com"},
			[]string{"plausible.io/js/script.js", `data-domain="example.com"`}, false},
		{"umami", config.TrackingConfig{Type: "umami", ID: "uuid-1", URL: "https://u.example.com"},
			[]string{"u.example.com/script.js", `data-website-id="uuid-1"`}, false},
		{"simple", config.TrackingConfig{Type: "simple", ID: ""},
			[]string{"simpleanalyticscdn.com/latest.js"}, false},
		{"cloudflare", config.TrackingConfig{Type: "cloudflare", ID: "tok123"},
			[]string{"cloudflareinsights.com/beacon.min.js", `"token": "tok123"`}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(renderTrackingScript(tc.track))
			if len(tc.want) == 0 {
				if got != "" {
					t.Fatalf("want empty, got %q", got)
				}
				return
			}
			for _, sub := range tc.want {
				if !strings.Contains(got, sub) {
					t.Errorf("output missing %q:\n%s", sub, got)
				}
			}
		})
	}
}

// TestBuildConsentClientConfig verifies the client consent config reflects the
// not-sold and sold variants and the opt-out defaults (PART 31).
func TestBuildConsentClientConfig(t *testing.T) {
	cfg := &config.Config{}
	p := &cfg.Server.Privacy
	p.Consent.ShowUntilAcknowledged = true
	p.Consent.DefaultEnabled = true
	p.Consent.Position = "top"
	p.Consent.Message = "We use cookies."
	p.Consent.MessageIfSold = "We use and sell cookies."
	p.Consent.Policy.Text = "Privacy Policy"
	p.Consent.Policy.URL = "/server/privacy"
	p.Consent.Buttons.Accept = "Accept"
	p.Consent.Buttons.Decline = "Decline"
	p.Consent.ShowPreferences = true
	p.Consent.PreferencesText = "Manage Preferences"
	p.Cookies.Preferences.Enabled = true
	p.Cookies.Analytics.Enabled = true
	p.Cookies.Essential.Description = "Required."
	p.Cookies.Preferences.Description = "Theme and language."
	p.Cookies.Analytics.Description = "Usage stats."
	p.Cookies.Analytics.DescriptionSuffixNotSold = "Never sold."
	p.Cookies.Analytics.DescriptionSuffixSold = "May be sold."
	cfg.Server.Tracking.Type = "plausible"
	cfg.Server.Tracking.ID = "example.com"

	t.Run("not sold", func(t *testing.T) {
		c := buildConsentClientConfig(cfg)
		if c.Message != "We use cookies." {
			t.Errorf("Message = %q, want not-sold variant", c.Message)
		}
		if c.Position != "top" {
			t.Errorf("Position = %q, want top", c.Position)
		}
		if !c.DefaultPreferences || !c.DefaultAnalytics {
			t.Errorf("defaults = (%v,%v), want both true", c.DefaultPreferences, c.DefaultAnalytics)
		}
		if !c.AnalyticsConfigured {
			t.Error("AnalyticsConfigured = false, want true")
		}
		if c.DataSold {
			t.Error("DataSold = true, want false")
		}
		if !strings.Contains(c.Descriptions.Analytics, "Never sold.") {
			t.Errorf("analytics desc = %q, want not-sold suffix", c.Descriptions.Analytics)
		}
	})

	t.Run("sold", func(t *testing.T) {
		p.Data.Sold = true
		c := buildConsentClientConfig(cfg)
		if c.Message != "We use and sell cookies." {
			t.Errorf("Message = %q, want sold variant", c.Message)
		}
		if !c.DataSold {
			t.Error("DataSold = false, want true")
		}
		if !strings.Contains(c.Descriptions.Analytics, "May be sold.") {
			t.Errorf("analytics desc = %q, want sold suffix", c.Descriptions.Analytics)
		}
	})

	t.Run("opt-out defaults off when default_enabled false", func(t *testing.T) {
		p.Consent.DefaultEnabled = false
		c := buildConsentClientConfig(cfg)
		if c.DefaultPreferences || c.DefaultAnalytics {
			t.Errorf("defaults = (%v,%v), want both false", c.DefaultPreferences, c.DefaultAnalytics)
		}
	})
}

func TestBuildConsentClientConfigLocalizesDefaultsAndPreservesOverrides(t *testing.T) {
	cfg := config.DefaultConfig()
	for _, lang := range []string{"en", "es", "fr", "de", "ar", "it", "ja", "zh"} {
		got := buildConsentClientConfig(cfg, lang)
		if want := i18n.Translate(lang, "privacy.consent_banner"); got.Message != want {
			t.Errorf("%s default consent message = %q, want %q", lang, got.Message, want)
		}
		if want := i18n.Translate(lang, "privacy.cookie_essential_description"); got.Descriptions.Essential != want {
			t.Errorf("%s essential description = %q, want %q", lang, got.Descriptions.Essential, want)
		}
	}

	cfg.Server.Privacy.Consent.Message = "Operator consent copy"
	cfg.Server.Privacy.Cookies.Essential.Description = "Operator cookie description"
	got := buildConsentClientConfig(cfg, "es")
	if got.Message != "Operator consent copy" || got.Descriptions.Essential != "Operator cookie description" {
		t.Errorf("operator overrides were translated: message=%q essential=%q", got.Message, got.Descriptions.Essential)
	}
}

// TestRenderConsentConfig verifies the emitted JSON is valid and carries the
// banner text.
func TestRenderConsentConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Privacy.Consent.ShowUntilAcknowledged = true

	js := string(renderConsentConfig(cfg, "es"))
	var decoded map[string]any
	if err := json.Unmarshal([]byte(js), &decoded); err != nil {
		t.Fatalf("emitted config is not valid JSON: %v\n%s", err, js)
	}
	if decoded["message"] != i18n.Translate("es", "privacy.consent_banner") {
		t.Errorf("message = %v, want translated Spanish default", decoded["message"])
	}
	if decoded["showUntilAcknowledged"] != true {
		t.Errorf("showUntilAcknowledged = %v, want true", decoded["showUntilAcknowledged"])
	}
}

// TestConsentPosition verifies banner position normalization.
func TestConsentPosition(t *testing.T) {
	if got := consentPosition("top"); got != "top" {
		t.Errorf("top = %q", got)
	}
	if got := consentPosition(""); got != "bottom" {
		t.Errorf("empty = %q, want bottom", got)
	}
	if got := consentPosition("weird"); got != "bottom" {
		t.Errorf("weird = %q, want bottom", got)
	}
}
