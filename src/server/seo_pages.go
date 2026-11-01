package server

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// sitemapURL is one <url> entry in the urlset (sitemaps.org schema 0.9).
type sitemapURL struct {
	Loc        string `xml:"loc"`
	Lastmod    string `xml:"lastmod,omitempty"`
	Changefreq string `xml:"changefreq,omitempty"`
	Priority   string `xml:"priority,omitempty"`
}

// sitemapURLSet is the root <urlset> element.
type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

// handleSitemap serves a dynamically generated /sitemap.xml (PART 16). Only
// world-safe public surfaces are listed: the homepage, public /server/* pages,
// and the API-docs UIs. Server-management/authenticated pages and every
// /api/* endpoint are excluded. Every URL is resolved per request via
// baseURL(r) so it matches the Host/proto the client actually used.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.baseURL(r), "/")
	lastmod := time.Now().UTC().Format("2006-01-02")

	add := func(set *sitemapURLSet, path, freq, priority string) {
		set.URLs = append(set.URLs, sitemapURL{
			Loc:        base + path,
			Lastmod:    lastmod,
			Changefreq: freq,
			Priority:   priority,
		})
	}

	set := sitemapURLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	// Homepage — always, 1.0, daily.
	add(&set, "/", "daily", "1.0")
	// Public pages — always, 0.8, weekly.
	for _, p := range []string{
		"/server/about",
		"/server/help",
		"/server/contact",
		"/server/privacy",
		"/server/terms",
		"/server/security",
	} {
		add(&set, p, "weekly", "0.8")
	}
	// API docs UIs — always, 0.7, weekly.
	add(&set, "/server/docs/swagger", "weekly", "0.7")
	add(&set, "/server/docs/graphql", "weekly", "0.7")
	// Project-specific public resource listings — dynamic/public, 0.6, weekly.
	// Kept in sync with seoPublicRoutes (seo.go), which grants these the same
	// "index,follow" robots directive.
	add(&set, "/recent", "weekly", "0.6")
	add(&set, "/pastes", "weekly", "0.6")

	body, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		// Sitemaps are fetched by crawlers, not browsers: send the canonical
		// JSON error envelope instead of an HTML page (PART 14).
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":      false,
			"error":   "SERVER_ERROR",
			"message": "Sitemap generation failed",
		})
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	w.Write(body)
	w.Write([]byte("\n"))
}

// handleLLMs serves /.well-known/llms.txt and its /llms.txt alias (PART 13/14):
// an AI-agent discovery document describing the app, its API base, key public
// endpoints, capabilities, and contacts. Generated per request — every URL is
// resolved via baseURL(r) so it matches the client's Host/proto (and the Tor
// variant when the request arrives over the onion service).
func (s *Server) handleLLMs(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	base := strings.TrimRight(s.baseURL(r), "/")
	apiVersion := s.apiVersion()
	brand := cfg.Server.Branding

	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", brand.EffectiveTitle())
	fmt.Fprintf(&b, "> %s\n\n", brand.EffectiveDescription())

	fmt.Fprintf(&b, "## API\n")
	fmt.Fprintf(&b, "Base URL: %s/api/%s\n", base, apiVersion)
	fmt.Fprintf(&b, "Authentication: Bearer token (server token from server.yml, or resource owner token issued on paste creation)\n")
	fmt.Fprintf(&b, "Rate limit: %d requests/minute\n", cfg.RateLimit.Read.Requests)
	if cfg.Server.MCP.Enabled {
		fmt.Fprintf(&b, "MCP: %s/mcp (Streamable HTTP; same Bearer token as the API)\n", base)
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "## Endpoints\n")
	fmt.Fprintf(&b, "- GET /server/healthz - Health check (no auth)\n")
	fmt.Fprintf(&b, "- GET /server/about - Server information (no auth)\n")
	fmt.Fprintf(&b, "- GET /api/%s/pastes - List public pastes (no auth)\n", apiVersion)
	fmt.Fprintf(&b, "- POST /api/%s/pastes - Create a paste (returns id + owner token)\n", apiVersion)
	fmt.Fprintf(&b, "- GET /api/%s/pastes/{id} - Fetch a paste (no auth)\n", apiVersion)
	fmt.Fprintf(&b, "- DELETE /api/%s/pastes/{id} - Delete a paste (owner token or operator auth)\n", apiVersion)
	if cfg.Server.MCP.Enabled {
		fmt.Fprintf(&b, "- POST /api/%s/server/mcp - Model Context Protocol endpoint (JSON-RPC 2.0; tools mirror the API)\n", apiVersion)
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "## Capabilities\n")
	fmt.Fprintf(&b, "- Create, fetch, list, and delete public pastes\n")
	fmt.Fprintf(&b, "- Syntax language selection per paste\n")
	fmt.Fprintf(&b, "- Drop-in compatible with pastebin.com, microbin, and lenpaste clients\n\n")

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

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}
