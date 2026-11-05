package config_test

// Tests for the PART 31 privacy/consent/tracking configuration: dynamic
// sold/not-sold messaging helpers, tracking validation, third-party
// auto-population, and consent-banner warn-and-default behaviour.

import (
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

// TestPrivacyDefaults verifies the documented PART 31 defaults land in the
// default config (data not sold, opt-out consent model, bottom banner).
func TestPrivacyDefaults(t *testing.T) {
	p := config.DefaultConfig().Server.Privacy
	if p.Data.Sold {
		t.Error("Data.Sold: got true, want false (default: we do not sell data)")
	}
	if !p.Data.StoredOnServer {
		t.Error("Data.StoredOnServer: got false, want true")
	}
	if len(p.Data.Sharing) != 3 {
		t.Errorf("Data.Sharing: got %d conditions, want 3", len(p.Data.Sharing))
	}
	if !p.Consent.ShowUntilAcknowledged || !p.Consent.DefaultEnabled {
		t.Error("Consent: ShowUntilAcknowledged and DefaultEnabled should default true")
	}
	if p.Consent.Position != "bottom" {
		t.Errorf("Consent.Position: got %q, want bottom", p.Consent.Position)
	}
	if !p.Consent.ShowPreferences || p.Consent.PreferencesText != "Manage Preferences" {
		t.Errorf("Consent preferences default mismatch: %+v", p.Consent)
	}
	if !p.Cookies.Essential.Enabled {
		t.Error("Cookies.Essential.Enabled must default true")
	}
	if !p.Retention.ExportAvailable || !p.Retention.DeletionAvailable {
		t.Error("Retention export/deletion should default true")
	}
}

// TestPrivacyDynamicMessaging verifies GetConsentMessage, GetAnalyticsDescription,
// GetDataUsageContent, and IsCCPAApplicable flip on Data.Sold.
func TestPrivacyDynamicMessaging(t *testing.T) {
	p := config.DefaultConfig().Server.Privacy

	// Not sold (default).
	if got := p.GetConsentMessage(); got != p.Consent.Message {
		t.Errorf("GetConsentMessage (not sold): got %q, want Message", got)
	}
	if got := p.GetDataUsageContent(); got != p.Content.DataUsage {
		t.Error("GetDataUsageContent (not sold): want DataUsage")
	}
	if p.IsCCPAApplicable() {
		t.Error("IsCCPAApplicable: got true when data not sold")
	}
	notSold := p.GetAnalyticsDescription()
	if want := p.Cookies.Analytics.Description + " " + p.Cookies.Analytics.DescriptionSuffixNotSold; notSold != want {
		t.Errorf("GetAnalyticsDescription (not sold): got %q, want %q", notSold, want)
	}

	// Sold.
	p.Data.Sold = true
	if got := p.GetConsentMessage(); got != p.Consent.MessageIfSold {
		t.Errorf("GetConsentMessage (sold): got %q, want MessageIfSold", got)
	}
	if got := p.GetDataUsageContent(); got != p.Content.DataUsageIfSold {
		t.Error("GetDataUsageContent (sold): want DataUsageIfSold")
	}
	if !p.IsCCPAApplicable() {
		t.Error("IsCCPAApplicable: got false when data sold")
	}
	sold := p.GetAnalyticsDescription()
	if want := p.Cookies.Analytics.Description + " " + p.Cookies.Analytics.DescriptionSuffixSold; sold != want {
		t.Errorf("GetAnalyticsDescription (sold): got %q, want %q", sold, want)
	}
}

// TestTrackingTypeName verifies friendly platform names and Enabled().
func TestTrackingTypeName(t *testing.T) {
	if (config.TrackingConfig{}).Enabled() {
		t.Error("empty tracking config must report disabled")
	}
	if (config.TrackingConfig{Type: "none"}).Enabled() {
		t.Error("type none must report disabled")
	}
	tc := config.TrackingConfig{Type: "matomo"}
	if !tc.Enabled() {
		t.Error("matomo must report enabled")
	}
	if got := tc.TypeName(); got != "Matomo" {
		t.Errorf("TypeName: got %q, want Matomo", got)
	}
	if got := (config.TrackingConfig{Type: "unknown"}).TypeName(); got != "unknown" {
		t.Errorf("TypeName unknown fallback: got %q", got)
	}
}

// TestValidateTracking covers the per-platform ID/URL validation rules (PART 31).
func TestValidateTracking(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.TrackingConfig
		wantErr bool
	}{
		{"disabled empty", config.TrackingConfig{}, false},
		{"disabled none", config.TrackingConfig{Type: "none"}, false},
		{"google ga4 ok", config.TrackingConfig{Type: "google", ID: "G-ABCDEF1234"}, false},
		{"google ua ok", config.TrackingConfig{Type: "google", ID: "UA-12345-6"}, false},
		{"google bad", config.TrackingConfig{Type: "google", ID: "nope"}, true},
		{"matomo ok", config.TrackingConfig{Type: "matomo", ID: "1", URL: "https://a.example.com"}, false},
		{"matomo no url", config.TrackingConfig{Type: "matomo", ID: "1"}, true},
		{"matomo bad id", config.TrackingConfig{Type: "matomo", ID: "x", URL: "https://a.example.com"}, true},
		{"umami ok", config.TrackingConfig{Type: "umami", ID: "3f2504e0-4f89-41d3-9a0c-0305e82c3301", URL: "https://a.example.com"}, false},
		{"umami bad uuid", config.TrackingConfig{Type: "umami", ID: "nope", URL: "https://a.example.com"}, true},
		{"cloudflare ok", config.TrackingConfig{Type: "cloudflare", ID: "tok"}, false},
		{"cloudflare no id", config.TrackingConfig{Type: "cloudflare"}, true},
		{"simple ok", config.TrackingConfig{Type: "simple"}, false},
		{"plausible ok", config.TrackingConfig{Type: "plausible", ID: "example.com"}, false},
		{"unknown type", config.TrackingConfig{Type: "bogus", ID: "x"}, true},
		{"bad url", config.TrackingConfig{Type: "fathom", ID: "ABCDEFGH", URL: "://broken"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := config.ValidateTracking(&tc.cfg)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateTracking(%+v) err = %v, wantErr %v", tc.cfg, err, tc.wantErr)
			}
		})
	}
}

// TestEffectiveThirdParty verifies analytics auto-population from server.tracking.
func TestEffectiveThirdParty(t *testing.T) {
	// Disabled tracking: only manual entries returned.
	c := config.DefaultConfig()
	c.Server.Privacy.ThirdParty.Services = []config.ThirdPartyService{{Name: "Manual", Purpose: "p"}}
	if got := c.EffectiveThirdParty(); len(got) != 1 || got[0].Name != "Manual" {
		t.Errorf("disabled tracking: got %+v, want just Manual", got)
	}

	// Enabled tracking: analytics service auto-prepended.
	c.Server.Tracking = config.TrackingConfig{Type: "matomo", ID: "1", URL: "https://a.example.com"}
	got := c.EffectiveThirdParty()
	if len(got) != 2 {
		t.Fatalf("enabled tracking: got %d services, want 2", len(got))
	}
	if got[0].Name != "Matomo" || got[0].PolicyURL == "" {
		t.Errorf("auto-populated analytics entry wrong: %+v", got[0])
	}

	// Already-listed analytics is not duplicated.
	c.Server.Privacy.ThirdParty.Services = []config.ThirdPartyService{{Name: "Matomo", Purpose: "manual"}}
	if got := c.EffectiveThirdParty(); len(got) != 1 {
		t.Errorf("duplicate analytics: got %d services, want 1", len(got))
	}
}

// TestValidateDisablesBadTracking verifies Validate warn-and-disables invalid
// tracking rather than failing startup (PART 5 warn-and-default).
func TestValidateDisablesBadTracking(t *testing.T) {
	c := config.DefaultConfig()
	c.Server.Tracking = config.TrackingConfig{Type: "google", ID: "invalid"}
	config.Validate(c)
	if c.Server.Tracking.Enabled() {
		t.Errorf("Validate should disable invalid tracking, got %+v", c.Server.Tracking)
	}
}

// TestValidateConsentDefaults verifies invalid banner position and blank text
// fields fall back to defaults, and essential cookies stay forced on.
func TestValidateConsentDefaults(t *testing.T) {
	c := config.DefaultConfig()
	c.Server.Privacy.Consent.Position = "sideways"
	c.Server.Privacy.Consent.Message = ""
	c.Server.Privacy.Consent.Buttons.Accept = ""
	c.Server.Privacy.Cookies.Essential.Enabled = false
	config.Validate(c)
	if c.Server.Privacy.Consent.Position != "bottom" {
		t.Errorf("Position: got %q, want bottom", c.Server.Privacy.Consent.Position)
	}
	if c.Server.Privacy.Consent.Message == "" {
		t.Error("blank Message should fall back to default")
	}
	if c.Server.Privacy.Consent.Buttons.Accept == "" {
		t.Error("blank Accept button should fall back to default")
	}
	if !c.Server.Privacy.Cookies.Essential.Enabled {
		t.Error("Essential cookies must be forced enabled")
	}
}
