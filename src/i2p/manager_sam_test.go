package i2p

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// lenientTempDir returns a temp dir whose cleanup retries removal for a short
// while. A just-terminated fake i2pd child can still be creating files under
// the dir when t.TempDir's single RemoveAll runs, which makes that fail with
// "directory not empty".
func lenientTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "i2p-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if os.RemoveAll(dir) == nil || time.Now().After(deadline) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}

// fakeSAMListener starts a real TCP listener and drives every accepted
// connection through fakeSAMServer (defined in i2p_test.go), so
// startSAMLocked's net.DialTimeout-based flow can be exercised end-to-end
// (unlike net.Pipe, which samCreateSession's own tests already cover).
func fakeSAMListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go fakeSAMServer(t, conn)
		}
	}()
	return ln.Addr().String()
}

func baseSAMConfig(t *testing.T, samAddr string) Config {
	t.Helper()
	tmp := t.TempDir()
	return Config{
		Enabled:          true,
		Binary:           filepath.Join(tmp, "no-such-i2pd-binary"),
		SAMAddress:       samAddr,
		VirtualPort:      80,
		InboundLength:    3,
		OutboundLength:   3,
		InboundQuantity:  5,
		OutboundQuantity: 5,
		SignatureType:    7,
		BootstrapTimeout: 5 * time.Second,
		ConfigDir:        filepath.Join(tmp, "config"),
		DataDir:          filepath.Join(tmp, "data"),
		LogDir:           filepath.Join(tmp, "log"),
	}
}

func TestManager_StartSAM_Success(t *testing.T) {
	samAddr := fakeSAMListener(t)
	cfg := baseSAMConfig(t, samAddr)

	m := NewManager(context.Background(), cfg, http.NewServeMux())
	defer m.Close()

	if err := m.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}

	if !m.Running() {
		t.Fatal("expected Manager to be Running() after a successful SAM start")
	}
	if got := m.Provider(); got != ProviderSAM {
		t.Errorf("Provider() = %v; want ProviderSAM", got)
	}
	addr := m.Address()
	if addr == "" || !strings.HasSuffix(addr, ".b32.i2p") {
		t.Errorf("Address() = %q; want a .b32.i2p address", addr)
	}

	m.Close()
	if m.Running() {
		t.Error("expected Manager to not be Running() after Close()")
	}
	if got := m.Address(); got != "" {
		t.Errorf("Address() after Close() = %q; want empty", got)
	}
}

func TestManager_StartSAM_UnreachableBridge(t *testing.T) {
	// Nothing listening on this address, and no i2pd binary configured —
	// startLocked must fall through to the "neither provider available"
	// no-op path without returning an error.
	cfg := baseSAMConfig(t, "127.0.0.1:1")

	m := NewManager(context.Background(), cfg, http.NewServeMux())
	defer m.Close()

	if err := m.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if m.Running() {
		t.Error("expected Manager to not be Running() when no provider is reachable")
	}
}

func TestManager_Monitor_ExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cfg := baseSAMConfig(t, "127.0.0.1:1")
	m := NewManager(ctx, cfg, http.NewServeMux())

	done := make(chan struct{})
	go func() {
		m.Monitor()
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Monitor() did not exit after context cancellation")
	}
}

func TestManager_Monitor_RestartsDeadI2PdProcess(t *testing.T) {
	bin := fakeI2PdScriptWithDestination(t)
	tmp := lenientTempDir(t)
	cfg := Config{
		Enabled:          true,
		Binary:           bin,
		VirtualPort:      80,
		InboundLength:    3,
		OutboundLength:   3,
		InboundQuantity:  5,
		OutboundQuantity: 5,
		SignatureType:    7,
		BootstrapTimeout: 5 * time.Second,
		ConfigDir:        filepath.Join(tmp, "config"),
		DataDir:          filepath.Join(tmp, "data"),
		LogDir:           filepath.Join(tmp, "log"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	m := NewManager(ctx, cfg, http.NewServeMux())
	defer func() {
		cancel()
		m.Close()
	}()

	if err := m.Start(); err != nil {
		t.Fatalf("Start() returned error: %v", err)
	}
	if !m.Running() {
		t.Fatal("expected Manager to be Running() with the fake i2pd script")
	}

	// Kill the underlying process out from under the Manager, then let
	// Monitor's restart branch (svc.cmd != nil && !svc.cmd.Alive()) fire on
	// its own tick by invoking the same logic Monitor uses, directly —
	// Monitor's real ticker is 30s, too slow for a unit test.
	m.mu.Lock()
	svc := m.svc
	m.mu.Unlock()
	if svc == nil || svc.cmd == nil {
		t.Fatal("expected a running i2pd-backed service")
	}
	_ = svc.cmd.Close()
	if svc.cmd.Alive() {
		t.Fatal("expected fake i2pd process to be dead after Close()")
	}

	m.mu.Lock()
	if m.svc == svc {
		m.closeSvcLocked()
		if err := m.startLocked(); err != nil {
			t.Errorf("restart startLocked() error: %v", err)
		}
	}
	m.mu.Unlock()

	if !m.Running() {
		t.Error("expected Manager to be Running() again after simulated restart")
	}
}
