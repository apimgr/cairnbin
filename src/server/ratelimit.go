package server

import (
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/metric"
)

// ipBucket tracks request counts for a single IP within a sliding window.
type ipBucket struct {
	mu       sync.Mutex
	requests []time.Time
}

// allow reports whether a new request is permitted under the given limit per window.
func (b *ipBucket) allow(limit int, window time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	cutoff := time.Now().Add(-window)

	// Drop expired entries.
	valid := b.requests[:0]
	for _, t := range b.requests {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	b.requests = valid

	if len(b.requests) >= limit {
		return false
	}
	b.requests = append(b.requests, time.Now())
	return true
}

// rateLimiter is a thread-safe in-memory per-IP rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	limit   int
	window  time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		buckets: make(map[string]*ipBucket),
		limit:   limit,
		window:  window,
	}
	go rl.gc()
	return rl
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	b, ok := rl.buckets[ip]
	if !ok {
		b = &ipBucket{}
		rl.buckets[ip] = b
	}
	limit := rl.limit
	rl.mu.Unlock()

	return b.allow(limit, rl.window)
}

// UpdateLimit replaces the per-window request limit without restarting the limiter.
// Existing buckets are not flushed; the new limit takes effect on the next request.
func (rl *rateLimiter) UpdateLimit(newLimit int) {
	rl.mu.Lock()
	rl.limit = newLimit
	rl.mu.Unlock()
}

// gc periodically removes IP buckets that have no recent requests.
func (rl *rateLimiter) gc() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-rl.window)
		rl.mu.Lock()
		for ip, b := range rl.buckets {
			b.mu.Lock()
			active := false
			for _, t := range b.requests {
				if t.After(cutoff) {
					active = true
					break
				}
			}
			b.mu.Unlock()
			if !active {
				delete(rl.buckets, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// onRateLimited, when set, is called once per client IP per minute when that
// client is rate limited, so the audit log records security.rate_limit_exceeded
// without a flood of identical entries.
var onRateLimited func(r *http.Request, endpointClass string)

var (
	rateLimitAuditMu   sync.Mutex
	rateLimitAuditLast = map[string]time.Time{}
)

// auditRateLimited invokes onRateLimited at most once per IP per minute.
func auditRateLimited(r *http.Request, ip, endpointClass string) {
	if onRateLimited == nil {
		return
	}
	rateLimitAuditMu.Lock()
	last := rateLimitAuditLast[ip]
	now := time.Now()
	if now.Sub(last) < time.Minute {
		rateLimitAuditMu.Unlock()
		return
	}
	rateLimitAuditLast[ip] = now
	if len(rateLimitAuditLast) > 4096 {
		for k, t := range rateLimitAuditLast {
			if now.Sub(t) >= time.Minute {
				delete(rateLimitAuditLast, k)
			}
		}
	}
	rateLimitAuditMu.Unlock()
	onRateLimited(r, endpointClass)
}

// rateLimitMiddleware returns a middleware that enforces the given rate limiter.
// On rejection it returns 429 with a Retry-After header. The ratelimit_* metrics
// (PART 20) use the fixed "per_ip" limit-type label; per-IP detail is never
// used as a metric label (unbounded cardinality). endpointClass identifies the
// caller's limiter class for future use but does not currently label metrics.
func rateLimitMiddleware(rl *rateLimiter, endpointClass string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Allowlisted clients bypass rate limiting (PART 5 middleware order).
			if isAllowlisted(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}

			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}

			if !rl.allow(ip) {
				metric.RateLimitRequestsTotal.WithLabelValues("per_ip", "limited").Inc()
				metric.RateLimitBlockedTotal.WithLabelValues("per_ip").Inc()
				auditRateLimited(r, ip, endpointClass)

				retryAfter := int(rl.window.Seconds())
				w.Header().Set("Retry-After", fmt.Sprint(retryAfter))
				// Canonical envelope per PART 14/15 (AI.md:15353). The wait time
				// is conveyed via the Retry-After header only — never as a
				// top-level body field. writeJSON applies the required 2-space
				// indent and single trailing newline (PART 14 JSON Formatting).
				writeJSON(w, http.StatusTooManyRequests, map[string]interface{}{
					"ok":      false,
					"error":   "RATE_LIMITED",
					"message": "Too many requests",
				})
				return
			}
			metric.RateLimitRequestsTotal.WithLabelValues("per_ip", "allowed").Inc()
			next.ServeHTTP(w, r)
		})
	}
}
