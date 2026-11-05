package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/common/httputil"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/go-chi/chi/v5"
)

// securityClosedGrace is how long a closed report's status page stays reachable
// before the one-shot token expires (AI.md 14161: "expires after the report is
// closed for 30 days").
const securityClosedGrace = 30 * 24 * time.Hour

// pgpPublicKeyPath is the on-disk location of the project's ASCII-armored PGP
// public key, served at /.well-known/pgp-key.asc (AI.md 14156, 14189).
func (s *Server) pgpPublicKeyPath() string {
	return s.configDir + "/security/pgp.pub.asc"
}

// handlePGPKey serves the project's PGP public key (RFC 9116 Encryption target).
// Returns 404 until a keypair has been generated and publishing is enabled
// (AI.md 14156: "404 if no keypair generated yet").
func (s *Server) handlePGPKey(w http.ResponseWriter, r *http.Request) {
	if !s.liveCfg().PublishPGPKeyEnabled() {
		http.NotFound(w, r)
		return
	}
	body, err := os.ReadFile(s.pgpPublicKeyPath())
	if err != nil || len(body) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/pgp-keys; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(body)
}

// hasPGPKey reports whether a servable PGP public key exists.
func (s *Server) hasPGPKey() bool {
	if !s.liveCfg().PublishPGPKeyEnabled() {
		return false
	}
	info, err := os.Stat(s.pgpPublicKeyPath())
	return err == nil && info.Size() > 0
}

// handleSecurityOverview renders the /server/security overview page (AI.md
// 14474). It is the human-readable rendering of everything in
// /.well-known/security.txt: contact channels in RFC 9116 preference order
// (GitHub private vulnerability reporting, the rotating security-report form,
// then the mailto CC address), the Expires date, and the Encryption key link
// when a keypair exists — plus plain-language reporting instructions and links
// to the policy, acknowledgments, PGP key, and machine-readable security.txt.
// Everything is rendered from live config per request via baseURL(r) so the
// URLs always match the security.txt the same client fetches.
func (s *Server) handleSecurityOverview(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	expires := strings.TrimSpace(cfg.Web.Security.Expires)
	if expires == "" {
		expires = time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)
	}
	securityID := s.currentSecurityID()
	reportFormURL := ""
	if securityID != "" {
		reportFormURL = strings.TrimRight(s.baseURL(r), "/") + "/server/contact?security_id=" + securityID
	}
	data := map[string]interface{}{
		"SiteTitle":          cfg.Web.SiteTitle,
		"Theme":              cfg.Web.Theme,
		"BaseURL":            s.baseURL(r),
		"ReportURL":          cfg.SecurityReportURL(),
		"ReportFormURL":      reportFormURL,
		"SecurityEmail":      s.publicSecurityEmail(r),
		"HasPGPKey":          s.hasPGPKey(),
		"Expires":            expires,
		"PreferredLanguages": cfg.SecurityPreferredLanguages(),
	}
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "security_overview.html", data)
		if err != nil {
			fmt.Fprintf(w, "Security: report vulnerabilities via %s\n", cfg.SecurityReportURL())
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "security_overview.html", data)
}

// handleSecurityPolicy renders the coordinated-disclosure policy page (AI.md
// 14157). Default content is provided here; the coordinated-disclosure window
// and security contact are sourced from config.
func (s *Server) handleSecurityPolicy(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	data := map[string]interface{}{
		"SiteTitle":      cfg.Web.SiteTitle,
		"Theme":          cfg.Web.Theme,
		"BaseURL":        s.baseURL(r),
		"DisclosureDays": defaultDisclosureDays,
		"SecurityEmail":  s.publicSecurityEmail(r),
		"HasPGPKey":      s.hasPGPKey(),
	}
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "security_policy.html", data)
		if err != nil {
			fmt.Fprintf(w, "Security policy: coordinated disclosure within %d days.\n", defaultDisclosureDays)
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "security_policy.html", data)
}

// ackEntry is one row on the public acknowledgments page.
type ackEntry struct {
	Year      int
	Display   string
	Severity  string
	Component string
}

// handleSecurityThanks renders the acknowledgments / hall-of-fame page (AI.md
// 14158). Researchers who opted into credit are listed by name or handle;
// anonymous opt-ins become "Anonymous Researcher #n". No-credit reports and
// non-disclosed reports are excluded by the store query.
func (s *Server) handleSecurityThanks(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	reports, err := s.db.ListDisclosedSecurityReports()
	if err != nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Could not load acknowledgments.")
		return
	}
	entries := buildAckEntries(reports)
	data := map[string]interface{}{
		"SiteTitle": cfg.Web.SiteTitle,
		"Theme":     cfg.Web.Theme,
		"BaseURL":   s.baseURL(r),
		"Entries":   entries,
	}
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "security_thanks.html", data)
		if err != nil {
			fmt.Fprintf(w, "Security acknowledgments: %d researcher(s).\n", len(entries))
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "security_thanks.html", data)
}

// buildAckEntries turns disclosed reports into acknowledgment rows, numbering
// anonymous researchers stably in disclosure order (newest first).
func buildAckEntries(reports []*database.SecurityReport) []ackEntry {
	entries := make([]ackEntry, 0, len(reports))
	anon := 0
	for _, rep := range reports {
		display := strings.TrimSpace(rep.CreditName)
		if rep.CreditPreference == "anonymous" || display == "" {
			anon++
			display = fmt.Sprintf("Anonymous Researcher #%d", anon)
		}
		year := 0
		if rep.DisclosedAt != nil {
			year = rep.DisclosedAt.Year()
		}
		entries = append(entries, ackEntry{
			Year:      year,
			Display:   display,
			Severity:  rep.Severity,
			Component: rep.Component,
		})
	}
	return entries
}

// handleSecurityReportStatus renders the researcher's status page for one report
// (AI.md 14161). Access is gated by the one-shot token in the query string: the
// token is compared in constant time, is single-use-per-day, and expires 30 days
// after the report is closed (Disclosed / Won't Fix).
func (s *Server) handleSecurityReportStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	trackingID := strings.TrimSpace(chi.URLParam(r, "tracking_id"))
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if trackingID == "" || token == "" {
		s.renderErrorPage(w, r, http.StatusNotFound, "Report not found.")
		return
	}

	rep, err := s.db.GetSecurityReport(trackingID)
	if err != nil {
		s.renderErrorPage(w, r, http.StatusInternalServerError, "Could not load the report status.")
		return
	}
	// Uniform not-found response whether the id is unknown or the token is wrong,
	// so the endpoint does not confirm the existence of a tracking id.
	if rep == nil {
		s.renderErrorPage(w, r, http.StatusNotFound, "Report not found.")
		return
	}
	sum := sha256.Sum256([]byte(token))
	supplied := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(rep.TokenHash)) != 1 {
		s.securityLog("security.status_token_invalid", "tracking_id", trackingID, "ip", clientIP(r))
		s.auditLog(r, audit.Entry{
			Event:    "security.status_token_invalid",
			Severity: audit.SeverityWarn,
			Result:   audit.ResultFailure,
			Target:   &audit.Target{Type: "security_report", ID: trackingID},
		})
		s.renderErrorPage(w, r, http.StatusNotFound, "Report not found.")
		return
	}

	now := time.Now()

	// Expiry: 30 days after the report is closed. A report is closed once it is
	// Disclosed or marked Won't Fix.
	if closedAt, closed := securityReportClosedAt(rep); closed && now.After(closedAt.Add(securityClosedGrace)) {
		s.renderErrorPage(w, r, http.StatusGone, "This report is closed and its status link has expired.")
		return
	}

	// Single-use-per-day: the token unlocks the page once per calendar day (UTC).
	if rep.TokenLastUsed != nil && sameUTCDate(*rep.TokenLastUsed, now) {
		s.securityLog("security.status_token_reused", "tracking_id", trackingID, "ip", clientIP(r))
		s.auditLog(r, audit.Entry{
			Event:    "security.status_token_reused",
			Severity: audit.SeverityWarn,
			Result:   audit.ResultFailure,
			Target:   &audit.Target{Type: "security_report", ID: trackingID},
		})
		data := s.reportStatusPageData(r, cfg, rep)
		data["RateLimited"] = true
		w.WriteHeader(http.StatusTooManyRequests)
		s.renderTemplate(w, r, "security_report_status.html", data)
		return
	}
	if err := s.db.MarkSecurityReportTokenUsed(trackingID, now); err != nil {
		log.Printf("security status: mark token used failed: %v", err)
	}
	s.securityLog("security.status_viewed", "tracking_id", trackingID, "status", rep.Status)
	s.auditLog(r, audit.Entry{
		Event:    "security.status_viewed",
		Severity: audit.SeverityInfo,
		Target:   &audit.Target{Type: "security_report", ID: trackingID},
		Details:  map[string]any{"status": rep.Status},
	})

	s.renderTemplate(w, r, "security_report_status.html", s.reportStatusPageData(r, cfg, rep))
}

// reportStatusPageData assembles the researcher-facing status view — triage
// state, maintainer comment, and the expected disclosure date. No encrypted
// report content is ever surfaced here.
func (s *Server) reportStatusPageData(r *http.Request, cfg *config.Config, rep *database.SecurityReport) map[string]interface{} {
	expected := rep.CreatedAt.AddDate(0, 0, rep.DisclosureDays)
	data := map[string]interface{}{
		"SiteTitle":     cfg.Web.SiteTitle,
		"Theme":         cfg.Web.Theme,
		"BaseURL":       s.baseURL(r),
		"TrackingID":    rep.TrackingID,
		"Status":        rep.Status,
		"Severity":      rep.Severity,
		"Component":     rep.Component,
		"Comment":       strings.TrimSpace(rep.MaintainerComment),
		"ReceivedAt":    rep.CreatedAt.Local().Format("January 2, 2006"),
		"UpdatedAt":     rep.UpdatedAt.Local().Format("January 2, 2006"),
		"ExpectedDate":  expected.Local().Format("January 2, 2006"),
		"Stages":        securityStatusStages,
		"CurrentStage":  rep.Status,
		"SecurityEmail": s.publicSecurityEmail(r),
	}
	if rep.DisclosedAt != nil {
		data["DisclosedAt"] = rep.DisclosedAt.UTC().Format("2006-01-02")
	}
	return data
}

// securityStatusStages is the ordered triage pipeline shown on the status page
// (AI.md 14161).
var securityStatusStages = []string{
	database.SecStatusReceived,
	database.SecStatusTriaged,
	database.SecStatusConfirmed,
	database.SecStatusPatching,
	database.SecStatusDisclosed,
}

// securityReportClosedAt returns the moment a report was closed and whether it
// is closed at all. Disclosed uses disclosed_at; Won't Fix uses updated_at (the
// state has no dedicated timestamp).
func securityReportClosedAt(rep *database.SecurityReport) (time.Time, bool) {
	switch rep.Status {
	case database.SecStatusDisclosed:
		if rep.DisclosedAt != nil {
			return *rep.DisclosedAt, true
		}
		return rep.UpdatedAt, true
	case database.SecStatusWontFix:
		return rep.UpdatedAt, true
	default:
		return time.Time{}, false
	}
}

// sameUTCDate reports whether two instants fall on the same calendar day in UTC.
func sameUTCDate(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}
