// Package metrics registers and exposes Prometheus metrics for the cairnbin service.
// All metrics are prefixed with "cairnbin_" per the project naming convention.
package metric

import (
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/apimgr/cairnbin/src/common/httputil"
)

const ns = "cairnbin"

// Metrics service names (AI.md PART 20). Each has its own bearer token so
// rotating one service's token never breaks the others.
const (
	ServicePrometheus = "prometheus"
	ServiceGrafana    = "grafana"
	ServiceLoki       = "loki"
)

// metricServices is the canonical service order, used for startup logging.
var metricServices = []string{ServicePrometheus, ServiceGrafana, ServiceLoki}

// Default histogram buckets, used when the config supplies no override.
var (
	defaultDurationBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}
	defaultSizeBuckets     = []float64{100, 1000, 10000, 100000, 1000000, 10000000}
)

// Registered metric variables — exported so server/middleware can observe them.
var (
	// Application info
	AppInfo = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "app_info",
			Help:      "Application information; value is always 1, labels carry build info.",
		},
		[]string{"version", "commit", "build_date", "go_version"},
	)
	AppUptime = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "app_uptime_seconds",
		Help:      "Seconds since the application started.",
	})
	AppStartTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "app_start_timestamp",
		Help:      "Unix timestamp when the application started.",
	})

	// HTTP metrics
	HTTPRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests processed.",
		},
		[]string{"method", "path", "status"},
	)
	HTTPActiveRequests = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "http_active_requests",
		Help:      "Number of HTTP requests currently being processed.",
	})

	// Database metrics
	DBQueriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "db_queries_total",
			Help:      "Total number of database queries executed.",
		},
		[]string{"operation", "table"},
	)
	DBQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "db_query_duration_seconds",
			Help:      "Database query latency distribution.",
			Buckets:   []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
		},
		[]string{"operation", "table"},
	)
	DBConnectionsOpen = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "db_connections_open",
		Help:      "Number of open database connections in the pool.",
	})
	DBConnectionsInUse = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "db_connections_in_use",
		Help:      "Number of database connections currently in use.",
	})
	DBErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "db_errors_total",
			Help:      "Total number of database errors.",
		},
		[]string{"operation", "error_type"},
	)

	// Authentication metrics (REQUIRED per PART 20).
	AuthAttemptsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "auth_attempts_total",
			Help:      "Total authentication attempts.",
		},
		[]string{"method", "status"},
	)
	AuthSessionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "auth_sessions_active",
		Help:      "Number of active authenticated sessions.",
	})
	APITokensActive = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "api_tokens_active",
		Help:      "Number of active (non-revoked, non-expired) API tokens.",
	})

	// Cache metrics (PART 20; project uses a cache subsystem).
	CacheHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "cache_hits_total",
			Help:      "Total cache hits.",
		},
		[]string{"cache"},
	)
	CacheMissesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "cache_misses_total",
			Help:      "Total cache misses.",
		},
		[]string{"cache"},
	)
	CacheEvictionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "cache_evictions_total",
			Help:      "Total cache evictions.",
		},
		[]string{"cache"},
	)
	CacheSize = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "cache_size",
			Help:      "Current number of items in the cache.",
		},
		[]string{"cache"},
	)
	CacheBytes = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "cache_bytes",
			Help:      "Estimated bytes used by the cache backend.",
		},
		[]string{"cache"},
	)

	// Scheduler metrics (PART 20; project uses the built-in scheduler, PART 18).
	SchedulerTasksTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "scheduler_tasks_total",
			Help:      "Total scheduled task executions.",
		},
		[]string{"task", "status"},
	)
	SchedulerTaskDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "scheduler_task_duration_seconds",
			Help:      "Scheduled task execution duration distribution.",
			Buckets:   []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300, 600},
		},
		[]string{"task"},
	)
	SchedulerTasksRunning = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "scheduler_tasks_running",
			Help:      "Number of scheduled tasks currently running, by task name.",
		},
		[]string{"task"},
	)
	SchedulerLastRunTimestamp = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "scheduler_last_run_timestamp",
			Help:      "Unix timestamp of the last run for each scheduled task.",
		},
		[]string{"task"},
	)

	// Rate limiting (REQUIRED per PART 20). Labelled by limit/status only;
	// never by ip — per-IP labels are an unbounded-cardinality memory-DoS
	// vector. Per-IP detail belongs in structured logs, not metric labels.
	RateLimitRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "ratelimit_requests_total",
			Help:      "Total rate-limited requests.",
		},
		[]string{"limit", "status"},
	)
	RateLimitBlockedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "ratelimit_blocked_total",
			Help:      "Requests blocked by the rate limiter.",
		},
		[]string{"limit"},
	)

	// Project-specific: paste metrics
	PastesCreatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "pastes_created_total",
		Help:      "Total number of pastes created.",
	})
	PastesDeletedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "pastes_deleted_total",
		Help:      "Total number of pastes deleted.",
	})
	PastesViewedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "pastes_viewed_total",
		Help:      "Total number of paste view events.",
	})

	// Go runtime metrics (registered only when include_runtime is true).
	GoGoroutines = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "go_goroutines",
		Help:      "Current number of goroutines.",
	})
	GoMemAllocBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "go_mem_alloc_bytes",
		Help:      "Bytes of heap memory currently allocated and in use.",
	})
	GoMemSysBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "go_mem_sys_bytes",
		Help:      "Total bytes obtained from the OS.",
	})
	GoGCRunsTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "go_gc_runs_total",
		Help:      "Total number of completed GC cycles.",
	})
	GoGCPauseTotalSeconds = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "go_gc_pause_total_seconds",
		Help:      "Cumulative seconds spent in GC stop-the-world pauses.",
	})

	// System metrics (registered only when include_system is true).
	SystemCPUUsagePct = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "system_cpu_usage_percent",
		Help:      "System CPU usage percentage (0-100).",
	})
	SystemMemUsagePct = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "system_memory_usage_percent",
		Help:      "System memory usage percentage (0-100).",
	})
	SystemMemUsedBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "system_memory_used_bytes",
		Help:      "System memory in use, in bytes.",
	})
	SystemMemTotalBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "system_memory_total_bytes",
		Help:      "Total system memory, in bytes.",
	})
	SystemDiskUsagePct = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "system_disk_usage_percent",
		Help:      "Disk usage percentage (0-100) for the data path.",
	}, []string{"path"})
	SystemDiskUsedBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "system_disk_used_bytes",
		Help:      "Disk space used, in bytes, for the data path.",
	}, []string{"path"})
	SystemDiskTotalBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "system_disk_total_bytes",
		Help:      "Total disk space, in bytes, for the data path.",
	}, []string{"path"})

	// Tor metrics (always registered; project ships Tor per PART 31).
	TorEnabled = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "tor_enabled",
		Help:      "1 when the Tor hidden service is enabled, else 0.",
	})
	TorRunning = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "tor_running",
		Help:      "1 when the Tor hidden service is running, else 0.",
	})
	TorCircuitEstablished = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: ns,
		Name:      "tor_circuit_established",
		Help:      "1 when a Tor circuit is established, else 0.",
	})
	TorRequestsTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: ns,
		Name:      "tor_requests_total",
		Help:      "Total requests received over the Tor hidden service (.onion Host).",
	})
)

// Collector bundles scrape-time collection dependencies.
type Collector struct {
	startTime time.Time
	// serviceTokens maps a metrics service name ("prometheus", "grafana",
	// "loki") to its bearer token. An empty token disables that service: its
	// endpoints answer 403 with an empty body. PART 20 forbids an
	// unauthenticated default, so every service requires a token unless
	// allowUnauthenticated is explicitly set.
	serviceTokens map[string]string
	// allowUnauthenticated is the firewalled-internal-networks escape hatch
	// that skips token checks for every metrics service.
	allowUnauthenticated bool
	// tracks accumulated GC pause nanoseconds between scrapes
	lastPauseTotalNs uint64

	includeSystem  bool
	includeRuntime bool
	dataDir        string

	// lokiMaxEntries and lokiMaxAge bound what the loki service returns.
	lokiMaxEntries int
	lokiMaxAge     time.Duration
	// logProvider supplies recent structured log lines for the loki service.
	// Nil means the loki service serves an empty stream rather than failing.
	logProvider func() []LokiEntry

	// Per-collector HTTP histograms, built from configured buckets.
	httpReqDuration *prometheus.HistogramVec
	httpReqSize     *prometheus.HistogramVec
	httpRespSize    *prometheus.HistogramVec

	// CPU sampling state for delta-based usage percentage (system metrics).
	lastCPUTotal uint64
	lastCPUIdle  uint64

	torMu    sync.Mutex
	torState func() (enabled, running bool)

	// collectMu guards the delta-sampling state (lastPauseTotalNs, lastCPUTotal,
	// lastCPUIdle) and serializes concurrent /metrics scrapes, which run in
	// separate request goroutines.
	collectMu sync.Mutex
}

// Options configures a Collector. Buckets default to the package defaults
// when empty; IncludeSystem/IncludeRuntime gate optional metric families.
type Options struct {
	Version string
	Commit  string
	// Token is the prometheus-service bearer token. Retained as the primary
	// field so callers that only expose a single token keep working; it is
	// folded into ServiceTokens during construction.
	Token     string
	BuildDate string
	// ServiceTokens holds the per-service bearer tokens for the metrics
	// routes. Rotating one service's token never breaks the others.
	ServiceTokens map[string]string
	// AllowUnauthenticated skips token checks for every metrics service. It is
	// an escape hatch for firewalled internal networks only.
	AllowUnauthenticated bool
	StartTime            time.Time
	IncludeSystem        bool
	IncludeRuntime       bool
	DurationBuckets      []float64
	SizeBuckets          []float64
	DataDir              string
	// LokiMaxEntries and LokiMaxAge bound what the loki service returns
	// (AI.md PART 20). MaxAge is a Go duration string; an unparsable value
	// falls back to one hour and an entry count of zero falls back to 1000.
	LokiMaxEntries int
	LokiMaxAge     string
	// LogProvider supplies recent structured log lines for the loki service.
	// It returns entries already ordered oldest-first and already sanitized.
	// Nil means the loki service serves an empty stream rather than failing.
	LogProvider func() []LokiEntry
}

// LokiEntry is one log line in the shape the loki service serves (AI.md
// PART 20): a label set plus a timestamped line.
type LokiEntry struct {
	Labels map[string]string
	Time   time.Time
	Line   string
}

// New creates a Collector with the default metric set (runtime metrics on,
// system metrics off, no Tor provider). Retained for callers and tests that
// only need the core collector.
func New(version, commit, buildDate string, startTime time.Time, token string) *Collector {
	return NewWithOptions(Options{
		Version:        version,
		Commit:         commit,
		BuildDate:      buildDate,
		Token:          token,
		StartTime:      startTime,
		IncludeRuntime: true,
	})
}

// NewWithOptions creates a Collector, seeds app_info, builds the HTTP
// histograms from the configured buckets, and conditionally registers the
// optional runtime and system metric families.
func NewWithOptions(o Options) *Collector {
	AppInfo.WithLabelValues(o.Version, o.Commit, o.BuildDate, runtime.Version()).Set(1)
	AppStartTimestamp.Set(float64(o.StartTime.Unix()))

	dur := o.DurationBuckets
	if len(dur) == 0 {
		dur = defaultDurationBuckets
	}
	sz := o.SizeBuckets
	if len(sz) == 0 {
		sz = defaultSizeBuckets
	}

	// Per-service tokens win over the flat Token field, which is kept for
	// configs that predate auth.tokens. Build a fresh map so a caller's map
	// is never aliased into the collector.
	tokens := make(map[string]string, len(metricServices)+1)
	for _, svc := range metricServices {
		if t := o.ServiceTokens[svc]; t != "" {
			tokens[svc] = t
		}
	}
	if _, ok := tokens[ServicePrometheus]; !ok && o.Token != "" {
		tokens[ServicePrometheus] = o.Token
	}

	// Bound the loki service. A bad max_age warns and falls back rather than
	// failing startup; the same lenient posture config validation takes.
	lokiMaxEntries := o.LokiMaxEntries
	if lokiMaxEntries <= 0 {
		lokiMaxEntries = 1000
	}
	lokiMaxAge := time.Hour
	if o.LokiMaxAge != "" {
		d, err := time.ParseDuration(o.LokiMaxAge)
		if err != nil || d <= 0 {
			log.Printf("metric: loki.max_age %q invalid, using 1h", o.LokiMaxAge)
		} else {
			lokiMaxAge = d
		}
	}

	c := &Collector{
		startTime:            o.StartTime,
		serviceTokens:        tokens,
		allowUnauthenticated: o.AllowUnauthenticated,
		includeSystem:        o.IncludeSystem && systemStatsSupported(),
		includeRuntime:       o.IncludeRuntime,
		dataDir:              o.DataDir,
		lokiMaxEntries:       lokiMaxEntries,
		lokiMaxAge:           lokiMaxAge,
		logProvider:          o.LogProvider,
	}

	c.httpReqDuration = registerHistogramVec(prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "http_request_duration_seconds",
			Help:      "HTTP request latency distribution.",
			Buckets:   dur,
		},
		[]string{"method", "path"},
	))
	c.httpReqSize = registerHistogramVec(prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "http_request_size_bytes",
			Help:      "HTTP request body size distribution.",
			Buckets:   sz,
		},
		[]string{"method", "path"},
	))
	c.httpRespSize = registerHistogramVec(prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "http_response_size_bytes",
			Help:      "HTTP response body size distribution.",
			Buckets:   sz,
		},
		[]string{"method", "path"},
	))

	if c.includeRuntime {
		register(GoGoroutines, GoMemAllocBytes, GoMemSysBytes, GoGCRunsTotal, GoGCPauseTotalSeconds)
	}
	if c.includeSystem {
		register(SystemCPUUsagePct, SystemMemUsagePct, SystemMemUsedBytes, SystemMemTotalBytes,
			SystemDiskUsagePct, SystemDiskUsedBytes, SystemDiskTotalBytes)
	}

	return c
}

// SetTorProvider wires a callback that reports Tor hidden-service state at
// scrape time. Set after the Tor manager is constructed.
func (c *Collector) SetTorProvider(fn func() (enabled, running bool)) {
	c.torMu.Lock()
	c.torState = fn
	c.torMu.Unlock()
}

// register registers collectors, ignoring AlreadyRegisteredError so that
// multiple Collector instances (e.g. across tests) do not panic.
func register(cs ...prometheus.Collector) {
	for _, col := range cs {
		if err := prometheus.Register(col); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				panic(err)
			}
		}
	}
}

// registerHistogramVec registers h and returns it, or returns the already
// registered collector when one exists (so observations reach the scraped
// instance rather than a dangling duplicate).
func registerHistogramVec(h *prometheus.HistogramVec) *prometheus.HistogramVec {
	if err := prometheus.Register(h); err != nil {
		if are, ok := err.(prometheus.AlreadyRegisteredError); ok {
			return are.ExistingCollector.(*prometheus.HistogramVec)
		}
		panic(err)
	}
	return h
}

// setBool sets a gauge to 1 when b is true, else 0.
func setBool(g prometheus.Gauge, b bool) {
	if b {
		g.Set(1)
		return
	}
	g.Set(0)
}

// DisabledServices returns the metrics services that have no configured token
// and are therefore disabled (their endpoints answer 403). The names are
// returned, never the tokens, so callers can log the reason at startup without
// leaking a secret.
func (c *Collector) DisabledServices() []string {
	if c.allowUnauthenticated {
		return nil
	}
	var disabled []string
	for _, svc := range metricServices {
		if c.serviceTokens[svc] == "" {
			disabled = append(disabled, svc)
		}
	}
	return disabled
}

// authzResult reports whether a metrics request may proceed.
type authzResult int

const (
	// authzOK means the request may be served.
	authzOK authzResult = iota
	// authzUnauthorized means the bearer token was absent or wrong (401).
	authzUnauthorized
	// authzDisabled means the service has no token configured (403).
	authzDisabled
)

// authorize applies the PART 20 token rules for a single metrics service.
//
// The escape hatch (allowUnauthenticated) skips checks entirely. Otherwise a
// service with an empty token is disabled → 403 with an empty body, and a
// non-empty token must match the Authorization header exactly. Query-string
// tokens are deliberately not consulted: they leak into access logs and
// intermediate proxies.
func (c *Collector) authorize(service string, r *http.Request) authzResult {
	if c.allowUnauthenticated {
		return authzOK
	}
	want := c.serviceTokens[service]
	if want == "" {
		return authzDisabled
	}
	auth := r.Header.Get("Authorization")
	// Constant-time comparison to prevent token timing attacks.
	if subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+want)) != 1 {
		return authzUnauthorized
	}
	return authzOK
}

// Handler returns an http.Handler that serves Prometheus metrics behind the
// prometheus service's bearer token. ServiceHandler is the per-service entry
// point used by the /server/metrics/{service} routes.
func (c *Collector) Handler() http.Handler {
	return c.ServiceHandler(ServicePrometheus)
}

// ServiceHandler returns an http.Handler serving the given metrics service,
// enforcing that service's bearer token.
func (c *Collector) ServiceHandler(service string) http.Handler {
	inner := promhttp.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch c.authorize(service, r) {
		case authzDisabled:
			// 403 with an empty body: the service is administratively off.
			// Deliberately no body so a disabled service leaks nothing.
			w.WriteHeader(http.StatusForbidden)
			return
		case authzUnauthorized:
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			// Prometheus scrapers are machine clients, so the rejection uses
			// the canonical PART 14 JSON envelope rather than bare text.
			httputil.WriteJSON(w, http.StatusUnauthorized, map[string]any{
				"ok":      false,
				"error":   "UNAUTHORIZED",
				"message": "Unauthorized",
			})
			return
		}

		// PART 20: only the prometheus service speaks Prometheus text
		// exposition. grafana serves a dashboard definition and loki serves a
		// stream; both are JSON, so they must not reach the promhttp handler.
		switch service {
		case ServiceGrafana:
			c.serveGrafana(w, r)
			return
		case ServiceLoki:
			c.serveLoki(w, r)
			return
		}

		// Collect runtime metrics just before scrape.
		c.collectRuntime()

		inner.ServeHTTP(w, r)
	})
}

// serveGrafana writes the importable dashboard definition as JSON. The
// datasource stays a template variable so the payload imports against whatever
// Prometheus datasource the operator already has.
func (c *Collector) serveGrafana(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(Dashboard()); err != nil {
		log.Printf("metric: encode grafana dashboard: %v", err)
	}
}

// serveLoki writes recent structured log entries in the Loki push-API stream
// format: {"streams":[{"stream":{labels},"values":[["<ns-ts>","<line>"]]}]}.
// Entries are bounded by loki.max_entries and loki.max_age and arrive already
// sanitized from the provider.
func (c *Collector) serveLoki(w http.ResponseWriter, r *http.Request) {
	// An absent provider is not an error: it just means no log source is wired
	// yet, so the stream is empty rather than the scrape failing.
	var entries []LokiEntry
	if c.logProvider != nil {
		entries = c.logProvider()
	}

	cutoff := time.Now().Add(-c.lokiMaxAge)
	kept := entries[:0:0]
	for _, e := range entries {
		if !e.Time.IsZero() && e.Time.Before(cutoff) {
			continue
		}
		kept = append(kept, e)
	}
	// Keep the newest max_entries; the provider returns oldest-first.
	if len(kept) > c.lokiMaxEntries {
		kept = kept[len(kept)-c.lokiMaxEntries:]
	}

	// Group by label set so Loki sees one entry per stream. Label keys are
	// sorted by encoding/json for map[string]string, so grouping is stable.
	type stream struct {
		labels map[string]string
		values []any
	}
	var order []string
	streams := make(map[string]*stream)
	for _, e := range kept {
		key := labelsKey(e.Labels)
		st, ok := streams[key]
		if !ok {
			st = &stream{labels: e.Labels}
			streams[key] = st
			order = append(order, key)
		}
		ts := e.Time
		if ts.IsZero() {
			ts = time.Now()
		}
		st.values = append(st.values, []any{strconv.FormatInt(ts.UnixNano(), 10), e.Line})
	}

	out := make([]any, 0, len(order))
	for _, key := range order {
		out = append(out, map[string]any{
			"stream": streams[key].labels,
			"values": streams[key].values,
		})
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(map[string]any{"streams": out}); err != nil {
		log.Printf("metric: encode loki streams: %v", err)
	}
}

// labelsKey renders a label set as a stable, unambiguous string for stream
// grouping. Keys and values are length-prefixed rather than joined with bare
// separators: a comma-joined form collides, so {"a":"b,c=d"} and
// {"a":"b","c":"d"} would hash to the same key and merge two distinct label
// sets into one Loki stream, mislabelling every line in it.
func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteString(strconv.Itoa(len(labels[k])))
		b.WriteByte(':')
		b.WriteString(labels[k])
	}
	return b.String()
}

// collectRuntime updates runtime-derived gauges immediately before each scrape.
func (c *Collector) collectRuntime() {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	AppUptime.Set(time.Since(c.startTime).Seconds())

	if c.includeRuntime {
		GoGoroutines.Set(float64(runtime.NumGoroutine()))

		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		GoMemAllocBytes.Set(float64(ms.Alloc))
		GoMemSysBytes.Set(float64(ms.Sys))
		GoGCRunsTotal.Set(float64(ms.NumGC))

		// Accumulate delta GC pause time since last scrape.
		if ms.PauseTotalNs > c.lastPauseTotalNs {
			delta := ms.PauseTotalNs - c.lastPauseTotalNs
			GoGCPauseTotalSeconds.Add(float64(delta) / 1e9)
			c.lastPauseTotalNs = ms.PauseTotalNs
		}
	}

	if c.includeSystem {
		c.collectSystem()
	}

	c.torMu.Lock()
	fn := c.torState
	c.torMu.Unlock()
	if fn != nil {
		enabled, running := fn()
		setBool(TorEnabled, enabled)
		setBool(TorRunning, running)
		// Running implies a bootstrapped circuit (Tor manager blocks on
		// bootstrap before reporting running).
		setBool(TorCircuitEstablished, running)
	}
}

// collectSystem samples CPU, memory, and disk usage into the system_* gauges.
func (c *Collector) collectSystem() {
	st, ok := readSystemStats(c.dataDir)
	if !ok {
		return
	}
	if st.memTotal > 0 {
		SystemMemTotalBytes.Set(float64(st.memTotal))
		SystemMemUsedBytes.Set(float64(st.memUsed))
		SystemMemUsagePct.Set(float64(st.memUsed) / float64(st.memTotal) * 100)
	}
	// CPU percentage is derived from the delta between consecutive scrapes.
	if c.lastCPUTotal != 0 && st.cpuTotal > c.lastCPUTotal {
		totalDelta := st.cpuTotal - c.lastCPUTotal
		idleDelta := st.cpuIdle - c.lastCPUIdle
		if totalDelta > 0 {
			SystemCPUUsagePct.Set(float64(totalDelta-idleDelta) / float64(totalDelta) * 100)
		}
	}
	c.lastCPUTotal = st.cpuTotal
	c.lastCPUIdle = st.cpuIdle
	if st.diskTotal > 0 && c.dataDir != "" {
		SystemDiskTotalBytes.WithLabelValues(c.dataDir).Set(float64(st.diskTotal))
		SystemDiskUsedBytes.WithLabelValues(c.dataDir).Set(float64(st.diskUsed))
		SystemDiskUsagePct.WithLabelValues(c.dataDir).Set(float64(st.diskUsed) / float64(st.diskTotal) * 100)
	}
}

// Middleware returns a chi-compatible middleware that records HTTP metrics.
func (c *Collector) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip the metrics endpoint itself to avoid self-referential noise.
			if r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			HTTPActiveRequests.Inc()
			defer HTTPActiveRequests.Dec()

			// Count requests arriving over the Tor hidden service.
			if strings.Contains(strings.ToLower(r.Host), ".onion") {
				TorRequestsTotal.Inc()
			}

			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(wrapped, r)

			path := normalizePath(r.URL.Path)
			dur := time.Since(start).Seconds()
			status := strconv.Itoa(wrapped.statusCode)

			HTTPRequestsTotal.WithLabelValues(r.Method, path, status).Inc()
			c.httpReqDuration.WithLabelValues(r.Method, path).Observe(dur)
			if r.ContentLength > 0 {
				c.httpReqSize.WithLabelValues(r.Method, path).Observe(float64(r.ContentLength))
			}
			c.httpRespSize.WithLabelValues(r.Method, path).Observe(float64(wrapped.written))
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture status code and bytes written.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
	written    int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	n, err := rw.ResponseWriter.Write(b)
	rw.written += n
	return n, err
}

// normalizePath replaces dynamic path segments (IDs, UUIDs, hashes) with ":id"
// to prevent label cardinality explosion.
func normalizePath(path string) string {
	if len(path) == 0 {
		return "/"
	}
	// Replace segments that look like IDs: 8+ hex/alphanumeric chars.
	out := make([]byte, 0, len(path))
	i := 0
	for i < len(path) {
		if path[i] == '/' {
			out = append(out, '/')
			i++
			j := i
			for j < len(path) && path[j] != '/' {
				j++
			}
			seg := path[i:j]
			if isIDSegment(seg) {
				out = append(out, ':')
				out = append(out, 'i', 'd')
			} else {
				out = append(out, seg...)
			}
			i = j
		} else {
			i++
		}
	}
	if len(out) == 0 {
		return "/"
	}
	return string(out)
}

// isIDSegment returns true for segments that look like database IDs or UUIDs.
func isIDSegment(s string) bool {
	if len(s) < 6 {
		return false
	}
	hex := 0
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == '-' {
			hex++
		}
	}
	// If 90%+ hex/UUID chars and 8+ long — treat as ID.
	return float64(hex)/float64(len(s)) > 0.9 && len(s) >= 8
}
