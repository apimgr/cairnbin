package task_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/task"
)

// ─── BlocklistUpdate ──────────────────────────────────────────────────────────

func TestBlocklistUpdate_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	fn := task.BlocklistUpdate(dir)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BlocklistUpdate error: %v", err)
	}
	expected := filepath.Join(dir, "security", "blocklists")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected directory %s to exist: %v", expected, err)
	}
}

func TestBlocklistUpdate_CountsFiles(t *testing.T) {
	dir := t.TempDir()
	blDir := filepath.Join(dir, "security", "blocklists")
	if err := os.MkdirAll(blDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Create 2 dummy files.
	for _, n := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(blDir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fn := task.BlocklistUpdate(dir)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BlocklistUpdate error: %v", err)
	}
}

// ─── CVEUpdate ────────────────────────────────────────────────────────────────

func TestCVEUpdate_CreatesDir(t *testing.T) {
	dir := t.TempDir()
	fn := task.CVEUpdate(dir)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("CVEUpdate error: %v", err)
	}
	expected := filepath.Join(dir, "security", "cve")
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("expected directory %s to exist: %v", expected, err)
	}
}

// ─── LogRotation ─────────────────────────────────────────────────────────────

// stubRotator records RotateCheck calls and returns a canned error.
type stubRotator struct {
	calls int
	err   error
}

func (s *stubRotator) RotateCheck() error {
	s.calls++
	return s.err
}

func TestLogRotation_CallsRotateCheck(t *testing.T) {
	rot := &stubRotator{}
	fn := task.LogRotation(rot)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("LogRotation error: %v", err)
	}
	if rot.calls != 1 {
		t.Errorf("RotateCheck calls = %d, want 1", rot.calls)
	}
}

func TestLogRotation_WrapsError(t *testing.T) {
	rot := &stubRotator{err: errors.New("disk full")}
	fn := task.LogRotation(rot)
	err := fn(context.Background())
	if err == nil {
		t.Fatal("expected error from LogRotation, got nil")
	}
	if !errors.Is(err, rot.err) {
		t.Errorf("error %v should wrap %v", err, rot.err)
	}
	if !strings.Contains(err.Error(), "log_rotation:") {
		t.Errorf("error %q should be prefixed with log_rotation:", err)
	}
}

func TestLogRotation_NilRotator(t *testing.T) {
	fn := task.LogRotation(nil)
	if err := fn(context.Background()); err != nil {
		t.Errorf("LogRotation(nil) should be a no-op, got %v", err)
	}
}

// ─── SSLRenewal ───────────────────────────────────────────────────────────────

func TestSSLRenewal_NoDir(t *testing.T) {
	// When certRoot does not exist, SSLRenewal returns nil (graceful no-op).
	dir := t.TempDir()
	fn := task.SSLRenewal(dir, "example.com")
	if err := fn(context.Background()); err != nil {
		t.Fatalf("SSLRenewal with no cert dir should return nil, got: %v", err)
	}
}

func TestSSLRenewal_WithNonCertFile(t *testing.T) {
	// certRoot exists but contains only a file with an unrecognized extension —
	// SSLRenewal should skip it and return nil.
	dir := t.TempDir()
	certRoot := filepath.Join(dir, "ssl", "letsencrypt", "example.com")
	if err := os.MkdirAll(certRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certRoot, "config.yaml"), []byte("key: value"), 0o644); err != nil {
		t.Fatal(err)
	}

	fn := task.SSLRenewal(dir, "example.com")
	if err := fn(context.Background()); err != nil {
		t.Fatalf("SSLRenewal with non-cert file: %v", err)
	}
}

func TestSSLRenewal_WithInvalidPEMFile(t *testing.T) {
	// A .pem file that does not contain a valid certificate — the parser gracefully
	// skips it (neither DER nor PEM cert found) and returns nil.
	dir := t.TempDir()
	certRoot := filepath.Join(dir, "ssl", "letsencrypt", "example.com")
	if err := os.MkdirAll(certRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certRoot, "invalid.pem"), []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}

	fn := task.SSLRenewal(dir, "example.com")
	if err := fn(context.Background()); err != nil {
		t.Fatalf("SSLRenewal with invalid pem: %v", err)
	}
}

// selfSignedCertPEM generates a minimal self-signed PEM cert valid for `dur`.
func selfSignedCertPEM(t *testing.T, dur time.Duration) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(dur),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestSSLRenewal_WithValidCert(t *testing.T) {
	// A .pem cert expiring in 30 days — SSLRenewal should log "valid for" (no warning).
	dir := t.TempDir()
	certRoot := filepath.Join(dir, "ssl", "letsencrypt", "example.com")
	if err := os.MkdirAll(certRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	certPEM := selfSignedCertPEM(t, 30*24*time.Hour)
	if err := os.WriteFile(filepath.Join(certRoot, "fullchain.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	fn := task.SSLRenewal(dir, "example.com")
	if err := fn(context.Background()); err != nil {
		t.Fatalf("SSLRenewal with valid cert: %v", err)
	}
}

func TestSSLRenewal_WithExpiringSoonCert(t *testing.T) {
	// A .pem cert expiring in 3 days — SSLRenewal should log a WARNING.
	dir := t.TempDir()
	certRoot := filepath.Join(dir, "ssl", "letsencrypt", "example.com")
	if err := os.MkdirAll(certRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	certPEM := selfSignedCertPEM(t, 3*24*time.Hour)
	if err := os.WriteFile(filepath.Join(certRoot, "expiring.crt"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	fn := task.SSLRenewal(dir, "example.com")
	if err := fn(context.Background()); err != nil {
		t.Fatalf("SSLRenewal with expiring cert: %v", err)
	}
}

// ─── TorHealth ────────────────────────────────────────────────────────────────

func TestTorHealth_NilFunc(t *testing.T) {
	fn := task.TorHealth(nil, nil)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("TorHealth(nil) should return nil, got: %v", err)
	}
}

func TestTorHealth_Running(t *testing.T) {
	fn := task.TorHealth(func() bool { return true }, nil)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("TorHealth running=true should return nil, got: %v", err)
	}
}

func TestTorHealth_NotRunning_NoRestart(t *testing.T) {
	fn := task.TorHealth(func() bool { return false }, nil)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("TorHealth running=false should return nil, got: %v", err)
	}
}

func TestTorHealth_RestartRecovers(t *testing.T) {
	running := false
	restarted := false
	fn := task.TorHealth(
		func() bool { return running },
		func() error { restarted = true; running = true; return nil },
	)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("TorHealth restart should return nil, got: %v", err)
	}
	if !restarted {
		t.Error("expected restart to be attempted when Tor was down")
	}
}

func TestTorHealth_RestartStillDown(t *testing.T) {
	fn := task.TorHealth(
		func() bool { return false },
		func() error { return nil },
	)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("TorHealth should not error when restart no-ops, got: %v", err)
	}
}

func TestTorHealth_RestartError(t *testing.T) {
	fn := task.TorHealth(
		func() bool { return false },
		func() error { return errTorRestart },
	)
	if err := fn(context.Background()); err == nil {
		t.Fatal("expected error when restart fails")
	}
}

var errTorRestart = &torRestartError{}

type torRestartError struct{}

func (*torRestartError) Error() string { return "restart boom" }

// ─── BackupDaily ──────────────────────────────────────────────────────────────

func TestBackupDaily_CreatesBackup(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	bkpDir := filepath.Join(root, "backup")
	for _, d := range []string{cfgDir, filepath.Join(dataDir, "db"), bkpDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), []byte("mode: production\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := task.BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   cfgDir,
		DataDir:     dataDir,
		BackupDir:   bkpDir,
		AppVersion:  "v1.0.0",
		Retention:   task.BackupRetention{MaxBackups: 3},
	}
	fn := task.BackupDaily(cfg)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BackupDaily error: %v", err)
	}

	entries, err := os.ReadDir(bkpDir)
	if err != nil {
		t.Fatal(err)
	}
	// Expect at least one dated backup file.
	if len(entries) == 0 {
		t.Error("BackupDaily: no backup files created")
	}
}

func TestBackupDaily_WithEncryption(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	bkpDir := filepath.Join(root, "backup")
	for _, d := range []string{cfgDir, filepath.Join(dataDir, "db"), bkpDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), []byte("mode: production\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := task.BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   cfgDir,
		DataDir:     dataDir,
		BackupDir:   bkpDir,
		AppVersion:  "v1.0.0",
		Password:    "supersecret",
		Retention:   task.BackupRetention{MaxBackups: 2},
	}
	fn := task.BackupDaily(cfg)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BackupDaily with encryption error: %v", err)
	}

	entries, err := os.ReadDir(bkpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Error("BackupDaily with encryption: no backup files created")
	}
}

func TestBackupDaily_WithRetentionAndMultipleRuns(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	bkpDir := filepath.Join(root, "backup")
	for _, d := range []string{cfgDir, filepath.Join(dataDir, "db"), bkpDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), []byte("mode: production\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := task.BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   cfgDir,
		DataDir:     dataDir,
		BackupDir:   bkpDir,
		AppVersion:  "v1.0.0",
		Retention: task.BackupRetention{
			MaxBackups:  1,
			KeepWeekly:  1,
			KeepMonthly: 1,
			KeepYearly:  1,
		},
	}
	fn := task.BackupDaily(cfg)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BackupDaily with full retention error: %v", err)
	}
}

// ─── BackupHourly ─────────────────────────────────────────────────────────────

func TestBackupHourly_CreatesRollingBackup(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	bkpDir := filepath.Join(root, "backup")
	for _, d := range []string{cfgDir, filepath.Join(dataDir, "db"), bkpDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), []byte("mode: production\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := task.BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   cfgDir,
		DataDir:     dataDir,
		BackupDir:   bkpDir,
		AppVersion:  "v1.0.0",
	}
	fn := task.BackupHourly(cfg)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BackupHourly error: %v", err)
	}

	// The hourly backup file should exist.
	hourlyPath := filepath.Join(bkpDir, "cairnbin-hourly.tar.gz")
	if _, err := os.Stat(hourlyPath); err != nil {
		t.Errorf("expected hourly backup at %s: %v", hourlyPath, err)
	}
}

func TestBackupHourly_WithEncryption(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "config")
	dataDir := filepath.Join(root, "data")
	bkpDir := filepath.Join(root, "backup")
	for _, d := range []string{cfgDir, filepath.Join(dataDir, "db"), bkpDir} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), []byte("mode: production\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := task.BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   cfgDir,
		DataDir:     dataDir,
		BackupDir:   bkpDir,
		AppVersion:  "v1.0.0",
		Password:    "secretpass",
	}
	fn := task.BackupHourly(cfg)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BackupHourly with encryption error: %v", err)
	}

	hourlyPath := filepath.Join(bkpDir, "cairnbin-hourly.tar.gz.enc")
	if _, err := os.Stat(hourlyPath); err != nil {
		t.Errorf("expected encrypted hourly backup at %s: %v", hourlyPath, err)
	}
}
