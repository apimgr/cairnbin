package server

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// sensitiveKeyPatterns lists substrings that mark a config key as secret. Any
// map key containing one of these (case-insensitive) is redacted in the
// /debug/config output so tokens, passwords, and keys never leak (PART 6/11).
var sensitiveKeyPatterns = []string{
	"password", "passwd", "secret", "token", "apikey", "api_key",
	"privatekey", "private_key", "encryptionkey", "encryption_key",
	"pass", "credential", "dsn", "webhook_url", "smtp_pass",
}

// redactValue is substituted for any sensitive config value in debug output.
const redactValue = "***REDACTED***"

// isSensitiveKey reports whether a config key name should have its value
// redacted before being exposed via /debug/config.
func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, p := range sensitiveKeyPatterns {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// redactMap walks an arbitrary decoded config structure and replaces the value
// of every sensitive key (see isSensitiveKey) with redactValue. Non-empty
// values are redacted; empty values are left as-is so operators can still see
// which secrets are unset.
func redactMap(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if isSensitiveKey(k) {
				if val != nil && val != "" {
					t[k] = redactValue
				}
				continue
			}
			t[k] = redactMap(val)
		}
		return t
	case []any:
		for i, item := range t {
			t[i] = redactMap(item)
		}
		return t
	default:
		return v
	}
}

// registerDebugRoutes registers the custom debug API endpoints (PART 6). Callers
// must gate this behind mode.ShouldShowDebugEndpoints(); pprof and expvar are
// registered separately in the route table.
func (s *Server) registerDebugRoutes(r chi.Router) {
	r.Get("/debug/config", s.handleDebugConfig)
	r.Get("/debug/routes", s.handleDebugRoutes)
	r.Get("/debug/cache", s.handleDebugCache)
	r.Get("/debug/db", s.handleDebugDB)
	r.Get("/debug/scheduler", s.handleDebugScheduler)
	r.Get("/debug/memory", s.handleDebugMemory)
	r.Get("/debug/goroutines", s.handleDebugGoroutines)
}

// handleDebugConfig returns the live configuration with all secret values
// redacted (PART 6). The config is round-tripped through YAML into a generic
// map so the redaction walk covers every nested field.
func (s *Server) handleDebugConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := yaml.Marshal(s.liveCfg())
	if err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	var generic map[string]any
	if err := yaml.Unmarshal(raw, &generic); err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": redactMap(generic)})
}

// handleDebugRoutes returns every route registered on the router (PART 6).
func (s *Server) handleDebugRoutes(w http.ResponseWriter, r *http.Request) {
	routes := []map[string]string{}
	walkFunc := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, map[string]string{"method": method, "route": route})
		return nil
	}
	if err := chi.Walk(s.router, walkFunc); err != nil {
		writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"count": len(routes), "routes": routes}})
}

// handleDebugCache reports the configured cache driver and a live liveness probe
// (PART 6). The Cache interface exposes no counters, so health is derived from
// Ping.
func (s *Server) handleDebugCache(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	status := "disabled"
	driver := ""
	if s.cacheStore != nil {
		driver = strings.ToLower(strings.TrimSpace(cfg.Server.Cache.Type))
		if driver == "" {
			driver = "memory"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.cacheStore.Ping(ctx); err != nil {
			status = "error"
		} else {
			status = "ok"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{
		"driver": driver,
		"status": status,
		"prefix": cfg.Server.Cache.Prefix,
	}})
}

// handleDebugDB reports database driver info and a liveness probe (PART 6). The
// DB interface exposes no sql.DBStats, so paste count and Ping are surfaced.
func (s *Server) handleDebugDB(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	if err := s.db.Ping(); err != nil {
		status = "error"
	}
	count, err := s.db.CountPastes()
	if err != nil {
		count = -1
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{
		"driver":      s.db.Type(),
		"status":      status,
		"paste_count": count,
	}})
}

// handleDebugScheduler returns the status of every scheduled task (PART 6).
func (s *Server) handleDebugScheduler(w http.ResponseWriter, r *http.Request) {
	if s.schedulerAPI == nil {
		writeAPIError(w, r, http.StatusServiceUnavailable, "MAINTENANCE", "errors.maintenance_mode")
		return
	}
	tasks := s.schedulerAPI.GetTasks()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"count": len(tasks), "tasks": tasks}})
}

// handleDebugMemory returns runtime memory statistics (PART 6).
func (s *Server) handleDebugMemory(w http.ResponseWriter, r *http.Request) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{
		"alloc_mb":       m.Alloc / 1024 / 1024,
		"total_alloc_mb": m.TotalAlloc / 1024 / 1024,
		"sys_mb":         m.Sys / 1024 / 1024,
		"num_gc":         m.NumGC,
		"heap_objects":   m.HeapObjects,
		"goroutines":     runtime.NumGoroutine(),
	}})
}

// handleDebugGoroutines returns a full goroutine stack dump as plain text (PART 6).
func (s *Server) handleDebugGoroutines(w http.ResponseWriter, r *http.Request) {
	buf := make([]byte, 1024*1024)
	n := runtime.Stack(buf, true)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf[:n])
}
