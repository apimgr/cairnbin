package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var feedMarkup = regexp.MustCompile(`<[^>]*>`)

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Self    atomLink    `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}
type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr,omitempty"`
}
type atomEntry struct {
	ID      string   `xml:"id"`
	Title   string   `xml:"title"`
	Link    atomLink `xml:"link"`
	Updated string   `xml:"updated"`
	Summary string   `xml:"summary"`
}
type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}
type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	LastBuildDate string    `xml:"lastBuildDate"`
	Items         []rssItem `xml:"item"`
}
type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
}

func (s *Server) publicFeedItems(r *http.Request) ([]atomEntry, time.Time, string) {
	cfg := s.liveCfg()
	if s.db == nil {
		title := strings.TrimSpace(cfg.Server.SEO.Feed.Title)
		if title == "" {
			title = cfg.Server.Branding.EffectiveTitle()
		}
		return nil, s.feedFallbackTime(), title
	}
	limit := cfg.Server.SEO.Feed.MaxItems
	if limit < 1 || limit > 200 {
		limit = 50
	}
	items, _, err := s.db.GetPublicPastes(1, limit*2)
	if err != nil {
		title := strings.TrimSpace(cfg.Server.SEO.Feed.Title)
		if title == "" {
			title = cfg.Server.Branding.EffectiveTitle()
		}
		return nil, s.feedFallbackTime(), title
	}
	base := strings.TrimRight(s.baseURL(r), "/")
	entries := make([]atomEntry, 0, limit)
	latest := time.Time{}
	for _, item := range items {
		if item.BurnAfter > 0 || (item.ExpiresAt != nil && !item.ExpiresAt.After(time.Now())) {
			continue
		}
		p, err := s.db.GetPasteByID(item.ID)
		if err != nil || p == nil || p.Visibility != 0 || p.BurnAfter > 0 || (p.ExpiresAt != nil && !p.ExpiresAt.After(time.Now())) {
			continue
		}
		permalink := base + "/" + p.ID
		title := feedPlain(p.Title)
		if title == "" {
			title = p.ID
		}
		title = runeLimit(title, 140)
		summary := runeLimit(feedPlain(p.Content), 280)
		updated := p.UpdatedAt
		if updated.IsZero() {
			updated = p.CreatedAt
		}
		if updated.After(latest) {
			latest = updated
		}
		entries = append(entries, atomEntry{ID: permalink, Title: title, Link: atomLink{Href: permalink}, Updated: updated.UTC().Format(time.RFC3339), Summary: summary})
		if len(entries) == limit {
			break
		}
	}
	if latest.IsZero() {
		latest = s.feedFallbackTime()
	}
	title := strings.TrimSpace(cfg.Server.SEO.Feed.Title)
	if title == "" {
		title = cfg.Server.Branding.EffectiveTitle()
	}
	return entries, latest.UTC(), title
}

func (s *Server) feedFallbackTime() time.Time {
	if s.startTime.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return s.startTime.UTC()
}

func feedPlain(value string) string {
	value = html.UnescapeString(feedMarkup.ReplaceAllString(value, " "))
	var b strings.Builder
	space := false
	for _, r := range value {
		if !xmlSafeRune(r) {
			continue
		}
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
func xmlSafeRune(r rune) bool {
	return r == 0x9 || r == 0xA || r == 0xD || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || (r >= 0x10000 && r <= 0x10FFFF)
}
func runeLimit(value string, limit int) string {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func (s *Server) writeFeed(w http.ResponseWriter, r *http.Request, contentType string, body []byte, modified time.Time) {
	if modified.IsZero() {
		modified = time.Now().UTC()
	}
	modified = modified.UTC().Truncate(time.Second)
	etagBytes := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(etagBytes[:]) + `"`
	lastModified := modified.Format(http.TimeFormat)
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", lastModified)
	if r.Header.Get("If-None-Match") == etag || (r.Header.Get("If-None-Match") == "" && modified.Unix() <= parseHTTPDate(r.Header.Get("If-Modified-Since"))) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}
func parseHTTPDate(value string) int64 {
	parsed, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}
func (s *Server) handleAtomFeed(w http.ResponseWriter, r *http.Request) {
	entries, updated, title := s.publicFeedItems(r)
	base := strings.TrimRight(s.baseURL(r), "/")
	feed := atomFeed{ID: base + "/feed.xml", Title: feedPlain(title), Updated: updated.Format(time.RFC3339), Self: atomLink{Rel: "self", Href: base + "/feed.xml", Type: "application/atom+xml"}, Entries: entries}
	body, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	s.writeFeed(w, r, "application/atom+xml", append([]byte(xml.Header), append(body, '\n')...), updated)
}
func (s *Server) handleRSSFeed(w http.ResponseWriter, r *http.Request) {
	entries, updated, title := s.publicFeedItems(r)
	base := strings.TrimRight(s.baseURL(r), "/")
	items := make([]rssItem, 0, len(entries))
	for _, entry := range entries {
		parsed, _ := time.Parse(time.RFC3339, entry.Updated)
		items = append(items, rssItem{Title: entry.Title, Link: entry.Link.Href, GUID: entry.ID, PubDate: parsed.Format(time.RFC822Z), Description: entry.Summary})
	}
	feed := rssFeed{Version: "2.0", Channel: rssChannel{Title: feedPlain(title), Link: base, Description: feedPlain(title), LastBuildDate: updated.Format(time.RFC822Z), Items: items}}
	body, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	s.writeFeed(w, r, "application/rss+xml", append([]byte(xml.Header), append(body, '\n')...), updated)
}
