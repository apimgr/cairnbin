package server

// SEO meta tag generation and site-verification validation (PART 16
// "Branding & SEO" / "Site Verification Meta Tags", AI.md 24380-24537).
//
// Title/tagline/description/keywords/author/og_image/twitter_handle feed the
// standard SEO + OpenGraph + Twitter Card tags, always rendered. Verification
// codes (google/bing/yandex/baidu/pinterest/facebook) and operator-supplied
// custom tags are rendered only when configured AND valid — an invalid or
// empty value is silently dropped from output (never rendered), while
// startup validation additionally logs the error so the operator notices.

import (
	"encoding/json"
	"html"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/apimgr/cairnbin/src/config"
)

// Provider verification-code format rules (AI.md 24501-24508).
var (
	seoVerifyGoogleRe    = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	seoVerifyBingRe      = regexp.MustCompile(`^[A-F0-9]+$`)
	seoVerifyYandexRe    = regexp.MustCompile(`^[a-f0-9]+$`)
	seoVerifyBaiduRe     = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
	seoVerifyPinterestRe = regexp.MustCompile(`^[a-f0-9]+$`)
	seoVerifyFacebookRe  = regexp.MustCompile(`^[a-z0-9]+$`)
	// seoCustomNameRe validates a custom tag's name/property attribute value:
	// alphanumeric plus hyphens/underscores only (AI.md 24531). Custom tag
	// names/properties may contain colons (e.g. "fb:app_id"), so ':' is
	// permitted in addition to the spec's alphanumeric+hyphen/underscore set.
	seoCustomNameRe = regexp.MustCompile(`^[a-zA-Z0-9_:-]+$`)
)

type seoVerifyRule struct {
	label   string
	value   string
	metaKey string
	isProp  bool
	pattern *regexp.Regexp
	maxLen  int
}

// seoVerifyRules returns the six named-provider verification rules in
// rendering order (AI.md 24487-24496).
func seoVerifyRules(v config.SEOVerificationConfig) []seoVerifyRule {
	return []seoVerifyRule{
		{"google", v.Google, "google-site-verification", false, seoVerifyGoogleRe, 43},
		{"bing", v.Bing, "msvalidate.01", false, seoVerifyBingRe, 32},
		{"yandex", v.Yandex, "yandex-verification", false, seoVerifyYandexRe, 32},
		{"baidu", v.Baidu, "baidu-site-verification", false, seoVerifyBaiduRe, 32},
		{"pinterest", v.Pinterest, "p:domain_verify", false, seoVerifyPinterestRe, 32},
		{"facebook", v.Facebook, "fb:domain_verification", true, seoVerifyFacebookRe, 64},
	}
}

// ValidateSEOVerification checks every configured verification code and
// custom tag against PART 16's format/length rules and returns one error
// message per invalid entry. Called at startup so operators are warned about
// misconfigured codes (AI.md 24551 "Server validates codes on startup and
// logs errors for invalid formats") — validation failures are warnings only
// and never abort startup; the offending tag is simply never rendered.
func ValidateSEOVerification(v config.SEOVerificationConfig) []string {
	var errs []string
	for _, rule := range seoVerifyRules(v) {
		if rule.value == "" {
			continue
		}
		if len(rule.value) > rule.maxLen {
			errs = append(errs, "seo.verification."+rule.label+": exceeds max length of "+itoaSEO(rule.maxLen))
			continue
		}
		if !rule.pattern.MatchString(rule.value) {
			errs = append(errs, "seo.verification."+rule.label+": does not match required format")
		}
	}
	for i, c := range v.Custom {
		name := strings.TrimSpace(c.Name)
		prop := strings.TrimSpace(c.Property)
		if name == "" && prop == "" {
			errs = append(errs, "seo.verification.custom["+itoaSEO(i)+"]: requires name or property")
			continue
		}
		if name != "" && prop != "" {
			errs = append(errs, "seo.verification.custom["+itoaSEO(i)+"]: only one of name or property is allowed")
			continue
		}
		attr := name
		if attr == "" {
			attr = prop
		}
		if !seoCustomNameRe.MatchString(attr) {
			errs = append(errs, "seo.verification.custom["+itoaSEO(i)+"]: name/property must be alphanumeric, hyphens, or underscores only")
		}
		if strings.TrimSpace(c.Content) == "" {
			errs = append(errs, "seo.verification.custom["+itoaSEO(i)+"]: content is required")
		} else if len(c.Content) > 256 {
			errs = append(errs, "seo.verification.custom["+itoaSEO(i)+"]: content exceeds max length of 256")
		}
	}
	return errs
}

// logSEOVerificationErrors runs ValidateSEOVerification and logs each finding
// (startup-only warning path; never fatal — PART 16).
func logSEOVerificationErrors(v config.SEOVerificationConfig) {
	for _, msg := range ValidateSEOVerification(v) {
		log.Printf("config: invalid %s", msg)
	}
}

// jsonMarshalSEO renders the JSON-LD structured-data object. json.Marshal
// HTML-escapes '<', '>', and '&' by default, so operator-supplied title/
// description/URL values can never break out of the surrounding
// <script type="application/ld+json"> element.
func jsonMarshalSEO(v map[string]string) (string, error) {
	b, err := json.MarshalIndent(v, "  ", "  ")
	if err != nil {
		return "", err
	}
	return "  " + string(b), nil
}

// itoaSEO avoids pulling in strconv just for small non-negative indices/lengths.
func itoaSEO(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// seoPublicRoutes is the explicit allow-list of routes that are safe to
// index (AI.md "Robots Directive": "Default to index,follow only for routes
// explicitly marked public; every other route defaults to noindex,nofollow
// (fail closed)"). Everything not listed here — including paste view/raw/
// download/embed/QR pages, preferences, contact, auth stubs, and all
// /api/*, /server/tor/*, /server/security/report/* pages — stays closed by
// default since it is either per-user, sensitive, or not a canonical
// content page.
var seoPublicRoutes = map[string]bool{
	"/":                    true,
	"/server/about":        true,
	"/server/help":         true,
	"/server/terms":        true,
	"/server/privacy":      true,
	"/server/security":     true,
	"/server/docs/swagger": true,
	"/server/docs/graphql": true,
	"/recent":              true,
	"/pastes":              true,
}

// seoRobotsDirective computes the "index,follow" vs "noindex,nofollow"
// value for the current route (AI.md "Robots Directive"). Fail closed: only
// an explicit allow-list hit returns the indexable directive.
func seoRobotsDirective(path string) string {
	if seoPublicRoutes[path] {
		return "index,follow"
	}
	return "noindex,nofollow"
}

// seoMetaTags renders the always-present SEO/OpenGraph/Twitter tags plus any
// valid, configured site-verification tags (AI.md 24433-24497) as a single
// template.HTML block. All dynamic values are HTML-escaped; verification
// codes/custom attribute names are further constrained by regex before
// being written into an attribute, so no unescaped operator input reaches
// the output.
func (s *Server) seoMetaTags(r *http.Request) template.HTML {
	cfg := s.liveCfg()
	b := cfg.Server.Branding
	seo := cfg.Server.SEO

	title := html.EscapeString(b.EffectiveTitle())
	description := html.EscapeString(b.EffectiveDescription())
	currentURL := html.EscapeString(s.baseURL(r) + r.URL.Path)

	var sb strings.Builder
	sb.WriteString(`<meta name="description" content="`)
	sb.WriteString(description)
	sb.WriteString("\">\n")
	sb.WriteString(`<meta name="robots" content="`)
	sb.WriteString(seoRobotsDirective(r.URL.Path))
	sb.WriteString("\">\n")
	sb.WriteString(`<link rel="canonical" href="`)
	sb.WriteString(currentURL)
	sb.WriteString("\">\n")
	if len(seo.Keywords) > 0 {
		sb.WriteString(`<meta name="keywords" content="`)
		sb.WriteString(html.EscapeString(strings.Join(seo.Keywords, ", ")))
		sb.WriteString("\">\n")
	}
	if a := strings.TrimSpace(seo.Author); a != "" {
		sb.WriteString(`<meta name="author" content="`)
		sb.WriteString(html.EscapeString(a))
		sb.WriteString("\">\n")
	}

	sb.WriteString(`<meta property="og:title" content="`)
	sb.WriteString(title)
	sb.WriteString("\">\n")
	sb.WriteString(`<meta property="og:description" content="`)
	sb.WriteString(description)
	sb.WriteString("\">\n")
	// PART 16 "Image Scaling": always point at the cached/generated
	// 1200x630 branding endpoint (never the raw og_image config value) so
	// a remote-fetch failure falls back to the embedded default instead of
	// a broken/oversized image or a missing tag.
	ogImage := html.EscapeString(s.baseURL(r) + "/static/branding/og-image.png")
	sb.WriteString(`<meta property="og:image" content="`)
	sb.WriteString(ogImage)
	sb.WriteString("\">\n")
	sb.WriteString(`<meta property="og:type" content="website">` + "\n")
	sb.WriteString(`<meta property="og:url" content="`)
	sb.WriteString(currentURL)
	sb.WriteString("\">\n")

	sb.WriteString(`<meta name="twitter:card" content="summary_large_image">` + "\n")
	sb.WriteString(`<meta name="twitter:title" content="`)
	sb.WriteString(title)
	sb.WriteString("\">\n")
	sb.WriteString(`<meta name="twitter:description" content="`)
	sb.WriteString(description)
	sb.WriteString("\">\n")
	sb.WriteString(`<meta name="twitter:image" content="`)
	sb.WriteString(ogImage)
	sb.WriteString("\">\n")
	if h := strings.TrimSpace(seo.TwitterHandle); h != "" {
		sb.WriteString(`<meta name="twitter:site" content="`)
		sb.WriteString(html.EscapeString(h))
		sb.WriteString("\">\n")
	}

	for _, rule := range seoVerifyRules(seo.Verification) {
		v := strings.TrimSpace(rule.value)
		if v == "" || len(v) > rule.maxLen || !rule.pattern.MatchString(v) {
			continue
		}
		if rule.isProp {
			sb.WriteString(`<meta property="`)
		} else {
			sb.WriteString(`<meta name="`)
		}
		sb.WriteString(rule.metaKey)
		sb.WriteString(`" content="`)
		sb.WriteString(html.EscapeString(v))
		sb.WriteString("\">\n")
	}

	for _, c := range seo.Verification.Custom {
		content := strings.TrimSpace(c.Content)
		if content == "" || len(content) > 256 {
			continue
		}
		name := strings.TrimSpace(c.Name)
		prop := strings.TrimSpace(c.Property)
		var attrKind, attrVal string
		switch {
		case name != "" && prop == "":
			attrKind, attrVal = "name", name
		case prop != "" && name == "":
			attrKind, attrVal = "property", prop
		default:
			continue
		}
		if !seoCustomNameRe.MatchString(attrVal) {
			continue
		}
		sb.WriteString(`<meta `)
		sb.WriteString(attrKind)
		sb.WriteString(`="`)
		sb.WriteString(attrVal)
		sb.WriteString(`" content="`)
		sb.WriteString(html.EscapeString(content))
		sb.WriteString("\">\n")
	}

	ld, err := jsonMarshalSEO(map[string]string{
		"@context":    "https://schema.org",
		"@type":       "WebSite",
		"name":        b.EffectiveTitle(),
		"description": b.EffectiveDescription(),
		"url":         s.baseURL(r) + r.URL.Path,
	})
	if err == nil {
		sb.WriteString(`<script type="application/ld+json">`)
		sb.WriteString("\n")
		sb.WriteString(ld)
		sb.WriteString("\n</script>\n")
	}

	return template.HTML(sb.String())
}
