package tor

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cretz/bine/control"
	binettor "github.com/cretz/bine/tor"
)

// ─── waitForHostname ──────────────────────────────────────────────────────────

// TestWaitForHostname_AlreadyPresent verifies waitForHostname returns
// immediately when the hostname file already has content before the call.
func TestWaitForHostname_AlreadyPresent(t *testing.T) {
	siteDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(siteDir, "hostname"), []byte("abc123.onion\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := waitForHostname(ctx, siteDir)
	if err != nil {
		t.Fatalf("waitForHostname error: %v", err)
	}
	if got != "abc123.onion" {
		t.Errorf("waitForHostname = %q; want %q (trimmed)", got, "abc123.onion")
	}
}

// TestWaitForHostname_TrimsWhitespace verifies surrounding whitespace is
// stripped from the file content.
func TestWaitForHostname_TrimsWhitespace(t *testing.T) {
	siteDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(siteDir, "hostname"), []byte("  xyz789.onion  \n\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got, err := waitForHostname(ctx, siteDir)
	if err != nil {
		t.Fatalf("waitForHostname error: %v", err)
	}
	if got != "xyz789.onion" {
		t.Errorf("waitForHostname = %q; want trimmed %q", got, "xyz789.onion")
	}
}

// TestWaitForHostname_TimesOut verifies waitForHostname returns a wrapped
// context error when the hostname file never appears within the deadline.
func TestWaitForHostname_TimesOut(t *testing.T) {
	siteDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	_, err := waitForHostname(ctx, siteDir)
	if err == nil {
		t.Fatal("expected timeout error when hostname file never appears")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected error to mention timeout, got: %v", err)
	}
}

// TestWaitForHostname_EmptyFileTimesOut verifies an existing-but-empty
// hostname file is treated the same as a missing file (Tor writes it
// atomically once fully populated; a zero-length file must not satisfy the
// wait).
func TestWaitForHostname_EmptyFileTimesOut(t *testing.T) {
	siteDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(siteDir, "hostname"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	_, err := waitForHostname(ctx, siteDir)
	if err == nil {
		t.Fatal("expected timeout error for an empty hostname file")
	}
}

// TestWaitForHostname_AppearsWhilePolling verifies waitForHostname picks up
// the hostname once it is written mid-poll, proving the ticker loop actually
// re-checks the file rather than only checking once at entry.
func TestWaitForHostname_AppearsWhilePolling(t *testing.T) {
	siteDir := t.TempDir()
	hostnamePath := filepath.Join(siteDir, "hostname")

	go func() {
		time.Sleep(350 * time.Millisecond)
		_ = os.WriteFile(hostnamePath, []byte("delayed.onion"), 0o600)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	got, err := waitForHostname(ctx, siteDir)
	if err != nil {
		t.Fatalf("waitForHostname error: %v", err)
	}
	if got != "delayed.onion" {
		t.Errorf("waitForHostname = %q; want %q", got, "delayed.onion")
	}
}

// ─── ApplyKeys success/error paths without a real Tor process ────────────────

// validNativeKey builds a fixture accepted by ApplyKeys, which requires
// exactly nativeKeyFileLen bytes (32-byte nativeKeyHeader + 64-byte key).
func validNativeKey() []byte {
	body := make([]byte, nativeKeyFileLen-len(nativeKeyHeader))
	for i := range body {
		body[i] = byte(200 + i)
	}
	return append(append([]byte{}, nativeKeyHeader...), body...)
}

// TestApplyKeys_WritesKeyFile verifies a well-formed native key is written
// verbatim, stale derived files are removed, and — since no Tor binary is
// configured — the call completes without error and reports an empty
// address rather than attempting to launch Tor.
func TestApplyKeys_WritesKeyFile(t *testing.T) {
	tmp := t.TempDir()
	siteDir := filepath.Join(tmp, "tor", "site")
	if err := os.MkdirAll(siteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Stale derived files that ApplyKeys must remove once new keys are installed.
	for _, name := range []string{"hs_ed25519_public_key", "hostname"} {
		if err := os.WriteFile(filepath.Join(siteDir, name), []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := Config{Binary: "/nonexistent/tor", DataDir: tmp}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())

	key := validNativeKey()
	addr, err := m.ApplyKeys(key)
	if err != nil {
		t.Fatalf("ApplyKeys error: %v", err)
	}
	if addr != "" {
		t.Errorf("ApplyKeys address = %q; want empty (no Tor binary)", addr)
	}

	got, err := os.ReadFile(filepath.Join(siteDir, "hs_ed25519_secret_key"))
	if err != nil {
		t.Fatalf("reading applied key: %v", err)
	}
	if string(got) != string(key) {
		t.Error("applied key file does not match the supplied key data")
	}
	for _, name := range []string{"hs_ed25519_public_key", "hostname"} {
		if _, err := os.Stat(filepath.Join(siteDir, name)); !os.IsNotExist(err) {
			t.Errorf("stale derived file %s was not removed", name)
		}
	}
}

// TestApplyKeys_CreatesParentDirs verifies ApplyKeys creates the hidden
// service site directory when it does not already exist.
func TestApplyKeys_CreatesParentDirs(t *testing.T) {
	tmp := t.TempDir()
	cfg := Config{Binary: "/nonexistent/tor", DataDir: tmp}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())

	if _, err := m.ApplyKeys(validNativeKey()); err != nil {
		t.Fatalf("ApplyKeys error: %v", err)
	}
	siteDir := filepath.Join(tmp, "tor", "site")
	if info, err := os.Stat(siteDir); err != nil || !info.IsDir() {
		t.Errorf("expected site dir %s to be created", siteDir)
	}
}

// TestApplyKeys_UnwritablePath verifies ApplyKeys returns an error when the
// hidden-service directory cannot be created.
func TestApplyKeys_UnwritablePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission enforcement differs on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	tmp := t.TempDir()
	locked := filepath.Join(tmp, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })

	cfg := Config{Binary: "/nonexistent/tor", DataDir: filepath.Join(locked, "data")}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())

	if _, err := m.ApplyKeys(validNativeKey()); err == nil {
		t.Error("expected error when hidden service dir cannot be created")
	}
}

// ─── RegenerateAddress ─────────────────────────────────────────────────────────

// TestRegenerateAddress_RemovesKeyFile verifies the existing identity files
// are deleted before Tor is (attempted to be) restarted.
func TestRegenerateAddress_RemovesKeyFile(t *testing.T) {
	tmp := t.TempDir()
	siteDir := filepath.Join(tmp, "tor", "site")
	if err := os.MkdirAll(siteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hs_ed25519_secret_key", "hs_ed25519_public_key", "hostname"} {
		if err := os.WriteFile(filepath.Join(siteDir, name), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cfg := Config{Binary: "/nonexistent/tor", DataDir: tmp}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())

	addr, err := m.RegenerateAddress()
	if err != nil {
		t.Fatalf("RegenerateAddress error: %v", err)
	}
	if addr != "" {
		t.Errorf("RegenerateAddress address = %q; want empty (no Tor binary)", addr)
	}
	for _, name := range []string{"hs_ed25519_secret_key", "hs_ed25519_public_key", "hostname"} {
		if _, err := os.Stat(filepath.Join(siteDir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed", name)
		}
	}
}

// TestRegenerateAddress_RemoveError verifies RegenerateAddress surfaces an
// error when an identity file exists but cannot be removed.
func TestRegenerateAddress_RemoveError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission enforcement differs on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	tmp := t.TempDir()
	siteDir := filepath.Join(tmp, "tor", "site")
	if err := os.MkdirAll(siteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteDir, "hs_ed25519_secret_key"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Removing a directory entry requires write permission on the directory
	// itself; read+execute-only makes every os.Remove inside it fail.
	if err := os.Chmod(siteDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(siteDir, 0o700) })

	cfg := Config{Binary: "/nonexistent/tor", DataDir: tmp}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())

	if _, err := m.RegenerateAddress(); err == nil {
		t.Error("expected error when identity file cannot be removed")
	}
}

// ─── UpdateConfig / Restart ────────────────────────────────────────────────────

// TestUpdateConfig_ReplacesStoredConfig verifies UpdateConfig swaps in the
// new Config before restarting (white-box check on the unexported field,
// same package).
func TestUpdateConfig_ReplacesStoredConfig(t *testing.T) {
	cfg1 := Config{Binary: "/nonexistent/tor", BandwidthRate: "1 MB"}
	m := NewManager(context.Background(), 8080, cfg1, http.NewServeMux())

	cfg2 := Config{Binary: "/nonexistent/tor", BandwidthRate: "5 MB"}
	if err := m.UpdateConfig(cfg2); err != nil {
		t.Fatalf("UpdateConfig error: %v", err)
	}
	if m.cfg.BandwidthRate != "5 MB" {
		t.Errorf("stored config BandwidthRate = %q; want %q", m.cfg.BandwidthRate, "5 MB")
	}
}

// TestUpdateConfig_ErrorPropagation verifies an error from the restart path
// (ensureTorDirs failing because the config dir is unwritable) is returned
// by UpdateConfig itself.
func TestUpdateConfig_ErrorPropagation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission enforcement differs on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	tmp := t.TempDir()
	// A configured binary that exists so FindBinary succeeds and startLocked
	// proceeds far enough to hit ensureTorDirs — no real Tor process is ever
	// launched because that failure happens first.
	bin := filepath.Join(tmp, "tor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(tmp, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })

	cfg := Config{
		Binary:    bin,
		ConfigDir: filepath.Join(locked, "config"),
		DataDir:   filepath.Join(tmp, "data"),
	}
	m := NewManager(context.Background(), 8080, Config{Binary: "/nonexistent/tor"}, http.NewServeMux())

	if err := m.UpdateConfig(cfg); err == nil {
		t.Error("expected UpdateConfig to propagate an ensureTorDirs error")
	}
}

// TestRestart_NoTorBinary verifies Restart succeeds (no-op close + graceful
// disable) repeatedly when no Tor binary is configured.
func TestRestart_NoTorBinary(t *testing.T) {
	cfg := Config{Binary: "/nonexistent/tor"}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())

	for i := 0; i < 3; i++ {
		if err := m.Restart(); err != nil {
			t.Fatalf("Restart() call %d error: %v", i, err)
		}
	}
	if m.Running() {
		t.Error("manager should not report running with no Tor binary")
	}
}

type successfulTorRuntime struct{ closed bool }

func (f *successfulTorRuntime) Close() error                              { f.closed = true; return nil }
func (f *successfulTorRuntime) EnableNetwork(context.Context, bool) error { return nil }
func (f *successfulTorRuntime) Dialer(context.Context, *binettor.DialConf) (*binettor.Dialer, error) {
	return nil, nil
}
func (f *successfulTorRuntime) GetInfo(...string) ([]*control.KeyVal, error) {
	return []*control.KeyVal{{Key: "version", Val: "fixture"}}, nil
}

func TestManager_SuccessfulLifecycleWithTorDouble(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "tor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	configDir, dataDir := filepath.Join(tmp, "config"), filepath.Join(tmp, "data")
	fixture := &successfulTorRuntime{}
	originalStart := startTorRuntime
	startTorRuntime = func(_ context.Context, conf *binettor.StartConf) (managedTor, error) {
		hostname := filepath.Join(conf.DataDir, "site", "hostname")
		if err := os.WriteFile(hostname, []byte("fixtureonionaddress.onion\n"), 0o600); err != nil {
			return nil, err
		}
		return fixture, nil
	}
	t.Cleanup(func() { startTorRuntime = originalStart })

	manager := NewManager(context.Background(), 8080, Config{
		Binary: bin, ConfigDir: configDir, DataDir: dataDir, VirtualPort: 80,
	}, http.NewServeMux())
	t.Cleanup(manager.Close)
	if err := manager.Start(); err != nil {
		t.Fatalf("Start with successful Tor double: %v", err)
	}
	if !manager.Running() {
		t.Fatal("manager did not report the started Tor service")
	}
	if got := manager.OnionAddress(); got != "fixtureonionaddress.onion" {
		t.Fatalf("onion address = %q", got)
	}
	if _, err := os.Stat(filepath.Join(configDir, "tor", "torrc")); err != nil {
		t.Fatalf("generated torrc: %v", err)
	}
	manager.Close()
	if manager.Running() || !fixture.closed {
		t.Fatal("Close did not stop the manager and Tor process double")
	}
}

// TestStartLocked_EnsureTorDirsError verifies startLocked (invoked via
// Start()) surfaces an ensureTorDirs failure once a Tor binary and handler
// are both present — again, no real Tor process is ever launched.
func TestStartLocked_EnsureTorDirsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission enforcement differs on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "tor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(tmp, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })

	cfg := Config{
		Binary:    bin,
		ConfigDir: filepath.Join(locked, "config"),
		DataDir:   filepath.Join(tmp, "data"),
	}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())

	if err := m.Start(); err == nil {
		t.Error("expected Start() to surface an ensureTorDirs error")
	}
	if m.Running() {
		t.Error("manager must not report running after a startup failure")
	}
}

// ─── Config / Manager field plumbing ───────────────────────────────────────────

// TestConfig_AllFieldsSettable verifies every documented Config field can be
// set and is stored verbatim by NewManager.
func TestConfig_AllFieldsSettable(t *testing.T) {
	cfg := Config{
		Binary:                    "/usr/bin/tor",
		UseNetwork:                true,
		MaxCircuits:               32,
		CircuitTimeout:            60,
		BootstrapTimeout:          180,
		SafeLogging:               true,
		MaxStreamsPerCircuit:      100,
		CloseCircuitOnStreamLimit: true,
		BandwidthRate:             "1 MB",
		BandwidthBurst:            "2 MB",
		MaxMonthlyBandwidth:       "100 GB",
		NumIntroPoints:            3,
		VirtualPort:               80,
		ConfigDir:                 "/etc/pastebin",
		DataDir:                   "/var/lib/pastebin",
	}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())
	if m.cfg != cfg {
		t.Errorf("stored config = %+v; want %+v", m.cfg, cfg)
	}
	if m.serverPort != 8080 {
		t.Errorf("serverPort = %d; want 8080", m.serverPort)
	}
	if m.handler == nil {
		t.Error("handler should be stored, not nil")
	}
}

// TestConfig_ZeroValue verifies a zero-value Config does not panic when fed
// through getTorConfig.
func TestConfig_ZeroValue(t *testing.T) {
	var cfg Config
	out := getTorConfig(&cfg, 0)
	if out == "" {
		t.Error("getTorConfig with zero-value Config returned empty string")
	}
}

// ─── getTorConfig table-driven ─────────────────────────────────────────────────

func TestGetTorConfig_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		port    int
		want    []string
		notWant []string
	}{
		{
			name: "network disabled, no accounting",
			cfg:  Config{BandwidthRate: "1 MB", BandwidthBurst: "2 MB"},
			port: 1000,
			want: []string{"SocksPort 0"},
			notWant: []string{
				"SocksPort 127.0.0.1:auto",
				"AccountingMax",
			},
		},
		{
			name: "network enabled with accounting",
			cfg: Config{
				UseNetwork:          true,
				BandwidthRate:       "1 MB",
				BandwidthBurst:      "2 MB",
				MaxMonthlyBandwidth: "50 GB",
			},
			port: 2000,
			want: []string{"SocksPort 127.0.0.1:auto", "AccountingMax 50 GB"},
		},
		{
			name: "safe logging disabled",
			cfg:  Config{SafeLogging: false, BandwidthRate: "1 MB", BandwidthBurst: "2 MB"},
			port: 3000,
			want: []string{"SafeLogging 0"},
		},
		{
			name: "unlimited bandwidth suppresses accounting",
			cfg: Config{
				BandwidthRate:       "1 MB",
				BandwidthBurst:      "2 MB",
				MaxMonthlyBandwidth: "unlimited",
			},
			port:    4000,
			notWant: []string{"AccountingMax"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := getTorConfig(&tc.cfg, tc.port)
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("expected %q in torrc, got:\n%s", w, out)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(out, nw) {
					t.Errorf("did not expect %q in torrc, got:\n%s", nw, out)
				}
			}
		})
	}
}

// ─── Manager concurrent access ─────────────────────────────────────────────────

// TestManager_ConcurrentAccess exercises Running/OnionAddress/GetHTTPClient
// from many goroutines simultaneously to catch data races (run with -race).
func TestManager_ConcurrentAccess(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{}, http.NewServeMux())
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			m.Running()
		}()
		go func() {
			defer wg.Done()
			m.OnionAddress()
		}()
		go func() {
			defer wg.Done()
			m.GetHTTPClient(false)
		}()
	}
	wg.Wait()
}

// ─── Close cancels the manager context ─────────────────────────────────────────

// TestClose_CancelsContext verifies Close() cancels the Manager's internal
// context so any goroutine selecting on it (e.g. Monitor) unblocks.
func TestClose_CancelsContext(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{}, http.NewServeMux())
	m.Close()
	select {
	case <-m.ctx.Done():
	default:
		t.Error("expected manager context to be cancelled after Close()")
	}
}

// ─── FindBinary / findInPath additional edge cases ─────────────────────────────

// TestFindBinary_ConfiguredPathIsDirectory documents FindBinary's actual
// behavior: it only Stat()s the configured path and does not verify it is a
// regular file, so a directory path is returned as-is.
func TestFindBinary_ConfiguredPathIsDirectory(t *testing.T) {
	tmp := t.TempDir()
	got := FindBinary(tmp)
	if got != tmp {
		t.Errorf("FindBinary(directory) = %q; want %q (Stat-only check)", got, tmp)
	}
}

// TestFindInPath_FirstMatchWins verifies the earliest PATH entry containing a
// match wins over a later one.
func TestFindInPath_FirstMatchWins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows-only skip")
	}
	tmp1 := t.TempDir()
	tmp2 := t.TempDir()
	first := filepath.Join(tmp1, "dupbin")
	second := filepath.Join(tmp2, "dupbin")
	if err := os.WriteFile(first, []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp1+":"+tmp2)

	got, err := findInPath("dupbin")
	if err != nil {
		t.Fatalf("findInPath error: %v", err)
	}
	if got != first {
		t.Errorf("findInPath = %q; want first match %q", got, first)
	}
}

// TestCommonTorPaths_HasCurrentOS verifies the well-known-locations table has
// an entry for every OS the project ships on.
func TestCommonTorPaths_HasCurrentOS(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows", "freebsd"} {
		if len(commonTorPaths[goos]) == 0 {
			t.Errorf("commonTorPaths missing entries for %q", goos)
		}
	}
}
