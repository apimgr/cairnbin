package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/config"
)

// TestValidateSEOVerification_NamedProviders covers each of the six provider
// regex/length rules with valid, invalid, and oversized inputs (AI.md 24501-24508).
func TestValidateSEOVerification_NamedProviders(t *testing.T) {
	cases := []struct {
		name    string
		build   func(v *config.SEOVerificationConfig)
		wantErr bool
	}{
		{"empty is valid (not configured)", func(v *config.SEOVerificationConfig) {}, false},
		{"google valid", func(v *config.SEOVerificationConfig) { v.Google = "abc123_-XYZ" }, false},
		{"google invalid chars", func(v *config.SEOVerificationConfig) { v.Google = "abc!123" }, true},
		{"google too long", func(v *config.SEOVerificationConfig) { v.Google = strings.Repeat("a", 44) }, true},
		{"bing valid", func(v *config.SEOVerificationConfig) { v.Bing = "ABCDEF0123456789" }, false},
		{"bing invalid lowercase", func(v *config.SEOVerificationConfig) { v.Bing = "abcdef0123456789" }, true},
		{"bing too long", func(v *config.SEOVerificationConfig) { v.Bing = strings.Repeat("A", 33) }, true},
		{"yandex valid", func(v *config.SEOVerificationConfig) { v.Yandex = "abcdef0123456789" }, false},
		{"yandex invalid uppercase", func(v *config.SEOVerificationConfig) { v.Yandex = "ABCDEF0123456789" }, true},
		{"baidu valid", func(v *config.SEOVerificationConfig) { v.Baidu = "AbC123xyz" }, false},
		{"baidu invalid chars", func(v *config.SEOVerificationConfig) { v.Baidu = "AbC-123" }, true},
		{"pinterest valid", func(v *config.SEOVerificationConfig) { v.Pinterest = "abcdef0123456789" }, false},
		{"pinterest invalid chars", func(v *config.SEOVerificationConfig) { v.Pinterest = "ABCDEF0123456789" }, true},
		{"facebook valid", func(v *config.SEOVerificationConfig) { v.Facebook = "abc0123xyz" }, false},
		{"facebook invalid uppercase", func(v *config.SEOVerificationConfig) { v.Facebook = "ABC0123xyz" }, true},
		{"facebook too long", func(v *config.SEOVerificationConfig) { v.Facebook = strings.Repeat("a", 65) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v config.SEOVerificationConfig
			tc.build(&v)
			errs := ValidateSEOVerification(v)
			if tc.wantErr && len(errs) == 0 {
				t.Errorf("expected validation error, got none")
			}
			if !tc.wantErr && len(errs) != 0 {
				t.Errorf("expected no validation error, got %v", errs)
			}
		})
	}
}

// TestValidateSEOVerification_CustomTags covers the custom-tag rules: exactly
// one of name/property required, content required and max 256, and name/property
// character restrictions (AI.md 24525-24531).
func TestValidateSEOVerification_CustomTags(t *testing.T) {
	cases := []struct {
		name    string
		tag     config.SEOCustomVerificationTag
		wantErr bool
	}{
		{"valid name", config.SEOCustomVerificationTag{Name: "custom-verify", Content: "abc123"}, false},
		{"valid property with colon", config.SEOCustomVerificationTag{Property: "fb:app_id", Content: "abc123"}, false},
		{"neither name nor property", config.SEOCustomVerificationTag{Content: "abc123"}, true},
		{"both name and property", config.SEOCustomVerificationTag{Name: "a", Property: "b", Content: "abc123"}, true},
		{"empty content", config.SEOCustomVerificationTag{Name: "custom-verify", Content: ""}, true},
		{"content too long", config.SEOCustomVerificationTag{Name: "custom-verify", Content: strings.Repeat("x", 257)}, true},
		{"invalid name chars", config.SEOCustomVerificationTag{Name: "custom verify!", Content: "abc123"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := config.SEOVerificationConfig{Custom: []config.SEOCustomVerificationTag{tc.tag}}
			errs := ValidateSEOVerification(v)
			if tc.wantErr && len(errs) == 0 {
				t.Errorf("expected validation error, got none")
			}
			if !tc.wantErr && len(errs) != 0 {
				t.Errorf("expected no validation error, got %v", errs)
			}
		})
	}
}

// TestLogSEOVerificationErrors verifies the startup-log helper does not panic
// for either valid or invalid configuration (AI.md 24551).
func TestLogSEOVerificationErrors(t *testing.T) {
	logSEOVerificationErrors(config.SEOVerificationConfig{})
	logSEOVerificationErrors(config.SEOVerificationConfig{Google: "!!!invalid!!!"})
}

func newSEOTestServer(t *testing.T) (*Server, *config.Config) {
	t.Helper()
	cfg := config.DefaultConfig()
	s := &Server{cfg: cfg}
	return s, cfg
}

// TestSeoMetaTags_AlwaysPresent verifies the title/description/OG/Twitter tags
// that render unconditionally, and that dynamic values are HTML-escaped.
func TestSeoMetaTags_AlwaysPresent(t *testing.T) {
	s, cfg := newSEOTestServer(t)
	cfg.Server.Branding.Title = `Cairnbin <script>alert(1)</script>`
	cfg.Server.Branding.Description = "A test & example description"

	r := httptest.NewRequest("GET", "http://example.com/paste/abc", nil)
	out := string(s.seoMetaTags(r))

	if !strings.Contains(out, `<meta property="og:title" content="Cairnbin &lt;script&gt;alert(1)&lt;/script&gt;">`) {
		t.Errorf("expected escaped og:title, got: %s", out)
	}
	if !strings.Contains(out, `<meta property="og:description" content="A test &amp; example description">`) {
		t.Errorf("expected escaped og:description, got: %s", out)
	}
	if !strings.Contains(out, `<meta name="twitter:card" content="summary_large_image">`) {
		t.Errorf("expected twitter:card tag, got: %s", out)
	}
	if !strings.Contains(out, `<meta property="og:type" content="website">`) {
		t.Errorf("expected og:type tag, got: %s", out)
	}
	if !strings.Contains(out, `<meta property="og:url" content="http://example.com/paste/abc">`) {
		t.Errorf("expected og:url tag, got: %s", out)
	}
}

// TestSeoMetaTags_Conditional verifies keywords/author/twitter_handle are
// only rendered when configured, and omitted when blank. og:image/
// twitter:image always render, pointing at the cached branding endpoint
// rather than the raw og_image config value (PART 16 "Image Scaling").
func TestSeoMetaTags_Conditional(t *testing.T) {
	s, cfg := newSEOTestServer(t)
	r := httptest.NewRequest("GET", "http://example.com/", nil)

	out := string(s.seoMetaTags(r))
	if strings.Contains(out, `name="keywords"`) || strings.Contains(out, `name="author"`) ||
		strings.Contains(out, `twitter:site`) {
		t.Errorf("expected no conditional tags when unset, got: %s", out)
	}
	wantImage := `<meta property="og:image" content="` + s.baseURL(r) + `/static/branding/og-image.png">`
	if !strings.Contains(out, wantImage) {
		t.Errorf("expected og:image tag pointing at cached branding endpoint, got: %s", out)
	}
	wantTwitterImage := `<meta name="twitter:image" content="` + s.baseURL(r) + `/static/branding/og-image.png">`
	if !strings.Contains(out, wantTwitterImage) {
		t.Errorf("expected twitter:image tag pointing at cached branding endpoint, got: %s", out)
	}

	cfg.Server.SEO.Keywords = []string{"paste", "code"}
	cfg.Server.SEO.Author = "Jane Doe"
	cfg.Server.SEO.OGImage = "https://example.com/og.png"
	cfg.Server.SEO.TwitterHandle = "@cairnbin"

	out = string(s.seoMetaTags(r))
	if !strings.Contains(out, `<meta name="keywords" content="paste, code">`) {
		t.Errorf("expected keywords tag, got: %s", out)
	}
	if !strings.Contains(out, `<meta name="author" content="Jane Doe">`) {
		t.Errorf("expected author tag, got: %s", out)
	}
	// og:image/twitter:image still point at the cached branding endpoint, not
	// the raw configured URL, even once server.seo.og_image is set — the raw
	// value only selects the source RefreshBrandingImages() fetches from.
	if !strings.Contains(out, wantImage) {
		t.Errorf("expected og:image tag to remain the cached branding endpoint, got: %s", out)
	}
	if !strings.Contains(out, wantTwitterImage) {
		t.Errorf("expected twitter:image tag to remain the cached branding endpoint, got: %s", out)
	}
	if !strings.Contains(out, `<meta name="twitter:site" content="@cairnbin">`) {
		t.Errorf("expected twitter:site tag, got: %s", out)
	}
}

// TestSeoMetaTags_Verification verifies valid verification codes render and
// invalid/empty codes are silently skipped (never rendered) — AI.md 24533-24537.
func TestSeoMetaTags_Verification(t *testing.T) {
	s, cfg := newSEOTestServer(t)
	r := httptest.NewRequest("GET", "http://example.com/", nil)

	cfg.Server.SEO.Verification.Google = "valid-code_123"
	cfg.Server.SEO.Verification.Bing = "not-valid-lowercase"
	cfg.Server.SEO.Verification.Facebook = "abc123"

	out := string(s.seoMetaTags(r))
	if !strings.Contains(out, `<meta name="google-site-verification" content="valid-code_123">`) {
		t.Errorf("expected google verification tag, got: %s", out)
	}
	if strings.Contains(out, `msvalidate.01`) {
		t.Errorf("invalid bing code must not be rendered, got: %s", out)
	}
	if !strings.Contains(out, `<meta property="fb:domain_verification" content="abc123">`) {
		t.Errorf("expected facebook verification tag (property attr), got: %s", out)
	}
}

// TestSeoMetaTags_CustomTags verifies custom tags render with the correct
// attribute (name vs property) and invalid/oversized ones are skipped.
func TestSeoMetaTags_CustomTags(t *testing.T) {
	s, cfg := newSEOTestServer(t)
	r := httptest.NewRequest("GET", "http://example.com/", nil)

	cfg.Server.SEO.Verification.Custom = []config.SEOCustomVerificationTag{
		{Name: "custom-name", Content: "value-one"},
		{Property: "fb:app_id", Content: "value-two"},
		{Name: "bad name!", Content: "skipped"},
		{Name: "empty-content", Content: ""},
		{Name: "both", Property: "set", Content: "skipped"},
	}

	out := string(s.seoMetaTags(r))
	if !strings.Contains(out, `<meta name="custom-name" content="value-one">`) {
		t.Errorf("expected custom name tag, got: %s", out)
	}
	if !strings.Contains(out, `<meta property="fb:app_id" content="value-two">`) {
		t.Errorf("expected custom property tag, got: %s", out)
	}
	if strings.Contains(out, "skipped") {
		t.Errorf("invalid custom tags must not be rendered, got: %s", out)
	}
}
