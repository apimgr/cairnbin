package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"expvar"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	// blank import side-effect: registers pprof handlers on DefaultServeMux
	_ "net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apimgr/pastebin/src/audit"
	"github.com/apimgr/pastebin/src/cache"
	"github.com/apimgr/pastebin/src/common/buildinfo"
	"github.com/apimgr/pastebin/src/common/email"
	"github.com/apimgr/pastebin/src/common/httputil"
	"github.com/apimgr/pastebin/src/common/i18n"
	"github.com/apimgr/pastebin/src/config"
	"github.com/apimgr/pastebin/src/database"
	"github.com/apimgr/pastebin/src/geoip"
	"github.com/apimgr/pastebin/src/graphql"
	"github.com/apimgr/pastebin/src/handler"
	"github.com/apimgr/pastebin/src/handler/compat"
	"github.com/apimgr/pastebin/src/health"
	"github.com/apimgr/pastebin/src/i2p"
	"github.com/apimgr/pastebin/src/logging"
	"github.com/apimgr/pastebin/src/mcp"
	"github.com/apimgr/pastebin/src/metric"
	"github.com/apimgr/pastebin/src/mode"
	"github.com/apimgr/pastebin/src/model"
	"github.com/apimgr/pastebin/src/notify"
	"github.com/apimgr/pastebin/src/ssl"
	"github.com/apimgr/pastebin/src/swagger"
	"github.com/apimgr/pastebin/src/tor"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	qrcode "github.com/skip2/go-qrcode"
)

//go:embed template/layout/*.tmpl template/partial/*.tmpl template/partial/public/*.tmpl template/page/*.tmpl
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// requestStats tracks total and per-hour request counts using a 24-bucket ring.
type requestStats struct {
	total      atomic.Int64
	activeConn atomic.Int64
	buckets    [24]atomic.Int64
	mu         sync.Mutex
	lastHour   int
}

func (rs *requestStats) inc() {
	rs.total.Add(1)
	h := time.Now().Hour()
	rs.mu.Lock()
	if h != rs.lastHour {
		// Zero out stale buckets between lastHour and h.
		for cur := (rs.lastHour + 1) % 24; cur != (h+1)%24; cur = (cur + 1) % 24 {
			rs.buckets[cur].Store(0)
		}
		rs.lastHour = h
	}
	rs.mu.Unlock()
	rs.buckets[h].Add(1)
}

func (rs *requestStats) last24h() int64 {
	var sum int64
	for i := 0; i < 24; i++ {
		sum += rs.buckets[i].Load()
	}
	return sum
}

// ─── Health types (PART 13) ──────────────────────────────────────────────────

// HealthResponse is the canonical /server/healthz response structure (PART 13).
type HealthResponse struct {
	Project        ProjectInfo  `json:"project"`
	Status         string       `json:"status"`
	PendingRestart bool         `json:"pending_restart,omitempty"`
	RestartReason  []string     `json:"restart_reason,omitempty"`
	Version        string       `json:"version"`
	GoVersion      string       `json:"go_version"`
	Build          BuildInfo    `json:"build"`
	Uptime         string       `json:"uptime"`
	Mode           string       `json:"mode"`
	Timestamp      time.Time    `json:"timestamp"`
	Features       FeaturesInfo `json:"features"`
	Checks         ChecksInfo   `json:"checks"`
	Stats          StatsInfo    `json:"stats"`
	// Maintenance is an APP-SPECIFIC field (AI.md:17142 "Add custom fields
	// here"), populated only while the server is in maintenance mode (PART 20).
	Maintenance *MaintenanceInfo `json:"maintenance,omitempty"`
}

// MaintenanceInfo reports maintenance-mode state in the healthz response (PART 20).
type MaintenanceInfo struct {
	Reason      string          `json:"reason"`
	Message     string          `json:"message"`
	Since       time.Time       `json:"since"`
	SelfHealing SelfHealingInfo `json:"self_healing"`
}

// SelfHealingInfo reports the self-healing loop state (PART 20).
type SelfHealingInfo struct {
	Enabled     bool       `json:"enabled"`
	Attempts    int        `json:"attempts"`
	LastAttempt *time.Time `json:"last_attempt,omitempty"`
	NextAttempt *time.Time `json:"next_attempt,omitempty"`
}

// ProjectInfo holds public branding fields.
type ProjectInfo struct {
	Name        string `json:"name"`
	Tagline     string `json:"tagline"`
	Description string `json:"description"`
}

// BuildInfo holds build-time metadata.
type BuildInfo struct {
	Commit string `json:"commit"`
	Date   string `json:"date"`
}

// FeaturesInfo reports which optional features are active.
type FeaturesInfo struct {
	Tor   TorInfo `json:"tor"`
	I2P   I2PInfo `json:"i2p"`
	GeoIP bool    `json:"geoip"`
}

// TorInfo reports Tor hidden service status.
type TorInfo struct {
	Enabled  bool   `json:"enabled"`
	Running  bool   `json:"running"`
	Status   string `json:"status"`
	Hostname string `json:"hostname"`
}

// I2PInfo reports I2P eepsite status.
type I2PInfo struct {
	Enabled  bool   `json:"enabled"`
	Running  bool   `json:"running"`
	Status   string `json:"status"`
	Hostname string `json:"hostname"`
	// Provider is the active backend: "i2pd", "sam", or "none".
	Provider string `json:"provider"`
}

// ChecksInfo reports component health as "ok" or "error".
type ChecksInfo struct {
	Database  string `json:"database"`
	Cache     string `json:"cache"`
	Disk      string `json:"disk"`
	Scheduler string `json:"scheduler"`
	Tor       string `json:"tor,omitempty"`
	I2P       string `json:"i2p,omitempty"`
	// Config is an APP-SPECIFIC check (AI.md:17202 "Add your checks here").
	Config string `json:"config"`
}

// StatsInfo holds public-safe aggregate statistics.
type StatsInfo struct {
	RequestsTotal int64 `json:"requests_total"`
	Requests24h   int64 `json:"requests_24h"`
	ActiveConns   int   `json:"active_connections"`
	PastesTotal   int64 `json:"pastes_total"`
}

// SchedulerAPI is the interface the server uses to interact with the scheduler.
// It is satisfied by *scheduler.Scheduler and can be set via SetSchedulerAPI.
type SchedulerAPI interface {
	GetTasks() []database.TaskState
	GetTask(id string) (database.TaskState, bool)
	RunNow(id string) error
	EnableTask(id string)
	DisableTask(id string)
}

// Server owns the HTTP router and all handler dependencies.
type Server struct {
	router         *chi.Mux
	db             database.DB
	cacheStore     cache.Cache
	cfg            *config.Config
	cfgMgr         *config.ConfigManager
	templates      map[string]*template.Template
	pasteHandler   *handler.PasteHandler
	compatHandler  *compat.Handler
	swaggerHandler *swagger.Handler
	mcpHandler     *mcp.Handler
	// ipBlocks holds temporary (auto) IP blocks and flood counters (PART 11).
	ipBlocks           *ipBlocks
	graphqlHandler     *graphql.Handler
	metricsCollector   *metric.Collector
	geoipDB            *geoip.DB
	torManager         *tor.Manager
	i2pManager         *i2p.Manager
	readLimiter        *rateLimiter
	writeLimiter       *rateLimiter
	healthLimiter      *rateLimiter
	burstLimiter       *rateLimiter
	strictLimiter      *rateLimiter
	restoreFailLimiter *rateLimiter
	version            string
	commitID           string
	buildDate          string
	configDir          string
	dataDir            string
	// logDir is the runtime logs directory, set by main via SetLogDir. Used by
	// the security.log writer (PART 11 Coordinated Disclosure Pipeline).
	logDir string
	// auditLogger is the JSON Lines audit-log writer (AI.md server.logs.audit),
	// set by main via SetAuditWriter. A nil writer silently drops entries.
	auditLogger *audit.Writer
	// logManager owns the server's log files (access/server/error/app/auth/
	// debug — AI.md server.logs), set by main via SetLogManager. A nil manager
	// silently drops all lines.
	logManager *logging.Manager
	// maintenance is the runtime self-healing maintenance-mode monitor (PART 20).
	maintenance *health.Monitor
	startTime   time.Time
	stats       requestStats
	// schedHealthFn is an optional callback that reports whether the scheduler
	// is running — set by main after constructing the server.
	schedHealthFn func() bool
	// schedulerAPI provides runtime access to the scheduler for the API handlers.
	schedulerAPI SchedulerAPI
	// operatorTokenHash is SHA-256(server.token), cached at construction time.
	// Constant-time compared against incoming Bearer tokens on protected routes.
	operatorTokenHash [32]byte
	// pendingRestartMu guards pendingRestartKeys.
	pendingRestartMu   sync.Mutex
	pendingRestartKeys []string
	// csrfSecret is the HMAC key for CSRF token signing, loaded from the DB at startup.
	csrfSecret []byte
	// installSecret is the root installation_secret (PART 11 Cryptographic Keys),
	// loaded from the DB at startup; base for the rotating security_id token.
	installSecret []byte
	// privDrop holds privilege-drop parameters set by main before Run (PART 23 step 8g).
	privDrop *privDropConfig
	// notifier delivers outbound role-scoped webhook notifications (PART 17).
	notifier *notify.Dispatcher
	// resolvedProxyMu guards resolvedProxyIPs.
	resolvedProxyMu sync.RWMutex
	// resolvedProxyIPs maps each DNS name in trusted_proxies.additional to its
	// current set of resolved IP addresses.  Refreshed every 5 minutes (PART 12).
	resolvedProxyIPs map[string][]net.IP
	// domainLearner observes Host headers and infers baseDomain / wildcardDomain
	// for dynamic CORS and URL-building support (PART 12 url_detection).
	domainLearner *domainLearner
	// shuttingDown becomes true as soon as a graceful shutdown signal arrives,
	// before the HTTP server stops accepting connections. The healthz handlers
	// surface this as status "shutting_down" with HTTP 503 (AI.md PART 13).
	shuttingDown atomic.Bool
}

// privDropConfig describes the unprivileged service account to switch to after
// privileged ports are bound, plus the runtime paths that must be chowned to that
// account first so the dropped process can still read and write them (PART 23).
type privDropConfig struct {
	user  string
	group string
	chown []string
}

// SetLogDir registers the runtime logs directory used by the security.log writer
// (PART 11). Call from main after resolving directories, before Run.
func (s *Server) SetLogDir(dir string) {
	s.logDir = dir
}

// SetAuditWriter registers the JSON Lines audit-log writer (AI.md
// server.logs.audit). Call from main after resolving directories, before Run.
func (s *Server) SetAuditWriter(w *audit.Writer) {
	s.auditLogger = w
}

// auditLog records one audit entry from an *http.Request, filling the actor IP
// and user-agent from the request. A nil auditLogger silently drops the entry.
func (s *Server) auditLog(r *http.Request, e audit.Entry) {
	if s.auditLogger == nil {
		return
	}
	if e.Actor.IP == "" && r != nil {
		e.Actor.IP = clientIP(r)
	}
	if e.Actor.UserAgent == "" && r != nil {
		e.Actor.UserAgent = r.UserAgent()
	}
	// Requests dispatched by the MCP endpoint are distinguishable in the audit
	// trail (PART 11: the audit `source` is `mcp`).
	if r != nil && mcp.FromContext(r.Context()) {
		details := make(map[string]any, len(e.Details)+1)
		for k, v := range e.Details {
			details[k] = v
		}
		details["source"] = "mcp"
		e.Details = details
	}
	s.auditLogger.Log(e)
}

// SetPrivilegeDrop registers the service user/group and the runtime paths to chown
// before dropping privileges. Call from main after constructing the server and
// resolving all runtime directories, before Run. A nil/empty user means the default
// service account ("pastebin"); the drop is a no-op when not running as root.
func (s *Server) SetPrivilegeDrop(user, group string, chownPaths []string) {
	s.privDrop = &privDropConfig{user: user, group: group, chown: chownPaths}
}

// bindAndDrop binds the TCP listener for addr while still privileged (so ports below
// 1024 succeed), then chowns the runtime paths and drops to the unprivileged service
// account (PART 23 step 8g). The returned listener is ready for Serve/ServeTLS.
func (s *Server) bindAndDrop(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if err := s.applyPrivilegeDrop(); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// applyPrivilegeDrop chowns runtime paths and permanently drops privileges to the
// configured service account. It is a no-op unless privilege drop was configured and
// the process is currently running as root. When no unprivileged target can be
// resolved it logs and keeps running as root rather than failing startup.
func (s *Server) applyPrivilegeDrop() error {
	if s.privDrop == nil || !currentlyRoot() {
		return nil
	}
	uid, gid, name, ok := resolvePrivDropTarget(s.privDrop.user, s.privDrop.group)
	if !ok {
		log.Printf("privilege: no drop target resolved; continuing as root")
		return nil
	}
	// Chown runtime paths so the dropped account retains read/write access.
	for _, p := range s.privDrop.chown {
		if p == "" {
			continue
		}
		if err := chownRecursive(p, uid, gid); err != nil {
			log.Printf("privilege: chown %s: %v", p, err)
		}
	}
	if err := dropPrivileges(uid, gid); err != nil {
		return fmt.Errorf("drop privileges to %s: %w", name, err)
	}
	log.Printf("privilege: dropped to %s (uid=%d gid=%d)", name, uid, gid)
	return nil
}

// chownRecursive changes ownership of root and everything beneath it to uid/gid.
// Missing paths are skipped; per-entry errors are returned to the caller.
func chownRecursive(root string, uid, gid int) error {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(path, uid, gid)
	})
}

// SetSchedulerHealthFn registers a callback that reports whether the scheduler is running.
// Call this from main after constructing the server, before calling Run.
func (s *Server) SetSchedulerHealthFn(fn func() bool) {
	s.schedHealthFn = fn
}

// SetSchedulerAPI wires the scheduler into the server so scheduler API routes can
// delegate to it. Call this from main after constructing both the server and the
// scheduler, before calling Run.
func (s *Server) SetSchedulerAPI(api SchedulerAPI) {
	s.schedulerAPI = api
}

// MarkPendingRestart records a config key that requires a restart to take effect.
// The key is surfaced in the healthz pending_restart / restart_reason fields.
func (s *Server) MarkPendingRestart(key string) {
	s.pendingRestartMu.Lock()
	defer s.pendingRestartMu.Unlock()
	for _, k := range s.pendingRestartKeys {
		if k == key {
			return
		}
	}
	s.pendingRestartKeys = append(s.pendingRestartKeys, key)
}

// liveCfg returns the most current config, applying hot-reloaded values if available.
func (s *Server) liveCfg() *config.Config {
	if s.cfgMgr != nil {
		return s.cfgMgr.Get()
	}
	return s.cfg
}

// apiVersion returns the configured {api_version} route segment (PART 14),
// falling back to "v1" when unset.
func (s *Server) apiVersion() string {
	v := strings.TrimSpace(s.liveCfg().Server.APIVersion)
	if v == "" {
		return "v1"
	}
	return v
}

// New constructs a Server and wires all routes.
// cfgMgr may be nil (e.g. in tests); when set, hot-reloadable settings are read live.
func New(db database.DB, cfg *config.Config, cfgMgr *config.ConfigManager, version, commitID, buildDate, configDir, dataDir string) *Server {
	s := &Server{
		ipBlocks:  newIPBlocks(),
		router:    chi.NewRouter(),
		db:        db,
		cfg:       cfg,
		cfgMgr:    cfgMgr,
		version:   version,
		commitID:  commitID,
		buildDate: buildDate,
		configDir: configDir,
		dataDir:   dataDir,
		startTime: time.Now(),
	}
	s.stats.lastHour = time.Now().Hour()

	// Publish the build identity used by every asset URL, cache header, and the
	// version-change purge cookie (AI.md PART 9, "Asset Version-Busting").
	buildinfo.Set(version, commitID)

	// Validate configured SEO site-verification codes/custom tags at startup and
	// log (never abort) on invalid format — invalid entries are simply never
	// rendered (PART 16 "Server validates codes on startup and logs errors").
	logSEOVerificationErrors(cfg.Server.SEO.Verification)

	// Webhook dispatcher for outbound role notifications (PART 17).
	s.notifier = notify.New(notify.Meta{
		ProjectName:    "pastebin",
		ProjectVersion: version,
		AppURL:         cfg.Server.BaseURL,
	}, nil)

	// Cache SHA-256(server.token) early so it can be passed to handlers below.
	// If the token is empty, operatorTokenHash remains zero — protected routes return 401.
	if cfg.Server.Token != "" {
		s.operatorTokenHash = sha256.Sum256([]byte(cfg.Server.Token))
	}

	s.pasteHandler = handler.NewPasteHandler(db, cfg.Server.BaseURL, s.operatorTokenHash)
	if key, err := cfg.EncryptionKey(); err == nil {
		s.pasteHandler.SetTokenEncryptionKey(key)
	}
	s.pasteHandler.SetBaseURLResolver(s.baseURL)
	s.compatHandler = compat.New(s.pasteHandler, db, version)
	s.swaggerHandler = swagger.New(cfg.Web.SiteTitle+" API", version, cfg.Server.BaseURL, cfg.Server.APIVersion)
	s.swaggerHandler.SetBaseURLResolver(s.baseURL)
	s.swaggerHandler.SetAssetPrefixResolver(s.assetPrefix)
	s.swaggerHandler.SetThemeResolver(s.themeFromRequest)
	s.swaggerHandler.SetCSRFTokenResolver(s.csrfTokenFromRequest)
	s.mcpHandler = mcp.New(mcp.Config{
		APIVersion:    s.apiVersion(),
		ServerName:    cfg.Web.SiteTitle,
		ServerVersion: version,
		Dispatch:      s.router,
		Enabled:       func() bool { return s.liveCfg().Server.MCP.Enabled },
		OriginAllowed: s.mcpOriginAllowed,
	})
	s.graphqlHandler = graphql.New(db, cfg.Web.SiteTitle)
	s.graphqlHandler.SetAssetPrefixResolver(s.assetPrefix)
	s.graphqlHandler.SetThemeResolver(s.themeFromRequest)
	s.graphqlHandler.SetCSRFTokenResolver(s.csrfTokenFromRequest)
	s.metricsCollector = metric.NewWithOptions(metric.Options{
		Version:   version,
		Commit:    commitID,
		BuildDate: buildDate,
		Token:     cfg.Server.Metrics.Token,
		ServiceTokens: map[string]string{
			metric.ServicePrometheus: cfg.Server.Metrics.Auth.Tokens.Prometheus,
			metric.ServiceGrafana:    cfg.Server.Metrics.Auth.Tokens.Grafana,
			metric.ServiceLoki:       cfg.Server.Metrics.Auth.Tokens.Loki,
		},
		AllowUnauthenticated: cfg.Server.Metrics.Auth.AllowUnauthenticated,
		StartTime:            s.startTime,
		IncludeSystem:        cfg.Server.Metrics.IncludeSystem,
		IncludeRuntime:       cfg.Server.Metrics.IncludeRuntime,
		DurationBuckets:      cfg.Server.Metrics.DurationBuckets,
		SizeBuckets:          cfg.Server.Metrics.SizeBuckets,
		DataDir:              dataDir,
		LokiMaxEntries:       cfg.Server.Metrics.Loki.MaxEntries,
		LokiMaxAge:           cfg.Server.Metrics.Loki.MaxAge,
		LogProvider:          s.recentLogEntries,
	})

	// PART 20: an empty per-service token disables that service (403). Log the
	// reason once at startup so an operator can tell "metrics off" from "metrics
	// broken" without a token ever appearing in a log line.
	if disabled := s.metricsCollector.DisabledServices(); len(disabled) > 0 {
		log.Printf("metrics: no bearer token configured; service(s) disabled and answering 403: %s",
			strings.Join(disabled, ", "))
	}

	if cfg.Server.GeoIP.Enabled {
		gcfg := geoip.Config{
			Dir:            cfg.Server.GeoIP.Dir,
			EnableASN:      cfg.Server.GeoIP.Databases.ASN,
			EnableCountry:  cfg.Server.GeoIP.Databases.Country,
			EnableCity:     cfg.Server.GeoIP.Databases.City,
			DenyCountries:  cfg.Server.GeoIP.DenyCountries,
			AllowCountries: cfg.Server.GeoIP.AllowCountries,
			// Wire server-wide security allowlist into geoip so GeoIP also
			// bypasses country-blocking for explicitly allowlisted IPs.
			Allowlist: cfg.Web.Security.Allowlist,
		}
		if gdb, err := geoip.Open(gcfg); err != nil {
			log.Printf("warning: geoip init: %v", err)
		} else {
			s.geoipDB = gdb
		}
	}

	// Tor hidden service — auto-enabled when Tor binary is found.
	torCfg := tor.Config{
		Binary:                    cfg.Server.Tor.Binary,
		UseNetwork:                cfg.Server.Tor.UseNetwork,
		MaxCircuits:               cfg.Server.Tor.MaxCircuits,
		CircuitTimeout:            cfg.Server.Tor.CircuitTimeout,
		BootstrapTimeout:          cfg.Server.Tor.BootstrapTimeout,
		SafeLogging:               cfg.Server.Tor.SafeLogging,
		MaxStreamsPerCircuit:      cfg.Server.Tor.MaxStreamsPerCircuit,
		CloseCircuitOnStreamLimit: cfg.Server.Tor.CloseCircuitOnStreamLimit,
		BandwidthRate:             cfg.Server.Tor.BandwidthRate,
		BandwidthBurst:            cfg.Server.Tor.BandwidthBurst,
		MaxMonthlyBandwidth:       cfg.Server.Tor.MaxMonthlyBandwidth,
		NumIntroPoints:            cfg.Server.Tor.NumIntroPoints,
		VirtualPort:               cfg.Server.Tor.VirtualPort,
		ConfigDir:                 configDir,
		DataDir:                   dataDir,
	}
	// Forward the hidden service to the server's real HTTP listener. Port may be a
	// dual "http,https" value (PART 5/15); the onion maps :virtual → the plaintext
	// HTTP port, so parse the first field. Never hardcode a dev port — an unresolved
	// value leaves serverPort 0 and the Tor manager skips rather than forward blindly.
	portField := cfg.Server.Port
	if i := strings.IndexByte(portField, ','); i >= 0 {
		portField = portField[:i]
	}
	serverPort, _ := strconv.Atoi(strings.TrimSpace(portField))
	s.torManager = tor.NewManager(context.Background(), serverPort, torCfg, s.router)

	// Report Tor state into the tor_* metrics at scrape time.
	s.metricsCollector.SetTorProvider(func() (enabled, running bool) {
		ti := s.buildTorInfo()
		return ti.Enabled, ti.Running
	})

	// Initialize cache driver (PART 9/12). Falls back to in-process memory on error.
	cacheCfg := cache.Config{
		Type:          cfg.Server.Cache.Type,
		URL:           cfg.Server.Cache.URL,
		Host:          cfg.Server.Cache.Host,
		Port:          cfg.Server.Cache.Port,
		Username:      cfg.Server.Cache.Username,
		Password:      cfg.Server.Cache.Password,
		DB:            cfg.Server.Cache.DB,
		TLS:           cfg.Server.Cache.TLS,
		TLSSkipVerify: cfg.Server.Cache.TLSSkipVerify,
		PoolSize:      cfg.Server.Cache.PoolSize,
		MinIdle:       cfg.Server.Cache.MinIdle,
		Prefix:        cfg.Server.Cache.Prefix,
	}
	if d, err := time.ParseDuration(cfg.Server.Cache.Timeout); err == nil {
		cacheCfg.Timeout = d
	}
	if d, err := time.ParseDuration(cfg.Server.Cache.TTL); err == nil {
		cacheCfg.TTL = d
	}
	if cs, err := cache.New(cacheCfg); err != nil {
		log.Printf("warning: cache init failed (%v); falling back to memory", err)
		fallback := cache.DefaultConfig()
		fallback.Prefix = cacheCfg.Prefix
		fallback.TTL = cacheCfg.TTL
		s.cacheStore, _ = cache.New(fallback)
	} else {
		s.cacheStore = cs
	}
	// Wire the cache driver into the paste read path (PART 9) now that it's ready.
	s.pasteHandler.SetCache(s.cacheStore)
	// Wire the configured max paste size (server.yml paste.max_size) into
	// the create path so the operator-set limit is actually enforced.
	s.pasteHandler.SetMaxSize(cfg.Paste.MaxSizeBytes)

	if cfg.Server.RateLimit.Enabled {
		readReqs := cfg.Server.RateLimit.Read.Requests
		writeReqs := cfg.Server.RateLimit.Write.Requests
		healthReqs := cfg.Server.RateLimit.Health.Requests
		if readReqs <= 0 {
			readReqs = 120
		}
		if writeReqs <= 0 {
			writeReqs = 10
		}
		if healthReqs <= 0 {
			healthReqs = 120
		}
		readWin := time.Duration(cfg.Server.RateLimit.Read.Window) * time.Second
		writeWin := time.Duration(cfg.Server.RateLimit.Write.Window) * time.Second
		healthWin := time.Duration(cfg.Server.RateLimit.Health.Window) * time.Second
		if readWin <= 0 {
			readWin = time.Minute
		}
		if writeWin <= 0 {
			writeWin = time.Minute
		}
		if healthWin <= 0 {
			healthWin = time.Minute
		}
		burstReqs := cfg.Server.RateLimit.GlobalBurst
		if burstReqs <= 0 {
			burstReqs = 240
		}
		s.readLimiter = newRateLimiter(readReqs, readWin)
		s.writeLimiter = newRateLimiter(writeReqs, writeWin)
		s.healthLimiter = newRateLimiter(healthReqs, healthWin)
		// Absolute ceiling across all endpoint classes combined (AI.md:16187,
		// 240 req/min per IP). Applied as the step-7 global RateLimitMiddleware;
		// the per-class limiters above remain attached to their routes as the
		// tighter per-class quotas.
		s.burstLimiter = newRateLimiter(burstReqs, time.Minute)
	}
	// Token disclosure, rotation, and identity restoration are login-like
	// operations and use a stricter per-IP limit than ordinary writes.
	s.strictLimiter = newRateLimiter(10, 15*time.Minute)
	s.restoreFailLimiter = newRateLimiter(5, 15*time.Minute)

	tmpl, err := s.buildTemplates()
	if err != nil {
		log.Printf("warning: could not parse templates: %v", err)
	}
	s.templates = tmpl

	// Ensure all project-level secrets exist in the DB (PART 11).
	// These are generated on first start and never returned in API responses.
	for _, secretKey := range []string{"installation_secret", "cookie_signing_key", "csrf_token_secret"} {
		if _, err := db.EnsureAppSecret(secretKey); err != nil {
			log.Printf("warning: could not initialize app secret %q: %v", secretKey, err)
		}
	}
	// Cache the CSRF signing secret for use in csrfMiddleware.
	if csrfSec, err := db.EnsureAppSecret("csrf_token_secret"); err == nil {
		s.csrfSecret = csrfSec
	}
	// Cache the installation secret for the rotating security_id token (PART 11).
	if instSec, err := db.EnsureAppSecret("installation_secret"); err == nil {
		s.installSecret = instSec
	}

	// Generate the project PGP keypair on first start when publishing is enabled
	// (PART 11 → GPG Keypair Management). Never fails startup; falls back to
	// AES-256-GCM report encryption when unavailable. Prune any expired parked key.
	s.ensureSecurityKeypair()
	s.pruneRotatedKey()

	// Runtime self-healing maintenance monitor (PART 20). Enters maintenance mode
	// on a critical error (DB connection loss or file-write failure) and rejects
	// writes with HTTP 503 while retrying recovery in the background.
	mc := cfg.Server.Maintenance
	retryInterval := 30 * time.Second
	if d, err := time.ParseDuration(mc.SelfHealing.RetryInterval); err == nil && d > 0 {
		retryInterval = d
	}
	s.maintenance = health.New(health.Config{
		SelfHealingEnabled: mc.SelfHealing.Enabled,
		RetryInterval:      retryInterval,
		MaxAttempts:        mc.SelfHealing.MaxAttempts,
		NotifyOnEnter:      mc.Notify.OnEnter,
		NotifyOnExit:       mc.Notify.OnExit,
	})
	s.maintenance.SetChecker(s.criticalCheck)
	s.maintenance.SetCleaner(s.maintenanceCleanup)
	s.maintenance.SetNotifier(s.maintenanceNotify)
	s.maintenance.SetTransitionHook(func(entered bool, reason string) {
		event := "server.maintenance_exited"
		if entered {
			event = "server.maintenance_entered"
		}
		s.auditLog(nil, audit.Entry{
			Event:    event,
			Severity: audit.SeverityWarn,
			Result:   audit.ResultSuccess,
			Reason:   reason,
		})
	})
	onRateLimited = func(r *http.Request, class string) {
		s.auditLog(r, audit.Entry{
			Event:    "security.rate_limit_exceeded",
			Severity: audit.SeverityWarn,
			Result:   audit.ResultFailure,
			Target:   &audit.Target{Type: "endpoint", ID: r.URL.Path},
			Details:  map[string]any{"limit": class},
		})
	}
	geoip.OnBlocked = func(r *http.Request, country string) {
		s.auditLog(r, audit.Entry{
			Event:    "security.country_blocked",
			Severity: audit.SeverityWarn,
			Result:   audit.ResultFailure,
			Target:   &audit.Target{Type: "endpoint", ID: r.URL.Path},
			Details:  map[string]any{"country": country},
		})
	}

	// Domain-learning subsystem (PART 12 url_detection).
	s.domainLearner = newDomainLearner(&cfg.Server.URLDetection)
	// Pre-resolved DNS proxy IPs map (Gap 1); populated by refreshDNSTrustedProxies.
	s.resolvedProxyIPs = make(map[string][]net.IP)

	s.setupRoutes()
	return s
}

// buildTemplates parses every embedded template with the server's FuncMap.
// Isolating parsing here keeps New readable and ensures the FuncMap used at
// startup is the single source of truth for template rendering.
//
// The layout and partials are parsed once into a shared base tree, then each
// page under template/page/*.tmpl is parsed into its own clone of that base.
// This is required because every page defines {{define "meta"}} and
// {{define "content"}} with the same names — a single shared *template.Template
// has one flat namespace, so parsing all pages together would collide. Cloning
// per page keeps each page's "meta"/"content" (and, for emb.tmpl, "layout")
// definitions isolated from every other page.
//
// Map keys retain the legacy "{name}.html" form (e.g. "about.html") so the
// ~40 existing renderTemplate(w, r, "about.html", data) call sites are unchanged.
func (s *Server) buildTemplates() (map[string]*template.Template, error) {
	funcMap := template.FuncMap{
		"t": func(lang, key string) string {
			return i18n.Translate(lang, key)
		},
		"tf": func(lang, key string, args ...interface{}) string {
			return i18n.TranslateFormat(lang, key, args...)
		},
		"i18njs": func(lang string) template.JS {
			return template.JS(i18n.JSBundle(lang))
		},
		"markdown": func(s string) template.HTML {
			return renderMarkdown(s)
		},
		"trackingEnabled": func() bool {
			return s.liveCfg().Server.Tracking.Enabled()
		},
		"trackingScript": func() template.HTML {
			return renderTrackingScript(s.liveCfg().Server.Tracking)
		},
		"consentConfig": func() template.JS {
			return renderConsentConfig(s.liveCfg())
		},
		// consentInfo exposes the same consent configuration as plain Go values
		// (rather than a JSON literal) for the no-JS <noscript> consent form
		// (AI.md 25598), which cannot execute app.js to read pb-consent-data.
		"consentInfo": func() consentClientConfig {
			return buildConsentClientConfig(s.liveCfg())
		},
		// asset appends the running build stamp to a static asset path so a
		// browser never serves a stale file after an update (AI.md PART 9,
		// line 13285 — hand-written bare /static/... URLs are a bug).
		"asset":   assetURL,
		"fmtTime": fmtUserTime,
		"fmtDate": fmtUserDate,
		// "list" builds a []string from its arguments so a template can range
		// over a fixed set of literal values (option lists, button rows).
		//
		// This is deliberately NOT named "slice": `slice` is a text/template
		// builtin implementing range-slicing (`slice coll 0 2`), and calling
		// it as a list-maker fails only at RENDER time, not at parse time —
		// "cannot index slice/array with type string" — so the breakage is
		// invisible until the page is actually served. Naming the helper
		// `list` also avoids shadowing that builtin for any future template
		// that legitimately wants a real slice expression.
		"list": func(values ...string) []string { return values },
	}
	base, err := template.New("").Funcs(funcMap).ParseFS(templatesFS,
		"template/layout/*.tmpl",
		"template/partial/*.tmpl",
		"template/partial/public/*.tmpl",
	)
	if err != nil {
		return nil, err
	}
	entries, err := fs.Glob(templatesFS, "template/page/*.tmpl")
	if err != nil {
		return nil, err
	}
	pages := make(map[string]*template.Template, len(entries))
	for _, entry := range entries {
		clone, err := base.Clone()
		if err != nil {
			return nil, err
		}
		clone, err = clone.ParseFS(templatesFS, entry)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(filepath.Base(entry), ".tmpl") + ".html"
		pages[name] = clone
	}
	return pages, nil
}

// userTimeLayout is the canonical user-facing timestamp format (AI.md 19714):
// "December 4, 2025 at 13:05:13 EST". RFC 3339 is reserved for machine-readable
// surfaces (API responses, logs, health endpoints).
const userTimeLayout = "January 2, 2006 at 15:04:05 MST"

// userDateLayout is the date-only variant for surfaces that show a day without a
// wall-clock time (e.g. a compact "recent pastes" list).
const userDateLayout = "January 2, 2006"

// ownerTokenCookieName is the project-unique browser storage name for the
// resource-owner token cookie (and, in app.js, the matching localStorage key).
// Per AI.md PART 11 "Naming": both MUST share this exact {project_name}_owner_token_XXXXXX
// name — fixed suffix recorded in IDEA.md (`owner_token: pastebin_owner_token_rU3uW5Ze`).
const ownerTokenCookieName = ownerCookieName

// fmtUserTime renders t in the canonical user-facing timestamp format in the
// server's local timezone. The zero time renders as an empty string so callers
// can omit unset timestamps without a template guard.
func fmtUserTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(userTimeLayout)
}

// fmtUserDate renders t as a date-only string in the server's local timezone.
func fmtUserDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(userDateLayout)
}

// OnConfigChange is called by the ConfigManager after each successful hot-reload.
// It updates rate limiter thresholds and tracks restart-required key changes.
func (s *Server) OnConfigChange(next *config.Config) {
	if s.readLimiter != nil && next.Server.RateLimit.Read.Requests > 0 {
		s.readLimiter.UpdateLimit(next.Server.RateLimit.Read.Requests)
	}
	if s.writeLimiter != nil && next.Server.RateLimit.Write.Requests > 0 {
		s.writeLimiter.UpdateLimit(next.Server.RateLimit.Write.Requests)
	}
	if s.healthLimiter != nil && next.Server.RateLimit.Health.Requests > 0 {
		s.healthLimiter.UpdateLimit(next.Server.RateLimit.Health.Requests)
	}
	if s.burstLimiter != nil && next.Server.RateLimit.GlobalBurst > 0 {
		s.burstLimiter.UpdateLimit(next.Server.RateLimit.GlobalBurst)
	}

	// Detect restart-required changes and record them for healthz. Settings
	// are prefix-matched per AI.md restartRequiredSettings: "server.port",
	// "server.address", "server.daemonize" are exact-match; "ssl.", "database.",
	// "tor." cover their whole section. logging.* file/format and
	// server.baseurl / server.timeouts.* are also restart-required.
	prev := s.liveCfg()
	if prev.Server.Port != next.Server.Port {
		s.MarkPendingRestart("server.port")
	}
	if prev.Server.Address != next.Server.Address {
		s.MarkPendingRestart("server.address")
	}
	if prev.Server.Daemonize != next.Server.Daemonize {
		s.MarkPendingRestart("server.daemonize")
	}
	if prev.Database.Type != next.Database.Type || prev.Database.Path != next.Database.Path {
		s.MarkPendingRestart("database")
	}
	if prev.Server.Tor.Binary != next.Server.Tor.Binary ||
		prev.Server.Tor.VirtualPort != next.Server.Tor.VirtualPort ||
		prev.Server.Tor.CircuitTimeout != next.Server.Tor.CircuitTimeout ||
		prev.Server.Tor.BootstrapTimeout != next.Server.Tor.BootstrapTimeout {
		s.MarkPendingRestart("tor")
	}
	if prev.Server.TLS.Enabled != next.Server.TLS.Enabled ||
		prev.Server.TLS.Cert != next.Server.TLS.Cert ||
		prev.Server.TLS.Key != next.Server.TLS.Key ||
		prev.Server.TLS.MinVersion != next.Server.TLS.MinVersion ||
		prev.Server.TLS.LetsEncrypt.Email != next.Server.TLS.LetsEncrypt.Email ||
		prev.Server.TLS.LetsEncrypt.Enabled != next.Server.TLS.LetsEncrypt.Enabled ||
		prev.Server.TLS.LetsEncrypt.Challenge != next.Server.TLS.LetsEncrypt.Challenge ||
		prev.Server.TLS.LetsEncrypt.Staging != next.Server.TLS.LetsEncrypt.Staging ||
		prev.Server.TLS.DNSProvider != next.Server.TLS.DNSProvider ||
		prev.Server.TLS.DNSCredentialsEncrypted != next.Server.TLS.DNSCredentialsEncrypted {
		s.MarkPendingRestart("ssl")
	}
	if prev.Server.BaseURL != next.Server.BaseURL {
		s.MarkPendingRestart("server.baseurl")
	}
	if prev.Server.Limits.ReadTimeout != next.Server.Limits.ReadTimeout ||
		prev.Server.Limits.WriteTimeout != next.Server.Limits.WriteTimeout ||
		prev.Server.Limits.IdleTimeout != next.Server.Limits.IdleTimeout {
		s.MarkPendingRestart("server.timeouts")
	}
	if loggingFileOrFormatChanged(prev.Server.Logging, next.Server.Logging) {
		s.MarkPendingRestart("logging")
	}
}

// loggingFileOrFormatChanged reports whether any server.logs.* file path,
// format, or rotation setting changed. server.logs.level is intentionally
// excluded — it is hot-reloadable (AI.md "Requires Restart": logging.* file
// paths/format require the log files to be closed and reopened, but the
// level does not).
func loggingFileOrFormatChanged(prev, next config.LoggingConfig) bool {
	if prev.Access != next.Access {
		return true
	}
	if prev.Server != next.Server {
		return true
	}
	if prev.Error != next.Error {
		return true
	}
	if prev.App != next.App {
		return true
	}
	if prev.Auth != next.Auth {
		return true
	}
	if prev.Security != next.Security {
		return true
	}
	if prev.Debug != next.Debug {
		return true
	}
	if prev.Audit.Enabled != next.Audit.Enabled ||
		prev.Audit.Filename != next.Audit.Filename ||
		prev.Audit.Format != next.Audit.Format ||
		prev.Audit.Rotate != next.Audit.Rotate ||
		prev.Audit.Keep != next.Audit.Keep ||
		prev.Audit.Compress != next.Audit.Compress {
		return true
	}
	return false
}

// GeoIPEnabled returns true when the GeoIP database was successfully opened.
func (s *Server) GeoIPEnabled() bool {
	return s.geoipDB != nil
}

// UpdateGeoIP downloads fresh GeoIP databases. Safe to call from a scheduler.
func (s *Server) UpdateGeoIP() error {
	if s.geoipDB == nil {
		return nil
	}
	return s.geoipDB.Update()
}

// TorRunning returns true when the Tor hidden service is active.
func (s *Server) TorRunning() bool {
	if s.torManager == nil {
		return false
	}
	return s.torManager.Running()
}

// TorRestart restarts the Tor hidden service. It is a no-op when Tor is not
// configured; when no Tor binary is present the underlying restart is itself a
// no-op, so this never errors in that case.
func (s *Server) TorRestart() error {
	if s.torManager == nil {
		return nil
	}
	return s.torManager.Restart()
}

// TorOnionAddress returns the .onion address, or empty string if not running.
func (s *Server) TorOnionAddress() string {
	if s.torManager == nil {
		return ""
	}
	return s.torManager.OnionAddress()
}

// TorRegenerateAddress deletes the current hidden-service identity and has
// Tor generate a fresh native .onion address, returning it on success. It is
// a no-op returning an empty address when Tor is not configured.
func (s *Server) TorRegenerateAddress() (string, error) {
	if s.torManager == nil {
		return "", nil
	}
	return s.torManager.RegenerateAddress()
}

// TorApplyKeys installs new hidden-service key material (Tor's native
// on-disk secret-key format) and restarts Tor so the resulting .onion
// address becomes active, returning it on success. It is a no-op returning
// an empty address when Tor is not configured.
func (s *Server) TorApplyKeys(keyData []byte) (string, error) {
	if s.torManager == nil {
		return "", nil
	}
	return s.torManager.ApplyKeys(keyData)
}

// TorVanityStart launches the in-process vanity .onion address search. It is
// a no-op returning a zero status when Tor is not configured.
func (s *Server) TorVanityStart(prefix string, workers int) (tor.VanityStatus, error) {
	if s.torManager == nil {
		return tor.VanityStatus{}, nil
	}
	return s.torManager.VanityStart(prefix, workers)
}

// TorVanityStop cancels a running vanity search, reporting whether one was
// actually running. Candidates already written to disk are kept.
func (s *Server) TorVanityStop() bool {
	if s.torManager == nil {
		return false
	}
	return s.torManager.VanityStop()
}

// TorVanityStatus reports vanity-search progress plus the candidates waiting
// on disk.
func (s *Server) TorVanityStatus() tor.VanityStatus {
	if s.torManager == nil {
		return tor.VanityStatus{State: "idle"}
	}
	return s.torManager.VanityStatus()
}

// TorVanityApply swaps a stored vanity candidate into the live hidden-service
// site directory and restarts Tor, returning the published address.
func (s *Server) TorVanityApply(address string) (string, error) {
	if s.torManager == nil {
		return "", nil
	}
	return s.torManager.VanityApply(address)
}

// TorImportKeyPath installs hidden-service key material from a path, reusing
// the same swap path a found vanity candidate takes.
func (s *Server) TorImportKeyPath(p string) (string, error) {
	if s.torManager == nil {
		return "", nil
	}
	return s.torManager.ImportKeyPath(p)
}

// I2PRunning reports whether the I2P eepsite is currently running.
func (s *Server) I2PRunning() bool {
	if s.i2pManager == nil {
		return false
	}
	return s.i2pManager.Running()
}

// I2PRestart restarts the I2P eepsite. No-op when I2P is not configured.
func (s *Server) I2PRestart() error {
	if s.i2pManager == nil {
		return nil
	}
	return s.i2pManager.Restart()
}

// I2PAddress returns the .b32.i2p address, or empty string if not running.
func (s *Server) I2PAddress() string {
	if s.i2pManager == nil {
		return ""
	}
	return s.i2pManager.Address()
}

// maybeReadRateLimit wraps h with the read (GET/HEAD) rate limiter if enabled (PART 12).
func (s *Server) maybeReadRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.readLimiter == nil {
		return h
	}
	mw := rateLimitMiddleware(s.readLimiter, "read")
	return mw(h).ServeHTTP
}

// maybeRateLimit wraps h with the write (POST/PUT/PATCH/DELETE) rate limiter if enabled (PART 12).
func (s *Server) maybeRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.writeLimiter == nil {
		return h
	}
	mw := rateLimitMiddleware(s.writeLimiter, "write")
	return mw(h).ServeHTTP
}

// maybeStrictRateLimit protects login-like operations that disclose or replace
// owner credentials, independently of the general rate-limit setting.
func (s *Server) maybeStrictRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.strictLimiter == nil {
		return h
	}
	return rateLimitMiddleware(s.strictLimiter, "strict")(h).ServeHTTP
}

// maybeDeleteRateLimit wraps h with the write rate limiter (DELETE is a write operation, PART 12).
func (s *Server) maybeDeleteRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.writeLimiter == nil {
		return h
	}
	mw := rateLimitMiddleware(s.writeLimiter, "write")
	return mw(h).ServeHTTP
}

// maybeHealthRateLimit wraps h with the health endpoint rate limiter if enabled (PART 12).
func (s *Server) maybeHealthRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.healthLimiter == nil {
		return h
	}
	mw := rateLimitMiddleware(s.healthLimiter, "health")
	return mw(h).ServeHTTP
}

func (s *Server) setupRoutes() {
	r := s.router

	// Themed 404 / 405 handlers (PART 16) — never emit chi's plain text responses.
	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		s.renderErrorPage(w, req, http.StatusNotFound, "The requested resource was not found.")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, req *http.Request) {
		s.renderErrorPage(w, req, http.StatusMethodNotAllowed, "That method is not allowed for this resource.")
	})

	// Canonical middleware execution order (AI.md:7156-7184). Numbered steps
	// map 1:1 onto the spec's ten-step chain; the unnumbered entries are
	// pre-chain plumbing (real-IP resolution, panic recovery) and project
	// additions attached to the step they belong with.
	//
	// RealIP (custom) — extract real client IP from trusted X-Forwarded-For /
	//   CF-Connecting-IP / True-Client-IP / X-Client-IP headers (PART 12)
	// Recoverer (chi) — panic recovery
	//  1. URLNormalize — trailing slash redirect (file-extension paths exempt)
	//  2. RequestID — attach/propagate the trace ID before anything can log
	//  3. PathSecurity — block path traversal, normalize double slashes
	//  4. SecurityHeaders (+ SecFetch, CSRF, CORS) — add response headers
	//  5. Allowlist — flag IPs that bypass blocklist/rate-limit/geoip
	//  6. Blocklist — reject blocked IPs (unless allowlisted)
	//  7. RateLimit — global burst ceiling (per-class quotas are per-route)
	//  8. GeoIP — country blocking (honours allowlist flag)
	//  9. Auth — resolve presented credentials into the request context
	// 10. Logging — access log, metrics, request counters
	r.Use(s.realIPMiddleware)
	r.Use(s.domainObserveMiddleware)
	r.Use(s.recoverer)
	r.Use(middleware.CleanPath)
	r.Use(s.noTrailingSlash)
	r.Use(s.requestIDMiddleware)
	r.Use(s.authFailureFloorMiddleware)
	r.Use(s.pathSecurityMiddleware)
	r.Use(s.securityHeadersMiddleware)
	r.Use(s.secFetchMiddleware)
	r.Use(s.csrfMiddleware)
	r.Use(s.corsMiddleware)
	// Subdomain compat-target route gating (not part of the numbered PART 5/12
	// middleware chain — project-specific route shaping). Runs after
	// CSRF/CORS/security-headers/path-security (still applied uniformly) and
	// before allowlist/blocklist/rate-limit/geoip/auth, so a gated route 404s
	// cleanly without those later checks running against it.
	r.Use(s.compatModeGate)
	r.Use(s.allowlistMiddleware)
	r.Use(s.blocklistMiddleware)
	r.Use(s.abuseMiddleware)
	// Step 7 — global burst ceiling across every endpoint class (AI.md:16187).
	// Tighter per-class read/write/health quotas stay attached to their routes
	// via maybeReadRateLimit / maybeRateLimit / maybeHealthRateLimit.
	if s.burstLimiter != nil {
		r.Use(rateLimitMiddleware(s.burstLimiter, "global_burst"))
	}
	if s.geoipDB != nil {
		r.Use(s.geoipDB.Middleware())
	}
	// Step 9 — authentication: resolve any presented token into the request
	// context. Per-route handlers perform authorization against that result.
	r.Use(s.authMiddleware)
	r.Use(s.ownerCookieMiddleware)
	r.Use(s.accessLogMiddleware)
	r.Use(s.countRequests)
	r.Use(s.metricsCollector.Middleware())
	// .txt extension middleware for API routes (PART 14): strips ".txt" suffix from /api/
	// paths so the router matches the canonical route, while preserving the original
	// URL in the request so GetAPIResponseFormat can detect the text-format intent.
	r.Use(s.txtExtensionMiddleware)

	// Response compression (PART 12): level and MIME types come from
	// server.compression; skipped entirely when enabled is false.
	if comp := s.cfg.Server.Compression; comp.Enabled {
		r.Use(middleware.Compress(comp.Level, comp.Types...))
	}

	// Maintenance mode (PART 20) — after metrics/logging so blocked requests are
	// still recorded; rejects write methods with HTTP 503 while healthz/metrics and
	// all read operations continue to pass through.
	r.Use(s.maintenanceMiddleware)

	// Persist ?lang= as the lang cookie on ordinary navigation (AI.md
	// Client-Side Preferences), not just via /server/preferences/import.
	r.Use(s.langCookieMiddleware)
	registerE2ETestRoutes(r)

	// ── Static assets & PWA ──────────────────────────────────────────────────
	// common.css / components.css / public.css are rendered server-side from
	// the canonical ThemePalette (src/common/theme/colors.go) rather than
	// served as static files — see theme_css.go. chi matches these exact
	// routes before the /static/* wildcard below (AI.md 24290-24355,
	// PART 16 §23602-23605, §23619-23620). Load order (common -> components
	// -> public) is enforced by <link> order in partial/head.tmpl.
	r.Get("/static/css/common.css", s.handleCSS("common"))
	r.Get("/static/css/components.css", s.handleCSS("components"))
	r.Get("/static/css/public.css", s.handleCSS("public"))
	staticSub, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", staticCacheHeaders(http.StripPrefix("/static/", http.FileServer(http.FS(staticSub)))))
	r.Get("/manifest.json", s.handleManifest)
	r.Get("/sw.js", s.handleServiceWorker)
	r.Get("/robots.txt", s.handleRobots)
	r.Get("/sitemap.xml", s.handleSitemap)
	// AI-agent discovery file (PART 13/14) — canonical well-known path plus the
	// bare /llms.txt alias; both serve the same generated document.
	r.Get("/.well-known/llms.txt", s.handleLLMs)
	r.Get("/llms.txt", s.handleLLMs)
	for _, size := range []int{72, 96, 128, 144, 152, 180, 192, 384, 512} {
		r.Get(fmt.Sprintf("/static/icons/icon-%d.png", size), s.handlePWAIcon(size))
	}
	for _, size := range []int{192, 512} {
		r.Get(fmt.Sprintf("/static/icons/icon-maskable-%d.png", size), s.handlePWAIconMaskable(size))
	}
	// RFC 9116: canonical path is /.well-known/security.txt only. Bare
	// /security.txt is intentionally NOT registered and returns 404 (PART 13).
	r.Get("/.well-known/security.txt", s.handleSecurity)
	// Coordinated-disclosure PGP public key — 404 until a keypair is generated
	// and publishing is enabled (PART 11, AI.md 14156).
	r.Get("/.well-known/pgp-key.asc", s.handlePGPKey)
	r.Get("/favicon.ico", s.handleFavicon)
	// Branding images (AI.md PART 16 "Image Sources"/"Image Scaling") — cached,
	// pre-scaled PNGs resolved from server.branding.favicon/.logo/
	// server.seo.og_image (local file, remote URL, or embedded default).
	for _, size := range faviconSizes {
		name := fmt.Sprintf("%d", size)
		r.Get(fmt.Sprintf("/static/branding/favicon-%s.png", name), s.handleBrandingFavicon(name))
	}
	for _, width := range logoWidths {
		name := "original"
		if width > 0 {
			name = fmt.Sprintf("%d", width)
		}
		r.Get(fmt.Sprintf("/static/branding/logo-%s.png", name), s.handleBrandingLogo(name))
	}
	r.Get("/static/branding/og-image.png", s.handleBrandingOGImage)

	// ── Metrics endpoint ─────────────────────────────────────────────────────
	if s.cfg.Server.Metrics.Enabled {
		endpoint := s.cfg.Server.Metrics.Endpoint
		if endpoint == "" {
			endpoint = "/metrics"
		}
		r.With(s.metricsIPAllowlistMiddleware).Handle(endpoint, s.metricsCollector.Handler())

		// PART 20: the bare /metrics root alias is gated on
		// server.metrics.root.enabled (default true, since Prometheus scrapers
		// default to /metrics). It serves the SAME handler and is never a
		// redirect — redirects break scrapers. Skipped when the configured
		// endpoint already is /metrics, since that registration covers it.
		if s.cfg.Server.Metrics.Root.Enabled && endpoint != "/metrics" {
			r.With(s.metricsIPAllowlistMiddleware).Handle("/metrics", s.metricsCollector.Handler())
			// Per-service subpaths on the root alias too, so an operator can
			// point grafana or loki at /metrics/grafana. Only mounted when the
			// bare path was mounted above, so the two never collide.
			s.mountMetricsServices(r, "/metrics", false)
		}

		// PART 20: the canonical /server/metrics route tree. The bare path is
		// the prometheus service; /{service} selects prometheus, grafana, or
		// loki, each behind its own bearer token. An unknown service is 404
		// rather than silently falling back to prometheus, so a typo never
		// serves the wrong payload.
		s.mountMetricsServices(r, "/server/metrics", true)
	}

	// ── Tor CLI control channel (AI.md PART 31.1) ───────────────────────────
	// Internal-only, loopback-gated: never in OpenAPI, GraphQL, well-known, or
	// FeaturesInfo, and never reachable through /api/{api_version}/**. Lets a
	// separately-invoked "{project_name} tor ..." CLI subcommand reach the
	// server's already-running embedded Tor process.
	r.Route("/server/tor", func(tr chi.Router) {
		tr.Use(torControlLoopbackMiddleware)
		tr.Get("/status", s.handleTorControlStatus)
		tr.Post("/validate", s.handleTorControlValidate)
		tr.Post("/restart", s.handleTorControlRestart)
		tr.Post("/regenerate", s.handleTorControlRegenerate)
		tr.Post("/vanity/start", s.handleTorControlVanityStart)
		tr.Post("/vanity/stop", s.handleTorControlVanityStop)
		tr.Post("/vanity/apply", s.handleTorControlVanityApply)
		tr.Post("/import-keys", s.handleTorControlImportKeys)
	})

	// ── Server info pages ────────────────────────────────────────────────────
	// Bare /server redirects to the About page (AI.md 26044, 301).
	r.Get("/server", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/server/about", http.StatusMovedPermanently)
	})
	r.Get("/server/about", s.handleAbout)
	r.Get("/server/help", s.handleHelp)
	r.Get("/server/privacy", s.handlePrivacy)
	// CCPA "Do Not Sell" opt-out toggle — sets/clears the ccpa_opt_out cookie
	// (PART 31). Only meaningful when server.privacy.data.sold is true.
	r.Post("/server/privacy/ccpa", s.handleCCPAOptOut)
	// Cookie-consent endpoint — persists the `cookie_consent` cookie server-side
	// and redirects back so the no-JS <noscript> banner/preferences form works
	// (AI.md 25598), not just the JS-driven banner in app.js.
	r.Post("/server/consent", s.handleConsentSet)
	// Site Banner dismissal endpoint — appends the submitted announcement id
	// to the dismissed_announcements cookie and redirects back, so the no-JS
	// <form method="post"> dismissal always works (AI.md 22529, 22541-22555).
	r.Post("/announcements/dismiss", s.handleAnnouncementDismiss)
	// Theme preference endpoint — persists the `theme` cookie and redirects back
	// so the no-JS <noscript> toggle form works (AI.md 21588, 22641, 23294, 24084).
	// Canonical route is POST /server/preferences (AI.md "Theme Toggle" HTML
	// Structure example, line 22641) — not a standalone /theme path.
	r.Post("/server/preferences", s.handlePreferencesSet)
	r.Post("/server/preferences/token/restore", s.maybeStrictRateLimit(s.handleOwnerTokenRestore))
	r.Post("/server/preferences/token/reveal", s.maybeStrictRateLimit(s.handleOwnerTokenRevealWeb))
	r.Post("/server/preferences/token/rotate", s.maybeStrictRateLimit(s.handleOwnerTokenRotateWeb))
	r.Post("/server/preferences/tokens", s.maybeRateLimit(s.handleLinkedTokenCreate))
	r.Post("/server/preferences/tokens/{id}/reveal", s.maybeStrictRateLimit(s.handleLinkedTokenReveal))
	r.Post("/server/preferences/tokens/{id}/rotate", s.maybeStrictRateLimit(s.handleLinkedTokenRotate))
	r.Post("/server/preferences/tokens/{id}/update", s.maybeRateLimit(s.handleLinkedTokenUpdate))
	r.Post("/server/preferences/tokens/{id}/revoke", s.maybeRateLimit(s.handleLinkedTokenRevoke))
	r.Get("/server/terms", s.handleTerms)
	// Cross-device preference sync (AI.md 23503-23513): stateless export/import
	// of the theme/lang cookies — no account, no preferences table. Same
	// handlers are mounted again under /api/{api_version}/server/preferences*
	// below; each already content-negotiates its own response.
	r.Get("/server/preferences", s.handlePreferences)
	r.Get("/server/preferences/export", s.handlePreferencesExport)
	r.Get("/server/preferences/import", s.handlePreferencesImport)
	r.Get("/server/contact", s.handleContact)
	r.Post("/server/contact", s.handleContactPost)
	// Coordinated-disclosure public pages (PART 11, AI.md 14157-14161).
	// Security overview page — human-readable security.txt (AI.md 14474).
	r.Get("/server/security", s.handleSecurityOverview)
	r.Get("/server/security/policy", s.handleSecurityPolicy)
	r.Get("/server/security/thanks", s.handleSecurityThanks)
	r.Get("/server/security/report/{tracking_id}", s.handleSecurityReportStatus)
	r.Get("/server/healthz", s.maybeHealthRateLimit(s.handleHealthz))
	// /healthz root alias — only when server.healthz.root.enabled: true (PART 13).
	if s.liveCfg().Server.Healthz.Root.Enabled {
		r.Get("/healthz", s.maybeHealthRateLimit(s.handleHealthz))
	}
	// PWA offline fallback page — referenced by service worker cache (PART 16 "Manifest Configuration")
	r.Get("/offline.html", s.handleOffline)

	// ── Debug endpoints (only when --debug flag is active) (PART 6) ──────────
	if mode.ShouldShowDebugEndpoints() {
		r.Mount("/debug/pprof", http.DefaultServeMux)
		r.Get("/debug/vars", expvar.Handler().ServeHTTP)
		s.registerDebugRoutes(r)
		log.Printf("debug: /debug/pprof, /debug/vars, /debug/{config,routes,cache,db,scheduler,memory,goroutines} endpoints enabled")
	}

	// ── Auth stubs (no user accounts — redirect to home) ─────────────────────
	for _, path := range []string{
		"/login", "/register", "/logout", "/settings",
		"/server/auth/login", "/server/auth/register",
		"/server/auth/logout", "/server/auth/settings",
	} {
		r.Get(path, handler.AuthStubRedirect)
		r.Post(path, handler.AuthStubRedirect)
	}
	// microbin auth-gate redirects
	// user profiles → home
	r.Get("/u/{username}", handler.AuthStubRedirect)
	r.Get("/auth/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+chi.URLParam(r, "id"), http.StatusFound)
	})
	r.Get("/auth_raw/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/raw/"+chi.URLParam(r, "id"), http.StatusFound)
	})
	r.Get("/auth_remove_private/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/remove/"+chi.URLParam(r, "id"), http.StatusFound)
	})

	// ── pastebin.com API compatibility ───────────────────────────────────────
	r.Post("/api/api_post.php", s.maybeRateLimit(s.compatHandler.PastebinPost))
	r.Get("/api/api_raw.php", s.maybeReadRateLimit(s.compatHandler.PastebinRaw))
	r.Post("/api/api_login.php", s.compatHandler.PastebinLogin)

	// ── lenpaste API compatibility ───────────────────────────────────────────
	r.Post("/api/new", s.maybeRateLimit(s.compatHandler.LenCreate))
	r.Get("/api/get", s.maybeReadRateLimit(s.compatHandler.LenGet))
	r.Delete("/api/remove", s.maybeDeleteRateLimit(s.compatHandler.LenRemove))
	// some clients use GET
	r.Get("/api/remove", s.maybeDeleteRateLimit(s.compatHandler.LenRemove))
	r.Get("/api/list", s.maybeReadRateLimit(s.compatHandler.LenList))

	// ── stikked API compatibility ────────────────────────────────────────────
	r.Post("/api/create", s.maybeRateLimit(s.compatHandler.StikkedCreate))
	r.Get("/api/paste/{id}", s.maybeReadRateLimit(s.compatHandler.StikkedJSON))
	r.Get("/view/raw/{id}", s.maybeReadRateLimit(s.pasteHandler.GetRawPaste))
	// /view/{id} always renders the normal paste-detail page — unlike the root
	// /{id} route, it never redirects to the target URL for link pastes.
	r.Get("/view/{id}", s.maybeReadRateLimit(func(w http.ResponseWriter, r *http.Request) {
		s.renderPasteView(w, r, true)
	}))

	// ── hastebin / haste-server compatibility ────────────────────────────────
	r.Post("/documents", s.maybeRateLimit(s.compatHandler.HastebinCreate))
	r.Get("/documents/{id}", s.maybeReadRateLimit(s.compatHandler.HastebinGet))

	// ── dpaste compatibility ─────────────────────────────────────────────────
	r.Post("/api/", s.maybeRateLimit(s.compatHandler.DpasteCreate))
	r.Post("/api", s.maybeRateLimit(s.compatHandler.DpasteCreate))
	r.Post("/api/v2/", s.maybeRateLimit(s.compatHandler.DpasteCreate))
	r.Post("/api/v2", s.maybeRateLimit(s.compatHandler.DpasteCreate))

	// ── Versioned API (native) ───────────────────────────────────────────────
	r.Get("/api", s.handleAPIInfo)

	r.Route("/api/"+s.apiVersion(), func(r chi.Router) {
		r.Get("/", s.handleAPIInfo)

		// Native REST API (PART 14): plural resource routes only; no singular forms.
		r.Route("/pastes", func(r chi.Router) {
			r.Get("/", s.maybeReadRateLimit(s.pasteHandler.ListPastes))
			r.Post("/", s.maybeRateLimit(s.pasteHandler.CreatePaste))
			r.Get("/{id}", s.maybeReadRateLimit(s.pasteHandler.GetPaste))
			r.Delete("/{id}", s.maybeDeleteRateLimit(s.pasteHandler.DeletePaste))
			r.Get("/{id}/raw", s.maybeReadRateLimit(s.pasteHandler.GetRawPaste))
		})

		// microbin-style /pasta alias
		r.Get("/pasta", s.maybeReadRateLimit(s.compatHandler.MicrobinList))
		r.Post("/pasta", s.maybeRateLimit(s.compatHandler.MicrobinCreate))
		r.Get("/pasta/{id}", s.maybeReadRateLimit(s.compatHandler.MicrobinGet))
		r.Delete("/pasta/{id}", s.maybeDeleteRateLimit(s.compatHandler.MicrobinDelete))

		// lenpaste v1 versioned aliases
		r.Post("/new", s.compatHandler.LenCreate)
		r.Get("/get", s.maybeReadRateLimit(s.compatHandler.LenGet))
		r.Get("/getServerInfo", s.maybeReadRateLimit(s.compatHandler.LenServerInfo))

		// Server info
		r.Get("/server/healthz", s.maybeHealthRateLimit(s.handleHealthzJSON))
		r.Get("/server/version", s.handleVersion)
		// Cross-device preference sync API mirror (AI.md 23509-23512) — same
		// handlers as the web routes above; each content-negotiates JSON here.
		r.Get("/server/preferences", s.handlePreferences)
		r.Post("/server/preferences", s.maybeRateLimit(s.handlePreferencesSetAPI))
		r.Get("/server/preferences/tokens", s.handleOwnerTokensAPI)
		r.Post("/server/preferences/tokens/primary/reveal", s.maybeStrictRateLimit(s.handleOwnerTokenRevealAPI))
		r.Post("/server/preferences/tokens/primary/rotate", s.maybeStrictRateLimit(s.handleOwnerTokenRotateAPI))
		r.Post("/server/preferences/tokens", s.maybeRateLimit(s.handleLinkedTokenCreate))
		r.Post("/server/preferences/tokens/{id}/reveal", s.maybeStrictRateLimit(s.handleLinkedTokenReveal))
		r.Post("/server/preferences/tokens/{id}/rotate", s.maybeStrictRateLimit(s.handleLinkedTokenRotate))
		r.Patch("/server/preferences/tokens/{id}", s.maybeRateLimit(s.handleLinkedTokenUpdate))
		r.Delete("/server/preferences/tokens/{id}", s.maybeRateLimit(s.handleLinkedTokenRevoke))
		r.Get("/server/preferences/export", s.handlePreferencesExport)
		r.Get("/server/preferences/import", s.handlePreferencesImport)
		r.Get("/server/swagger", s.swaggerHandler.ServeSpec)
		r.Handle("/server/mcp", s.mcpHandler)
		r.Post("/server/reports/{name}", s.handleReports)
		// /api/v1/server/graphql — versioned GraphQL endpoint (PART 14)
		r.Handle("/server/graphql", s.graphqlHandler)

		// Scheduler API (PART 18)
		// Read-only status routes are public. Mutating routes require server.token.
		// Plural route name per PART 14 "Route Naming Convention" (always plural, no exceptions).
		r.Route("/schedulers", func(r chi.Router) {
			r.Get("/", s.handleSchedulerList)
			r.Get("/{id}", s.handleSchedulerShow)
			r.Get("/{id}/history", s.handleSchedulerHistory)
			r.With(s.requireOperatorToken).Post("/{id}/run", s.handleSchedulerRun)
			r.With(s.requireOperatorToken).Post("/{id}/enable", s.handleSchedulerEnable)
			r.With(s.requireOperatorToken).Post("/{id}/disable", s.handleSchedulerDisable)
		})

		// PART 20: the metrics tree also answers under the versioned API path,
		// invoking the same handlers with the same per-service tokens.
		if s.cfg.Server.Metrics.Enabled {
			s.mountMetricsServices(r, "/server/metrics", true)
		}
	})

	// Unversioned aliases — same handler as versioned, served directly (no redirect) per PART 14.
	r.Get("/api/swagger", s.swaggerHandler.ServeSpec)
	r.Handle("/api/graphql", s.graphqlHandler)
	// MCP: the same handler at all three paths, never a redirect (PART 11).
	r.Handle("/api/mcp", s.mcpHandler)
	r.Handle("/mcp", s.mcpHandler)
	r.Get("/api/healthz", s.maybeHealthRateLimit(s.handleHealthzJSON))
	// PART 20: unversioned machine-friendly metrics alias, same handlers.
	if s.cfg.Server.Metrics.Enabled {
		s.mountMetricsServices(r, "/api/metrics", true)
	}
	// autodiscover is non-versioned by design (PART 32/14): clients use it before knowing the version.
	r.Get("/api/autodiscover", s.handleAutodiscover)

	// ── Web: main pages ──────────────────────────────────────────────────────
	r.Get("/", s.handleHome)
	// POST / — curl-upload family dispatcher (sprunge/0x0/ix.io); falls through
	// to the native create handler (lenpaste form-POST to root) when no
	// curl-upload field is present.
	r.Post("/", s.maybeRateLimit(s.compatHandler.RootUpload))
	// Canonical web resource routes (PART 16 dual-route table): GET /pastes is
	// the list page and POST /pastes is the no-JS HTML create form, mirroring
	// GET/POST /api/v1/pastes. /create below is a compatibility alias.
	r.Get("/pastes", s.handleRecent)
	r.Post("/pastes", s.maybeRateLimit(s.handleWebCreate))
	r.Get("/recent", s.handleRecent)
	// microbin alias
	r.Get("/list", s.handleRecent)
	// pastebin.com alias
	r.Get("/archive", s.handleRecent)
	// pastebin.com alias
	r.Get("/trends", s.handleRecent)
	// Compatibility aliases for the canonical /pastes create route.
	r.Post("/create", s.maybeRateLimit(s.handleWebCreate))
	r.Get("/create", s.handleCreatePage)

	// ── Redirect aliases ────────────────────────────────────────────────────
	r.Get("/guide", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/server/help", http.StatusFound)
	})
	r.Get("/emb_help", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/server/help", http.StatusFound)
	})
	r.Get("/about", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/server/about", http.StatusFound)
	})

	// ── Web: paste views ─────────────────────────────────────────────────────
	r.Get("/raw/{id}", s.maybeReadRateLimit(s.pasteHandler.GetRawPaste))
	r.Get("/r/{id}", s.maybeReadRateLimit(s.pasteHandler.GetRawPaste))
	r.Get("/dl/{id}", s.maybeReadRateLimit(s.handleDownload))
	r.Get("/download/{id}", s.maybeReadRateLimit(s.handleDownload))
	// microbin alias
	r.Get("/file/{id}", s.maybeReadRateLimit(s.handleDownload))
	r.Get("/emb/{id}", s.maybeReadRateLimit(s.handleEmbed))
	r.Get("/qr/{id}", s.maybeReadRateLimit(s.handleQR))
	r.Get("/qr/{id}/image", s.maybeReadRateLimit(s.handleQRImage))
	r.Get("/remove/{id}", s.handleRemovePage)
	r.Post("/remove/{id}", s.maybeDeleteRateLimit(s.handleRemoveSubmit))
	// microbin upload
	r.Post("/upload", s.maybeRateLimit(s.pasteHandler.CreatePaste))
	// microbin upload alias
	r.Get("/upload/{id}", s.maybeReadRateLimit(s.handleViewPaste))
	// microbin short URL
	r.Get("/p/{id}", s.maybeReadRateLimit(s.handleViewPaste))
	// pastebin.com /id/raw
	r.Get("/{id}/raw", s.maybeReadRateLimit(s.pasteHandler.GetRawPaste))
	// microbin URL-paste redirect
	r.Get("/url/{id}", s.maybeReadRateLimit(s.handleURLRedirect))
	// microbin short URL redirect
	r.Get("/u/{id}", s.maybeReadRateLimit(s.handleURLRedirect))

	// Swagger UI (human-readable docs page)
	r.Get("/server/swagger", s.swaggerHandler.ServeUI)
	r.Get("/server/docs/swagger", s.swaggerHandler.ServeUI)
	r.Get("/server/docs/graphql", s.graphqlHandler.ServeHTTP)

	// ── Paste view — catch-all (must be last) ────────────────────────────────
	r.Get("/{id}", s.maybeReadRateLimit(s.handleViewPaste))
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// requireOperatorToken is middleware that enforces server.token authentication.
// It reads the credential resolved by authMiddleware (execution step 9, which
// honours every PART 8 auth header in priority order), SHA-256 hashes it, and
// compares against the cached hash using constant-time comparison (PART 11).
// Returns 401 on missing/invalid credentials — always with the same generic message
// to prevent user enumeration.
func (s *Server) requireOperatorToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		incoming, source := TokenFromContext(r.Context())
		if incoming == "" {
			// authMiddleware may be bypassed in tests or sub-routers mounted
			// without the global chain; fall back to direct extraction so the
			// authorization decision never depends on middleware ordering.
			incoming, source = extractRequestToken(r)
		}
		if source == "" {
			source = "bearer"
		}
		if incoming == "" {
			metric.AuthAttemptsTotal.WithLabelValues(source, "failure").Inc()
			s.authLog(r, "operator", "fail", "missing_bearer_token")
			w.Header().Set("WWW-Authenticate", `Bearer realm="pastebin"`)
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"ok": false, "error": "UNAUTHORIZED", "message": "operator token required",
			})
			return
		}
		incomingHash := sha256.Sum256([]byte(incoming))
		var zeroHash [32]byte
		if s.operatorTokenHash == zeroHash {
			metric.AuthAttemptsTotal.WithLabelValues(source, "failure").Inc()
			s.authLog(r, "operator", "fail", "token_not_configured")
			writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
				"ok": false, "error": "SERVER_ERROR", "message": "server.token not configured",
			})
			return
		}
		if subtle.ConstantTimeCompare(incomingHash[:], s.operatorTokenHash[:]) != 1 {
			metric.AuthAttemptsTotal.WithLabelValues(source, "failure").Inc()
			s.authLog(r, "operator", "fail", "invalid_token")
			w.Header().Set("WWW-Authenticate", `Bearer realm="pastebin"`)
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
				"ok": false, "error": "UNAUTHORIZED", "message": "operator token required",
			})
			return
		}
		metric.AuthAttemptsTotal.WithLabelValues(source, "success").Inc()
		s.authLog(r, "operator", "success", "")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.stats.inc()
		s.stats.activeConn.Add(1)
		defer s.stats.activeConn.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// txtExtensionMiddleware strips ".txt" from API route paths so chi can match
// the canonical route (e.g., /api/v1/pastes.txt → /api/v1/pastes). It sets
// the httputil txt-extension flag in context so GetAPIResponseFormat detects
// that text output was requested even after the suffix is removed.
func (s *Server) txtExtensionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && strings.HasSuffix(r.URL.Path, ".txt") {
			// Strip .txt before routing so chi matches the canonical route path.
			stripped := *r.URL
			stripped.Path = strings.TrimSuffix(r.URL.Path, ".txt")
			stripped.RawPath = ""
			r2 := httputil.WithTxtExtension(r)
			r2.URL = &stripped
			// middleware.CleanPath (registered earlier in the chain) already
			// cached the pre-strip path onto the route context's RoutePath;
			// chi's router reads that cached value instead of r.URL.Path, so
			// it must be updated here too or routing still 404s on the
			// original .txt-suffixed path.
			if rctx := chi.RouteContext(r2.Context()); rctx != nil {
				rctx.RoutePath = stripped.Path
			}
			next.ServeHTTP(w, r2)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// corsAllowedHeaders is the full set of headers PART 16 requires the
// Access-Control-Allow-Headers response to name explicitly.  A wildcard is
// never used here: it does not cover Authorization and is invalid whenever
// credentials are allowed, so every supported auth header must be listed by
// name (PART 8 "Auth Token Headers (All Headers Supported)").
const corsAllowedHeaders = "Content-Type, Accept, X-Requested-With, Authorization, X-API-Key, X-Api-Key, API-Key, ApiKey, X-Auth-Token, X-Access-Token, X-Token, Token, X-CSRF-Token, X-XSRF-Token, X-Session-ID, X-Service-Token, X-Internal-Token"

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, allowCredentials, disabled := s.resolveCORSOrigin(r)
		if !disabled {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", corsAllowedHeaders)
			// Credentials are only ever advertised for a specific resolved
			// origin — never alongside "*" (PART 16 CORS behavior table).
			if allowCredentials && origin != "*" {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Max-Age", strconv.Itoa(s.liveCfg().Server.Cors.MaxAge))
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// resolveCORSAllowList resolves the effective CORS allow-list per PART 16's
// resolution order:
//  1. Explicit config (server.cors.allowed_origins). A single "" entry
//     disables CORS headers entirely and stops resolution.
//  2. DOMAIN env entries (comma-separated; first is primary for FQDN
//     purposes, but every hostname becomes an allowed https:// origin here).
//  3. Reverse-proxy-learned hosts (X-Forwarded-Host observed from trusted
//     proxies only — see domainLearner, gated by isTrustedPeer).
//  4. Default: ["*"] (credentials never allowed with this fallback).
func (s *Server) resolveCORSAllowList() (list []string, disabled bool) {
	cfg := s.liveCfg().Server.Cors
	if len(cfg.AllowedOrigins) == 1 && cfg.AllowedOrigins[0] == "" {
		return nil, true
	}
	if len(cfg.AllowedOrigins) > 0 {
		return cfg.AllowedOrigins, false
	}

	var resolved []string
	if v := os.Getenv("DOMAIN"); v != "" {
		for _, h := range strings.Split(v, ",") {
			h = strings.TrimSpace(h)
			if h != "" {
				resolved = append(resolved, "https://"+h)
			}
		}
	}
	if s.domainLearner != nil {
		resolved = append(resolved, s.domainLearner.CORSOrigins()...)
	}
	if len(resolved) == 0 {
		return []string{"*"}, false
	}
	return resolved, false
}

// corsOriginMatches reports whether a resolved allow-list entry matches an
// actual request Origin. Entries in the scheme://*.base form (as produced by
// domainLearner.CORSOrigins) match the base host and any subdomain of it;
// all other entries must match exactly.
func corsOriginMatches(allowed, origin string) bool {
	if allowed == origin {
		return true
	}
	scheme, wildcardHost, ok := strings.Cut(allowed, "://*.")
	if !ok {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Scheme == scheme && (u.Hostname() == wildcardHost || strings.HasSuffix(u.Hostname(), "."+wildcardHost))
}

// resolveCORSOrigin returns the Access-Control-Allow-Origin value for the
// request, whether Access-Control-Allow-Credentials should be sent, and
// whether CORS headers should be suppressed entirely (operator disabled CORS
// via a single "" allowed_origins entry).
func (s *Server) resolveCORSOrigin(r *http.Request) (origin string, allowCredentials bool, disabled bool) {
	// Tor requests always answer with the onion origin — the onion address is
	// auto-added to the CORS allow-list and the clearnet/operator-configured
	// origin must NOT appear in Tor response headers (PART 12 Tor privacy).
	if onion := s.torRequestOnion(r); onion != "" {
		return "http://" + onion, false, false
	}

	list, disabled := s.resolveCORSAllowList()
	if disabled {
		return "", false, true
	}
	for _, allowed := range list {
		if allowed == "*" {
			return "*", false, false
		}
	}

	reqOrigin := r.Header.Get("Origin")
	if reqOrigin == "" {
		// Non-browser / same-origin request: expose the first configured
		// origin as a stable value.
		return list[0], false, false
	}
	for _, allowed := range list {
		if corsOriginMatches(allowed, reqOrigin) {
			return reqOrigin, s.liveCfg().Server.Cors.AllowCredentials, false
		}
	}
	// Origin header present but it matched nothing in the allow-list: fall
	// back to the wildcard rather than reflecting a mismatched origin, and
	// never with credentials.
	return "*", false, false
}

// permissionsPolicy is the default Permissions-Policy header value built from
// the PART 11 spec defaults at package init time.
var permissionsPolicy = strings.Join([]string{
	"accelerometer=()", "ambient-light-sensor=()", "battery=()", "camera=()",
	"display-capture=()", "geolocation=()", "gyroscope=()", "hid=()",
	"idle-detection=()", "magnetometer=()", "microphone=()", "midi=()",
	"screen-wake-lock=()", "serial=()", "usb=()", "xr-spatial-tracking=()",
	"attribution-reporting=()", "browsing-topics=()", "interest-cohort=()",
	"autoplay=(self)", "encrypted-media=(self)", "fullscreen=(self)",
	"payment=(self)", "picture-in-picture=(self)",
	"publickey-credentials-get=(self)", "storage-access=(self)", "web-share=(self)",
}, ", ")

// buildCSP returns the Content-Security-Policy header value for a request,
// using the server config's CSP settings.  When TLS is off, upgrade-insecure-requests
// is omitted.  In development mode, report-only mode is used.
func (s *Server) buildCSP(r *http.Request) (header string, reportOnly bool) {
	cfg := s.liveCfg()
	if !cfg.Web.CSP.Enabled {
		return "", false
	}

	fqdn := cfg.Server.FQDN
	reportURI := "/api/" + s.apiVersion() + "/server/reports/csp"

	csp := cfg.Web.CSP
	// script-src stays 'self' only — no 'unsafe-inline' — per the CSP spec
	// (AI.md "Content Security Policy" table): all JS lives in
	// static/js/app.js, so this blocks every injected inline <script>.
	scriptSrc := "'self'"
	if csp.ScriptSrcOverride != "" {
		scriptSrc = csp.ScriptSrcOverride
	} else if csp.ScriptSrcExtra != "" {
		scriptSrc += " " + csp.ScriptSrcExtra
	}
	styleSrc := "'self' 'unsafe-inline'"
	if csp.StyleSrcExtra != "" {
		styleSrc += " " + csp.StyleSrcExtra
	}
	imgSrc := "'self' data: blob: https:"
	if csp.ImgSrcExtra != "" {
		imgSrc += " " + csp.ImgSrcExtra
	}
	fontSrc := "'self' https:"
	if csp.FontSrcExtra != "" {
		fontSrc += " " + csp.FontSrcExtra
	}
	connectSrc := "'self'"
	if fqdn != "" && fqdn != "localhost" {
		connectSrc += " https://" + fqdn
	}
	if csp.ConnectSrcExtra != "" {
		connectSrc += " " + csp.ConnectSrcExtra
	}
	frameSrc := "'self'"
	if csp.FrameSrcExtra != "" {
		frameSrc += " " + csp.FrameSrcExtra
	}
	// /emb/{id} is the frame-ancestors allow-listed endpoint (PART 11) — it is
	// designed to be iframed by third-party sites. Everything else stays 'self'.
	frameAncestors := "'self'"
	if isEmbedPath(r.URL.Path) {
		frameAncestors = strings.TrimSpace(csp.EmbedFrameAncestors)
		if frameAncestors == "" {
			frameAncestors = "*"
		}
	}
	formAction := "'self'"
	if csp.FormActionExtra != "" {
		formAction += " " + csp.FormActionExtra
	}

	directives := []string{
		"default-src 'self'",
		"script-src " + scriptSrc,
		"style-src " + styleSrc,
		"img-src " + imgSrc,
		"font-src " + fontSrc,
		"connect-src " + connectSrc,
		"media-src 'self' blob:",
		"worker-src 'self' blob:",
		"manifest-src 'self'",
		"frame-src " + frameSrc,
		"frame-ancestors " + frameAncestors,
		"base-uri 'self'",
		"form-action " + formAction,
		"object-src 'none'",
	}

	// Only include upgrade-insecure-requests when TLS is enabled.
	if cfg.Server.TLS.Enabled {
		directives = append(directives, "upgrade-insecure-requests")
	}

	directives = append(directives,
		"report-to default",
		"report-uri "+reportURI,
	)

	policy := strings.Join(directives, "; ")
	isReportOnly := csp.Mode == "report-only" || cfg.Server.Mode == "development" || cfg.Server.Mode == "debug"
	return policy, isReportOnly
}

// buildReportingHeaders returns the Reporting-Endpoints, Report-To, and NEL header values.
func (s *Server) buildReportingHeaders() (endpoints, reportTo, nel string) {
	cfg := s.liveCfg()
	fqdn := cfg.Server.FQDN
	if fqdn == "" || fqdn == "localhost" || !cfg.Server.TLS.Enabled {
		return "", "", ""
	}
	base := "https://" + fqdn + "/api/" + s.apiVersion() + "/server/reports"
	endpoints = `default="` + base + `/default"`
	reportTo = `{"group":"default","max_age":10886400,"endpoints":[{"url":"` + base + `/default"}]}`
	nel = `{"report_to":"default","max_age":2592000,"include_subdomains":true}`
	return endpoints, reportTo, nel
}

// securityHeadersMiddleware sets all mandatory security response headers per PART 11.
func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.liveCfg()
		h := w.Header()

		// Mandatory legacy security headers (PART 11).
		h.Set("X-Content-Type-Options", "nosniff")
		// X-Frame-Options has no per-origin allow form, so the embeddable
		// /emb/{id} endpoint omits it and lets CSP frame-ancestors govern
		// framing; every other route stays SAMEORIGIN.
		if !isEmbedPath(r.URL.Path) {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		h.Set("X-XSS-Protection", "1; mode=block")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")
		h.Set("Origin-Agent-Cluster", "?1")

		// Cross-origin isolation headers — defaults keep broad compatibility (PART 11).
		h.Set("Cross-Origin-Opener-Policy", "unsafe-none")
		h.Set("Cross-Origin-Embedder-Policy", "unsafe-none")
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")

		// Content-Security-Policy.
		if policy, reportOnly := s.buildCSP(r); policy != "" {
			if reportOnly {
				h.Set("Content-Security-Policy-Report-Only", policy)
			} else {
				h.Set("Content-Security-Policy", policy)
			}
		}

		// Permissions-Policy.
		h.Set("Permissions-Policy", permissionsPolicy)

		// Strict-Transport-Security — only when TLS is active (RFC 6797).
		if cfg.Server.TLS.Enabled {
			hsts := cfg.Web.HSTS
			if hsts.Enabled {
				hstsVal := fmt.Sprintf("max-age=%d", hsts.MaxAgeSeconds)
				if hsts.IncludeSubdomains {
					hstsVal += "; includeSubDomains"
				}
				if hsts.Preload {
					hstsVal += "; preload"
				}
				h.Set("Strict-Transport-Security", hstsVal)
			}
		}

		// Reporting API (modern + legacy NEL) — only when TLS is enabled.
		if endpoints, reportTo, nel := s.buildReportingHeaders(); endpoints != "" {
			h.Set("Reporting-Endpoints", endpoints)
			h.Set("Report-To", reportTo)
			h.Set("NEL", nel)
		}

		// The X-Request-ID response header is set by requestIDMiddleware
		// (execution step 2), which always runs before this middleware.

		next.ServeHTTP(w, r)
	})
}

// mountMetricsServices registers the per-service metrics routes under base
// (PART 20): base/{prometheus,grafana,loki}, each behind that service's own
// bearer token via ServiceHandler. When withBare is true the bare base path is
// also registered as the prometheus service.
//
// Every route is wrapped in metricsIPAllowlistMiddleware, so the per-service
// paths are no more exposed than the bare /metrics path is — they are not a way
// around the allowlist.
//
// An unrecognized service name is 404 rather than falling back to prometheus,
// so a typo in a scraper config fails loudly instead of silently returning the
// wrong payload.
func (s *Server) mountMetricsServices(r chi.Router, base string, withBare bool) {
	handlerFor := func(service string) http.Handler {
		return s.metricsCollector.ServiceHandler(service)
	}

	if withBare {
		r.With(s.metricsIPAllowlistMiddleware).Handle(base, handlerFor(metric.ServicePrometheus))
	}

	r.With(s.metricsIPAllowlistMiddleware).Handle(base+"/prometheus", handlerFor(metric.ServicePrometheus))
	r.With(s.metricsIPAllowlistMiddleware).Handle(base+"/grafana", handlerFor(metric.ServiceGrafana))
	r.With(s.metricsIPAllowlistMiddleware).Handle(base+"/loki", handlerFor(metric.ServiceLoki))

	// Catch-all for the service segment so an unknown name 404s with the
	// canonical PART 14 envelope instead of chi's bare-text default.
	r.With(s.metricsIPAllowlistMiddleware).HandleFunc(base+"/{service}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":      false,
			"error":   "NOT_FOUND",
			"message": "unknown metrics service: " + chi.URLParam(r, "service"),
		})
	})
}

// metricsIPAllowlistMiddleware restricts /metrics to loopback addresses plus any
// IPs or CIDRs listed in cfg.Server.Metrics.AllowedIPs (PART 20).
// Loopback (127.0.0.1, ::1) is always permitted regardless of the configured list.
// When AllowedIPs is empty the endpoint is loopback-only; add CIDRs to permit
// additional internal networks (e.g. "10.0.0.0/8").
func (s *Server) metricsIPAllowlistMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		ip := net.ParseIP(host)
		// Always allow loopback so monitoring on the same host works without config.
		if ip != nil && ip.IsLoopback() {
			next.ServeHTTP(w, r)
			return
		}
		cfg := s.liveCfg()
		allowed := cfg.Server.Metrics.AllowedIPs
		if len(allowed) > 0 {
			al := newAllowlistSet(allowed)
			if ip != nil && al.contains(ip) {
				next.ServeHTTP(w, r)
				return
			}
		}
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"ok":      false,
			"error":   "FORBIDDEN",
			"message": "metrics access denied",
		})
	})
}

// secFetchMiddleware rejects cross-site state-changing requests per PART 11.
// Validation rules (when sec_fetch_validation=true):
//   - Reject POST/PUT/PATCH/DELETE where Sec-Fetch-Site: cross-site AND no Bearer/API token.
//   - Reject POST/PUT/PATCH/DELETE to /api/* where Sec-Fetch-Mode: navigate (form-based nav CSRF).
//   - GET/HEAD navigation to /api/* is ALLOWED (AI.md:13931): opening an API URL in a browser
//     returns the JSON normally, since GETs are side-effect-free and responses carry nosniff.
//   - Absence of Sec-Fetch-* is treated as pass-through (legacy-browser compat).
func (s *Server) secFetchMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.liveCfg()
		if !cfg.Web.Headers.SecFetchValidation {
			next.ServeHTTP(w, r)
			return
		}

		// Check Sec-Fetch-Site: cross-site on state-changing methods.
		fetchSite := r.Header.Get("Sec-Fetch-Site")

		// Sec-Fetch-Dest defense-in-depth (AI.md PART 11): reject cross-site
		// framing of any endpoint not on the frame-ancestors allow-list.
		// /emb/{id} is the only allow-listed endpoint. Same-site framing falls
		// through to X-Frame-Options/frame-ancestors response headers.
		if fetchSite == "cross-site" && !isEmbedPath(r.URL.Path) {
			switch r.Header.Get("Sec-Fetch-Dest") {
			case "iframe", "frame", "embed", "object":
				writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "FORBIDDEN", "message": "cross-site framing of this endpoint is not allowed"})
				return
			}
		}

		if fetchSite == "cross-site" {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				// Allow if Bearer or API-token auth is present — Bearer is not auto-attached
				// by browsers, so no CSRF risk.
				if r.Header.Get("Authorization") == "" && r.Header.Get("X-API-Token") == "" {
					// Check CSRF exempt paths.
					if !isCSRFExempt(r.URL.Path, cfg.Web.CSRF.ExemptPaths) {
						writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "FORBIDDEN", "message": "cross-site state-changing request blocked"})
						return
					}
				}
			}
		}

		// Reject form-based navigation CSRF on /api/* — only for state-changing methods.
		// GET/HEAD navigation is explicitly allowed (AI.md:13931): opening an API URL in a
		// browser must return the JSON normally.
		if r.Header.Get("Sec-Fetch-Mode") == "navigate" && strings.HasPrefix(r.URL.Path, "/api/") {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				writeJSON(w, http.StatusForbidden, map[string]interface{}{"ok": false, "error": "FORBIDDEN", "message": "direct navigation to API endpoint blocked"})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// reservedSlugs is the set of names that must not resolve to paste IDs (PART 16).
// These are system routes, common paths, and technical endpoints.
var reservedSlugs = map[string]struct{}{
	"api":           {},
	"server":        {},
	"static":        {},
	"assets":        {},
	"healthz":       {},
	"metrics":       {},
	"webhook":       {},
	"webhooks":      {},
	"search":        {},
	"explore":       {},
	"discover":      {},
	"trending":      {},
	"help":          {},
	"support":       {},
	"docs":          {},
	"documentation": {},
	"about":         {},
	"contact":       {},
	"terms":         {},
	"privacy":       {},
	"legal":         {},
	"security":      {},
	"graphql":       {},
	"swagger":       {},
	"rest":          {},
	"rpc":           {},
	"ws":            {},
	"websocket":     {},
	"cdn":           {},
	"media":         {},
	"uploads":       {},
	"files":         {},
	"images":        {},
	"robots.txt":    {},
	"sitemap.xml":   {},
	"llms.txt":      {},
	"mcp":           {},
	"favicon.ico":   {},
	".well-known":   {},
	"raw":           {},
	"dl":            {},
	"download":      {},
	"file":          {},
	"r":             {},
	"emb":           {},
	"qr":            {},
	"remove":        {},
	"upload":        {},
	"p":             {},
	"u":             {},
	"url":           {},
	"auth":          {},
	"recent":        {},
}

// isReservedSlug reports whether id is a reserved system name and must not
// be treated as a paste identifier.
func isReservedSlug(id string) bool {
	_, ok := reservedSlugs[strings.ToLower(id)]
	return ok
}

// csrfTokenKey is the context key under which the generated CSRF token string is stored.
type csrfTokenKeyType struct{}

var csrfTokenKey csrfTokenKeyType

// generateCSRFToken creates a new HMAC-SHA256 signed CSRF token using the server's csrfSecret.
// Format: base64(32-random-bytes) + "." + base64(HMAC of those bytes).
func (s *Server) generateCSRFToken() (string, error) {
	nonce := make([]byte, 32)
	if _, err := crand.Read(nonce); err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.csrfSecret)
	mac.Write(nonce)
	sig := mac.Sum(nil)
	token := base64.RawURLEncoding.EncodeToString(nonce) + "." + base64.RawURLEncoding.EncodeToString(sig)
	return token, nil
}

// csrfTokenFromRequest returns the CSRF token resolved by csrfMiddleware for
// this request (from context), or "" if the middleware never ran. Used by
// both HTML page-data injection and the Swagger/GraphiQL viewer resolvers so
// every no-JS form (including the theme toggle) carries a valid token.
func (s *Server) csrfTokenFromRequest(r *http.Request) string {
	if tok, ok := r.Context().Value(csrfTokenKey).(string); ok {
		return tok
	}
	return ""
}

// validateCSRFToken reports whether token is a valid HMAC-signed CSRF token (constant-time).
func (s *Server) validateCSRFToken(token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(nonce) != 32 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, s.csrfSecret)
	mac.Write(nonce)
	expected := mac.Sum(nil)
	return subtle.ConstantTimeCompare(sig, expected) == 1
}

// csrfMiddleware implements the double-submit CSRF protection pattern (PART 11,
// AI.md "CSRF Protection"). The token cookie is stable across requests (reused
// while valid, re-minted only when absent or invalid) so tokens embedded in
// already-rendered forms keep working. Validation runs ONLY when the request is
// state-mutating AND carries a CSRF cookie (i.e. it is a browser that loaded a
// page from us) AND is from a cross-site/unknown origin. Bearer/API-token,
// read-only, WebSocket-upgrade, same-origin, exempt-path, and cookie-less
// (non-browser: CLI, compat, webhook) requests are bypassed — those callers
// carry no auto-attached credential, so there is nothing to forge.
func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.liveCfg()
		if !cfg.Web.CSRF.Enabled || len(s.csrfSecret) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		// Inspect the request's existing CSRF cookie before issuing a new one.
		reqCookie, cookieErr := r.Cookie(cfg.Web.CSRF.CookieName)
		hasCookie := cookieErr == nil && reqCookie.Value != ""

		// Reuse a valid existing token; mint a fresh one only when absent/invalid.
		token := ""
		if hasCookie && s.validateCSRFToken(reqCookie.Value) {
			token = reqCookie.Value
		}
		if token == "" {
			t, err := s.generateCSRFToken()
			if err != nil {
				// Fail open — log and continue without setting the cookie.
				log.Printf("csrf: token generation failed: %v", err)
				next.ServeHTTP(w, r)
				return
			}
			token = t
		}

		// Validate the token if and only if ALL hold (AI.md "When CSRF
		// Validation Runs"): mutating method, cookie-authenticated, cross-site.
		isMutating := r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodPatch || r.Method == http.MethodDelete

		// Bypass conditions (any one is sufficient). Exactly the AI.md PART 11
		// bypass list — non-mutating methods, Bearer credentials, WebSocket
		// upgrades, and operator-declared exempt paths. A missing CSRF cookie
		// is NOT a bypass: a mutating request without Bearer must present a
		// valid token, and "token absent" is a rejection reason, not an escape.
		hasBearer := r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Token") != ""
		bypass := !isMutating || hasBearer ||
			isWebSocketUpgrade(r) ||
			isCSRFExempt(r.URL.Path, cfg.Web.CSRF.ExemptPaths) ||
			isCSRFExempt(r.URL.Path, config.CSRFBuiltinExemptPaths)

		if !bypass {
			// Read the submitted token from the header, falling back to the form
			// field. The no-JS create form posts multipart/form-data (file
			// upload support), and ParseForm never reads a multipart body —
			// parse multipart explicitly so the hidden csrf_token field is
			// visible. ParseMultipartForm caches the parsed form, so the
			// handler's own later parse of the same body is a no-op.
			submitted := r.Header.Get(cfg.Web.CSRF.HeaderName)
			if submitted == "" {
				if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
					// Malformed multipart falls through to token-absent
					// rejection below; no special handling needed.
					_ = r.ParseMultipartForm(32 << 10)
				} else {
					_ = r.ParseForm()
				}
				submitted = r.FormValue("csrf_token")
			}
			// Double-submit comparison target: the request cookie when present;
			// when the cookie is absent there is nothing to match, so the token
			// alone can never satisfy double-submit.
			reason := ""
			switch {
			case submitted == "":
				reason = "token absent"
			case !s.validateCSRFToken(submitted):
				reason = "token signature invalid"
			case reqCookie == nil || subtle.ConstantTimeCompare([]byte(submitted), []byte(reqCookie.Value)) != 1:
				reason = "token mismatch"
			}
			if reason != "" {
				clientHost, _, splitErr := net.SplitHostPort(r.RemoteAddr)
				if splitErr != nil {
					clientHost = r.RemoteAddr
				}
				s.securityLog("security.csrf_failure",
					"ip", clientHost, "endpoint", r.URL.Path, "reason", reason)
				s.auditLog(r, audit.Entry{
					Event:    "security.csrf_failure",
					Severity: audit.SeverityWarn,
					Result:   audit.ResultFailure,
					Target:   &audit.Target{Type: "endpoint", ID: r.URL.Path},
					Reason:   reason,
				})
				// API and non-interactive clients get the canonical CSRF_FAILED envelope
				// (AI.md PART 14 error table); browsers get the themed error page
				// (PART 16 error-page rules). Both share csrfFailureResponse.
				s.csrfFailureResponse(w, r, reason, hasCookie)
				return
			}
		}

		secure := r.TLS != nil
		if cfg.Web.CSRF.Secure == "true" {
			secure = true
		} else if cfg.Web.CSRF.Secure == "false" {
			secure = false
		}

		// Double-submit cookie: HttpOnly=false so progressive-enhancement JS can
		// echo the token into the X-CSRF-Token header; SameSite=Strict is the
		// primary defense (blocks cross-site cookie attachment entirely).
		http.SetCookie(w, &http.Cookie{
			Name:     cfg.Web.CSRF.CookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: false,
			Secure:   secure,
			SameSite: http.SameSiteStrictMode,
		})

		// Store token in context so renderTemplate can inject it into page data.
		ctx := context.WithValue(r.Context(), csrfTokenKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isWebSocketUpgrade reports whether the request is a WebSocket upgrade handshake.
// csrfFailureResponse writes the CSRF rejection response with content
// negotiation (PART 14 + PART 16): API and non-interactive clients get the
// canonical CSRF_FAILED JSON envelope; browser clients get the themed error
// page. Never a bare unstyled body.
func (s *Server) csrfFailureResponse(w http.ResponseWriter, r *http.Request, reason string, hasCookie bool) {
	if detectClientType(r) != "html" {
		body := map[string]interface{}{
			"ok":      false,
			"error":   "CSRF_FAILED",
			"message": "CSRF token validation failed",
		}
		// Tier 3 diagnostics (PART 11): which check tripped is only revealed in
		// debug mode; production responses never carry the _debug field.
		if mode.IsDebugEnabled() {
			body["_debug"] = map[string]interface{}{
				"check":          reason,
				"cookie_present": hasCookie,
				"origin":         r.Header.Get("Origin"),
				"fix_hint":       "send the csrf_token cookie value in the X-CSRF-Token header or csrf_token form field, or call the API with an Authorization header",
			}
		}
		writeJSON(w, http.StatusForbidden, body)
		return
	}
	s.renderErrorPage(w, r, http.StatusForbidden, "CSRF token validation failed. Please reload the page and try again.")
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// isSameOrigin reports whether the request originates from the app's own host.
// It compares the Origin header (or Referer when Origin is absent) host against
// r.Host. A missing/unparseable source is treated as cross-site (not same-origin)
// so it falls through to token validation per the CSRF spec.
func isSameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// isCSRFExempt reports whether path matches any of the exempt glob patterns.
// Only simple prefix and wildcard-suffix patterns are supported (e.g., /foo/*, /foo/bar).
func isCSRFExempt(path string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasSuffix(p, "/*") {
			if strings.HasPrefix(path, strings.TrimSuffix(p, "/*")+"/") || path == strings.TrimSuffix(p, "/*") {
				return true
			}
		} else if p == path {
			return true
		}
	}
	return false
}

// noTrailingSlash redirects paths with trailing slashes to the canonical
// form (no trailing slash). Root "/" is left unchanged. Paths whose last
// segment contains a "." (explicit file requests, e.g. /static/app.js/) are
// also left unchanged per PART 16 URL-normalization rules.
func (s *Server) noTrailingSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		dpasteEndpoint := r.Method == http.MethodPost && (p == "/api/" || p == "/api/v2/")
		if p != "/" && strings.HasSuffix(p, "/") && !dpasteEndpoint {
			// Skip redirect for explicit file requests (last segment has a ".").
			lastSeg := p[strings.LastIndex(p, "/"):]
			if !strings.Contains(lastSeg, ".") {
				r.URL.Path = strings.TrimRight(p, "/")
				http.Redirect(w, r, r.URL.String(), http.StatusMovedPermanently)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Run ─────────────────────────────────────────────────────────────────────

// Run starts the HTTP server and blocks until ctx is cancelled.
// startTermbin starts the raw-TCP termbin/fiche compatibility listener when
// enabled in config. It returns a stop function that closes the listener; the
// function is a no-op when the listener is disabled or fails to bind (non-fatal,
// so the HTTP server still starts). The listener is also closed when ctx ends.
func (s *Server) startTermbin(ctx context.Context, cfg *config.Config) func() {
	tb := cfg.Server.Termbin
	if !tb.Enabled {
		return func() {}
	}

	addr := net.JoinHostPort(cfg.Server.Address, strconv.Itoa(tb.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("termbin: listen on %s failed: %v — disabled", addr, err)
		return func() {}
	}

	timeout := 5 * time.Second
	if d, perr := time.ParseDuration(tb.Timeout); perr == nil && d > 0 {
		timeout = d
	}

	// Raw-TCP connections carry no Host header, so the config-derived base
	// applies: server.base_url, else {proto}://{fqdn}[:{port}] using the full
	// FQDN resolution chain from PART 12 with :80/:443 stripped.
	base := cfg.StartupBaseURL()

	log.Printf("termbin: listening on %s (max %d bytes, timeout %s)", addr, tb.MaxSize, timeout)

	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				// Accept fails permanently once the listener is closed.
				return
			}
			go s.compatHandler.TermbinServe(conn, base, tb.MaxSize, timeout)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	return func() { _ = ln.Close() }
}

// startManagedNetworkServices launches children after the HTTP listener binds
// and the process drops privileges, so their private state is owned by the
// dedicated service account and the child processes run as that account too.
func (s *Server) startManagedNetworkServices() {
	if s.torManager != nil {
		go func() {
			if err := s.torManager.Start(); err != nil {
				log.Printf("tor: start failed: %v", err)
			} else if s.torManager.Running() {
				go s.torManager.Monitor()
				s.persistOnionAddress()
			}
		}()
	}
	if s.i2pManager != nil {
		go func() {
			if err := s.i2pManager.Start(); err != nil {
				log.Printf("i2p: start failed: %v", err)
			} else if s.i2pManager.Running() {
				go s.i2pManager.Monitor()
				s.persistB32Address()
			}
		}()
	}
}

// Run starts the HTTP server on addr and blocks until ctx is cancelled or a
// fatal error occurs, performing graceful shutdown and closing the GeoIP
// database, cache store, and other owned resources on exit.
func (s *Server) Run(ctx context.Context, addr string) error {
	if s.geoipDB != nil {
		defer s.geoipDB.Close()
	}
	if s.cacheStore != nil {
		defer s.cacheStore.Close()
	}
	if s.notifier != nil {
		// Cap at 10s so an in-flight webhook retry ladder can't stall shutdown;
		// any goroutine still running past that continues in the background
		// with its own request timeout and simply won't block the process exit.
		defer s.notifier.Shutdown(10 * time.Second)
	}

	if s.torManager != nil {
		defer s.torManager.Close()
	}

	// Start I2P eepsite (opt-in; disabled unless server.i2p.enabled is set).
	// Constructed here rather than in New() because it depends on s.logDir,
	// which main only populates via SetLogDir() after New() returns.
	i2pCfg := s.liveCfg().Server.I2P
	if i2pCfg.Enabled {
		bootstrapTimeout := 5 * time.Minute
		if d, err := time.ParseDuration(i2pCfg.BootstrapTimeout); err == nil && d > 0 {
			bootstrapTimeout = d
		}
		s.i2pManager = i2p.NewManager(context.Background(), i2p.Config{
			Enabled:          i2pCfg.Enabled,
			Binary:           i2pCfg.Binary,
			SAMAddress:       i2pCfg.SAMAddress,
			VirtualPort:      i2pCfg.VirtualPort,
			InboundLength:    i2pCfg.InboundLength,
			OutboundLength:   i2pCfg.OutboundLength,
			InboundQuantity:  i2pCfg.InboundQuantity,
			OutboundQuantity: i2pCfg.OutboundQuantity,
			SignatureType:    i2pCfg.SignatureType,
			BootstrapTimeout: bootstrapTimeout,
			ConfigDir:        s.configDir,
			DataDir:          s.dataDir,
			LogDir:           s.logDir,
		}, s.router)
		defer s.i2pManager.Close()
	}

	cfg := s.liveCfg()

	// PART 15: "Server validates credentials on startup and before
	// certificate requests" — non-fatal, warn-and-continue (config.Validate()
	// pattern). A bad DNS-01 provider config must never abort startup.
	if ssl.ParseChallenge(cfg.Server.TLS.LetsEncrypt.Challenge) == "dns-01" && cfg.Server.TLS.DNSProvider != "" {
		creds, err := cfg.DecryptDNSCredentials()
		if err != nil {
			log.Printf("WARN: ssl: dns-01 provider %q: failed to decrypt credentials: %v", cfg.Server.TLS.DNSProvider, err)
		} else if err := ssl.ValidateDNSProvider(cfg.Server.TLS.DNSProvider, creds); err != nil {
			log.Printf("WARN: ssl: dns-01 provider %q failed startup validation: %v", cfg.Server.TLS.DNSProvider, err)
		}
	}

	// Server-lifecycle audit events (AI.md server.* catalog).
	s.auditLog(nil, audit.Entry{
		Event:    "server.started",
		Severity: audit.SeverityInfo,
		Details:  map[string]any{"address": addr, "version": s.version},
	})
	defer s.auditLog(nil, audit.Entry{
		Event:    "server.stopped",
		Severity: audit.SeverityInfo,
	})

	// Start the runtime self-healing maintenance monitor (PART 20). It probes
	// critical systems and toggles maintenance mode until ctx is cancelled.
	if s.maintenance != nil {
		go s.maintenance.Start(ctx)
	}

	// Resolve DNS-valued trusted_proxies.additional entries once at startup, then
	// refresh every 5 minutes so CDN / load-balancer IP changes are picked up
	// without a restart (PART 12 Trusted Proxies — DNS names).
	s.refreshDNSTrustedProxies(ctx)
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshDNSTrustedProxies(ctx)
			}
		}
	}()

	// Start the termbin/fiche raw-TCP listener when enabled; the returned stop
	// function closes the listener on shutdown.
	defer s.startTermbin(ctx, cfg)()

	// Parse HTTP server timeouts from config, falling back to safe defaults.
	readTimeout := 30 * time.Second
	if d, err := time.ParseDuration(cfg.Server.Limits.ReadTimeout); err == nil && d > 0 {
		readTimeout = d
	}
	writeTimeout := 30 * time.Second
	if d, err := time.ParseDuration(cfg.Server.Limits.WriteTimeout); err == nil && d > 0 {
		writeTimeout = d
	}
	idleTimeout := 120 * time.Second
	if d, err := time.ParseDuration(cfg.Server.Limits.IdleTimeout); err == nil && d > 0 {
		idleTimeout = d
	}

	// Dual-port mode (PART 15): "--port 80,443" → plain HTTP on the first port
	// (ACME HTTP-01 challenge + redirect to HTTPS) and HTTPS on the second port.
	// A single port keeps the existing behavior below. SplitHostPort tolerates
	// the "host:80,443" form because the comma lives in the port field.
	if host, portSpec, splitErr := net.SplitHostPort(addr); splitErr == nil {
		if httpPort, httpsPort := config.SplitPorts(portSpec); httpsPort != "" {
			return s.runDualPort(ctx, cfg, host, httpPort, httpsPort, readTimeout, writeTimeout, idleTimeout)
		}
	}

	srv := &http.Server{
		Addr:         addr,
		Handler:      s.router,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	errCh := make(chan error, 1)

	// When TLS is configured, set up SSL manager and serve HTTPS.
	if cfg.Server.TLS.Enabled {
		fqdn := cfg.Server.FQDN
		dnsCreds, err := cfg.DecryptDNSCredentials()
		if err != nil {
			log.Printf("WARN: ssl: failed to decrypt dns-01 credentials: %v", err)
			dnsCreds = map[string]string{}
		}
		sslMgr := ssl.NewManager(ssl.Config{
			Enabled: true,
			CertDir: s.configDir + "/ssl",
			FQDN:    fqdn,
			LetsEncrypt: ssl.LetsEncryptConfig{
				Enabled:         cfg.Server.TLS.LetsEncrypt.Enabled,
				Email:           cfg.Server.TLS.LetsEncrypt.Email,
				Challenge:       ssl.ParseChallenge(cfg.Server.TLS.LetsEncrypt.Challenge),
				DNSProviderType: cfg.Server.TLS.DNSProvider,
				DNSCredentials:  dnsCreds,
				Staging:         cfg.Server.TLS.LetsEncrypt.Staging,
			},
		})

		domains := []string{fqdn}
		tlsCfg, err := sslMgr.GetTLSConfig(domains)
		if err != nil {
			log.Printf("ssl: TLS setup failed: %v — falling back to HTTP", err)
			// Fall through to plain HTTP so the server still starts.
		} else if tlsCfg != nil {
			srv.TLSConfig = tlsCfg
			// Wrap the router so autocert HTTP-01 challenges are handled on port 80.
			srv.Handler = sslMgr.GetHTTPHandler(s.router)
			ln, err := s.bindAndDrop(addr)
			if err != nil {
				return fmt.Errorf("bind %s: %w", addr, err)
			}
			s.startManagedNetworkServices()
			go func() {
				if err := srv.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
					errCh <- err
				}
			}()

			select {
			case <-ctx.Done():
				s.shuttingDown.Store(true)
				shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				return srv.Shutdown(shut)
			case err := <-errCh:
				return fmt.Errorf("serve https %s: %w", addr, err)
			}
		}
	}

	// Plain HTTP (no TLS, or TLS setup failed).
	ln, err := s.bindAndDrop(addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}
	s.startManagedNetworkServices()
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		s.shuttingDown.Store(true)
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shut)
	case err := <-errCh:
		return fmt.Errorf("serve http %s: %w", addr, err)
	}
}

// runDualPort serves the dual-port configuration required by PART 15:
// plain HTTP on httpPort (ACME HTTP-01 challenge handler + redirect to HTTPS)
// and HTTPS on httpsPort (the application router with TLS). Both listeners are
// bound while still privileged so ports below 1024 succeed, then privileges are
// dropped once before serving. Shutdown cancels both listeners together.
func (s *Server) runDualPort(ctx context.Context, cfg *config.Config, host, httpPort, httpsPort string, readTimeout, writeTimeout, idleTimeout time.Duration) error {
	fqdn := cfg.Server.FQDN
	dnsCreds, err := cfg.DecryptDNSCredentials()
	if err != nil {
		log.Printf("WARN: ssl: failed to decrypt dns-01 credentials: %v", err)
		dnsCreds = map[string]string{}
	}
	sslMgr := ssl.NewManager(ssl.Config{
		Enabled: true,
		CertDir: s.configDir + "/ssl",
		FQDN:    fqdn,
		LetsEncrypt: ssl.LetsEncryptConfig{
			Enabled:         cfg.Server.TLS.LetsEncrypt.Enabled,
			Email:           cfg.Server.TLS.LetsEncrypt.Email,
			Challenge:       ssl.ParseChallenge(cfg.Server.TLS.LetsEncrypt.Challenge),
			DNSProviderType: cfg.Server.TLS.DNSProvider,
			DNSCredentials:  dnsCreds,
			Staging:         cfg.Server.TLS.LetsEncrypt.Staging,
		},
	})

	domains := []string{fqdn}
	tlsCfg, err := sslMgr.GetTLSConfig(domains)
	if err != nil {
		return fmt.Errorf("dual-port: TLS setup failed for HTTPS on port %s: %w", httpsPort, err)
	}
	if tlsCfg == nil {
		return fmt.Errorf("dual-port: no TLS configuration available for HTTPS on port %s", httpsPort)
	}

	httpsAddr := net.JoinHostPort(host, httpsPort)
	httpAddr := net.JoinHostPort(host, httpPort)

	// HTTPS server serves the application router with TLS on the second port.
	httpsSrv := &http.Server{
		Addr:         httpsAddr,
		Handler:      s.router,
		TLSConfig:    tlsCfg,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	// HTTP server serves the ACME HTTP-01 challenge on the first port and
	// redirects every other request to HTTPS. GetHTTPHandler wraps the redirect
	// fallback with autocert's challenge handler when Let's Encrypt is managing
	// certificates; with static certs it returns the redirect fallback directly.
	httpSrv := &http.Server{
		Addr:         httpAddr,
		Handler:      sslMgr.GetHTTPHandler(s.redirectToHTTPS(httpsPort)),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}

	// Bind both privileged listeners before dropping privileges so ports <1024
	// succeed. bindAndDrop is not reused here because it drops privileges on the
	// first call, which would prevent binding the second privileged port.
	httpsLn, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		return fmt.Errorf("dual-port: listen HTTPS %s: %w", httpsAddr, err)
	}
	httpLn, err := net.Listen("tcp", httpAddr)
	if err != nil {
		_ = httpsLn.Close()
		return fmt.Errorf("dual-port: listen HTTP %s: %w", httpAddr, err)
	}
	if dropErr := s.applyPrivilegeDrop(); dropErr != nil {
		_ = httpsLn.Close()
		_ = httpLn.Close()
		return fmt.Errorf("dual-port: %w", dropErr)
	}
	s.startManagedNetworkServices()

	log.Printf("dual-port: HTTP on %s, HTTPS on %s", httpAddr, httpsAddr)

	errCh := make(chan error, 2)
	go func() {
		if serveErr := httpsSrv.ServeTLS(httpsLn, "", ""); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- fmt.Errorf("dual-port: HTTPS server: %w", serveErr)
		}
	}()
	go func() {
		if serveErr := httpSrv.Serve(httpLn); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- fmt.Errorf("dual-port: HTTP server: %w", serveErr)
		}
	}()

	select {
	case <-ctx.Done():
		s.shuttingDown.Store(true)
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpErr := httpSrv.Shutdown(shut)
		httpsErr := httpsSrv.Shutdown(shut)
		if httpsErr != nil {
			return httpsErr
		}
		return httpErr
	case err := <-errCh:
		return err
	}
}

// redirectToHTTPS returns a handler that permanently redirects every request to
// the same host and path over HTTPS on httpsPort. It is the non-challenge
// fallback for the plain-HTTP listener in dual-port mode. The :443 default port
// is stripped from the target URL.
func (s *Server) redirectToHTTPS(httpsPort string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		target := "https://" + host
		if httpsPort != "" && httpsPort != "443" {
			target += ":" + httpsPort
		}
		target += r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
}

// ─── JSON helpers ─────────────────────────────────────────────────────────────

// writeJSON encodes v as indented JSON and writes it to w. Delegates to
// httputil.WriteJSON so the server and handler packages share one encoder
// (SetEscapeHTML(false) prevents < > & from being mangled to &lt; &gt; &amp;).
func writeJSON(w http.ResponseWriter, status int, v any) {
	httputil.WriteJSON(w, status, v)
}

// ─── Health & info handlers ───────────────────────────────────────────────────

func (s *Server) buildHealthResponse() HealthResponse {
	checks := ChecksInfo{
		Database:  "ok",
		Cache:     "ok",
		Disk:      "ok",
		Config:    "ok",
		Scheduler: "ok",
	}

	if err := s.db.Ping(); err != nil {
		checks.Database = "error"
	}

	// Ping the cache driver (non-fatal for health status but surfaced in checks).
	if s.cacheStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.cacheStore.Ping(ctx); err != nil {
			checks.Cache = "error"
		}
		cancel()
	}

	if !s.checkDisk() {
		checks.Disk = "error"
	}

	// Scheduler health check via optional callback registered by main.
	if s.schedHealthFn != nil && !s.schedHealthFn() {
		checks.Scheduler = "error"
	}

	// Tor health check — report only when Tor is enabled.
	torInfo := s.buildTorInfo()
	if torInfo.Enabled {
		if torInfo.Running {
			checks.Tor = "ok"
		} else {
			checks.Tor = "error"
		}
	}

	// I2P health check — report only when I2P is enabled.
	i2pInfo := s.buildI2PInfo()
	if i2pInfo.Enabled {
		if i2pInfo.Running {
			checks.I2P = "ok"
		} else {
			checks.I2P = "error"
		}
	}

	status := "healthy"
	if checks.Database == "error" || checks.Disk == "error" {
		status = "unhealthy"
	} else if checks.Cache == "error" || checks.Scheduler == "error" || checks.Tor == "error" || checks.I2P == "error" {
		status = "degraded"
	}

	// Maintenance mode overrides both status and mode (PART 20).
	mode := s.cfg.Server.Mode
	var maint *MaintenanceInfo
	if s.maintenance != nil && s.maintenance.InMaintenance() {
		snap := s.maintenance.Snapshot()
		status = "maintenance"
		mode = "maintenance"
		sh := SelfHealingInfo{
			Enabled:  snap.SelfHealingEnabled,
			Attempts: snap.Attempts,
		}
		if !snap.LastAttempt.IsZero() {
			la := snap.LastAttempt.UTC()
			sh.LastAttempt = &la
		}
		if !snap.NextAttempt.IsZero() {
			na := snap.NextAttempt.UTC()
			sh.NextAttempt = &na
		}
		maint = &MaintenanceInfo{
			Reason:      snap.Reason,
			Message:     snap.Message,
			Since:       snap.Since.UTC(),
			SelfHealing: sh,
		}
	}

	// Fetch total paste count for stats (best-effort — zero on error).
	var pastesTotal int64
	if n, err := s.db.CountPastes(); err == nil {
		pastesTotal = n
	}

	// Collect pending-restart keys under the lock.
	s.pendingRestartMu.Lock()
	pendingKeys := make([]string, len(s.pendingRestartKeys))
	copy(pendingKeys, s.pendingRestartKeys)
	s.pendingRestartMu.Unlock()

	// Health Status Values & HTTP Codes priority (AI.md PART 13): shutting_down
	// takes precedence over everything, including maintenance/unhealthy — the
	// server is going away regardless of what the last probe found. restart_required
	// only applies when the server is otherwise healthy/degraded (not unhealthy,
	// not in maintenance, not shutting down).
	if s.shuttingDown.Load() {
		status = "shutting_down"
	} else if status != "unhealthy" && status != "maintenance" && len(pendingKeys) > 0 {
		status = "restart_required"
	}

	cfg := s.liveCfg()
	hr := HealthResponse{
		Project: ProjectInfo{
			Name:        cfg.Server.Branding.EffectiveTitle(),
			Tagline:     cfg.Server.Branding.EffectiveTagline(),
			Description: cfg.Server.Branding.EffectiveDescription(),
		},
		Status:         status,
		PendingRestart: len(pendingKeys) > 0,
		RestartReason:  pendingKeys,
		Version:        s.version,
		GoVersion:      runtime.Version(),
		Build: BuildInfo{
			Commit: s.commitID,
			Date:   s.buildDate,
		},
		Uptime:      formatUptime(time.Since(s.startTime)),
		Mode:        mode,
		Timestamp:   time.Now().UTC(),
		Maintenance: maint,
		Features: FeaturesInfo{
			Tor:   torInfo,
			I2P:   i2pInfo,
			GeoIP: s.geoipDB != nil,
		},
		Checks: checks,
		Stats: StatsInfo{
			RequestsTotal: s.stats.total.Load(),
			Requests24h:   s.stats.last24h(),
			ActiveConns:   int(s.stats.activeConn.Load()),
			PastesTotal:   pastesTotal,
		},
	}
	return hr
}

// buildTorInfo returns the TorInfo block for health responses.
func (s *Server) buildTorInfo() TorInfo {
	if s.torManager == nil || !s.torManager.Enabled() {
		return TorInfo{Enabled: false, Running: false, Status: "disabled", Hostname: ""}
	}
	running := s.torManager.Running()
	onion := s.torManager.OnionAddress()
	status := "starting"
	if running {
		status = "healthy"
	}
	return TorInfo{
		Enabled:  true,
		Running:  running,
		Status:   status,
		Hostname: onion,
	}
}

// buildI2PInfo returns the I2PInfo block for health responses.
func (s *Server) buildI2PInfo() I2PInfo {
	if s.i2pManager == nil {
		return I2PInfo{Enabled: false, Running: false, Status: "disabled", Hostname: "", Provider: "none"}
	}
	running := s.i2pManager.Running()
	addr := s.i2pManager.Address()
	status := "starting"
	if running {
		status = "healthy"
	}
	return I2PInfo{
		Enabled:  true,
		Running:  running,
		Status:   status,
		Hostname: addr,
		Provider: s.i2pManager.Provider().String(),
	}
}

// formatUptime converts a duration to a human-readable string like "2d 5h 30m".
func formatUptime(d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// handleHealthz serves /server/healthz using the standard frontend content
// negotiation rules (PART 13/14): JSON for clients that ask for
// "Accept: application/json" (mirrors the versioned
// /api/{api_version}/server/healthz endpoint), plain text for non-interactive
// HTTP tools, and HTML for browsers.
// httpStatusForHealth maps a HealthResponse.Status value to its HTTP response
// code per the "Health Status Values & HTTP Codes" table (AI.md PART 13):
// unhealthy, maintenance, and shutting_down all report 503; every other
// status (healthy, degraded, restart_required) reports 200. Applies
// identically to every health route and every content-negotiated format.
func httpStatusForHealth(status string) int {
	switch status {
	case "unhealthy", "maintenance", "shutting_down":
		return http.StatusServiceUnavailable
	default:
		return http.StatusOK
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	switch detectClientType(r) {
	case "json":
		s.handleHealthzJSON(w, r)
		return
	case "text":
		hr := s.buildHealthResponse()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(httpStatusForHealth(hr.Status))
		writeHealthText(w, hr)
		return
	}
	hr := s.buildHealthResponse()
	s.renderTemplate(w, r, "healthz.html", map[string]interface{}{
		"Health":    hr,
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
	}, httpStatusForHealth(hr.Status))
}

// writeHealthText emits the /server/healthz response in the canonical
// flattened dot-notation plain-text format defined in PART 13.
func writeHealthText(w http.ResponseWriter, hr HealthResponse) {
	fmt.Fprintf(w, "project.name: %s\n", hr.Project.Name)
	fmt.Fprintf(w, "project.tagline: %s\n", hr.Project.Tagline)
	fmt.Fprintf(w, "project.description: %s\n", hr.Project.Description)
	fmt.Fprintf(w, "status: %s\n", hr.Status)
	fmt.Fprintf(w, "version: %s\n", hr.Version)
	fmt.Fprintf(w, "go_version: %s\n", hr.GoVersion)
	fmt.Fprintf(w, "build.commit: %s\n", hr.Build.Commit)
	fmt.Fprintf(w, "build.date: %s\n", hr.Build.Date)
	fmt.Fprintf(w, "uptime: %s\n", hr.Uptime)
	fmt.Fprintf(w, "mode: %s\n", hr.Mode)
	fmt.Fprintf(w, "timestamp: %s\n", hr.Timestamp.UTC().Format(time.RFC3339))
	if hr.Maintenance != nil {
		fmt.Fprintf(w, "maintenance.reason: %s\n", hr.Maintenance.Reason)
		fmt.Fprintf(w, "maintenance.message: %s\n", hr.Maintenance.Message)
		fmt.Fprintf(w, "maintenance.since: %s\n", hr.Maintenance.Since.UTC().Format(time.RFC3339))
		fmt.Fprintf(w, "maintenance.self_healing.enabled: %t\n", hr.Maintenance.SelfHealing.Enabled)
		fmt.Fprintf(w, "maintenance.self_healing.attempts: %d\n", hr.Maintenance.SelfHealing.Attempts)
		if hr.Maintenance.SelfHealing.LastAttempt != nil {
			fmt.Fprintf(w, "maintenance.self_healing.last_attempt: %s\n", hr.Maintenance.SelfHealing.LastAttempt.UTC().Format(time.RFC3339))
		}
		if hr.Maintenance.SelfHealing.NextAttempt != nil {
			fmt.Fprintf(w, "maintenance.self_healing.next_attempt: %s\n", hr.Maintenance.SelfHealing.NextAttempt.UTC().Format(time.RFC3339))
		}
	}
	fmt.Fprintf(w, "features.tor.enabled: %t\n", hr.Features.Tor.Enabled)
	fmt.Fprintf(w, "features.tor.running: %t\n", hr.Features.Tor.Running)
	if hr.Features.Tor.Status != "" {
		fmt.Fprintf(w, "features.tor.status: %s\n", hr.Features.Tor.Status)
	}
	if hr.Features.Tor.Hostname != "" {
		fmt.Fprintf(w, "features.tor.hostname: %s\n", hr.Features.Tor.Hostname)
	}
	fmt.Fprintf(w, "features.i2p.enabled: %t\n", hr.Features.I2P.Enabled)
	fmt.Fprintf(w, "features.i2p.running: %t\n", hr.Features.I2P.Running)
	if hr.Features.I2P.Status != "" {
		fmt.Fprintf(w, "features.i2p.status: %s\n", hr.Features.I2P.Status)
		fmt.Fprintf(w, "features.i2p.provider: %s\n", hr.Features.I2P.Provider)
	}
	if hr.Features.I2P.Hostname != "" {
		fmt.Fprintf(w, "features.i2p.hostname: %s\n", hr.Features.I2P.Hostname)
	}
	fmt.Fprintf(w, "features.geoip: %t\n", hr.Features.GeoIP)
	fmt.Fprintf(w, "checks.database: %s\n", hr.Checks.Database)
	fmt.Fprintf(w, "checks.cache: %s\n", hr.Checks.Cache)
	fmt.Fprintf(w, "checks.disk: %s\n", hr.Checks.Disk)
	fmt.Fprintf(w, "checks.config: %s\n", hr.Checks.Config)
	fmt.Fprintf(w, "checks.scheduler: %s\n", hr.Checks.Scheduler)
	if hr.Checks.Tor != "" {
		fmt.Fprintf(w, "checks.tor: %s\n", hr.Checks.Tor)
	}
	if hr.Checks.I2P != "" {
		fmt.Fprintf(w, "checks.i2p: %s\n", hr.Checks.I2P)
	}
	fmt.Fprintf(w, "stats.requests_total: %d\n", hr.Stats.RequestsTotal)
	fmt.Fprintf(w, "stats.requests_24h: %d\n", hr.Stats.Requests24h)
	fmt.Fprintf(w, "stats.active_connections: %d\n", hr.Stats.ActiveConns)
}

func (s *Server) handleHealthzJSON(w http.ResponseWriter, r *http.Request) {
	hr := s.buildHealthResponse()
	writeJSON(w, httpStatusForHealth(hr.Status), hr)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	// Reuse the canonical version/go_version/build{commit,date} shape defined
	// for /server/healthz (PART 13, HealthResponse) for consistency across
	// endpoints that report build metadata.
	data := map[string]interface{}{
		"version":    s.version,
		"go_version": runtime.Version(),
		"build": BuildInfo{
			Commit: s.commitID,
			Date:   s.buildDate,
		},
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": data})
}

func (s *Server) handleAPIInfo(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.baseURL(r), "/")
	av := s.apiVersion()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
		"data": map[string]interface{}{
			"name":    "Pastebin API",
			"version": s.version,
			"endpoints": map[string]interface{}{
				"native": map[string]string{
					fmt.Sprintf("GET    /api/%s/pastes", av):          "list public pastes",
					fmt.Sprintf("POST   /api/%s/pastes", av):          "create paste (JSON/multipart/raw)",
					fmt.Sprintf("GET    /api/%s/pastes/{id}", av):     "get paste JSON",
					fmt.Sprintf("DELETE /api/%s/pastes/{id}", av):     "delete paste (requires token)",
					fmt.Sprintf("GET    /api/%s/pastes/{id}/raw", av): "get paste raw text",
				},
				"web": map[string]string{
					"GET  /":            "home",
					"GET  /create":      "create form",
					"POST /create":      "create paste (form/raw)",
					"GET  /{id}":        "view paste",
					"GET  /raw/{id}":    "raw content",
					"GET  /dl/{id}":     "download",
					"GET  /emb/{id}":    "embed view",
					"GET  /remove/{id}": "delete form",
				},
				"compat_pastebin": map[string]string{
					"POST /api/api_post.php":  "create/list/delete paste (api_option field dispatches)",
					"GET  /api/api_raw.php":   "get raw paste (?i=ID)",
					"POST /api/api_login.php": "always returns ANONYMOUS",
				},
				"compat_microbin": map[string]string{
					fmt.Sprintf("POST   /api/%s/pasta", av):      "create paste",
					fmt.Sprintf("GET    /api/%s/pasta", av):      "list pastes",
					fmt.Sprintf("GET    /api/%s/pasta/{id}", av): "get paste",
					fmt.Sprintf("DELETE /api/%s/pasta/{id}", av): "delete paste",
				},
				"compat_lenpaste": map[string]string{
					"POST   /api/new":    fmt.Sprintf("create paste (also /api/%s/new)", av),
					"GET    /api/get":    fmt.Sprintf("get paste (?id=ID) (also /api/%s/get)", av),
					"DELETE /api/remove": fmt.Sprintf("delete paste (?id=ID&deleteToken=TOKEN) (also /api/%s/remove)", av),
					"GET    /api/list":   fmt.Sprintf("list pastes (also /api/%s/list)", av),
					fmt.Sprintf("GET    /api/%s/getServerInfo", av): "server metadata",
				},
				"compat_stikked": map[string]string{
					"POST /api/create":     "create paste; returns bare URL",
					"GET  /api/paste/{id}": "get paste as JSON",
				},
				"compat_hastebin": map[string]string{
					"POST /documents":       "create document; returns {\"key\":\"…\"}",
					"GET  /documents/{key}": "get document; returns {\"key\":\"…\",\"data\":\"…\"}",
				},
				"compat_dpaste": map[string]string{
					"POST /api/":    "create paste (also /api/v2/); format=url|json",
					"POST /api/v2/": "create paste (dpaste v2 alias)",
				},
				"compat_curl_upload": map[string]string{
					"POST /": "0x0.st (-F file=@…) / sprunge (-F sprunge=<-) / ix.io (-F f:1=<-) / raw body; returns bare URL",
				},
			},
			"examples": map[string]string{
				"curl_raw":  "curl --data-binary @file.txt " + base + "/create",
				"curl_file": "curl -F 'files=@code.py' " + base + "/create",
				"curl_json": `curl -H "Content-Type: application/json" -d '{"content":"hello"}' ` + base + "/api/" + s.apiVersion() + "/pastes",
				"pipe":      "cat file.txt | curl --data-binary @- " + base + "/create",
			},
		},
	})
}

// handleAutodiscover serves /api/autodiscover — returns server info and CLI
// update metadata (PART 32 / PART 14). Non-versioned by design: clients use it
// before knowing which API version the server supports.
func (s *Server) handleAutodiscover(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	base := strings.TrimRight(s.baseURL(r), "/")

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok": true,
		"data": map[string]interface{}{
			// Server identity
			"server":      "pastebin",
			"version":     s.version,
			"api_version": s.apiVersion(),
			"base_url":    base,

			// CLI update metadata (PART 32).
			// cli_versions maps os-arch → {version, sha256} for each available binary.
			// Empty map = no CLI binaries hosted by this server; clients stay on their
			// installed version. Operators can populate via the release workflow.
			"cli_versions":    map[string]interface{}{},
			"cli_min_version": "0.0.0",

			// Feature flags visible to clients
			"features": map[string]interface{}{
				"tor":     s.torManager != nil && s.torManager.Running(),
				"i2p":     s.i2pManager != nil && s.i2pManager.Running(),
				"metrics": cfg.Server.Metrics.Enabled,
			},
		},
	})
}

// ─── Web page handlers ────────────────────────────────────────────────────────

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	pastes, _, err := s.db.GetPublicPastes(1, 5)
	if err != nil {
		// Fail-open: the home page still renders (just without the recent
		// list) so a transient DB error never takes the landing page down.
		log.Printf("home: recent pastes query failed: %v", err)
	}
	data := map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
		"BaseURL":   s.baseURL(r),
		"Recent":    pastes,
	}

	// Content negotiation: HTTP tools get the full template rendered as plain text.
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "home.html", data)
		if err != nil {
			// Fallback when templates are unavailable: minimal plain text.
			fmt.Fprintf(w, "%s\nPOST %s/api/%s/pastes to create a paste.\n", s.liveCfg().Web.SiteTitle, s.baseURL(r), s.apiVersion())
			for _, p := range pastes {
				fmt.Fprintf(w, "%s/%s\t%s\n", s.baseURL(r), p.ID, p.Title)
			}
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}

	s.renderTemplate(w, r, "home.html", data)
}

func (s *Server) handleCreatePage(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, r, "create.html", map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
	})
}

// handleWebCreate handles browser form submissions to POST /pastes. The form
// uses enctype="multipart/form-data" to support file uploads (PART 16); the
// result — including the one-time owner token — is rendered server-side back
// into create.html. Non-browser callers (JSON API, raw, CLI) are delegated to
// the content-negotiating handler.
func (s *Server) handleWebCreate(w http.ResponseWriter, r *http.Request) {
	ct := r.Header.Get("Content-Type")
	// Route browser form submissions (urlencoded or multipart) to the HTML
	// result flow. All other callers — JSON API, raw body, CLI — get the
	// content-negotiating handler that returns text or JSON (PART 16).
	isFormPost := strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
		strings.HasPrefix(ct, "multipart/form-data")
	if !isFormPost || detectClientType(r) != "html" {
		s.pasteHandler.CreatePaste(w, r)
		return
	}

	data := map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
	}

	resp, status, err := s.pasteHandler.CreateFromForm(r)
	if err != nil {
		if status == 0 {
			status = http.StatusBadRequest
		}
		data["Error"] = err.Error()
		w.WriteHeader(status)
		s.renderTemplate(w, r, "create.html", data)
		return
	}

	data["Created"] = resp
	// Derive full raw/download URLs from the same base as the view Link
	// (Link is "{base}/{id}") so all three URLs share an identical origin.
	base := strings.TrimSuffix(resp.Link, "/"+resp.ID)
	data["RawLink"] = base + "/raw/" + resp.ID
	data["DownloadLink"] = base + "/dl/" + resp.ID

	// Set the owner_token cookie so web management (delete form) works with
	// JS disabled (PART 11 / PART 16). HttpOnly keeps the raw token off the
	// JS heap; SameSite=Strict + POST-only mutations provide CSRF protection.
	// Secure is set when the connection is TLS; for HTTP dev environments the
	// cookie is still set so the delete form works locally.
	// Newly issued primary tokens receive a one-year cookie. Reused identities
	// keep the existing sliding cookie untouched.
	if resp.OwnerToken != "" {
		s.setOwnerCookie(w, r, resp.OwnerToken)
	}

	s.renderTemplate(w, r, "create.html", data)
}

func (s *Server) handleRecent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Guest preferences (AI.md 23485: results-per-page, sort order): an
	// explicit query param always wins and is persisted back to the
	// pastebin_pref_{sort,order,per_page} cookies; otherwise fall back to
	// the visitor's saved preference, then the hard default.
	prefs := appPreferencesFromRequest(r)

	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}

	limitParam := strings.TrimSpace(q.Get("limit"))
	limit, _ := strconv.Atoi(limitParam)
	if limitParam == "" {
		limit, _ = strconv.Atoi(prefs[prefKeyPerPage])
	} else {
		s.rememberPreference(w, r, prefKeyPerPage, limitParam)
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 250 {
		limit = 250
	}

	search := strings.TrimSpace(q.Get("search"))

	sortBy := strings.TrimSpace(q.Get("sort"))
	if sortBy == "" {
		sortBy = prefs[prefKeySort]
	} else {
		s.rememberPreference(w, r, prefKeySort, sortBy)
	}
	if sortBy == "" {
		sortBy = "date"
	}

	order := strings.TrimSpace(q.Get("order"))
	if order == "" {
		order = prefs[prefKeyOrder]
	} else {
		s.rememberPreference(w, r, prefKeyOrder, order)
	}
	if order == "" {
		order = "desc"
	}

	pastes, total, _ := s.db.SearchPublicPastes(page, limit, search, sortBy, order)
	pages := (total + limit - 1) / limit
	if pages < 1 {
		pages = 1
	}
	data := map[string]interface{}{
		"SiteTitle":   s.liveCfg().Web.SiteTitle,
		"Theme":       s.liveCfg().Web.Theme,
		"BaseURL":     s.baseURL(r),
		"Pastes":      pastes,
		"Total":       total,
		"Page":        page,
		"Limit":       limit,
		"Pages":       pages,
		"HasPrev":     page > 1,
		"HasNext":     page < pages,
		"PrevPage":    page - 1,
		"NextPage":    page + 1,
		"Search":      search,
		"Sort":        sortBy,
		"Order":       order,
		"CurrentPath": r.URL.Path,
	}

	// Content negotiation: HTTP tools get the full template rendered as plain text.
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "recent.html", data)
		if err != nil {
			// Fallback when templates are unavailable: minimal plain text.
			fmt.Fprintf(w, "# Recent pastes (%d total)\n", total)
			for _, p := range pastes {
				fmt.Fprintf(w, "%s/%s\t%s\t%s\n", s.baseURL(r), p.ID, p.Language, p.Title)
			}
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}

	s.renderTemplate(w, r, "recent.html", data)
}

// detectClientType returns "html", "json", or "text" based on User-Agent and
// Accept header per PART 14 content negotiation rules.
//
// Priority order:
//  1. Explicit Accept header overrides UA detection.
//  2. Our CLI client (pastebin-cli/) → "json" (INTERACTIVE, renders its own TUI).
//  3. Text browsers (lynx, w3m, etc.) → "html" (INTERACTIVE, no JavaScript).
//  4. HTTP tools (curl, wget, empty UA) → "text" (NON-INTERACTIVE, dump output).
//  5. Anything else (regular browsers) → "html".
func detectClientType(r *http.Request) string {
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		return "json"
	}
	if strings.Contains(accept, "text/html") {
		return "html"
	}
	if strings.Contains(accept, "text/plain") {
		return "text"
	}

	// Our client is INTERACTIVE — receives JSON, renders own TUI/GUI.
	if httputil.IsOurCliClient(r) {
		return "json"
	}

	// Text browsers are INTERACTIVE but have no JavaScript support.
	// They receive the standard HTML templates (forms use POST, no JS required).
	if httputil.IsTextBrowser(r) {
		return "html"
	}

	// HTTP tools are NON-INTERACTIVE — send pre-formatted plain text.
	if httputil.IsHttpTool(r) {
		return "text"
	}

	// Default: regular browser.
	return "html"
}

func (s *Server) handleViewPaste(w http.ResponseWriter, r *http.Request) {
	s.renderPasteView(w, r, false)
}

// renderPasteView renders (or redirects to) a paste for a given request. The
// root vanity route (/{id}) redirects link pastes straight to their target
// URL; the explicit /view/{id} route always renders the normal paste detail
// view, even for links, so forceView lets callers opt out of the redirect.
func (s *Server) renderPasteView(w http.ResponseWriter, r *http.Request, forceView bool) {
	id := chi.URLParam(r, "id")

	// Reject reserved system slugs — these are never valid paste IDs.
	if isReservedSlug(id) {
		s.renderErrorPage(w, r, http.StatusNotFound, "The requested paste was not found.")
		return
	}

	paste, err := s.pasteHandler.GetPasteForWeb(id)
	if err != nil {
		switch detectClientType(r) {
		case "json":
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": "SERVER_ERROR", "message": "internal server error"})
		default:
			s.renderErrorPage(w, r, http.StatusInternalServerError, "An internal error occurred. Please try again.")
		}
		return
	}
	if paste == nil {
		s.renderErrorPage(w, r, http.StatusNotFound, "The requested paste was not found.")
		return
	}

	// Links redirect instead of rendering on the root route, for every client
	// type — the raw route (/raw/{id}) still returns the target URL as plain
	// text, no redirect, and /view/{id} always renders the normal view.
	if paste.IsLink && !forceView {
		http.Redirect(w, r, paste.Content, http.StatusFound)
		return
	}

	// Content negotiation per PART 16: CLI tools get raw text, browsers get HTML.
	switch detectClientType(r) {
	case "text":
		// Binary pastes (base64-stored) should be fetched via /raw/{id}.
		if paste.ContentType != "" && !strings.HasPrefix(paste.ContentType, "text/") {
			http.Redirect(w, r, "/raw/"+id, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(paste.Content)) //nolint:errcheck
		return
	case "json":
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"data": paste,
		})
		return
	}

	cc := classifyPasteContent(paste)
	embedHTML, embedMarkdown := buildEmbedSnippets(s.baseURL(r), id, paste.Title, cc.IsImage)

	s.renderTemplate(w, r, "paste.html", map[string]interface{}{
		"SiteTitle":       s.liveCfg().Web.SiteTitle,
		"Theme":           s.liveCfg().Web.Theme,
		"Paste":           paste,
		"ID":              id,
		"Content":         handler.HighlightedContent(paste, true),
		"IsImage":         cc.IsImage,
		"ImageDataURI":    cc.ImageDataURI,
		"IsAudio":         cc.IsAudio,
		"IsVideo":         cc.IsVideo,
		"MediaDataURI":    cc.MediaDataURI,
		"IsActiveContent": cc.IsActiveContent,
		"IsBinary":        cc.IsBinary,
		"FileContentType": cc.FileContentType,
		"EmbedHTML":       embedHTML,
		"EmbedMarkdown":   embedMarkdown,
	})
}

// markdownTitleEscaper neutralises the markdown link-syntax characters in a
// paste title so a copied snippet cannot break out of its link text.
var markdownTitleEscaper = strings.NewReplacer(
	`\`, `\\`, `[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`,
)

// buildEmbedSnippets returns copy-ready HTML and Markdown embed snippets for a
// paste. The HTML snippet iframes /emb/{id} (the frame-ancestors allow-listed
// endpoint); Markdown has no iframe syntax, so images link inline via /raw/{id}
// and everything else becomes a plain link to the paste view.
func buildEmbedSnippets(base, id, title string, isImage bool) (embedHTML, embedMarkdown string) {
	escTitle := template.HTMLEscapeString(title)
	embedHTML = `<iframe src="` + base + `/emb/` + id + `" width="100%" height="400" loading="lazy" title="` + escTitle + `"></iframe>`
	mdTitle := markdownTitleEscaper.Replace(title)
	if isImage {
		embedMarkdown = "![" + mdTitle + "](" + base + "/raw/" + id + ")"
	} else {
		embedMarkdown = "[" + mdTitle + "](" + base + "/" + id + ")"
	}
	return embedHTML, embedMarkdown
}

// pasteContentClass categorises stored paste content for template rendering.
type pasteContentClass struct {
	IsImage         bool
	IsAudio         bool
	IsVideo         bool
	IsActiveContent bool
	IsBinary        bool
	FileContentType string
	ImageDataURI    template.URL
	MediaDataURI    template.URL
}

// classifyPasteContent categorises the stored content so templates render it
// correctly. Binary pastes are stored as base64 (ContentType is set for all
// non-text uploads). Active types must NEVER be rendered inline — download only.
func classifyPasteContent(paste *model.Paste) pasteContentClass {
	// Canonicalize the media type so case, whitespace, or trailing parameters
	// cannot smuggle an active type past the classification check; an
	// unparseable-but-present value fails closed as active (download only).
	fileCT := paste.ContentType
	ctParseErr := false
	if fileCT != "" {
		if mt, _, err := mime.ParseMediaType(fileCT); err == nil {
			fileCT = mt
		} else {
			ctParseErr = true
		}
	}

	// Active content: can execute scripts in a browser — force download only.
	isActiveContent := ctParseErr || fileCT == "text/html" || fileCT == "application/xhtml+xml" ||
		fileCT == "image/svg+xml" || fileCT == "text/xml" || fileCT == "application/xml" ||
		fileCT == "application/javascript" || fileCT == "text/javascript"

	// Raster images safe to embed as data URI (SVG excluded — active content).
	isImage := strings.HasPrefix(fileCT, "image/") && !isActiveContent

	// Audio and video safe for native browser players.
	isAudio := strings.HasPrefix(fileCT, "audio/")
	isVideo := strings.HasPrefix(fileCT, "video/")

	// All other non-text binary content (executables, archives, PDFs, etc.).
	isBinary := fileCT != "" && !isImage && !isAudio && !isVideo && !isActiveContent &&
		!strings.HasPrefix(fileCT, "text/")

	// Binary content is stored base64-encoded; legacy pastes may hold raw bytes,
	// so re-encode anything that does not decode to keep the data URI valid.
	binaryB64 := func(content string) string {
		if _, err := base64.StdEncoding.DecodeString(content); err != nil {
			return base64.StdEncoding.EncodeToString([]byte(content))
		}
		return content
	}

	// Data URIs must be typed template.URL — html/template rewrites untyped
	// data: URLs to #ZgotmplZ. Safe here: the URI is built server-side from a
	// sniffed non-active MIME type plus base64 (cannot break out of the attr).
	var imageDataURI, mediaDataURI template.URL
	if isImage {
		imageDataURI = template.URL("data:" + fileCT + ";base64," + binaryB64(paste.Content))
	}
	if isAudio || isVideo {
		mediaDataURI = template.URL("data:" + fileCT + ";base64," + binaryB64(paste.Content))
	}

	return pasteContentClass{
		IsImage:         isImage,
		IsAudio:         isAudio,
		IsVideo:         isVideo,
		IsActiveContent: isActiveContent,
		IsBinary:        isBinary,
		FileContentType: fileCT,
		ImageDataURI:    imageDataURI,
		MediaDataURI:    mediaDataURI,
	}
}

// isEmbedPath reports whether the request path is the embeddable paste view.
// /emb/{id} is the only endpoint on the frame-ancestors allow-list (PART 11):
// it drops X-Frame-Options, widens frame-ancestors, and is exempt from the
// Sec-Fetch-Dest iframe rejection so third-party sites can iframe it.
func isEmbedPath(path string) bool {
	return strings.HasPrefix(path, "/emb/") && len(path) > len("/emb/")
}

func (s *Server) handleEmbed(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	paste, err := s.pasteHandler.GetPasteForWeb(id)
	if err != nil || paste == nil {
		s.renderErrorPage(w, r, http.StatusNotFound, "The requested paste was not found.")
		return
	}

	cc := classifyPasteContent(paste)

	s.renderTemplate(w, r, "emb.html", map[string]interface{}{
		"ID":              id,
		"IsImage":         cc.IsImage,
		"ImageDataURI":    cc.ImageDataURI,
		"IsAudio":         cc.IsAudio,
		"IsVideo":         cc.IsVideo,
		"MediaDataURI":    cc.MediaDataURI,
		"IsActiveContent": cc.IsActiveContent,
		"IsBinary":        cc.IsBinary,
		"FileContentType": cc.FileContentType,
		"Paste":           paste,
		"Content":         handler.HighlightedContent(paste, false),
	})
}

func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	link := s.baseURL(r) + "/" + id
	// Render a simple page that generates a QR code client-side (or redirect to QR API).
	s.renderTemplate(w, r, "qr.html", map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
		"ID":        id,
		"Link":      link,
	})
}

// handleQRImage generates a QR code PNG server-side and streams it directly to
// the client. This avoids any external API dependency per PART 16.
func (s *Server) handleQRImage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	link := s.baseURL(r) + "/" + id
	png, err := qrcode.Encode(link, qrcode.Medium, 300)
	if err != nil {
		// The QR route serves an image, so the failure body must stay
		// machine-readable rather than negotiating to an HTML page.
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":      false,
			"error":   "SERVER_ERROR",
			"message": "QR generation failed",
		})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(png) //nolint:errcheck
}

func (s *Server) handleRemovePage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.renderTemplate(w, r, "remove.html", map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
		"ID":        id,
		"Error":     "",
		"Success":   false,
	})
}

func (s *Server) handleRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "BAD_REQUEST", "message": "bad request"})
		return
	}
	token := r.FormValue("token")
	// Cookie fallback: web management forms may use the owner_token cookie set
	// on creation when the token field is left blank (PART 11 / PART 16).
	// API routes (/api/...) never reach this handler, so this is always a
	// browser-context form POST — cookie use is safe here.
	if token == "" {
		if c, err := r.Cookie(ownerTokenCookieName); err == nil && c.Value != "" {
			token = c.Value
		}
	}
	if token == "" {
		s.renderTemplate(w, r, "remove.html", map[string]interface{}{
			"SiteTitle": s.liveCfg().Web.SiteTitle,
			"Theme":     s.liveCfg().Web.Theme,
			"ID":        id,
			"Error":     "owner token is required",
			"Success":   false,
		})
		return
	}

	// Two-tier auth (PART 11): operator token bypasses api_tokens lookup.
	incomingHash := sha256.Sum256([]byte(token))
	var zeroHash [32]byte
	if s.operatorTokenHash != zeroHash &&
		subtle.ConstantTimeCompare(incomingHash[:], s.operatorTokenHash[:]) == 1 {
		if err := s.db.DeletePaste(id); err != nil {
			s.renderTemplate(w, r, "remove.html", map[string]interface{}{
				"SiteTitle": s.liveCfg().Web.SiteTitle,
				"Theme":     s.liveCfg().Web.Theme,
				"ID":        id,
				"Error":     "paste not found",
				"Success":   false,
			})
			return
		}
	} else if err := s.db.VerifyAPIToken(incomingHash, "paste", id); err != nil {
		s.authLog(r, "owner", "fail", "invalid_owner_token")
		// The token (field or cookie-fallback) is confirmed invalid/stale (e.g.
		// the database was wiped). Expire the owner_token cookie now: it is
		// HttpOnly so app.js can never clear it itself, and without this the
		// browser keeps silently re-submitting the same dead token on every
		// future remove attempt, forcing the user to manually clear cookies.
		http.SetCookie(w, &http.Cookie{
			Name:     ownerTokenCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   r.TLS != nil,
			SameSite: http.SameSiteStrictMode,
		})
		s.renderTemplate(w, r, "remove.html", map[string]interface{}{
			"SiteTitle": s.liveCfg().Web.SiteTitle,
			"Theme":     s.liveCfg().Web.Theme,
			"ID":        id,
			"Error":     "paste not found or invalid token",
			"Success":   false,
		})
		return
	} else if err := s.db.DeletePaste(id); err != nil {
		s.renderTemplate(w, r, "remove.html", map[string]interface{}{
			"SiteTitle": s.liveCfg().Web.SiteTitle,
			"Theme":     s.liveCfg().Web.Theme,
			"ID":        id,
			"Error":     "paste not found",
			"Success":   false,
		})
		return
	}

	s.renderTemplate(w, r, "remove.html", map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
		"ID":        id,
		"Error":     "",
		"Success":   true,
	})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	paste, err := s.pasteHandler.GetPasteForWeb(id)
	if err != nil || paste == nil {
		s.renderErrorPage(w, r, http.StatusNotFound, "The requested paste was not found.")
		return
	}

	filename := paste.Title
	if filename == "" || filename == "Untitled" {
		filename = id
	}
	filename = sanitizeAttachmentFilename(filename, id)

	// Binary pastes are stored as base64; decode to raw bytes before sending.
	var body []byte
	ct := paste.ContentType
	if ct != "" && !strings.HasPrefix(ct, "text/") {
		if decoded, err := base64.StdEncoding.DecodeString(paste.Content); err == nil {
			body = decoded
		} else {
			body = []byte(paste.Content)
		}
	} else {
		body = []byte(paste.Content)
	}

	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"; filename*=UTF-8''`+url.PathEscape(filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(body) //nolint:errcheck
}

// sanitizeAttachmentFilename strips characters that could break the quoted
// Content-Disposition filename parameter (double-quote, backslash, path
// separators, and control characters) and caps the length. Falls back to id
// when nothing usable remains.
func sanitizeAttachmentFilename(name, id string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' || r == '/' || r == '\n' || r == '\r' {
			return '_'
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	if len(cleaned) > 200 {
		cleaned = cleaned[:200]
	}
	if cleaned == "" {
		return id
	}
	return cleaned
}

// handleURLRedirect redirects to the paste content if it is a URL; otherwise
// shows the paste view. Used for microbin /url/{id} and /u/{id}.
func (s *Server) handleURLRedirect(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	paste, err := s.pasteHandler.GetPasteForWeb(id)
	if err != nil || paste == nil {
		s.renderErrorPage(w, r, http.StatusNotFound, "The requested paste was not found.")
		return
	}
	content := strings.TrimSpace(paste.Content)
	if strings.HasPrefix(content, "http://") || strings.HasPrefix(content, "https://") {
		http.Redirect(w, r, content, http.StatusFound)
		return
	}
	// Fall back to normal paste view.
	s.renderTemplate(w, r, "paste.html", map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
		"Paste":     paste,
		"ID":        id,
		"Content":   handler.HighlightedContent(paste, true),
	})
}

// ─── Server info pages ────────────────────────────────────────────────────────

func (s *Server) handleAbout(w http.ResponseWriter, r *http.Request) {
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "about.html", s.aboutPageData())
		if err != nil {
			fmt.Fprintf(w, "%s\n%s/server/about\n", s.liveCfg().Web.SiteTitle, s.baseURL(r))
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "about.html", s.aboutPageData())
}

// aboutPageData builds the /server/about template data, sourcing the tagline,
// description, feature list, and links from the operator's Branding config with a
// real IDEA.md fallback (PART 16 page-content sourcing). No content is hardcoded
// in the template and no field is ever blank or a generic placeholder.
func (s *Server) aboutPageData() map[string]interface{} {
	b := s.liveCfg().Server.Branding
	data := s.pageData()
	data["Tagline"] = b.EffectiveTagline()
	data["Description"] = b.EffectiveDescription()
	data["Features"] = b.EffectiveFeatures()
	data["Links"] = b.EffectiveLinks()
	data["GeoIPEnabled"] = s.liveCfg().Server.GeoIP.Enabled
	return data
}

func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
		"BaseURL":   s.baseURL(r),
	}
	s.injectTermbinData(r, data)
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "help.html", data)
		if err != nil {
			fmt.Fprintf(w, "Usage: curl %s/create\nAPI docs: %s/server/docs/swagger\n", s.baseURL(r), s.baseURL(r))
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "help.html", data)
}

// contactData builds the template data for the /server/contact page, minting a
// fresh simple-captcha challenge when the built-in captcha is selected (PART 31).
func (s *Server) contactData(r *http.Request) map[string]interface{} {
	cfg := s.liveCfg()
	pc := cfg.Server.Pages.Contact
	data := map[string]interface{}{
		"SiteTitle":   cfg.Web.SiteTitle,
		"Theme":       cfg.Web.Theme,
		"Contact":     s.publicContactEmail(r),
		"Abuse":       s.publicAbuseEmail(r),
		"BaseURL":     s.baseURL(r),
		"FormEnabled": pc.Enabled,
		"CaptchaType": pc.Captcha,
	}
	if pc.Enabled && pc.Captcha == "simple" {
		if cap, err := s.generateSimpleCaptcha(); err == nil {
			data["CaptchaQuestion"] = cap.Question
			data["CaptchaToken"] = cap.Token
		}
	}
	// Security-report mode switch (PART 11 Coordinated Disclosure Pipeline): a
	// valid, non-expired security_id in the query string swaps the standard
	// contact form for the security-research form. An absent or invalid id
	// silently leaves the standard form in place.
	if pc.Enabled && s.validSecurityID(strings.TrimSpace(r.URL.Query().Get("security_id"))) {
		data["SecurityMode"] = true
		data["SecurityID"] = s.currentSecurityID()
		data["Components"] = securityComponents
		data["FormDisclosureDays"] = defaultDisclosureDays
	}
	return data
}

func (s *Server) handleContact(w http.ResponseWriter, r *http.Request) {
	data := s.contactData(r)
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "contact.html", data)
		if err != nil {
			if email := s.publicContactEmail(r); email != "" {
				fmt.Fprintf(w, "Contact: %s\n", email)
			}
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "contact.html", data)
}

// handleContactPost processes a /server/contact form submission: it validates
// the required fields and captcha, then emails the message to the general
// contact recipient (falling back to admin) and re-renders the page with a
// success or error banner (PART 31, AI.md /server/contact).
func (s *Server) handleContactPost(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	pc := cfg.Server.Pages.Contact

	data := s.contactData(r)

	// When the form is disabled, show the static page without accepting input.
	if !pc.Enabled {
		w.WriteHeader(http.StatusNotFound)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}

	if err := r.ParseForm(); err != nil {
		data["ContactError"] = "Invalid form submission."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}

	// Coordinated-disclosure branch (PART 11): when a security_id is supplied,
	// re-validate it server-side (the form value can be tampered with — prefer
	// the query string, fall back to the hidden field). A valid id routes into
	// the encrypted security-report pipeline; an invalid id is logged as a
	// possible scrape attempt and falls through to the standard contact form.
	suppliedSecID := strings.TrimSpace(r.URL.Query().Get("security_id"))
	if suppliedSecID == "" {
		suppliedSecID = strings.TrimSpace(r.PostFormValue("security_id"))
	}
	if suppliedSecID != "" {
		if s.validSecurityID(suppliedSecID) {
			s.handleSecurityReport(w, r, cfg)
			return
		}
		s.securityLog("security.security_id_invalid",
			"ip", clientIP(r), "ua", r.UserAgent(), "supplied_id", suppliedSecID)
		s.auditLog(r, audit.Entry{
			Event:    "security.security_id_invalid",
			Severity: audit.SeverityWarn,
			Result:   audit.ResultFailure,
		})
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	fromEmail := strings.TrimSpace(r.PostFormValue("email"))
	subject := strings.TrimSpace(r.PostFormValue("subject"))
	message := strings.TrimSpace(r.PostFormValue("message"))

	// Preserve entered values so a validation error does not clear the form.
	data["FormName"] = name
	data["FormEmail"] = fromEmail
	data["FormSubject"] = subject
	data["FormMessage"] = message

	if name == "" || fromEmail == "" || subject == "" || message == "" {
		data["ContactError"] = "All fields are required."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	if !strings.Contains(fromEmail, "@") || strings.ContainsAny(fromEmail, " \t\r\n") {
		data["ContactError"] = "Please enter a valid email address."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}

	// Validate the built-in simple captcha; provider captchas (recaptcha,
	// hcaptcha) require site keys and are not verified server-side here.
	if pc.Captcha == "simple" {
		if !s.validateSimpleCaptcha(r.PostFormValue("captcha_token"), r.PostFormValue("captcha")) {
			data["ContactError"] = "Captcha answer was incorrect. Please try again."
			w.WriteHeader(http.StatusBadRequest)
			s.renderTemplate(w, r, "contact.html", data)
			return
		}
	}

	to := strings.TrimSpace(cfg.GeneralEmail())
	m := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, s.baseURL(r), cfg.Server.FQDN)
	emailReady := to != "" && m.Enabled()
	webhookTargets := cfg.WebhookTargets("general")

	// The form is available if any delivery channel is configured (PART 17): an
	// SMTP recipient or at least one general-role webhook.
	if !emailReady && len(webhookTargets) == 0 {
		data["ContactError"] = "The contact form is currently unavailable. Please try again later."
		w.WriteHeader(http.StatusServiceUnavailable)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}

	// General webhook — spam-filtered sender details and message (PART 17).
	s.notifyRole(cfg, "general", "general.contact_submitted",
		"Contact form: "+subject,
		"From: "+name+" <"+fromEmail+">\n\n"+message,
		"info")

	if emailReady {
		if err := m.Send(to, "contact", map[string]string{
			"name":    name,
			"email":   fromEmail,
			"subject": subject,
			"message": message,
		}); err != nil {
			log.Printf("contact: send failed: %v", err)
			data["ContactError"] = "Failed to send your message. Please try again later."
			w.WriteHeader(http.StatusBadGateway)
			s.renderTemplate(w, r, "contact.html", data)
			return
		}
	}

	data["ContactSuccess"] = pc.SuccessMessage
	// Clear the preserved fields on success so the form resets.
	delete(data, "FormName")
	delete(data, "FormEmail")
	delete(data, "FormSubject")
	delete(data, "FormMessage")
	s.renderTemplate(w, r, "contact.html", data)
}

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "privacy.html", s.privacyPageData(r))
		if err != nil {
			fmt.Fprintf(w, "Privacy Policy — %s/server/privacy\n", s.baseURL(r))
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "privacy.html", s.privacyPageData(r))
}

// privacyPageData builds the template data for the privacy page from live config
// (PART 31). Templates access the privacy tree via .Privacy.*, the analytics
// platform via .Tracking.*, the auto-merged third-party list via
// .ThirdPartyServices, and the CCPA opt-out cookie state via .CCPAOptedOut.
func (s *Server) privacyPageData(r *http.Request) map[string]interface{} {
	cfg := s.liveCfg()
	privacy := cfg.Server.Privacy
	tracking := cfg.Server.Tracking
	ccpaOptedOut := false
	if cookie, err := r.Cookie("ccpa_opt_out"); err == nil && cookie.Value == "true" {
		ccpaOptedOut = true
	}
	data := s.pageData()
	data["Privacy"] = &privacy
	data["Tracking"] = &tracking
	data["CCPAOptedOut"] = ccpaOptedOut
	data["ThirdPartyServices"] = cfg.EffectiveThirdParty()
	return data
}

// safeRedirectPath returns u's path+query, but only when it is a genuine
// single-slash relative path — never "//host/..." or "/\\host/..." style
// values, which browsers can interpret as protocol-relative/scheme-relative
// URLs and redirect off-site even when url.Parse reported an empty or
// matching Host (open-redirect hardening for every Referer/return_to-based
// same-page redirect in this file).
func safeRedirectPath(u *url.URL) (string, bool) {
	p := u.RequestURI()
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "", false
	}
	return p, true
}

// handleCCPAOptOut sets or clears the ccpa_opt_out cookie in response to the
// "Do Not Sell My Personal Information" toggle on the privacy page (PART 31).
// action=opt_in clears the opt-out; any other value records the opt-out for one
// year. The cookie is readable by client JS so the consent UI can reflect state.
func (s *Server) handleCCPAOptOut(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil
	if s.liveCfg().Web.CSRF.Secure == "true" {
		secure = true
	} else if s.liveCfg().Web.CSRF.Secure == "false" {
		secure = false
	}
	cookie := &http.Cookie{
		Name:     "ccpa_opt_out",
		Path:     "/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if strings.TrimSpace(r.PostFormValue("action")) == "opt_in" {
		cookie.Value = ""
		cookie.MaxAge = -1
	} else {
		cookie.Value = "true"
		cookie.MaxAge = 365 * 24 * 60 * 60
	}
	http.SetCookie(w, cookie)
	// Return to the referring page (privacy or preferences, AI.md 23485: the
	// CCPA toggle is now also a control on /server/preferences) rather than
	// always bouncing to /server/privacy, mirroring handleConsentSet.
	dest := "/server/privacy#ccpa-opt-out"
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host == r.Host {
			if p, ok := safeRedirectPath(u); ok {
				dest = p
			}
		}
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// consentState mirrors the JSON persisted in the server-readable cookie_consent
// cookie (AI.md 16919-16936): the granular categories a visitor has consented
// to, plus when. Essential is always true — it cannot be declined.
type consentState struct {
	Essential   bool  `json:"essential"`
	Preferences bool  `json:"preferences"`
	Analytics   bool  `json:"analytics"`
	Timestamp   int64 `json:"timestamp"`
}

// hasConsentCookie reports whether the visitor already recorded a cookie
// consent choice, so the no-JS fallback form (AI.md 25598) can hide once a
// decision exists, matching the JS banner's own dismiss behavior.
func hasConsentCookie(r *http.Request) bool {
	c, err := r.Cookie("cookie_consent")
	return err == nil && strings.TrimSpace(c.Value) != ""
}

// priorConsentState parses the visitor's existing cookie_consent cookie, if
// any, so handleConsentSet can detect a withdrawal/downgrade against it.
// A missing or unparseable cookie yields ok=false — there is nothing to
// withdraw from a visitor recording their first-ever choice.
func priorConsentState(r *http.Request) (consentState, bool) {
	c, err := r.Cookie("cookie_consent")
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return consentState{}, false
	}
	var prior consentState
	if err := json.Unmarshal([]byte(c.Value), &prior); err != nil {
		return consentState{}, false
	}
	return prior, true
}

// handleConsentSet persists the visitor's cookie-consent choice in the
// server-readable cookie_consent cookie and redirects back to the originating
// page (AI.md 25598), giving no-JS visitors a working consent banner via the
// <noscript> form that POSTs here. `choice` is "accept", "decline", or "save"
// (granular selection from the preferences form); anything else is rejected.
func (s *Server) handleConsentSet(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	analyticsConfigured := cfg.Server.Tracking.Enabled()
	prior, hadPrior := priorConsentState(r)
	state := consentState{Essential: true, Timestamp: time.Now().Unix()}
	switch strings.TrimSpace(r.PostFormValue("choice")) {
	case "accept":
		state.Preferences = true
		state.Analytics = analyticsConfigured
	case "decline":
		// Essential only — Preferences/Analytics stay false.
	case "save":
		state.Preferences = strings.TrimSpace(r.PostFormValue("preferences")) != ""
		state.Analytics = analyticsConfigured && strings.TrimSpace(r.PostFormValue("analytics")) != ""
	default:
		s.renderErrorPage(w, r, http.StatusBadRequest, "Invalid consent choice.")
		return
	}
	// A prior choice that granted Preferences/Analytics storage, now revoked
	// or downgraded, is a consent withdrawal: purge any cache/cookies/storage
	// that non-essential category may have written (AI.md 14078: "On
	// token-revocation and consent-withdrawal responses, also include:
	// Clear-Site-Data: \"cache\", \"cookies\", \"storage\"").
	if hadPrior && ((prior.Preferences && !state.Preferences) || (prior.Analytics && !state.Analytics)) {
		w.Header().Set("Clear-Site-Data", `"cache", "cookies", "storage"`)
	}
	b, err := json.Marshal(state)
	if err != nil {
		b = []byte(`{"essential":true}`)
	}
	secure := r.TLS != nil
	if cfg.Web.CSRF.Secure == "true" {
		secure = true
	} else if cfg.Web.CSRF.Secure == "false" {
		secure = false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "cookie_consent",
		Value:    string(b),
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Return to the referring page so the visitor lands back where they were;
	// fall back to the site root when no same-origin referer is present.
	dest := "/"
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host == r.Host {
			if p, ok := safeRedirectPath(u); ok {
				dest = p
			}
		}
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// announcementView is the template-facing shape of one active, non-dismissed
// site banner entry (AI.md 22517-22605). Icon/Role are pre-resolved here
// rather than in the template so the icon-per-type and ARIA-role-per-type
// mapping (AI.md 22528: role="status" for info/success, role="alert" for
// warning/error) live in one place.
type announcementView struct {
	ID          string
	Type        string
	Title       string
	Message     string
	Dismissible bool
	Icon        string
	Role        string
}

// announcementIcons maps each announcement type to its ARIA-hidden glyph
// (AI.md 22538 shows "⚠" for warning; the other three follow the same
// convention used elsewhere in the spec for toast/banner icons).
var announcementIcons = map[string]string{
	"info":    "ℹ",
	"warning": "⚠",
	"error":   "✕",
	"success": "✓",
}

// dismissedAnnouncementIDs parses the visitor's dismissed_announcements
// cookie (comma-separated announcement ids, AI.md 22529-22530) into a set.
func dismissedAnnouncementIDs(r *http.Request) map[string]bool {
	ids := map[string]bool{}
	c, err := r.Cookie("dismissed_announcements")
	if err != nil || c.Value == "" {
		return ids
	}
	value, err := url.QueryUnescape(c.Value)
	if err != nil {
		value = c.Value
	}
	for _, id := range strings.Split(value, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			ids[id] = true
		}
	}
	return ids
}

// activeAnnouncements returns the operator-configured announcements
// (server.yml web.announcements) whose start-end window is currently active
// and whose id is absent from the visitor's dismissed_announcements cookie,
// in config order (AI.md 22525-22532: disabled/empty = no banner; multiple
// active announcements stack in config order).
func (s *Server) activeAnnouncements(r *http.Request) []announcementView {
	cfg := s.liveCfg()
	if !cfg.Web.Announcements.Enabled || len(cfg.Web.Announcements.Messages) == 0 {
		return nil
	}
	dismissed := dismissedAnnouncementIDs(r)
	now := time.Now().UTC()
	var out []announcementView
	for _, m := range cfg.Web.Announcements.Messages {
		if dismissed[m.ID] {
			continue
		}
		if m.Start != "" {
			if start, err := time.Parse(time.RFC3339, m.Start); err == nil && now.Before(start) {
				continue
			}
		}
		if m.End != "" {
			if end, err := time.Parse(time.RFC3339, m.End); err == nil && now.After(end) {
				continue
			}
		}
		typ := m.Type
		if typ == "" {
			typ = "info"
		}
		role := "status"
		if typ == "warning" || typ == "error" {
			role = "alert"
		}
		out = append(out, announcementView{
			ID:          m.ID,
			Type:        typ,
			Title:       m.Title,
			Message:     m.Message,
			Dismissible: m.Dismissible,
			Icon:        announcementIcons[typ],
			Role:        role,
		})
	}
	return out
}

// handleAnnouncementDismiss appends the submitted announcement id to the
// dismissed_announcements cookie and redirects back to return_to, giving
// zero-JS dismissal of the Site Banner (AI.md 22529, 22541-22545, 22555).
// The cookie is keyed on the announcement id, so changing an announcement's
// id in server.yml reshows the banner for everyone (AI.md 22529).
func (s *Server) handleAnnouncementDismiss(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PostFormValue("id"))
	if id == "" {
		s.renderErrorPage(w, r, http.StatusBadRequest, "Invalid announcement id.")
		return
	}
	ids := dismissedAnnouncementIDs(r)
	ids[id] = true
	list := make([]string, 0, len(ids))
	for existing := range ids {
		list = append(list, existing)
	}
	sort.Strings(list)
	cfg := s.liveCfg()
	secure := r.TLS != nil
	if cfg.Web.CSRF.Secure == "true" {
		secure = true
	} else if cfg.Web.CSRF.Secure == "false" {
		secure = false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "dismissed_announcements",
		Value:    url.QueryEscape(strings.Join(list, ",")),
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Same-site relative paths only (mirrors handleConsentSet's referer check).
	dest := "/"
	if rt := r.PostFormValue("return_to"); rt != "" {
		if u, err := url.Parse(rt); err == nil && u.Host == "" {
			if p, ok := safeRedirectPath(u); ok {
				dest = p
			}
		}
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// validThemes is the closed set of theme modes the server will render as a
// class on <html> (AI.md 24552: theme-light, theme-dark, theme-auto).
var validThemes = map[string]bool{"light": true, "dark": true, "auto": true}

// themeFromRequest resolves the active theme for the current request from the
// server-readable `theme` cookie (AI.md 23474). Precedence: valid cookie value
// → operator's configured default → "dark" (AI.md 23474 fallback, 24552).
func (s *Server) themeFromRequest(r *http.Request) string {
	if c, err := r.Cookie("theme"); err == nil {
		v := strings.TrimSpace(c.Value)
		if validThemes[v] {
			return v
		}
	}
	if def := strings.TrimSpace(s.liveCfg().Web.Theme); validThemes[def] {
		return def
	}
	return "dark"
}

// nextTheme returns the following mode in the dark → light → auto → dark cycle,
// letting the no-JS <noscript> toggle advance one step per POST to /theme.
func nextTheme(cur string) string {
	switch cur {
	case "dark":
		return "light"
	case "light":
		return "auto"
	default:
		return "dark"
	}
}

// handlePreferencesSet is the single POST endpoint behind every no-JS
// preference control on /server/preferences (AI.md 23489: "the server sets
// the same cookies on its POST endpoints"). It persists the `theme` cookie
// and any `pastebin_pref_*` fields present on the form, then redirects back
// so each control feels in-place. The theme cookie is server-readable so the
// next render emits the correct class on <html> with no FOUC (AI.md 21588,
// 23294, 24084).
//
// A form carries only the field it is for, so a bare theme form must not
// clobber the app-specific cookies (and vice versa) — each field is written
// only when it was actually submitted. An app-pref field whose value fails
// validAppPrefValue is ignored rather than stored, so a hand-crafted POST can
// never set a cookie the preferences page has no `<option>` to render.
func (s *Server) handlePreferencesSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderErrorPage(w, r, http.StatusBadRequest, "Invalid form submission.")
		return
	}

	// Only treat `theme` as submitted when the form actually carried the
	// field — an app-pref form omits it, and must not reset the visitor's
	// theme to the default as a side effect.
	if _, submitted := r.PostForm["theme"]; submitted {
		theme := strings.TrimSpace(r.PostFormValue("theme"))
		if !validThemes[theme] {
			theme = "dark"
		}
		s.setPreferenceCookie(w, r, "theme", theme)
	}

	for key, values := range r.PostForm {
		if !strings.HasPrefix(key, prefCookiePrefix) || len(values) == 0 {
			continue
		}
		prefKey := strings.TrimPrefix(key, prefCookiePrefix)
		value := strings.TrimSpace(values[0])
		if !prefKeyPattern.MatchString(prefKey) || !validAppPrefValue(prefKey, value) {
			continue
		}
		s.setPreferenceCookie(w, r, key, value)
	}
	// Return to the referring page so the toggle feels in-place; fall back to
	// the site root when no same-origin referer is present.
	dest := "/"
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host == r.Host {
			if p, ok := safeRedirectPath(u); ok {
				dest = p
			}
		}
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

func (s *Server) handleTerms(w http.ResponseWriter, r *http.Request) {
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "terms.html", s.pageData())
		if err != nil {
			fmt.Fprintf(w, "Terms of Service — %s/server/terms\n", s.baseURL(r))
			return
		}
		fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "terms.html", s.pageData())
}

// ─── Misc handlers ────────────────────────────────────────────────────────────

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	// PWA manifest is revalidated on every load (PART 16) so a new build's icons
	// and metadata are picked up promptly; the version-derived ETag lets the
	// browser short-circuit with 304 when nothing changed.
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+buildinfo.AssetStamp()+`"`)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":             s.liveCfg().Web.SiteTitle,
		"short_name":       "Paste",
		"description":      "A fast, public pastebin service",
		"start_url":        "/",
		"display":          "standalone",
		"background_color": "#1e1e2e",
		"theme_color":      "#89b4fa",
		"icons": []map[string]interface{}{
			{"src": "/static/icons/icon-72.png", "sizes": "72x72", "type": "image/png"},
			{"src": "/static/icons/icon-96.png", "sizes": "96x96", "type": "image/png"},
			{"src": "/static/icons/icon-128.png", "sizes": "128x128", "type": "image/png"},
			{"src": "/static/icons/icon-144.png", "sizes": "144x144", "type": "image/png"},
			{"src": "/static/icons/icon-152.png", "sizes": "152x152", "type": "image/png"},
			{"src": "/static/icons/icon-192.png", "sizes": "192x192", "type": "image/png"},
			{"src": "/static/icons/icon-384.png", "sizes": "384x384", "type": "image/png"},
			{"src": "/static/icons/icon-512.png", "sizes": "512x512", "type": "image/png"},
			{"src": "/static/icons/icon-maskable-192.png", "sizes": "192x192", "type": "image/png", "purpose": "maskable"},
			{"src": "/static/icons/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	})
}

func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	stamp := buildinfo.AssetStamp()
	// The full build stamp changes for every build, including builds in the
	// same release channel, so controlled browsers install the matching worker.
	w.Header().Set("ETag", `"`+stamp+`"`)
	sw := fmt.Sprintf(`// Pastebin Service Worker — build %s
const CACHE_VERSION = %q;
const CACHE_NAME = 'pastebin-cache-' + CACHE_VERSION;
const PRECACHE_ASSETS = [
  '/',
  '/create',
  '/recent',
  '/offline.html',
  '/static/css/common.css',
  '/static/css/components.css',
  '/static/css/public.css',
  '/static/js/app.js',
  '/static/icons/icon-192.png',
  '/static/icons/icon-512.png'
];

// INSTALL — pre-cache assets and activate immediately
self.addEventListener('install', event => {
  event.waitUntil(
    caches.open(CACHE_NAME)
      .then(cache => cache.addAll(PRECACHE_ASSETS))
      .then(() => self.skipWaiting())
  );
});

// ACTIVATE — purge stale caches and claim all clients
self.addEventListener('activate', event => {
  event.waitUntil(
    caches.keys()
      .then(keys => Promise.all(
        keys
          .filter(k => k.startsWith('pastebin-cache-') && k !== CACHE_NAME)
          .map(k => caches.delete(k))
      ))
      .then(() => self.clients.claim())
  );
});

// MESSAGE — allow clients to trigger skipWaiting for instant updates
self.addEventListener('message', event => {
  if (event.data && event.data.type === 'SKIP_WAITING') {
    self.skipWaiting();
  }
});

// FETCH — every response path MUST resolve to a real Response. A promise that
// resolves to undefined — or rejects — makes the browser render net::ERR_FAILED
// instead of a page. Every branch below therefore ends in a guaranteed Response.
self.addEventListener('fetch', event => {
  const { request } = event;
  const url = new URL(request.url);

  // Only same-origin GET is handled here; everything else falls through to the
  // browser untouched (never call respondWith for it)
  if (request.method !== 'GET' || url.origin !== self.location.origin) return;

  // API calls are network-only — never intercept
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/graphql')) return;

  // Navigations (page loads): network-first, then cache, then a GUARANTEED
  // synthesized offline page — a transient network failure renders a real error
  // page, never net::ERR_FAILED
  if (request.mode === 'navigate') {
    event.respondWith(
      fetch(request)
        .then(response => {
          const clone = response.clone();
          caches.open(CACHE_NAME).then(cache => cache.put(request, clone));
          return response;
        })
        .catch(async () =>
          (await caches.match(request))
            || (await caches.match('/offline.html'))
            || offlineFallbackResponse()
        )
    );
    return;
  }

  // Static assets: cache-first, then network, then a GUARANTEED 504 — a failed
  // subresource must never reject respondWith
  if (url.pathname.startsWith('/static/')) {
    event.respondWith(
      caches.match(request)
        .then(cached => cached || fetch(request).then(response => {
          const clone = response.clone();
          caches.open(CACHE_NAME).then(cache => cache.put(request, clone));
          return response;
        }))
        .catch(() => new Response('', { status: 504, statusText: 'Gateway Timeout' }))
    );
    return;
  }

  // Everything else: network-first, then cache, then a GUARANTEED 504
  event.respondWith(
    fetch(request)
      .catch(async () =>
        (await caches.match(request))
          || new Response('', { status: 504, statusText: 'Gateway Timeout' })
      )
  );
});

// GUARANTEED last-resort page — synthesized in the worker so it needs no cache
// hit and can never itself miss
function offlineFallbackResponse() {
  return new Response(
    '<!doctype html><html lang="en"><head><meta charset="utf-8">'
      + '<meta name="viewport" content="width=device-width,initial-scale=1">'
      + '<title>Offline</title></head><body><main>'
      + '<h1>You are offline</h1><p>This page could not be loaded and no '
      + 'cached copy is available. Check your connection and try again.</p>'
      + '</main></body></html>',
    { status: 503, statusText: 'Service Unavailable',
      headers: { 'Content-Type': 'text/html; charset=utf-8' } }
  );
}
`, stamp, stamp)
	w.Write([]byte(sw))
}

// handlePWAIcon serves a generated PNG PWA icon at one of the fixed sizes
// required by the PWA File Structure spec (PART 16): real PNG rasters, not
// SVG — iOS Safari does not reliably render SVG for `apple-touch-icon`.
func (s *Server) handlePWAIcon(size int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		servePWAIconPNG(w, size, false)
	}
}

// handlePWAIconMaskable serves a generated maskable PNG PWA icon (full-bleed
// background, glyph confined to the inner safe zone) at the given size.
func (s *Server) handlePWAIconMaskable(size int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		servePWAIconPNG(w, size, true)
	}
}

func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	b.WriteString("User-agent: *\n")
	for _, p := range s.liveCfg().Web.Robots.Allow {
		b.WriteString("Allow: " + p + "\n")
	}
	for _, p := range s.liveCfg().Web.Robots.Deny {
		b.WriteString("Disallow: " + p + "\n")
	}
	// Sitemap reference (PART 16) — resolved per request so it matches the
	// Host/proto the client actually used (and the onion service over Tor).
	b.WriteString("Sitemap: " + strings.TrimRight(s.baseURL(r), "/") + "/sitemap.xml\n")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}

// handleSecurity serves /.well-known/security.txt (RFC 9116). The Contact line
// is the canonical server.contact.security.email (PART 12); Expires is taken
// from web.security.expires or auto-calculated one year out when unset.
func (s *Server) handleSecurity(w http.ResponseWriter, r *http.Request) {
	cfg := s.liveCfg()
	expires := strings.TrimSpace(cfg.Web.Security.Expires)
	if expires == "" {
		expires = time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)
	}
	// Tor variant (PART 12, AI.md 15836-15848): every absolute URL uses
	// http://{onion}; the mailto line is tor.contact_email only and is omitted
	// entirely when unset (the clearnet email is NEVER disclosed on Tor);
	// Preferred-Languages is omitted to reduce fingerprinting. Built
	// per-request via Tor detection — never cached.
	if onion := s.torRequestOnion(r); onion != "" {
		var b strings.Builder
		fmt.Fprintf(&b, "Contact: %s\n", cfg.SecurityReportURL())
		if id := s.currentSecurityID(); id != "" {
			fmt.Fprintf(&b, "Contact: http://%s/server/contact?security_id=%s\n", onion, id)
		}
		if email := strings.TrimSpace(cfg.Server.Tor.ContactEmail); email != "" {
			fmt.Fprintf(&b, "Contact: mailto:%s\n", email)
		}
		fmt.Fprintf(&b, "Policy: http://%s/server/security\n", onion)
		fmt.Fprintf(&b, "Expires: %s\n", expires)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(b.String()))
		return
	}
	var b strings.Builder
	// Contact lines in RFC 9116 preference order (PART 11 → security.txt):
	// (1) GitHub private vulnerability reporting, (2) instance security-report
	// form with rotating security_id, (3) mailto CC address last.
	fmt.Fprintf(&b, "Contact: %s\n", cfg.SecurityReportURL())
	// Rotating security_id contact (PART 11 → Coordinated Disclosure Pipeline).
	// This is the only place the id is ever emitted; it switches the contact
	// form into security-report mode when a researcher follows it.
	if id := s.currentSecurityID(); id != "" {
		fmt.Fprintf(&b, "Contact: %s/server/contact?security_id=%s\n", strings.TrimRight(s.baseURL(r), "/"), id)
	}
	fmt.Fprintf(&b, "Contact: mailto:%s\n", cfg.SecurityEmail())
	fmt.Fprintf(&b, "Expires: %s\n", expires)
	// Encryption line seeded from the project PGP key (PART 11 → GPG Keypair
	// Management). Emitted only when a key exists and publishing is enabled.
	if s.hasPGPKey() {
		fmt.Fprintf(&b, "Encryption: %s/.well-known/pgp-key.asc\n", strings.TrimRight(s.baseURL(r), "/"))
	}
	fmt.Fprintf(&b, "Preferred-Languages: %s\n", cfg.SecurityPreferredLanguages())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(b.String()))
}

// handleFavicon serves the operator-configured/embedded-default favicon
// (AI.md PART 16 "Image Sources"/"Image Scaling") at its 32x32 size — the
// classic favicon dimension browsers request from this path. Content-Type is
// set explicitly to image/png since browsers sniff content over extension.
func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	s.serveBrandingImage(w, r, "favicon", "32", faviconVariants(), s.liveCfg().Server.Branding.Favicon)
}

// handleBrandingFavicon serves one favicon raster size, e.g.
// /static/branding/favicon-180.png for apple-touch-icon.
func (s *Server) handleBrandingFavicon(size string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveBrandingImage(w, r, "favicon", size, faviconVariants(), s.liveCfg().Server.Branding.Favicon)
	}
}

// handleBrandingLogo serves one logo variant, e.g.
// /static/branding/logo-200.png for the header logo.
func (s *Server) handleBrandingLogo(variant string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.serveBrandingImage(w, r, "logo", variant, logoVariants(), s.liveCfg().Server.Branding.Logo)
	}
}

// handleBrandingOGImage serves the 1200x630 OpenGraph/Twitter card image.
func (s *Server) handleBrandingOGImage(w http.ResponseWriter, r *http.Request) {
	s.serveBrandingImage(w, r, "og-image", "1200x630", ogImageVariants(), s.liveCfg().Server.SEO.OGImage)
}

// serveBrandingImage writes the cached (or on-demand-generated) PNG for one
// branding field/variant, falling back to a freshly rendered embedded
// default on any cache/generation failure so the response is never a 5xx.
func (s *Server) serveBrandingImage(w http.ResponseWriter, r *http.Request, field, variant string, variants map[string][2]int, configuredValue string) {
	data, err := s.brandingCachedPNG(field, variant, variants, configuredValue)
	if err != nil {
		dims := variants[variant]
		w2, h2 := dims[0], dims[1]
		if w2 == 0 {
			w2 = 200
		}
		if h2 == 0 {
			h2 = w2
		}
		fallback, encErr := encodePNG(embeddedDefaultBrandingImage(w2, h2))
		if encErr != nil {
			// Same as the QR route: an image endpoint answering with the
			// canonical JSON envelope, never an HTML error page.
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"ok":      false,
				"error":   "SERVER_ERROR",
				"message": "Image unavailable",
			})
			return
		}
		data = fallback
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(data)
}

// handleOffline serves the PWA offline fallback page.
// The service worker caches this page and serves it when the user is offline
// and no cached version of the requested page is available.
func (s *Server) handleOffline(w http.ResponseWriter, r *http.Request) {
	d := s.pageData()
	s.renderTemplate(w, r, "offline.html", d)
}

// ─── Scheduler API handlers (PART 18) ────────────────────────────────────────

// handleSchedulerList returns all registered tasks.
func (s *Server) handleSchedulerList(w http.ResponseWriter, r *http.Request) {
	if s.schedulerAPI == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"ok": false, "error": "MAINTENANCE", "message": "scheduler not available"})
		return
	}
	tasks := s.schedulerAPI.GetTasks()
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": tasks})
}

// handleSchedulerShow returns the state for a single task.
func (s *Server) handleSchedulerShow(w http.ResponseWriter, r *http.Request) {
	if s.schedulerAPI == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"ok": false, "error": "MAINTENANCE", "message": "scheduler not available"})
		return
	}
	id := chi.URLParam(r, "id")
	t, ok := s.schedulerAPI.GetTask(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "NOT_FOUND", "message": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": t})
}

// handleSchedulerRun triggers a task to run immediately.
func (s *Server) handleSchedulerRun(w http.ResponseWriter, r *http.Request) {
	if s.schedulerAPI == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"ok": false, "error": "MAINTENANCE", "message": "scheduler not available"})
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.schedulerAPI.RunNow(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "BAD_REQUEST", "message": err.Error()})
		return
	}
	// PART 11 audit event: task manually triggered (task name + actor IP).
	s.auditLog(r, audit.Entry{
		Event:    "scheduler.task_manual_run",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Target:   &audit.Target{Type: "scheduler_task", ID: id},
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": map[string]string{"status": "triggered", "task": id}})
}

// handleSchedulerEnable enables a task.
func (s *Server) handleSchedulerEnable(w http.ResponseWriter, r *http.Request) {
	if s.schedulerAPI == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"ok": false, "error": "MAINTENANCE", "message": "scheduler not available"})
		return
	}
	id := chi.URLParam(r, "id")
	if _, ok := s.schedulerAPI.GetTask(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "NOT_FOUND", "message": "task not found"})
		return
	}
	s.schedulerAPI.EnableTask(id)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": map[string]string{"status": "enabled", "task": id}})
}

// handleSchedulerDisable disables a task.
func (s *Server) handleSchedulerDisable(w http.ResponseWriter, r *http.Request) {
	if s.schedulerAPI == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"ok": false, "error": "MAINTENANCE", "message": "scheduler not available"})
		return
	}
	id := chi.URLParam(r, "id")
	if _, ok := s.schedulerAPI.GetTask(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"ok": false, "error": "NOT_FOUND", "message": "task not found"})
		return
	}
	s.schedulerAPI.DisableTask(id)
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": map[string]string{"status": "disabled", "task": id}})
}

// handleSchedulerHistory returns recent execution history for a task.
func (s *Server) handleSchedulerHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	history, err := s.db.ListTaskHistory(id, 20)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"ok": false, "error": "SERVER_ERROR", "message": "could not retrieve history"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": history})
}

// maxPasteSizeDisplay renders the operator-configured paste.max_size as a
// human-readable string for templates. Zero or negative (server.yml
// paste.max_size <= 0) means the operator configured an unlimited paste size,
// shown via the translated "paste.max_size_unlimited" i18n key rather than a
// misleading "0 B" (config.FormatSize only formats positive byte counts).
func (s *Server) maxPasteSizeDisplay(lang string) string {
	bytes := s.liveCfg().Paste.MaxSizeBytes
	if bytes <= 0 {
		return i18n.Translate(lang, "paste.max_size_unlimited")
	}
	return config.FormatSize(bytes)
}

// ─── Template helpers ─────────────────────────────────────────────────────────

func (s *Server) renderTemplate(w http.ResponseWriter, r *http.Request, name string, data map[string]interface{}, status ...int) {
	tmpl := s.templates[name]
	if tmpl == nil {
		log.Printf("template %s not loaded", name)
		s.renderErrorPage(w, r, http.StatusInternalServerError, "internal server error")
		return
	}
	if data == nil {
		data = make(map[string]interface{})
	}
	// Inject the request path so nav.tmpl can mark the active nav-link
	data["CurrentPath"] = r.URL.Path
	// Inject language for i18n — templates access it as .Lang
	lang := i18n.LangFromRequest(r)
	data["Lang"] = lang
	// Inject text direction for RTL languages (Arabic) — templates access it as .Dir
	data["Dir"] = i18n.Direction(lang)
	// Inject the theme resolved from the server-readable `theme` cookie so every
	// <html> renders class="theme-{{.Theme}}" with no init JS and no FOUC (AI.md 23474, 24552)
	theme := s.themeFromRequest(r)
	data["Theme"] = theme
	// Inject the next mode in the cycle so the no-JS toggle form can advance it (AI.md 21588)
	data["NextTheme"] = nextTheme(theme)
	// Inject whether the visitor already has a recorded cookie_consent choice so
	// the footer's no-JS consent form only renders while a decision is still
	// outstanding, matching the JS banner's own show/hide behavior (AI.md 25598).
	data["HasConsented"] = hasConsentCookie(r)
	// Inject CSRF token for forms — templates access it as .CSRFToken
	data["CSRFToken"] = s.csrfTokenFromRequest(r)
	// Inject build metadata for the footer — templates access them as .Version and .BuildDate
	data["Version"] = s.version
	data["BuildDate"] = s.buildDate
	data["CommitID"] = s.commitID
	// Inject the request-resolved absolute base URL ({proto}://{fqdn}[:{port}][{baseurl}])
	// so templates emit full URLs instead of bare paths (AI.md PART 12, 4959)
	if _, ok := data["BaseURL"]; !ok {
		data["BaseURL"] = s.baseURL(r)
	}
	// Inject the path-only prefix used for same-origin subresources so asset URLs
	// stay root-relative and avoid mixed-content blocking behind a TLS proxy
	if _, ok := data["AssetPrefix"]; !ok {
		data["AssetPrefix"] = s.assetPrefix(r)
	}
	// Inject the generated SEO/OpenGraph/Twitter/site-verification <meta> tag
	// block so templates render it as {{.SEOMeta}} inside <head> (PART 16)
	if _, ok := data["SEOMeta"]; !ok {
		data["SEOMeta"] = s.seoMetaTags(r)
	}
	// Inject the operator-configured max paste size as a human-readable string
	// so create/help pages can display the actual limit (server.yml paste.max_size)
	if _, ok := data["MaxPasteSize"]; !ok {
		data["MaxPasteSize"] = s.maxPasteSizeDisplay(lang)
	}
	// Inject sanitized operator footer branding rendered above the default footer (PART 16)
	s.injectFooterData(data)
	// Inject Tor hidden-service status so footers/help can show the "Tor Support"
	// link and onion address only when the service is enabled and running (PART 31)
	s.injectTorData(data)
	s.injectI2PData(data)
	// Inject the header profile/preferences zone's owner_token-dependent state
	// (AI.md "Header Layout" / "Profile/Preferences Zone", PART 16)
	s.injectOwnerTokenData(r, data)
	// Inject the active, non-dismissed site-wide announcements rendered as the
	// Site Banner, first element inside <body> (AI.md 22517-22605, 26078-26121)
	data["Announcements"] = s.activeAnnouncements(r)
	// Render into a buffer first so a mid-template failure never leaks a
	// partially-written, corrupted response — headers/status are only sent
	// once rendering has fully succeeded. On failure, fall back through
	// renderErrorPage's content-negotiated response (HTML for browsers,
	// JSON envelope for API/non-interactive clients) instead of always
	// writing a JSON body (AI.md PART 16, Error Pages).
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("template %s error: %v", name, err)
		s.renderErrorPage(w, r, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Every HTML document is uncacheable and carries the build-stamp ETag, and
	// every HTML response refreshes the build cookie so a browser holding caches
	// from an older build is purged exactly once (AI.md PART 9, 13287 and
	// 13323-13351). This is the single HTML choke point, so static, API and
	// /sw.js responses can never pick these up.
	setHTMLCacheHeaders(w)
	s.versionPurge(w, r)
	statusCode := http.StatusOK
	if len(status) > 0 {
		statusCode = status[0]
	}
	w.WriteHeader(statusCode)
	buf.WriteTo(w)
}

// renderTemplateToString renders a named template to a string instead of writing to a ResponseWriter.
// Used by handlers that need to apply HTML2TextConverter for plain-text clients.
func (s *Server) renderTemplateToString(r *http.Request, name string, data map[string]interface{}) (string, error) {
	tmpl := s.templates[name]
	if tmpl == nil {
		return "", fmt.Errorf("templates not loaded")
	}
	if data == nil {
		data = make(map[string]interface{})
	}
	data["CurrentPath"] = r.URL.Path
	lang := i18n.LangFromRequest(r)
	data["Lang"] = lang
	data["Dir"] = i18n.Direction(lang)
	// Inject the theme resolved from the server-readable `theme` cookie (AI.md 23474)
	theme := s.themeFromRequest(r)
	data["Theme"] = theme
	data["NextTheme"] = nextTheme(theme)
	// Inject build metadata for the footer — templates access them as .Version and .BuildDate
	data["Version"] = s.version
	data["BuildDate"] = s.buildDate
	data["CommitID"] = s.commitID
	if _, ok := data["BaseURL"]; !ok {
		data["BaseURL"] = s.baseURL(r)
	}
	if _, ok := data["AssetPrefix"]; !ok {
		data["AssetPrefix"] = s.assetPrefix(r)
	}
	// Inject the operator-configured max paste size as a human-readable string
	// so create/help pages can display the actual limit (server.yml paste.max_size)
	if _, ok := data["MaxPasteSize"]; !ok {
		data["MaxPasteSize"] = s.maxPasteSizeDisplay(lang)
	}
	// Keep parity with renderTemplate: headAssets always references .SEOMeta,
	// so it must be populated here too or a missing-map-key placeholder would
	// leak into the <head> of every HTML2Text-converted text/plain response.
	if _, ok := data["SEOMeta"]; !ok {
		data["SEOMeta"] = s.seoMetaTags(r)
	}
	s.injectFooterData(data)
	s.injectTorData(data)
	s.injectI2PData(data)
	s.injectOwnerTokenData(r, data)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (s *Server) pageData() map[string]interface{} {
	return map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
	}
}

// injectFooterData adds the sanitized operator footer branding rendered above
// the default application footer (PART 16 Footer Customization). The value is
// operator-supplied config content already stripped by bluemonday, so wrapping
// it as template.HTML is safe; it is never user-submitted content. An empty
// result (default or disabled branding) drops the branding row entirely.
func (s *Server) injectFooterData(data map[string]interface{}) {
	if html := s.liveCfg().FooterCustomHTML(); html != "" {
		data["FooterCustomHTML"] = template.HTML(html)
	} else {
		data["FooterCustomHTML"] = template.HTML("")
	}
}

// injectTorData adds the Tor hidden-service status fields used by page footers
// and the /server/help Tor Access section (PART 31). TorEnabled reflects that a
// hidden service is configured; TorRunning that it is currently active; the
// onion address is only meaningful while running.
func (s *Server) injectTorData(data map[string]interface{}) {
	running := s.TorRunning()
	data["TorEnabled"] = s.torManager != nil
	data["TorRunning"] = running
	if running {
		data["OnionAddress"] = s.TorOnionAddress()
	} else {
		data["OnionAddress"] = ""
	}
}

// injectI2PData adds the I2P eepsite status fields used by page footers and
// the /server/help I2P Access section (PART 31.2). I2PEnabled reflects that
// the eepsite is configured (opt-in); I2PRunning that it is currently
// active; the .b32.i2p address is only meaningful while running.
func (s *Server) injectI2PData(data map[string]interface{}) {
	running := s.I2PRunning()
	data["I2PEnabled"] = s.i2pManager != nil
	data["I2PRunning"] = running
	if running {
		data["I2PAddress"] = s.I2PAddress()
	} else {
		data["I2PAddress"] = ""
	}
}

// injectOwnerTokenData adds the header profile/preferences zone's state fields
// (AI.md "Header Layout" / "Profile/Preferences Zone", PART 16). HasOwnerToken
// reflects whether the visitor's owner_token cookie still resolves to a live,
// non-revoked, non-expired paste; when it does, OwnedResourceURL/ResourceLabel
// back the dropdown's "Manage my paste" link so it goes straight to that paste
// instead of just to /server/preferences. An invalid/stale/missing cookie is
// not an error — it just falls back to the plain preferences link.
func (s *Server) injectOwnerTokenData(r *http.Request, data map[string]interface{}) {
	data["HasOwnerToken"] = false
	data["OwnedResourceURL"] = ""
	data["ResourceLabel"] = ""
	c, err := r.Cookie(ownerTokenCookieName)
	if err != nil || c.Value == "" {
		return
	}
	tokenHash := sha256.Sum256([]byte(c.Value))
	resourceID, err := s.db.FindResourceIDByToken(tokenHash, "paste")
	if err != nil || resourceID == "" {
		return
	}
	data["HasOwnerToken"] = true
	data["OwnedResourceURL"] = s.assetPrefix(r) + "/" + resourceID
	// The dropdown's visible link text uses the full pre-translated
	// nav.manage_my_paste sentence (grammar/word-order differs too much
	// across the 7 supported languages to safely concatenate "Manage my"
	// with a bare noun); ResourceLabel is kept as plain data for callers
	// that need the resource kind on its own.
	data["ResourceLabel"] = "paste"
}

// injectTermbinData adds the termbin config fields used by /server/help to
// display the raw-TCP usage section when termbin is enabled (PART 18).
func (s *Server) injectTermbinData(r *http.Request, data map[string]interface{}) {
	cfg := s.liveCfg()
	tb := cfg.Server.Termbin
	data["TermbinEnabled"] = tb.Enabled
	if tb.Enabled {
		// Use the same FQDN resolution as every other example on this page
		// (BaseURL override / trusted proxy headers / request Host, PART 12)
		// rather than the bind address or machine hostname — otherwise the nc
		// example shows a different host than the curl examples on the page.
		host := r.Host
		if u, err := url.Parse(s.baseURL(r)); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		data["TermbinHost"] = host
		data["TermbinPort"] = tb.Port
		data["TermbinMaxSize"] = fmt.Sprintf("%d", tb.MaxSize)
		data["TermbinTimeout"] = tb.Timeout
	} else {
		data["TermbinHost"] = ""
		data["TermbinPort"] = 0
		data["TermbinMaxSize"] = ""
		data["TermbinTimeout"] = ""
	}
}

// recoverer recovers from panics and renders a themed 500 error page (PART 16),
// replacing chi's plain-text Recoverer. Full detail is logged server-side; the
// client never receives a stack trace.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil && rec != http.ErrAbortHandler {
				log.Printf("panic recovered: %v\n%s", rec, debug.Stack())
				s.logManager.Error("panic recovered",
					"panic", fmt.Sprintf("%v", rec),
					"method", r.Method,
					"path", r.URL.Path,
					"ip", clientIP(r))
				s.renderErrorPage(w, r, http.StatusInternalServerError, "An internal server error occurred.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// errorCodeForStatus maps an HTTP status to its canonical API error code (PART 14).
func errorCodeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusBadGateway:
		return "BAD_GATEWAY"
	case http.StatusServiceUnavailable:
		return "MAINTENANCE"
	default:
		return "SERVER_ERROR"
	}
}

// renderErrorPage renders a themed error page for browser clients or a canonical
// JSON envelope for API/non-interactive clients (PART 16 error-page requirements).
// No plain/unstyled browser error pages are ever produced.
func (s *Server) renderErrorPage(w http.ResponseWriter, r *http.Request, status int, message string) {
	statusText := http.StatusText(status)
	if message == "" {
		message = statusText
	}
	// API and non-interactive clients receive the canonical error envelope.
	if detectClientType(r) != "html" {
		writeJSON(w, status, map[string]interface{}{
			"ok":      false,
			"error":   errorCodeForStatus(status),
			"message": message,
		})
		return
	}
	// Browser clients receive the themed error template.
	data := s.pageData()
	data["StatusCode"] = status
	data["StatusText"] = statusText
	data["Message"] = message
	tmpl := s.templates["error.html"]
	if tmpl == nil {
		// Degraded bootstrap state: error.html failed to parse, so the themed
		// page cannot be rendered. Fall back to the canonical JSON envelope so
		// this path still never emits a bare unstyled text error page. Nothing
		// has been written to w yet, so the status is still unset here.
		httputil.WriteJSON(w, status, map[string]any{
			"ok":      false,
			"error":   errorCodeForStatus(status),
			"message": message,
		})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data["CurrentPath"] = r.URL.Path
	lang := i18n.LangFromRequest(r)
	data["Lang"] = lang
	data["Dir"] = i18n.Direction(lang)
	theme := s.themeFromRequest(r)
	data["Theme"] = theme
	data["NextTheme"] = nextTheme(theme)
	data["CSRFToken"] = s.csrfTokenFromRequest(r)
	data["Version"] = s.version
	data["BuildDate"] = s.buildDate
	data["CommitID"] = s.commitID
	if _, ok := data["BaseURL"]; !ok {
		data["BaseURL"] = s.baseURL(r)
	}
	if _, ok := data["AssetPrefix"]; !ok {
		data["AssetPrefix"] = s.assetPrefix(r)
	}
	if _, ok := data["SEOMeta"]; !ok {
		data["SEOMeta"] = s.seoMetaTags(r)
	}
	s.injectFooterData(data)
	s.injectTorData(data)
	s.injectI2PData(data)
	s.injectOwnerTokenData(r, data)
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("error template render failed: %v", err)
	}
}

// persistOnionAddress records the generated .onion hostname under
// server.tor.onion_address on the first successful Tor bootstrap (PART 12:
// "Set automatically on first successful Tor bootstrap"). The stored address
// is what enables Tor request detection (baseURL priority 0, the Tor
// security.txt variant, and Tor CORS). Runs in Run() before HTTP serving
// starts, so writing s.cfg here is race-free. Already-set addresses are
// never overwritten.
func (s *Server) persistOnionAddress() {
	onion := strings.TrimSpace(s.TorOnionAddress())
	if onion == "" || strings.TrimSpace(s.cfg.Server.Tor.OnionAddress) != "" {
		return
	}
	s.cfg.Server.Tor.OnionAddress = onion
	if live := s.liveCfg(); live != s.cfg {
		live.Server.Tor.OnionAddress = onion
	}
	cfgPath := filepath.Join(s.configDir, "server.yml")
	if err := config.SetOnionAddress(cfgPath, onion); err != nil {
		log.Printf("tor: persisting onion address to %s failed: %v", cfgPath, err)
	}
}

// persistB32Address writes the .b32.i2p address to server.i2p.b32_address on
// the first successful I2P bootstrap (PART 31.2, mirroring
// persistOnionAddress's PART 12 behavior). The stored address enables I2P
// request detection (baseURL priority 0, contact-email privacy rules).
// Runs in Run() before HTTP serving starts, so writing s.cfg here is
// race-free. Already-set addresses are never overwritten.
func (s *Server) persistB32Address() {
	b32 := strings.TrimSpace(s.I2PAddress())
	if b32 == "" || strings.TrimSpace(s.cfg.Server.I2P.B32Address) != "" {
		return
	}
	s.cfg.Server.I2P.B32Address = b32
	if live := s.liveCfg(); live != s.cfg {
		live.Server.I2P.B32Address = b32
	}
	cfgPath := filepath.Join(s.configDir, "server.yml")
	if err := config.SetB32Address(cfgPath, b32); err != nil {
		log.Printf("i2p: persisting b32 address to %s failed: %v", cfgPath, err)
	}
}

// torRequestOnion returns the configured onion address when the request
// arrived via the Tor hidden service — the request Host (after port
// stripping) matches tor.onion_address (PART 12 priority-0 Tor detection).
// Returns "" for clearnet requests or when no onion address is configured.
func (s *Server) torRequestOnion(r *http.Request) string {
	if r == nil {
		return ""
	}
	onion := strings.TrimSpace(s.cfg.Server.Tor.OnionAddress)
	if onion == "" {
		return ""
	}
	reqHost := r.Host
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		reqHost = h
	}
	if strings.EqualFold(reqHost, onion) {
		return onion
	}
	return ""
}

// i2pRequestB32 returns the configured .b32.i2p address when the request
// arrived via the I2P eepsite — the request Host (after port stripping)
// matches i2p.b32_address (PART 31.2 priority-0 I2P detection, same trust
// chain as torRequestOnion). Returns "" for clearnet requests or when no
// I2P address is configured (I2P disabled or not yet bootstrapped).
func (s *Server) i2pRequestB32(r *http.Request) string {
	if r == nil {
		return ""
	}
	b32 := strings.TrimSpace(s.cfg.Server.I2P.B32Address)
	if b32 == "" {
		return ""
	}
	reqHost := r.Host
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		reqHost = h
	}
	if strings.EqualFold(reqHost, b32) {
		return b32
	}
	return ""
}

// publicContactEmail returns the general contact address safe to disclose for
// this request. Tor/I2P responses show tor.contact_email only — when it is
// unset no email is shown; the clearnet email is NEVER a fallback (PART 12
// Tor privacy rules, extended to .b32.i2p per PART 31.2).
func (s *Server) publicContactEmail(r *http.Request) string {
	if s.torRequestOnion(r) != "" || s.i2pRequestB32(r) != "" {
		return strings.TrimSpace(s.liveCfg().Server.Tor.ContactEmail)
	}
	return s.liveCfg().GeneralEmailPublic()
}

// publicAbuseEmail returns the abuse contact address safe to disclose for
// this request — same Tor/I2P privacy rule as publicContactEmail.
func (s *Server) publicAbuseEmail(r *http.Request) string {
	if s.torRequestOnion(r) != "" || s.i2pRequestB32(r) != "" {
		return strings.TrimSpace(s.liveCfg().Server.Tor.ContactEmail)
	}
	return s.liveCfg().AbuseEmailPublic()
}

// publicSecurityEmail returns the security contact address safe to disclose
// for this request — same Tor/I2P privacy rule as publicContactEmail.
func (s *Server) publicSecurityEmail(r *http.Request) string {
	if s.torRequestOnion(r) != "" || s.i2pRequestB32(r) != "" {
		return strings.TrimSpace(s.liveCfg().Server.Tor.ContactEmail)
	}
	return s.liveCfg().SecurityEmail()
}

// baseURL constructs the base URL for the current request (PART 12).
// Resolution order (highest priority first):
//  0. Tor: when Host matches tor.onion_address → http://{onion_address} (no port, always http)
//  1. Config/CLI: server.base_url
//  2. X-Forwarded-Prefix header (from trusted reverse proxy)
//  3. X-Forwarded-Path header (alternative prefix header)
//  4. X-Script-Name header (WSGI-style)
//  5. Default: scheme://host derived from TLS state and X-Forwarded-Proto
//
// X-Forwarded-* headers are only honored when the immediate peer is a trusted
// proxy (loopback, private ranges, or server.trusted_proxies.additional).
func (s *Server) baseURL(r *http.Request) string {
	// Priority 0: Tor hidden service — when the request Host matches the configured
	// onion address, resolve unconditionally as http://{onion_address}. No port, always
	// http, no proxy header inspection, no IP check (PART 12 priority-0 rule).
	if onion := s.torRequestOnion(r); onion != "" {
		return "http://" + onion
	}

	// Priority 0: I2P eepsite — identical rule for .b32.i2p (PART 31.2 "Trust
	// chain integration": trusted for FQDN resolution exactly like .onion).
	if b32 := s.i2pRequestB32(r); b32 != "" {
		return "http://" + b32
	}

	if s.cfg.Server.BaseURL != "" {
		return s.cfg.Server.BaseURL
	}

	trusted := s.isTrustedPeer(r)

	// {proto} — trusted reverse-proxy headers win over the connection's TLS
	// state, in PART 12 priority order (X-Forwarded-Proto → X-Forwarded-Ssl →
	// X-Url-Scheme); otherwise fall back to the actual TLS state.
	scheme := "http"
	if fp := forwardedScheme(r, trusted); fp != "" {
		scheme = fp
	} else if r.TLS != nil {
		scheme = "https"
	}

	// {fqdn} — trusted proxy host headers in priority order, else the request
	// Host (PART 12).
	host := r.Host
	hostFromProxy := false
	if trusted {
		for _, hdr := range []string{"X-Forwarded-Host", "X-Real-Host", "X-Original-Host"} {
			if fh := strings.TrimSpace(r.Header.Get(hdr)); fh != "" {
				host = fh
				hostFromProxy = true
				break
			}
		}
	}

	// {port} — split host, let a trusted X-Forwarded-Port override, then strip
	// the standard :80/:443 which are NEVER included in URLs (PART 12).
	hostname, port := splitHostPort(host)

	// {fqdn} fallback (PART 12 priority 2+): when no trusted proxy supplied a
	// Host header and the request Host is empty or a non-routable literal — e.g.
	// an nginx `proxy_pass https://127.0.0.1:PORT/` upstream that does not forward
	// the original Host, so r.Host is the internal loopback/private address — fall
	// back to the configured DOMAIN/FQDN. Absolute template URLs must render the
	// public host the client actually reached, never the internal upstream.
	if !hostFromProxy && !isRoutableHost(hostname) {
		if f := strings.TrimSpace(s.cfg.ResolveFQDN()); f != "" && !strings.EqualFold(f, "localhost") {
			hostname, port = splitHostPort(f)
		}
	}
	if trusted {
		if fp := strings.TrimSpace(r.Header.Get("X-Forwarded-Port")); fp != "" {
			port = fp
		}
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	hostport := hostname
	if port != "" {
		hostport = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		hostport = "[" + hostname + "]"
	}

	base := scheme + "://" + hostport

	if trusted {
		// Append reverse-proxy path prefix when present (trailing slash stripped).
		for _, hdr := range []string{"X-Forwarded-Prefix", "X-Forwarded-Path", "X-Script-Name"} {
			if prefix := r.Header.Get(hdr); prefix != "" {
				prefix = strings.TrimRight(prefix, "/")
				if validPathPrefix(prefix) {
					base += prefix
				}
				break
			}
		}
	}
	return base
}

// forwardedScheme resolves {proto} from trusted reverse-proxy headers in the
// PART 12 priority order: X-Forwarded-Proto → X-Forwarded-Ssl (on=https) →
// X-Url-Scheme. Returns "" when no trusted header dictates a scheme, letting the
// caller fall back to the connection's TLS state. Headers are ignored entirely
// when the immediate peer is not a trusted proxy (header-spoofing guard).
func forwardedScheme(r *http.Request, trusted bool) string {
	if !trusted {
		return ""
	}
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); v != "" {
		if strings.EqualFold(v, "https") {
			return "https"
		}
		if strings.EqualFold(v, "http") {
			return "http"
		}
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Ssl")), "on") {
		return "https"
	}
	if v := strings.TrimSpace(r.Header.Get("X-Url-Scheme")); v != "" {
		if strings.EqualFold(v, "https") {
			return "https"
		}
		if strings.EqualFold(v, "http") {
			return "http"
		}
	}
	return ""
}

// splitHostPort separates a Host value into hostname and port, tolerating a
// missing port. A bracketed IPv6 literal without a port is unwrapped so callers
// can re-join it with net.JoinHostPort.
func splitHostPort(host string) (hostname, port string) {
	if h, p, err := net.SplitHostPort(host); err == nil {
		return h, p
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return host[1 : len(host)-1], ""
	}
	return host, ""
}

// isRoutableHost reports whether h is usable as a public {fqdn} in a rendered
// absolute URL. A registered domain name (net.ParseIP == nil) is always
// considered routable. An IP literal is routable only when it is a global
// unicast address that is not loopback, private (RFC 1918 / RFC 4193), or
// link-local. An empty host is never routable. This gates the configured-FQDN
// fallback in baseURL so an internal upstream literal (127.0.0.1, 10.x, etc.)
// forwarded as the Host header is replaced by the operator's real domain.
func isRoutableHost(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" || strings.EqualFold(h, "localhost") {
		return false
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	return ip.IsGlobalUnicast()
}

// validPathPrefix reports whether p is a safe rooted URL path prefix. It must
// begin with a single "/", must not be protocol-relative ("//" or "/\") and
// must contain no scheme separator, backslash, whitespace or control character.
// This blocks a header-injected value such as "//evil.com" from reaching an
// href/src attribute (html/template's URL filter permits protocol-relative
// URLs, so escaping alone is not sufficient). An empty string is not a valid
// prefix — callers treat that as "no prefix" and render root-relative.
func validPathPrefix(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return false
	}
	for _, ch := range p {
		if ch == ':' || ch == '\\' || ch <= ' ' || ch == 0x7f {
			return false
		}
	}
	return true
}

// assetPrefix returns the path-only base prefix for same-origin subresources
// (stylesheets, scripts, icons, manifest). It is empty by default so those URLs
// render root-relative and inherit the page's actual scheme, preventing
// mixed-content blocking when TLS is terminated at a reverse proxy (a full
// scheme://host would emit http:// on an https page and the browser drops it).
// A non-empty prefix is only produced for a sub-path mount advertised by a
// trusted proxy or configured base URL (AI.md PART 12 {baseurl} chain).
func (s *Server) assetPrefix(r *http.Request) string {
	if s.isTrustedPeer(r) {
		for _, hdr := range []string{"X-Forwarded-Prefix", "X-Forwarded-Path", "X-Script-Name"} {
			if p := strings.TrimRight(r.Header.Get(hdr), "/"); p != "" {
				if validPathPrefix(p) {
					return p
				}
				break
			}
		}
	}
	if b := s.cfg.Server.BaseURL; b != "" {
		if u, err := url.Parse(b); err == nil {
			if p := strings.TrimRight(u.Path, "/"); validPathPrefix(p) {
				return p
			}
		}
	}
	return ""
}

// privateNets contains the CIDR ranges that are always treated as trusted proxies
// (loopback, RFC 1918 IPv4 private, RFC 4193 IPv6 unique-local, link-local).
var privateNets = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",
		"::1/128",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"fc00::/7",
		"169.254.0.0/16",
		"fe80::/10",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			nets = append(nets, ipNet)
		}
	}
	return nets
}()

// peerAddrKey is the context key under which realIPMiddleware preserves the
// immediate TCP peer address (the pre-rewrite r.RemoteAddr). The X-Forwarded-*
// trust gate MUST evaluate the immediate peer, not the resolved client IP
// (PART 12 "Used by X-Forwarded-* trust gate").
type peerAddrKeyType struct{}

var peerAddrKey peerAddrKeyType

// peerAddr returns the immediate TCP peer address for the request: the value
// preserved by realIPMiddleware before it rewrote r.RemoteAddr to the resolved
// client IP, falling back to r.RemoteAddr when the middleware has not run.
func peerAddr(r *http.Request) string {
	if v, ok := r.Context().Value(peerAddrKey).(string); ok && v != "" {
		return v
	}
	return r.RemoteAddr
}

// isTrustedPeer returns true when the immediate peer's IP is a loopback,
// private-range address, or in server.trusted_proxies.additional (PART 12).
// It checks the connection's original peer address (preserved by
// realIPMiddleware) — never the client IP resolved from proxy headers, which
// would make every proxied request look untrusted and break {proto}/{fqdn}
// detection for TLS-terminating reverse proxies.
func (s *Server) isTrustedPeer(r *http.Request) bool {
	host, _, err := net.SplitHostPort(peerAddr(r))
	if err != nil {
		host = peerAddr(r)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range privateNets {
		if n.Contains(ip) {
			return true
		}
	}
	// Same /24 as the configured listen address — reverse-proxy sidecar pattern
	// where a proxy on the same routable subnet terminates TLS (AI.md PART 12
	// Trusted Proxies always-trusted list). Only meaningful for a concrete IPv4
	// listen address; 0.0.0.0/empty/hostname/IPv6-any yield no usable /24 match.
	if listenIP := net.ParseIP(strings.TrimSpace(s.cfg.Server.Address)); listenIP != nil {
		if ip4, listen4 := ip.To4(), listenIP.To4(); ip4 != nil && listen4 != nil {
			mask := net.CIDRMask(24, 32)
			if ip4.Mask(mask).Equal(listen4.Mask(mask)) {
				return true
			}
		}
	}
	// Check server.trusted_proxies.additional entries (IPs and CIDRs).
	for _, entry := range s.cfg.Server.TrustedProxies.Additional {
		if strings.Contains(entry, "/") {
			if _, ipNet, err := net.ParseCIDR(entry); err == nil && ipNet.Contains(ip) {
				return true
			}
		} else if net.ParseIP(entry) != nil && net.ParseIP(entry).Equal(ip) {
			return true
		}
	}
	// Check DNS-resolved IPs for hostname entries in trusted_proxies.additional
	// (resolved at startup and refreshed every 5 minutes — PART 12).
	s.resolvedProxyMu.RLock()
	for _, ips := range s.resolvedProxyIPs {
		for _, resolvedIP := range ips {
			if resolvedIP.Equal(ip) {
				s.resolvedProxyMu.RUnlock()
				return true
			}
		}
	}
	s.resolvedProxyMu.RUnlock()
	return false
}

// refreshDNSTrustedProxies resolves every hostname-valued entry in
// server.trusted_proxies.additional and caches the resulting IPs in
// s.resolvedProxyIPs.  IP and CIDR entries are skipped (they need no
// resolution).  Called once at startup and every 5 minutes by Run (PART 12).
func (s *Server) refreshDNSTrustedProxies(ctx context.Context) {
	additional := s.liveCfg().Server.TrustedProxies.Additional
	fresh := make(map[string][]net.IP, len(additional))
	for _, entry := range additional {
		// Skip plain IPs and CIDRs — they are handled directly in isTrustedPeer.
		if strings.Contains(entry, "/") || net.ParseIP(entry) != nil {
			continue
		}
		// entry is a hostname — resolve it.
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, entry)
		if err != nil {
			log.Printf("trusted_proxies: DNS lookup for %q failed: %v", entry, err)
			continue
		}
		ips := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
		fresh[entry] = ips
	}
	s.resolvedProxyMu.Lock()
	s.resolvedProxyIPs = fresh
	s.resolvedProxyMu.Unlock()
}

// realIPMiddleware extracts the real client IP from trusted proxy headers and
// updates r.RemoteAddr accordingly.  Only headers from a trusted peer (as
// determined by isTrustedPeer) are honoured; untrusted headers are ignored.
// Priority order (PART 12): CF-Connecting-IP → True-Client-IP → X-Real-IP →
// X-Forwarded-For (leftmost) → X-Client-IP.
func (s *Server) realIPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Preserve the immediate TCP peer before any rewrite: the trust gate
		// for every downstream X-Forwarded-* consumer (baseURL {proto}/{fqdn},
		// X-Forwarded-Prefix, domain learning) must judge the connection's
		// peer, not the client IP substituted below (PART 12).
		r = r.WithContext(context.WithValue(r.Context(), peerAddrKey, r.RemoteAddr))
		if s.isTrustedPeer(r) {
			// Preserve original port if present so RemoteAddr stays host:port.
			_, port, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				port = ""
			}
			var realIP string
			for _, hdr := range []string{
				"CF-Connecting-IP",
				"True-Client-IP",
				"X-Real-IP",
			} {
				if v := r.Header.Get(hdr); v != "" {
					realIP = strings.TrimSpace(v)
					break
				}
			}
			// X-Forwarded-For may contain a comma-separated list; use the leftmost.
			if realIP == "" {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
					parts := strings.SplitN(xff, ",", 2)
					realIP = strings.TrimSpace(parts[0])
				}
			}
			// X-Client-IP as final fallback (Akamai and some custom proxies).
			if realIP == "" {
				if v := r.Header.Get("X-Client-IP"); v != "" {
					realIP = strings.TrimSpace(v)
				}
			}
			if realIP != "" && net.ParseIP(realIP) != nil {
				if port != "" {
					r.RemoteAddr = net.JoinHostPort(realIP, port)
				} else {
					r.RemoteAddr = realIP
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// domainObserveMiddleware feeds incoming host values into the domain learner
// so it can infer baseDomain / wildcardDomain over time (PART 12
// url_detection).  Only observations from trusted peers are recorded, and only
// the X-Forwarded-Host header is used — r.Host is client-controlled and must
// never be trusted for this purpose.
func (s *Server) domainObserveMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.domainLearner != nil && s.isTrustedPeer(r) {
			host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			if host != "" {
				s.domainLearner.Observe(host)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// langCookieMiddleware persists a valid `?lang=` query parameter as the
// `lang` cookie on ordinary page navigation, mirroring setPreferenceCookie
// (AI.md Client-Side Preferences: "?lang= sets a persistent cookie"). Only
// the preferences-import flow used to do this; every other route left the
// override request-scoped only. An unsupported value is left for
// i18n.LangFromRequest to silently fall back to "en" — never written here.
func (s *Server) langCookieMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lang := r.URL.Query().Get("lang"); lang != "" && i18n.IsSupported(lang) {
			s.setPreferenceCookie(w, r, "lang", strings.ToLower(lang))
		}
		next.ServeHTTP(w, r)
	})
}

// mcpOriginAllowed validates an MCP request's Origin header against the
// request's own resolved origin (DNS-rebinding protection, PART 11): the Origin
// host must equal the host this request was served on.
func (s *Server) mcpOriginAllowed(r *http.Request) bool {
	u, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || u.Host == "" {
		return false
	}
	own, err := url.Parse(s.baseURL(r))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, own.Host)
}
