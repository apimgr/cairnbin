package maintenance

// Internal tests for pgpManager helpers — path methods, writePrivateKey/readPrivateKey,
// mergeKeyservers, isRoot, lastPrivateExport/recordPrivateExport.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/database"
)

// ─── path helpers ────────────────────────────────────────────────────────────

func newTestManager(t *testing.T) *pgpManager {
	t.Helper()
	return &pgpManager{
		cfg:           config.DefaultConfig(),
		configDir:     "/config/test",
		installSecret: []byte("test-installation-secret-32bytes"),
	}
}

func TestPGPManager_SecurityDir(t *testing.T) {
	m := newTestManager(t)
	got := m.securityDir()
	want := "/config/test/security"
	if got != want {
		t.Errorf("securityDir = %q, want %q", got, want)
	}
}

func TestPGPManager_PubPath(t *testing.T) {
	m := newTestManager(t)
	got := m.pubPath()
	if !strings.HasSuffix(got, filepath.Join("security", "pgp.pub.asc")) {
		t.Errorf("pubPath = %q, does not end in security/pgp.pub.asc", got)
	}
}

func TestPGPManager_PrivPath(t *testing.T) {
	m := newTestManager(t)
	got := m.privPath()
	if !strings.HasSuffix(got, "pgp.priv.asc.enc") {
		t.Errorf("privPath = %q, does not end in pgp.priv.asc.enc", got)
	}
}

func TestPGPManager_RotatedPath(t *testing.T) {
	m := newTestManager(t)
	got := m.rotatedPath()
	if !strings.HasSuffix(got, "pgp.priv.asc.enc.old") {
		t.Errorf("rotatedPath = %q, does not end in pgp.priv.asc.enc.old", got)
	}
}

func TestPGPManager_KeyserversStatePath(t *testing.T) {
	m := newTestManager(t)
	got := m.keyserversStatePath()
	if !strings.HasSuffix(got, "keyservers.state") {
		t.Errorf("keyserversStatePath = %q, does not end in keyservers.state", got)
	}
}

func TestPGPManager_ExportStatePath(t *testing.T) {
	m := newTestManager(t)
	got := m.exportStatePath()
	if !strings.HasSuffix(got, "private_export.state") {
		t.Errorf("exportStatePath = %q, does not end in private_export.state", got)
	}
}

func TestPGPManager_PathsAllUnderSecurityDir(t *testing.T) {
	m := newTestManager(t)
	secDir := m.securityDir()
	paths := []string{
		m.pubPath(),
		m.privPath(),
		m.rotatedPath(),
		m.keyserversStatePath(),
		m.exportStatePath(),
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, secDir) {
			t.Errorf("path %q is not under securityDir %q", p, secDir)
		}
	}
}

// ─── writePrivateKey / readPrivateKey ─────────────────────────────────────────

func TestWriteReadPrivateKey_Roundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	m := &pgpManager{
		cfg:           config.DefaultConfig(),
		configDir:     tmpDir,
		installSecret: []byte("test-installation-secret-32bytes"),
	}
	armored := "-----BEGIN PGP PRIVATE KEY BLOCK-----\ntest content\n-----END PGP PRIVATE KEY BLOCK-----"
	if err := m.writePrivateKey(armored); err != nil {
		t.Fatalf("writePrivateKey: %v", err)
	}
	if _, err := os.Stat(m.privPath()); err != nil {
		t.Fatalf("private key file not created: %v", err)
	}
	got, err := m.readPrivateKey(m.privPath())
	if err != nil {
		t.Fatalf("readPrivateKey: %v", err)
	}
	if got != armored {
		t.Errorf("readPrivateKey = %q, want %q", got, armored)
	}
}

func TestReadPrivateKey_MissingFile(t *testing.T) {
	m := &pgpManager{
		configDir:     t.TempDir(),
		installSecret: []byte("test-installation-secret-32bytes"),
	}
	_, err := m.readPrivateKey("/no/such/file")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestWritePrivateKey_CreatesSecurityDir(t *testing.T) {
	tmpDir := t.TempDir()
	m := &pgpManager{
		cfg:           config.DefaultConfig(),
		configDir:     tmpDir,
		installSecret: []byte("any-secret"),
	}
	if err := m.writePrivateKey("test-content"); err != nil {
		t.Fatalf("writePrivateKey: %v", err)
	}
	if _, err := os.Stat(m.securityDir()); err != nil {
		t.Errorf("securityDir not created: %v", err)
	}
}

// ─── mergeKeyservers ──────────────────────────────────────────────────────────

func TestMergeKeyservers_FreshOverridesPrior(t *testing.T) {
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prior := []database.KeyserverPublish{
		{URL: "https://keys.openpgp.org", PublishedAt: old},
	}
	freshs := []database.KeyserverPublish{
		{URL: "https://keys.openpgp.org", PublishedAt: fresh},
	}
	result := mergeKeyservers(prior, freshs)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if !result[0].PublishedAt.Equal(fresh) {
		t.Error("fresh entry should override prior")
	}
}

func TestMergeKeyservers_UnionOfURLs(t *testing.T) {
	prior := []database.KeyserverPublish{
		{URL: "https://keyserver.ubuntu.com"},
	}
	fresh := []database.KeyserverPublish{
		{URL: "https://keys.openpgp.org"},
	}
	result := mergeKeyservers(prior, fresh)
	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
}

func TestMergeKeyservers_EmptyInputs(t *testing.T) {
	result := mergeKeyservers(nil, nil)
	if len(result) != 0 {
		t.Errorf("expected empty result for nil inputs, got %d", len(result))
	}
}

func TestMergeKeyservers_OnlyPrior(t *testing.T) {
	prior := []database.KeyserverPublish{
		{URL: "https://keys.openpgp.org"},
	}
	result := mergeKeyservers(prior, nil)
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
}

// ─── isRoot ───────────────────────────────────────────────────────────────────

func TestIsRoot_ReturnsBool(t *testing.T) {
	got := isRoot()
	want := os.Geteuid() == 0
	if got != want {
		t.Errorf("isRoot() = %v, want %v (euid=%d)", got, want, os.Geteuid())
	}
}

// ─── lastPrivateExport / recordPrivateExport ──────────────────────────────────

func TestRecordAndReadPrivateExport_Roundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	m := &pgpManager{configDir: tmpDir}

	_, ok := m.lastPrivateExport()
	if ok {
		t.Fatal("expected no export state initially")
	}
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	m.recordPrivateExport(now)

	got, ok := m.lastPrivateExport()
	if !ok {
		t.Fatal("expected export state after recording")
	}
	if !got.Equal(now) {
		t.Errorf("lastPrivateExport = %v, want %v", got, now)
	}
}

func TestLastPrivateExport_MissingFile(t *testing.T) {
	m := &pgpManager{configDir: t.TempDir()}
	_, ok := m.lastPrivateExport()
	if ok {
		t.Error("expected ok=false for missing state file")
	}
}

// ─── audit helper ────────────────────────────────────────────────────────────

func TestAudit_NilWriter_NoPanic(t *testing.T) {
	m := &pgpManager{auditW: nil}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("audit with nil writer panicked: %v", r)
		}
	}()
	m.audit(audit.Entry{Event: "pgp.test", Category: "security", Severity: "info"})
}

// ─── outboundIP ──────────────────────────────────────────────────────────────

func TestOutboundIP_ReturnsString(t *testing.T) {
	ip := outboundIP()
	if ip == "" {
		t.Skip("no network access to 1.1.1.1 in this environment")
	}
	if !strings.Contains(ip, ".") && !strings.Contains(ip, ":") {
		t.Errorf("outboundIP = %q, want dotted-decimal or IPv6 address", ip)
	}
}
