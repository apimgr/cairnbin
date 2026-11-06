package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/client/paths"
)

type responseCacheConfig struct {
	enabled bool
	ttl     time.Duration
	maxSize int64
}

type cachedResponse struct {
	StatusCode int         `json:"status_code"`
	Header     http.Header `json:"header"`
	Body       []byte      `json:"body"`
	ExpiresAt  time.Time   `json:"expires_at"`
}

func (cfg responseCacheConfig) cachePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(paths.CacheDir(), "responses", hex.EncodeToString(sum[:])+".json")
}

func (cfg responseCacheConfig) get(key string) (*http.Response, bool) {
	if !cfg.enabled || cfg.ttl <= 0 {
		return nil, false
	}
	data, err := os.ReadFile(cfg.cachePath(key))
	if err != nil {
		return nil, false
	}
	var cached cachedResponse
	if json.Unmarshal(data, &cached) != nil || time.Now().After(cached.ExpiresAt) {
		_ = os.Remove(cfg.cachePath(key))
		return nil, false
	}
	return responseFromCache(cached), true
}

func (cfg responseCacheConfig) put(key string, resp *http.Response, body []byte) {
	if !cfg.enabled || cfg.ttl <= 0 || resp.StatusCode != http.StatusOK || cfg.maxSize <= 0 || int64(len(body)) > cfg.maxSize {
		return
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Cache-Control")), "no-store") {
		return
	}
	dir := filepath.Join(paths.CacheDir(), "responses")
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	_ = os.Chmod(dir, 0o700)
	cached := cachedResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       body,
		ExpiresAt:  time.Now().Add(cfg.ttl),
	}
	data, err := json.Marshal(cached)
	if err != nil || int64(len(data)) > cfg.maxSize {
		return
	}
	path := cfg.cachePath(key)
	tmp, err := os.CreateTemp(dir, ".response-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if tmp.Chmod(0o600) != nil {
		_ = tmp.Close()
		return
	}
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return
	}
	if tmp.Close() != nil || os.Rename(tmpPath, path) != nil {
		return
	}
	cfg.enforceSize(dir)
}

func (cfg responseCacheConfig) enforceSize(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type item struct {
		path string
		mod  time.Time
		size int64
	}
	items := make([]item, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, item{path: filepath.Join(dir, entry.Name()), mod: info.ModTime(), size: info.Size()})
		total += info.Size()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, entry := range items {
		if total <= cfg.maxSize {
			break
		}
		if os.Remove(entry.path) == nil {
			total -= entry.size
		}
	}
}

func (cfg responseCacheConfig) invalidate() {
	_ = os.RemoveAll(filepath.Join(paths.CacheDir(), "responses"))
}

func responseFromCache(cached cachedResponse) *http.Response {
	return &http.Response{
		StatusCode: cached.StatusCode,
		Header:     cached.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(cached.Body)),
	}
}
