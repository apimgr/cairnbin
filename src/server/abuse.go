package server

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apimgr/pastebin/src/audit"
	"github.com/apimgr/pastebin/src/common/email"
)

// floodWindow is the fixed counting window for request-flood detection.
const floodWindow = time.Minute

// maxTrackedIPs bounds the per-IP flood counters so a spoofed-source flood
// cannot grow server memory without limit.
const maxTrackedIPs = 100000

// tempBlock is one automatic, expiring IP block (PART 11 "IP Block Management").
type tempBlock struct {
	reason       string
	blockedAt    time.Time
	expiresAt    time.Time
	offenseCount int
}

type floodCounter struct {
	windowStart time.Time
	count       int
}

// ipBlocks tracks temporary (auto) blocks and per-IP request counts.
type ipBlocks struct {
	mu       sync.Mutex
	blocks   map[string]*tempBlock
	counters map[string]*floodCounter
	// offenses remembers repeat offenders across expiries.
	offenses map[string]int
}

func newIPBlocks() *ipBlocks {
	return &ipBlocks{
		blocks:   map[string]*tempBlock{},
		counters: map[string]*floodCounter{},
		offenses: map[string]int{},
	}
}

// blocked reports whether ip currently has an unexpired temporary block.
func (b *ipBlocks) blocked(ip string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	blk, ok := b.blocks[ip]
	if !ok {
		return false
	}
	if !now.Before(blk.expiresAt) {
		delete(b.blocks, ip)
		return false
	}
	return true
}

// hit counts one request from ip and reports whether the per-window count has
// exceeded threshold.
func (b *ipBlocks) hit(ip string, now time.Time, threshold int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.counters[ip]
	if !ok || now.Sub(c.windowStart) >= floodWindow {
		if !ok && len(b.counters) >= maxTrackedIPs {
			for k, v := range b.counters {
				if now.Sub(v.windowStart) >= floodWindow {
					delete(b.counters, k)
				}
			}
			if len(b.counters) >= maxTrackedIPs {
				return false
			}
		}
		c = &floodCounter{windowStart: now}
		b.counters[ip] = c
	}
	c.count++
	return c.count > threshold
}

// block records a temporary block and returns the new offense count.
func (b *ipBlocks) block(ip, reason string, now time.Time, dur time.Duration) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.offenses[ip]++
	b.blocks[ip] = &tempBlock{reason: reason, blockedAt: now, expiresAt: now.Add(dur), offenseCount: b.offenses[ip]}
	delete(b.counters, ip)
	return b.offenses[ip]
}

// sweep releases expired blocks and stale counters; it returns the released IPs.
// The scheduler calls it every minute (PART 11 auto-release).
func (b *ipBlocks) sweep(now time.Time) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var released []string
	for ip, blk := range b.blocks {
		if !now.Before(blk.expiresAt) {
			delete(b.blocks, ip)
			released = append(released, ip)
		}
	}
	for ip, c := range b.counters {
		if now.Sub(c.windowStart) >= floodWindow {
			delete(b.counters, ip)
		}
	}
	return released
}

// release removes a temporary block (used when an IP is allowlisted).
func (b *ipBlocks) release(ip string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.blocks[ip]
	delete(b.blocks, ip)
	return ok
}

// SweepIPBlocks releases expired temporary IP blocks and audits each release.
// It is registered as a scheduler task that runs every minute.
func (s *Server) SweepIPBlocks() int {
	released := s.ipBlocks.sweep(time.Now())
	for _, ip := range released {
		s.auditLog(nil, audit.Entry{
			Event:    "security.ip_unblocked",
			Severity: audit.SeverityInfo,
			Result:   audit.ResultSuccess,
			Target:   &audit.Target{Type: "ip", ID: ip},
			Reason:   "block expired",
		})
	}
	return len(released)
}

// requestIP extracts the connection's client IP string.
func requestIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// abuseMiddleware enforces permanent (config) and temporary (auto) IP blocks
// and runs request-flood detection. Allowlisted clients bypass it entirely, so
// a trusted IP is never auto-blocked (PART 11 allowlist rules). It runs right
// after the blocklist (execution step 6) and before rate limiting.
func (s *Server) abuseMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAllowlisted(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}
		cfg := s.liveCfg()
		ipStr := requestIP(r)
		ip := net.ParseIP(ipStr)

		if ip != nil && len(cfg.Web.Security.BlockedIPs) > 0 && newAllowlistSet(cfg.Web.Security.BlockedIPs).contains(ip) {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "FORBIDDEN", "message": "forbidden"})
			return
		}

		now := time.Now()
		if s.ipBlocks.blocked(ipStr, now) {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "FORBIDDEN", "message": "forbidden"})
			return
		}

		ad := cfg.Web.Security.AbuseDetection
		limit := cfg.Server.RateLimit.Read.Requests
		if limit < 1 {
			limit = 1
		}
		threshold := limit * ad.RequestFlood.Multiplier
		if cfg.Server.RateLimit.Enabled && ad.AutoBlockIP && s.ipBlocks.hit(ipStr, now, threshold) {
			dur, err := time.ParseDuration(ad.RequestFlood.BlockDuration)
			if err != nil || dur <= 0 {
				dur = time.Hour
			}
			offenses := s.ipBlocks.block(ipStr, "request flood", now, dur)
			s.auditIPBlocked(r, ipStr, "request flood", dur, offenses)
			if ad.AutoAlert {
				go s.alertIPBlocked(ipStr, dur, threshold)
			}
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "FORBIDDEN", "message": "forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auditIPBlocked(r *http.Request, ip, reason string, dur time.Duration, offenses int) {
	s.securityLog("security.ip_blocked", "ip", ip, "reason", reason, "duration", dur.String())
	s.auditLog(r, audit.Entry{
		Event:    "security.ip_blocked",
		Severity: audit.SeverityWarn,
		Result:   audit.ResultSuccess,
		Target:   &audit.Target{Type: "ip", ID: ip},
		Reason:   reason,
		Details:  map[string]any{"duration": dur.String(), "offense_count": offenses, "auto_blocked": true},
	})
	s.auditLog(r, audit.Entry{
		Event:    "security.suspicious_activity",
		Severity: audit.SeverityWarn,
		Result:   audit.ResultFailure,
		Target:   &audit.Target{Type: "ip", ID: ip},
		Reason:   reason,
		Details:  map[string]any{"activity": "request_flood"},
	})
}

// alertIPBlocked emails the operator about an automatic block. It is a no-op
// when SMTP is not configured or no admin address is set ("No SMTP = No emails").
func (s *Server) alertIPBlocked(ip string, dur time.Duration, threshold int) {
	cfg := s.liveCfg()
	m := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, cfg.StartupBaseURL(), cfg.Server.FQDN)
	if !m.Enabled() {
		return
	}
	to := strings.TrimSpace(cfg.AdminEmail())
	if to == "" {
		return
	}
	if err := m.Send(to, "security_alert", map[string]string{
		"event":     "IP automatically blocked: request flood",
		"ip":        ip,
		"details":   fmt.Sprintf("more than %d requests in one minute; blocked for %s", threshold, dur),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		log.Printf("abuse: security alert send failed: %v", err)
	}
}
