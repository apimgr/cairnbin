package server

import (
	"html"
	"html/template"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/microcosm-cc/bluemonday"
)

const operatorContentMaxBytes = 256 * 1024

var forbiddenContactLink = regexp.MustCompile(`(?is)<a\b[^>]*href\s*=\s*["']?/server/contact(?:[/?#"'\s>]|$)[^>]*>(.*?)</a\s*>`)

// operatorHTML returns the final sanitized page fragment from branding.yml.
func (s *Server) operatorHTML(r *http.Request, name string) template.HTML {
	if name != "header" && name != "about" && name != "privacy" && name != "terms" && name != "footer" {
		return ""
	}
	entry, ok := s.liveCfg().Server.Branding.Content[name]
	if !ok {
		return ""
	}
	format := strings.ToLower(strings.TrimSpace(entry.Format))
	if format == "" {
		format = "md"
		if name == "header" || name == "footer" {
			format = "html"
		}
	}
	if name == "header" || name == "footer" {
		if format != "html" {
			log.Printf("[config] invalid server.branding.content.%s.format %q; using built-in content", name, format)
			return ""
		}
	} else if format != "md" && format != "html" && format != "txt" {
		log.Printf("[config] invalid server.branding.content.%s.format %q; using built-in content", name, format)
		return ""
	}
	body, ok := localizedBrandingBody(entry.Body, i18n.LangFromRequest(r))
	if !ok || len(body) > operatorContentMaxBytes {
		log.Printf("[config] invalid or oversized server.branding.content.%s.body; using built-in content", name)
		return ""
	}
	if format == "md" {
		body = string(renderMarkdown(body))
	} else if format == "txt" {
		paragraphs := strings.Split(strings.TrimSpace(body), "\n\n")
		for i := range paragraphs {
			paragraphs[i] = "<p>" + html.EscapeString(strings.TrimSpace(paragraphs[i])) + "</p>"
		}
		body = strings.Join(paragraphs, "\n")
	}
	body = s.expandFooterVars(body)
	if name == "header" || name == "footer" {
		body = config.SanitizeFooterHTML(body)
	} else {
		body = sanitizeOperatorDocument(body)
	}
	body = forbiddenContactLink.ReplaceAllString(body, "$1")
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return template.HTML(body)
}

func localizedBrandingBody(value interface{}, lang string) (string, bool) {
	switch body := value.(type) {
	case string:
		return body, true
	case map[string]interface{}:
		if localized, ok := body[lang].(string); ok {
			return localized, true
		}
		fallback, ok := body["default"].(string)
		return fallback, ok
	case map[interface{}]interface{}:
		for key, raw := range body {
			if key == lang {
				localized, ok := raw.(string)
				return localized, ok
			}
		}
		for key, raw := range body {
			if key == "default" {
				fallback, ok := raw.(string)
				return fallback, ok
			}
		}
	}
	return "", false
}

func sanitizeOperatorDocument(content string) string {
	policy := bluemonday.NewPolicy()
	policy.AllowElements("p", "br", "span", "div", "strong", "b", "em", "i", "u", "s", "small", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "table", "thead", "tbody", "tr", "th", "td", "pre", "code", "blockquote", "hr", "dl", "dt", "dd", "sup", "sub")
	policy.AllowAttrs("href", "title", "target", "rel").OnElements("a")
	policy.RequireNoReferrerOnLinks(true)
	policy.AllowAttrs("class", "id").Globally()
	policy.AllowRelativeURLs(true)
	policy.AllowURLSchemes("http", "https", "mailto")
	return policy.Sanitize(content)
}

func pageContentHTML(markdown string) template.HTML {
	if strings.TrimSpace(markdown) == "" {
		return ""
	}
	content := string(renderMarkdown(markdown))
	content = strings.ReplaceAll(content, "<h1", "<h3")
	content = strings.ReplaceAll(content, "</h1>", "</h3>")
	content = strings.ReplaceAll(content, "<h2", "<h3")
	content = strings.ReplaceAll(content, "</h2>", "</h3>")
	return template.HTML(content)
}

func (s *Server) injectOperatorHTML(r *http.Request, data map[string]interface{}) {
	for name, key := range map[string]string{
		"header": "OperatorHeader", "footer": "FooterCustomHTML", "about": "OperatorAboutHTML",
		"privacy": "OperatorPrivacyHTML", "terms": "OperatorTermsHTML",
	} {
		data[key] = s.operatorHTML(r, name)
	}
}

func brandingContentSource(content config.BrandingContent, lang string) (string, string) {
	format := strings.TrimSpace(content.Format)
	if format == "" {
		format = "md"
	}
	body, ok := localizedBrandingBody(content.Body, lang)
	if !ok {
		return "", format
	}
	if len(body) > operatorContentMaxBytes {
		return "", format
	}
	return body, format
}
