package i2p

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestProviderString verifies the provider names reported in /server/healthz.
func TestProviderString(t *testing.T) {
	cases := map[Provider]string{
		ProviderNone: "none",
		ProviderI2Pd: "i2pd",
		ProviderSAM:  "sam",
	}
	for p, want := range cases {
		if got := p.String(); got != want {
			t.Errorf("Provider(%d).String() = %q, want %q", p, got, want)
		}
	}
}

// ─── FindBinary ─────────────────────────────────────────────────────────────

func TestFindBinary_ConfiguredPathExisting(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "i2pd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindBinary(bin); got != bin {
		t.Errorf("FindBinary(%q) = %q; want %q", bin, got, bin)
	}
}

func TestFindBinary_ConfiguredPathMissing(t *testing.T) {
	if got := FindBinary("/nonexistent/path/to/i2pd"); got != "" {
		t.Errorf("FindBinary of missing configured path returned %q; want empty", got)
	}
}

func TestFindBinary_EmptyPath_NeverPanics(t *testing.T) {
	got := FindBinary("")
	if got != "" {
		if _, err := os.Stat(got); err != nil {
			t.Errorf("FindBinary returned %q but file does not exist: %v", got, err)
		}
	}
}

// ─── findInPath ─────────────────────────────────────────────────────────────

func TestFindInPath_NotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	if _, err := findInPath("definitely-not-a-real-binary-xyzzy"); err == nil {
		t.Error("expected error when binary is not in PATH")
	}
}

func TestFindInPath_Found(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH sep and .exe suffix differ on Windows")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "i2pd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tmp)
	got, err := findInPath("i2pd")
	if err != nil {
		t.Fatalf("findInPath returned error: %v", err)
	}
	if got != bin {
		t.Errorf("findInPath = %q; want %q", got, bin)
	}
}

func TestFindInPath_EmptyPathEnv(t *testing.T) {
	t.Setenv("PATH", "")
	if _, err := findInPath("i2pd"); err == nil {
		t.Error("expected error when PATH is empty")
	}
}

// ─── b32Address ─────────────────────────────────────────────────────────────

func TestB32Address_Deterministic(t *testing.T) {
	dest := []byte("some-fake-destination-blob-for-testing")
	a := b32Address(dest)
	b := b32Address(dest)
	if a != b {
		t.Errorf("b32Address not deterministic: %q != %q", a, b)
	}
	if !strings.HasSuffix(a, ".b32.i2p") {
		t.Errorf("b32Address(%q) missing .b32.i2p suffix", a)
	}
	if a != strings.ToLower(a) {
		t.Errorf("b32Address(%q) not lowercase", a)
	}
	if strings.ContainsAny(a, "=") {
		t.Errorf("b32Address(%q) contains padding, want unpadded", a)
	}
}

func TestB32Address_DifferentInputsDifferentOutput(t *testing.T) {
	a := b32Address([]byte("destination-one"))
	b := b32Address([]byte("destination-two"))
	if a == b {
		t.Errorf("b32Address collided for distinct inputs: %q", a)
	}
}

// ─── getTunnelsConfig ───────────────────────────────────────────────────────

func TestGetTunnelsConfig_ContainsExpectedDirectives(t *testing.T) {
	cfg := &Config{
		InboundLength:    3,
		OutboundLength:   3,
		InboundQuantity:  5,
		OutboundQuantity: 5,
		SignatureType:    7,
	}
	siteDir := "/tmp/example/i2p/site"
	out := getTunnelsConfig(cfg, siteDir, 54321)

	for _, want := range []string{
		"[site]",
		"type = server",
		"host = 127.0.0.1",
		"port = 54321",
		"keys = " + filepath.Join(siteDir, "site-keys.dat"),
		"inbound.length = 3",
		"outbound.length = 3",
		"inbound.quantity = 5",
		"outbound.quantity = 5",
		"signaturetype = 7",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("getTunnelsConfig missing %q\nfull output:\n%s", want, out)
		}
	}
}

// ─── ensureI2PDirs ──────────────────────────────────────────────────────────

func TestEnsureI2PDirs_CreatesExpectedTreeWithPerms(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "config")
	dataDir := filepath.Join(tmp, "data")

	if err := ensureI2PDirs(configDir, dataDir); err != nil {
		t.Fatalf("ensureI2PDirs: %v", err)
	}

	for _, d := range []string{
		filepath.Join(configDir, "i2p"),
		filepath.Join(dataDir, "i2p"),
		filepath.Join(dataDir, "i2p", "site"),
	} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("expected dir %s to exist: %v", d, err)
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
		if runtime.GOOS != "windows" {
			if perm := info.Mode().Perm(); perm != 0o700 {
				t.Errorf("%s perms = %o; want 0700", d, perm)
			}
		}
	}
}

// ─── waitForKeysAddress ─────────────────────────────────────────────────────

func TestWaitForKeysAddress_TimesOutWhenFileNeverAppears(t *testing.T) {
	tmp := t.TempDir()
	ctx := context.Background()
	_, err := waitForKeysAddress(ctx, tmp, 500*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error when key file never appears")
	}
}

func TestWaitForKeysAddress_ReturnsAddressOnceFileAppears(t *testing.T) {
	tmp := t.TempDir()
	keyPath := filepath.Join(tmp, "site-keys.dat")

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.WriteFile(keyPath, []byte("fake-destination-bytes"), 0o600)
	}()

	ctx := context.Background()
	addr, err := waitForKeysAddress(ctx, tmp, 3*time.Second)
	if err != nil {
		t.Fatalf("waitForKeysAddress: %v", err)
	}
	want := b32Address([]byte("fake-destination-bytes"))
	if addr != want {
		t.Errorf("waitForKeysAddress = %q; want %q", addr, want)
	}
}

func TestWaitForKeysAddress_ContextCancelled(t *testing.T) {
	tmp := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForKeysAddress(ctx, tmp, 5*time.Second); err == nil {
		t.Error("expected error when context already cancelled")
	}
}

// ─── probeSAM ───────────────────────────────────────────────────────────────

func TestProbeSAM_EmptyAddress(t *testing.T) {
	if probeSAM("") {
		t.Error("probeSAM(\"\") should be false")
	}
}

func TestProbeSAM_NothingListening(t *testing.T) {
	if probeSAM("127.0.0.1:1") {
		t.Error("probeSAM should fail when nothing is listening")
	}
}

func TestProbeSAM_Listening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	if !probeSAM(ln.Addr().String()) {
		t.Error("probeSAM should succeed when a listener is present")
	}
}

// ─── samField / samSend / samReadLine ──────────────────────────────────────

func TestSamField_ExtractsValue(t *testing.T) {
	line := "SESSION STATUS RESULT=OK DESTINATION=abc123XYZ"
	if got := samField(line, "RESULT"); got != "OK" {
		t.Errorf("samField(RESULT) = %q; want OK", got)
	}
	if got := samField(line, "DESTINATION"); got != "abc123XYZ" {
		t.Errorf("samField(DESTINATION) = %q; want abc123XYZ", got)
	}
}

func TestSamField_MissingKey(t *testing.T) {
	line := "SESSION STATUS RESULT=OK"
	if got := samField(line, "NOPE"); got != "" {
		t.Errorf("samField for missing key = %q; want empty", got)
	}
}

func TestSamSendAndReadLine_RoundTrip(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	go func() {
		r := bufio.NewReader(server)
		line, _ := r.ReadString('\n')
		_, _ = server.Write([]byte("ECHO " + line))
	}()

	rw := bufio.NewReadWriter(bufio.NewReader(client), bufio.NewWriter(client))
	if err := samSend(rw, "HELLO VERSION MIN=3.0 MAX=3.3\n"); err != nil {
		t.Fatalf("samSend: %v", err)
	}
	line, err := samReadLine(rw)
	if err != nil {
		t.Fatalf("samReadLine: %v", err)
	}
	if !strings.Contains(line, "HELLO VERSION MIN=3.0 MAX=3.3") {
		t.Errorf("round-trip line = %q, missing expected content", line)
	}
}

// ─── samCreateSession against a fake SAM bridge ─────────────────────────────

// fakeSAMServer speaks just enough SAMv3 to exercise samCreateSession's
// success path: HELLO -> RESULT=OK, SESSION CREATE -> RESULT=OK plus a
// DESTINATION, STREAM FORWARD -> RESULT=OK.
func fakeSAMServer(t *testing.T, conn net.Conn) {
	t.Helper()
	r := bufio.NewReader(conn)
	for i := 0; i < 3; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		switch {
		case strings.HasPrefix(line, "HELLO"):
			_, _ = conn.Write([]byte("HELLO REPLY RESULT=OK VERSION=3.3\n"))
		case strings.HasPrefix(line, "SESSION CREATE"):
			_, _ = conn.Write([]byte("SESSION STATUS RESULT=OK DESTINATION=fakeDestinationBlobValue\n"))
		case strings.HasPrefix(line, "STREAM FORWARD"):
			_, _ = conn.Write([]byte("STREAM STATUS RESULT=OK\n"))
		}
	}
}

func TestSamCreateSession_Success(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()

	go fakeSAMServer(t, server)

	tmp := t.TempDir()
	destPath := filepath.Join(tmp, "sam-dest.dat")
	cfg := Config{
		InboundLength: 3, OutboundLength: 3,
		InboundQuantity: 5, OutboundQuantity: 5,
		SignatureType: 7,
	}

	done := make(chan struct{})
	var addr string
	var err error
	go func() {
		addr, err = samCreateSession(client, destPath, cfg, 12345)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("samCreateSession timed out")
	}

	if err != nil {
		t.Fatalf("samCreateSession: %v", err)
	}
	want := b32Address([]byte("fakeDestinationBlobValue"))
	if addr != want {
		t.Errorf("addr = %q; want %q", addr, want)
	}

	persisted, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("expected destination to be persisted: %v", err)
	}
	if string(persisted) != "fakeDestinationBlobValue" {
		t.Errorf("persisted destination = %q; want fakeDestinationBlobValue", persisted)
	}
}

func TestSamCreateSession_ReusesPersistedDestination(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()

	tmp := t.TempDir()
	destPath := filepath.Join(tmp, "sam-dest.dat")
	if err := os.WriteFile(destPath, []byte("already-persisted-dest"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sawDestInCreate bool
	go func() {
		r := bufio.NewReader(server)
		for i := 0; i < 3; i++ {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "HELLO"):
				_, _ = server.Write([]byte("HELLO REPLY RESULT=OK VERSION=3.3\n"))
			case strings.HasPrefix(line, "SESSION CREATE"):
				sawDestInCreate = strings.Contains(line, "DESTINATION=already-persisted-dest")
				_, _ = server.Write([]byte("SESSION STATUS RESULT=OK DESTINATION=already-persisted-dest\n"))
			case strings.HasPrefix(line, "STREAM FORWARD"):
				_, _ = server.Write([]byte("STREAM STATUS RESULT=OK\n"))
			}
		}
	}()

	cfg := Config{InboundLength: 3, OutboundLength: 3, InboundQuantity: 5, OutboundQuantity: 5, SignatureType: 7}
	done := make(chan struct{})
	var err error
	go func() {
		_, err = samCreateSession(client, destPath, cfg, 12345)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("samCreateSession timed out")
	}
	if err != nil {
		t.Fatalf("samCreateSession: %v", err)
	}
	if !sawDestInCreate {
		t.Error("expected SESSION CREATE to reuse the persisted destination, not TRANSIENT")
	}
}

func TestSamCreateSession_HelloFailure(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()

	go func() {
		r := bufio.NewReader(server)
		_, _ = r.ReadString('\n')
		_, _ = server.Write([]byte("HELLO REPLY RESULT=NOVERSION\n"))
	}()

	tmp := t.TempDir()
	cfg := Config{InboundLength: 3, OutboundLength: 3, InboundQuantity: 5, OutboundQuantity: 5, SignatureType: 7}
	_, err := samCreateSession(client, filepath.Join(tmp, "d"), cfg, 1234)
	if err == nil {
		t.Error("expected error on SAM HELLO failure")
	}
}

// ─── Manager: disabled / no-handler / opt-in gating ─────────────────────────

func TestManager_StartDisabled_NoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, Config{Enabled: false}, http.NewServeMux())
	if err := m.Start(); err != nil {
		t.Fatalf("Start() with Enabled=false should be a no-op, got: %v", err)
	}
	if m.Running() {
		t.Error("Running() should be false when I2P is disabled")
	}
	if m.Address() != "" {
		t.Errorf("Address() = %q; want empty when disabled", m.Address())
	}
	if m.Provider() != ProviderNone {
		t.Errorf("Provider() = %v; want ProviderNone when disabled", m.Provider())
	}
	m.Close()
}

func TestManager_StartEnabledNoHandler_NoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, Config{Enabled: true}, nil)
	if err := m.Start(); err != nil {
		t.Fatalf("Start() with nil handler should be a non-fatal no-op, got: %v", err)
	}
	if m.Running() {
		t.Error("Running() should be false when handler is nil")
	}
	m.Close()
}

func TestManager_StartEnabledNoProviderAvailable_NoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No i2pd binary configured/found in this test's PATH, and the SAM
	// address points at a port nothing is listening on: neither provider
	// resolves, so Start() must return cleanly (non-fatal) with I2P off.
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)

	m := NewManager(ctx, Config{
		Enabled:    true,
		Binary:     filepath.Join(tmp, "does-not-exist"),
		SAMAddress: "127.0.0.1:1",
	}, http.NewServeMux())

	if err := m.Start(); err != nil {
		t.Fatalf("Start() with no provider available should be a non-fatal no-op, got: %v", err)
	}
	if m.Running() {
		t.Error("Running() should be false when neither provider is available")
	}
	m.Close()
}

// ─── Manager: Close is idempotent and safe pre-Start ────────────────────────

func TestManager_CloseWithoutStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := NewManager(ctx, Config{Enabled: false}, http.NewServeMux())
	m.Close()
	m.Close() // must not panic on double-close
}
