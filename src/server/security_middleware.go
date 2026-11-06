package server

import (
	"bufio"
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// ctxKeyType is an unexported type for context keys to avoid collisions.
type ctxKeyType int

const (
	// ctxKeyAllowlisted marks a request's client IP as allowlisted.
	// Downstream middleware (blocklist, rate limit, geoip) checks this flag.
	// Auth middleware ignores it.
	ctxKeyAllowlisted ctxKeyType = iota
)

// isAllowlisted returns true when the request's client IP has been flagged as allowlisted.
func isAllowlisted(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyAllowlisted).(bool)
	return v
}

// ─── Path Security ────────────────────────────────────────────────────────────

// pathSecurityMiddleware normalizes paths and blocks path-traversal attempts.
// PART 4/5 compliance: must run second in the middleware chain (after URL normalize).
func (s *Server) pathSecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		original := r.URL.Path

		rawPath := r.URL.RawPath
		if rawPath == "" {
			rawPath = r.URL.Path
		}

		// Block path traversal attempts (both decoded and percent-encoded).
		// %2e = '.' so %2e%2e = '..'
		if strings.Contains(original, "..") ||
			strings.Contains(rawPath, "..") ||
			strings.Contains(strings.ToLower(rawPath), "%2e") {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "BAD_REQUEST", "message": "bad request"})
			return
		}

		// Normalize by cleaning the path.
		cleaned := path.Clean(original)

		// Ensure leading slash.
		if !strings.HasPrefix(cleaned, "/") {
			cleaned = "/" + cleaned
		}

		// Preserve trailing slash for directory paths (URLNormalize will handle redirect).
		if original != "/" && strings.HasSuffix(original, "/") && !strings.HasSuffix(cleaned, "/") {
			cleaned += "/"
		}

		r.URL.Path = cleaned
		next.ServeHTTP(w, r)
	})
}

// ─── Allowlist ────────────────────────────────────────────────────────────────

// allowlistSet holds parsed CIDRs for O(1) prefix lookup.
type allowlistSet struct {
	mu   sync.RWMutex
	nets []*net.IPNet
}

// newAllowlistSet builds a set from a slice of IP/CIDR strings.
// Single IPs are expanded to /32 (IPv4) or /128 (IPv6).
// Invalid entries are logged and skipped.
func newAllowlistSet(entries []string) *allowlistSet {
	s := &allowlistSet{}
	for _, e := range entries {
		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				log.Printf("allowlist: invalid entry %q — skipped", e)
				continue
			}
			if ip.To4() != nil {
				e = e + "/32"
			} else {
				e = e + "/128"
			}
		}
		_, ipNet, err := net.ParseCIDR(e)
		if err != nil {
			log.Printf("allowlist: invalid CIDR %q — skipped", e)
			continue
		}
		s.nets = append(s.nets, ipNet)
	}
	return s
}

// contains returns true when ip is covered by any CIDR in the set.
func (s *allowlistSet) contains(ip net.IP) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// allowlistMiddleware sets ctxKeyAllowlisted in the request context when the
// client IP matches any entry in server.security.allowlist.
// Downstream middleware (blocklist, rate limit, geoip) checks this flag.
// Auth middleware ignores it.
func (s *Server) allowlistMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.liveCfg()
		al := newAllowlistSet(cfg.Server.Security.AllowlistCIDRs())

		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip != nil && al.contains(ip) {
			ctx := context.WithValue(r.Context(), ctxKeyAllowlisted, true)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Blocklist ────────────────────────────────────────────────────────────────

// blocklistStore holds an in-memory IP/CIDR blocklist loaded from text files.
type blocklistStore struct {
	mu sync.RWMutex
	// exact IP strings for O(1) lookup
	ips map[string]struct{}
	// CIDR ranges
	nets []*net.IPNet
}

// loadBlocklists reads all *.txt files from dir and populates the store.
// Each line is either an IP address or a CIDR range.
// Lines starting with '#' or empty lines are skipped.
// Returns nil when dir does not exist (graceful — blocklist is optional).
func loadBlocklists(dir string) *blocklistStore {
	bs := &blocklistStore{ips: make(map[string]struct{})}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory absent is not an error — blocklist feature just inactive.
		return bs
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("blocklist: open %s: %v", e.Name(), err)
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if strings.Contains(line, "/") {
				_, ipNet, err := net.ParseCIDR(line)
				if err == nil {
					bs.nets = append(bs.nets, ipNet)
				}
			} else if ip := net.ParseIP(line); ip != nil {
				bs.ips[ip.String()] = struct{}{}
			}
		}
		_ = f.Close()
	}
	return bs
}

// contains returns true when ip matches any entry in the blocklist.
func (bs *blocklistStore) contains(ip net.IP) bool {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	if _, ok := bs.ips[ip.String()]; ok {
		return true
	}
	for _, n := range bs.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// blocklistMiddleware rejects requests from IPs in the blocklist files unless
// the IP is flagged as allowlisted (ctxKeyAllowlisted).
// Must run after allowlistMiddleware (PART 5 middleware order).
func (s *Server) blocklistMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Allowlisted clients bypass blocklist enforcement entirely.
		if isAllowlisted(r.Context()) {
			next.ServeHTTP(w, r)
			return
		}

		cfg := s.liveCfg()
		dir := filepath.Join(cfg.Server.DataDir, "security", "blocklists")
		bs := loadBlocklists(dir)

		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip != nil && bs.contains(ip) {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "FORBIDDEN", "message": "forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
