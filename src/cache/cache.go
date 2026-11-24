package cache

import (
	"container/list"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/metric"
	"github.com/redis/go-redis/v9"
)

// Cache is the unified interface for all cache drivers.
// All keys are prefixed with Config.Prefix automatically — callers pass
// un-prefixed keys (e.g., "paste:abc123", not "cairnbin:paste:abc123").
type Cache interface {
	// Get retrieves a value. Returns ErrCacheMiss if the key is absent.
	Get(ctx context.Context, key string) (string, error)
	// Set stores a value. ttl=0 uses the driver's default TTL.
	Set(ctx context.Context, key, value string, ttl time.Duration) error
	// Delete removes a key. No error if the key does not exist.
	Delete(ctx context.Context, key string) error
	// Ping verifies the cache connection is alive.
	Ping(ctx context.Context) error
	CheckRateLimit(ctx context.Context, key string, now time.Time, limit int, window time.Duration) (bool, time.Duration, error)
	RateLimitExceeded(ctx context.Context, key string, now time.Time, limit int, window time.Duration) (bool, error)
	// Close releases underlying resources.
	Close() error
}

// ErrCacheMiss is returned by Get when the key is not present.
var ErrCacheMiss = fmt.Errorf("cache miss")

// Config holds cache driver settings (PART 9/12).
type Config struct {
	// Type selects the driver: "none", "memory" (default), "valkey", "redis".
	Type string `yaml:"type"`
	// URL is the connection string (redis://user:pass@host:port/db).
	// Takes precedence over Host/Port/Username/Password when non-empty.
	URL      string `yaml:"url"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`

	TLS           bool `yaml:"tls"`
	TLSSkipVerify bool `yaml:"tls_skip_verify"`
	PoolSize      int  `yaml:"pool_size"`
	MinIdle       int  `yaml:"min_idle"`
	// Timeout is the read/write/dial timeout for remote drivers.
	Timeout time.Duration `yaml:"timeout"`
	// Prefix is prepended to every key to avoid namespace collisions.
	Prefix string `yaml:"prefix"`
	// TTL is the default time-to-live used when Set is called with ttl=0.
	TTL time.Duration `yaml:"ttl"`
}

// DefaultConfig returns a config safe to use on first run.
func DefaultConfig() Config {
	return Config{
		Type:     "memory",
		Host:     "localhost",
		Port:     6379,
		PoolSize: 10,
		MinIdle:  2,
		Timeout:  5 * time.Second,
		Prefix:   "cairnbin:",
		TTL:      time.Hour,
	}
}

// New creates a Cache from cfg.
func New(cfg Config) (Cache, error) {
	prefix := cfg.Prefix
	defaultTTL := cfg.TTL
	if defaultTTL == 0 {
		defaultTTL = time.Hour
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "", "memory":
		return newMemoryCache(prefix, defaultTTL), nil
	case "none":
		return newNoopCache(), nil
	case "valkey", "redis":
		return newRedisCache(cfg, prefix, defaultTTL)
	default:
		return nil, fmt.Errorf("cache: unknown driver %q (valid: none, memory, valkey, redis)", cfg.Type)
	}
}

// ── Memory driver ─────────────────────────────────────────────────────────────

type memEntry struct {
	value     string
	expiresAt time.Time
}

type memoryCache struct {
	mu         sync.RWMutex
	data       map[string]memEntry
	limits     map[string]memoryRateLimitEntry
	limitLRU   *list.List
	limitByKey map[string]*list.Element
	prefix     string
	defaultTTL time.Duration
}

type memoryRateLimitEntry struct {
	count, previous int
	windowStart     int64
	expiresAt       time.Time
}

const maxMemoryRateLimitEntries = 10000

type memoryRateLimitLRUEntry struct{ key string }

func newMemoryCache(prefix string, defaultTTL time.Duration) *memoryCache {
	c := &memoryCache{
		data:       make(map[string]memEntry),
		limits:     make(map[string]memoryRateLimitEntry),
		limitLRU:   list.New(),
		limitByKey: make(map[string]*list.Element),
		prefix:     prefix,
		defaultTTL: defaultTTL,
	}
	go c.reaper()
	return c
}

func (c *memoryCache) key(k string) string { return c.prefix + k }

func (c *memoryCache) Get(_ context.Context, key string) (string, error) {
	c.mu.RLock()
	e, ok := c.data[c.key(key)]
	c.mu.RUnlock()
	if !ok {
		metric.CacheMissesTotal.WithLabelValues("memory").Inc()
		return "", ErrCacheMiss
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		c.mu.Lock()
		delete(c.data, c.key(key))
		size := len(c.data)
		c.mu.Unlock()
		metric.CacheEvictionsTotal.WithLabelValues("memory").Inc()
		metric.CacheSize.WithLabelValues("memory").Set(float64(size))
		metric.CacheMissesTotal.WithLabelValues("memory").Inc()
		return "", ErrCacheMiss
	}
	metric.CacheHitsTotal.WithLabelValues("memory").Inc()
	return e.value, nil
}

func (c *memoryCache) Set(_ context.Context, key, value string, ttl time.Duration) error {
	if ttl == 0 {
		ttl = c.defaultTTL
	}
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	c.mu.Lock()
	c.data[c.key(key)] = memEntry{value: value, expiresAt: expiresAt}
	size := len(c.data)
	c.mu.Unlock()
	metric.CacheSize.WithLabelValues("memory").Set(float64(size))
	return nil
}

func (c *memoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.data, c.key(key))
	size := len(c.data)
	c.mu.Unlock()
	metric.CacheSize.WithLabelValues("memory").Set(float64(size))
	return nil
}

func (c *memoryCache) Ping(_ context.Context) error { return nil }
func (c *memoryCache) Close() error                 { return nil }

func (c *memoryCache) CheckRateLimit(_ context.Context, key string, now time.Time, limit int, window time.Duration) (bool, time.Duration, error) {
	if limit <= 0 || window < time.Second {
		return false, 0, fmt.Errorf("invalid rate limit parameters")
	}
	windowSeconds, nowUnix := int64(window/time.Second), now.Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	storageKey := c.key(key)
	entry, exists := c.limits[storageKey]
	if !exists {
		entry = c.newRateLimitEntryLocked(storageKey)
	} else {
		c.touchRateLimitEntryLocked(storageKey)
	}
	if entry.windowStart == 0 {
		entry.windowStart = nowUnix
	}
	if nowUnix >= entry.windowStart+windowSeconds {
		if nowUnix < entry.windowStart+2*windowSeconds {
			entry.previous = entry.count
		} else {
			entry.previous = 0
		}
		entry.count = 0
		if nowUnix < entry.windowStart+2*windowSeconds {
			entry.windowStart += windowSeconds
		} else {
			entry.windowStart = nowUnix
		}
	}
	elapsed := nowUnix - entry.windowStart
	weight := 1 - float64(elapsed)/float64(windowSeconds)
	if weight < 0 {
		weight = 0
	}
	allowed := float64(entry.previous)*weight+float64(entry.count) < float64(limit)
	if allowed {
		entry.count++
	}
	entry.expiresAt = now.Add(2 * window)
	c.limits[storageKey] = entry
	if allowed {
		return true, 0, nil
	}
	return false, time.Duration(windowSeconds-elapsed) * time.Second, nil
}

func (c *memoryCache) RateLimitExceeded(_ context.Context, key string, now time.Time, limit int, window time.Duration) (bool, error) {
	if limit <= 0 || window < time.Second {
		return false, fmt.Errorf("invalid rate limit parameters")
	}
	windowSeconds, nowUnix := int64(window/time.Second), now.Unix()
	c.mu.Lock()
	storageKey := c.key(key)
	entry, exists := c.limits[storageKey]
	if exists {
		c.touchRateLimitEntryLocked(storageKey)
	}
	c.mu.Unlock()
	if entry.windowStart == 0 || !entry.expiresAt.After(now) {
		return false, nil
	}
	if nowUnix >= entry.windowStart+2*windowSeconds {
		return false, nil
	}
	if nowUnix >= entry.windowStart+windowSeconds {
		entry.previous, entry.count = entry.count, 0
		entry.windowStart += windowSeconds
	}
	weight := 1 - float64(nowUnix-entry.windowStart)/float64(windowSeconds)
	if weight < 0 {
		weight = 0
	}
	return float64(entry.previous)*weight+float64(entry.count) >= float64(limit), nil
}

func (c *memoryCache) newRateLimitEntryLocked(key string) memoryRateLimitEntry {
	if len(c.limits) >= maxMemoryRateLimitEntries {
		if oldest := c.limitLRU.Back(); oldest != nil {
			entry := oldest.Value.(memoryRateLimitLRUEntry)
			delete(c.limits, entry.key)
			delete(c.limitByKey, entry.key)
			c.limitLRU.Remove(oldest)
		}
	}
	c.limitByKey[key] = c.limitLRU.PushFront(memoryRateLimitLRUEntry{key: key})
	return memoryRateLimitEntry{}
}

func (c *memoryCache) touchRateLimitEntryLocked(key string) {
	if element := c.limitByKey[key]; element != nil {
		c.limitLRU.MoveToFront(element)
	}
}

// reaperTickInterval controls how often the reaper sweeps for expired entries.
// Overridden in tests to avoid waiting 5 minutes.
var reaperTickInterval = 5 * time.Minute

// reaper evicts expired entries on every reaperTickInterval to prevent unbounded growth.
func (c *memoryCache) reaper() {
	t := time.NewTicker(reaperTickInterval)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		c.mu.Lock()
		evicted := 0
		for k, e := range c.data {
			if !e.expiresAt.IsZero() && now.After(e.expiresAt) {
				delete(c.data, k)
				evicted++
			}
		}
		for k, e := range c.limits {
			if !e.expiresAt.After(now) {
				delete(c.limits, k)
				if element := c.limitByKey[k]; element != nil {
					delete(c.limitByKey, k)
					c.limitLRU.Remove(element)
				}
				evicted++
			}
		}
		size := len(c.data)
		c.mu.Unlock()
		if evicted > 0 {
			metric.CacheEvictionsTotal.WithLabelValues("memory").Add(float64(evicted))
		}
		metric.CacheSize.WithLabelValues("memory").Set(float64(size))
	}
}

// ── Noop driver ───────────────────────────────────────────────────────────────

type noopCache struct{}

func newNoopCache() *noopCache { return &noopCache{} }

func (c *noopCache) Get(_ context.Context, _ string) (string, error)           { return "", ErrCacheMiss }
func (c *noopCache) Set(_ context.Context, _, _ string, _ time.Duration) error { return nil }
func (c *noopCache) Delete(_ context.Context, _ string) error                  { return nil }
func (c *noopCache) Ping(_ context.Context) error                              { return nil }
func (c *noopCache) Close() error                                              { return nil }
func (c *noopCache) CheckRateLimit(context.Context, string, time.Time, int, time.Duration) (bool, time.Duration, error) {
	return false, 0, fmt.Errorf("cache unavailable")
}
func (c *noopCache) RateLimitExceeded(context.Context, string, time.Time, int, time.Duration) (bool, error) {
	return false, fmt.Errorf("cache unavailable")
}

// ── Redis / Valkey driver ─────────────────────────────────────────────────────

type redisCache struct {
	client     *redis.Client
	prefix     string
	defaultTTL time.Duration
}

func newRedisCache(cfg Config, prefix string, defaultTTL time.Duration) (*redisCache, error) {
	var opts *redis.Options
	var err error

	if cfg.URL != "" {
		cacheURL := cfg.URL
		if cfg.Type == "valkey" {
			cacheURL = strings.TrimPrefix(cacheURL, "valkey://")
			cacheURL = strings.TrimPrefix(cacheURL, "valkeys://")
			if cacheURL != cfg.URL {
				scheme := "redis://"
				if strings.HasPrefix(cfg.URL, "valkeys://") {
					scheme = "rediss://"
				}
				cacheURL = scheme + cacheURL
			}
		}
		opts, err = redis.ParseURL(cacheURL)
		if err != nil {
			return nil, fmt.Errorf("cache: parse url: %w", err)
		}
	} else {
		port := cfg.Port
		if port == 0 {
			port = 6379
		}
		opts = &redis.Options{
			Addr:     fmt.Sprintf("%s:%d", cfg.Host, port),
			Username: cfg.Username,
			Password: cfg.Password,
			DB:       cfg.DB,
		}
	}

	if cfg.PoolSize > 0 {
		opts.PoolSize = cfg.PoolSize
	}
	if cfg.MinIdle > 0 {
		opts.MinIdleConns = cfg.MinIdle
	}
	if cfg.Timeout > 0 {
		opts.DialTimeout = cfg.Timeout
		opts.ReadTimeout = cfg.Timeout
		opts.WriteTimeout = cfg.Timeout
	}

	client := redis.NewClient(opts)

	// Verify connectivity.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("cache: redis/valkey ping failed: %w", err)
	}

	return &redisCache{client: client, prefix: prefix, defaultTTL: defaultTTL}, nil
}

func (c *redisCache) key(k string) string { return c.prefix + k }

func (c *redisCache) Get(ctx context.Context, key string) (string, error) {
	val, err := c.client.Get(ctx, c.key(key)).Result()
	if err == redis.Nil {
		metric.CacheMissesTotal.WithLabelValues("redis").Inc()
		return "", ErrCacheMiss
	}
	if err != nil {
		return "", fmt.Errorf("cache get %q: %w", key, err)
	}
	metric.CacheHitsTotal.WithLabelValues("redis").Inc()
	return val, nil
}

func (c *redisCache) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if ttl == 0 {
		ttl = c.defaultTTL
	}
	err := c.client.Set(ctx, c.key(key), value, ttl).Err()
	if err != nil {
		return fmt.Errorf("cache set %q: %w", key, err)
	}
	return nil
}

func (c *redisCache) Delete(ctx context.Context, key string) error {
	err := c.client.Del(ctx, c.key(key)).Err()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("cache delete %q: %w", key, err)
	}
	return nil
}

func (c *redisCache) Ping(ctx context.Context) error {
	return c.client.Ping(ctx).Err()
}

var redisRateLimitScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local window = tonumber(ARGV[3])
local count = tonumber(redis.call('HGET', KEYS[1], 'count') or '0')
local previous = tonumber(redis.call('HGET', KEYS[1], 'previous') or '0')
local start = tonumber(redis.call('HGET', KEYS[1], 'start') or now)
if now >= start + window then
  if now < start + 2 * window then previous = count else previous = 0 end
  count = 0
  if now < start + 2 * window then start = start + window else start = now end
end
local elapsed = now - start
local weight = 1 - elapsed / window
if weight < 0 then weight = 0 end
local allowed = previous * weight + count < limit
if allowed then count = count + 1 end
redis.call('HSET', KEYS[1], 'count', count, 'previous', previous, 'start', start)
redis.call('EXPIRE', KEYS[1], window * 2)
if allowed then return {1, 0} end
return {0, window - elapsed}
`)

var redisRateLimitPeekScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local window = tonumber(ARGV[3])
local count = tonumber(redis.call('HGET', KEYS[1], 'count') or '0')
local previous = tonumber(redis.call('HGET', KEYS[1], 'previous') or '0')
local start = tonumber(redis.call('HGET', KEYS[1], 'start') or '0')
if start == 0 or now >= start + 2 * window then return 0 end
if now >= start + window then previous = count; count = 0; start = start + window end
local weight = 1 - (now - start) / window
if weight < 0 then weight = 0 end
if previous * weight + count >= limit then return 1 end
return 0
`)

func (c *redisCache) CheckRateLimit(ctx context.Context, key string, now time.Time, limit int, window time.Duration) (bool, time.Duration, error) {
	seconds := int64(window / time.Second)
	if limit <= 0 || seconds < 1 {
		return false, 0, fmt.Errorf("invalid rate limit parameters")
	}
	result, err := redisRateLimitScript.Run(ctx, c.client, []string{c.key("ratelimit:" + key)}, now.Unix(), limit, seconds).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("cache rate-limit counter: %w", err)
	}
	if len(result) != 2 {
		return false, 0, fmt.Errorf("cache rate-limit counter returned invalid result")
	}
	allowed, err := redisInt(result[0])
	if err != nil {
		return false, 0, err
	}
	retry, err := redisInt(result[1])
	if err != nil {
		return false, 0, err
	}
	return allowed == 1, time.Duration(retry) * time.Second, nil
}

func (c *redisCache) RateLimitExceeded(ctx context.Context, key string, now time.Time, limit int, window time.Duration) (bool, error) {
	seconds := int64(window / time.Second)
	if limit <= 0 || seconds < 1 {
		return false, fmt.Errorf("invalid rate limit parameters")
	}
	value, err := redisRateLimitPeekScript.Run(ctx, c.client, []string{c.key("ratelimit:" + key)}, now.Unix(), limit, seconds).Int()
	return value == 1, err
}

func redisInt(value interface{}) (int64, error) {
	switch number := value.(type) {
	case int64:
		return number, nil
	case string:
		return strconv.ParseInt(number, 10, 64)
	default:
		return 0, fmt.Errorf("unexpected cache integer type %T", value)
	}
}

func (c *redisCache) Close() error { return c.client.Close() }
