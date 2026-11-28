package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"

	"net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/cache"
	"github.com/apimgr/cairnbin/src/common/browsercookie"
	"github.com/apimgr/cairnbin/src/common/buildinfo"
	"github.com/apimgr/cairnbin/src/common/email"
	"github.com/apimgr/cairnbin/src/common/httputil"
	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/common/listener"
	"github.com/apimgr/cairnbin/src/common/theme"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/geoip"
	"github.com/apimgr/cairnbin/src/graphql"
	"github.com/apimgr/cairnbin/src/handler"
	"github.com/apimgr/cairnbin/src/handler/compat"
	"github.com/apimgr/cairnbin/src/health"
	"github.com/apimgr/cairnbin/src/i2p"
	"github.com/apimgr/cairnbin/src/logging"
	"github.com/apimgr/cairnbin/src/mcp"
	"github.com/apimgr/cairnbin/src/metric"
	"github.com/apimgr/cairnbin/src/mode"
	"github.com/apimgr/cairnbin/src/model"
	"github.com/apimgr/cairnbin/src/notify"
	"github.com/apimgr/cairnbin/src/paths"
	"github.com/apimgr/cairnbin/src/ssl"
	"github.com/apimgr/cairnbin/src/swagger"
	"github.com/apimgr/cairnbin/src/tor"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/text/unicode/norm"
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
	Database  string                `json:"database"`
	Cache     string                `json:"cache"`
	Disk      string                `json:"disk"`
	Scheduler string                `json:"scheduler"`
	Tor       string                `json:"tor,omitempty"`
	I2P       string                `json:"i2p,omitempty"`
	Datasets  []health.DatasetCheck `json:"-"`
	// Config is an APP-SPECIFIC check (AI.md:17202 "Add your checks here").
	Config string `json:"config"`
}

func (c ChecksInfo) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	write := func(key string, value string) error {
		if !first {
			out.WriteByte(',')
		}
		first = false
		name, err := json.Marshal(key)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		out.Write(name)
		out.WriteByte(':')
		out.Write(encoded)
		return nil
	}
	for _, item := range []struct{ key, value string }{{"database", c.Database}, {"cache", c.Cache}, {"disk", c.Disk}, {"scheduler", c.Scheduler}} {
		if err := write(item.key, item.value); err != nil {
			return nil, err
		}
	}
	if c.Tor != "" {
		if err := write("tor", c.Tor); err != nil {
			return nil, err
		}
	}
	if c.I2P != "" {
		if err := write("i2p", c.I2P); err != nil {
			return nil, err
		}
	}
	for _, check := range c.Datasets {
		if err := write(check.Key, check.Status); err != nil {
			return nil, err
		}
	}
	if c.Config != "" {
		if err := write("config", c.Config); err != nil {
			return nil, err
		}
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// StatsInfo holds public-safe aggregate statistics.
type StatsInfo struct {
	RequestsTotal int64 `json:"requests_total"`
	Requests24h   int64 `json:"requests_24h"`
	ActiveConns   int   `json:"active_connections"`
	PastesTotal   int64 `json:"pastes_total"`
}

// SchedulerStatus provides a read-only task snapshot for the protected debug route.
type SchedulerStatus interface {
	GetTasks() []database.TaskState
}

// Server owns the HTTP router and all handler dependencies.
type Server struct {
	router               *chi.Mux
	db                   database.DB
	cacheStore           cache.Cache
	cacheDegraded        bool
	cfg                  *config.Config
	cfgMgr               *config.ConfigManager
	lastSMTPConfig       config.EmailConfig
	lastEmailLanguage    string
	lastTorConfig        config.TorConfig
	lastI2PConfig        config.I2PConfig
	lastBlocklistsConfig config.BlocklistsConfig
	lastGeoIPConfig      config.GeoIPConfig
	lastCVEConfig        config.CVEConfig
	lastTrivyConfig      config.TrivyConfig
	emailConfigChangeFn  func(config.EmailConfig, string)
	templates            map[string]*template.Template
	pasteHandler         *handler.PasteHandler
	compatHandler        *compat.Handler
	swaggerHandler       *swagger.Handler
	mcpHandler           *mcp.Handler
	// ipBlocks holds temporary (auto) IP blocks and flood counters (PART 11).
	ipBlocks           *ipBlocks
	graphqlHandler     *graphql.Handler
	metricsCollector   *metric.Collector
	geoipDB            *geoip.DB
	sslManagerMu       sync.RWMutex
	sslManager         *ssl.Manager
	tlsHealthMu        sync.RWMutex
	tlsHealthy         bool
	tlsLoadedAt        time.Time
	torManager         *tor.Manager
	torServerPort      int
	outboundHTTPClient atomic.Pointer[http.Client]
	i2pManager         *i2p.Manager
	i2pMonitorOnce     sync.Once
	overlayMu          sync.RWMutex
	torStatus          string
	torHostname        string
	startupBannerDone  bool
	torAddressLogged   bool
	i2pStatus          string
	i2pHostname        string
	i2pAddressLogged   bool
	i2pProvider        i2p.Provider
	readLimiter        *rateLimiter
	writeLimiter       *rateLimiter
	healthLimiter      *rateLimiter
	burstLimiter       *rateLimiter
	strictLimiter      *rateLimiter
	authFailureLimiter *rateLimiter
	captchaMu          sync.Mutex
	captchaChallenges  map[string]simpleCaptchaState
	contactSpamMu      sync.Mutex
	contactSpam        *contactSpamState
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
	// schedulerStatus provides task state for the protected debug route.
	schedulerStatus SchedulerStatus
	// schedulerStopFn drains scheduled tasks after HTTP shutdown and before
	// child providers are closed.
	schedulerStopFn func()
	// operatorTokenHash is SHA-256(server.token), cached at construction time.
	// Constant-time compared against incoming Bearer tokens on protected routes.
	operatorTokenHash [32]byte
	// pendingRestartMu guards pendingRestartKeys.
	pendingRestartMu   sync.Mutex
	pendingRestartKeys []string
	lifecycleMu        sync.Mutex
	shutdownReason     string
	// csrfSecret is the HMAC key for CSRF token signing, loaded from the DB at startup.
	csrfSecret []byte
	// installSecret is the root installation_secret (PART 11 Cryptographic Keys),
	// loaded from the DB at startup; base for the rotating security_id token.
	installSecret []byte
	// prevInstallSecret is the installation_secret in force before the last
	// manual rotation, loaded only inside its 7-day grace window.
	prevInstallSecret []byte
	// prevEncKey is the at-rest encryption key in force before the last manual
	// rotation, loaded only inside its 30-day grace window.
	prevEncKey []byte
	// privDrop holds privilege-drop parameters set by main before Run (PART 23 step 8g).
	privDrop *privDropConfig
	// preparedListeners were bound before privilege drop by main and are consumed
	// by the first Run listener setup.
	preparedListeners []net.Listener
	// startupBannerFn emits the process banner after overlays and listeners are ready.
	startupBannerFn func()
	// afterPrivilegeDropFn starts user-mode startup tasks after the root phase.
	afterPrivilegeDropFn func()
	stopTermbinFn        func()
	// notifier delivers outbound role-scoped webhook notifications (PART 17).
	notifier *notify.Dispatcher
	// resolvedProxyMu guards resolvedProxyIPs.
	resolvedProxyMu sync.RWMutex
	// resolvedProxyIPs maps each DNS name in trusted_proxies.additional to its
	// current set of resolved IP addresses.  Refreshed every 5 minutes (PART 12).
	resolvedProxyIPs     map[string][]net.IP
	blocklistCacheMu     sync.RWMutex
	blocklistCache       *blocklistStore
	blocklistFingerprint string
	datasetChecks        health.CheckRegistry
	// dnsConfigMu protects the last applied DNS credential identity across config reloads.
	dnsConfigMu           sync.Mutex
	lastDNSProvider       string
	lastDNSCredentialHash [32]byte
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

func (s *Server) dnsValidatedCallback() func(time.Time) error {
	store, ok := s.db.(interface{ SetAppSecretValue(string, string) error })
	if !ok {
		return nil
	}
	return func(validatedAt time.Time) error {
		return store.SetAppSecretValue("dns_validated_at", validatedAt.UTC().Format(time.RFC3339))
	}
}

func dnsCredentialHash(cfg *config.Config) [32]byte {
	credentials, err := cfg.DecryptDNSCredentials()
	if err != nil {
		return sha256.Sum256([]byte("invalid-dns-credentials"))
	}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		return sha256.Sum256([]byte("invalid-dns-credentials"))
	}
	return sha256.Sum256(encoded)
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
// service account ("cairnbin"); the drop is a no-op when not running as root.
func (s *Server) SetPrivilegeDrop(user, group string, chownPaths []string) {
	s.privDrop = &privDropConfig{user: user, group: group, chown: chownPaths}
}

// SetPreparedListeners transfers sockets bound during the root setup phase.
func (s *Server) SetPreparedListeners(listeners []net.Listener) {
	s.preparedListeners = listeners
}

func (s *Server) setSSLManager(manager *ssl.Manager) {
	s.sslManagerMu.Lock()
	s.sslManager = manager
	s.sslManagerMu.Unlock()
}

// RenewManagedCertificate renews the app-managed Let's Encrypt certificate
// through the same manager that serves TLS.
func (s *Server) RenewManagedCertificate(ctx context.Context) error {
	s.sslManagerMu.RLock()
	manager := s.sslManager
	s.sslManagerMu.RUnlock()
	if manager == nil {
		return nil
	}
	return manager.RenewCertificate(ctx)
}

// SetStartupBannerFn registers the startup banner callback run after listeners
// are serving and overlay startup has been scheduled.
func (s *Server) SetStartupBannerFn(fn func()) {
	s.startupBannerFn = fn
}

// SetAfterPrivilegeDropFn registers user-mode startup work such as the
// scheduler and initial retention sweep.
func (s *Server) SetAfterPrivilegeDropFn(fn func()) {
	s.afterPrivilegeDropFn = fn
}

func (s *Server) afterPrivilegeDrop(ctx context.Context, cfg *config.Config) {
	if s.maintenance != nil {
		go s.maintenance.Start(ctx)
	}
	if s.afterPrivilegeDropFn != nil {
		s.afterPrivilegeDropFn()
	}
	s.stopTermbinFn = s.startTermbin(ctx, cfg)
}

func (s *Server) printStartupBanner() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.overlayMu.RLock()
		torPending := s.torStatus == "starting" && s.torManager != nil
		i2pPending := s.i2pStatus == "starting" && s.i2pManager != nil
		s.overlayMu.RUnlock()
		if (!torPending || s.torManager.OnionAddress() != "") && (!i2pPending || s.i2pManager.Address() != "") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if s.startupBannerFn != nil {
		s.startupBannerFn()
	}
	s.overlayMu.Lock()
	s.startupBannerDone = true
	s.overlayMu.Unlock()
}

// bindPrivilegedListenersAndDrop binds only ports below 1024 while root, then
// chowns runtime paths and drops privileges. Unprivileged ports are bound later.
func (s *Server) bindPrivilegedListenersAndDrop(addrs ...string) ([]net.Listener, error) {
	if len(s.preparedListeners) > 0 {
		if len(s.preparedListeners) != len(addrs) {
			return nil, fmt.Errorf("prepared listener count %d does not match address count %d", len(s.preparedListeners), len(addrs))
		}
		listeners := s.preparedListeners
		s.preparedListeners = nil
		return listeners, nil
	}
	listeners := make([]net.Listener, len(addrs))
	closeListeners := func() {
		for _, ln := range listeners {
			if ln != nil {
				_ = ln.Close()
			}
		}
	}
	if s.privDrop != nil && currentlyRoot() {
		for i, addr := range addrs {
			_, portText, err := net.SplitHostPort(addr)
			if err != nil {
				closeListeners()
				return nil, err
			}
			port, err := strconv.Atoi(portText)
			if err != nil {
				closeListeners()
				return nil, err
			}
			if port >= 1024 {
				continue
			}
			listeners[i], err = listener.ListenTCP(addr)
			if err != nil {
				closeListeners()
				return nil, err
			}
		}
	}
	if err := s.applyPrivilegeDrop(); err != nil {
		closeListeners()
		return nil, err
	}
	return listeners, nil
}

// PreparePrivilegeDrop binds privileged addresses, fixes runtime ownership, and
// drops root before the remaining startup initialization begins.
func PreparePrivilegeDrop(user, group string, chownPaths []string, addrs ...string) ([]net.Listener, error) {
	s := &Server{privDrop: &privDropConfig{user: user, group: group, chown: chownPaths}}
	return s.bindPrivilegedListenersAndDrop(addrs...)
}

func bindListenerAfterDrop(addr string, prebound net.Listener) (net.Listener, error) {
	if prebound != nil {
		return prebound, nil
	}
	return listener.ListenTCP(addr)
}

// applyPrivilegeDrop chowns runtime paths and permanently drops privileges to the
// configured service account. It is a no-op unless privilege drop was configured and
// the process is currently running as root. An unresolved or root target fails closed.
func (s *Server) applyPrivilegeDrop() error {
	if s.privDrop == nil || !currentlyRoot() {
		return nil
	}
	uid, gid, name, ok := resolvePrivDropTarget(s.privDrop.user, s.privDrop.group)
	if !ok {
		return fmt.Errorf("privilege: no service-user drop target resolved")
	}
	if uid == 0 {
		return fmt.Errorf("privilege: service-user target %q resolves to root", name)
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

// SetSchedulerStatus wires the read-only task snapshot used by the protected
// debug route. Call this before Run.
func (s *Server) SetSchedulerStatus(status SchedulerStatus) {
	s.schedulerStatus = status
}

// SetSchedulerStopFn registers the scheduler drain hook used during shutdown.
// Call this before Run so tasks finish before their child providers close.
func (s *Server) SetSchedulerStopFn(fn func()) {
	s.schedulerStopFn = fn
}

// SetEmailConfigChangeFn registers the SMTP/template refresh hook used after a
// successful server.yml reload.
func (s *Server) SetEmailConfigChangeFn(fn func(config.EmailConfig, string)) {
	s.emailConfigChangeFn = fn
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

func resolveSSLPath(configDir, value string) string {
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(configDir, "ssl", value)
}

func makeI2PConfig(cfg config.I2PConfig, configDir, dataDir, logDir string) i2p.Config {
	bootstrapTimeout := 5 * time.Minute
	if d, err := time.ParseDuration(cfg.BootstrapTimeout); err == nil && d > 0 {
		bootstrapTimeout = d
	}
	return i2p.Config{
		Enabled:          cfg.Enabled,
		Binary:           cfg.Binary,
		SAMAddress:       cfg.SAMAddress,
		VirtualPort:      cfg.VirtualPort,
		InboundLength:    cfg.InboundLength,
		OutboundLength:   cfg.OutboundLength,
		InboundQuantity:  cfg.InboundQuantity,
		OutboundQuantity: cfg.OutboundQuantity,
		SignatureType:    cfg.SignatureType,
		BootstrapTimeout: bootstrapTimeout,
		ConfigDir:        configDir,
		DataDir:          dataDir,
		LogDir:           logDir,
	}
}

func makeTorConfig(cfg config.TorConfig, configDir, dataDir, logDir string) tor.Config {
	return tor.Config{
		Binary:                    cfg.Binary,
		UseNetwork:                cfg.UseNetwork,
		MaxCircuits:               cfg.MaxCircuits,
		CircuitTimeout:            cfg.CircuitTimeout,
		BootstrapTimeout:          cfg.BootstrapTimeout,
		SafeLogging:               cfg.SafeLogging,
		MaxStreamsPerCircuit:      cfg.MaxStreamsPerCircuit,
		CloseCircuitOnStreamLimit: cfg.CloseCircuitOnStreamLimit,
		BandwidthRate:             cfg.BandwidthRate,
		BandwidthBurst:            cfg.BandwidthBurst,
		MaxMonthlyBandwidth:       cfg.MaxMonthlyBandwidth,
		NumIntroPoints:            cfg.NumIntroPoints,
		VirtualPort:               cfg.VirtualPort,
		ConfigDir:                 configDir,
		DataDir:                   dataDir,
		LogDir:                    logDir,
	}
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
		ipBlocks:              newIPBlocks(),
		router:                chi.NewRouter(),
		db:                    db,
		cfg:                   cfg,
		cfgMgr:                cfgMgr,
		lastSMTPConfig:        cfg.Server.Notifications.Email,
		lastEmailLanguage:     cfg.Server.Lang,
		lastTorConfig:         cfg.Server.Tor,
		lastI2PConfig:         cfg.Server.I2P,
		lastBlocklistsConfig:  cfg.Server.Security.Blocklists,
		lastGeoIPConfig:       cfg.Server.GeoIP,
		lastCVEConfig:         cfg.Data.CVE,
		lastTrivyConfig:       cfg.Data.Trivy,
		version:               version,
		commitID:              commitID,
		buildDate:             buildDate,
		configDir:             configDir,
		dataDir:               dataDir,
		lastDNSProvider:       cfg.Server.TLS.LetsEncrypt.DNSProvider,
		lastDNSCredentialHash: dnsCredentialHash(cfg),
		startTime:             time.Now(),
	}
	s.stats.lastHour = time.Now().Hour()

	// Publish the build identity used by every asset URL, cache header, and the
	// version-change purge cookie (AI.md PART 9, "Asset Version-Busting").
	buildinfo.Set(version, commitID)
	applyI18nConfig(cfg)

	// Validate configured SEO site-verification codes/custom tags at startup and
	// log (never abort) on invalid format — invalid entries are simply never
	// rendered (PART 16 "Server validates codes on startup and logs errors").
	logSEOVerificationErrors(cfg.Server.SEO.Verification)

	// Webhook dispatcher for outbound role notifications (PART 17).
	s.notifier = notify.New(notify.Meta{
		ProjectName:    "cairnbin",
		ProjectVersion: version,
		AppURL:         cfg.StartupBaseURL(),
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
	s.swaggerHandler.SetLanguageResolver(i18n.LangFromRequest)
	s.swaggerHandler.SetPageRenderer(func(w http.ResponseWriter, r *http.Request, title, lead, section, content string) {
		data := s.pageData()
		data["ViewerTitle"], data["ViewerLead"], data["ViewerSection"], data["ViewerContent"] = title, lead, section, template.HTML(content)
		s.renderTemplate(w, r, "docs_viewer.html", data)
	})
	s.mcpHandler = mcp.New(mcp.Config{
		APIVersion:    s.apiVersion(),
		ServerName:    cfg.Web.SiteTitle,
		ServerVersion: version,
		Dispatch:      s.router,
		Authenticated: s.validBearer,
		OriginAllowed: s.mcpOriginAllowed,
	})
	s.graphqlHandler = graphql.New(db, cfg.Web.SiteTitle)
	s.graphqlHandler.SetAssetPrefixResolver(s.assetPrefix)
	s.graphqlHandler.SetThemeResolver(s.themeFromRequest)
	s.graphqlHandler.SetCSRFTokenResolver(s.csrfTokenFromRequest)
	s.graphqlHandler.SetPageRenderer(func(w http.ResponseWriter, r *http.Request, title, lead, section, content string) {
		data := s.pageData()
		data["ViewerTitle"], data["ViewerLead"], data["ViewerSection"], data["ViewerContent"] = title, lead, section, template.HTML(content)
		s.renderTemplate(w, r, "docs_viewer.html", data)
	})
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

	// Tor hidden service — auto-enabled when Tor binary is found.
	torCfg := makeTorConfig(cfg.Server.Tor, configDir, dataDir, paths.GetLogsDir("cairnbin"))
	// Forward the hidden service to the server's real HTTP listener. Port may be a
	// dual "http,https" value (PART 5/15); the onion maps :virtual → the plaintext
	// HTTP port, so parse the first field. Never hardcode a dev port — an unresolved
	// value leaves serverPort 0 and the Tor manager skips rather than forward blindly.
	portField := cfg.Server.Port
	if i := strings.IndexByte(portField, ','); i >= 0 {
		portField = portField[:i]
	}
	serverPort, _ := strconv.Atoi(strings.TrimSpace(portField))
	s.torServerPort = serverPort
	s.torManager = tor.NewManager(context.Background(), serverPort, torCfg, s.router)
	s.outboundHTTPClient.Store(s.torManager.GetHTTPClient(cfg.Server.Tor.UseNetwork))

	if cfg.Server.GeoIP.Enabled {
		gcfg := geoip.Config{
			Dir:            cfg.Server.GeoIP.Dir,
			HTTPClient:     s.OutboundHTTPClient(),
			DeferDownloads: cfg.Server.Tor.UseNetwork,
			EnableASN:      cfg.Server.GeoIP.Databases.ASN,
			EnableCountry:  cfg.Server.GeoIP.Databases.Country,
			EnableCity:     cfg.Server.GeoIP.Databases.City,
			DenyCountries:  cfg.Server.GeoIP.DenyCountries,
			AllowCountries: cfg.Server.GeoIP.AllowCountries,
			// Wire server-wide security allowlist into geoip so GeoIP also
			// bypasses country-blocking for explicitly allowlisted IPs.
			Allowlist: cfg.Server.Security.AllowlistCIDRs(),
		}
		if gdb, err := geoip.Open(gcfg); err != nil {
			log.Printf("warning: geoip init: %v", err)
		} else {
			s.geoipDB = gdb
		}
	}
	s.i2pManager = i2p.NewManager(context.Background(), makeI2PConfig(cfg.Server.I2P,
		configDir, dataDir, paths.GetLogsDir("cairnbin")), s.router)

	// Report Tor state into the tor_* metrics at scrape time.
	s.metricsCollector.SetTorProvider(func() (enabled, running bool) {
		ti := s.buildTorInfo()
		return ti.Enabled, ti.Running
	})

	// Initialize cache driver (PART 9/12). Falls back to in-process memory on error.
	externalCacheConfigured := strings.EqualFold(cfg.Server.Cache.Type, "redis") || strings.EqualFold(cfg.Server.Cache.Type, "valkey")
	externalCacheReady := false
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
		TLSVerify:     cfg.Server.Cache.TLSCA != "" || (cfg.SourceFile("server.cache.tls_skip_verify") != "" && !cfg.Server.Cache.TLSSkipVerify),
		TLSCA:         resolveSSLPath(configDir, cfg.Server.Cache.TLSCA),
		TLSCert:       resolveSSLPath(configDir, cfg.Server.Cache.TLSCert),
		TLSKey:        resolveSSLPath(configDir, cfg.Server.Cache.TLSKey),
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
		if externalCacheConfigured {
			s.cacheDegraded = true
		}
		fallback := cache.DefaultConfig()
		fallback.Prefix = cacheCfg.Prefix
		fallback.TTL = cacheCfg.TTL
		s.cacheStore, _ = cache.New(fallback)
	} else {
		s.cacheStore = cs
		externalCacheReady = externalCacheConfigured
	}
	s.cacheStore = &debugCache{Cache: s.cacheStore, s: s}
	configureReportLimits(cfg.Web.Reports.RateLimitPerMinute, cfg.Web.Reports.RateLimitPerIPBurst)
	// Wire the cache driver into the paste read path (PART 9) now that it's ready.
	s.pasteHandler.SetCache(s.cacheStore)
	// Wire the configured max paste size (server.yml paste.max_size) into
	// the create path so the operator-set limit is actually enforced.
	s.pasteHandler.SetMaxSize(cfg.Paste.MaxSizeBytes)

	{
		readReqs := cfg.Server.RateLimit.Read.Requests
		writeReqs := cfg.Server.RateLimit.Write.Requests
		healthReqs := cfg.Server.RateLimit.Health.Requests
		strictReqs := cfg.Server.RateLimit.Strict.Requests
		authFailuresReqs := cfg.Server.RateLimit.AuthFailures.Requests
		if readReqs <= 0 {
			readReqs = 120
		}
		if writeReqs <= 0 {
			writeReqs = 10
		}
		if healthReqs <= 0 {
			healthReqs = 120
		}
		if strictReqs <= 0 {
			strictReqs = 10
		}
		if authFailuresReqs <= 0 {
			authFailuresReqs = 5
		}
		readWin := time.Duration(cfg.Server.RateLimit.Read.Window) * time.Second
		writeWin := time.Duration(cfg.Server.RateLimit.Write.Window) * time.Second
		healthWin := time.Duration(cfg.Server.RateLimit.Health.Window) * time.Second
		strictWin := time.Duration(cfg.Server.RateLimit.Strict.Window) * time.Second
		authFailuresWin := time.Duration(cfg.Server.RateLimit.AuthFailures.Window) * time.Second
		if readWin <= 0 {
			readWin = time.Minute
		}
		if writeWin <= 0 {
			writeWin = time.Minute
		}
		if healthWin <= 0 {
			healthWin = time.Minute
		}
		if strictWin <= 0 {
			strictWin = 15 * time.Minute
		}
		if authFailuresWin <= 0 {
			authFailuresWin = 15 * time.Minute
		}
		burstReqs := cfg.Server.RateLimit.GlobalBurst
		if burstReqs <= 0 {
			burstReqs = 240
		}
		s.readLimiter = newRateLimiter(readReqs, readWin)
		s.writeLimiter = newRateLimiter(writeReqs, writeWin)
		s.healthLimiter = newRateLimiter(healthReqs, healthWin)
		s.strictLimiter = newRateLimiter(strictReqs, strictWin)
		s.authFailureLimiter = newRateLimiter(authFailuresReqs, authFailuresWin)
		// Absolute ceiling across all endpoint classes combined (AI.md:16187,
		// 240 req/min per IP). Applied as the step-7 global RateLimitMiddleware;
		// the per-class limiters above remain attached to their routes as the
		// tighter per-class quotas.
		s.burstLimiter = newRateLimiter(burstReqs, time.Minute)
	}
	var rateStore rateLimitCounter
	if externalCacheConfigured {
		if externalCacheReady {
			rateStore = cacheRateLimitCounter{Cache: s.cacheStore}
		}
	} else if store, ok := db.(database.RateLimitDB); ok {
		rateStore = store
	}
	if rateStore != nil {
		for _, limiter := range []*rateLimiter{s.readLimiter, s.writeLimiter, s.healthLimiter, s.strictLimiter, s.authFailureLimiter, s.burstLimiter} {
			limiter.store = rateStore
		}
	}
	// Restoration failures share the authentication-failure quota.
	s.restoreFailLimiter = s.authFailureLimiter

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
	s.loadPreviousSecrets(db)

	// Generate the project PGP keypair on first start when publishing is enabled
	// (PART 11 → GPG Keypair Management). Never fails startup; falls back to
	// AES-256-GCM report encryption when unavailable. Prune any expired parked key.
	s.ensureSecurityKeypair()
	s.CleanupRotatedPGPKey()

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
	s.registerBlocklistHealthChecks(cfg)
	s.registerSecurityDataHealthChecks(cfg)
	s.registerTLSHealthCheck(cfg)
	s.ReloadBlocklists()

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
			return i18n.TranslateFormat(lang, key, i18n.Args(args...))
		},
		// th and tfh return a translation as trusted HTML: the embedded locale
		// strings may carry inline <code>/<a> markup, while every interpolated
		// argument is HTML-escaped so runtime values can never inject markup.
		"th": func(lang, key string) template.HTML {
			return template.HTML(i18n.Translate(lang, key))
		},
		"tfh": func(lang, key string, args ...interface{}) template.HTML {
			escaped := make(map[string]string)
			for k, v := range i18n.Args(args...) {
				escaped[k] = template.HTMLEscapeString(v)
			}
			return template.HTML(i18n.TranslateFormat(lang, key, escaped))
		},
		"i18njs": func(lang string) template.JS {
			return template.JS(i18n.JSBundle(lang))
		},
		"markdown": func(s string) template.HTML {
			return renderMarkdown(s)
		},
		"privacyText": localizedPrivacyText,
		"healthCheckLabel": func(lang, key string) string {
			return healthCheckLabel(lang, key)
		},
		"trackingEnabled": func() bool {
			return s.liveCfg().Server.Tracking.Enabled()
		},
		"trackingScript": func() template.HTML {
			return renderTrackingScript(s.liveCfg().Server.Tracking)
		},
		"consentConfig": func(lang string) template.JS {
			return renderConsentConfig(s.liveCfg(), lang)
		},
		// consentInfo exposes the same consent configuration as plain Go values
		// (rather than a JSON literal) for the no-JS <noscript> consent form
		// (AI.md 25598), which cannot execute app.js to read pb-consent-data.
		"consentInfo": func(lang string) consentClientConfig {
			return buildConsentClientConfig(s.liveCfg(), lang)
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

func healthCheckLabel(lang, key string) string {
	translation := i18n.Translate(lang, "health.check."+key)
	if translation != "health.check."+key {
		return translation
	}
	kind, id, found := strings.Cut(key, "_")
	if !found {
		return strings.ReplaceAll(key, "_", " ")
	}
	label := strings.ReplaceAll(id, "_", " ")
	switch kind {
	case "blocklist":
		return "Blocklist: " + label
	case "geoip":
		return "GeoIP: " + label
	case "data":
		return "Data: " + label
	default:
		return strings.ReplaceAll(key, "_", " ")
	}
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
// Per AI.md PART 11 "Naming": both MUST share the project-unique name recorded in IDEA.md.
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
	if next != nil {
		// paste.max_size is hot-reloadable. Keep request enforcement in sync
		// with liveCfg so a page's displayed limit and create paths cannot drift.
		if s.pasteHandler != nil {
			s.pasteHandler.SetMaxSize(next.Paste.MaxSizeBytes)
		}
		if !reflect.DeepEqual(s.lastTorConfig, next.Server.Tor) {
			if s.torManager != nil {
				if err := s.torManager.UpdateConfig(makeTorConfig(next.Server.Tor,
					s.configDir, s.dataDir, paths.GetLogsDir("cairnbin"))); err != nil {
					log.Printf("tor: config reload failed: %v", err)
				} else {
					s.lastTorConfig = next.Server.Tor
					s.outboundHTTPClient.Store(s.torManager.GetHTTPClient(next.Server.Tor.UseNetwork))
				}
			} else {
				s.lastTorConfig = next.Server.Tor
			}
		}
		datasetsChanged := !reflect.DeepEqual(s.lastBlocklistsConfig, next.Server.Security.Blocklists) ||
			!reflect.DeepEqual(s.lastGeoIPConfig, next.Server.GeoIP) ||
			!reflect.DeepEqual(s.lastCVEConfig, next.Data.CVE) ||
			!reflect.DeepEqual(s.lastTrivyConfig, next.Data.Trivy)
		if datasetsChanged {
			s.lastBlocklistsConfig = next.Server.Security.Blocklists
			s.lastGeoIPConfig = next.Server.GeoIP
			s.lastCVEConfig = next.Data.CVE
			s.lastTrivyConfig = next.Data.Trivy
			s.datasetChecks.Reset()
			s.registerBlocklistHealthChecks(next)
			s.registerSecurityDataHealthChecks(next)
			s.reloadBlocklistsWithConfig(next)
		}
		emailConfigChanged := !reflect.DeepEqual(s.lastSMTPConfig, next.Server.Notifications.Email)
		languageChanged := s.lastEmailLanguage != next.Server.Lang
		if emailConfigChanged || languageChanged {
			s.lastSMTPConfig = next.Server.Notifications.Email
			s.lastEmailLanguage = next.Server.Lang
			if s.emailConfigChangeFn != nil {
				s.emailConfigChangeFn(next.Server.Notifications.Email, next.Server.Lang)
			}
			if emailConfigChanged && email.SMTPPrivateCertVerificationSkipped(s.lastSMTPConfig.SMTP.Host, s.lastSMTPConfig.SMTP.TLS) {
				log.Printf("SMTP host %s is a private address: TLS certificate verification skipped", s.lastSMTPConfig.SMTP.Host)
			}
		}
		if !reflect.DeepEqual(s.lastI2PConfig, next.Server.I2P) {
			s.lastI2PConfig = next.Server.I2P
			if s.i2pManager != nil {
				if err := s.i2pManager.UpdateConfig(makeI2PConfig(next.Server.I2P,
					s.configDir, s.dataDir, s.logDir)); err != nil {
					log.Printf("i2p: config reload failed: %v", err)
				} else if s.i2pManager.Running() {
					s.startI2PMonitor()
					s.persistB32Address()
				}
			}
		}
	}
	applyI18nConfig(next)
	if s.readLimiter != nil && next.Server.RateLimit.Read.Requests > 0 {
		s.readLimiter.UpdateLimit(next.Server.RateLimit.Read.Requests)
		s.readLimiter.UpdateWindow(time.Duration(next.Server.RateLimit.Read.Window) * time.Second)
	}
	if s.writeLimiter != nil && next.Server.RateLimit.Write.Requests > 0 {
		s.writeLimiter.UpdateLimit(next.Server.RateLimit.Write.Requests)
		s.writeLimiter.UpdateWindow(time.Duration(next.Server.RateLimit.Write.Window) * time.Second)
	}
	if s.healthLimiter != nil && next.Server.RateLimit.Health.Requests > 0 {
		s.healthLimiter.UpdateLimit(next.Server.RateLimit.Health.Requests)
		s.healthLimiter.UpdateWindow(time.Duration(next.Server.RateLimit.Health.Window) * time.Second)
	}
	if s.burstLimiter != nil && next.Server.RateLimit.GlobalBurst > 0 {
		s.burstLimiter.UpdateLimit(next.Server.RateLimit.GlobalBurst)
	}
	if s.strictLimiter != nil && next.Server.RateLimit.Strict.Requests > 0 {
		s.strictLimiter.UpdateLimit(next.Server.RateLimit.Strict.Requests)
		s.strictLimiter.UpdateWindow(time.Duration(next.Server.RateLimit.Strict.Window) * time.Second)
	}
	if s.authFailureLimiter != nil && next.Server.RateLimit.AuthFailures.Requests > 0 {
		s.authFailureLimiter.UpdateLimit(next.Server.RateLimit.AuthFailures.Requests)
		s.authFailureLimiter.UpdateWindow(time.Duration(next.Server.RateLimit.AuthFailures.Window) * time.Second)
	}

	// Detect restart-required changes and record them for healthz. Settings
	// are prefix-matched per AI.md restartRequiredSettings: "server.port",
	// "server.address", "server.daemonize" are exact-match; "ssl." and "database."
	// cover their whole section. logging.* file/format and
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
	if prev.Server.Database.Type != next.Server.Database.Type || prev.Server.Database.Path != next.Server.Database.Path {
		s.MarkPendingRestart("database")
	}
	s.dnsConfigMu.Lock()
	nextDNSHash := dnsCredentialHash(next)
	dnsConfigChanged := s.lastDNSProvider != next.Server.TLS.LetsEncrypt.DNSProvider ||
		s.lastDNSCredentialHash != nextDNSHash
	s.lastDNSProvider = next.Server.TLS.LetsEncrypt.DNSProvider
	s.lastDNSCredentialHash = nextDNSHash
	s.dnsConfigMu.Unlock()
	if dnsConfigChanged {
		if store, ok := s.db.(interface{ DeleteAppSecretValue(string) error }); ok {
			if err := store.DeleteAppSecretValue("dns_validated_at"); err != nil {
				log.Printf("ERROR: ssl: failed to clear DNS credential validation timestamp")
			}
		}
	}
	if prev.Server.TLS.Enabled != next.Server.TLS.Enabled ||
		prev.Server.TLS.Cert != next.Server.TLS.Cert ||
		prev.Server.TLS.Key != next.Server.TLS.Key ||
		prev.Server.TLS.MinVersion != next.Server.TLS.MinVersion ||
		prev.Server.TLS.LetsEncrypt.Email != next.Server.TLS.LetsEncrypt.Email ||
		prev.Server.TLS.LetsEncrypt.Enabled != next.Server.TLS.LetsEncrypt.Enabled ||
		prev.Server.TLS.LetsEncrypt.Challenge != next.Server.TLS.LetsEncrypt.Challenge ||
		prev.Server.TLS.LetsEncrypt.Staging != next.Server.TLS.LetsEncrypt.Staging ||
		dnsConfigChanged {
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
	s.auditConfigChange(prev, next)
}

// auditConfigChange records the PART 11 configuration audit events for a
// hot-reloaded server.yml: config.updated with the changed top-level keys
// (never values), plus the specific smtp / ssl / branding events. Secrets and
// credentials are never written because only key names are logged.
func (s *Server) auditConfigChange(prev, next *config.Config) {
	if s.auditLogger == nil || prev == nil || next == nil {
		return
	}
	emit := func(event string, keys []string) {
		details := map[string]any{}
		if keys != nil {
			details["changed_keys"] = keys
		}
		s.auditLog(nil, audit.Entry{Event: event, Category: "config", Severity: audit.SeverityInfo, Result: audit.ResultSuccess, Target: &audit.Target{Type: "config", ID: "server.yml"}, Details: details})
	}
	// Standards toggled on or off get their own compliance events (PART 11).
	pv, nv := reflect.ValueOf(prev.Server.Compliance), reflect.ValueOf(next.Server.Compliance)
	for i := 0; i < pv.NumField(); i++ {
		if pv.Field(i).Bool() == nv.Field(i).Bool() || pv.Type().Field(i).Name == "Enabled" {
			continue
		}
		event := "compliance.standard_disabled"
		if nv.Field(i).Bool() {
			event = "compliance.standard_enabled"
		}
		name := strings.Split(pv.Type().Field(i).Tag.Get("yaml"), ",")[0]
		s.auditLog(nil, audit.Entry{Event: event, Category: "security", Severity: audit.SeverityInfo, Result: audit.ResultSuccess, Details: map[string]any{"standard": name, "enabled_by": "config"}})
	}
	// Allowlist additions / removals (PART 11 audit events).
	prevAllow, nextAllow := map[string]config.AllowlistEntry{}, map[string]config.AllowlistEntry{}
	for _, e := range prev.Server.Security.Allowlist {
		prevAllow[e.CIDR] = e
	}
	for _, e := range next.Server.Security.Allowlist {
		nextAllow[e.CIDR] = e
		if _, ok := prevAllow[e.CIDR]; !ok {
			s.auditLog(nil, audit.Entry{Event: "security.ip_allowlisted", Category: "security", Severity: audit.SeverityInfo, Result: audit.ResultSuccess, Details: map[string]any{"cidr": e.CIDR, "description": e.Description}})
		}
	}
	for cidr := range prevAllow {
		if _, ok := nextAllow[cidr]; !ok {
			s.auditLog(nil, audit.Entry{Event: "security.ip_allowlist_removed", Category: "security", Severity: audit.SeverityInfo, Result: audit.ResultSuccess, Details: map[string]any{"cidr": cidr}})
		}
	}
	var keys []string
	if !reflect.DeepEqual(prev.Server, next.Server) {
		keys = append(keys, "server")
	}
	if !reflect.DeepEqual(prev.Web, next.Web) {
		keys = append(keys, "web")
	}
	if !reflect.DeepEqual(prev.Server.Database, next.Server.Database) {
		keys = append(keys, "database")
	}
	if len(keys) == 0 {
		return
	}
	emit("config.updated", keys)
	if !reflect.DeepEqual(prev.Server.Notifications.Email, next.Server.Notifications.Email) {
		emit("config.smtp_updated", nil)
	}
	if !reflect.DeepEqual(prev.Server.TLS, next.Server.TLS) {
		emit("config.ssl_updated", nil)
	}
	if !reflect.DeepEqual(prev.Server.Branding, next.Server.Branding) {
		emit("config.branding_updated", nil)
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
	err := s.geoipDB.Update()
	s.datasetChecks.Refresh(time.Now())
	return err
}

// OutboundHTTPClient returns the server's shared client for app-owned external
// requests. When Tor networking is enabled, requests fail closed until Tor is
// ready instead of falling back to clearnet.
func (s *Server) OutboundHTTPClient() *http.Client {
	return s.outboundHTTPClient.Load()
}

// TorEnabled reports whether a Tor binary is available for the managed service.
func (s *Server) TorEnabled() bool {
	return s.torManager != nil && s.torManager.Enabled()
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
		return strings.TrimSpace(s.liveCfg().Server.Tor.OnionAddress)
	}
	if addr := strings.TrimSpace(s.torManager.OnionAddress()); addr != "" {
		return addr
	}
	return strings.TrimSpace(s.liveCfg().Server.Tor.OnionAddress)
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
	mw := s.rateLimitMiddleware(s.readLimiter, "read")
	return mw(h).ServeHTTP
}

// maybeRateLimit wraps h with the write (POST/PUT/PATCH/DELETE) rate limiter if enabled (PART 12).
func (s *Server) maybeRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.writeLimiter == nil {
		return h
	}
	mw := s.rateLimitMiddleware(s.writeLimiter, "write")
	return mw(h).ServeHTTP
}

// maybeStrictRateLimit protects login-like operations that disclose or replace
// owner credentials, independently of the general rate-limit setting.
func (s *Server) maybeStrictRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.strictLimiter == nil {
		return h
	}
	return s.rateLimitMiddleware(s.strictLimiter, "strict")(h).ServeHTTP
}

// maybeDeleteRateLimit wraps h with the write rate limiter (DELETE is a write operation, PART 12).
func (s *Server) maybeDeleteRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.writeLimiter == nil {
		return h
	}
	mw := s.rateLimitMiddleware(s.writeLimiter, "write")
	return mw(h).ServeHTTP
}

// maybeHealthRateLimit wraps h with the health endpoint rate limiter if enabled (PART 12).
func (s *Server) maybeHealthRateLimit(h http.HandlerFunc) http.HandlerFunc {
	if s.healthLimiter == nil {
		return h
	}
	mw := s.rateLimitMiddleware(s.healthLimiter, "health")
	return mw(h).ServeHTTP
}

func (s *Server) rateLimitMiddleware(rl *rateLimiter, class string) func(http.Handler) http.Handler {
	return rateLimitMiddlewareWithKey(rl, class, s.rateLimitKey)
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
	r.Use(s.seoIndexMiddleware)
	r.Use(middleware.CleanPath)
	r.Use(s.noTrailingSlash)
	r.Use(s.requestIDMiddleware)
	r.Use(s.authFailureFloorMiddleware)
	r.Use(s.pathSecurityMiddleware)
	r.Use(s.securityHeadersMiddleware)
	r.Use(s.secFetchMiddleware)
	r.Use(s.contactBodyLimitMiddleware)
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
		r.Use(s.rateLimitMiddleware(s.burstLimiter, "global_burst"))
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
	r.Get("/sitemap-{page}.xml", s.handleSitemap)
	r.Get("/feed.xml", s.maybeReadRateLimit(s.handleAtomFeed))
	r.Head("/feed.xml", s.maybeReadRateLimit(s.handleAtomFeed))
	r.Get("/rss.xml", s.maybeReadRateLimit(s.handleRSSFeed))
	r.Head("/rss.xml", s.maybeReadRateLimit(s.handleRSSFeed))
	// AI-agent discovery file (PART 13/14) — canonical well-known path plus the
	// bare /llms.txt alias; both serve the same generated document.
	r.Get("/.well-known/llms.txt", s.handleLLMs)
	r.Get("/llms.txt", s.handleLLMs)
	for _, size := range []int{72, 96, 128, 144, 152, 180, 192, 384, 512} {
		r.Get(fmt.Sprintf("/static/icons/icon-%d.png", size), s.handlePWAIcon(size))
	}
	r.Get("/static/icons/shortcut-preferences.png", s.handlePWAIcon(96))
	for _, size := range []int{192, 512} {
		r.Get(fmt.Sprintf("/static/icons/icon-maskable-%d.png", size), s.handlePWAIconMaskable(size))
	}
	// RFC 9116: canonical path is /.well-known/security.txt only. Bare
	// /security.txt is intentionally NOT registered and returns 404 (PART 13).
	r.Get("/.well-known/security.txt", s.handleSecurity)
	// Coordinated-disclosure PGP public key — 404 until a keypair is generated
	// and publishing is enabled (PART 11, AI.md 14156).
	r.Get("/.well-known/pgp-key.asc", s.handlePGPKey)
	// Well-known namespace contract (PART 11): only GET/HEAD are valid, every
	// other method is 405, and any non-allowlisted entry is 404 with no listing.
	r.HandleFunc("/.well-known/*", wellKnownFallback(s))
	r.HandleFunc("/.well-known", wellKnownFallback(s))
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
		// PART 20: the bare /metrics root alias is gated on
		// server.metrics.root.enabled (default true, since Prometheus scrapers
		// default to /metrics). It serves the SAME handler and is never a
		// redirect — redirects break scrapers.
		if s.cfg.Server.Metrics.Root.Enabled {
			s.mountMetricsServices(r, "/metrics", true)
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
	r.Get("/server/dpo", s.handleDPO)
	r.Get("/server/help", s.handleHelp)
	r.Get("/server/help/mcp", s.handleHelpMCP)
	r.Get("/server/privacy", s.handlePrivacy)
	// CCPA "Do Not Sell" opt-out toggle — sets/clears the ccpa_opt_out cookie
	// (PART 31). Only meaningful when server.privacy.data.sold is true.
	r.Post("/server/ccpa", s.handleCCPAOptOut)
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
	r.Get("/server/preferences/tokens", s.handleOwnerTokensPage)
	r.Get("/server/preferences/sync", s.handlePreferencesExport)
	r.Post("/server/preferences/sync", s.maybeRateLimit(s.handlePreferencesSyncPost))
	r.Get("/server/preferences/sync/import", s.handlePreferencesImport)
	r.Get("/server/preferences/export", s.redirectLegacyPreferencesPath)
	r.Get("/server/preferences/import", s.redirectLegacyPreferencesPath)
	r.Get("/server/contact", s.maybeReadRateLimit(s.handleContact))
	r.Post("/server/contact", s.maybeStrictRateLimit(s.handleContactPost))
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
		dbg := s.liveCfg().Server.Debug
		r.Route("/debug", func(debug chi.Router) {
			debug.Use(s.requireOperatorToken)
			if dbg.Pprof {
				debug.HandleFunc("/pprof", pprof.Index)
				debug.HandleFunc("/pprof/cmdline", handleDebugCmdline)
				debug.HandleFunc("/pprof/profile", pprof.Profile)
				debug.HandleFunc("/pprof/symbol", pprof.Symbol)
				debug.HandleFunc("/pprof/trace", pprof.Trace)
				debug.Handle("/pprof/heap", pprof.Handler("heap"))
				debug.Handle("/pprof/goroutine", pprof.Handler("goroutine"))
				debug.Handle("/pprof/allocs", pprof.Handler("allocs"))
				debug.Handle("/pprof/block", pprof.Handler("block"))
				debug.Handle("/pprof/mutex", pprof.Handler("mutex"))
				debug.Handle("/pprof/threadcreate", pprof.Handler("threadcreate"))
				debug.Get("/vars", handleDebugVars)
			}
			if dbg.RuntimeEndpoints {
				s.registerDebugRoutes(debug)
			}
		})
		if dbg.Pprof {
			log.Printf("debug: /debug/pprof and /debug/vars endpoints enabled")
		}
		if dbg.RuntimeEndpoints {
			log.Printf("debug: /debug runtime endpoints enabled")
		}
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
		r.Get("/pastes", s.maybeReadRateLimit(s.pasteHandler.ListPastes))
		r.Post("/pastes", s.maybeRateLimit(s.pasteHandler.CreatePaste))

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
		r.Get("/server/healthz", s.maybeHealthRateLimit(s.handleHealthzAPI))
		r.Get("/server/version", s.handleVersion)
		r.Get("/server/about", s.handleAboutAPI)
		r.Get("/server/privacy", s.handlePrivacyAPI)
		r.Get("/server/dpo", s.handleDPOAPI)
		r.Get("/server/help", s.handleHelpAPI)
		r.Get("/server/help/mcp", s.handleHelpMCP)
		r.Get("/server/terms", s.handleTermsAPI)
		r.Get("/server/security", s.handleSecurityAPI)
		r.Get("/server/security/policy", s.handleSecurityPolicyAPI)
		r.Get("/server/security/thanks", s.handleSecurityThanksAPI)
		r.Get("/server/contact", s.maybeReadRateLimit(s.handleContactAPIGet))
		r.Post("/server/contact", s.maybeStrictRateLimit(s.handleContactAPI))
		r.Get("/server/auth/check", s.maybeStrictRateLimit(s.handleAuthCheck))
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
		r.Get("/server/preferences/sync", s.handlePreferencesExport)
		r.Get("/server/preferences/sync/import", s.handlePreferencesImport)
		r.Get("/server/swagger", s.swaggerHandler.ServeSpec)
		r.Post("/server/mcp", s.mcpHandler.ServeHTTP)
		r.Post("/server/reports/{name}", s.handleReports)
		// /api/v1/server/graphql — versioned GraphQL endpoint (PART 14)
		r.Get("/server/graphql", s.graphqlHandler.ServeHTTP)
		r.Post("/server/graphql", s.graphqlHandler.ServeHTTP)

		// PART 20: the metrics tree also answers under the versioned API path,
		// invoking the same handlers with the same per-service tokens.
		if s.cfg.Server.Metrics.Enabled {
			s.mountMetricsServices(r, "/server/metrics", true)
		}
	})

	// Unversioned aliases — same handler as versioned, served directly (no redirect) per PART 14.
	r.Get("/api/swagger", s.swaggerHandler.ServeSpec)
	r.Get("/api/graphql", s.graphqlHandler.ServeHTTP)
	r.Post("/api/graphql", s.graphqlHandler.ServeHTTP)
	// MCP: the same handler at all three paths, never a redirect (PART 11).
	r.Post("/api/mcp", s.mcpHandler.ServeHTTP)
	r.Post("/mcp", s.mcpHandler.ServeHTTP)
	r.Get("/api/healthz", s.maybeHealthRateLimit(s.handleHealthzAPI))
	// PART 20: unversioned machine-friendly metrics alias, same handlers.
	if s.cfg.Server.Metrics.Enabled {
		s.mountMetricsServices(r, "/api/metrics", true)
	}
	// autodiscover is non-versioned by design (PART 32/14): clients use it before knowing the version.
	r.Get("/api/autodiscover", s.handleAutodiscover)
	// CLI binary distribution (PART 32): public unless server.cli.binary_download.require_auth.
	r.Get("/cli/binaries/{name}", s.handleCLIBinary)

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
	// microbin upload compatibility alias uses the compat token and response contract.
	r.Post("/upload", s.maybeRateLimit(s.compatHandler.MicrobinCreate))
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
	r.Get("/server/docs/swagger", s.swaggerHandler.ServeUI)
	r.Get("/server/docs/graphql", s.graphqlHandler.ServeHTTP)

	// ── Paste view — catch-all (must be last) ────────────────────────────────
	r.Get("/{id}", s.maybeReadRateLimit(s.handleViewPaste))
}

func (s *Server) seoIndexMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.liveCfg().Server.SEO.NoIndex {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// requireOperatorToken is middleware that enforces server.token authentication.
// It reads the credential resolved by authMiddleware (execution step 9, which
// honours every PART 8 auth header in priority order), SHA-256 hashes it, and
// compares against the cached hash using constant-time comparison (PART 11).
// Returns 401 on missing/invalid credentials and 403 for a recognized owner
// token that lacks operator scope.
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
			w.Header().Set("WWW-Authenticate", `Bearer realm="cairnbin"`)
			writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "errors.unauthorized")
			return
		}
		incomingHash := sha256.Sum256([]byte(incoming))
		var zeroHash [32]byte
		if s.operatorTokenHash == zeroHash {
			metric.AuthAttemptsTotal.WithLabelValues(source, "failure").Inc()
			s.authLog(r, "operator", "fail", "token_not_configured")
			writeAPIError(w, r, http.StatusServiceUnavailable, "SERVER_ERROR", "errors.server_error")
			return
		}
		if subtle.ConstantTimeCompare(incomingHash[:], s.operatorTokenHash[:]) != 1 {
			if s.validBearer(r) {
				metric.AuthAttemptsTotal.WithLabelValues(source, "failure").Inc()
				s.authLog(r, "operator", "fail", "insufficient_scope")
				w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope"`)
				writeAPIError(w, r, http.StatusForbidden, "FORBIDDEN", "errors.forbidden")
				return
			}
			metric.AuthAttemptsTotal.WithLabelValues(source, "failure").Inc()
			s.authLog(r, "operator", "fail", "invalid_token")
			w.Header().Set("WWW-Authenticate", `Bearer realm="cairnbin"`)
			writeAPIError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "errors.unauthorized")
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
// Access-Control-Allow-Headers response to name explicitly, including
// Authorization and CSRF headers.
const corsAllowedHeaders = "Content-Type, Accept, X-Requested-With, Authorization, X-CSRF-Token, X-XSRF-Token, MCP-Protocol-Version, Mcp-Method, Mcp-Name"

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

// buildPermissionsPolicy emits the project's fixed Permissions-Policy.
func buildPermissionsPolicy() string {
	features := config.DefaultPermissionsPolicy()
	names := make([]string, 0, len(features))
	for name, value := range features {
		if strings.TrimSpace(value) != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+strings.TrimSpace(features[name]))
	}
	return strings.Join(parts, ", ")
}

// cspNonceKey is the request-context key holding the per-request CSP nonce.
type cspNonceKey struct{}

// cspNonceToken is the placeholder operators put in *_override CSP values; the
// middleware substitutes a fresh nonce per response (PART 11 nonce-based CSP).
const cspNonceToken = "{request_nonce}"

// cspNonce returns the per-request CSP nonce, or "" when nonce-based CSP is not
// in use for this request.
func cspNonce(r *http.Request) string {
	v, _ := r.Context().Value(cspNonceKey{}).(string)
	return v
}

// cspUsesNonce reports whether any configured CSP override references {request_nonce}.
func cspUsesNonce(c config.CSPConfig) bool {
	for _, v := range []string{
		c.ScriptSrcOverride, c.StyleSrcOverride, c.ImgSrcOverride, c.FontSrcOverride,
		c.ConnectSrcOverride, c.FrameSrcOverride, c.FormActionOverride,
	} {
		if strings.Contains(v, cspNonceToken) {
			return true
		}
	}
	return false
}

// newCSPNonce returns 16 random bytes, base64-encoded (128 bits, per the CSP spec minimum).
func newCSPNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// cspDirective returns the override when set, otherwise base plus the extra
// sources (PART 11 per-directive append / override configuration).
func cspDirective(base, extra, override, nonce string) string {
	if override != "" {
		return strings.ReplaceAll(override, cspNonceToken, nonce)
	}
	if extra != "" {
		return base + " " + extra
	}
	return base
}

// buildCSP returns the Content-Security-Policy header value for a request,
// using the server config's CSP settings.  When TLS is off, upgrade-insecure-requests
// is omitted.  In development mode, report-only mode is used.
func (s *Server) buildCSP(r *http.Request) (header string, reportOnly bool) {
	cfg := s.liveCfg()
	if !cfg.Web.CSP.Enabled {
		return "", false
	}

	fqdn := cfg.ResolveFQDN()
	reportURI := "/api/" + s.apiVersion() + "/server/reports/csp"

	csp := cfg.Web.CSP
	nonce := cspNonce(r)
	// script-src stays 'self' only — no 'unsafe-inline' — per the CSP spec
	// (AI.md "Content Security Policy" table): all JS lives in
	// static/js/app.js, so this blocks every injected inline <script>.
	scriptSrc := cspDirective("'self'", csp.ScriptSrcExtra, csp.ScriptSrcOverride, nonce)
	styleSrc := cspDirective("'self' 'unsafe-inline'", csp.StyleSrcExtra, csp.StyleSrcOverride, nonce)
	imgSrc := cspDirective("'self' data: blob: https:", csp.ImgSrcExtra, csp.ImgSrcOverride, nonce)
	fontSrc := cspDirective("'self' https:", csp.FontSrcExtra, csp.FontSrcOverride, nonce)
	connectBase := "'self'"
	if fqdn != "" && fqdn != "localhost" {
		connectBase += " https://" + fqdn
	}
	connectSrc := cspDirective(connectBase, csp.ConnectSrcExtra, csp.ConnectSrcOverride, nonce)
	frameSrc := cspDirective("'self'", csp.FrameSrcExtra, csp.FrameSrcOverride, nonce)
	if r.URL.Path == "/server/contact" && !s.isOverlayRequest(r) {
		var scriptExtra, connectExtra, frameExtra string
		switch cfg.Server.Pages.Contact.Captcha {
		case "recaptcha":
			scriptExtra, connectExtra, frameExtra = " https://www.google.com/recaptcha/ https://www.gstatic.com/recaptcha/", " https://www.google.com/recaptcha/", " https://www.google.com/recaptcha/"
		case "hcaptcha":
			scriptExtra, connectExtra, frameExtra = " https://hcaptcha.com https://*.hcaptcha.com", " https://hcaptcha.com https://*.hcaptcha.com", " https://hcaptcha.com https://*.hcaptcha.com"
		}
		if csp.ScriptSrcOverride == "" {
			scriptSrc += scriptExtra
		}
		if csp.ConnectSrcOverride == "" {
			connectSrc += connectExtra
		}
		if csp.FrameSrcOverride == "" {
			frameSrc += frameExtra
		}
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
	formAction := cspDirective("'self'", csp.FormActionExtra, csp.FormActionOverride, nonce)

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

	// Only include upgrade-insecure-requests when TLS is enabled, and never for
	// Tor / I2P requests (it would rewrite same-origin http:// to a nonexistent https://).
	if cfg.Server.TLS.Enabled && !s.isOverlayRequest(r) {
		directives = append(directives, "upgrade-insecure-requests")
	}

	if csp.ReportsEnabled {
		directives = append(directives,
			"report-to default",
			"report-uri "+reportURI,
		)
	}

	policy := strings.Join(directives, "; ")
	isReportOnly := csp.Mode == "report-only" || cfg.Server.Mode == "development" || cfg.Server.Mode == "debug"
	return policy, isReportOnly
}

// buildReportingHeaders returns the Reporting-Endpoints, Report-To, and NEL header values.
// Per PART 11 these ride on every response (the endpoint URL is always https://),
// independent of whether this listener terminates TLS itself — a TLS-terminating
// reverse proxy in front is the common case. Only an unresolvable FQDN omits them.
func (s *Server) buildReportingHeaders() (endpoints, reportTo, nel string) {
	cfg := s.liveCfg()
	fqdn := cfg.ResolveFQDN()
	if fqdn == "" || fqdn == "localhost" {
		return "", "", ""
	}
	base := "https://" + fqdn + "/api/" + s.apiVersion() + "/server/reports"
	endpoints = `default="` + base + `/default"`
	defaultGroup := `{"group":"default","max_age":10886400,"endpoints":[{"url":"` + base + `/default"}]}`
	if n := cfg.Web.Headers.NEL; n.Enabled {
		endpoints += `, nel="` + base + `/nel"`
		reportTo = `[` + defaultGroup + `,{"group":"nel","max_age":10886400,"endpoints":[{"url":"` + base + `/nel"}]}]`
		nel = fmt.Sprintf(`{"report_to":"nel","max_age":%d,"include_subdomains":%t`, n.MaxAgeSeconds, n.IncludeSubdomains)
		if n.SampleRate < 1 {
			nel += fmt.Sprintf(`,"failure_fraction":%g`, n.SampleRate)
		}
		nel += "}"
	} else {
		reportTo = defaultGroup
	}
	return endpoints, reportTo, nel
}

// wellKnownFallback handles every /.well-known/** request that no allowlisted
// handler claimed: 405 (with Allow) for non-GET/HEAD methods, otherwise 404.
func wellKnownFallback(s *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			s.renderErrorPage(w, r, http.StatusMethodNotAllowed, "That method is not allowed for this resource.")
			return
		}
		s.renderErrorPage(w, r, http.StatusNotFound, "")
	}
}

// privacyOptOutKey marks any honored request privacy signal.
type privacyOptOutKey struct{}

// gpcReceivedKey marks a Sec-GPC signal separately from the optional DNT signal.
type gpcReceivedKey struct{}

// privacyOptOut reports whether the request carried an honored privacy opt-out
// signal (PART 11 "Privacy Signal Headers").
func privacyOptOut(r *http.Request) bool {
	v, _ := r.Context().Value(privacyOptOutKey{}).(bool)
	return v
}

// gpcReceived reports whether this request carried an honored Sec-GPC signal.
func gpcReceived(r *http.Request) bool {
	v, _ := r.Context().Value(gpcReceivedKey{}).(bool)
	return v
}

// gpcSignalHonored reports whether Sec-GPC: 1 is enabled and present.
func gpcSignalHonored(r *http.Request, hc config.HeadersConfig) bool {
	return hc.HonorSecGPC && r.Header.Get("Sec-GPC") == "1"
}

// privacySignalOptOut reports whether Sec-GPC: 1 is honored or DNT: 1 is
// explicitly enabled by web.headers.honor_dnt.
func privacySignalOptOut(r *http.Request, hc config.HeadersConfig) bool {
	if gpcSignalHonored(r, hc) {
		return true
	}
	return hc.HonorDNT && r.Header.Get("DNT") == "1"
}

// headerOrDefault returns v, or def when v is empty.
func headerOrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// clearSiteDataValue builds the Clear-Site-Data header value for token
// revocation / consent withdrawal, honoring execution_contexts (PART 11).
func clearSiteDataValue(c config.ClearSiteDataConfig) string {
	v := `"cache", "cookies", "storage"`
	if c.ExecutionContexts {
		v += `, "executionContexts"`
	}
	return v
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
		hc := cfg.Web.Headers
		h.Set("X-Permitted-Cross-Domain-Policies", headerOrDefault(hc.CrossDomainPolicies, "none"))
		if hc.OriginAgentCluster {
			h.Set("Origin-Agent-Cluster", "?1")
		}
		if hc.DNSPrefetchControl != "" {
			h.Set("X-DNS-Prefetch-Control", hc.DNSPrefetchControl)
		}

		// Cross-origin isolation headers — defaults keep broad compatibility (PART 11).
		h.Set("Cross-Origin-Opener-Policy", headerOrDefault(hc.COOP, "unsafe-none"))
		h.Set("Cross-Origin-Embedder-Policy", headerOrDefault(hc.COEP, "unsafe-none"))
		h.Set("Cross-Origin-Resource-Policy", headerOrDefault(hc.CORP, "cross-origin"))

		// Honored privacy signals suppress non-essential cookies and tracking.
		if privacySignalOptOut(r, hc) {
			r = r.WithContext(context.WithValue(r.Context(), privacyOptOutKey{}, true))
		}
		if gpcSignalHonored(r, hc) {
			r = r.WithContext(context.WithValue(r.Context(), gpcReceivedKey{}, true))
			if !strings.HasPrefix(r.URL.Path, "/static/") {
				s.auditLog(r, audit.Entry{Event: "compliance.gpc_honored", Category: "security", Severity: audit.SeverityInfo, Result: audit.ResultSuccess})
			}
		}

		// Per-request nonce for operators using nonce-based CSP overrides
		// ({request_nonce}); the same value is rendered into every server-side
		// <script> tag via the CSPNonce template variable.
		if cspUsesNonce(cfg.Web.CSP) {
			if nonce, err := newCSPNonce(); err == nil {
				r = r.WithContext(context.WithValue(r.Context(), cspNonceKey{}, nonce))
			}
		}

		// Content-Security-Policy.
		if policy, reportOnly := s.buildCSP(r); policy != "" {
			if reportOnly {
				h.Set("Content-Security-Policy-Report-Only", policy)
			} else {
				h.Set("Content-Security-Policy", policy)
			}
		}

		// Permissions-Policy.
		h.Set("Permissions-Policy", buildPermissionsPolicy())

		// Strict-Transport-Security — only when TLS is active (RFC 6797).
		overlay := s.isOverlayRequest(r)
		if cfg.Server.TLS.Enabled && !overlay {
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

		// Reporting API (modern + legacy NEL) — every clearnet response (PART 11).
		if endpoints, reportTo, nel := s.buildReportingHeaders(); endpoints != "" && !overlay {
			h.Set("Reporting-Endpoints", endpoints)
			h.Set("Report-To", reportTo)
			if nel != "" {
				h.Set("NEL", nel)
			}
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
// An unrecognized service name is 404 rather than falling back to prometheus,
// so a typo in a scraper config fails loudly instead of silently returning the
// wrong payload.
func (s *Server) mountMetricsServices(r chi.Router, base string, withBare bool) {
	handlerFor := func(service string) http.Handler {
		return s.metricsCollector.ServiceHandler(service)
	}

	if withBare {
		r.Method(http.MethodGet, base, handlerFor(metric.ServicePrometheus))
	}

	r.Method(http.MethodGet, base+"/prometheus", handlerFor(metric.ServicePrometheus))
	r.Method(http.MethodGet, base+"/grafana", handlerFor(metric.ServiceGrafana))
	r.Method(http.MethodGet, base+"/loki", handlerFor(metric.ServiceLoki))

	// Catch-all for the service segment so an unknown name 404s with the
	// canonical PART 14 envelope instead of chi's bare-text default.
	r.Get(base+"/{service}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok": false, "error": "NOT_FOUND",
			"message": i18n.Translate(i18n.LangFromRequest(r), "errors.not_found"),
			"details": map[string]string{"service": chi.URLParam(r, "service")},
		})
	})
}

// secFetchMiddleware rejects cross-site state-changing frontend requests per
// PART 11. Backend and compatibility routes bypass this middleware behavior.
func (s *Server) secFetchMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.csrfOutOfScope(w, r) {
			next.ServeHTTP(w, r)
			return
		}
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
				writeAPIError(w, r, http.StatusForbidden, "FORBIDDEN", "errors.forbidden")
				return
			}
		}

		if fetchSite == "cross-site" {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				// Allow if Bearer or API-token auth is present — Bearer is not auto-attached
				// by browsers, so no CSRF risk.
				if !hasBearerCredential(r) {
					// Check CSRF exempt paths.
					if !isCSRFExempt(r.URL.Path, cfg.Server.CSRF.ExemptPaths) {
						writeAPIError(w, r, http.StatusForbidden, "FORBIDDEN", "errors.forbidden")
						return
					}
				}
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

var specReservedSlugs = strings.Fields(`api server static assets healthz health status metrics version webhook webhooks internal private public well-known cgi-bin tmp temp backup backups debug test dev staging null undefined nil true false default index home main admin administrator administration root superuser sysadmin sysop system sys operator owner staff moderator mod manager management manage console panel cpanel backend config configuration wp-admin wp-login phpmyadmin adminer webmaster postmaster hostmaster abuse noreply no-reply bot bots anonymous guest everyone nobody me self www mail ftp smtp imap pop ns ns1 ns2 localhost login logout register signup signin signout auth oauth callback account accounts profile settings dashboard user users org orgs organization organizations team teams group groups password reset verify invite invites preferences search explore discover trending help support docs documentation about contact terms privacy legal security info new edit delete create update all download downloads upload billing sales hello graphql mcp swagger rest rpc ws websocket cdn media uploads files images css js fonts img .well-known robots.txt sitemap.xml feed.xml rss.xml atom.xml favicon.ico manifest.json humans.txt security.txt llms.txt ads.txt feed rss atom sitemap robots favicon manifest browserconfig.xml sw.js service-worker.js`)

// isReservedSlug reports whether id is a reserved system name and must not
// be treated as a paste identifier.
func isReservedSlug(id string) bool {
	name := norm.NFKC.String(strings.ToLower(strings.TrimSpace(id)))
	name = strings.TrimPrefix(name, "@")
	if name == "" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	if _, ok := reservedSlugs[name]; ok {
		return true
	}
	for _, reserved := range specReservedSlugs {
		if name == reserved {
			return true
		}
	}
	for _, ext := range []string{".xml", ".txt", ".json", ".ico", ".html", ".htm", ".php", ".js", ".css", ".map", ".webmanifest", ".png", ".jpg", ".svg", ".zip"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	privileged := strings.Fields("admin administrator administration root superuser sysadmin sysop system sys operator owner staff moderator mod manager management manage console panel cpanel backend config configuration wp-admin wp-login phpmyadmin adminer webmaster postmaster hostmaster abuse noreply no-reply bot bots anonymous guest everyone nobody me self www mail ftp smtp imap pop ns ns1 ns2 localhost")
	for _, reserved := range privileged {
		if strings.HasPrefix(name, reserved) && len(name) > len(reserved) {
			suffix := name[len(reserved):]
			if allASCIIDigits(suffix) {
				return true
			}
		}
	}
	return false
}

func allASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
// already-rendered forms keep working. Every mutating frontend request without
// a bearer header validates the double-submit token, regardless of Origin,
// Referer, or cookie presence. Backend and compatibility paths are filtered
// before token issuance.
func (s *Server) csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.csrfOutOfScope(w, r) {
			next.ServeHTTP(w, r)
			return
		}
		isMutating := r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodPatch || r.Method == http.MethodDelete
		if isWebSocketUpgrade(r) || (isMutating && hasBearerCredential(r)) ||
			(!isMutating && !csrfHTMLNavigation(r)) {
			next.ServeHTTP(w, r)
			return
		}
		cfg := s.liveCfg()
		if !cfg.Server.CSRF.Enabled || len(s.csrfSecret) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		// Inspect the request's existing CSRF cookie before issuing a new one.
		reqCookie, cookieErr := r.Cookie(cfg.Server.CSRF.CookieName)
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
		// Bypass conditions (any one is sufficient). Exactly the AI.md PART 11
		// bypass list — non-mutating methods and operator-declared exempt paths.
		// Bearer and WebSocket requests returned before token setup. A missing CSRF cookie
		// is NOT a bypass: a mutating request without Bearer must present a
		// valid token, and "token absent" is a rejection reason, not an escape.
		bypass := !isMutating ||
			isWebSocketUpgrade(r) ||
			isCSRFExempt(r.URL.Path, cfg.Server.CSRF.ExemptPaths)

		if !bypass {
			// Read the submitted token from the header, falling back to the form
			// field. The no-JS create form posts multipart/form-data (file
			// upload support), and ParseForm never reads a multipart body —
			// parse multipart explicitly so the hidden csrf_token field is
			// visible. ParseMultipartForm caches the parsed form, so the
			// handler's own later parse of the same body is a no-op.
			submitted := r.Header.Get(cfg.Server.CSRF.HeaderName)
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

		secure := s.cookieSecure(r)
		switch cfg.Server.CSRF.Secure {
		case "true":
			secure = true
		case "false":
			secure = false
		}
		if s.i2pRequestB32(r) != "" {
			secure = false
		}

		// Double-submit cookie: HttpOnly=false so progressive-enhancement JS can
		// echo the token into the X-CSRF-Token header; SameSite=Strict is the
		// primary defense (blocks cross-site cookie attachment entirely).
		http.SetCookie(w, &http.Cookie{
			Name:     cfg.Server.CSRF.CookieName,
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

// csrfHTMLNavigation limits token issuance on safe requests to HTML page
// navigations. Static assets, raw/download responses, and non-browser API
// consumers do not need a frontend form token cookie.
func csrfHTMLNavigation(r *http.Request) bool {
	return r.Method == http.MethodGet && (strings.Contains(r.Header.Get("Accept"), "text/html") ||
		r.Header.Get("Sec-Fetch-Dest") == "document")
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
			"message": i18n.Translate(i18n.LangFromRequest(r), "errors.forbidden"),
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
				status := http.StatusMovedPermanently
				if r.Method != http.MethodGet && r.Method != http.MethodHead {
					status = http.StatusPermanentRedirect
				}
				http.Redirect(w, r, r.URL.String(), status)
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

	addr := net.JoinHostPort(strings.Trim(cfg.Server.Address, "[]"), strconv.Itoa(tb.Port))
	ln, err := listener.ListenTCP(addr)
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

// startManagedNetworkServices schedules overlay startup after privilege drop.
// Starting each manager in its own goroutine keeps provider bootstrap from
// delaying the clearnet listener.
func (s *Server) startManagedNetworkServices(ctx context.Context) {
	if s.torManager != nil {
		s.overlayMu.Lock()
		if s.torManager.Enabled() {
			s.torStatus = "starting"
		} else {
			s.torStatus = "disabled"
		}
		s.overlayMu.Unlock()
		if s.torManager.Enabled() {
			go s.startTorManager()
			go s.watchOverlayAddress(ctx, true)
		}
	}
	if s.i2pManager != nil {
		if s.i2pManager.Enabled() {
			provider := s.i2pManager.Provider()
			s.overlayMu.Lock()
			s.i2pStatus = "starting"
			s.i2pProvider = provider
			s.overlayMu.Unlock()
			go s.startI2PManager()
			go s.watchOverlayAddress(ctx, false)
		} else {
			s.overlayMu.Lock()
			s.i2pStatus = "disabled"
			s.i2pProvider = i2p.ProviderNone
			s.overlayMu.Unlock()
		}
	}
}

func (s *Server) watchOverlayAddress(ctx context.Context, isTor bool) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var address string
		if isTor && s.torManager != nil {
			address = s.torManager.OnionAddress()
		} else if !isTor && s.i2pManager != nil {
			address = s.i2pManager.Address()
		}
		if address != "" {
			s.overlayMu.Lock()
			logAddress := s.startupBannerDone
			if isTor {
				s.torHostname = address
				logAddress = logAddress && !s.torAddressLogged
				s.torAddressLogged = s.torAddressLogged || logAddress
			} else {
				s.i2pHostname = address
				logAddress = logAddress && !s.i2pAddressLogged
				s.i2pAddressLogged = s.i2pAddressLogged || logAddress
			}
			s.overlayMu.Unlock()
			if logAddress {
				if isTor {
					log.Printf("tor: address available: %s", address)
				} else {
					log.Printf("i2p: address available: %s", address)
				}
			}
			if isTor {
				s.captureOnionAddress()
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) startTorManager() {
	err := s.torManager.Start()
	running := err == nil && s.torManager.Running()
	s.overlayMu.Lock()
	switch {
	case running:
		s.torStatus = "healthy"
		s.torHostname = s.torManager.OnionAddress()
	case err != nil:
		s.torStatus = "unavailable"
	default:
		s.torStatus = "disabled"
	}
	s.overlayMu.Unlock()
	if err != nil {
		log.Printf("tor: start failed: %v", err)
		return
	}
	if !running {
		return
	}
	go s.torManager.Monitor()
	s.captureOnionAddress()
	if s.liveCfg().Server.Tor.UseNetwork {
		if s.geoipDB != nil {
			go func() {
				if err := s.UpdateGeoIP(); err != nil {
					log.Printf("geoip: initial Tor-routed refresh failed: %v", err)
				}
			}()
		}
		if key := s.projectPGPPublicKey(); key != "" && s.liveCfg().PublishPGPKeyEnabled() {
			go s.publishToKeyservers(key)
		}
	}
}

func (s *Server) startI2PManager() {
	err := s.i2pManager.Start()
	running := err == nil && s.i2pManager.Running()
	s.overlayMu.Lock()
	switch {
	case running:
		s.i2pStatus = "healthy"
		s.i2pHostname = s.i2pManager.Address()
		s.i2pProvider = s.i2pManager.Provider()
	case err != nil:
		s.i2pStatus = "unavailable"
		s.i2pProvider = i2p.ProviderI2Pd
	default:
		s.i2pStatus = "disabled"
		s.i2pProvider = i2p.ProviderNone
	}
	s.overlayMu.Unlock()
	if err != nil {
		log.Printf("i2p: start failed: %v", err)
		return
	}
	if running {
		s.startI2PMonitor()
		s.persistB32Address()
	}
}

func (s *Server) startI2PMonitor() {
	if s.i2pManager == nil {
		return
	}
	s.i2pMonitorOnce.Do(func() { go s.i2pManager.Monitor() })
}

// Run starts the HTTP server on addr and blocks until ctx is cancelled or a
// fatal error occurs, performing graceful shutdown and closing the GeoIP
// database, cache store, and other owned resources on exit.
func (s *Server) Run(ctx context.Context, addr string) error {
	if s.geoipDB != nil {
		defer s.geoipDB.Close()
	}
	if s.cacheStore != nil {
		defer func() { _ = s.cacheStore.Close() }()
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

	if s.i2pManager != nil {
		defer s.i2pManager.Close()
	}
	defer func() {
		if s.stopTermbinFn != nil {
			s.stopTermbinFn()
		}
	}()
	if s.schedulerStopFn != nil {
		defer s.schedulerStopFn()
	}

	cfg := s.liveCfg()

	// PART 15: invalid DNS-01 configuration is a startup error and must not
	// silently fall back to another ACME challenge.
	if ssl.ParseChallenge(cfg.Server.TLS.LetsEncrypt.Challenge) == "dns-01" {
		creds, err := cfg.DecryptDNSCredentials()
		if err != nil {
			log.Printf("ERROR: ssl: dns-01 provider %q: failed to decrypt credentials", cfg.Server.TLS.LetsEncrypt.DNSProvider)
		} else if err := ssl.ValidateDNSProvider(cfg.Server.TLS.LetsEncrypt.DNSProvider, creds); err != nil {
			log.Printf("ERROR: ssl: dns-01 provider %q failed startup validation: %v", cfg.Server.TLS.LetsEncrypt.DNSProvider, err)
		}
	}

	defer func() {
		if !gracefulShutdown(ctx) {
			return
		}
		reason := s.getShutdownReason()
		s.serverStopped(s.liveCfg(), reason)
	}()

	// Resolve DNS-valued trusted_proxies.additional entries once after dropping
	// privileges, then
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
		fqdn := cfg.ResolveFQDN()
		dnsCreds, err := cfg.DecryptDNSCredentials()
		if err != nil {
			log.Printf("ERROR: ssl: failed to decrypt DNS-01 credential values")
			dnsCreds = map[string]string{}
		}
		sslMgr := ssl.NewManager(ssl.Config{
			Enabled: true,
			CertDir: s.configDir + "/ssl",
			Cert:    resolveSSLPath(s.configDir, cfg.Server.TLS.Cert),
			Key:     resolveSSLPath(s.configDir, cfg.Server.TLS.Key),
			FQDN:    fqdn,
			LetsEncrypt: ssl.LetsEncryptConfig{
				Enabled:         cfg.Server.TLS.LetsEncrypt.Enabled,
				Email:           cfg.Server.TLS.LetsEncrypt.Email,
				Challenge:       ssl.ParseChallenge(cfg.Server.TLS.LetsEncrypt.Challenge),
				DNSProviderType: cfg.Server.TLS.LetsEncrypt.DNSProvider,
				DNSCredentials:  dnsCreds,
				Staging:         cfg.Server.TLS.LetsEncrypt.Staging,
				OnDNSValidated:  s.dnsValidatedCallback(),
			},
		})
		s.setSSLManager(sslMgr)

		domains := []string{fqdn}
		tlsCfg, err := sslMgr.GetTLSConfig(domains)
		if err != nil {
			s.setTLSHealth(false)
			log.Printf("warning: ssl: TLS setup failed; HTTPS is disabled for this run and the server is continuing over HTTP: %v", err)
			// Fall through to plain HTTP so the server still starts.
		} else if tlsCfg != nil {
			s.setTLSHealth(true)
			srv.TLSConfig = tlsCfg
			// Wrap the router so autocert HTTP-01 challenges are handled on port 80.
			srv.Handler = sslMgr.GetHTTPHandler(s.router)
			listeners, err := s.bindPrivilegedListenersAndDrop(addr)
			if err != nil {
				return fmt.Errorf("bind %s: %w", addr, err)
			}
			s.afterPrivilegeDrop(ctx, cfg)
			s.startManagedNetworkServices(ctx)
			ln, err := bindListenerAfterDrop(addr, listeners[0])
			if err != nil {
				return fmt.Errorf("bind %s: %w", addr, err)
			}
			go func() {
				if err := srv.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
					errCh <- err
				}
			}()
			s.printStartupBanner()
			s.serverStarted(cfg, addr)

			select {
			case <-ctx.Done():
				s.shuttingDown.Store(true)
				shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				return srv.Shutdown(shut)
			case err := <-errCh:
				return fmt.Errorf("serve https %s: %w", addr, err)
			}
		} else {
			s.setTLSHealth(false)
			log.Printf("warning: ssl: TLS setup returned no configuration; HTTPS is disabled for this run and the server is continuing over HTTP")
		}
	}

	// Plain HTTP (no TLS, or TLS setup failed).
	listeners, err := s.bindPrivilegedListenersAndDrop(addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}
	s.afterPrivilegeDrop(ctx, cfg)
	s.startManagedNetworkServices(ctx)
	ln, err := bindListenerAfterDrop(addr, listeners[0])
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	s.printStartupBanner()
	s.serverStarted(cfg, addr)

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
	fqdn := cfg.ResolveFQDN()
	dnsCreds, err := cfg.DecryptDNSCredentials()
	if err != nil {
		log.Printf("ERROR: ssl: failed to decrypt DNS-01 credential values")
		dnsCreds = map[string]string{}
	}
	sslMgr := ssl.NewManager(ssl.Config{
		Enabled: true,
		CertDir: s.configDir + "/ssl",
		Cert:    resolveSSLPath(s.configDir, cfg.Server.TLS.Cert),
		Key:     resolveSSLPath(s.configDir, cfg.Server.TLS.Key),
		FQDN:    fqdn,
		LetsEncrypt: ssl.LetsEncryptConfig{
			Enabled:         cfg.Server.TLS.LetsEncrypt.Enabled,
			Email:           cfg.Server.TLS.LetsEncrypt.Email,
			Challenge:       ssl.ParseChallenge(cfg.Server.TLS.LetsEncrypt.Challenge),
			DNSProviderType: cfg.Server.TLS.LetsEncrypt.DNSProvider,
			DNSCredentials:  dnsCreds,
			Staging:         cfg.Server.TLS.LetsEncrypt.Staging,
			OnDNSValidated:  s.dnsValidatedCallback(),
		},
	})
	s.setSSLManager(sslMgr)

	domains := []string{fqdn}
	tlsCfg, err := sslMgr.GetTLSConfig(domains)
	if err != nil {
		s.setTLSHealth(false)
		log.Printf("warning: dual-port TLS setup failed; HTTPS is disabled for this run and the server is continuing on HTTP port %s: %v", httpPort, err)
		return s.runHTTPOnly(ctx, cfg, host, httpPort, sslMgr.GetHTTPHandler(s.router), readTimeout, writeTimeout, idleTimeout)
	}
	if tlsCfg == nil {
		s.setTLSHealth(false)
		log.Printf("warning: dual-port TLS setup returned no configuration; HTTPS is disabled for this run and the server is continuing on HTTP port %s", httpPort)
		return s.runHTTPOnly(ctx, cfg, host, httpPort, sslMgr.GetHTTPHandler(s.router), readTimeout, writeTimeout, idleTimeout)
	}
	s.setTLSHealth(true)

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

	// Bind only privileged sockets as root; drop privileges before overlays and
	// bind unprivileged ports afterward as the service account.
	listeners, err := s.bindPrivilegedListenersAndDrop(httpAddr, httpsAddr)
	if err != nil {
		return fmt.Errorf("dual-port: %w", err)
	}
	s.afterPrivilegeDrop(ctx, cfg)
	s.startManagedNetworkServices(ctx)
	httpLn, err := bindListenerAfterDrop(httpAddr, listeners[0])
	if err != nil {
		return fmt.Errorf("dual-port: listen HTTP %s: %w", httpAddr, err)
	}
	httpsLn, err := bindListenerAfterDrop(httpsAddr, listeners[1])
	if err != nil {
		_ = httpLn.Close()
		return fmt.Errorf("dual-port: listen HTTPS %s: %w", httpsAddr, err)
	}

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
	s.printStartupBanner()
	s.serverStarted(cfg, httpAddr+","+httpsAddr)

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

func (s *Server) runHTTPOnly(ctx context.Context, cfg *config.Config, host, port string, handler http.Handler, readTimeout, writeTimeout, idleTimeout time.Duration) error {
	addr := net.JoinHostPort(host, port)
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}
	if len(s.preparedListeners) > 1 {
		for _, extra := range s.preparedListeners[1:] {
			if extra != nil {
				_ = extra.Close()
			}
		}
		s.preparedListeners = s.preparedListeners[:1]
	}
	listeners, err := s.bindPrivilegedListenersAndDrop(addr)
	if err != nil {
		return fmt.Errorf("dual-port HTTP fallback: %w", err)
	}
	s.afterPrivilegeDrop(ctx, cfg)
	s.startManagedNetworkServices(ctx)
	ln, err := bindListenerAfterDrop(addr, listeners[0])
	if err != nil {
		return fmt.Errorf("dual-port HTTP fallback listen %s: %w", addr, err)
	}
	errCh := make(chan error, 1)
	go func() {
		if serveErr := httpSrv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			errCh <- serveErr
		}
	}()
	s.printStartupBanner()
	s.serverStarted(cfg, addr)
	select {
	case <-ctx.Done():
		s.shuttingDown.Store(true)
		shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shut)
	case serveErr := <-errCh:
		return fmt.Errorf("dual-port HTTP fallback serve %s: %w", addr, serveErr)
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
		Datasets:  s.datasetChecks.Snapshot(),
	}

	var databaseErr error
	if probe, ok := s.db.(database.ConnectionProbe); ok {
		databaseErr = probe.CheckConnection(context.Background())
	} else {
		databaseErr = s.db.Ping()
	}
	if databaseErr != nil {
		checks.Database = "error"
	}

	// Ping the cache driver (non-fatal for health status but surfaced in checks).
	if s.cacheStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := s.cacheStore.Ping(ctx); err != nil {
			checks.Cache = "error"
		} else if s.cacheDegraded {
			checks.Cache = "degraded"
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

	// Overlay bootstrap is non-critical: starting is healthy, while a failed
	// provider degrades the overall status without interrupting clearnet.
	torInfo := s.buildTorInfo()
	if torInfo.Enabled {
		checks.Tor = "ok"
		if torInfo.Status == "unavailable" {
			checks.Tor = "degraded"
		}
	}

	// I2P health follows the same non-critical overlay contract as Tor.
	i2pInfo := s.buildI2PInfo()
	if i2pInfo.Enabled {
		checks.I2P = "ok"
		if i2pInfo.Status == "unavailable" {
			checks.I2P = "degraded"
		}
	}

	status := "healthy"
	if checks.Database == "error" || checks.Disk == "error" {
		status = "unhealthy"
	} else if checks.Cache == "error" || checks.Cache == "degraded" || checks.Scheduler == "error" || checks.Tor == "error" || checks.I2P == "error" || checks.Tor == "degraded" || checks.I2P == "degraded" {
		status = "degraded"
	}
	for _, check := range checks.Datasets {
		if check.Status == "error" || check.Status == "degraded" {
			if check.Critical {
				status = "unhealthy"
				break
			}
			if status == "healthy" {
				status = "degraded"
			}
		}
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
	s.overlayMu.RLock()
	status, onion := s.torStatus, s.torHostname
	s.overlayMu.RUnlock()
	if status == "" {
		status = "starting"
	}
	running := status == "healthy"
	if !running {
		onion = ""
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
	s.overlayMu.RLock()
	status, addr, provider := s.i2pStatus, s.i2pHostname, s.i2pProvider
	s.overlayMu.RUnlock()
	if !s.i2pManager.Enabled() || status == "disabled" {
		return I2PInfo{Enabled: false, Running: false, Status: "disabled", Hostname: "", Provider: "none"}
	}
	if status == "" {
		status = "starting"
	}
	running := status == "healthy"
	if !running {
		addr = ""
	}
	return I2PInfo{
		Enabled:  true,
		Running:  running,
		Status:   status,
		Hostname: addr,
		Provider: provider.String(),
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

// handleHealthz serves /server/healthz using the standard frontend content
// negotiation rules (PART 13/14): JSON for clients that ask for
// "Accept: application/json", plain text for non-interactive HTTP tools, and
// HTML for browsers.
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
	_, _ = fmt.Fprintf(w, "project.name: %s\n", hr.Project.Name)
	_, _ = fmt.Fprintf(w, "project.tagline: %s\n", hr.Project.Tagline)
	_, _ = fmt.Fprintf(w, "project.description: %s\n", hr.Project.Description)
	_, _ = fmt.Fprintf(w, "status: %s\n", hr.Status)
	_, _ = fmt.Fprintf(w, "version: %s\n", hr.Version)
	_, _ = fmt.Fprintf(w, "go_version: %s\n", hr.GoVersion)
	_, _ = fmt.Fprintf(w, "build.commit: %s\n", hr.Build.Commit)
	_, _ = fmt.Fprintf(w, "build.date: %s\n", hr.Build.Date)
	_, _ = fmt.Fprintf(w, "uptime: %s\n", hr.Uptime)
	_, _ = fmt.Fprintf(w, "mode: %s\n", hr.Mode)
	_, _ = fmt.Fprintf(w, "timestamp: %s\n", hr.Timestamp.UTC().Format(time.RFC3339))
	if hr.Maintenance != nil {
		_, _ = fmt.Fprintf(w, "maintenance.reason: %s\n", hr.Maintenance.Reason)
		_, _ = fmt.Fprintf(w, "maintenance.message: %s\n", hr.Maintenance.Message)
		_, _ = fmt.Fprintf(w, "maintenance.since: %s\n", hr.Maintenance.Since.UTC().Format(time.RFC3339))
		_, _ = fmt.Fprintf(w, "maintenance.self_healing.enabled: %t\n", hr.Maintenance.SelfHealing.Enabled)
		_, _ = fmt.Fprintf(w, "maintenance.self_healing.attempts: %d\n", hr.Maintenance.SelfHealing.Attempts)
		if hr.Maintenance.SelfHealing.LastAttempt != nil {
			_, _ = fmt.Fprintf(w, "maintenance.self_healing.last_attempt: %s\n", hr.Maintenance.SelfHealing.LastAttempt.UTC().Format(time.RFC3339))
		}
		if hr.Maintenance.SelfHealing.NextAttempt != nil {
			_, _ = fmt.Fprintf(w, "maintenance.self_healing.next_attempt: %s\n", hr.Maintenance.SelfHealing.NextAttempt.UTC().Format(time.RFC3339))
		}
	}
	_, _ = fmt.Fprintf(w, "features.tor.enabled: %t\n", hr.Features.Tor.Enabled)
	_, _ = fmt.Fprintf(w, "features.tor.running: %t\n", hr.Features.Tor.Running)
	if hr.Features.Tor.Status != "" {
		_, _ = fmt.Fprintf(w, "features.tor.status: %s\n", hr.Features.Tor.Status)
	}
	if hr.Features.Tor.Hostname != "" {
		_, _ = fmt.Fprintf(w, "features.tor.hostname: %s\n", hr.Features.Tor.Hostname)
	}
	_, _ = fmt.Fprintf(w, "features.i2p.enabled: %t\n", hr.Features.I2P.Enabled)
	_, _ = fmt.Fprintf(w, "features.i2p.running: %t\n", hr.Features.I2P.Running)
	_, _ = fmt.Fprintf(w, "features.i2p.status: %s\n", hr.Features.I2P.Status)
	_, _ = fmt.Fprintf(w, "features.i2p.hostname: %s\n", hr.Features.I2P.Hostname)
	_, _ = fmt.Fprintf(w, "features.i2p.provider: %s\n", hr.Features.I2P.Provider)
	_, _ = fmt.Fprintf(w, "features.geoip: %t\n", hr.Features.GeoIP)
	_, _ = fmt.Fprintf(w, "checks.database: %s\n", hr.Checks.Database)
	_, _ = fmt.Fprintf(w, "checks.cache: %s\n", hr.Checks.Cache)
	_, _ = fmt.Fprintf(w, "checks.disk: %s\n", hr.Checks.Disk)
	_, _ = fmt.Fprintf(w, "checks.scheduler: %s\n", hr.Checks.Scheduler)
	if hr.Checks.Tor != "" {
		_, _ = fmt.Fprintf(w, "checks.tor: %s\n", hr.Checks.Tor)
	}
	if hr.Checks.I2P != "" {
		_, _ = fmt.Fprintf(w, "checks.i2p: %s\n", hr.Checks.I2P)
	}
	for _, check := range hr.Checks.Datasets {
		_, _ = fmt.Fprintf(w, "checks.%s: %s\n", check.Key, check.Status)
	}
	if hr.Checks.Config != "" {
		_, _ = fmt.Fprintf(w, "checks.config: %s\n", hr.Checks.Config)
	}
	_, _ = fmt.Fprintf(w, "stats.requests_total: %d\n", hr.Stats.RequestsTotal)
	_, _ = fmt.Fprintf(w, "stats.requests_24h: %d\n", hr.Stats.Requests24h)
	_, _ = fmt.Fprintf(w, "stats.active_connections: %d\n", hr.Stats.ActiveConns)
}

func (s *Server) handleHealthzJSON(w http.ResponseWriter, r *http.Request) {
	hr := s.buildHealthResponse()
	writeJSON(w, httpStatusForHealth(hr.Status), hr)
}

// handleHealthzAPI serves the /api/** health routes (versioned and the
// unversioned /api/healthz alias) using the PART 14 API negotiation rules:
// JSON by default, plain text for the .txt extension, Accept: text/plain, or
// non-interactive clients.
func (s *Server) handleHealthzAPI(w http.ResponseWriter, r *http.Request) {
	if httputil.GetAPIResponseFormat(r) == "text" {
		hr := s.buildHealthResponse()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(httpStatusForHealth(hr.Status))
		writeHealthText(w, hr)
		return
	}
	s.handleHealthzJSON(w, r)
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
			"name":    "Cairnbin API",
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
	base := strings.TrimRight(s.baseURL(r), "/")
	data := map[string]interface{}{
		"name":    "cairnbin",
		"version": s.version,
		"api":     map[string]string{"current_version": s.apiVersion()},
		"urls":    []string{base},

		// CLI update metadata (PART 32).
		// cli_versions maps os-arch → {version, sha256} for each CLI binary this
		// server hosts at /cli/binaries/{name}; empty when none are available.
		"cli_versions":    s.cliVersionsPayload(),
		"cli_min_version": s.liveCfg().Server.Update.CLIMinVersion,
	}
	cfg := s.liveCfg()
	if cfg.Server.Tor.Expose && s.buildTorInfo().Enabled {
		if address := s.TorOnionAddress(); address != "" {
			data["onion"] = address
		}
	}
	if cfg.Server.I2P.Expose && s.buildI2PInfo().Enabled {
		if address := s.I2PAddress(); address != "" {
			data["i2p"] = address
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":   true,
		"data": data,
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
	if pastes == nil {
		pastes = []model.PasteListItem{}
	}
	data := map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
		"BaseURL":   s.baseURL(r),
		"Recent":    pastes,
	}
	if detectClientType(r) == "json" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": pastes})
		return
	}

	// Content negotiation: HTTP tools get the full template rendered as plain text.
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "home.html", data)
		if err != nil {
			// Fallback when templates are unavailable: minimal plain text.
			_, _ = fmt.Fprintf(w, "%s\nPOST %s/api/%s/pastes to create a paste.\n", s.liveCfg().Web.SiteTitle, s.baseURL(r), s.apiVersion())
			for _, p := range pastes {
				_, _ = fmt.Fprintf(w, "%s/%s\t%s\n", s.baseURL(r), p.ID, p.Title)
			}
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
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
	// cairnbin_pref_{sort,order,per_page} cookies; otherwise fall back to
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
	pasteType := strings.TrimSpace(q.Get("type"))

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

	pastes, total, _ := s.db.SearchPublicPastes(page, limit, search, sortBy, order, pasteType)
	if pastes == nil {
		pastes = []model.PasteListItem{}
	}
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
		"Type":        pasteType,
		"Sort":        sortBy,
		"Order":       order,
		"CurrentPath": r.URL.Path,
	}
	if detectClientType(r) == "json" {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":   true,
			"data": pastes,
			"pagination": map[string]interface{}{
				"page": page, "limit": limit, "total": total, "pages": pages,
			},
		})
		return
	}

	// Content negotiation: HTTP tools get the full template rendered as plain text.
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "recent.html", data)
		if err != nil {
			// Fallback when templates are unavailable: minimal plain text.
			_, _ = fmt.Fprintf(w, "# Recent pastes (%d total)\n", total)
			for _, p := range pastes {
				pasteType := p.ContentType
				if pasteType == "" {
					pasteType = p.Language
				}
				_, _ = fmt.Fprintf(w, "%s/%s\t%s\t%s\n", s.baseURL(r), p.ID, pasteType, p.Title)
			}
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}

	s.renderTemplate(w, r, "recent.html", data)
}

// detectClientType returns "html", "json", or "text" based on User-Agent and
// Accept header per PART 14 content negotiation rules.
//
// Priority order:
//  1. Explicit Accept header overrides UA detection, checked application/json,
//     then text/html, then text/plain (PART 16 Smart Content Detection).
//  2. Our CLI client (cairnbin-cli/) → "json" (INTERACTIVE, renders its own TUI).
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
			writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
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
	content := handler.HighlightedContent(paste, true)
	var markdownContent template.HTML
	if cc.IsMarkdown {
		content = template.HTML(template.HTMLEscapeString(paste.Content))
		markdownContent = renderPasteMarkdown(paste.Content)
	}

	s.renderTemplate(w, r, "paste.html", map[string]interface{}{
		"SiteTitle":       s.liveCfg().Web.SiteTitle,
		"Theme":           s.liveCfg().Web.Theme,
		"Paste":           paste,
		"ID":              id,
		"Content":         content,
		"MarkdownContent": markdownContent,
		"IsImage":         cc.IsImage,
		"IsMarkdown":      cc.IsMarkdown,
		"ContentLabel":    cc.ContentLabel,
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
	IsMarkdown      bool
	IsAudio         bool
	IsVideo         bool
	IsActiveContent bool
	IsBinary        bool
	FileContentType string
	ContentLabel    string
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
		IsMarkdown:      fileCT == "text/markdown" || strings.EqualFold(paste.Language, "markdown") || strings.EqualFold(filepath.Ext(paste.Title), ".md") || strings.EqualFold(filepath.Ext(paste.Title), ".markdown"),
		IsAudio:         isAudio,
		IsVideo:         isVideo,
		IsActiveContent: isActiveContent,
		IsBinary:        isBinary,
		FileContentType: fileCT,
		ContentLabel:    pasteContentLabel(paste, fileCT),
		ImageDataURI:    imageDataURI,
		MediaDataURI:    mediaDataURI,
	}
}

func pasteContentLabel(paste *model.Paste, contentType string) string {
	ext := strings.ToLower(filepath.Ext(paste.Title))
	labels := map[string]string{
		".avif": "AVIF image", ".bmp": "BMP image", ".gif": "GIF image", ".jpeg": "JPEG image", ".jpg": "JPEG image", ".png": "PNG image", ".webp": "WebP image",
		".md": "Markdown", ".markdown": "Markdown", ".txt": "Text",
		".doc": "Word document", ".docx": "Word document (DOCX)", ".odt": "OpenDocument text",
		".pdf": "PDF document", ".rtf": "Rich text document", ".xls": "Excel spreadsheet", ".xlsx": "Excel spreadsheet (XLSX)", ".ods": "OpenDocument spreadsheet",
		".ppt": "PowerPoint presentation", ".pptx": "PowerPoint presentation (PPTX)", ".odp": "OpenDocument presentation",
	}
	if label := labels[ext]; label != "" {
		return label
	}
	if contentType != "" {
		return contentType
	}
	if paste.Language != "" {
		return paste.Language
	}
	return "Text"
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
		writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	// ?download=1 hands the file to the browser's native save flow via a plain
	// link (PART 16 Import/Export UI Convention); the inline <img> omits it.
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+qrDownloadName(id)+`"`)
	}
	w.Write(png) //nolint:errcheck
}

// qrDownloadName builds the attachment filename for a QR download, keeping
// only characters that are safe inside a quoted Content-Disposition value.
func qrDownloadName(id string) string {
	var b strings.Builder
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 {
		return "paste-qr.png"
	}
	return "paste-" + b.String() + "-qr.png"
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
		writeAPIError(w, r, http.StatusBadRequest, "BAD_REQUEST", "errors.bad_request")
		return
	}
	token := r.FormValue("token")
	// Cookie fallback: web management forms may use the owner_token cookie set
	// on creation when the token field is left blank (PART 11 / PART 16).
	// API routes (/api/...) never reach this handler, so this is always a
	// browser-context form POST — cookie use is safe here.
	if token == "" {
		token = browsercookie.OwnerTokenValue(r)
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
			Secure:   s.ownerCookieSecure(r),
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
		html, err := s.renderTemplateToString(r, "about.html", s.aboutPageData(r))
		if err != nil {
			_, _ = fmt.Fprintf(w, "%s\n%s/server/about\n", s.liveCfg().Web.SiteTitle, s.baseURL(r))
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "about.html", s.aboutPageData(r))
}

// aboutPageData builds the /server/about template data, sourcing the tagline,
// description, feature list, and links from the operator's Branding config with a
// real IDEA.md fallback (PART 16 page-content sourcing). No content is hardcoded
// in the template and no field is ever blank or a generic placeholder.
func (s *Server) aboutPageData(r *http.Request) map[string]interface{} {
	b := s.liveCfg().Server.Branding
	lang := i18n.LangFromRequest(r)
	defaults := config.DefaultConfig().Server.Branding
	tagline := b.EffectiveTagline()
	if tagline == defaults.EffectiveTagline() {
		tagline = i18n.Translate(lang, "about.default_tagline")
	}
	description := b.EffectiveDescription()
	if description == defaults.EffectiveDescription() {
		description = i18n.Translate(lang, "about.default_description")
	}
	features := b.EffectiveFeatures()
	if len(features) == len(defaults.EffectiveFeatures()) {
		matchesDefaults := true
		for idx := range features {
			if features[idx] != defaults.EffectiveFeatures()[idx] {
				matchesDefaults = false
				break
			}
		}
		if matchesDefaults {
			features = make([]string, len(features))
			for idx := range features {
				features[idx] = i18n.Translate(lang, fmt.Sprintf("about.default_feature_%d", idx+1))
			}
		}
	}
	data := s.pageData()
	data["Tagline"] = tagline
	data["Description"] = description
	data["Features"] = features
	data["Links"] = b.EffectiveLinks()
	data["GeoIPEnabled"] = s.liveCfg().Server.GeoIP.Enabled
	return data
}

func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"SiteTitle": s.liveCfg().Web.SiteTitle,
		"Theme":     s.liveCfg().Web.Theme,
		"BaseURL":   s.baseURL(r),
		"MoreHelp":  s.hubPageData(s.helpPageRegistry(), i18n.LangFromRequest(r)),
	}
	helpContent := localizedHelpContent(s.liveCfg().Server.Pages.Help.Content, i18n.LangFromRequest(r))
	data["PageContent"] = renderMarkdown(helpContent)
	s.injectTermbinData(r, data)
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "help.html", data)
		if err != nil {
			_, _ = fmt.Fprintf(w, "Usage: curl %s/create\nAPI docs: %s/server/docs/swagger\n", s.baseURL(r), s.baseURL(r))
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
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
		"SiteTitle":        cfg.Web.SiteTitle,
		"Theme":            cfg.Web.Theme,
		"Contact":          s.publicContactEmail(r),
		"Abuse":            s.publicAbuseEmail(r),
		"BaseURL":          s.baseURL(r),
		"FormEnabled":      pc.Enabled,
		"CaptchaType":      s.contactCaptchaProvider(r),
		"CaptchaSiteKey":   pc.CaptchaSiteKey,
		"CaptchaScript":    map[string]string{"recaptcha": "https://www.google.com/recaptcha/api.js", "hcaptcha": "https://js.hcaptcha.com/1/api.js"}[s.contactCaptchaProvider(r)],
		"ContactFormToken": s.issueContactFormToken(time.Now(), contactAntispam(cfg)),
	}
	if pc.Enabled {
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
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if !s.liveCfg().Server.Pages.Contact.Enabled {
		s.renderErrorPage(w, r, http.StatusNotFound, "The requested resource was not found.")
		return
	}
	data := s.contactData(r)
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "contact.html", data)
		if err != nil {
			if email := s.publicContactEmail(r); email != "" {
				_, _ = fmt.Fprintf(w, "Contact: %s\n", email)
			}
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "contact.html", data)
}

// handleContactPost processes a /server/contact form submission: it validates
// the required fields and captcha, then emails the message to the general
// contact recipient (falling back to admin) and re-renders the page with a
// success or error banner (PART 31, AI.md /server/contact).
func (s *Server) handleContactPost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	cfg := s.liveCfg()
	pc := cfg.Server.Pages.Contact

	data := s.contactData(r)

	// When the form is disabled, show the static page without accepting input.
	if !pc.Enabled {
		w.WriteHeader(http.StatusNotFound)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	if !s.validContactOrigin(r) {
		s.recordContactBlock(contactAntispam(cfg), r, "origin", true, time.Now())
		data["ContactError"] = "Please submit the form from this site."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, contactBodyLimit)
	if err := r.ParseForm(); err != nil {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		data["ContactError"] = "Invalid form submission."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	fields, singleValues := contactFieldValues(r.PostForm)
	for _, key := range []string{"good_faith", "accuracy"} {
		if _, ok := fields[key]; !ok {
			fields[key] = "0"
		}
	}
	step := strings.TrimSpace(r.PostFormValue("step"))
	if step == "" {
		step = "submit"
	}
	if step != "submit" && step != "confirm" && step != "edit" {
		s.recordContactBlock(contactAntispam(cfg), r, "review", true, time.Now())
		data["ContactError"] = "Please read the details and confirm."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	if step == "edit" {
		data["ContactFormToken"] = s.issueContactFormToken(time.Now(), contactAntispam(cfg))
		data["FormName"] = fields["name"]
		data["FormEmail"] = fields["email"]
		data["FormSubject"] = fields["subject"]
		data["FormMessage"] = fields["message"]
		data["FormGPG"] = fields["researcher_gpg"]
		data["FormComponentOther"] = fields["component_other"]
		data["FormEndpoint"] = fields["endpoint"]
		data["FormSummary"] = fields["summary"]
		data["FormSteps"] = fields["steps"]
		data["FormImpact"] = fields["impact"]
		data["FormSuggestedFix"] = fields["suggested_fix"]
		data["FormCreditName"] = fields["credit_name"]
		data["FormType"] = fields["type"]
		data["FormReportedURL"] = fields["reported_url"]
		data["FormReason"] = fields["reason"]
		data["FormCopyrightedWork"] = fields["copyrighted_work"]
		data["FormGoodFaith"] = config.IsTruthy(fields["good_faith"])
		data["FormAccuracy"] = config.IsTruthy(fields["accuracy"])
		data["FormSignature"] = fields["signature"]
		data["FormRequestKind"] = fields["request_kind"]
		data["SecurityMode"] = strings.TrimSpace(fields["security_id"]) != ""
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
			if !singleValues || securityContactShapeError(fields) != "" {
				s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
				data["ContactError"] = "Invalid form submission."
				w.WriteHeader(http.StatusBadRequest)
				s.renderTemplate(w, r, "contact.html", data)
				return
			}
			guard := s.contactFormGuard(r, cfg, fields, time.Now())
			if guard == "honeypot" {
				data["SecurityMode"] = true
				data["SecurityID"] = suppliedSecID
				s.setContactReview(data, fields, "d", time.Now())
				s.renderTemplate(w, r, "contact.html", data)
				return
			}
			if guard != "" {
				if guard == "banned" {
					w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(contactAntispam(cfg))))
					w.WriteHeader(http.StatusTooManyRequests)
					data["ContactError"] = "Please try again later."
				} else {
					w.WriteHeader(http.StatusBadRequest)
					data["ContactError"] = "Please wait a moment and submit again."
				}
				s.renderTemplate(w, r, "contact.html", data)
				return
			}
			a := contactAntispam(cfg)
			now := time.Now()
			if step == "submit" {
				response := r.PostFormValue("g-recaptcha-response")
				if response == "" {
					response = r.PostFormValue("h-captcha-response")
				}
				captchaID := r.PostFormValue("captcha_id")
				if captchaID == "" {
					captchaID = r.PostFormValue("captcha_token")
				}
				captchaAnswer := r.PostFormValue("captcha_answer")
				if captchaAnswer == "" {
					captchaAnswer = r.PostFormValue("captcha")
				}
				if !s.verifyContactCaptcha(r, captchaID, captchaAnswer, response) {
					s.recordContactBlock(a, r, "captcha", true, now)
					data["ContactError"] = "Captcha answer was incorrect. Please try again."
					w.WriteHeader(http.StatusBadRequest)
					s.renderTemplate(w, r, "contact.html", data)
					return
				}
				if reason := s.checkContactSubmission(a, r, fields["email"], now, false); reason != "" {
					s.recordContactBlock(a, r, reason, false, now)
					w.Header().Set("Retry-After", strconv.Itoa(contactRetryAfter(a)))
					w.WriteHeader(http.StatusTooManyRequests)
					data["ContactError"] = "Please try again later."
					s.renderTemplate(w, r, "contact.html", data)
					return
				}
				data["SecurityMode"] = true
				data["SecurityID"] = suppliedSecID
				s.setContactReview(data, fields, "", now)
				s.renderTemplate(w, r, "contact.html", data)
				return
			}
			valid, flags := s.contactReviewConfirmed(r, fields, now, a)
			if !valid {
				s.recordContactBlock(a, r, "review", true, now)
				data["ContactError"] = "Please read the details and confirm."
				w.WriteHeader(http.StatusBadRequest)
				s.renderTemplate(w, r, "contact.html", data)
				return
			}
			if flags == "d" {
				trackingID, err := randomHex(8)
				if err != nil {
					trackingID = "0000000000000000"
				}
				data["ContactSuccess"] = fmt.Sprintf("Thank you — your security report was received and encrypted. Your tracking ID is sec_%s. An acknowledgment email may be sent with your private status link.", trackingID)
				s.renderTemplate(w, r, "contact.html", data)
				return
			}
			s.handleSecurityReport(w, markContactCaptchaVerified(r), cfg)
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
	requiredFields, allowedFields := standardContactFields()
	if !singleValues || contactShapeError(fields, requiredFields, allowedFields) != "" {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		data["ContactError"] = "Invalid form submission."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
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
	data["FormType"] = fields["type"]
	data["FormReportedURL"] = fields["reported_url"]
	data["FormReason"] = fields["reason"]
	data["FormCopyrightedWork"] = fields["copyrighted_work"]
	data["FormGoodFaith"] = config.IsTruthy(fields["good_faith"])
	data["FormAccuracy"] = config.IsTruthy(fields["accuracy"])
	data["FormSignature"] = fields["signature"]
	data["FormRequestKind"] = fields["request_kind"]

	if name == "" || fromEmail == "" || subject == "" || message == "" {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		data["ContactError"] = "All fields are required."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	for field, limit := range map[string]int{"name": 100, "email": 254, "subject": 200, "message": 5000} {
		value := map[string]string{"name": name, "email": fromEmail, "subject": subject, "message": message}[field]
		if utf8.RuneCountInString(value) > limit {
			s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
			data["ContactError"] = fmt.Sprintf("%s must be at most %d characters.", strings.ToUpper(field[:1])+field[1:], limit)
			w.WriteHeader(http.StatusBadRequest)
			s.renderTemplate(w, r, "contact.html", data)
			return
		}
	}
	if !validContactEmail(fromEmail) {
		s.recordContactBlock(contactAntispam(cfg), r, "shape", false, time.Now())
		data["ContactError"] = "Please enter a valid email address."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	guard := s.contactFormGuard(r, cfg, fields, time.Now())
	honeypot := guard == "honeypot"
	if guard != "" && !honeypot {
		if guard == "banned" {
			w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(contactAntispam(cfg))))
			w.WriteHeader(http.StatusTooManyRequests)
			data["ContactError"] = "Please try again later."
		} else {
			w.WriteHeader(http.StatusBadRequest)
			data["ContactError"] = "Please wait a moment and submit again."
		}
		s.renderTemplate(w, r, "contact.html", data)
		return
	}

	response := r.PostFormValue("g-recaptcha-response")
	if response == "" {
		response = r.PostFormValue("h-captcha-response")
	}
	captchaID := r.PostFormValue("captcha_id")
	if captchaID == "" {
		captchaID = r.PostFormValue("captcha_token")
	}
	captchaAnswer := r.PostFormValue("captcha_answer")
	if captchaAnswer == "" {
		captchaAnswer = r.PostFormValue("captcha")
	}
	if step == "submit" && !honeypot && !s.verifyContactCaptcha(r, captchaID, captchaAnswer, response) {
		banned := s.recordContactBlock(contactAntispam(cfg), r, "captcha", true, time.Now())
		if banned {
			w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(contactAntispam(cfg))))
			w.WriteHeader(http.StatusTooManyRequests)
			data["ContactError"] = "Please try again later."
			s.renderTemplate(w, r, "contact.html", data)
			return
		}
		data["ContactError"] = "Captcha answer was incorrect. Please try again."
		w.WriteHeader(http.StatusBadRequest)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	a := contactAntispam(cfg)
	now := time.Now()
	if step == "submit" && !honeypot {
		if reason := contactContentReason(a, name, fromEmail, subject, message); reason != "" {
			banned := s.recordContactBlock(a, r, reason, true, time.Now())
			if banned {
				w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(a)))
				w.WriteHeader(http.StatusTooManyRequests)
				data["ContactError"] = "Please try again later."
			} else {
				w.WriteHeader(http.StatusBadRequest)
				data["ContactError"] = "Your message could not be sent."
			}
			s.renderTemplate(w, r, "contact.html", data)
			return
		}
		if reason := s.reportContentReason(cfg, r, fields); reason != "" {
			banned := s.recordContactBlock(a, r, reason, true, now)
			if banned {
				w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(a)))
				w.WriteHeader(http.StatusTooManyRequests)
				data["ContactError"] = "Please try again later."
			} else {
				w.WriteHeader(http.StatusBadRequest)
				data["ContactError"] = "Your message could not be sent."
			}
			s.renderTemplate(w, r, "contact.html", data)
			return
		}
	}
	if step == "submit" {
		if !honeypot {
			if reason := s.checkContactSubmission(a, r, fromEmail, now, false); reason != "" {
				s.recordContactBlock(a, r, reason, false, now)
				w.Header().Set("Retry-After", strconv.Itoa(contactRetryAfter(a)))
				w.WriteHeader(http.StatusTooManyRequests)
				data["ContactError"] = "Please try again later."
				s.renderTemplate(w, r, "contact.html", data)
				return
			}
		}
		flags := ""
		if honeypot {
			flags = "d"
		}
		s.setContactReview(data, fields, flags, now)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	validReview, reviewFlags := s.contactReviewConfirmed(r, fields, now, a)
	if !validReview {
		banned := s.recordContactBlock(a, r, "review", true, now)
		if banned {
			w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(a)))
			w.WriteHeader(http.StatusTooManyRequests)
			data["ContactError"] = "Please try again later."
		} else {
			w.WriteHeader(http.StatusBadRequest)
			data["ContactError"] = "Please read the details and confirm."
		}
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	if reviewFlags == "d" {
		data["ContactSuccess"] = pc.SuccessMessage
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	if s.contactDuplicate(a, fromEmail, subject, message, now) {
		banned := s.recordContactBlock(a, r, "duplicate", true, now)
		if banned {
			w.Header().Set("Retry-After", strconv.Itoa(contactBanRetryAfter(a)))
			w.WriteHeader(http.StatusTooManyRequests)
			data["ContactError"] = "Please try again later."
		} else {
			data["ContactSuccess"] = pc.SuccessMessage
		}
		s.renderTemplate(w, r, "contact.html", data)
		return
	}
	if reason := s.allowContactSubmission(a, r, fromEmail, now); reason != "" {
		s.recordContactBlock(a, r, reason, false, now)
		w.Header().Set("Retry-After", strconv.Itoa(contactRetryAfter(a)))
		w.WriteHeader(http.StatusTooManyRequests)
		data["ContactError"] = "Please try again later."
		s.renderTemplate(w, r, "contact.html", data)
		return
	}

	if status, msg := s.deliverReportContact(r, cfg, fields); status != http.StatusOK {
		data["ContactError"] = msg
		w.WriteHeader(status)
		s.renderTemplate(w, r, "contact.html", data)
		return
	}

	data["ContactSuccess"] = pc.SuccessMessage
	// Clear the preserved fields on success so the form resets.
	delete(data, "FormName")
	delete(data, "FormEmail")
	delete(data, "FormSubject")
	delete(data, "FormMessage")
	s.renderTemplate(w, r, "contact.html", data)
}

func (s *Server) deliverReportContact(r *http.Request, cfg *config.Config, fields map[string]string) (int, string) {
	typeName := fields["type"]
	role := "abuse"
	to := strings.TrimSpace(cfg.AbuseEmail())
	if typeName == "privacy" {
		role = "dpo"
		to = strings.TrimSpace(cfg.DPOEmail())
	}
	name := strings.TrimSpace(fields["name"])
	from := strings.TrimSpace(fields["email"])
	subject := strings.TrimSpace(fields["subject"])
	body := strings.Join([]string{
		"Name: " + name,
		"Type: " + typeName,
		"Subject: " + subject,
		"Message: " + strings.TrimSpace(fields["message"]),
		"Reported URL: " + strings.TrimSpace(fields["reported_url"]),
		"Reason: " + strings.TrimSpace(fields["reason"]),
		"Copyrighted work: " + strings.TrimSpace(fields["copyrighted_work"]),
		"Good faith: " + fields["good_faith"],
		"Accuracy: " + fields["accuracy"],
		"Signature: " + strings.TrimSpace(fields["signature"]),
		"Privacy request: " + strings.TrimSpace(fields["request_kind"]),
	}, "\n\n")
	m := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, s.baseURL(r), cfg.ResolveFQDN(), cfg.Server.Lang)
	emailReady := to != "" && m.Enabled()
	webhookTargets := cfg.WebhookTargets(role)
	if !emailReady && len(webhookTargets) == 0 {
		return http.StatusServiceUnavailable, "The report form is currently unavailable. Please try again later."
	}
	s.notifyRole(cfg, role, role+".report_submitted", "["+cfg.Web.SiteTitle+" contact] "+typeName+": "+subject, body, "info")
	if emailReady {
		if err := m.SendRawMessageWithReplyTo(to, "["+cfg.Web.SiteTitle+" contact] "+typeName+": "+subject, body, from); err != nil {
			log.Printf("contact: send failed: %v", err)
			return http.StatusBadGateway, "Failed to send your report. Please try again later."
		}
	}
	return http.StatusOK, ""
}

func (s *Server) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "privacy.html", s.privacyPageData(r))
		if err != nil {
			_, _ = fmt.Fprintf(w, "Privacy Policy — %s/server/privacy\n", s.baseURL(r))
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "privacy.html", s.privacyPageData(r))
}

// handleDPO serves the public data-protection contact page.
func (s *Server) handleDPO(w http.ResponseWriter, r *http.Request) {
	data := s.pageData()
	cfg := s.liveCfg()
	data["DPOName"] = strings.TrimSpace(cfg.Server.Contact.DPO.Name)
	data["DPOEmail"] = cfg.DPOEmailPublic()
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "dpo.html", data)
		if err != nil {
			_, _ = fmt.Fprintf(w, "Data Protection Officer — %s/server/dpo\n", s.baseURL(r))
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "dpo.html", data)
}

// privacyPageData builds the template data for the privacy page from live config
// (PART 31). Templates access the privacy tree via .Privacy.*, the analytics
// platform via .Tracking.*, the auto-merged third-party list via
// .ThirdPartyServices, and the CCPA opt-out cookie state via .CCPAOptedOut.
func (s *Server) privacyPageData(r *http.Request) map[string]interface{} {
	cfg := s.liveCfg()
	privacy := cfg.Server.Privacy
	privacy.Content = config.DefaultConfig().Server.Privacy.Content
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
	data["DPOName"] = strings.TrimSpace(cfg.Server.Contact.DPO.Name)
	data["DPOEmail"] = cfg.DPOEmailPublic()
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
	if s.liveCfg().Server.CSRF.Secure == "true" {
		secure = true
	} else if s.liveCfg().Server.CSRF.Secure == "false" {
		secure = false
	}
	if s.i2pRequestB32(r) != "" {
		secure = false
	}
	cookie := &http.Cookie{
		Name:     "ccpa_opt_out",
		Path:     "/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	if strings.TrimSpace(r.PostFormValue("choice")) == "opt-in" {
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

// checkTrackingAllowed reports whether analytics may be injected for this
// request: the cookie_consent cookie grants analytics AND tracking is configured.
func (s *Server) checkTrackingAllowed(r *http.Request) bool {
	// Sec-GPC: 1 is an opt-out of behavioral analytics regardless of any
	// stored consent (PART 11 "Privacy Signal Headers").
	if privacyOptOut(r) {
		return false
	}
	st, ok := priorConsentState(r)
	return ok && st.Analytics && s.liveCfg().Server.Tracking.Enabled()
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
	value, err := url.PathUnescape(c.Value)
	if err != nil {
		return consentState{}, false
	}
	if err := json.Unmarshal([]byte(value), &prior); err != nil {
		return consentState{}, false
	}
	return prior, true
}

// auditConsentUpdate records the current cookie-consent categories and time
// without creating a server-side preference profile.
func (s *Server) auditConsentUpdate(r *http.Request, state consentState) {
	s.auditLog(r, audit.Entry{Event: "compliance.consent_updated", Category: "security", Severity: audit.SeverityInfo, Result: audit.ResultSuccess, Details: map[string]any{
		"essential":   state.Essential,
		"preferences": state.Preferences,
		"analytics":   state.Analytics,
		"timestamp":   state.Timestamp,
	}})
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
	if hadPrior && cfg.Web.Headers.ClearSiteData.OnConsentWithdrawal && ((prior.Preferences && !state.Preferences) || (prior.Analytics && !state.Analytics)) {
		w.Header().Set("Clear-Site-Data", clearSiteDataValue(cfg.Web.Headers.ClearSiteData))
	}
	b, err := json.Marshal(state)
	if err != nil {
		b = []byte(`{"essential":true}`)
	}
	secure := r.TLS != nil
	switch cfg.Server.CSRF.Secure {
	case "true":
		secure = true
	case "false":
		secure = false
	}
	if s.i2pRequestB32(r) != "" {
		secure = false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "cookie_consent",
		Value:    url.PathEscape(string(b)),
		Path:     "/",
		MaxAge:   365 * 24 * 60 * 60,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Consent changes are always audited with a timestamp (AI.md PART 11).
	s.auditConsentUpdate(r, state)
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
	switch cfg.Server.CSRF.Secure {
	case "true":
		secure = true
	case "false":
		secure = false
	}
	if s.i2pRequestB32(r) != "" {
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
// and any `cairnbin_pref_*` fields present on the form, then redirects back
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
		r.PostForm = url.Values{}
	}
	if r.PostFormValue("action") == "reset" {
		s.resetPreferences(w, r)
		w.Header().Set("Clear-Site-Data", `"storage"`)
		prefSeeOther(w, s.prefRedirectDest(r, r.PostFormValue("redirect"), "/"))
		return
	}

	// Only fields the form actually carried are considered, so an app-pref
	// form never resets the visitor's theme as a side effect. Invalid values
	// are dropped; a save always answers 303, never a 4xx page (rule 9).
	merged := url.Values{}
	for name, values := range r.PostForm {
		if name != "code" && name != "redirect" && name != "csrf_token" && name != "consent_form" && name != "consent_preferences" && name != "consent_analytics" && name != "ccpa_opt_out" && name != "action" {
			merged[name] = values
		}
	}
	valid, _, _ := validateGuestPrefs(merged)
	resetLang := r.PostForm.Has("lang") && r.PostFormValue("lang") == "auto"
	if resetLang {
		delete(valid, "lang")
	}
	var applied []string
	var blocked []string
	prefsReq := r
	if r.PostFormValue("consent_form") == "1" {
		state := consentState{
			Essential: true, Preferences: r.PostForm.Has("consent_preferences"),
			Analytics: s.liveCfg().Server.Tracking.Enabled() && r.PostForm.Has("consent_analytics"),
			Timestamp: time.Now().Unix(),
		}
		encoded, _ := json.Marshal(state)
		http.SetCookie(w, &http.Cookie{Name: "cookie_consent", Value: url.PathEscape(string(encoded)), Path: "/", MaxAge: prefCookieMaxAge, Secure: s.cookieSecure(r), SameSite: http.SameSiteLaxMode})
		if s.liveCfg().Server.Privacy.Data.Sold {
			ccpa := "false"
			if r.PostForm.Has("ccpa_opt_out") {
				ccpa = "true"
			}
			http.SetCookie(w, &http.Cookie{Name: "ccpa_opt_out", Value: ccpa, Path: "/", MaxAge: prefCookieMaxAge, Secure: s.cookieSecure(r), SameSite: http.SameSiteLaxMode})
		}
		s.auditConsentUpdate(r, state)
		prefsReq = r.Clone(r.Context())
		cookiePairs := make([]string, 0, len(r.Cookies())+1)
		for _, cookie := range r.Cookies() {
			if cookie.Name != "cookie_consent" {
				cookiePairs = append(cookiePairs, cookie.Name+"="+cookie.Value)
			}
		}
		cookiePairs = append(cookiePairs, "cookie_consent="+url.PathEscape(string(encoded)))
		prefsReq.Header.Set("Cookie", strings.Join(cookiePairs, "; "))
	}
	if resetLang {
		if preferenceCookiesAllowed(prefsReq) {
			http.SetCookie(w, &http.Cookie{Name: i18n.CookieName(), Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: s.cookieSecure(r), SameSite: http.SameSiteLaxMode})
		} else {
			blocked = append(blocked, "lang")
		}
	}
	applied, blockedPrefs := s.applyGuestPrefs(w, prefsReq, valid)
	blocked = append(blocked, blockedPrefs...)
	if len(applied) > 0 && refererIsPreferencesPage(r) {
		setFlash(w, r, "preferences_saved", s.cookieSecure(r))
	}
	dest := s.prefRedirectDest(r, r.PostFormValue("redirect"), "/")
	dest = preferencesConsentRedirect(dest, len(applied) == 0 && len(blocked) > 0 && preferenceConsentMissing(prefsReq))
	prefSeeOther(w, dest)
}

func (s *Server) resetPreferences(w http.ResponseWriter, r *http.Request) {
	seen := map[string]bool{"theme": true, i18n.CookieName(): true, "cookie_consent": true, "ccpa_opt_out": true, "cairnbin_build": true, "dismissed_announcements": true, flashCookieName: true}
	for _, cookie := range r.Cookies() {
		if strings.HasPrefix(cookie.Name, prefCookiePrefix) {
			seen[cookie.Name] = true
		}
	}
	for name := range seen {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: s.cookieSecure(r), SameSite: http.SameSiteLaxMode})
	}
	s.auditConsentUpdate(r, consentState{Essential: true, Timestamp: time.Now().Unix()})
}

func (s *Server) handleTerms(w http.ResponseWriter, r *http.Request) {
	data := s.pageData()
	if detectClientType(r) == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		html, err := s.renderTemplateToString(r, "terms.html", data)
		if err != nil {
			_, _ = fmt.Fprintf(w, "Terms of Service — %s/server/terms\n", s.baseURL(r))
			return
		}
		_, _ = fmt.Fprint(w, httputil.HTML2TextConverter(html, 80))
		return
	}
	s.renderTemplate(w, r, "terms.html", data)
}

// ─── Misc handlers ────────────────────────────────────────────────────────────

// themeColorFor returns the browser-chrome color for a theme mode from the
// canonical palette; "auto" resolves to the dark palette (the default theme).
func themeColorFor(mode string) string {
	if mode == "light" {
		return theme.ThemePaletteLight.Background
	}
	return theme.ThemePaletteDark.Background
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	// PWA manifest is revalidated on every load (PART 16) so a new build's icons
	// and metadata are picked up promptly; the version-derived ETag lets the
	// browser short-circuit with 304 when nothing changed.
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", `"`+buildinfo.AssetStamp()+`"`)
	basePath := s.assetPrefix(r)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"name":             s.liveCfg().Web.SiteTitle,
		"short_name":       "Paste",
		"description":      "A fast, public cairnbin service",
		"start_url":        basePath + "/?source=pwa",
		"scope":            basePath + "/",
		"display":          "standalone",
		"orientation":      "any",
		"categories":       []string{"utilities"},
		"background_color": theme.ThemePaletteDark.Background,
		"theme_color":      theme.ThemePaletteDark.Primary,
		"icons": []map[string]interface{}{
			{"src": basePath + "/static/icons/icon-72.png", "sizes": "72x72", "type": "image/png"},
			{"src": basePath + "/static/icons/icon-96.png", "sizes": "96x96", "type": "image/png"},
			{"src": basePath + "/static/icons/icon-128.png", "sizes": "128x128", "type": "image/png"},
			{"src": basePath + "/static/icons/icon-144.png", "sizes": "144x144", "type": "image/png"},
			{"src": basePath + "/static/icons/icon-152.png", "sizes": "152x152", "type": "image/png"},
			{"src": basePath + "/static/icons/icon-192.png", "sizes": "192x192", "type": "image/png"},
			{"src": basePath + "/static/icons/icon-384.png", "sizes": "384x384", "type": "image/png"},
			{"src": basePath + "/static/icons/icon-512.png", "sizes": "512x512", "type": "image/png"},
			{"src": basePath + "/static/icons/icon-maskable-192.png", "sizes": "192x192", "type": "image/png", "purpose": "maskable"},
			{"src": basePath + "/static/icons/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
		"shortcuts": []map[string]interface{}{
			{
				"name": "Preferences",
				"url":  basePath + "/server/preferences?source=shortcut",
				"icons": []map[string]string{{
					"src":   basePath + "/static/icons/shortcut-preferences.png",
					"sizes": "96x96",
				}},
			},
		},
	})
}

func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	stamp := buildinfo.AssetStamp()
	basePath := s.assetPrefix(r)
	// The full build stamp changes for every build, including builds in the
	// same release channel, so controlled browsers install the matching worker.
	w.Header().Set("ETag", `"`+stamp+`"`)
	sw := fmt.Sprintf(`// Cairnbin Service Worker — build %s
const BASE_PATH = %q;
const CACHE_VERSION = %q;
const CACHE_PREFIX = BASE_PATH
  ? 'cairnbin-cache-' + encodeURIComponent(BASE_PATH) + '-'
  : 'cairnbin-cache-root-';
const CACHE_NAME = CACHE_PREFIX + CACHE_VERSION;
function appPath(path) { return BASE_PATH + path; }
function belongsToAppCache(key) {
  if (BASE_PATH) return key.startsWith(CACHE_PREFIX);
  return key.startsWith(CACHE_PREFIX)
    || (key.startsWith('cairnbin-cache-') && !key.startsWith('cairnbin-cache-%%2F'));
}
const PRECACHE_ASSETS = [
  appPath('/static/css/common.css?v=' + CACHE_VERSION),
  appPath('/static/css/components.css?v=' + CACHE_VERSION),
  appPath('/static/css/public.css?v=' + CACHE_VERSION),
  appPath('/static/js/app.js?v=' + CACHE_VERSION),
  appPath('/static/icons/icon-192.png?v=' + CACHE_VERSION),
  appPath('/static/icons/icon-512.png?v=' + CACHE_VERSION),
  appPath('/offline.html'),
];

// INSTALL — pre-cache safe responses. An updated worker waits for the user;
// a first install has no active worker to wait behind and activates by itself.
self.addEventListener('install', event => {
  event.waitUntil(
    withTimeout(caches.open(CACHE_NAME), CACHE_OPERATION_TIMEOUT_MS)
      .then(async cache => {
        await Promise.all(PRECACHE_ASSETS.map(async asset => {
          try {
            const response = await withTimeout(fetch(asset), NAV_TIMEOUT_MS);
            if (response.ok && !response.redirected) {
              await withTimeout(cache.put(asset, response), CACHE_OPERATION_TIMEOUT_MS);
            }
          } catch (_) {
            // A failed precache is recoverable; navigation has its own fallback.
          }
        }));
      })
      .catch(() => {})
  );
});

// ACTIVATE — purge stale caches and claim all clients
self.addEventListener('activate', event => {
  event.waitUntil(
    withTimeout(caches.keys(), CACHE_OPERATION_TIMEOUT_MS)
      .catch(() => [])
      .then(keys => Promise.all(
        keys
          .filter(k => belongsToAppCache(k) && k !== CACHE_NAME)
          .map(k => withTimeout(caches.delete(k), CACHE_OPERATION_TIMEOUT_MS).catch(() => false))
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

// A navigation never waits on the network forever: its bounded wait rejects
// after NAV_TIMEOUT_MS so the cache/offline fallbacks can render.
const NAV_TIMEOUT_MS = 8000;
const CACHE_OPERATION_TIMEOUT_MS = 1000;
function withTimeout(promise, ms) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('timeout')), ms);
    promise.then(
      value => { clearTimeout(timer); resolve(value); },
      error => { clearTimeout(timer); reject(error); }
    );
  });
}

async function cachedResponse(request) {
  try {
    const cache = await withTimeout(caches.open(CACHE_NAME), CACHE_OPERATION_TIMEOUT_MS);
    const response = await withTimeout(cache.match(request), CACHE_OPERATION_TIMEOUT_MS);
    if (response && response.ok && !response.redirected) return response;
  } catch (_) {
    // Cache failures fall through to network or the synthesized fallback.
  }
  return null;
}

async function cacheResponse(request, response) {
  if (!response.ok || response.redirected) return;
  try {
    const cache = await withTimeout(caches.open(CACHE_NAME), CACHE_OPERATION_TIMEOUT_MS);
    await withTimeout(cache.put(request, response.clone()), CACHE_OPERATION_TIMEOUT_MS);
  } catch (_) {
    // Cache failures must never prevent the network response from reaching the page.
  }
}

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
  if (url.pathname.startsWith(appPath('/api/')) || url.pathname.startsWith(appPath('/graphql'))) return;

  // Navigations (page loads): network-first, then cache, then a GUARANTEED
  // synthesized offline page — a transient network failure renders a real error
  // page, never net::ERR_FAILED
  if (request.mode === 'navigate') {
    event.respondWith(
      withTimeout(fetch(request), NAV_TIMEOUT_MS)
        .then(response => {
          if (response.redirected) throw new Error('redirected navigation');
          event.waitUntil(cacheResponse(request, response));
          return response;
        })
        .catch(async () =>
          (await cachedResponse(request))
            || (await cachedResponse(appPath('/offline.html')))
            || offlineFallbackResponse()
        )
    );
    return;
  }

  // Static assets: cache-first, then network, then a GUARANTEED 504 — a failed
  // subresource must never reject respondWith
  if (url.pathname.startsWith(appPath('/static/'))) {
    event.respondWith(
      cachedResponse(request)
        .then(cached => cached || withTimeout(fetch(request), NAV_TIMEOUT_MS).then(response => {
          event.waitUntil(cacheResponse(request, response));
          return response;
        }))
        .catch(() => new Response('', { status: 504, statusText: 'Gateway Timeout' }))
    );
    return;
  }

  // Everything else: network-first, then cache, then a GUARANTEED 504
  event.respondWith(
    withTimeout(fetch(request), NAV_TIMEOUT_MS)
      .catch(async () =>
        (await cachedResponse(request))
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
`, stamp, basePath, stamp)
	_, _ = w.Write([]byte(sw))
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
	// Denied AI crawlers each get their own stanza, since a bot's own
	// User-agent block overrides the wildcard block for that bot only.
	for _, bot := range s.liveCfg().Web.Robots.DeniedAIBots() {
		b.WriteString("\nUser-agent: " + bot + "\nDisallow: /\n")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
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
		_, _ = fmt.Fprintf(&b, "Contact: %s\n", cfg.SecurityReportURL())
		if id := s.currentSecurityID(); id != "" {
			_, _ = fmt.Fprintf(&b, "Contact: http://%s/server/contact?security_id=%s\n", onion, id)
		}
		if email := strings.TrimSpace(cfg.Server.Tor.ContactEmail); email != "" {
			_, _ = fmt.Fprintf(&b, "Contact: mailto:%s\n", email)
		}
		_, _ = fmt.Fprintf(&b, "Policy: http://%s/server/security\n", onion)
		_, _ = fmt.Fprintf(&b, "Expires: %s\n", expires)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(b.String()))
		return
	}
	var b strings.Builder
	// Contact lines in RFC 9116 preference order (PART 11 → security.txt):
	// (1) GitHub private vulnerability reporting, (2) instance security-report
	// form with rotating security_id, (3) mailto CC address last.
	_, _ = fmt.Fprintf(&b, "Contact: %s\n", cfg.SecurityReportURL())
	// Rotating security_id contact (PART 11 → Coordinated Disclosure Pipeline).
	// This is the only place the id is ever emitted; it switches the contact
	// form into security-report mode when a researcher follows it.
	if id := s.currentSecurityID(); id != "" {
		_, _ = fmt.Fprintf(&b, "Contact: %s/server/contact?security_id=%s\n", strings.TrimRight(s.baseURL(r), "/"), id)
	}
	if email := cfg.SecurityEmailPublic(); email != "" {
		_, _ = fmt.Fprintf(&b, "Contact: mailto:%s\n", email)
	}
	_, _ = fmt.Fprintf(&b, "Expires: %s\n", expires)
	// Encryption line seeded from the project PGP key (PART 11 → GPG Keypair
	// Management). Emitted only when a key exists and publishing is enabled.
	if s.hasPGPKey() {
		_, _ = fmt.Fprintf(&b, "Encryption: %s/.well-known/pgp-key.asc\n", strings.TrimRight(s.baseURL(r), "/"))
	}
	_, _ = fmt.Fprintf(&b, "Preferred-Languages: %s\n", cfg.SecurityPreferredLanguages())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
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
			writeAPIError(w, r, http.StatusInternalServerError, "SERVER_ERROR", "errors.server_error")
			return
		}
		data = fallback
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(data)
}

// handleOffline serves the PWA offline fallback page.
// The service worker caches this page and serves it when the user is offline
// and no cached version of the requested page is available.
func (s *Server) handleOffline(w http.ResponseWriter, r *http.Request) {
	d := s.pageData()
	s.renderTemplate(w, r, "offline.html", d)
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
	data["PreferencesConsentRequired"] = r.URL.Query().Get("consent_required") == "1"
	// Inject language for i18n — templates access it as .Lang
	lang := i18n.LangFromRequest(r)
	data["Lang"] = lang
	data["LangName"] = nativeLanguageName(lang)
	// Inject text direction for RTL languages (Arabic) — templates access it as .Dir
	data["Dir"] = i18n.Direction(lang)
	data["AvailableLanguages"] = i18n.Languages()
	// Inject the theme resolved from the server-readable `theme` cookie so every
	// <html> renders class="theme-{{.Theme}}" with no init JS and no FOUC (AI.md 23474, 24552)
	theme := s.themeFromRequest(r)
	data["Theme"] = theme
	data["ThemeColor"] = themeColorFor(theme)
	data["APIBase"] = "/api/" + s.apiVersion()
	toastConfig := s.liveCfg().Server.Notifications.WebUI
	data["ToastPosition"] = toastConfig.Position
	data["ToastDuration"] = toastConfig.Duration
	data["ToastWarningDuration"] = toastConfig.WarningDuration
	titleSeparator := s.liveCfg().Server.SEO.TitleSeparator
	if len([]rune(titleSeparator)) > 8 {
		titleSeparator = " - "
	}
	if titleSeparator == "" {
		titleSeparator = " - "
	}
	data["TitleSeparator"] = titleSeparator
	if s.liveCfg().Server.SEO.NoIndex {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	// Inject the next mode in the cycle so the no-JS toggle form can advance it (AI.md 21588)
	data["NextTheme"] = nextTheme(theme)
	// Inject whether the visitor already has a recorded cookie_consent choice so
	// the base layout's no-JS consent form only renders while a decision is still
	// outstanding, matching the JS banner's own show/hide behavior (AI.md 25598).
	data["HasConsented"] = hasConsentCookie(r)
	// Tracking markup is only emitted when the visitor consented to analytics
	// and server.tracking is configured (AI.md CheckTrackingAllowed).
	data["TrackingAllowed"] = s.checkTrackingAllowed(r)
	// Inject CSRF token for forms — templates access it as .CSRFToken
	data["CSRFToken"] = s.csrfTokenFromRequest(r)
	// Inject build metadata for the footer — templates access them as .Version and .BuildDate
	data["Version"] = s.version
	data["BuildDate"] = s.buildDate
	data["CommitID"] = s.commitID
	data["RepoURL"] = s.liveCfg().RepositoryURL()
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
	data["CSPNonce"] = cspNonce(r)
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
	s.injectFooterData(r, data)
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
	// Inject the one-shot flash message set by a no-JS form POST redirect
	// (PART 16 Toast Notifications, "No-JS fallback"); consumed exactly once.
	data["Flash"] = takeFlash(w, r, lang, s.cookieSecure(r))
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
	statusCode := http.StatusOK
	if len(status) > 0 {
		statusCode = status[0]
	}
	setHTMLCacheHeaders(w)
	s.versionPurge(w, r, statusCode)
	// Onion-Location (PART 12): advertise the hidden service on clearnet 2xx
	// HTML document responses only — never on the onion's own responses.
	if statusCode >= 200 && statusCode < 300 && r.Method == http.MethodGet && !s.isOverlayRequest(r) {
		if onion := strings.TrimSpace(s.liveCfg().Server.Tor.OnionAddress); onion != "" {
			w.Header().Set("Onion-Location", "http://"+onion+r.URL.RequestURI())
		}
	}
	w.WriteHeader(statusCode)
	_, _ = buf.WriteTo(w)
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
	data["PreferencesConsentRequired"] = r.URL.Query().Get("consent_required") == "1"
	lang := i18n.LangFromRequest(r)
	data["Lang"] = lang
	data["LangName"] = nativeLanguageName(lang)
	data["Dir"] = i18n.Direction(lang)
	data["AvailableLanguages"] = i18n.Languages()
	// Inject the theme resolved from the server-readable `theme` cookie (AI.md 23474)
	theme := s.themeFromRequest(r)
	data["Theme"] = theme
	data["ThemeColor"] = themeColorFor(theme)
	data["NextTheme"] = nextTheme(theme)
	// Inject build metadata for the footer — templates access them as .Version and .BuildDate
	data["Version"] = s.version
	data["BuildDate"] = s.buildDate
	data["CommitID"] = s.commitID
	data["RepoURL"] = s.liveCfg().RepositoryURL()
	if _, ok := data["BaseURL"]; !ok {
		data["BaseURL"] = s.baseURL(r)
	}
	if _, ok := data["AssetPrefix"]; !ok {
		data["AssetPrefix"] = s.assetPrefix(r)
	}
	data["CSPNonce"] = cspNonce(r)
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
	s.injectFooterData(r, data)
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
func (s *Server) injectFooterData(r *http.Request, data map[string]interface{}) {
	s.injectOperatorHTML(r, data)
}

// Project identity used by the footer variables; fixed regardless of binary rename.
const (
	footerProjectName = "cairnbin"
	footerProjectOrg  = "apimgr"
)

// expandFooterVars substitutes the AI.md "Available Footer Variables" in
// already-sanitized footer HTML. Values are HTML-escaped; the onion and I2P
// addresses are empty unless the service is enabled, running and published.
func (s *Server) expandFooterVars(html string) string {
	if html == "" || !strings.Contains(html, "{") {
		return html
	}
	onion, i2p := "", ""
	if s.torManager != nil && s.TorRunning() {
		onion = s.TorOnionAddress()
	}
	if s.i2pManager != nil && s.I2PRunning() {
		i2p = s.I2PAddress()
	}
	return strings.NewReplacer(
		"{current_year}", strconv.Itoa(time.Now().Year()),
		"{project_name}", footerProjectName,
		"{project_org}", footerProjectOrg,
		"{project_version}", template.HTMLEscapeString(s.version),
		"{build_datetime}", template.HTMLEscapeString(s.buildDate),
		"{onion_address}", template.HTMLEscapeString(onion),
		"{i2p_address}", template.HTMLEscapeString(i2p),
	).Replace(html)
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
	data["I2PEnabled"] = s.i2pManager != nil && s.i2pManager.Enabled()
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
	for _, c := range s.ownerTokenCookies(r) {
		tokenHash := sha256.Sum256([]byte(c.Value))
		resourceID, err := s.db.FindResourceIDByToken(tokenHash, "paste")
		if err == nil && resourceID != "" {
			data["HasOwnerToken"] = true
			data["OwnedResourceURL"] = s.assetPrefix(r) + "/" + resourceID
			data["ResourceLabel"] = "paste"
			return
		}
	}
	// The dropdown's visible link text uses the full pre-translated
	// nav.manage_my_paste sentence (grammar/word-order differs too much
	// across all supported languages to safely concatenate "Manage my"
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
				// The error path itself must never fail the request: if logging
				// or the themed page panics, fall back to the hardcoded response.
				defer func() {
					if again := recover(); again != nil {
						log.Printf("panic in error path: %v", again)
						renderFallbackError(w, r, http.StatusInternalServerError)
					}
				}()
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
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
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
		// The themed template is unavailable. Preserve browser/API content
		// negotiation with the hardcoded last-resort response.
		renderFallbackError(w, r, status)
		return
	}
	data["CurrentPath"] = r.URL.Path
	lang := i18n.LangFromRequest(r)
	data["Lang"] = lang
	data["LangName"] = nativeLanguageName(lang)
	data["Dir"] = i18n.Direction(lang)
	data["AvailableLanguages"] = i18n.Languages()
	theme := s.themeFromRequest(r)
	data["Theme"] = theme
	data["ThemeColor"] = themeColorFor(theme)
	data["NextTheme"] = nextTheme(theme)
	data["CSRFToken"] = s.csrfTokenFromRequest(r)
	data["Version"] = s.version
	data["BuildDate"] = s.buildDate
	data["CommitID"] = s.commitID
	data["RepoURL"] = s.liveCfg().RepositoryURL()
	if _, ok := data["BaseURL"]; !ok {
		data["BaseURL"] = s.baseURL(r)
	}
	if _, ok := data["AssetPrefix"]; !ok {
		data["AssetPrefix"] = s.assetPrefix(r)
	}
	data["CSPNonce"] = cspNonce(r)
	if _, ok := data["SEOMeta"]; !ok {
		data["SEOMeta"] = s.seoMetaTags(r)
	}
	s.injectFooterData(r, data)
	s.injectTorData(data)
	s.injectI2PData(data)
	s.injectOwnerTokenData(r, data)
	// Render to a buffer first so a template failure can fall back to the
	// hardcoded response instead of leaving a truncated page behind.
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("error template render failed: %v", err)
		renderFallbackError(w, r, status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// renderFallbackError is the last-resort error response used when the themed
// error template fails to render or the error path itself panics. It depends
// on nothing but plain strings: JSON envelope for API/non-HTML clients, a
// minimal HTML body for browsers.
func renderFallbackError(w http.ResponseWriter, r *http.Request, status int) {
	text := http.StatusText(status)
	if detectClientType(r) != "html" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "{\"ok\":false,\"error\":%q,\"message\":%q}\n", errorCodeForStatus(status), text)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!DOCTYPE html>\n<html lang=\"en\">\n  <head>\n    <meta charset=\"utf-8\">\n    <title>%d %s</title>\n  </head>\n  <body>\n    <h1>%d %s</h1>\n  </body>\n</html>\n", status, text, status, text)
}

// captureOnionAddress retains the Tor manager's runtime hostname in memory for
// request detection. The address is never persisted to server.yml.
func (s *Server) captureOnionAddress() {
	onion := strings.TrimSpace(s.TorOnionAddress())
	if onion == "" || strings.TrimSpace(s.cfg.Server.Tor.OnionAddress) != "" {
		return
	}
	s.cfg.Server.Tor.OnionAddress = onion
	if live := s.liveCfg(); live != s.cfg {
		live.Server.Tor.OnionAddress = onion
	}
}

// persistB32Address writes the .b32.i2p address to server.i2p.b32_address on
// the first successful I2P bootstrap (PART 31.2). The stored address enables I2P
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

// torRequestOnion returns the runtime onion hostname when the request
// arrived via the Tor hidden service — the request Host (after port
// stripping) matches the hostname read from the Tor HiddenServiceDir (PART 12).
// Returns "" for clearnet requests or when no runtime hostname is available.
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

// isOverlayRequest reports whether the request arrived through the Tor onion
// service or the I2P eepsite, where TLS semantics (HSTS, upgrade-insecure-
// requests, clearnet reporting URLs) must never apply (PART 12).
func (s *Server) isOverlayRequest(r *http.Request) bool {
	return s.torRequestOnion(r) != "" || s.i2pRequestB32(r) != ""
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
	return s.liveCfg().SecurityEmailPublic()
}

// baseURL constructs the base URL for the current request (PART 12).
// Resolution order (highest priority first):
//  0. Tor: when Host matches the runtime onion hostname → http://{onion_address} (no port, always http)
//  1. Config/CLI: server.base_url
//  2. X-Forwarded-Prefix header (from trusted reverse proxy)
//  3. X-Forwarded-Path header (alternative prefix header)
//  4. X-Script-Name header (WSGI-style)
//  5. Default: scheme://host derived from TLS state and X-Forwarded-Proto
//
// X-Forwarded-* headers are only honored when the immediate peer is a trusted
// proxy (loopback, private ranges, or server.trusted_proxies.additional).
func (s *Server) baseURL(r *http.Request) string {
	// Priority 0: Tor hidden service — when the request Host matches the runtime
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
	if !hostFromProxy && (!isRoutableHost(hostname) || !hostAllowedByDomain(hostname, os.Getenv("DOMAIN"))) {
		if f := strings.TrimSpace(s.cfg.ResolveFQDN()); f != "" && !strings.EqualFold(f, "localhost") {
			hostname, port = splitHostPort(f)
		} else {
			return ""
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

	return base + s.assetPrefix(r)
}

func hostAllowedByDomain(host, configured string) bool {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return true
	}
	var domains []string
	for _, domain := range strings.Split(configured, ",") {
		if domain = strings.ToLower(strings.TrimSpace(domain)); domain != "" {
			domains = append(domains, domain)
		}
	}
	if len(domains) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	for _, domain := range domains {
		if host == domain {
			return true
		}
	}
	return strings.HasSuffix(host, "."+domains[0])
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

// loadBlocklists returns the current in-memory blocklists, loading them once
// only when startup has not populated the cache yet.
func (s *Server) loadBlocklists(dir string) *blocklistStore {
	s.blocklistCacheMu.RLock()
	cached := s.blocklistCache
	s.blocklistCacheMu.RUnlock()
	if cached != nil {
		return cached
	}
	loaded := s.buildBlocklists(dir)
	s.blocklistCacheMu.Lock()
	if s.blocklistCache == nil {
		s.blocklistCache = loaded
	}
	cached = s.blocklistCache
	s.blocklistCacheMu.Unlock()
	return cached
}

// ReloadBlocklists atomically replaces the in-memory lists after startup,
// configuration changes, or a scheduled data refresh.
func (s *Server) ReloadBlocklists() {
	s.reloadBlocklistsWithConfig(s.liveCfg())
}

func (s *Server) reloadBlocklistsWithConfig(cfg *config.Config) {
	dir := s.blocklistDir(cfg)
	loaded := s.buildBlocklists(dir)
	fingerprint := blocklistDirectoryFingerprint(dir)
	s.blocklistCacheMu.Lock()
	s.blocklistCache = loaded
	s.blocklistFingerprint = fingerprint
	s.blocklistCacheMu.Unlock()
	s.datasetChecks.Refresh(time.Now())
}

func (s *Server) RefreshDatasetChecks() {
	dir := s.blocklistDir(s.liveCfg())
	fingerprint := blocklistDirectoryFingerprint(dir)
	s.blocklistCacheMu.RLock()
	changed := fingerprint != s.blocklistFingerprint
	s.blocklistCacheMu.RUnlock()
	if changed {
		s.ReloadBlocklists()
		return
	}
	s.datasetChecks.Refresh(time.Now())
}

func (s *Server) blocklistDir(cfg *config.Config) string {
	dataDir := strings.TrimSpace(cfg.Server.DataDir)
	if dataDir == "" {
		dataDir = s.dataDir
	}
	return filepath.Join(dataDir, "security", "blocklists")
}

func blocklistDirectoryFingerprint(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var fingerprint strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(&fingerprint, "%s:%d:%d;", entry.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return fingerprint.String()
}

func (s *Server) registerBlocklistHealthChecks(cfg *config.Config) {
	ids := []string{"firehol_level1"}
	if cfg.Server.Security.Blocklists.Enabled {
		for _, source := range cfg.Server.Security.Blocklists.ExtraSources {
			if source.ValidID() && source.IsEnabled() && (source.Type == "ip" || source.Type == "domain") {
				ids = append(ids, source.ID)
			}
		}
	}
	for _, id := range ids {
		sourceID := id
		_ = s.datasetChecks.Register("blocklist_"+sourceID, false, 72*time.Hour, func() (time.Time, bool) {
			s.blocklistCacheMu.RLock()
			defer s.blocklistCacheMu.RUnlock()
			if s.blocklistCache == nil {
				return time.Time{}, false
			}
			state := s.blocklistCache.sources[sourceID]
			return state.loadedAt, state.loaded
		})
	}
	if cfg.Server.GeoIP.Enabled {
		geoipIDs := []struct {
			id      string
			enabled bool
		}{{"asn", cfg.Server.GeoIP.Databases.ASN}, {"country", cfg.Server.GeoIP.Databases.Country}, {"city", cfg.Server.GeoIP.Databases.City}}
		for _, dataset := range geoipIDs {
			if !dataset.enabled {
				continue
			}
			id := dataset.id
			_ = s.datasetChecks.Register("geoip_"+id, false, 7*24*time.Hour, func() (time.Time, bool) {
				if s.geoipDB == nil {
					return time.Time{}, false
				}
				for _, status := range s.geoipDB.DatasetStatuses() {
					if status.ID == id {
						return status.UpdatedAt, status.Loaded
					}
				}
				return time.Time{}, false
			})
		}
	}
}

func (s *Server) registerSecurityDataHealthChecks(cfg *config.Config) {
	dataDir := cfg.Server.DataDir
	if strings.TrimSpace(dataDir) == "" {
		dataDir = s.dataDir
	}
	path := filepath.Join(dataDir, "security", "cve", "nvd.json")
	_ = s.datasetChecks.Register("data_nvd", false, 48*time.Hour, func() (time.Time, bool) {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() == 0 {
			return time.Time{}, false
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return time.Time{}, false
		}
		var records []json.RawMessage
		if json.Unmarshal(content, &records) != nil || len(records) == 0 {
			return time.Time{}, false
		}
		for _, record := range records {
			var cve struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(record, &cve) != nil || strings.TrimSpace(cve.ID) == "" {
				return time.Time{}, false
			}
		}
		return info.ModTime(), true
	})
	if trivy := cfg.Data.Trivy; trivy.Enabled {
		path := filepath.Join(dataDir, "security", "trivy", "db.tar.gz")
		_ = s.datasetChecks.Register("data_trivy", false, 48*time.Hour, func() (time.Time, bool) {
			info, err := os.Stat(path)
			if err != nil || info.IsDir() || info.Size() == 0 {
				return time.Time{}, false
			}
			file, err := os.Open(path)
			if err != nil {
				return time.Time{}, false
			}
			defer func() { _ = file.Close() }()
			gz, err := gzip.NewReader(file)
			if err != nil {
				return time.Time{}, false
			}
			defer func() { _ = gz.Close() }()
			tarReader := tar.NewReader(gz)
			foundDB, foundMetadata := false, false
			for {
				header, err := tarReader.Next()
				if err != nil {
					break
				}
				switch filepath.Base(header.Name) {
				case "trivy.db":
					foundDB = header.Size > 0
				case "metadata.json":
					foundMetadata = header.Size > 0
				}
			}
			return info.ModTime(), foundDB && foundMetadata
		})
	}
}

func (s *Server) registerTLSHealthCheck(cfg *config.Config) {
	if !cfg.Server.TLS.Enabled {
		return
	}
	_ = s.datasetChecks.Register("certificates_tls", false, 0, func() (time.Time, bool) {
		s.tlsHealthMu.RLock()
		defer s.tlsHealthMu.RUnlock()
		return s.tlsLoadedAt, s.tlsHealthy
	})
}

func (s *Server) setTLSHealth(healthy bool) {
	s.tlsHealthMu.Lock()
	s.tlsHealthy = healthy
	if healthy {
		s.tlsLoadedAt = time.Now().UTC()
	} else {
		s.tlsLoadedAt = time.Time{}
	}
	s.tlsHealthMu.Unlock()
	s.datasetChecks.Refresh(time.Now())
}

// buildBlocklists removes entries that overlap the networks this server always
// trusts before publishing the set used by request middleware.
func (s *Server) buildBlocklists(dir string) *blocklistStore {
	cfg := s.liveCfg()
	trusted := append([]string(nil), cfg.Server.TrustedProxies.Additional...)
	s.resolvedProxyMu.RLock()
	for _, ips := range s.resolvedProxyIPs {
		for _, ip := range ips {
			trusted = append(trusted, ip.String())
		}
	}
	s.resolvedProxyMu.RUnlock()
	if listenIP := net.ParseIP(strings.TrimSpace(cfg.Server.Address)); listenIP != nil {
		if ip4 := listenIP.To4(); ip4 != nil && !ip4.Equal(net.IPv4zero) {
			mask := net.CIDRMask(24, 32)
			trusted = append(trusted, (&net.IPNet{IP: ip4.Mask(mask), Mask: mask}).String())
		}
	}
	sourceTypes := make(map[string]string, len(cfg.Server.Security.Blocklists.ExtraSources)+1)
	sourceTypes["firehol_level1"] = "ip"
	if cfg.Server.Security.Blocklists.Enabled {
		for _, source := range cfg.Server.Security.Blocklists.ExtraSources {
			if source.ValidID() && source.IsEnabled() && (source.Type == "ip" || source.Type == "domain") {
				if _, duplicate := sourceTypes[source.ID]; duplicate {
					continue
				}
				sourceTypes[source.ID] = source.Type
			}
		}
	}
	return loadBlocklistsWithTypes(dir, trusted, sourceTypes)
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

// applyI18nConfig pushes server.i18n into the shared i18n package so language
// resolution, the selector and the language cookie follow the live config.
func applyI18nConfig(cfg *config.Config) {
	c := cfg.Server.I18n
	i18n.Configure(i18n.Settings{
		Enabled:            c.Enabled,
		DefaultLanguage:    c.DefaultLanguage,
		FallbackLanguage:   c.FallbackLanguage,
		AvailableLanguages: c.AvailableLanguages,
		CookieName:         c.CookieName,
		CookieMaxAge:       c.I18nCookieSeconds(),
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
		if lang := r.URL.Query().Get("lang"); lang != "" && i18n.IsAvailable(lang) {
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
