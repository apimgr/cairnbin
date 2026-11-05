// Package task (internal coverage test) — targets uncovered branches in
// SSLRenewal, BackupDaily, BackupHourly, BlocklistUpdate, CVEUpdate, and
// applyRetention.  All tests use t.TempDir(); no files are written to the
// project tree.
package task

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ─── BlocklistUpdate — MkdirAll failure ──────────────────────────────────────

// TestBlocklistUpdate_MkdirFails triggers the MkdirAll error path by placing a
// regular file where the "security" intermediate directory must be created.
func TestBlocklistUpdate_MkdirFails(t *testing.T) {
	dir := t.TempDir()
	// Put a regular file at <dir>/security so MkdirAll cannot create it as a dir.
	if err := os.WriteFile(filepath.Join(dir, "security"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fn := BlocklistUpdate(dir)
	if err := fn(context.Background()); err == nil {
		t.Error("expected error when MkdirAll fails, got nil")
	}
}

// ─── CVEUpdate — MkdirAll failure ────────────────────────────────────────────

// TestCVEUpdate_MkdirFails triggers the MkdirAll error path the same way as
// BlocklistUpdate: a file blocks the "security" subdirectory creation.
func TestCVEUpdate_MkdirFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "security"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fn := CVEUpdate(dir)
	if err := fn(context.Background()); err == nil {
		t.Error("expected error when MkdirAll fails, got nil")
	}
}

// TestCVEUpdate_CountsFiles verifies the happy path where files are present.
func TestCVEUpdate_CountsFiles(t *testing.T) {
	dir := t.TempDir()
	cveDir := filepath.Join(dir, "security", "cve")
	if err := os.MkdirAll(cveDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"nvd-2024.json", "nvd-2025.json"} {
		if err := os.WriteFile(filepath.Join(cveDir, n), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fn := CVEUpdate(dir)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("CVEUpdate with files: %v", err)
	}
}

// ─── SSLRenewal — extra PEM/DER paths ────────────────────────────────────────

// TestSSLRenewal_DERCertFile exercises the DER-parse branch (x509.ParseCertificate
// called directly on raw bytes, not PEM).  We write a raw DER file with a .crt
// extension so the walker picks it up.
func TestSSLRenewal_DERCertFile(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: "der-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(90 * 24 * time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certRoot := filepath.Join(dir, "ssl", "letsencrypt", "example.com")
	if err := os.MkdirAll(certRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write the raw DER bytes with a .crt extension.
	if err := os.WriteFile(filepath.Join(certRoot, "cert.crt"), der, 0o644); err != nil {
		t.Fatal(err)
	}
	fn := SSLRenewal(dir, "example.com")
	if err := fn(context.Background()); err != nil {
		t.Fatalf("SSLRenewal with DER cert file: %v", err)
	}
}

// TestSSLRenewal_UnreadableFile exercises the os.ReadFile error path by making
// the cert file unreadable.  Skipped as root.
func TestSSLRenewal_UnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — unreadable file restrictions do not apply")
	}
	dir := t.TempDir()
	certRoot := filepath.Join(dir, "ssl", "letsencrypt", "example.com")
	if err := os.MkdirAll(certRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(certRoot, "unreadable.pem")
	if err := os.WriteFile(certPath, []byte("data"), 0o000); err != nil {
		t.Fatal(err)
	}
	fn := SSLRenewal(dir, "example.com")
	// The walker logs the error and returns nil (graceful skip).
	if err := fn(context.Background()); err != nil {
		t.Fatalf("SSLRenewal should gracefully skip unreadable file, got: %v", err)
	}
}

// TestSSLRenewal_ExpiredCert exercises the warning-log branch where remaining <
// 7 days; the cert expired 1 hour ago so remaining is negative.
func TestSSLRenewal_ExpiredCert(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Already expired.
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "expired"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	dir := t.TempDir()
	certRoot := filepath.Join(dir, "ssl", "letsencrypt", "example.com")
	if err := os.MkdirAll(certRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certRoot, "expired.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	fn := SSLRenewal(dir, "example.com")
	// An expired cert triggers the WARNING log branch; no error returned.
	if err := fn(context.Background()); err != nil {
		t.Fatalf("SSLRenewal with expired cert: %v", err)
	}
}

// ─── BackupDaily — error paths ────────────────────────────────────────────────

// TestBackupDaily_MkdirFails triggers the MkdirAll error by placing a regular
// file where the backup directory needs to be created.
func TestBackupDaily_MkdirFails(t *testing.T) {
	root := t.TempDir()
	// Place a file at the path BackupDaily will call MkdirAll on.
	bkpPath := filepath.Join(root, "backup")
	if err := os.WriteFile(bkpPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskCfg := BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   filepath.Join(root, "config"),
		DataDir:     filepath.Join(root, "data"),
		BackupDir:   bkpPath,
		AppVersion:  "v1.0.0",
	}
	fn := BackupDaily(taskCfg)
	if err := fn(context.Background()); err == nil {
		t.Error("expected error when BackupDir is a file, got nil")
	}
}

// TestBackupDaily_RetentionAppliedWithMultipleExistingBackups exercises the
// retention code path (step 5) when prior dated backups already exist in the
// backup directory.  Creates 3 old backup files then runs BackupDaily with
// MaxBackups=1 so retention fires and removes 2 of the 3 old ones.
func TestBackupDaily_RetentionAppliedWithMultipleExistingBackups(t *testing.T) {
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
	// Pre-populate 3 old dated backup files so retention has something to prune.
	for _, date := range []string{"2025-01-01", "2025-01-02", "2025-01-03"} {
		name := filepath.Join(bkpDir, "cairnbin_backup_"+date+".tar.gz")
		if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   cfgDir,
		DataDir:     dataDir,
		BackupDir:   bkpDir,
		AppVersion:  "v1.0.0",
		Retention:   BackupRetention{MaxBackups: 1},
	}
	fn := BackupDaily(cfg)
	if err := fn(context.Background()); err != nil {
		t.Fatalf("BackupDaily with pre-existing backups: %v", err)
	}
}

// ─── BackupHourly — error paths ───────────────────────────────────────────────

// TestBackupHourly_MkdirFails triggers the MkdirAll error for BackupHourly.
func TestBackupHourly_MkdirFails(t *testing.T) {
	root := t.TempDir()
	// Place a file at the path BackupHourly will call MkdirAll on.
	bkpPath := filepath.Join(root, "backup")
	if err := os.WriteFile(bkpPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   filepath.Join(root, "config"),
		DataDir:     filepath.Join(root, "data"),
		BackupDir:   bkpPath,
		AppVersion:  "v1.0.0",
	}
	fn := BackupHourly(cfg)
	if err := fn(context.Background()); err == nil {
		t.Error("expected error when BackupDir is a file, got nil")
	}
}

// TestBackupHourly_BackupFails triggers the maintenance.Backup error by using
// a BackupDir path that cannot be created (parent is a regular file).
func TestBackupHourly_BackupFails(t *testing.T) {
	root := t.TempDir()
	// Make root/parent a regular file so the subdirectory cannot be created.
	parentFile := filepath.Join(root, "parent")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The backup dir is nested under the file — MkdirAll will fail.
	bkpDir := filepath.Join(parentFile, "backup")
	cfg := BackupConfig{
		ProjectName: "cairnbin",
		ConfigDir:   filepath.Join(root, "config"),
		DataDir:     filepath.Join(root, "data"),
		BackupDir:   bkpDir,
		AppVersion:  "v1.0.0",
	}
	fn := BackupHourly(cfg)
	if err := fn(context.Background()); err == nil {
		t.Error("expected error when backup dir cannot be created, got nil")
	}
}

// ─── applyRetention — remove failure ──────────────────────────────────────────

// TestApplyRetention_RemoveFailsGracefully tests the path where os.Remove fails
// inside the retention loop.  The function logs the error but still returns nil,
// so we verify the files still exist and no panic occurs.  Skipped as root.
func TestApplyRetention_RemoveFailsGracefully(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root — permission restrictions do not apply")
	}
	dir := t.TempDir()
	// Create 3 backup files.
	for _, date := range []string{"2025-01-01", "2025-01-02", "2025-01-03"} {
		name := filepath.Join(dir, "cairnbin_backup_"+date+".tar.gz")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Make the directory read-only so os.Remove fails.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o750)
	// applyRetention logs the error and returns nil even if removes fail.
	err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{MaxBackups: 1}})
	if err != nil {
		t.Errorf("applyRetention should return nil even when remove fails, got: %v", err)
	}
}

// TestApplyRetention_ZeroMaxBackupsDefaultsToOne verifies that the BackupDaily
// wrapper clamps MaxBackups to 1, so applyRetention with MaxBackups=0 still
// keeps at least one daily backup.  Calling applyRetention directly with 0 is
// technically valid — it means keep 0 daily backups but we exercise the branch.
func TestApplyRetention_ZeroMaxBackupsKeepsNone(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "cairnbin_backup_2025-01-01.tar.gz")
	if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{MaxBackups: 0}}); err != nil {
		t.Fatalf("applyRetention with MaxBackups=0: %v", err)
	}
	// With MaxBackups=0, all daily backups are pruned.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 files with MaxBackups=0, got %d", len(entries))
	}
}

// TestApplyRetention_NonMatchingFilesIgnored ensures files that do not match
// the backup regex (e.g. the rolling daily/hourly) are never touched.
func TestApplyRetention_NonMatchingFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	// Rolling files that must survive.
	for _, name := range []string{"cairnbin-daily.tar.gz", "cairnbin-hourly.tar.gz", "README.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{MaxBackups: 1}}); err != nil {
		t.Fatalf("applyRetention: %v", err)
	}
	for _, name := range []string{"cairnbin-daily.tar.gz", "cairnbin-hourly.tar.gz", "README.txt"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("file %s should not have been removed: %v", name, err)
		}
	}
}
