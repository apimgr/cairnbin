package tor

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ─── FindBinary with PATH override ───────────────────────────────────────────

// TestFindBinary_FoundInPath verifies FindBinary picks up a binary placed in
// a temp directory that is prepended to PATH (empty configuredPath branch).
func TestFindBinary_FoundInPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH separator differences on Windows")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "tor")
	if err := os.WriteFile(bin, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp)

	got := FindBinary("")
	if got != bin {
		t.Errorf("FindBinary(\"\") = %q; want %q", got, bin)
	}
}

// TestFindBinary_EmptyPATH_NoCommonPath verifies FindBinary returns "" when
// PATH is empty and no common OS path exists in a temp-only environment.
func TestFindBinary_EmptyPATH_NoCommonPath(t *testing.T) {
	t.Setenv("PATH", "")
	got := FindBinary("")
	if got != "" {
		if _, err := os.Stat(got); err != nil {
			t.Errorf("FindBinary returned non-existent path %q", got)
		}
	}
}

// ─── findInPath edge cases ────────────────────────────────────────────────────

// TestFindInPath_IsDir verifies that a directory with the same name as the
// binary is not returned (must be a regular file, not a dir).
func TestFindInPath_IsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows-only skip")
	}
	tmp := t.TempDir()
	dirEntry := filepath.Join(tmp, "tor")
	if err := os.Mkdir(dirEntry, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp)

	_, err := findInPath("tor")
	if err == nil {
		t.Error("expected error when only a directory named 'tor' exists in PATH")
	}
}

// TestFindInPath_MultiplePathEntries verifies the function scans all PATH
// entries and returns the first match.
func TestFindInPath_MultiplePathEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows-only skip")
	}
	tmp1 := t.TempDir()
	tmp2 := t.TempDir()
	bin := filepath.Join(tmp2, "myapp")
	if err := os.WriteFile(bin, []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp1+":"+tmp2)

	got, err := findInPath("myapp")
	if err != nil {
		t.Fatalf("findInPath: unexpected error: %v", err)
	}
	if got != bin {
		t.Errorf("got %q; want %q", got, bin)
	}
}

// ─── ensureTorDirs ────────────────────────────────────────────────────────────

// TestEnsureTorDirs_PermissionsAre0700 verifies that all created directories
// carry 0700 permissions on non-Windows platforms.
func TestEnsureTorDirs_PermissionsAre0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not enforced on Windows")
	}
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config")
	dataDir := filepath.Join(tmp, "data")

	if err := ensureTorDirs(configDir, dataDir); err != nil {
		t.Fatalf("ensureTorDirs: %v", err)
	}

	expectedDirs := []string{
		filepath.Join(configDir, "tor"),
		filepath.Join(dataDir, "tor"),
		filepath.Join(dataDir, "tor", "site"),
	}
	for _, d := range expectedDirs {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("stat %s: %v", d, err)
			continue
		}
		perm := info.Mode().Perm()
		if perm != 0o700 {
			t.Errorf("dir %s has perm %o; want 0700", d, perm)
		}
	}
}

// TestEnsureTorDirs_MkdirError verifies ensureTorDirs returns an error when
// the parent directory cannot be written to.
func TestEnsureTorDirs_MkdirError(t *testing.T) {
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
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	configDir := filepath.Join(locked, "config")
	dataDir := filepath.Join(tmp, "data")
	if err := ensureTorDirs(configDir, dataDir); err == nil {
		t.Error("expected error when config dir parent is unwritable")
	}
}

// ─── getTorConfig additional branches ────────────────────────────────────────

// TestGetTorConfig_UseNetwork sets UseNetwork=true and verifies SocksPort is
// set to auto.
func TestGetTorConfig_UseNetwork(t *testing.T) {
	cfg := &Config{
		UseNetwork:     true,
		BandwidthRate:  "1 MB",
		BandwidthBurst: "2 MB",
	}
	out := getTorConfig(cfg, 1234)
	if !strings.Contains(out, "SocksPort 127.0.0.1:auto") {
		t.Errorf("expected 'SocksPort 127.0.0.1:auto' when UseNetwork=true, got:\n%s", out)
	}
}

// TestGetTorConfig_UnlimitedBandwidth verifies no accounting block is
// generated when MaxMonthlyBandwidth is "unlimited".
func TestGetTorConfig_UnlimitedBandwidth(t *testing.T) {
	cfg := &Config{
		BandwidthRate:       "1 MB",
		BandwidthBurst:      "2 MB",
		MaxMonthlyBandwidth: "unlimited",
	}
	out := getTorConfig(cfg, 1234)
	if strings.Contains(out, "AccountingMax") {
		t.Errorf("expected no AccountingMax when MaxMonthlyBandwidth='unlimited', got:\n%s", out)
	}
}

// TestGetTorConfig_EmptyBandwidth verifies no accounting block is generated
// when MaxMonthlyBandwidth is empty.
func TestGetTorConfig_EmptyBandwidth(t *testing.T) {
	cfg := &Config{
		BandwidthRate:  "1 MB",
		BandwidthBurst: "2 MB",
	}
	out := getTorConfig(cfg, 1234)
	if strings.Contains(out, "AccountingMax") {
		t.Errorf("expected no AccountingMax when MaxMonthlyBandwidth='', got:\n%s", out)
	}
}

// TestGetTorConfig_ExitPolicyPresent verifies the config always disables
// relay and exit traffic.
func TestGetTorConfig_ExitPolicyPresent(t *testing.T) {
	cfg := &Config{BandwidthRate: "512 KB", BandwidthBurst: "1 MB"}
	out := getTorConfig(cfg, 1234)
	for _, required := range []string{"ORPort 0", "DirPort 0", "ExitRelay 0", "ExitPolicy reject *:*"} {
		if !strings.Contains(out, required) {
			t.Errorf("missing %q in torrc:\n%s", required, out)
		}
	}
}

// TestGetTorConfig_BandwidthValuesPresent verifies BandwidthRate and
// BandwidthBurst appear verbatim in the config output.
func TestGetTorConfig_BandwidthValuesPresent(t *testing.T) {
	cfg := &Config{BandwidthRate: "2 MB", BandwidthBurst: "4 MB"}
	out := getTorConfig(cfg, 1234)
	if !strings.Contains(out, "BandwidthRate 2 MB") {
		t.Errorf("expected 'BandwidthRate 2 MB' in config, got:\n%s", out)
	}
	if !strings.Contains(out, "BandwidthBurst 4 MB") {
		t.Errorf("expected 'BandwidthBurst 4 MB' in config, got:\n%s", out)
	}
}

// TestGetTorConfig_StartupOptimizationFlags verifies early-directory fetch
// and debugger-attachment directives are present.
func TestGetTorConfig_StartupOptimizationFlags(t *testing.T) {
	cfg := &Config{BandwidthRate: "1 MB", BandwidthBurst: "2 MB"}
	out := getTorConfig(cfg, 1234)
	for _, flag := range []string{
		"FetchDirInfoEarly 1",
		"FetchDirInfoExtraEarly 1",
		"DisableDebuggerAttachment 1",
	} {
		if !strings.Contains(out, flag) {
			t.Errorf("expected %q in torrc, got:\n%s", flag, out)
		}
	}
}

// TestGetTorConfig_HiddenServiceHardening verifies the PROXY-protocol export
// and single-hop / vanguards directives required for the new backend
// architecture are present.
func TestGetTorConfig_HiddenServiceHardening(t *testing.T) {
	cfg := &Config{BandwidthRate: "1 MB", BandwidthBurst: "2 MB"}
	out := getTorConfig(cfg, 1234)
	for _, want := range []string{
		"HiddenServiceExportCircuitID haproxy",
		"VanguardsLiteEnabled 1",
		"HiddenServiceSingleHopMode 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in torrc, got:\n%s", want, out)
		}
	}
}

// TestGetTorConfig_BackendPortVaries verifies the generated HiddenServicePort
// line always targets the caller-supplied loopback backend port, never the
// clearnet server port.
func TestGetTorConfig_BackendPortVaries(t *testing.T) {
	cfg := &Config{BandwidthRate: "1 MB", BandwidthBurst: "2 MB", VirtualPort: 80}
	out := getTorConfig(cfg, 55123)
	if !strings.Contains(out, "HiddenServicePort 80 127.0.0.1:55123") {
		t.Errorf("expected backend port 55123 wired into HiddenServicePort line, got:\n%s", out)
	}
}

// ─── Manager state after Close ────────────────────────────────────────────────

// TestManager_DoubleClose verifies that calling Close twice does not panic.
func TestManager_DoubleClose(t *testing.T) {
	m := NewManager(context.Background(), 9000, Config{}, http.NewServeMux())
	m.Close()
	m.Close()
}

// TestManager_GetHTTPClient_UseTorNoSvc verifies that requesting a Tor client
// when no service is running returns a direct client with a non-zero timeout.
func TestManager_GetHTTPClient_UseTorNoSvc(t *testing.T) {
	m := NewManager(context.Background(), 9000, Config{}, http.NewServeMux())
	c := m.GetHTTPClient(true)
	if c == nil {
		t.Fatal("GetHTTPClient returned nil")
	}
	if c.Timeout == 0 {
		t.Error("expected non-zero timeout on fallback direct client")
	}
}

// TestManager_GetHTTPClient_DirectTimeout verifies the direct client uses
// a 30-second timeout exactly.
func TestManager_GetHTTPClient_DirectTimeout(t *testing.T) {
	m := NewManager(context.Background(), 9000, Config{}, http.NewServeMux())
	c := m.GetHTTPClient(false)
	if c.Timeout.Seconds() != 30 {
		t.Errorf("direct client timeout = %v; want 30s", c.Timeout)
	}
}

// ─── Monitor exits on context cancellation ────────────────────────────────────

// TestManager_Monitor_ExitsOnCancel verifies the Monitor goroutine exits when
// the manager's context is cancelled. No Tor binary is needed — the goroutine
// wakes on ctx.Done() and returns without touching the service.
func TestManager_Monitor_ExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(ctx, 9000, Config{}, http.NewServeMux())

	done := make(chan struct{})
	go func() {
		m.Monitor()
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Monitor did not exit after context cancellation within 2 seconds")
	}
}

// ─── migrateLegacyKey additional branches ─────────────────────────────────────

// TestMigrateLegacyKey_NativeSizeWrongHeader verifies a 96-byte file that does
// not carry the correct native header is left untouched and an error is
// returned rather than the file being reinterpreted or overwritten.
func TestMigrateLegacyKey_NativeSizeWrongHeader(t *testing.T) {
	siteDir := t.TempDir()
	keyPath := filepath.Join(siteDir, "hs_ed25519_secret_key")

	bogus := make([]byte, nativeKeyFileLen)
	for i := range bogus {
		bogus[i] = 0xAA
	}
	if err := os.WriteFile(keyPath, bogus, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyKey(siteDir); err == nil {
		t.Error("expected error for a native-length file with an incorrect header")
	}

	got, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bogus) {
		t.Error("migrateLegacyKey must not modify a file it cannot recognize")
	}
}

// ─── RegenerateAddress / ApplyKeys without a real Tor process ─────────────────

// TestRegenerateAddress_NoKeyFileToRemove verifies RegenerateAddress succeeds
// (no error) when no hidden-service identity exists yet, and — since no Tor
// binary is configured — reports an empty address rather than starting Tor.
func TestRegenerateAddress_NoKeyFileToRemove(t *testing.T) {
	tmp := t.TempDir()
	cfg := Config{
		Binary:  "/nonexistent/tor",
		DataDir: tmp,
	}
	m := NewManager(context.Background(), 8080, cfg, http.NewServeMux())
	addr, err := m.RegenerateAddress()
	if err != nil {
		t.Fatalf("RegenerateAddress error: %v", err)
	}
	if addr != "" {
		t.Errorf("RegenerateAddress address = %q; want empty (no Tor binary)", addr)
	}
}

// TestApplyKeys_InvalidLength verifies ApplyKeys rejects key data that is not
// exactly the native 96-byte secret-key file size.
func TestApplyKeys_InvalidLength(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{DataDir: t.TempDir()}, http.NewServeMux())
	_, err := m.ApplyKeys([]byte("too short"))
	if err == nil {
		t.Error("expected error for key data of the wrong length")
	}
}

// TestApplyKeys_InvalidHeader verifies ApplyKeys rejects 96-byte key data
// that does not carry Tor's native secret-key header.
func TestApplyKeys_InvalidHeader(t *testing.T) {
	m := NewManager(context.Background(), 8080, Config{DataDir: t.TempDir()}, http.NewServeMux())
	bogus := make([]byte, nativeKeyFileLen)
	_, err := m.ApplyKeys(bogus)
	if err == nil {
		t.Error("expected error for key data with an invalid header")
	}
}
