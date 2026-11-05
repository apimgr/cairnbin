// Access-log middleware and the server-side glue for the logging manager
// (AI.md "Logging"). Replaces chi's middleware.Logger: every request is
// recorded to access.log in the configured format, 5xx responses are also
// recorded to error.log, and per-request diagnostics go to debug.log when
// debug mode is active.
package server

import (
	"crypto/tls"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/apimgr/cairnbin/src/logging"
	"github.com/apimgr/cairnbin/src/mode"
)

// SetLogManager registers the logging manager that owns access/server/error/
// app/auth/debug log files. Call from main after resolving directories,
// before Run. A nil manager silently disables file logging.
func (s *Server) SetLogManager(m *logging.Manager) {
	s.logManager = m
}

// tlsVersionName maps a TLS version constant to its display name.
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS1.0"
	case tls.VersionTLS11:
		return "TLS1.1"
	case tls.VersionTLS12:
		return "TLS1.2"
	case tls.VersionTLS13:
		return "TLS1.3"
	}
	return ""
}

// isHealthzPath reports whether p is one of the health-check endpoints
// eligible for access-log suppression (AI.md "Health-Check Log Suppression"):
// /server/healthz, /api/{version}/server/healthz, /api/healthz, and the
// optional root alias /healthz.
func isHealthzPath(p string) bool {
	if p == "/healthz" || p == "/server/healthz" || p == "/api/healthz" {
		return true
	}
	return strings.HasPrefix(p, "/api/") && strings.HasSuffix(p, "/server/healthz")
}

// accessLogMiddleware records every request to access.log via the logging
// manager and mirrors 5xx responses to error.log. It must run after
// realIPMiddleware (RemoteAddr rewritten) and securityHeadersMiddleware
// (X-Request-ID assigned).
//
// Successful (2xx) health-check requests are excluded from access.log by
// default to avoid ~8,640 identical lines/day from a 10s Docker healthcheck;
// failures and all requests under debug mode are always logged (AI.md
// "Health-Check Log Suppression").
func (s *Server) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		bodies := s.newBodyCapture(ww, r)
		next.ServeHTTP(ww, r)
		latency := time.Since(start)
		bodies.log(s, r, ww.Status())

		if ww.Status() >= 200 && ww.Status() < 300 && !mode.IsDebugEnabled() &&
			!s.liveCfg().Server.Logging.Access.LogHealthChecks && isHealthzPath(r.URL.Path) {
			return
		}

		e := logging.AccessEntry{
			RemoteIP:  clientIP(r),
			Time:      start,
			Method:    r.Method,
			Path:      r.URL.Path,
			Query:     r.URL.RawQuery,
			Protocol:  r.Proto,
			Status:    ww.Status(),
			Bytes:     int64(ww.BytesWritten()),
			Referer:   r.Referer(),
			UserAgent: r.UserAgent(),
			Latency:   latency,
			RequestID: w.Header().Get("X-Request-ID"),
			FQDN:      r.Host,
		}
		if r.TLS != nil {
			e.TLSVersion = tlsVersionName(r.TLS.Version)
		}
		// Country/ASN are custom-format variables; look them up only when a
		// GeoIP database is loaded (values stay blank otherwise — fail-open).
		if s.geoipDB != nil {
			if info := s.geoipDB.LookupRequest(r); info != nil {
				e.Country = info.CountryCode
				if info.ASN != 0 {
					e.ASN = strconv.FormatUint(uint64(info.ASN), 10)
				}
			}
		}
		s.logManager.Access(e)

		// All 5xx responses are also errors (AI.md: error.log records all
		// 5xx, panics, and internal errors).
		if e.Status >= 500 {
			s.logManager.Error("http "+strconv.Itoa(e.Status),
				"error_code", errorCodeForStatus(e.Status),
				"http_status", strconv.Itoa(e.Status),
				"method", e.Method,
				"path", e.Path,
				"ip", e.RemoteIP,
				"request_id", e.RequestID)
		} else if e.Status >= 400 {
			// Client errors are logged at WARN with the same context (PART 9
			// "Error Logging"); the internal cause is never part of the response.
			s.logManager.Server("warn", "http "+strconv.Itoa(e.Status),
				"error_code", errorCodeForStatus(e.Status),
				"http_status", strconv.Itoa(e.Status),
				"method", e.Method,
				"path", e.Path,
				"ip", e.RemoteIP,
				"request_id", e.RequestID)
		}
		s.debugLog(r, e.Status, latency, ww.BytesWritten())
	})
}

// authLog records an authentication event to auth.log (AI.md auth.log:
// RFC 3164 lines with stable machine reason codes). tokenID identifies the
// credential class ("operator", "owner") — raw tokens are never logged.
func (s *Server) authLog(r *http.Request, tokenID, result, reason string) {
	ip := ""
	if r != nil {
		ip = clientIP(r)
	}
	s.logManager.Auth(tokenID, ip, result, reason)
}
