package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/common/domainutil"
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
			writeAPIError(w, r, http.StatusBadRequest, "BAD_REQUEST", "errors.bad_request")
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
	nets    []*net.IPNet
	domains []domainBlockEntry
	skipped int
	sources map[string]blocklistSourceHealth
}

type blocklistSourceHealth struct {
	loadedAt time.Time
	loaded   bool
}

type domainBlockEntry struct {
	source  string
	pattern string
}

// loadBlocklists reads all *.txt files from dir and populates the store.
// Each line is either an IP address or a CIDR range.
// Lines starting with '#' or empty lines are skipped.
// Returns nil when dir does not exist (graceful — blocklist is optional).
func loadBlocklists(dir string) *blocklistStore {
	return loadBlocklistsWithTrustedRanges(dir, nil)
}

func loadBlocklistsWithTrustedRanges(dir string, trustedRanges []string) *blocklistStore {
	return loadBlocklistsWithTypes(dir, trustedRanges, nil)
}

func loadBlocklistsWithTypes(dir string, trustedRanges []string, sourceTypes map[string]string) *blocklistStore {
	bs := &blocklistStore{ips: make(map[string]struct{}), sources: make(map[string]blocklistSourceHealth)}
	excluded := blocklistExcludedRanges(trustedRanges)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory absent is not an error — blocklist feature just inactive.
		return bs
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		sourceID := strings.TrimSuffix(e.Name(), ".txt")
		if sourceTypes != nil && sourceID != "firehol_level1" {
			if _, enabled := sourceTypes[sourceID]; !enabled {
				continue
			}
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			log.Printf("blocklist: open %s: %v", e.Name(), err)
			continue
		}
		_, statErr := e.Info()
		if statErr != nil {
			_ = f.Close()
			continue
		}
		stampPath := filepath.Join(dir, sourceID+".last_updated")
		_, stampErr := os.Stat(stampPath)
		scanner := bufio.NewScanner(f)
		skipped := 0
		validEntries := 0
		sourceType := sourceTypes[sourceID]
		if sourceType == "" {
			sourceType = "ip"
		}
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
				continue
			}
			if comment := strings.IndexAny(line, "; \t"); comment >= 0 {
				line = line[:comment]
			}
			if sourceType == "domain" {
				if entry, ok := domainutil.NormalizeEntry(line); ok {
					bs.domains = append(bs.domains, domainBlockEntry{source: sourceID, pattern: entry})
					validEntries++
				} else {
					skipped++
				}
				continue
			}
			var ip net.IP
			var ipNet *net.IPNet
			if strings.Contains(line, "/") {
				ip, ipNet, err = net.ParseCIDR(line)
			} else {
				ip = net.ParseIP(line)
				if ip != nil {
					bits := 128
					if ip.To4() != nil {
						bits = 32
					}
					mask := net.CIDRMask(bits, bits)
					ipNet = &net.IPNet{IP: ip.Mask(mask), Mask: mask}
				}
			}
			if ip == nil || ipNet == nil || err != nil {
				skipped++
				err = nil
				continue
			}
			blocked := false
			for _, excludedNet := range excluded {
				if blocklistRangesOverlap(ipNet, excludedNet) {
					blocked = true
					break
				}
			}
			if blocked {
				skipped++
				continue
			}
			if strings.Contains(line, "/") {
				bs.nets = append(bs.nets, ipNet)
			} else {
				bs.ips[ip.String()] = struct{}{}
			}
			validEntries++
		}
		scanErr := scanner.Err()
		if scanErr != nil {
			log.Printf("blocklist: scan %s: %v", e.Name(), scanErr)
		}
		_ = f.Close()
		loadedAt := time.Time{}
		if stampErr == nil {
			if stamp, readErr := os.ReadFile(stampPath); readErr == nil {
				loadedAt, _ = time.Parse(time.RFC3339, strings.TrimSpace(string(stamp)))
			}
		}
		bs.sources[sourceID] = blocklistSourceHealth{loadedAt: loadedAt, loaded: scanErr == nil && validEntries > 0 && !loadedAt.IsZero()}
		if skipped > 0 {
			log.Printf("blocklist: %s: skipped %d invalid or trusted/private-overlapping entries", e.Name(), skipped)
			bs.skipped += skipped
		}
	}
	return bs
}

func (bs *blocklistStore) matchingDomainSources(targets []string) []string {
	bs.mu.RLock()
	defer bs.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, target := range targets {
		host, ok := domainutil.NormalizeHost(target)
		if !ok {
			continue
		}
		for _, entry := range bs.domains {
			if domainutil.Matches(entry.pattern, host) {
				seen[entry.source] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	sources := make([]string, 0, len(seen))
	for source := range seen {
		sources = append(sources, source)
	}
	return sources
}

func blocklistExcludedRanges(additional []string) []*net.IPNet {
	ranges := append([]*net.IPNet(nil), privateNets...)
	for _, cidr := range []string{"100.64.0.0/10"} {
		if _, ipNet, err := net.ParseCIDR(cidr); err == nil {
			ranges = append(ranges, ipNet)
		}
	}
	for _, entry := range additional {
		if strings.Contains(entry, "/") {
			if _, ipNet, err := net.ParseCIDR(entry); err == nil {
				ranges = append(ranges, ipNet)
			}
			continue
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			continue
		}
		bits := 128
		if ip.To4() != nil {
			bits = 32
		}
		mask := net.CIDRMask(bits, bits)
		ranges = append(ranges, &net.IPNet{IP: ip.Mask(mask), Mask: mask})
	}
	return ranges
}

func blocklistRangesOverlap(candidate, excluded *net.IPNet) bool {
	if candidate == nil || excluded == nil {
		return false
	}
	candidate4, excluded4 := candidate.IP.To4(), excluded.IP.To4()
	if (candidate4 == nil) != (excluded4 == nil) {
		return false
	}
	return candidate.Contains(excluded.IP) || excluded.Contains(candidate.IP)
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
		bs := s.loadBlocklists(dir)

		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if ip := net.ParseIP(host); ip != nil && bs.contains(ip) {
			writeAPIError(w, r, http.StatusForbidden, "FORBIDDEN", "errors.forbidden")
			return
		}
		if sources := bs.matchingDomainSources(requestDomainTargets(r)); len(sources) > 0 {
			for _, source := range sources {
				log.Printf("blocklist: rejected request by domain source %s", source)
			}
			writeAPIError(w, r, http.StatusForbidden, "FORBIDDEN", "errors.forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestDomainTargets(r *http.Request) []string {
	targets := make([]string, 0, 4)
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin != "" {
		targets = append(targets, origin)
	} else if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		targets = append(targets, referer)
	}
	for key, values := range r.URL.Query() {
		addDomainFormTarget(&targets, key, values)
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return targets
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") && r.Body != nil {
		const maxDomainScanBody = 8 << 20
		body, err := io.ReadAll(io.LimitReader(r.Body, maxDomainScanBody+1))
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		if err == nil && len(body) <= maxDomainScanBody {
			var payload map[string]interface{}
			if json.Unmarshal(body, &payload) == nil {
				collectJSONDomainTargets(&targets, payload)
			}
		}
	} else if err := r.ParseForm(); err == nil {
		for key, values := range r.PostForm {
			addDomainFormTarget(&targets, key, values)
		}
	}
	return targets
}

func addDomainFormTarget(targets *[]string, key string, values []string) {
	name := strings.ToLower(key)
	if !strings.Contains(name, "email") && !strings.Contains(name, "url") && !strings.Contains(name, "uri") && !strings.Contains(name, "link") && !strings.Contains(name, "webhook") && !strings.Contains(name, "endpoint") && !strings.Contains(name, "destination") && !strings.Contains(name, "target") {
		return
	}
	*targets = append(*targets, values...)
}

func collectJSONDomainTargets(targets *[]string, values map[string]interface{}) {
	for key, value := range values {
		switch typed := value.(type) {
		case string:
			addDomainFormTarget(targets, key, []string{typed})
		case map[string]interface{}:
			collectJSONDomainTargets(targets, typed)
		case []interface{}:
			for _, item := range typed {
				if nested, ok := item.(map[string]interface{}); ok {
					collectJSONDomainTargets(targets, nested)
				}
			}
		}
	}
}
