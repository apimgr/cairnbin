package cache

import (
	"context"
	"fmt"
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
	prefix     string
	defaultTTL time.Duration
}

func newMemoryCache(prefix string, defaultTTL time.Duration) *memoryCache {
	c := &memoryCache{
		data:       make(map[string]memEntry),
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
		client.Close()
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

func (c *redisCache) Close() error { return c.client.Close() }
