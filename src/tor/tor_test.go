package tor

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ─── FindBinary ───────────────────────────────────────────────────────────────

func TestFindBinary_ConfiguredPathExisting(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "tor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := FindBinary(bin)
	if got != bin {
		t.Errorf("FindBinary(%q) = %q; want %q", bin, got, bin)
	}
}

func TestFindBinary_ConfiguredPathMissing(t *testing.T) {
	got := FindBinary("/nonexistent/path/to/tor")
	if got != "" {
		t.Errorf("FindBinary of missing path returned %q; want empty", got)
	}
}

func TestFindBinary_EmptyPath_ReturnsSomethingOrEmpty(t *testing.T) {
	// When configured path is empty, FindBinary searches PATH and common locs.
	// We cannot guarantee tor is installed; just verify it doesn't panic and
	// returns a non-empty string only when the path actually exists.
	got := FindBinary("")
	if got != "" {
		if _, err := os.Stat(got); err != nil {
			t.Errorf("FindBinary returned %q but file does not exist: %v", got, err)
		}
	}
}

// ─── findInPath ───────────────────────────────────────────────────────────────

func TestFindInPath_NotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)

	_, err := findInPath("definitely-not-a-real-binary-xyzzy")
	if err == nil {
		t.Error("expected error when binary is not in PATH")
	}
}

func TestFindInPath_Found(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH sep and .exe suffix differ on Windows")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "mytor")
	if err := os.WriteFile(bin, []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp)

	got, err := findInPath("mytor")
	if err != nil {
		t.Fatalf("findInPath returned error: %v", err)
	}
	if got != bin {
		t.Errorf("findInPath = %q; want %q", got, bin)
	}
}

func TestFindInPath_EmptyPATH(t *testing.T) {
	t.Setenv("PATH", "")

	_, err := findInPath("tor")
	if err == nil {
		t.Error("expected error when PATH is empty")
	}
}

// ─── NewManager ───────────────────────────────────────────────────────────────

func TestNewManager_ReturnsManager(t *testing.T) {
	ctx := context.Background()
	cfg := Config{
		SafeLogging:      true,
		BandwidthRate:    "1 MB",
		BandwidthBurst:   "2 MB",
		VirtualPort:      80,
		BootstrapTimeout: 60,
	}
	m := NewManager(ctx, 8080, cfg, http.NewServeMux())
	if m == nil {
		t.Fatal("NewManager returned nil")
	}
	if m.Running() {
		t.Error("newly created manager should not be running")
	}
	if m.OnionAddress() != "" {
		t.Errorf("newly created manager OnionAddress should be empty, got %q", m.OnionAddress())
	}
}

// ─── Running / OnionAddress / GetHTTPClient ───────────────────────────────────

func TestManager_RunningFalseWhenNotStarted(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{}, http.NewServeMux())
	if m.Running() {
		t.Error("Running() should be false before Start()")
	}
}

func TestManager_OnionAddressEmptyWhenNotStarted(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{}, http.NewServeMux())
	if addr := m.OnionAddress(); addr != "" {
		t.Errorf("OnionAddress() = %q; want empty string", addr)
	}
}

func TestManager_StatusReadsDoNotWaitForLifecycleOperation(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{}, http.NewServeMux())
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.Running() {
		t.Fatal("Running() = true while lifecycle state is locked")
	}
	if address := m.OnionAddress(); address != "" {
		t.Fatalf("OnionAddress() = %q while lifecycle state is locked; want empty", address)
	}
}

func TestManager_GetHTTPClient_Direct(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{}, http.NewServeMux())
	c := m.GetHTTPClient(false)
	if c == nil {
		t.Fatal("GetHTTPClient returned nil")
	}
	if c.Timeout == 0 {
		t.Error("expected non-zero timeout on direct HTTP client")
	}
}

func TestManager_GetHTTPClient_TorNotRunning(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{}, http.NewServeMux())
	// Tor not started — requesting Tor client falls back to direct.
	c := m.GetHTTPClient(true)
	if c == nil {
		t.Fatal("GetHTTPClient returned nil")
	}
}

func TestManager_Close_NoOp(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{}, http.NewServeMux())
	// Close on an unstarted manager must not panic.
	m.Close()
	if m.Running() {
		t.Error("Running() should be false after Close()")
	}
}

// ─── Start without a real Tor binary ─────────────────────────────────────────

func TestManager_Start_NoTorBinary(t *testing.T) {
	// Point to a non-existent binary; Start should return nil (graceful disable).
	cfg := Config{
		Binary:           "/nonexistent/tor",
		BandwidthRate:    "1 MB",
		BandwidthBurst:   "2 MB",
		BootstrapTimeout: 30,
	}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())
	err := m.Start()
	if err != nil {
		t.Errorf("Start() with missing tor binary returned error: %v", err)
	}
	if m.Running() {
		t.Error("manager should not be running when Tor binary is missing")
	}
}

func TestManager_Start_NilHandler(t *testing.T) {
	// A configured binary path that resolves (FindBinary only Stat()s it) but a
	// nil handler must still gracefully disable before any Tor process launch.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "tor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Binary: bin}
	m := NewManager(context.Background(), 8080, cfg, nil)
	if err := m.Start(); err != nil {
		t.Errorf("Start() with nil handler returned error: %v", err)
	}
	if m.Running() {
		t.Error("manager should not be running when handler is nil")
	}
}

// ─── getTorConfig ─────────────────────────────────────────────────────────────

func TestGetTorConfig_SafeLoggingEnabled(t *testing.T) {
	cfg := &Config{
		SafeLogging:    true,
		BandwidthRate:  "1 MB",
		BandwidthBurst: "2 MB",
	}
	out := getTorConfig(cfg, 9999)
	if !strings.Contains(out, "SafeLogging 1") {
		t.Errorf("expected 'SafeLogging 1' in config, got:\n%s", out)
	}
}

func TestGetTorConfig_SafeLoggingDisabled(t *testing.T) {
	cfg := &Config{
		SafeLogging:    false,
		BandwidthRate:  "1 MB",
		BandwidthBurst: "2 MB",
	}
	out := getTorConfig(cfg, 9999)
	if !strings.Contains(out, "SafeLogging 0") {
		t.Errorf("expected 'SafeLogging 0' in config, got:\n%s", out)
	}
}

func TestGetTorConfig_SocksPortZeroByDefault(t *testing.T) {
	cfg := &Config{BandwidthRate: "1 MB", BandwidthBurst: "2 MB"}
	out := getTorConfig(cfg, 9999)
	if !strings.Contains(out, "SocksPort 0") {
		t.Errorf("expected 'SocksPort 0' when UseNetwork=false, got:\n%s", out)
	}
}

func TestGetTorConfig_SocksPortAutoWhenNetworkEnabled(t *testing.T) {
	cfg := &Config{
		UseNetwork:     true,
		BandwidthRate:  "1 MB",
		BandwidthBurst: "2 MB",
	}
	out := getTorConfig(cfg, 9999)
	if !strings.Contains(out, "SocksPort 127.0.0.1:auto") {
		t.Errorf("expected 'SocksPort 127.0.0.1:auto' when UseNetwork=true, got:\n%s", out)
	}
}

func TestGetTorConfig_MonthlyBandwidthLimit(t *testing.T) {
	cfg := &Config{
		BandwidthRate:       "1 MB",
		BandwidthBurst:      "2 MB",
		MaxMonthlyBandwidth: "100 GB",
	}
	out := getTorConfig(cfg, 9999)
	if !strings.Contains(out, "AccountingMax 100 GB") {
		t.Errorf("expected 'AccountingMax 100 GB' in config, got:\n%s", out)
	}
}

func TestGetTorConfig_NoDefaultPorts(t *testing.T) {
	cfg := &Config{BandwidthRate: "1 MB", BandwidthBurst: "2 MB"}
	out := getTorConfig(cfg, 9999)
	// ControlPort must use auto (never a hardcoded port number).
	if !strings.Contains(out, "ControlPort 127.0.0.1:auto") {
		t.Errorf("expected 'ControlPort 127.0.0.1:auto' in config, got:\n%s", out)
	}
	// SocksPort must be 0 (hidden-service-only mode) or auto — never hardcoded.
	if strings.Contains(out, "SocksPort 9050") || strings.Contains(out, "SocksPort 9051") {
		t.Errorf("torrc must not bind SocksPort to default ports 9050/9051, got:\n%s", out)
	}
}

func TestGetTorConfig_NoRelayOrExit(t *testing.T) {
	cfg := &Config{BandwidthRate: "1 MB", BandwidthBurst: "2 MB"}
	out := getTorConfig(cfg, 9999)
	if !strings.Contains(out, "ExitRelay 0") {
		t.Errorf("expected 'ExitRelay 0' in config, got:\n%s", out)
	}
	if !strings.Contains(out, "ExitPolicy reject *:*") {
		t.Errorf("expected reject exit policy in config, got:\n%s", out)
	}
}

func TestGetTorConfig_HiddenServiceDeclaration(t *testing.T) {
	cfg := &Config{
		BandwidthRate:  "1 MB",
		BandwidthBurst: "2 MB",
		VirtualPort:    80,
		DataDir:        "/data",
	}
	out := getTorConfig(cfg, 54321)
	wantDir := "HiddenServiceDir " + filepath.Join("/data", "tor", "site")
	if !strings.Contains(out, wantDir) {
		t.Errorf("expected %q in config, got:\n%s", wantDir, out)
	}
	if !strings.Contains(out, "HiddenServiceVersion 3") {
		t.Errorf("expected 'HiddenServiceVersion 3' in config, got:\n%s", out)
	}
	if !strings.Contains(out, "HiddenServicePort 80 127.0.0.1:54321") {
		t.Errorf("expected backend forwarding line in config, got:\n%s", out)
	}
	if !strings.Contains(out, "HiddenServiceExportCircuitID haproxy") {
		t.Errorf("expected PROXY-protocol circuit-id export directive, got:\n%s", out)
	}
}

// ─── ensureTorDirs ────────────────────────────────────────────────────────────

func TestEnsureTorDirs_CreatesDirectories(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config")
	dataDir := filepath.Join(tmp, "data")

	if err := ensureTorDirs(configDir, dataDir); err != nil {
		t.Fatalf("ensureTorDirs error: %v", err)
	}

	expectedDirs := []string{
		filepath.Join(configDir, "tor"),
		filepath.Join(dataDir, "tor"),
		filepath.Join(dataDir, "tor", "site"),
	}
	for _, d := range expectedDirs {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("expected directory %s to exist: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
	}
}

func TestEnsureTorDirs_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config")
	dataDir := filepath.Join(tmp, "data")

	// Call twice; second call should succeed without error.
	if err := ensureTorDirs(configDir, dataDir); err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if err := ensureTorDirs(configDir, dataDir); err != nil {
		t.Fatalf("second call error: %v", err)
	}
}

// ─── migrateLegacyKey ─────────────────────────────────────────────────────────

func TestMigrateLegacyKey_NoFilePresent(t *testing.T) {
	siteDir := filepath.Join(t.TempDir(), "site")
	if err := migrateLegacyKey(siteDir); err != nil {
		t.Fatalf("migrateLegacyKey with no key file returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(siteDir, "hs_ed25519_secret_key")); err == nil {
		t.Error("migrateLegacyKey must not create a key file when none exists")
	}
}

func TestMigrateLegacyKey_LegacyToNative_PreservesKeyMaterial(t *testing.T) {
	siteDir := t.TempDir()
	keyPath := filepath.Join(siteDir, "hs_ed25519_secret_key")

	// Legacy blob: raw 64-byte expanded ed25519 key, no header.
	legacy := make([]byte, legacyKeyBlobLen)
	for i := range legacy {
		legacy[i] = byte(i + 1)
	}
	if err := os.WriteFile(keyPath, legacy, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyKey(siteDir); err != nil {
		t.Fatalf("migrateLegacyKey error: %v", err)
	}

	got, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	// The migrated file must be exactly the 32-byte native header plus the
	// original 64-byte key material — nativeKeyFileLen (96 bytes) total.
	wantLen := len(nativeKeyHeader) + legacyKeyBlobLen
	if len(got) != wantLen {
		t.Fatalf("migrated file length = %d; want %d", len(got), wantLen)
	}
	if string(got[:len(nativeKeyHeader)]) != string(nativeKeyHeader) {
		t.Errorf("migrated file header = %q; want %q", got[:len(nativeKeyHeader)], nativeKeyHeader)
	}
	// The trailing key material must be byte-for-byte identical to the
	// original legacy blob — this is what preserves the .onion address.
	if string(got[len(nativeKeyHeader):]) != string(legacy) {
		t.Error("migrated key material does not match original legacy blob byte-for-byte")
	}
}

func TestMigrateLegacyKey_AlreadyNative_Untouched(t *testing.T) {
	siteDir := t.TempDir()
	keyPath := filepath.Join(siteDir, "hs_ed25519_secret_key")

	// A file is only recognized as "already native" by migrateLegacyKey when
	// its length equals nativeKeyFileLen (96) exactly.
	body := make([]byte, nativeKeyFileLen-len(nativeKeyHeader))
	native := append(append([]byte{}, nativeKeyHeader...), body...)
	if len(native) != nativeKeyFileLen {
		t.Fatalf("test fixture length = %d; want %d", len(native), nativeKeyFileLen)
	}
	if err := os.WriteFile(keyPath, native, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyKey(siteDir); err != nil {
		t.Fatalf("migrateLegacyKey on already-native file returned error: %v", err)
	}

	got, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(native) {
		t.Error("migrateLegacyKey modified an already-native key file")
	}
}

func TestMigrateLegacyKey_UnexpectedSize_LeftUntouched(t *testing.T) {
	siteDir := t.TempDir()
	keyPath := filepath.Join(siteDir, "hs_ed25519_secret_key")

	odd := []byte("this is not a valid key file at all")
	if err := os.WriteFile(keyPath, odd, 0o600); err != nil {
		t.Fatal(err)
	}

	err := migrateLegacyKey(siteDir)
	if err == nil {
		t.Error("expected error for a key file of unexpected size")
	}

	got, readErr := os.ReadFile(keyPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(odd) {
		t.Error("migrateLegacyKey must leave an unrecognized-size file untouched")
	}
}

func TestGetTorConfig_RelayHardeningAndTuning(t *testing.T) {
	cfg := &Config{VirtualPort: 80, DataDir: "/d", SafeLogging: true, BandwidthRate: "1 MB", BandwidthBurst: "2 MB",
		UseNetwork: true, NumIntroPoints: 5, MaxStreamsPerCircuit: 100, CloseCircuitOnStreamLimit: true}
	out := getTorConfig(cfg, 4242)
	for _, want := range []string{
		"SocksPort 127.0.0.1:auto", "SocksPolicy accept 127.0.0.1", "PublishServerDescriptor 0",
		"ExitRelay 0", "ExitPolicy reject *:*", "ORPort 0", "DirPort 0",
		"HiddenServiceNumIntroductionPoints 5", "HiddenServiceMaxStreams 100", "HiddenServiceMaxStreamsCloseCircuit 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("torrc missing %q", want)
		}
	}
	off := getTorConfig(&Config{VirtualPort: 80, DataDir: "/d", NumIntroPoints: 99}, 1)
	if strings.Contains(off, "SocksPolicy") || strings.Contains(off, "HiddenServiceNumIntroductionPoints") || !strings.Contains(off, "SocksPort 0") {
		t.Errorf("hardening/tuning leaked into default torrc:\n%s", off)
	}
}

func TestGetTorConfig_LogFile(t *testing.T) {
	out := getTorConfig(&Config{VirtualPort: 80, DataDir: "/d", LogDir: "/var/log/apimgr/pastebin"}, 1)
	if !strings.Contains(out, "Log notice file /var/log/apimgr/pastebin/tor.log") {
		t.Errorf("torrc missing tor.log directive:\n%s", out)
	}
	if strings.Contains(getTorConfig(&Config{VirtualPort: 80, DataDir: "/d"}, 1), "Log notice file") {
		t.Error("log directive emitted without LogDir")
	}
}
