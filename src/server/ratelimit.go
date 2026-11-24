package server

import (
	"container/list"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/cache"
	"github.com/apimgr/cairnbin/src/metric"
)

// ipBucket tracks the current and previous fixed-window request counts for an IP.
type ipBucket struct {
	mu          sync.Mutex
	windowStart time.Time
	count       int
	prevCount   int
}

const maxInMemoryRateLimitBuckets = 10000

type rateLimitBucketEntry struct {
	key    string
	bucket *ipBucket
}

// allow reports whether a new request is permitted under the given limit per window.
func (b *ipBucket) allow(limit int, window time.Duration) bool {
	return b.allowAt(limit, window, time.Now())
}

func (b *ipBucket) allowAt(limit int, window time.Duration, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if window <= 0 || limit <= 0 {
		return false
	}
	if b.windowStart.IsZero() {
		b.windowStart = now.Truncate(window)
	}
	windowEnd := b.windowStart.Add(window)
	if !now.Before(windowEnd) {
		if now.Before(windowEnd.Add(window)) {
			b.prevCount = b.count
		} else {
			b.prevCount = 0
		}
		b.count = 0
		b.windowStart = now.Truncate(window)
	}
	elapsed := now.Sub(b.windowStart).Seconds()
	weighted := float64(b.prevCount)*(1-elapsed/window.Seconds()) + float64(b.count)
	if weighted >= float64(limit) {
		return false
	}
	b.count++
	return true
}

func (b *ipBucket) limitedAt(limit int, window time.Duration, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.windowStart.IsZero() || window <= 0 || limit <= 0 {
		return false
	}
	start, count, previous := b.windowStart, b.count, b.prevCount
	if !now.Before(start.Add(window)) {
		if now.Before(start.Add(2 * window)) {
			previous, count = count, 0
		} else {
			previous, count = 0, 0
		}
		start = now.Truncate(window)
	}
	elapsed := now.Sub(start).Seconds()
	weight := 1 - elapsed/window.Seconds()
	if weight < 0 {
		weight = 0
	}
	return float64(previous)*weight+float64(count) >= float64(limit)
}

// rateLimiter is a thread-safe in-memory per-IP rate limiter.
type rateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*ipBucket
	bucketLRU   *list.List
	bucketByKey map[string]*list.Element
	limit       int
	window      time.Duration
	store       rateLimitCounter
}

var rateLimitStoreWarn sync.Once

type rateLimitCounter interface {
	CheckRateLimit(key string, now time.Time, limit int, window time.Duration) (bool, time.Duration, error)
	RateLimitExceeded(key string, now time.Time, limit int, window time.Duration) (bool, error)
}

type cacheRateLimitCounter struct{ cache.Cache }

func (c cacheRateLimitCounter) CheckRateLimit(key string, now time.Time, limit int, window time.Duration) (bool, time.Duration, error) {
	return c.Cache.CheckRateLimit(context.Background(), key, now, limit, window)
}

func (c cacheRateLimitCounter) RateLimitExceeded(key string, now time.Time, limit int, window time.Duration) (bool, error) {
	return c.Cache.RateLimitExceeded(context.Background(), key, now, limit, window)
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		buckets:     make(map[string]*ipBucket),
		bucketLRU:   list.New(),
		bucketByKey: make(map[string]*list.Element),
		limit:       limit,
		window:      window,
	}
	go rl.gc()
	return rl
}

func (rl *rateLimiter) allow(ip string) bool {
	allowed, _ := rl.allowWithRetry(ip)
	return allowed
}

func (rl *rateLimiter) allowWithRetry(ip string) (bool, time.Duration) {
	rl.mu.Lock()
	store, limit, window := rl.store, rl.limit, rl.window
	rl.mu.Unlock()
	if store != nil {
		allowed, retry, err := store.CheckRateLimit(ip, time.Now(), limit, window)
		if err == nil {
			return allowed, retry
		}
		rateLimitStoreWarn.Do(func() {
			log.Printf("[server] WARN persistent rate-limit counter unavailable; using in-process counters: %v", err)
		})
	}
	rl.mu.Lock()
	b := rl.bucketLocked(ip)
	rl.mu.Unlock()

	return b.allow(limit, window), window
}

func (rl *rateLimiter) limited(key string) bool {
	rl.mu.Lock()
	store, limit, window := rl.store, rl.limit, rl.window
	rl.mu.Unlock()
	if store != nil {
		limited, err := store.RateLimitExceeded(key, time.Now(), limit, window)
		if err == nil {
			return limited
		}
		rateLimitStoreWarn.Do(func() {
			log.Printf("[server] WARN persistent rate-limit counter unavailable; using in-process counters: %v", err)
		})
	}
	rl.mu.Lock()
	b := rl.buckets[key]
	if b == nil {
		rl.mu.Unlock()
		return false
	}
	rl.touchBucketLocked(key)
	rl.mu.Unlock()
	return b.limitedAt(limit, window, time.Now())
}

func (rl *rateLimiter) bucketLocked(key string) *ipBucket {
	if bucket := rl.buckets[key]; bucket != nil {
		rl.touchBucketLocked(key)
		return bucket
	}
	if len(rl.buckets) >= maxInMemoryRateLimitBuckets {
		if oldest := rl.bucketLRU.Back(); oldest != nil {
			entry := oldest.Value.(rateLimitBucketEntry)
			delete(rl.buckets, entry.key)
			delete(rl.bucketByKey, entry.key)
			rl.bucketLRU.Remove(oldest)
		}
	}
	bucket := &ipBucket{}
	rl.buckets[key] = bucket
	rl.bucketByKey[key] = rl.bucketLRU.PushFront(rateLimitBucketEntry{key: key, bucket: bucket})
	return bucket
}

func (rl *rateLimiter) touchBucketLocked(key string) {
	if element := rl.bucketByKey[key]; element != nil {
		rl.bucketLRU.MoveToFront(element)
	}
}

// UpdateLimit replaces the per-window request limit without restarting the limiter.
// Existing buckets are not flushed; the new limit takes effect on the next request.
func (rl *rateLimiter) UpdateLimit(newLimit int) {
	rl.mu.Lock()
	rl.limit = newLimit
	rl.mu.Unlock()
}

func (rl *rateLimiter) UpdateWindow(window time.Duration) {
	if window <= 0 {
		return
	}
	rl.mu.Lock()
	rl.window = window
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
			active := (b.count > 0 || b.prevCount > 0) && b.windowStart.Add(rl.window).After(cutoff)
			b.mu.Unlock()
			if !active {
				delete(rl.buckets, ip)
				if element := rl.bucketByKey[ip]; element != nil {
					delete(rl.bucketByKey, ip)
					rl.bucketLRU.Remove(element)
				}
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
	return rateLimitMiddlewareWithKey(rl, endpointClass, func(_ *http.Request, ip, class string) string {
		return "ip:" + ip + ":" + class
	})
}

func rateLimitMiddlewareWithKey(rl *rateLimiter, endpointClass string, keyFor func(*http.Request, string, string) string) func(http.Handler) http.Handler {
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

			key := keyFor(r, ip, endpointClass)
			limitType := "per_endpoint"
			if endpointClass == "global_burst" {
				limitType = "global"
			} else if len(key) >= 3 && key[:3] == "ip:" {
				limitType = "per_ip"
			} else if strings.HasPrefix(key, "key:") || strings.HasPrefix(key, "owner:") {
				limitType = "per_user"
			}
			allowed, retryAfter := rl.allowWithRetry(key)
			if !allowed {
				metric.RateLimitRequestsTotal.WithLabelValues(limitType, "limited").Inc()
				metric.RateLimitBlockedTotal.WithLabelValues(limitType).Inc()
				auditRateLimited(r, ip, endpointClass)

				retrySeconds := int(retryAfter.Seconds())
				if retrySeconds < 1 {
					retrySeconds = 1
				}
				w.Header().Set("Retry-After", fmt.Sprint(retrySeconds))
				// Canonical envelope per PART 14/15 (AI.md:15353). The wait time
				// is conveyed via the Retry-After header only — never as a
				// top-level body field. writeJSON applies the required 2-space
				// indent and single trailing newline (PART 14 JSON Formatting).
				writeAPIError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "errors.rate_limited")
				return
			}
			metric.RateLimitRequestsTotal.WithLabelValues(limitType, "allowed").Inc()
			next.ServeHTTP(w, r)
		})
	}
}
