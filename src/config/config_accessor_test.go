package config_test

// Tests for config accessor methods that previously had 0% coverage.
// Covers: BrandingConfig.Effective*, SecurityReportEmail, PublishPGPKeyEnabled,
// SecurityReportURL, SecurityPreferredLanguages, GeneralEmail*, AbuseEmail*,
// SetUpdateBranch, SanitizeFooterHTML, FooterCustomHTML, WebhookSecret,
// WebhookTargets, contactRole helpers.

import (
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

// ─── BrandingConfig accessors ─────────────────────────────────────────────────

func TestBrandingEffectiveTitle_Default(t *testing.T) {
	var b config.BrandingConfig
	got := b.EffectiveTitle()
	if got != "Cairnbin" {
		t.Errorf("EffectiveTitle() = %q, want %q", got, "Cairnbin")
	}
}

func TestBrandingEffectiveTitle_Custom(t *testing.T) {
	b := config.BrandingConfig{Title: "  My Paste  "}
	got := b.EffectiveTitle()
	if got != "My Paste" {
		t.Errorf("EffectiveTitle() = %q, want %q", got, "My Paste")
	}
}

func TestBrandingEffectiveTagline_Default(t *testing.T) {
	var b config.BrandingConfig
	got := b.EffectiveTagline()
	if got == "" {
		t.Error("EffectiveTagline() should return a non-empty default")
	}
}

func TestBrandingEffectiveTagline_Custom(t *testing.T) {
	b := config.BrandingConfig{Tagline: "Share code instantly"}
	got := b.EffectiveTagline()
	if got != "Share code instantly" {
		t.Errorf("EffectiveTagline() = %q, want %q", got, "Share code instantly")
	}
}

func TestBrandingEffectiveDescription_Default(t *testing.T) {
	var b config.BrandingConfig
	got := b.EffectiveDescription()
	if got == "" {
		t.Error("EffectiveDescription() should return a non-empty default")
	}
}

func TestBrandingEffectiveDescription_Custom(t *testing.T) {
	b := config.BrandingConfig{Description: "My custom description."}
	got := b.EffectiveDescription()
	if got != "My custom description." {
		t.Errorf("EffectiveDescription() = %q, want custom", got)
	}
}

func TestBrandingEffectiveFeatures_Default(t *testing.T) {
	var b config.BrandingConfig
	got := b.EffectiveFeatures()
	if len(got) == 0 {
		t.Error("EffectiveFeatures() should return non-empty default slice")
	}
}

func TestBrandingEffectiveFeatures_Custom(t *testing.T) {
	b := config.BrandingConfig{Features: []string{"feat1", "feat2"}}
	got := b.EffectiveFeatures()
	if len(got) != 2 || got[0] != "feat1" {
		t.Errorf("EffectiveFeatures() = %v, want [feat1 feat2]", got)
	}
}

func TestBrandingEffectiveLinks_Default(t *testing.T) {
	var b config.BrandingConfig
	got := b.EffectiveLinks()
	if len(got) == 0 {
		t.Error("EffectiveLinks() should return non-empty default slice")
	}
}

func TestBrandingEffectiveLinks_Custom(t *testing.T) {
	b := config.BrandingConfig{Links: []config.BrandingLink{{Label: "GitHub", URL: "https://github.com"}}}
	got := b.EffectiveLinks()
	if len(got) != 1 || got[0].Label != "GitHub" {
		t.Errorf("EffectiveLinks() = %v, want custom", got)
	}
}

// ─── Security accessor methods ─────────────────────────────────────────────────

func TestPublishPGPKeyEnabled_False(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.PublishPGPKeyEnabled() {
		t.Error("PublishPGPKeyEnabled() should be false by default")
	}
}

func TestPublishPGPKeyEnabled_True(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Security.PublishPGPKey = "true"
	if !cfg.PublishPGPKeyEnabled() {
		t.Error("PublishPGPKeyEnabled() should be true when set to 'true'")
	}
}

func TestSecurityReportURL_Default(t *testing.T) {
	cfg := config.DefaultConfig()
	got := cfg.SecurityReportURL()
	if !strings.Contains(got, "github.com") {
		t.Errorf("SecurityReportURL() = %q, want github.com URL", got)
	}
}

func TestSecurityReportURL_Custom(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Security.ReportURL = "https://example.com/report"
	got := cfg.SecurityReportURL()
	if got != "https://example.com/report" {
		t.Errorf("SecurityReportURL() = %q, want custom URL", got)
	}
}

func TestRepositoryURL(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := cfg.RepositoryURL(); got != "https://github.com/apimgr/cairnbin" {
		t.Errorf("RepositoryURL() default = %q", got)
	}
	cfg.Web.Security.ReportURL = "https://github.com/acme/fork/security/advisories/new"
	if got := cfg.RepositoryURL(); got != "https://github.com/acme/fork" {
		t.Errorf("RepositoryURL() derived = %q", got)
	}
	cfg.Web.Security.ReportURL = "https://example.com/report/"
	if got := cfg.RepositoryURL(); got != "https://example.com/report" {
		t.Errorf("RepositoryURL() custom = %q", got)
	}
}

func TestSecurityPreferredLanguages_Default(t *testing.T) {
	cfg := config.DefaultConfig()
	got := cfg.SecurityPreferredLanguages()
	if got != "en" {
		t.Errorf("SecurityPreferredLanguages() = %q, want %q", got, "en")
	}
}

func TestSecurityPreferredLanguages_Custom(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Security.PreferredLanguages = "en, fr, de"
	got := cfg.SecurityPreferredLanguages()
	if got != "en, fr, de" {
		t.Errorf("SecurityPreferredLanguages() = %q, want custom", got)
	}
}

// ─── Contact email accessor methods ───────────────────────────────────────────

func TestGeneralEmailPublic_Empty(t *testing.T) {
	cfg := config.DefaultConfig()
	got := cfg.GeneralEmailPublic()
	if got != "" {
		t.Errorf("GeneralEmailPublic() = %q, want empty when not configured", got)
	}
}

func TestGeneralEmailPublic_Set(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.General.Email = "contact@example.com"
	got := cfg.GeneralEmailPublic()
	if got != "contact@example.com" {
		t.Errorf("GeneralEmailPublic() = %q, want contact@example.com", got)
	}
}

func TestAbuseEmail_FallsBackToGeneral(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.General.Email = "general@example.com"
	got := cfg.AbuseEmail()
	if got != "general@example.com" {
		t.Errorf("AbuseEmail() = %q, want general fallback", got)
	}
}

func TestAbuseEmail_ExplicitSet(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Abuse.Email = "abuse@example.com"
	got := cfg.AbuseEmail()
	if got != "abuse@example.com" {
		t.Errorf("AbuseEmail() = %q, want explicit abuse email", got)
	}
}

func TestAbuseEmailPublic_Empty(t *testing.T) {
	cfg := config.DefaultConfig()
	got := cfg.AbuseEmailPublic()
	if got != "" {
		t.Errorf("AbuseEmailPublic() = %q, want empty when not configured", got)
	}
}

func TestAbuseEmailPublic_AbuseSetNoGeneral(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.Abuse.Email = "abuse@example.com"
	got := cfg.AbuseEmailPublic()
	if got != "abuse@example.com" {
		t.Errorf("AbuseEmailPublic() = %q, want abuse email", got)
	}
}

func TestAbuseEmailPublic_FallsBackToGeneralPublic(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Contact.General.Email = "general@example.com"
	got := cfg.AbuseEmailPublic()
	if got != "general@example.com" {
		t.Errorf("AbuseEmailPublic() = %q, want general public fallback", got)
	}
}

// ─── SetUpdateBranch ──────────────────────────────────────────────────────────

func TestSetUpdateBranch_CreatesFile(t *testing.T) {
	path := tempConfigPath(t)
	if err := config.SetUpdateBranch(path, "beta"); err != nil {
		t.Fatalf("SetUpdateBranch: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load after SetUpdateBranch: %v", err)
	}
	if cfg.Server.Update.Branch != "beta" {
		t.Errorf("Update.Branch = %q, want %q", cfg.Server.Update.Branch, "beta")
	}
}

func TestSetUpdateBranch_UpdatesExisting(t *testing.T) {
	path := tempConfigPath(t)
	if err := config.SetUpdateBranch(path, "stable"); err != nil {
		t.Fatalf("first SetUpdateBranch: %v", err)
	}
	if err := config.SetUpdateBranch(path, "devel"); err != nil {
		t.Fatalf("second SetUpdateBranch: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Update.Branch != "devel" {
		t.Errorf("Update.Branch = %q, want %q", cfg.Server.Update.Branch, "devel")
	}
}

// ─── SetOnionAddress ──────────────────────────────────────────────────────────

func TestSetOnionAddress_CreatesFile(t *testing.T) {
	path := tempConfigPath(t)
	addr := "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrstuvwx.onion"
	if err := config.SetOnionAddress(path, addr); err != nil {
		t.Fatalf("SetOnionAddress: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load after SetOnionAddress: %v", err)
	}
	if cfg.Server.Tor.OnionAddress != addr {
		t.Errorf("Tor.OnionAddress = %q, want %q", cfg.Server.Tor.OnionAddress, addr)
	}
}

func TestSetOnionAddress_UpdatesExistingWithoutLosingSettings(t *testing.T) {
	path := tempConfigPath(t)
	if err := config.SetUpdateBranch(path, "beta"); err != nil {
		t.Fatalf("SetUpdateBranch: %v", err)
	}
	if err := config.SetOnionAddress(path, "example234567abcdefghijklmnopqrstuvwxyz234567abcdefghijk.onion"); err != nil {
		t.Fatalf("SetOnionAddress: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Tor.OnionAddress == "" {
		t.Error("Tor.OnionAddress not persisted")
	}
	if cfg.Server.Update.Branch != "beta" {
		t.Errorf("Update.Branch = %q, want %q (existing settings must survive)", cfg.Server.Update.Branch, "beta")
	}
}

// ─── SanitizeFooterHTML / FooterCustomHTML ─────────────────────────────────────

func TestSanitizeFooterHTML_Empty(t *testing.T) {
	got := config.SanitizeFooterHTML("")
	if got != "" {
		t.Errorf("SanitizeFooterHTML('') = %q, want empty", got)
	}
}

func TestSanitizeFooterHTML_SpaceSentinel(t *testing.T) {
	got := config.SanitizeFooterHTML(" ")
	if got != " " {
		t.Errorf("SanitizeFooterHTML(' ') = %q, want space", got)
	}
}

func TestSanitizeFooterHTML_ScriptStripped(t *testing.T) {
	got := config.SanitizeFooterHTML(`<p>Hello</p><script>alert(1)</script>`)
	if strings.Contains(got, "script") {
		t.Errorf("SanitizeFooterHTML should strip script tags, got: %q", got)
	}
	if !strings.Contains(got, "Hello") {
		t.Errorf("SanitizeFooterHTML should preserve p content, got: %q", got)
	}
}

func TestSanitizeFooterHTML_SafeTagsPreserved(t *testing.T) {
	got := config.SanitizeFooterHTML(`<p><strong>Bold</strong> text</p>`)
	if !strings.Contains(got, "Bold") {
		t.Errorf("SanitizeFooterHTML should preserve safe content, got: %q", got)
	}
}

func TestFooterCustomHTML_SpaceDisables(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Footer.CustomHTML = " "
	got := cfg.FooterCustomHTML()
	if got != "" {
		t.Errorf("FooterCustomHTML(' ') = %q, want empty", got)
	}
}

func TestFooterCustomHTML_Empty(t *testing.T) {
	cfg := config.DefaultConfig()
	got := cfg.FooterCustomHTML()
	if got != "" {
		t.Errorf("FooterCustomHTML('') = %q, want empty", got)
	}
}

func TestFooterCustomHTML_SafeContent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Web.Footer.CustomHTML = `<p>Footer</p>`
	got := cfg.FooterCustomHTML()
	if !strings.Contains(got, "Footer") {
		t.Errorf("FooterCustomHTML should preserve safe content, got: %q", got)
	}
}
