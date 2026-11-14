package server

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// sitemapURL is one <url> entry in the urlset (sitemaps.org schema 0.9).
type sitemapURL struct {
	Loc        string `xml:"loc"`
	Lastmod    string `xml:"lastmod,omitempty"`
	Changefreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
	// Image is the optional Google image-sitemap extension entry.
	Image *sitemapImage `xml:"http://www.google.com/schemas/sitemap-image/1.1 image,omitempty"`
}

// sitemapImage is one image-sitemap <image:image> entry.
type sitemapImage struct {
	Loc string `xml:"http://www.google.com/schemas/sitemap-image/1.1 loc"`
}

// sitemapURLSet is the root <urlset> element.
type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapIndex struct {
	XMLName  xml.Name      `xml:"sitemapindex"`
	Xmlns    string        `xml:"xmlns,attr"`
	Sitemaps []sitemapFile `xml:"sitemap"`
}
type sitemapFile struct {
	Loc     string `xml:"loc"`
	Lastmod string `xml:"lastmod"`
}

// handleSitemap serves a dynamically generated /sitemap.xml (PART 16). Only
// world-safe public surfaces are listed: the homepage, public /server/* pages,
// and the API-docs UIs. Server-management/authenticated pages and every
// /api/* endpoint are excluded. Every URL is resolved per request via
// baseURL(r) so it matches the Host/proto the client actually used.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	sm := s.liveCfg().Server.SEO.Sitemap
	if sm.MaxURLs < 1 || sm.MaxURLs > 50000 {
		sm.MaxURLs = 50000
	}
	page := 0
	if value := chi.URLParam(r, "page"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "errors.not_found")
			return
		}
		page = parsed
	}
	base := strings.TrimRight(s.baseURL(r), "/")
	lastmod := time.Now().UTC().Format("2006-01-02")

	add := func(set *sitemapURLSet, path, freq, priority string) {
		if !robotsAllows(s.liveCfg().Web.Robots.Allow, s.liveCfg().Web.Robots.Deny, path) {
			return
		}
		set.URLs = append(set.URLs, sitemapURL{
			Loc:        base + path,
			Lastmod:    lastmod,
			Changefreq: freq,
			Priority:   priority,
		})
	}

	static := make([]sitemapURL, 0, 11)
	appendStatic := func(path, freq, priority string) {
		set := sitemapURLSet{}
		add(&set, path, freq, priority)
		if len(set.URLs) != 0 {
			static = append(static, set.URLs[0])
		}
	}
	appendStatic("/", "daily", "1.0")
	if sm.IncludeImages && len(static) != 0 {
		static[0].Image = &sitemapImage{Loc: base + "/static/branding/og-image.png"}
	}
	publicPages := []string{"/server/about", "/server/help", "/server/privacy", "/server/terms", "/server/security"}
	if s.liveCfg().Server.Pages.Contact.Enabled {
		publicPages = append(publicPages, "/server/contact")
	}
	for _, p := range publicPages {
		appendStatic(p, "weekly", "0.8")
	}
	for _, p := range []string{"/server/docs/swagger", "/server/docs/graphql"} {
		appendStatic(p, "weekly", "0.7")
	}
	// Project-specific public resource listings share the public-resource score.
	for _, p := range []string{"/recent", "/pastes"} {
		appendStatic(p, "weekly", "0.6")
	}
	// Anonymous public paste pages are the application's dynamic public
	// resources. DB listing excludes unlisted and expired items; the remaining
	// checks exclude burn-after-read content before any URL is emitted.
	var total int
	if s.db != nil {
		_, count, err := s.db.GetPublicPastes(1, 1)
		if err == nil {
			total = count
		}
	}
	totalURLs := len(static) + total
	pageCount := (totalURLs + sm.MaxURLs - 1) / sm.MaxURLs
	if pageCount < 1 {
		pageCount = 1
	}
	if page == 0 && totalURLs > sm.MaxURLs {
		index := sitemapIndex{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}
		for n := 1; n <= pageCount; n++ {
			index.Sitemaps = append(index.Sitemaps, sitemapFile{Loc: base + "/sitemap-" + strconv.Itoa(n) + ".xml", Lastmod: lastmod})
		}
		body, err := xml.MarshalIndent(index, "", "  ")
		if err != nil {
			writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
			return
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = w.Write([]byte(xml.Header))
		_, _ = w.Write(body)
		_, _ = w.Write([]byte("\n"))
		return
	}
	if page > pageCount {
		writeAPIError(w, r, http.StatusNotFound, "NOT_FOUND", "errors.not_found")
		return
	}
	if page == 0 {
		page = 1
	}
	start := (page - 1) * sm.MaxURLs
	end := start + sm.MaxURLs
	set := sitemapURLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	if start < len(static) {
		staticEnd := min(end, len(static))
		set.URLs = append(set.URLs, static[start:staticEnd]...)
	}
	dynamicStart := max(0, start-len(static))
	dynamicEnd := max(0, end-len(static))
	if dynamicEnd > dynamicStart && s.db != nil {
		for offset := dynamicStart; offset < dynamicEnd; {
			lookupPage := offset/sm.MaxURLs + 1
			within := offset % sm.MaxURLs
			items, _, err := s.db.GetPublicPastes(lookupPage, sm.MaxURLs)
			if err != nil || within >= len(items) {
				break
			}
			available := len(items) - within
			needed := dynamicEnd - offset
			if available > needed {
				available = needed
			}
			for _, item := range items[within : within+available] {
				if item.BurnAfter > 0 || (item.ExpiresAt != nil && !item.ExpiresAt.After(time.Now())) {
					continue
				}
				p, err := s.db.GetPasteByID(item.ID)
				if err != nil || p == nil || p.Visibility != 0 || p.BurnAfter > 0 || (p.ExpiresAt != nil && !p.ExpiresAt.After(time.Now())) {
					continue
				}
				add(&set, "/"+p.ID, "weekly", "0.6")
			}
			offset += available
		}
	}
	body, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		// Sitemaps are fetched by crawlers, not browsers: send the canonical
		// JSON error envelope instead of an HTML page (PART 14).
		writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
}

func robotsAllows(allow, deny []string, path string) bool {
	allowed := false
	for _, rule := range allow {
		if robotsRuleMatches(strings.TrimSpace(rule), path) {
			allowed = true
			break
		}
	}
	if !allowed {
		return false
	}
	for _, rule := range deny {
		if robotsRuleMatches(strings.TrimSpace(rule), path) {
			return false
		}
	}
	return true
}

func robotsRuleMatches(rule, path string) bool {
	if rule == "" {
		return false
	}
	if strings.HasSuffix(rule, "$") {
		return strings.TrimSuffix(rule, "$") == path
	}
	if !strings.Contains(rule, "*") {
		return strings.HasPrefix(path, rule)
	}
	parts := strings.Split(rule, "*")
	pos := 0
	for _, part := range parts {
		if part == "" {
			continue
		}
		next := strings.Index(path[pos:], part)
		if next < 0 {
			return false
		}
		pos += next + len(part)
	}
	return true
}

// handleLLMs serves /.well-known/llms.txt and its /llms.txt alias (PART 13/14):
// an AI-agent discovery document describing the app, its API base, key public
// endpoints, capabilities, and contacts. Generated per request — every URL is
// resolved via baseURL(r) so it matches the client's Host/proto (and the Tor
// variant when the request arrives over the onion service).
func (s *Server) handleLLMs(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	if !cfg.Web.LLMs.Enabled {
		s.renderErrorPage(w, r, http.StatusNotFound, "")
		return
	}
	base := strings.TrimRight(s.baseURL(r), "/")
	apiVersion := s.apiVersion()
	brand := cfg.Server.Branding

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", brand.EffectiveTitle())
	fmt.Fprintf(&b, "> %s\n\n", brand.EffectiveDescription())

	fmt.Fprintf(&b, "## API\n")
	fmt.Fprintf(&b, "Base URL: %s/api/%s\n", base, apiVersion)
	fmt.Fprintf(&b, "Authentication: Bearer token (server token from server.yml, or resource owner token issued on paste creation)\n")
	fmt.Fprintf(&b, "Rate limit: %d requests/minute\n", cfg.Server.RateLimit.Read.Requests)
	if cfg.Server.MCP.Enabled {
		fmt.Fprintf(&b, "MCP: %s/mcp (Streamable HTTP; same Bearer token as the API)\n", base)
	}
	fmt.Fprintf(&b, "\n")

	if cfg.Web.LLMs.IncludeEndpoints {
		fmt.Fprintf(&b, "## Endpoints\n")
		fmt.Fprintf(&b, "- GET /server/healthz - Health check (no auth)\n")
		fmt.Fprintf(&b, "- GET /server/about - Server information (no auth)\n")
		fmt.Fprintf(&b, "- GET /api/%s/pastes - List public pastes, or owned pastes with scope=owned and an owner token\n", apiVersion)
		fmt.Fprintf(&b, "- POST /api/%s/pastes - Create a paste (returns id + owner token)\n", apiVersion)
		fmt.Fprintf(&b, "- GET /api/%s/pastes/{id} - Fetch a paste (no auth)\n", apiVersion)
		fmt.Fprintf(&b, "- DELETE /api/%s/pastes/{id} - Delete a paste (owner token or operator auth)\n", apiVersion)
		if cfg.Server.MCP.Enabled {
			fmt.Fprintf(&b, "- POST /api/%s/server/mcp - Model Context Protocol endpoint (JSON-RPC 2.0; tools mirror the API)\n", apiVersion)
		}
		fmt.Fprintf(&b, "\n")
	}

	if cfg.Web.LLMs.IncludeSchemas {
		fmt.Fprintf(&b, "## Schemas\n")
		fmt.Fprintf(&b, "Success envelope: {\"ok\": true, \"data\": {...}}\n")
		fmt.Fprintf(&b, "Error envelope: {\"ok\": false, \"error\": \"CODE\", \"message\": \"...\", \"details\": {}}\n")
		fmt.Fprintf(&b, "Paste create request: {\"title\": string, \"content\": string, \"language\": string, \"visibility\": int, \"burn_after\": int, \"expires_at\": RFC3339 timestamp (optional)}\n")
		fmt.Fprintf(&b, "Paste response: {\"id\": string, \"title\": string, \"language\": string, \"visibility\": int, \"is_link\": bool, \"burn_after\": int, \"expires_at\": RFC3339 timestamp (optional), \"views\": int, \"created_at\": RFC3339 timestamp, \"link\": URL, \"owner_token\": string (create only)}\n")
		fmt.Fprintf(&b, "Paste list item: {\"id\": string, \"title\": string, \"language\": string, \"views\": int, \"expires_at\": RFC3339 timestamp (optional), \"burn_after\": int, \"created_at\": RFC3339 timestamp}\n")
		fmt.Fprintf(&b, "List envelope: {\"ok\": true, \"data\": [...], \"pagination\": {\"page\": int, \"limit\": int, \"total\": int, \"pages\": int}}\n")
		fmt.Fprintf(&b, "Full OpenAPI definition: %s/api/%s/server/swagger\n\n", base, apiVersion)
	}

	fmt.Fprintf(&b, "## Capabilities\n")
	fmt.Fprintf(&b, "- Create, fetch, list, and delete public pastes\n")
	fmt.Fprintf(&b, "- Syntax language selection per paste\n")
	fmt.Fprintf(&b, "- Drop-in compatible with pastebin.com, microbin, lenpaste, stikked, hastebin/haste-server, dpaste, 0x0.st, sprunge.us, ix.io, and termbin/fiche clients\n\n")

	fmt.Fprintf(&b, "## Contact\n")
	fmt.Fprintf(&b, "API issues: %s\n", cfg.SecurityReportURL())
	// Over Tor the clearnet security email is NEVER disclosed (PART 12): use the
	// tor-specific contact if configured, otherwise the report URL only.
	if onion := s.torRequestOnion(r); onion != "" {
		if email := strings.TrimSpace(cfg.Server.Tor.ContactEmail); email != "" {
			fmt.Fprintf(&b, "Security: mailto:%s\n", email)
		} else {
			fmt.Fprintf(&b, "Security: %s\n", cfg.SecurityReportURL())
		}
	} else if email := strings.TrimSpace(cfg.SecurityEmail()); email != "" {
		fmt.Fprintf(&b, "Security: mailto:%s\n", email)
	} else {
		fmt.Fprintf(&b, "Security: %s\n", cfg.SecurityReportURL())
	}

	for _, section := range cfg.Web.LLMs.CustomSections {
		if section = strings.TrimSpace(section); section != "" {
			fmt.Fprintf(&b, "\n%s\n", section)
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
