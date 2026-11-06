package cache_test

// Tests for the cache package — memory driver, noop driver, and New() factory.
// Redis driver methods require an external service and are covered by
// integration tests in tests/docker.sh and tests/incus.sh (PART 28).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/cache"
)

// ── DefaultConfig ─────────────────────────────────────────────────────────────

func TestDefaultConfig(t *testing.T) {
	cfg := cache.DefaultConfig()

	cases := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"Type", cfg.Type, "memory"},
		{"Host", cfg.Host, "localhost"},
		{"Port", cfg.Port, 6379},
		{"Prefix", cfg.Prefix, "cairnbin:"},
		{"TTL", cfg.TTL, time.Hour},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("DefaultConfig().%s = %v, want %v", tc.name, tc.got, tc.want)
			}
		})
	}
}

// ── New factory ───────────────────────────────────────────────────────────────

func TestNew_Drivers(t *testing.T) {
	cases := []struct {
		name    string
		cfg     cache.Config
		wantErr bool
	}{
		{"memory_explicit", cache.Config{Type: "memory"}, false},
		{"memory_default_empty_type", cache.Config{}, false},
		{"none", cache.Config{Type: "none"}, false},
		{"invalid_driver", cache.Config{Type: "invalid"}, true},
		// Redis/valkey with bad host — covers newRedisCache host/port path
		{"redis_no_server", cache.Config{Type: "redis", Host: "localhost", Port: 1}, true},
		{"valkey_no_server", cache.Config{Type: "valkey", Host: "localhost", Port: 1}, true},
		// Invalid URL — covers newRedisCache URL parse-error path
		{"redis_bad_url", cache.Config{Type: "redis", URL: "not-a-valid-redis-url"}, true},
		// Valid URL, server unreachable — covers PoolSize/MinIdle/Timeout option paths
		{
			"redis_url_with_options",
			cache.Config{
				Type: "redis", Host: "localhost", Port: 1,
				PoolSize: 5, MinIdle: 2, Timeout: 100 * time.Millisecond,
			},
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := cache.New(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c == nil {
				t.Error("expected non-nil Cache, got nil")
			}
		})
	}
}

func TestNew_ValkeyURLSchemesParse(t *testing.T) {
	for _, rawURL := range []string{"valkey://127.0.0.1:1", "valkeys://127.0.0.1:1"} {
		t.Run(rawURL, func(t *testing.T) {
			_, err := cache.New(cache.Config{Type: "valkey", URL: rawURL})
			if err == nil {
				t.Fatal("cache.New unexpectedly connected to an unavailable Valkey server")
			}
			if strings.Contains(err.Error(), "parse url") {
				t.Fatalf("Valkey URL scheme was rejected before dialing: %v", err)
			}
		})
	}
}

// ── Memory driver — basic operations ─────────────────────────────────────────

func newMemory(t *testing.T) cache.Cache {
	t.Helper()
	c, err := cache.New(cache.Config{Type: "memory", Prefix: "test:", TTL: time.Hour})
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	return c
}

func TestMemory_SetGet_RoundTrip(t *testing.T) {
	ctx := context.Background()
	c := newMemory(t)
	defer func() { _ = c.Close() }()

	if err := c.Set(ctx, "key1", "hello", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := c.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "hello" {
		t.Errorf("Get returned %q, want %q", got, "hello")
	}
}

func TestMemory_Get_MissingKey(t *testing.T) {
	ctx := context.Background()
	c := newMemory(t)
	defer func() { _ = c.Close() }()

	_, err := c.Get(ctx, "no-such-key")
	if !errors.Is(err, cache.ErrCacheMiss) {
		t.Errorf("expected ErrCacheMiss, got %v", err)
	}
}

func TestMemory_Delete(t *testing.T) {
	ctx := context.Background()
	c := newMemory(t)
	defer func() { _ = c.Close() }()

	if err := c.Set(ctx, "del-key", "value", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Key should be present.
	if _, err := c.Get(ctx, "del-key"); err != nil {
		t.Fatalf("Get before Delete: %v", err)
	}

	if err := c.Delete(ctx, "del-key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Key must be gone.
	_, err := c.Get(ctx, "del-key")
	if !errors.Is(err, cache.ErrCacheMiss) {
		t.Errorf("after Delete: expected ErrCacheMiss, got %v", err)
	}
}

func TestMemory_Delete_NonExistentKey_NoError(t *testing.T) {
	ctx := context.Background()
	c := newMemory(t)
	defer func() { _ = c.Close() }()

	if err := c.Delete(ctx, "ghost-key"); err != nil {
		t.Errorf("Delete of non-existent key returned error: %v", err)
	}
}

func TestMemory_Ping(t *testing.T) {
	ctx := context.Background()
	c := newMemory(t)
	defer func() { _ = c.Close() }()

	if err := c.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

func TestMemory_Close(t *testing.T) {
	c := newMemory(t)
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ── Memory driver — TTL expiry ────────────────────────────────────────────────

func TestMemory_TTL_Expiry(t *testing.T) {
	ctx := context.Background()
	c, err := cache.New(cache.Config{Type: "memory", Prefix: "ttl:", TTL: time.Hour})
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Store with a very short explicit TTL.
	if err := c.Set(ctx, "expiring", "value", 50*time.Millisecond); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Should still be there immediately.
	if _, err := c.Get(ctx, "expiring"); err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}

	// Wait for the entry to expire.
	time.Sleep(120 * time.Millisecond)

	_, err = c.Get(ctx, "expiring")
	if !errors.Is(err, cache.ErrCacheMiss) {
		t.Errorf("after TTL expiry: expected ErrCacheMiss, got %v", err)
	}
}

// ── Memory driver — key prefix isolation ──────────────────────────────────────

// Callers must NOT include the prefix themselves. The driver prepends it
// internally. Two caches with different prefixes must not share keys.
func TestMemory_KeyPrefix_Isolation(t *testing.T) {
	ctx := context.Background()

	c1, err := cache.New(cache.Config{Type: "memory", Prefix: "ns1:", TTL: time.Hour})
	if err != nil {
		t.Fatalf("New c1: %v", err)
	}
	defer func() { _ = c1.Close() }()

	c2, err := cache.New(cache.Config{Type: "memory", Prefix: "ns2:", TTL: time.Hour})
	if err != nil {
		t.Fatalf("New c2: %v", err)
	}
	defer func() { _ = c2.Close() }()

	if err := c1.Set(ctx, "shared", "from-c1", 0); err != nil {
		t.Fatalf("c1.Set: %v", err)
	}

	// c2 must not see c1's key even though the caller-visible key is the same.
	_, err = c2.Get(ctx, "shared")
	if !errors.Is(err, cache.ErrCacheMiss) {
		t.Errorf("c2 should not see c1 key; got err=%v", err)
	}

	// c1 must still return the value.
	got, err := c1.Get(ctx, "shared")
	if err != nil {
		t.Fatalf("c1.Get: %v", err)
	}
	if got != "from-c1" {
		t.Errorf("c1.Get = %q, want %q", got, "from-c1")
	}
}

// ── Memory driver — multiple keys, overwrite ─────────────────────────────────

func TestMemory_MultipleKeys(t *testing.T) {
	ctx := context.Background()
	c := newMemory(t)
	defer func() { _ = c.Close() }()

	keys := []string{"a", "b", "c"}
	for _, k := range keys {
		if err := c.Set(ctx, k, "val-"+k, 0); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	for _, k := range keys {
		got, err := c.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get %q: %v", k, err)
		}
		if got != "val-"+k {
			t.Errorf("Get %q = %q, want %q", k, got, "val-"+k)
		}
	}
}

func TestMemory_Overwrite(t *testing.T) {
	ctx := context.Background()
	c := newMemory(t)
	defer func() { _ = c.Close() }()

	if err := c.Set(ctx, "k", "first", 0); err != nil {
		t.Fatalf("Set first: %v", err)
	}
	if err := c.Set(ctx, "k", "second", 0); err != nil {
		t.Fatalf("Set second: %v", err)
	}

	got, err := c.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "second" {
		t.Errorf("Get = %q, want %q", got, "second")
	}
}

// ── Noop driver ───────────────────────────────────────────────────────────────

func TestNoop_AlwaysMiss(t *testing.T) {
	ctx := context.Background()
	c, err := cache.New(cache.Config{Type: "none"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()

	// Set must succeed silently.
	if err := c.Set(ctx, "k", "v", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Get must always miss (noop doesn't store anything).
	_, err = c.Get(ctx, "k")
	if !errors.Is(err, cache.ErrCacheMiss) {
		t.Errorf("noop Get: expected ErrCacheMiss, got %v", err)
	}
}

func TestNoop_Operations(t *testing.T) {
	ctx := context.Background()
	c, err := cache.New(cache.Config{Type: "none"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Run("Delete", func(t *testing.T) {
		if err := c.Delete(ctx, "any"); err != nil {
			t.Errorf("Delete: %v", err)
		}
	})
	t.Run("Ping", func(t *testing.T) {
		if err := c.Ping(ctx); err != nil {
			t.Errorf("Ping: %v", err)
		}
	})
	t.Run("Close", func(t *testing.T) {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}
