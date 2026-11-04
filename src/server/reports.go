package server

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apimgr/pastebin/src/audit"
)

const (
	// reportsMaxBody bounds one browser report; real reports are well under 8 KiB.
	reportsMaxBody = 64 << 10
	// reportsPerMinute is the default per-IP ceiling across all report types
	// (`web.reports.rate_limit_per_minute`, PART 11).
	reportsPerMinute = 60
	// reportField caps every logged report field (PART 11 sanitization stage 4).
	reportField = 200
)

// reportLimiter throttles the public reports endpoints per client IP.
var reportLimiter = newRateLimiter(reportsPerMinute, time.Minute)

// reportBurstLimiter caps reports per second per IP (`web.reports.rate_limit_per_ip_burst`);
// nil until configureReportLimits runs.
var reportBurstLimiter *rateLimiter

// configureReportLimits applies web.reports.* to the shared report limiters.
func configureReportLimits(perMinute, burst int) {
	if perMinute <= 0 || burst <= 0 {
		return
	}
	reportLimiter = newRateLimiter(perMinute, time.Minute)
	reportBurstLimiter = newRateLimiter(burst, time.Second)
}

// reportName restricts the {name} path segment to the documented report
// groups; anything else is a 404 so the endpoint never becomes a free-form sink.
var reportNames = map[string]bool{
	"csp": true, "nel": true, "default": true,
	"deprecation": true, "intervention": true, "crash": true,
}

// sanitizeReportURL drops the query string and fragment (which may carry
// tokens), then truncates, per the Output Sanitization Pipeline (PART 11).
func sanitizeReportURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		u.RawQuery = ""
		u.Fragment = ""
		u.User = nil
		raw = u.String()
	} else if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	return sanitizeReportText(raw)
}

// sanitizeReportText strips control characters and truncates.
func sanitizeReportText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > reportField {
		s = s[:reportField]
	}
	return s
}

// cspViolation holds the sanitized fields logged for a CSP violation.
type cspViolation struct {
	blocked   string
	directive string
	document  string
}

// parseCSPReports extracts violations from either the legacy
// `application/csp-report` body ({"csp-report": {...}}) or a Reporting API
// `application/reports+json` array of {"type":"csp-violation","body":{...}}.
func parseCSPReports(body []byte) []cspViolation {
	var legacy struct {
		Report map[string]any `json:"csp-report"`
	}
	if json.Unmarshal(body, &legacy) == nil && legacy.Report != nil {
		return []cspViolation{fromCSPMap(legacy.Report)}
	}
	var batch []struct {
		Type string         `json:"type"`
		URL  string         `json:"url"`
		Body map[string]any `json:"body"`
	}
	var out []cspViolation
	if json.Unmarshal(body, &batch) == nil {
		for _, rep := range batch {
			if rep.Type == "csp-violation" && rep.Body != nil {
				out = append(out, fromCSPMap(rep.Body))
			}
		}
	}
	return out
}

func strField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok {
			return v
		}
	}
	return ""
}

func fromCSPMap(m map[string]any) cspViolation {
	return cspViolation{
		blocked:   sanitizeReportURL(strField(m, "blocked-uri", "blockedURL", "blockedURI")),
		directive: sanitizeReportText(strField(m, "violated-directive", "effectiveDirective", "effective-directive")),
		document:  sanitizeReportURL(strField(m, "document-uri", "documentURL", "documentURI")),
	}
}

// handleReports receives browser-emitted reports (CSP, NEL, deprecation, ...).
// It accepts application/csp-report, application/reports+json and
// application/json, is rate limited per IP, logs sanitized fields, and always
// answers 204 without echoing anything the client sent (PART 11 "Reports
// Endpoint").
func (s *Server) handleReports(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !reportNames[name] {
		s.renderErrorPage(w, r, http.StatusNotFound, "")
		return
	}
	ip := clientIP(r)
	if !reportLimiter.allow(ip) || (reportBurstLimiter != nil && !reportBurstLimiter.allow(ip)) {
		w.Header().Set("Retry-After", "60")
		writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
			"ok": false, "error": "RATE_LIMITED", "message": "Too many requests",
		})
		return
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mt {
	case "application/csp-report", "application/reports+json", "application/json":
	default:
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": "Unsupported content type",
		})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, reportsMaxBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
			"ok": false, "error": "BAD_REQUEST", "message": "Report too large",
		})
		return
	}

	if name == "csp" || name == "default" {
		for _, v := range parseCSPReports(body) {
			s.securityLog("security.csp_violation",
				"ip", ip, "blocked_uri", v.blocked, "violated_directive", v.directive, "document_uri", v.document)
			s.auditLog(r, audit.Entry{
				Event:    "security.csp_violation",
				Severity: audit.SeverityWarn,
				Result:   audit.ResultFailure,
				Target:   &audit.Target{Type: "endpoint", ID: v.document},
				Details: map[string]any{
					"blocked_uri":        v.blocked,
					"violated_directive": v.directive,
				},
			})
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
