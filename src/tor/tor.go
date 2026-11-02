// Package tor manages a dedicated Tor hidden service for the pastebin server.
// The server binary fully owns the Tor process lifecycle — it starts, monitors,
// and stops the process. The hidden service is auto-enabled whenever a Tor
// binary is found in PATH or common install locations; there is no enable flag.
//
// Uses github.com/cretz/bine (CGO_ENABLED=0 compatible) to launch and control
// a dedicated Tor process. The v3 hidden service itself is declared in torrc
// via HiddenServiceDir/HiddenServicePort — Tor generates and persists the
// ed25519 identity key and .onion hostname under HiddenServiceDir itself.
// Tor is configured to export the circuit ID as a HAProxy PROXY-protocol v1
// header on the hidden-service backend connection (HiddenServiceExportCircuitID
// haproxy); the backend listener parses it via github.com/pires/go-proxyproto.
package tor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/cretz/bine/control"
	bineprocess "github.com/cretz/bine/process"
	binettor "github.com/cretz/bine/tor"
	"github.com/pires/go-proxyproto"
)

// backendPortRangeLo/Hi bound the random-unused-port scan used for the
// dedicated Tor backend listener — the same 64000-64999 range the server
// uses for its own clearnet port.
const (
	backendPortRangeLo = 64000
	backendPortRangeHi = 64999
)

// listenBackendLoopback binds the dedicated PROXY-protocol backend listener
// on a random unused loopback port in the 64000-64999 range, using the same
// random-unused-port detection the server uses for its own port.
func listenBackendLoopback() (net.Listener, error) {
	span := backendPortRangeHi - backendPortRangeLo + 1
	for _, offset := range rand.Perm(span) {
		port := backendPortRangeLo + offset
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			return ln, nil
		}
	}
	return nil, fmt.Errorf("no unused port found in %d-%d", backendPortRangeLo, backendPortRangeHi)
}

// pidCapturingCreator wraps bine's default external-process creator so the
// spawned Tor child's PID can be captured and written to {data_dir}/tor/tor.pid,
// since bine's process.Process interface exposes no PID accessor itself.
type pidCapturingCreator struct {
	exePath string
	onStart func(pid int)
}

func (c *pidCapturingCreator) New(ctx context.Context, args ...string) (bineprocess.Process, error) {
	cmd := exec.CommandContext(ctx, c.exePath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return &pidCapturingProcess{Cmd: cmd, onStart: c.onStart}, nil
}

type pidCapturingProcess struct {
	*exec.Cmd
	onStart func(pid int)
}

func (p *pidCapturingProcess) Start() error {
	if err := p.Cmd.Start(); err != nil {
		return err
	}
	if p.onStart != nil && p.Cmd.Process != nil {
		p.onStart(p.Cmd.Process.Pid)
	}
	return nil
}

func (p *pidCapturingProcess) EmbeddedControlConn() (net.Conn, error) {
	return nil, bineprocess.ErrControlConnUnsupported
}

// writeTorPIDFile persists the running Tor child's PID to
// {data_dir}/tor/tor.pid with 0600 permissions, enforcing mode and ownership.
func writeTorPIDFile(dataDir string, pid int) error {
	pidPath := filepath.Join(dataDir, "tor", "tor.pid")
	return writeSecureTorFile(pidPath, []byte(fmt.Sprintf("%d\n", pid)))
}

// removeTorPIDFile removes the Tor PID file on shutdown, ignoring a
// not-exist error.
func removeTorPIDFile(dataDir string) {
	pidPath := filepath.Join(dataDir, "tor", "tor.pid")
	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		log.Printf("Tor: warning: failed to remove pid file: %v", err)
	}
}

// nativeKeyHeader is the fixed 32-byte header Tor prepends to a native
// on-disk ed25519 hidden-service secret-key file.
var nativeKeyHeader = []byte("== ed25519v1-secret: type0 ==\x00\x00\x00")

const (
	// nativeKeyFileLen is the total size of a native-format secret-key file:
	// 32-byte header + 64-byte expanded ed25519 private key.
	nativeKeyFileLen = 32 + 64
	// legacyKeyBlobLen is the size of the raw 64-byte expanded ed25519 private
	// key as previously persisted (no header) via the bine ADD_ONION blob.
	legacyKeyBlobLen = 64
)

// commonTorPaths lists well-known Tor binary locations per OS.
var commonTorPaths = map[string][]string{
	"linux":   {"/usr/bin/tor", "/usr/sbin/tor", "/usr/local/bin/tor", "/bin/tor"},
	"darwin":  {"/usr/local/bin/tor", "/opt/homebrew/bin/tor"},
	"windows": {`C:\Program Files\Tor\tor.exe`, `C:\Program Files (x86)\Tor\tor.exe`},
	"freebsd": {"/usr/local/bin/tor"},
	"openbsd": {"/usr/local/bin/tor"},
	"netbsd":  {"/usr/local/bin/tor"},
}

// FindBinary locates the Tor binary. Returns empty string if not found.
// Checks (in order, per PART 31.1): configured path, common OS locations,
// PATH lookup.
func FindBinary(configuredPath string) string {
	if configuredPath != "" {
		if _, err := os.Stat(configuredPath); err == nil {
			return configuredPath
		}
		return ""
	}
	// Common locations.
	for _, p := range commonTorPaths[runtime.GOOS] {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// PATH lookup.
	if p, err := findInPath("tor"); err == nil {
		return p
	}
	return ""
}

func findInPath(name string) (string, error) {
	// Cross-platform PATH search without exec.LookPath to keep CGO_ENABLED=0.
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		return "", fmt.Errorf("PATH empty")
	}
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
		if !strings.HasSuffix(name, ".exe") {
			name += ".exe"
		}
	}
	for _, dir := range strings.Split(pathEnv, sep) {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found in PATH")
}

// Config holds Tor-related configuration; mirrors config.TorConfig.
type Config struct {
	Binary                    string
	UseNetwork                bool
	MaxCircuits               int
	CircuitTimeout            int
	BootstrapTimeout          int
	SafeLogging               bool
	MaxStreamsPerCircuit      int
	CloseCircuitOnStreamLimit bool
	BandwidthRate             string
	BandwidthBurst            string
	MaxMonthlyBandwidth       string
	NumIntroPoints            int
	VirtualPort               int

	// Directory paths resolved at startup.
	// {config_dir} — torrc written here
	ConfigDir string
	// {data_dir} — Tor data + hidden service keys
	DataDir string
	// {log_dir} — tor.log is written here (empty disables the Tor log file)
	LogDir string
}

// service holds a running Tor instance.
type service struct {
	t             managedTor
	onionAddress  string
	serverPort    int
	dialer        *binettor.Dialer
	backendLn     net.Listener
	backendServer *http.Server
}

type managedTor interface {
	Close() error
	EnableNetwork(context.Context, bool) error
	Dialer(context.Context, *binettor.DialConf) (*binettor.Dialer, error)
	GetInfo(...string) ([]*control.KeyVal, error)
}

type bineTor struct{ tor *binettor.Tor }

func (t bineTor) Close() error { return t.tor.Close() }
func (t bineTor) EnableNetwork(ctx context.Context, wait bool) error {
	return t.tor.EnableNetwork(ctx, wait)
}
func (t bineTor) Dialer(ctx context.Context, conf *binettor.DialConf) (*binettor.Dialer, error) {
	return t.tor.Dialer(ctx, conf)
}
func (t bineTor) GetInfo(keys ...string) ([]*control.KeyVal, error) {
	return t.tor.Control.GetInfo(keys...)
}

var startTorRuntime = func(ctx context.Context, conf *binettor.StartConf) (managedTor, error) {
	t, err := binettor.Start(ctx, conf)
	if err != nil {
		return nil, err
	}
	return bineTor{tor: t}, nil
}

// Manager owns the Tor process lifecycle.
type Manager struct {
	mu         sync.Mutex
	svc        *service
	cfg        Config
	serverPort int
	handler    http.Handler
	ctx        context.Context
	cancel     context.CancelFunc
	// vanity holds the state of the single allowed in-process vanity
	// onion-address search (AI.md PART 31.1).
	vanity vanitySearcher
}

// NewManager returns a Manager. Call Start() to launch Tor. handler is the
// server's own HTTP router — the hidden-service backend listener serves it
// directly so every existing Tor-detection/privacy middleware applies
// identically regardless of which listener a request arrived on.
func NewManager(ctx context.Context, serverPort int, cfg Config, handler http.Handler) *Manager {
	child, cancel := context.WithCancel(ctx)
	return &Manager{
		cfg:        cfg,
		serverPort: serverPort,
		handler:    handler,
		ctx:        child,
		cancel:     cancel,
	}
}

// Start finds the Tor binary, ensures directories, writes torrc, and starts
// the dedicated Tor process.  Returns nil if Tor is not installed (non-fatal).
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.startLocked()
}

func (m *Manager) startLocked() error {
	bin := FindBinary(m.cfg.Binary)
	if bin == "" {
		log.Printf("Tor binary not found, hidden service disabled")
		return nil
	}

	// The hidden service forwards to a dedicated backend listener that serves
	// the same router as the clearnet server; without a handler there is
	// nothing to forward to.
	if m.handler == nil {
		log.Printf("Tor: no HTTP handler configured, hidden service disabled")
		return nil
	}

	if err := ensureTorDirsWithLog(m.cfg.ConfigDir, m.cfg.DataDir, m.cfg.LogDir); err != nil {
		return fmt.Errorf("tor dirs: %w", err)
	}

	siteDir := filepath.Join(m.cfg.DataDir, "tor", "site")
	if err := migrateLegacyKey(siteDir); err != nil {
		log.Printf("Tor: warning: key migration: %v", err)
	}

	// Dedicated loopback backend listener — Tor forwards hidden-service
	// connections here (never the clearnet port). Tor prepends a HAProxy
	// PROXY-protocol v1 header carrying the circuit ID (HiddenServiceExportCircuitID
	// haproxy), so the listener is wrapped to parse it.
	rawLn, err := listenBackendLoopback()
	if err != nil {
		return fmt.Errorf("tor backend listen: %w", err)
	}
	backendPort := rawLn.Addr().(*net.TCPAddr).Port
	backendLn := &proxyproto.Listener{Listener: rawLn}
	backendServer := &http.Server{Handler: m.handler}
	go func() {
		if serveErr := backendServer.Serve(backendLn); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("Tor: backend server error: %v", serveErr)
		}
	}()

	torrcPath := filepath.Join(m.cfg.ConfigDir, "tor", "torrc")
	torDataDir := filepath.Join(m.cfg.DataDir, "tor")
	// torrc is fully-derived state and is regenerated on every startup.
	if writeErr := writeSecureTorFile(torrcPath, []byte(getTorConfig(&m.cfg, backendPort))); writeErr != nil {
		_ = backendServer.Close()
		return fmt.Errorf("write torrc: %w", writeErr)
	}

	conf := &binettor.StartConf{
		TorrcFile:       torrcPath,
		DataDir:         torDataDir,
		NoAutoSocksPort: true,
		ProcessCreator: &pidCapturingCreator{
			exePath: bin,
			onStart: func(pid int) {
				if err := writeTorPIDFile(m.cfg.DataDir, pid); err != nil {
					log.Printf("Tor: warning: failed to write pid file: %v", err)
				}
			},
		},
	}

	log.Printf("Starting Tor hidden service...")
	t, err := startTorRuntime(m.ctx, conf)
	if err != nil {
		removeTorPIDFile(m.cfg.DataDir)
		_ = backendServer.Close()
		return fmt.Errorf("start tor: %w", err)
	}

	bootstrapTimeout := time.Duration(m.cfg.BootstrapTimeout) * time.Second
	dialCtx, cancel := context.WithTimeout(m.ctx, bootstrapTimeout)
	defer cancel()

	// Show "connecting…" message if bootstrap takes >30 s.
	slow := time.AfterFunc(30*time.Second, func() {
		log.Printf("Tor: connecting...")
	})
	if err := t.EnableNetwork(dialCtx, true); err != nil {
		slow.Stop()
		t.Close()
		removeTorPIDFile(m.cfg.DataDir)
		_ = backendServer.Close()
		return fmt.Errorf("tor bootstrap: %w", err)
	}
	slow.Stop()

	// Tor publishes the hidden service and writes the hostname file itself
	// (HiddenServiceDir); wait for it to appear rather than parsing an
	// ADD_ONION response.
	onion, err := waitForHostname(dialCtx, siteDir)
	if err != nil {
		t.Close()
		removeTorPIDFile(m.cfg.DataDir)
		_ = backendServer.Close()
		return fmt.Errorf("tor hostname: %w", err)
	}

	svc := &service{
		t:             t,
		onionAddress:  onion,
		serverPort:    m.serverPort,
		backendLn:     backendLn,
		backendServer: backendServer,
	}

	// Outbound dialer for optional Tor-routed HTTP clients (server-wide setting).
	if m.cfg.UseNetwork {
		if d, err := t.Dialer(m.ctx, nil); err != nil {
			log.Printf("Tor: warning: outbound dialer failed: %v", err)
		} else {
			svc.dialer = d
		}
	}

	m.svc = svc
	log.Printf("Tor: %s:%d → 127.0.0.1:%d", onion, m.cfg.VirtualPort, backendPort)
	return nil
}

// waitForHostname polls the hidden service's hostname file (written by Tor
// itself once the descriptor is published) until it appears or ctx expires.
func waitForHostname(ctx context.Context, siteDir string) (string, error) {
	hostnamePath := filepath.Join(siteDir, "hostname")
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(hostnamePath); err == nil {
			if host := strings.TrimSpace(string(data)); host != "" {
				return host, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for hostname file: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// migrateLegacyKey converts a pre-migration secret-key file (raw 64-byte
// expanded ed25519 key, as previously persisted via the bine ADD_ONION blob
// format with no header) into Tor's native on-disk format (32-byte header +
// the same 64-byte key), preserving the exact private key material so the
// resulting .onion address is identical to the pre-migration address.
//
// Files already in native format (96 bytes, correct header) are left
// untouched. Files of any other size are left untouched and logged — Tor
// will fail loudly on an unrecognized file rather than the app silently
// generating a new identity.
func migrateLegacyKey(siteDir string) error {
	keyPath := filepath.Join(siteDir, "hs_ed25519_secret_key")
	data, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read secret key: %w", err)
	}
	switch len(data) {
	case nativeKeyFileLen:
		if strings.HasPrefix(string(data), string(nativeKeyHeader)) {
			// Already native format; nothing to do.
			return nil
		}
		return fmt.Errorf("secret key file has unexpected header, leaving untouched: %s", keyPath)
	case legacyKeyBlobLen:
		native := append(append([]byte{}, nativeKeyHeader...), data...)
		if err := writeSecureTorFile(keyPath, native); err != nil {
			return fmt.Errorf("write migrated secret key: %w", err)
		}
		log.Printf("Tor: migrated legacy hidden-service key to native format (%s)", keyPath)
		return nil
	default:
		return fmt.Errorf("secret key file has unexpected size %d, leaving untouched: %s", len(data), keyPath)
	}
}

// Close shuts down the Tor process and its backend listener.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel()
	m.closeSvcLocked()
}

func (m *Manager) closeSvcLocked() {
	if m.svc == nil {
		return
	}
	if m.svc.backendServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = m.svc.backendServer.Shutdown(shutdownCtx)
		cancel()
	}
	_ = m.svc.t.Close()
	removeTorPIDFile(m.cfg.DataDir)
	m.svc = nil
}

// Running returns true when Tor is active.
func (m *Manager) Running() bool {
	if !m.mu.TryLock() {
		return false
	}
	defer m.mu.Unlock()
	return m.svc != nil
}

// Enabled reports whether a Tor binary is available for the managed service.
func (m *Manager) Enabled() bool {
	return m != nil && FindBinary(m.cfg.Binary) != ""
}

// OnionAddress returns the full .onion address, or empty string if not running.
func (m *Manager) OnionAddress() string {
	if !m.mu.TryLock() {
		return ""
	}
	defer m.mu.Unlock()
	if m.svc != nil {
		return m.svc.onionAddress
	}
	return ""
}

// GetHTTPClient returns a direct or Tor-routed HTTP client.
func (m *Manager) GetHTTPClient(useTor bool) *http.Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if useTor && m.svc != nil && m.svc.dialer != nil {
		return &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				DialContext: m.svc.dialer.DialContext,
			},
		}
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Restart closes the current Tor process and starts a fresh one, preserving
// the existing torrc and hidden-service keys.
func (m *Manager) Restart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeSvcLocked()
	return m.startLocked()
}

// UpdateConfig applies a new Config to the Manager and restarts the Tor
// process so the change takes effect.
func (m *Manager) UpdateConfig(cfg Config) error {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
	return m.Restart()
}

// RegenerateAddress deletes the existing hidden-service identity (secret key,
// public key, and hostname) so that Start() has Tor generate a fresh native
// .onion address, and returns the new address on success.
func (m *Manager) RegenerateAddress() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	siteDir := filepath.Join(m.cfg.DataDir, "tor", "site")
	// Stop Tor first so it cannot rewrite the key material while it is deleted (PART 31.1).
	m.closeSvcLocked()
	for _, name := range []string{"hs_ed25519_secret_key", "hs_ed25519_public_key", "hostname"} {
		if err := os.Remove(filepath.Join(siteDir, name)); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove %s: %w", name, err)
		}
	}
	if err := m.startLocked(); err != nil {
		return "", err
	}
	if m.svc != nil {
		return m.svc.onionAddress, nil
	}
	return "", nil
}

// ApplyKeys installs new hidden-service key material and restarts Tor so the
// resulting .onion address becomes active, returning it on success. keyData
// must be Tor's native on-disk secret-key format (32-byte
// "== ed25519v1-secret: type0 ==" header + 64-byte expanded ed25519 private
// key) — the same bytes produced by vanity-address tools such as mkp224o.
func (m *Manager) ApplyKeys(keyData []byte) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(keyData) != nativeKeyFileLen || !strings.HasPrefix(string(keyData), string(nativeKeyHeader)) {
		return "", fmt.Errorf("applykeys: expected %d-byte native ed25519 secret key file with valid header", nativeKeyFileLen)
	}
	siteDir := filepath.Join(m.cfg.DataDir, "tor", "site")
	// Stop Tor before replacing its key material (PART 31.1 ApplyKeys).
	m.closeSvcLocked()
	if err := os.MkdirAll(siteDir, 0o700); err != nil {
		return "", fmt.Errorf("applykeys mkdir: %w", err)
	}
	keyPath := filepath.Join(siteDir, "hs_ed25519_secret_key")
	if err := writeSecureTorFile(keyPath, keyData); err != nil {
		return "", fmt.Errorf("applykeys write: %w", err)
	}
	// Remove any stale derived files so Tor regenerates them from the new key.
	for _, name := range []string{"hs_ed25519_public_key", "hostname"} {
		if err := os.Remove(filepath.Join(siteDir, name)); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	if err := m.startLocked(); err != nil {
		return "", err
	}
	if m.svc != nil {
		return m.svc.onionAddress, nil
	}
	return "", nil
}

// Monitor watches the control connection and restarts Tor if it becomes
// unresponsive.  Runs in its own goroutine; exits when ctx is cancelled.
func (m *Manager) Monitor() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			svc := m.svc
			m.mu.Unlock()
			if svc == nil {
				continue
			}
			if _, err := svc.t.GetInfo("version"); err != nil {
				log.Printf("Tor: control connection lost, restarting")
				m.mu.Lock()
				// Another path (Restart/UpdateConfig) may have already replaced
				// or torn down this service while the lock was released; only act
				// if the live service is still the one we sampled, to avoid a
				// double Close() and clobbering a concurrent restart.
				if m.svc == svc {
					m.closeSvcLocked()
					if err := m.startLocked(); err != nil {
						log.Printf("Tor: restart failed: %v", err)
					}
				}
				m.mu.Unlock()
			}
		}
	}
}

// ensureTorDirs creates all required Tor directories with 0700 permissions
// and enforces ownership to match the process UID/GID, even if the
// directories already existed. Chown failures are fatal on non-Windows;
// Windows has no chown and relies on inherited ACLs from the user profile.
func ensureTorDirs(configDir, dataDir string) error {
	return ensureTorDirsWithLog(configDir, dataDir, "")
}

// ensureTorDirsWithLog is ensureTorDirs plus the log directory that holds tor.log.
func ensureTorDirsWithLog(configDir, dataDir, logDir string) error {
	if logDir != "" {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", logDir, err)
		}
	}
	dirs := []string{
		filepath.Join(configDir, "tor"),
		filepath.Join(dataDir, "tor"),
		filepath.Join(dataDir, "tor", "site"),
	}
	uid := os.Getuid()
	gid := os.Getgid()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", d, err)
		}
		if err := os.Chown(d, uid, gid); err != nil {
			if runtime.GOOS != "windows" {
				return fmt.Errorf("chown %s: %w", d, err)
			}
		}
	}
	return nil
}

// writeSecureTorFile writes content to path with 0600 permissions, enforcing
// both the mode and ownership even if the file already existed. Mirrors the
// spec's updateTorrc/ensureTorFile helpers. Chown failures are fatal on
// non-Windows; Windows has no chown and relies on inherited ACLs.
func writeSecureTorFile(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod file: %w", err)
	}
	if err := os.Chown(path, os.Getuid(), os.Getgid()); err != nil {
		if runtime.GOOS != "windows" {
			return fmt.Errorf("chown file: %w", err)
		}
	}
	return nil
}

// getTorConfig generates torrc content from the given Config. The hidden
// service is declared via HiddenServiceDir/HiddenServicePort — Tor itself
// generates and persists the ed25519 identity and .onion hostname under
// HiddenServiceDir. backendPort is the dedicated loopback port the hidden
// service forwards to (never the clearnet server port).
func getTorConfig(cfg *Config, backendPort int) string {
	socksLine := "SocksPort 0"
	if cfg.UseNetwork {
		// Loopback only, runtime-chosen port: never reachable from the hidden-service listener.
		socksLine = "SocksPort 127.0.0.1:auto\nSocksPolicy accept 127.0.0.1"
	}
	// Hidden-service tuning derived from server.tor.* (omitted when unset or out of range).
	logLine := ""
	if cfg.LogDir != "" {
		logLine = fmt.Sprintf("\n# Tor log file ({log_dir}/tor.log)\nLog notice file %s\n", filepath.Join(cfg.LogDir, "tor.log"))
	}
	hsTuning := ""
	if n := cfg.NumIntroPoints; n >= 3 && n <= 10 {
		hsTuning += fmt.Sprintf("HiddenServiceNumIntroductionPoints %d\n", n)
	}
	if n := cfg.MaxStreamsPerCircuit; n > 0 && n <= 65535 {
		hsTuning += fmt.Sprintf("HiddenServiceMaxStreams %d\n", n)
		if cfg.CloseCircuitOnStreamLimit {
			hsTuning += "HiddenServiceMaxStreamsCloseCircuit 1\n"
		}
	}
	safeLog := "1"
	if !cfg.SafeLogging {
		safeLog = "0"
	}
	accounting := ""
	if cfg.MaxMonthlyBandwidth != "" && cfg.MaxMonthlyBandwidth != "unlimited" {
		accounting = fmt.Sprintf("\n# Monthly bandwidth limit\nAccountingStart month 1 00:00\nAccountingMax %s", cfg.MaxMonthlyBandwidth)
	}
	return fmt.Sprintf(`# Tor configuration — managed by pastebin server binary
# Regenerated on every startup; this file is fully-derived state.
# NEVER uses default ports 9050/9051 — runtime auto-ports only

# SOCKS (0 = hidden service only, auto = outbound enabled)
%s

# Control port — localhost only, runtime port selection
ControlPort 127.0.0.1:auto

# Hidden service — Tor generates/persists the v3 identity and .onion
# hostname under HiddenServiceDir; forwards to a dedicated loopback backend.
HiddenServiceDir %s
HiddenServiceVersion 3
HiddenServicePort %d 127.0.0.1:%d
HiddenServiceExportCircuitID haproxy
%sVanguardsLiteEnabled 1
HiddenServiceSingleHopMode 0

# Security
SafeLogging %s

# Circuit settings
MaxCircuitDirtiness 600

# Bandwidth limits
BandwidthRate %s
BandwidthBurst %s
%s

%s
# Not a relay or exit
ExitRelay 0
ExitPolicy reject *:*
ORPort 0
DirPort 0
PublishServerDescriptor 0

# Startup optimization
FetchDirInfoEarly 1
FetchDirInfoExtraEarly 1
DisableDebuggerAttachment 1
`, socksLine, filepath.Join(cfg.DataDir, "tor", "site"), cfg.VirtualPort, backendPort, hsTuning, safeLog, cfg.BandwidthRate, cfg.BandwidthBurst, accounting, logLine)
}
