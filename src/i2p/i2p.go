// Package i2p manages an optional I2P eepsite for the cairnbin server.
// Unlike Tor, I2P is opt-in: no port is allocated and no session is
// established unless Config.Enabled is explicitly true (PART 31.2).
//
// Two provider models are supported, resolved in priority order at Start():
//
//  1. Model A — a local i2pd binary is found: the server writes
//     {config_dir}/i2p/tunnels.conf (regenerated every startup, mirroring
//     the torrc pattern) and manages the i2pd process directly.
//  2. Model B — no i2pd binary, but a SAMv3 bridge answers at
//     Config.SAMAddress (default 127.0.0.1:7656): the server speaks the raw
//     SAMv3 text protocol over net.Conn to create a STREAM session and
//     forward it to the local backend listener.
//
// If neither provider is available the eepsite is disabled with a
// non-fatal WARN log — I2P is always best-effort, never blocks startup.
//
// Neither provider prepends a PROXY-protocol header, so the backend
// listener here is a plain loopback net.Listener (unlike tor.go's
// proxyproto-wrapped listener).
package i2p

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// backendPortRangeLo/Hi bound the random-unused-port scan used for the
// dedicated I2P backend listener — the same 64000-64999 range the server
// uses for its own clearnet port and the Tor backend listener.
const (
	backendPortRangeLo = 64000
	backendPortRangeHi = 64999
)

// listenBackendLoopback binds the dedicated plain backend listener on a
// random unused loopback port in the 64000-64999 range, using the same
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

// commonI2PdPaths lists well-known i2pd binary locations per OS.
var commonI2PdPaths = map[string][]string{
	"linux":   {"/usr/bin/i2pd", "/usr/sbin/i2pd", "/usr/local/bin/i2pd", "/bin/i2pd"},
	"darwin":  {"/usr/local/bin/i2pd", "/opt/homebrew/bin/i2pd"},
	"windows": {`C:\Program Files\i2pd\i2pd.exe`, `C:\Program Files (x86)\i2pd\i2pd.exe`},
	"freebsd": {"/usr/local/bin/i2pd"},
	"openbsd": {"/usr/local/bin/i2pd"},
	"netbsd":  {"/usr/local/bin/i2pd"},
}

// FindBinary locates the i2pd binary. Returns empty string if not found.
// Checks (in order, per PART 31.2): configured path, common OS locations,
// PATH lookup.
func FindBinary(configuredPath string) string {
	if configuredPath != "" {
		if _, err := os.Stat(configuredPath); err == nil {
			return configuredPath
		}
		return ""
	}
	for _, p := range commonI2PdPaths[runtime.GOOS] {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := findInPath("i2pd"); err == nil {
		return p
	}
	return ""
}

func findInPath(name string) (string, error) {
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

// Provider identifies which I2P backend a running Manager is using.
type Provider int

const (
	// ProviderNone means I2P is disabled or unavailable.
	ProviderNone Provider = iota
	// ProviderI2Pd means a local i2pd binary is being managed directly.
	ProviderI2Pd
	// ProviderSAM means a SAMv3 bridge is being used (no local binary).
	ProviderSAM
)

// Config holds I2P-related configuration; mirrors config.I2PConfig.
type Config struct {
	Enabled          bool
	Binary           string
	SAMAddress       string
	VirtualPort      int
	InboundLength    int
	OutboundLength   int
	InboundQuantity  int
	OutboundQuantity int
	SignatureType    int
	BootstrapTimeout time.Duration

	// Directory paths resolved at startup.
	// {config_dir} — tunnels.conf written here (Model A only)
	ConfigDir string
	// {data_dir} — I2P destination keys
	DataDir string
	// {log_dir} — i2pd.log (Model A only)
	LogDir string
}

// service holds a running I2P instance.
type service struct {
	provider      Provider
	address       string // full {base32}.b32.i2p address
	cmd           *osProcess
	samConn       net.Conn
	backendLn     net.Listener
	backendServer *http.Server
}

// Manager owns the I2P eepsite lifecycle.
type Manager struct {
	mu      sync.Mutex
	svc     *service
	cfg     Config
	handler http.Handler
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewManager returns a Manager. Call Start() to launch the eepsite.
// handler is the server's own HTTP router — the eepsite backend listener
// serves it directly, identical to how the Tor hidden service backend works.
func NewManager(ctx context.Context, cfg Config, handler http.Handler) *Manager {
	child, cancel := context.WithCancel(ctx)
	return &Manager{
		cfg:     cfg,
		handler: handler,
		ctx:     child,
		cancel:  cancel,
	}
}

// Start resolves an I2P provider and launches the eepsite. Returns nil
// (non-fatal) if I2P is disabled, or if neither provider is available.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.svc != nil || !m.cfg.Enabled {
		return nil
	}
	return m.startLocked()
}

// UpdateConfig stops the current provider and starts it again with the new
// settings. Disabled configs remain inert and allocate no backend resources.
func (m *Manager) UpdateConfig(cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeSvcLocked()
	m.cfg = cfg
	if !cfg.Enabled {
		return nil
	}
	return m.startLocked()
}

// Enabled reports whether the operator opted into I2P, independent of whether
// a provider is currently available.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Enabled
}

func (m *Manager) startLocked() error {
	if !m.cfg.Enabled {
		return nil
	}
	if m.handler == nil {
		log.Printf("I2P: no HTTP handler configured, eepsite disabled")
		return nil
	}

	bin := FindBinary(m.cfg.Binary)
	if bin != "" {
		return m.startI2PdLocked(bin)
	}

	if probeSAM(m.cfg.SAMAddress) {
		return m.startSAMLocked()
	}

	log.Printf("I2P: no i2pd binary found and no SAM bridge at %s, eepsite disabled", m.cfg.SAMAddress)
	return nil
}

// probeSAM performs a short-timeout TCP dial to see whether a SAM bridge is
// listening. Best-effort only — a real HELLO handshake happens in
// startSAMLocked once a provider is chosen.
func probeSAM(addr string) bool {
	if addr == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// startI2PdLocked launches and manages a local i2pd process (Model A).
func (m *Manager) startI2PdLocked(bin string) error {
	if err := ensureI2PDirs(m.cfg.ConfigDir, m.cfg.DataDir); err != nil {
		return fmt.Errorf("i2p dirs: %w", err)
	}

	rawLn, err := listenBackendLoopback()
	if err != nil {
		return fmt.Errorf("i2p backend listen: %w", err)
	}
	backendPort := rawLn.Addr().(*net.TCPAddr).Port
	backendServer := &http.Server{Handler: m.handler}
	go func() {
		if serveErr := backendServer.Serve(rawLn); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("I2P: backend server error: %v", serveErr)
		}
	}()

	tunnelsPath := filepath.Join(m.cfg.ConfigDir, "i2p", "tunnels.conf")
	siteDir := filepath.Join(m.cfg.DataDir, "i2p", "site")
	// tunnels.conf is fully-derived state and is regenerated on every startup.
	if writeErr := writeSecureI2PFile(tunnelsPath, []byte(getTunnelsConfig(&m.cfg, siteDir, backendPort))); writeErr != nil {
		_ = backendServer.Close()
		return fmt.Errorf("write tunnels.conf: %w", writeErr)
	}

	log.Printf("Starting I2P eepsite (i2pd)...")
	proc, err := startI2Pd(bin, m.cfg.DataDir, m.cfg.LogDir, tunnelsPath)
	if err != nil {
		_ = backendServer.Close()
		return fmt.Errorf("start i2pd: %w", err)
	}

	addr, err := waitForKeysAddress(m.ctx, siteDir, m.cfg.BootstrapTimeout)
	if err != nil {
		_ = proc.Close()
		_ = backendServer.Close()
		return fmt.Errorf("i2p destination: %w", err)
	}

	m.svc = &service{
		provider:      ProviderI2Pd,
		address:       addr,
		cmd:           proc,
		backendLn:     rawLn,
		backendServer: backendServer,
	}
	log.Printf("I2P: %s:%d -> 127.0.0.1:%d", addr, m.cfg.VirtualPort, backendPort)
	return nil
}

// startSAMLocked establishes a SAMv3 STREAM session forwarding to the
// backend listener (Model B) — no local i2pd binary required.
func (m *Manager) startSAMLocked() error {
	if err := ensureI2PDirs(m.cfg.ConfigDir, m.cfg.DataDir); err != nil {
		return fmt.Errorf("i2p dirs: %w", err)
	}

	rawLn, err := listenBackendLoopback()
	if err != nil {
		return fmt.Errorf("i2p backend listen: %w", err)
	}
	backendPort := rawLn.Addr().(*net.TCPAddr).Port
	backendServer := &http.Server{Handler: m.handler}
	go func() {
		if serveErr := backendServer.Serve(rawLn); serveErr != nil && serveErr != http.ErrServerClosed {
			log.Printf("I2P: backend server error: %v", serveErr)
		}
	}()

	log.Printf("Starting I2P eepsite (SAM bridge %s)...", m.cfg.SAMAddress)
	bootstrapTimeout := m.cfg.BootstrapTimeout
	if bootstrapTimeout <= 0 {
		bootstrapTimeout = 5 * time.Minute
	}
	conn, err := net.DialTimeout("tcp", m.cfg.SAMAddress, bootstrapTimeout)
	if err != nil {
		_ = backendServer.Close()
		return fmt.Errorf("sam dial: %w", err)
	}
	if err := conn.SetDeadline(time.Now().Add(bootstrapTimeout)); err != nil {
		_ = conn.Close()
		_ = backendServer.Close()
		return fmt.Errorf("sam bootstrap deadline: %w", err)
	}

	destPath := filepath.Join(m.cfg.DataDir, "i2p", "site", "site-keys.dat")
	addr, err := samCreateSession(conn, destPath, m.cfg, backendPort)
	if err != nil {
		_ = conn.Close()
		_ = backendServer.Close()
		return fmt.Errorf("sam session: %w", err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		_ = backendServer.Close()
		return fmt.Errorf("sam clear bootstrap deadline: %w", err)
	}

	m.svc = &service{
		provider:      ProviderSAM,
		address:       addr,
		samConn:       conn,
		backendLn:     rawLn,
		backendServer: backendServer,
	}
	log.Printf("I2P: %s:%d -> 127.0.0.1:%d (SAM)", addr, m.cfg.VirtualPort, backendPort)
	return nil
}

// Close shuts down the I2P eepsite and its backend listener.
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
	if m.svc.cmd != nil {
		_ = m.svc.cmd.Close()
	}
	if m.svc.samConn != nil {
		_ = m.svc.samConn.Close()
	}
	m.svc = nil
}

// Restart stops the eepsite and starts it again, reusing the persisted
// destination key so the .b32.i2p address is unchanged. Used by the
// i2p_health scheduled task (PART 18) when the provider is found down.
func (m *Manager) Restart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeSvcLocked()
	return m.startLocked()
}

// Running returns true when the eepsite is active.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.svc != nil
}

// Address returns the full {base32}.b32.i2p address, or empty string if
// not running.
func (m *Manager) Address() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.svc != nil {
		return m.svc.address
	}
	return ""
}

// String returns the provider name used in health responses: "i2pd", "sam",
// or "none".
func (p Provider) String() string {
	switch p {
	case ProviderI2Pd:
		return "i2pd"
	case ProviderSAM:
		return "sam"
	default:
		return "none"
	}
}

// Provider returns which backend the running Manager is using.
func (m *Manager) Provider() Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.svc != nil {
		return m.svc.provider
	}
	return ProviderNone
}

// Monitor watches the running eepsite and restarts it if it becomes
// unresponsive. Runs in its own goroutine; exits when ctx is cancelled.
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
			if svc.provider == ProviderI2Pd && svc.cmd != nil && !svc.cmd.Alive() {
				log.Printf("I2P: i2pd process exited, restarting")
				m.mu.Lock()
				if m.svc == svc {
					m.closeSvcLocked()
					if err := m.startLocked(); err != nil {
						log.Printf("I2P: restart failed: %v", err)
					}
				}
				m.mu.Unlock()
			}
		}
	}
}

// ensureI2PDirs creates all required I2P directories with 0700 permissions
// and enforces ownership to match the process UID/GID, even if the
// directories already existed (mirrors ensureTorDirs). Chown failures are
// fatal on non-Windows; Windows has no chown support and relies instead on
// inherited ACLs from the user profile.
func ensureI2PDirs(configDir, dataDir string) error {
	dirs := []string{
		filepath.Join(configDir, "i2p"),
		filepath.Join(dataDir, "i2p"),
		filepath.Join(dataDir, "i2p", "site"),
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

// writeSecureI2PFile writes content to path with 0600 permissions, enforcing
// both the mode and ownership even if the file already existed (mirrors
// writeSecureTorFile). Chown failures are fatal on non-Windows; Windows has
// no chown support and relies instead on inherited ACLs.
func writeSecureI2PFile(path string, content []byte) error {
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

// getTunnelsConfig generates i2pd tunnels.conf content for a single server
// eepsite tunnel forwarding to the given backend port.
func getTunnelsConfig(cfg *Config, siteDir string, backendPort int) string {
	return fmt.Sprintf(`# i2pd tunnels configuration — managed by cairnbin server binary
# Regenerated on every startup; this file is fully-derived state.

[site]
type = server
host = 127.0.0.1
port = %d
keys = %s
inbound.length = %d
outbound.length = %d
inbound.quantity = %d
outbound.quantity = %d
signaturetype = %d
`, backendPort, filepath.Join(siteDir, "site-keys.dat"),
		cfg.InboundLength, cfg.OutboundLength,
		cfg.InboundQuantity, cfg.OutboundQuantity, cfg.SignatureType)
}

// b32Address derives an I2P .b32.i2p address from a raw destination blob:
// lowercase, unpadded base32 of the SHA-256 digest of the destination,
// suffixed with ".b32.i2p".
func b32Address(destination []byte) string {
	sum := sha256.Sum256(destination)
	enc := base32.StdEncoding.WithPadding(base32.NoPadding)
	return strings.ToLower(enc.EncodeToString(sum[:])) + ".b32.i2p"
}

// waitForKeysAddress polls for i2pd's destination key file and, once it
// appears, derives the .b32.i2p address from it. i2pd writes the key file
// once the tunnel is published; there is no separate hostname file (unlike
// Tor), so the address is always derived rather than read verbatim.
func waitForKeysAddress(ctx context.Context, siteDir string, timeout time.Duration) (string, error) {
	keyPath := filepath.Join(siteDir, "site-keys.dat")
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(keyPath); err == nil && len(data) > 0 {
			return b32Address(data), nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for i2p destination key file")
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("i2p destination wait cancelled: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// samCreateSession speaks the SAMv3 text protocol over conn: HELLO, then
// either loads a persisted destination from destPath or requests a
// transient one and persists it, then creates a STREAM session forwarding
// to 127.0.0.1:backendPort. Returns the derived .b32.i2p address.
func samCreateSession(conn net.Conn, destPath string, cfg Config, backendPort int) (string, error) {
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))

	if err := samSend(rw, "HELLO VERSION MIN=3.0 MAX=3.3\n"); err != nil {
		return "", err
	}
	line, err := samReadLine(rw)
	if err != nil {
		return "", err
	}
	if !strings.Contains(line, "RESULT=OK") {
		return "", fmt.Errorf("sam hello failed: %s", line)
	}

	dest := "TRANSIENT"
	if existing, readErr := os.ReadFile(destPath); readErr == nil && len(existing) > 0 {
		dest = strings.TrimSpace(string(existing))
	}

	sessionID := "site"
	cmd := fmt.Sprintf(
		"SESSION CREATE STYLE=STREAM ID=%s DESTINATION=%s SIGNATURE_TYPE=%d inbound.length=%d outbound.length=%d inbound.quantity=%d outbound.quantity=%d\n",
		sessionID, dest, cfg.SignatureType, cfg.InboundLength, cfg.OutboundLength, cfg.InboundQuantity, cfg.OutboundQuantity,
	)
	if err := samSend(rw, cmd); err != nil {
		return "", err
	}
	line, err = samReadLine(rw)
	if err != nil {
		return "", err
	}
	if !strings.Contains(line, "RESULT=OK") {
		return "", fmt.Errorf("sam session create failed: %s", line)
	}

	destValue := samField(line, "DESTINATION")
	if destValue == "" {
		return "", fmt.Errorf("sam session create: no DESTINATION in reply: %s", line)
	}
	if dest == "TRANSIENT" {
		if err := writeSecureI2PFile(destPath, []byte(destValue)); err != nil {
			return "", fmt.Errorf("persist sam destination: %w", err)
		}
	}

	if err := samSend(rw, fmt.Sprintf("STREAM FORWARD ID=%s PORT=%d HOST=127.0.0.1\n", sessionID, backendPort)); err != nil {
		return "", err
	}
	line, err = samReadLine(rw)
	if err != nil {
		return "", err
	}
	if !strings.Contains(line, "RESULT=OK") {
		return "", fmt.Errorf("sam stream forward failed: %s", line)
	}

	return b32Address([]byte(destValue)), nil
}

func samSend(rw *bufio.ReadWriter, s string) error {
	if _, err := rw.WriteString(s); err != nil {
		return fmt.Errorf("sam write: %w", err)
	}
	return rw.Flush()
}

func samReadLine(rw *bufio.ReadWriter) (string, error) {
	line, err := rw.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("sam read: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// samField extracts KEY=VALUE from a SAM reply line, tolerating VALUE
// containing further "=" characters (destination blobs do).
func samField(line, key string) string {
	prefix := key + "="
	idx := strings.Index(line, prefix)
	if idx == -1 {
		return ""
	}
	rest := line[idx+len(prefix):]
	if sp := strings.IndexByte(rest, ' '); sp != -1 {
		return rest[:sp]
	}
	return rest
}
