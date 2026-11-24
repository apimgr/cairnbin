package config

import (
	"errors"
	"log"
	"strings"

	"github.com/microcosm-cc/bluemonday"
)

// footerPolicy is the strict sanitization policy for operator footer branding
// (PART 16). It permits only basic text-formatting tags, safe links, and images
// from https and relative URLs; scripts, event handlers, javascript: URLs, forms, and
// the style attribute are stripped.
func footerPolicy() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements("p", "br", "span", "div")
	p.AllowElements("strong", "b", "em", "i", "u", "s", "small")
	p.AllowElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowElements("ul", "ol", "li")
	p.AllowAttrs("href", "title", "target", "rel").OnElements("a")
	p.RequireNoReferrerOnLinks(true)
	p.AllowAttrs("src", "alt", "title", "width", "height").OnElements("img")
	p.AllowURLSchemes("https")
	p.AllowRelativeURLs(true)
	p.AllowAttrs("class", "id").Globally()
	return p
}

// SanitizeFooterHTML sanitizes operator footer branding HTML (PART 16). Empty
// input and a single space (the "disable branding" sentinel) pass through
// unchanged; all other input is stripped down to the safe policy above.
func SanitizeFooterHTML(html string) string {
	if strings.TrimSpace(html) == "" {
		return html
	}
	return footerPolicy().Sanitize(html)
}

// FooterCustomHTML returns the sanitized operator footer branding for rendering.
// The single-space "disable" sentinel resolves to an empty string so no branding
// row is emitted.
func (c *Config) FooterCustomHTML() string {
	raw := c.Web.Footer.CustomHTML
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	return SanitizeFooterHTML(raw)
}

// ValidateFooterHTML sanitizes html and rejects input that sanitized down to
// nothing (PART 16 Custom HTML Validation) — non-empty, non-sentinel input
// that contains only disallowed elements is a configuration error, not
// silently-rendered-blank branding.
func ValidateFooterHTML(html string) (string, error) {
	sanitized := SanitizeFooterHTML(html)

	if strings.TrimSpace(html) != "" && len(sanitized) == 0 {
		return "", errors.New("custom HTML contained only disallowed elements")
	}

	if html != sanitized && strings.TrimSpace(html) != "" {
		log.Printf("[config] WARNING: web.footer.custom_html was sanitized: removed potentially dangerous content")
	}

	return sanitized, nil
}

// LogFooterSanitizationPreview logs the raw vs. sanitized web.footer.custom_html
// at startup (PART 16 Sanitization Preview) so operators can see exactly what
// will render before it reaches a browser: the configured raw input, the
// sanitized output that actually renders, and a warning if the sanitizer
// modified it.
func LogFooterSanitizationPreview(html string) {
	if strings.TrimSpace(html) == "" {
		return
	}
	sanitized := SanitizeFooterHTML(html)
	log.Printf("[config] footer.custom_html raw input: %s", html)
	log.Printf("[config] footer.custom_html sanitized output: %s", sanitized)
	if sanitized != html {
		log.Printf("[config] WARNING: footer.custom_html was modified by the sanitizer")
	}
}
