package maintenance_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/database"
	"github.com/apimgr/cairnbin/src/maintenance"
)

type backupCacheDB struct {
	database.DB
	snapshotDB database.DB
	syncErr    error
	syncCalls  int
}

func (db *backupCacheDB) SyncCache() error {
	db.syncCalls++
	return db.syncErr
}

func (db *backupCacheDB) SnapshotTo(path string) error {
	if err := db.SyncCache(); err != nil {
		return err
	}
	snapshotter, ok := db.snapshotDB.(interface{ SnapshotTo(string) error })
	if !ok {
		return errors.New("database snapshot is unavailable")
	}
	return snapshotter.SnapshotTo(path)
}

func (db *backupCacheDB) ExportAppSecrets() ([]database.AppSecretRecord, error) {
	exporter, ok := db.DB.(interface {
		ExportAppSecrets() ([]database.AppSecretRecord, error)
	})
	if !ok {
		return nil, errors.New("application secret export is unavailable")
	}
	return exporter.ExportAppSecrets()
}

// makeTestDirs creates temp config and data directories with dummy files.
func makeTestDirs(t *testing.T) (configDir, dataDir, backupDir string) {
	t.Helper()
	root := t.TempDir()
	configDir = filepath.Join(root, "config")
	dataDir = filepath.Join(root, "data")
	backupDir = filepath.Join(root, "backup")
	for _, d := range []string{
		configDir,
		filepath.Join(dataDir, "db"),
		backupDir,
	} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// Write a dummy server.yml.
	if err := os.WriteFile(filepath.Join(configDir, "server.yml"),
		[]byte("mode: production\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configDir, dataDir, backupDir
}

// ─── Backup + VerifyBackup ────────────────────────────────────────────────────

func TestBackupAndVerify_NoEncryption(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	opts := maintenance.BackupOptions{
		ConfigDir:  cfgDir,
		DataDir:    dataDir,
		BackupDir:  bkpDir,
		AppVersion: "v1.0.0",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup error: %v", err)
	}

	// Find the created backup file.
	entries, err := os.ReadDir(bkpDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no backup file created: %v", err)
	}
	bkpPath := filepath.Join(bkpDir, entries[0].Name())
	if got := maintenance.NewestFullBackup(bkpDir, "cairnbin"); got != bkpPath {
		t.Fatalf("default backup selection = %q, want generated archive %q", got, bkpPath)
	}

	if err := maintenance.VerifyBackup(bkpPath, ""); err != nil {
		t.Fatalf("VerifyBackup error: %v", err)
	}
}

func TestBackupRemoteDatabaseSelectsOnlyEnabledLocalCache(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cache      bool
		wantServer bool
	}{
		{name: "remote without cache", wantServer: false},
		{name: "remote with cache", cache: true, wantServer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir, dataDir, backupDir := makeTestDirs(t)
			stalePath := filepath.Join(dataDir, "db", "server.db")
			cachePath := filepath.Join(dataDir, "db", "cache.db")
			if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), []byte("server:\n  mode: production\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			db, err := database.NewDatabase("sqlite", filepath.Join(t.TempDir(), "export.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			backupDB := db
			var cacheDB *backupCacheDB
			if tc.cache {
				cacheSource, err := database.NewDatabase("sqlite", cachePath)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = cacheSource.Close() }()
				cacheDB = &backupCacheDB{DB: db, snapshotDB: cacheSource}
				backupDB = cacheDB
			}
			for _, path := range []string{stalePath} {
				localDB, err := database.NewDatabase("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				if err := localDB.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := maintenance.Backup(maintenance.BackupOptions{
				ConfigDir: cfgDir, DataDir: dataDir, BackupDir: backupDir,
				DatabaseDriver: "libsql", DatabaseCacheEnabled: tc.cache,
				Database: backupDB,
			}); err != nil {
				t.Fatal(err)
			}
			if tc.cache && cacheDB.syncCalls != 1 {
				t.Fatalf("remote cache sync calls = %d, want one", cacheDB.syncCalls)
			}
			archives, err := os.ReadDir(backupDir)
			if err != nil || len(archives) != 1 {
				t.Fatalf("archives=%v, err=%v", archives, err)
			}
			manifest, err := maintenance.ReadBackupManifest(filepath.Join(backupDir, archives[0].Name()), "")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			foundSecrets := false
			for _, file := range manifest.Files {
				found = found || file.Path == "server.db"
				foundSecrets = foundSecrets || file.Path == "app_secrets.json"
			}
			if found != tc.wantServer || !foundSecrets {
				t.Fatalf("manifest server.db=%v app_secrets.json=%v; want %v and true", found, foundSecrets, tc.wantServer)
			}
		})
	}
}

func TestBackupRefusesUnsynchronizedRemoteCache(t *testing.T) {
	cfgDir, dataDir, backupDir := makeTestDirs(t)
	baseDB, err := database.NewDatabase("sqlite", filepath.Join(t.TempDir(), "export.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = baseDB.Close() }()
	cacheDB := &backupCacheDB{DB: baseDB, syncErr: errors.New("remote unavailable")}
	err = maintenance.Backup(maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: backupDir,
		DatabaseDriver: "libsql", DatabaseCacheEnabled: true,
		Database: cacheDB,
	})
	if err == nil || !strings.Contains(err.Error(), "remote unavailable") {
		t.Fatalf("Backup error = %v, want cache synchronization failure", err)
	}
	entries, readErr := os.ReadDir(backupDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("backup files after failed synchronization = %v, read error = %v", entries, readErr)
	}
}

func TestRestoreIncrementalAppliesLimitsToBaseAndDeltaTogether(t *testing.T) {
	cfgDir, dataDir, backupDir := makeTestDirs(t)
	baseName := "cairnbin_backup_2026-11-25.tar.gz"
	basePath := filepath.Join(backupDir, baseName)
	if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), []byte(strings.Repeat("a", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Backup(maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: backupDir, Filename: baseName,
	}); err != nil {
		t.Fatalf("create full backup: %v", err)
	}
	baseManifest, err := maintenance.ReadBackupManifest(basePath, "")
	if err != nil {
		t.Fatalf("read full manifest: %v", err)
	}
	baseSHA, err := maintenance.FileSHA256(basePath)
	if err != nil {
		t.Fatalf("checksum full backup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), []byte(strings.Repeat("b", 300)), 0o600); err != nil {
		t.Fatal(err)
	}
	incrementalPath := filepath.Join(backupDir, "cairnbin-daily.tar.gz")
	if err := maintenance.Backup(maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: backupDir,
		Filename: filepath.Base(incrementalPath), BaseBackup: baseName,
		BaseManifest: baseManifest, BaseSHA256: baseSHA,
	}); err != nil {
		t.Fatalf("create incremental backup: %v", err)
	}
	configOut := filepath.Join(t.TempDir(), "config")
	dataOut := filepath.Join(t.TempDir(), "data")
	limits := maintenance.RestoreLimits{
		MaxArchiveSize: 1 << 20, MaxExpandedSize: 500, MaxFiles: 10,
		MaxFileSize: 400,
	}
	if err := maintenance.RestoreWithLimits(incrementalPath, configOut, dataOut, "", "", limits); err == nil {
		t.Fatal("restore accepted a base plus delta larger than the combined expanded-size limit")
	}
}

func TestBackupAndVerify_WithEncryption(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	opts := maintenance.BackupOptions{
		ConfigDir:  cfgDir,
		DataDir:    dataDir,
		BackupDir:  bkpDir,
		AppVersion: "v1.0.0",
		Password:   "s3cur3p@ssw0rd",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup error: %v", err)
	}

	entries, err := os.ReadDir(bkpDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no backup file created: %v", err)
	}
	bkpPath := filepath.Join(bkpDir, entries[0].Name())
	header, err := maintenance.ReadEncryptedBackupHeader(bkpPath)
	if err != nil {
		t.Fatalf("ReadEncryptedBackupHeader error: %v", err)
	}
	if header.Threads != 4 || header.Offset != 55 {
		t.Fatalf("BKUP1 header threads/offset = %d/%d, want 4/55", header.Threads, header.Offset)
	}
	if got := maintenance.BackupKeyIDForPassword(opts.Password, header); got != header.KeyID {
		t.Fatalf("backup key id = %q, header key id = %q", got, header.KeyID)
	}

	// Verify with correct password.
	if err := maintenance.VerifyBackup(bkpPath, "s3cur3p@ssw0rd"); err != nil {
		t.Fatalf("VerifyBackup (correct password) error: %v", err)
	}
}

func TestBackup_StagingFailurePreservesExistingBackup(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)
	name := "cairnbin_backup_2026-11-06.tar.gz"
	dest := filepath.Join(bkpDir, name)
	staged := dest + ".tmp"
	original := []byte("previous verified backup")
	if err := os.WriteFile(dest, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("another task owns this staging file"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := maintenance.Backup(maintenance.BackupOptions{
		ConfigDir: cfgDir,
		DataDir:   dataDir,
		BackupDir: bkpDir,
		Filename:  name,
	})
	if err == nil {
		t.Fatal("Backup succeeded despite an occupied staging path")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("existing backup changed after staging failure: %q", got)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("Backup removed a staging file it did not create: %v", err)
	}
}

// ─── Compliance-mode enforcement (M11, AI.md 28945-28989) ─────────────────────

func TestBackup_ComplianceMode(t *testing.T) {
	tests := []struct {
		name       string
		compliance bool
		password   string
		wantErr    bool
	}{
		{
			name:       "compliance enabled, no password: blocked",
			compliance: true,
			password:   "",
			wantErr:    true,
		},
		{
			name:       "compliance enabled, password set: succeeds",
			compliance: true,
			password:   "s3cur3p@ssw0rd",
			wantErr:    false,
		},
		{
			name:       "compliance disabled, no password: unaffected",
			compliance: false,
			password:   "",
			wantErr:    false,
		},
		{
			name:       "compliance disabled, password set: unaffected",
			compliance: false,
			password:   "s3cur3p@ssw0rd",
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgDir, dataDir, bkpDir := makeTestDirs(t)
			auditDir := t.TempDir()
			auditW := audit.New(audit.Config{
				Enabled: true,
				Dir:     auditDir,
				Events:  audit.EventCategories{Backup: true},
			})

			opts := maintenance.BackupOptions{
				ConfigDir:         cfgDir,
				DataDir:           dataDir,
				BackupDir:         bkpDir,
				AppVersion:        "v1.0.0",
				Password:          tt.password,
				ComplianceEnabled: tt.compliance,
				Audit:             auditW,
			}
			err := maintenance.Backup(opts)

			if tt.wantErr {
				if err == nil {
					t.Fatal("Backup: expected error, got nil")
				}
				if !errors.Is(err, maintenance.ErrComplianceBackupBlocked) {
					t.Fatalf("Backup error = %v, want wrapped ErrComplianceBackupBlocked", err)
				}
				logPath := filepath.Join(auditDir, "audit.log")
				data, rerr := os.ReadFile(logPath)
				if rerr != nil {
					t.Fatalf("audit log not written: %v", rerr)
				}
				if !strings.Contains(string(data), "backup.compliance_blocked") {
					t.Fatalf("audit log missing backup.compliance_blocked event: %s", data)
				}
				return
			}

			if err != nil {
				t.Fatalf("Backup error: %v", err)
			}
			entries, rerr := os.ReadDir(bkpDir)
			if rerr != nil || len(entries) == 0 {
				t.Fatalf("no backup file created: %v", rerr)
			}
		})
	}
}

func TestBackupAndVerify_WrongPassword(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	opts := maintenance.BackupOptions{
		ConfigDir:  cfgDir,
		DataDir:    dataDir,
		BackupDir:  bkpDir,
		AppVersion: "v1.0.0",
		Password:   "correct",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup error: %v", err)
	}

	entries, _ := os.ReadDir(bkpDir)
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	// Verify with wrong password should fail.
	if err := maintenance.VerifyBackup(bkpPath, "wrong"); err == nil {
		t.Error("expected error with wrong password, got nil")
	}
}

func TestBackupAndVerify_CustomFilename(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	opts := maintenance.BackupOptions{
		ConfigDir:  cfgDir,
		DataDir:    dataDir,
		BackupDir:  bkpDir,
		AppVersion: "v1.0.0",
		Filename:   "custom_backup.tar.gz",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup error: %v", err)
	}

	bkpPath := filepath.Join(bkpDir, "custom_backup.tar.gz")
	if _, err := os.Stat(bkpPath); err != nil {
		t.Fatalf("expected custom backup at %s: %v", bkpPath, err)
	}

	if err := maintenance.VerifyBackup(bkpPath, ""); err != nil {
		t.Fatalf("VerifyBackup error: %v", err)
	}
}

func TestBackup_RejectsTraversalFilename(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	for _, name := range []string{"../escape.tar.gz", "sub/dir.tar.gz", "..", "a..b/../c"} {
		err := maintenance.Backup(maintenance.BackupOptions{
			ConfigDir:  cfgDir,
			DataDir:    dataDir,
			BackupDir:  bkpDir,
			AppVersion: "v1.0.0",
			Filename:   name,
		})
		if err == nil {
			t.Errorf("Backup accepted filename %q, want error", name)
		}
	}
}

func TestNewestFullBackupUsesFilenameDateThenModificationTime(t *testing.T) {
	dir := t.TempDir()
	files := []struct {
		name string
		mod  time.Time
	}{
		{"cairnbin_backup_2026-01-03.tar.gz.enc", time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)},
		{"cairnbin_backup_2026-01-04.tar.gz", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"cairnbin_backup_2026-01-04.tar.gz.enc", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{"cairnbin_backup_2026-01-05_010203.tar.gz", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"cairnbin-daily.tar.gz", time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, file := range files {
		path := filepath.Join(dir, file.name)
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, file.mod, file.mod); err != nil {
			t.Fatal(err)
		}
	}
	want := filepath.Join(dir, "cairnbin_backup_2026-01-05_010203.tar.gz")
	if got := maintenance.NewestFullBackup(dir, "cairnbin"); got != want {
		t.Fatalf("NewestFullBackup = %q, want %q", got, want)
	}
}

func TestNewestFullBackupReturnsEmptyWhenDirectoryHasNoFull(t *testing.T) {
	if got := maintenance.NewestFullBackup(t.TempDir(), "cairnbin"); got != "" {
		t.Fatalf("NewestFullBackup = %q, want empty", got)
	}
}

func TestVerifyBackup_Nonexistent(t *testing.T) {
	if err := maintenance.VerifyBackup("/nonexistent/backup.tar.gz", ""); err == nil {
		t.Error("expected error for nonexistent file, got nil")
	}
}

func TestVerifyBackup_EncryptedRequiresPassword(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	opts := maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: bkpDir,
		AppVersion: "v1.0.0", Password: "secret",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup error: %v", err)
	}

	entries, _ := os.ReadDir(bkpDir)
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	// Verify without password on an .enc file should fail.
	if err := maintenance.VerifyBackup(bkpPath, ""); err == nil {
		t.Error("expected error verifying encrypted backup without password, got nil")
	}
}

// ─── Restore ─────────────────────────────────────────────────────────────────

func TestRestoreRoundTrip(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	opts := maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: bkpDir,
		AppVersion: "v1.0.0",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup error: %v", err)
	}
	entries, _ := os.ReadDir(bkpDir)
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	// Restore to new directories.
	restCfg := t.TempDir()
	restData := t.TempDir()
	if err := maintenance.Restore(bkpPath, restCfg, restData, ""); err != nil {
		t.Fatalf("Restore error: %v", err)
	}

	// server.yml should have been restored.
	if _, err := os.Stat(filepath.Join(restCfg, "server.yml")); err != nil {
		t.Errorf("server.yml not restored: %v", err)
	}
}

func TestBackupRestoresSecretsAndOperatorHTML(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)
	if err := os.WriteFile(filepath.Join(cfgDir, "secrets.yml"), []byte("server:\n  token: secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	htmlDir := filepath.Join(cfgDir, "html")
	if err := os.MkdirAll(htmlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(htmlDir, "about.md"), []byte("Operator page"), 0o600); err != nil {
		t.Fatal(err)
	}
	assetsDir := filepath.Join(cfgDir, "assets")
	if err := os.MkdirAll(assetsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "logo.svg"), []byte("<svg/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Backup(maintenance.BackupOptions{ConfigDir: cfgDir, DataDir: dataDir, BackupDir: bkpDir, AppVersion: "v1.0.0"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(bkpDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("backup entries: %v, error: %v", entries, err)
	}
	restoredConfig, restoredData := t.TempDir(), t.TempDir()
	if err := maintenance.Restore(filepath.Join(bkpDir, entries[0].Name()), restoredConfig, restoredData, ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(restoredConfig, "secrets.yml"), filepath.Join(restoredConfig, "html", "about.md"), filepath.Join(restoredConfig, "assets", "logo.svg")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("restored backup is missing %s: %v", path, err)
		}
	}
	secret, err := os.ReadFile(filepath.Join(restoredConfig, "secrets.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(secret), "secret-value") || !strings.Contains(string(secret), "token:") {
		t.Errorf("restore must preserve or generate a target server.token, got %q", secret)
	}
	if source, err := os.ReadFile(filepath.Join(cfgDir, "secrets.yml")); err != nil || !strings.Contains(string(source), "secret-value") {
		t.Errorf("backup modified source secrets: %q (%v)", source, err)
	}
}

func TestBackupIncludesReferencedTLSFilesAndRewritesExternalPaths(t *testing.T) {
	cfgDir, dataDir, backupDir := makeTestDirs(t)
	sslDir := filepath.Join(cfgDir, "ssl")
	if err := os.MkdirAll(sslDir, 0o700); err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(sslDir, "cache-ca.pem")
	if err := os.WriteFile(caPath, []byte("cache CA"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalDir := t.TempDir()
	certPath := filepath.Join(externalDir, "server-cert.pem")
	keyPath := filepath.Join(externalDir, "server-key.pem")
	if err := os.WriteFile(certPath, []byte("server certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("server private key"), 0o600); err != nil {
		t.Fatal(err)
	}
	serverConfig := []byte("server:\n  ssl:\n    cert: " + certPath + "\n    key: " + keyPath + "\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "server.yml"), serverConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.Backup(maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: backupDir,
		ConnectionTLSFiles: []string{caPath}, ExternalTLSCert: certPath, ExternalTLSKey: keyPath,
	}); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	archives, err := os.ReadDir(backupDir)
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives=%v, err=%v", archives, err)
	}
	configOut, dataOut := t.TempDir(), t.TempDir()
	if err := maintenance.Restore(filepath.Join(backupDir, archives[0].Name()), configOut, dataOut, ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, name := range []string{"cache-ca.pem", "external/cert.pem", "external/key.pem"} {
		if _, err := os.Stat(filepath.Join(configOut, "ssl", name)); err != nil {
			t.Errorf("restored TLS file %s: %v", name, err)
		}
	}
	restoredConfig, err := os.ReadFile(filepath.Join(configOut, "server.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(restoredConfig), certPath) || strings.Contains(string(restoredConfig), keyPath) || !strings.Contains(string(restoredConfig), "external/cert.pem") || !strings.Contains(string(restoredConfig), "external/key.pem") {
		t.Errorf("restored config did not rewrite external certificate paths: %s", restoredConfig)
	}
}

// ─── SetMode ─────────────────────────────────────────────────────────────────

func TestSetMode(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.yml")
	if err := os.WriteFile(cfgPath, []byte("mode: production\nport: 8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := maintenance.SetMode(dir, "development"); err != nil {
		t.Fatalf("SetMode error: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "mode: development") {
		t.Errorf("expected mode to be development, got: %s", string(data))
	}
}

func TestSetMode_NonexistentConfig(t *testing.T) {
	if err := maintenance.SetMode("/nonexistent/dir", "development"); err == nil {
		t.Error("expected error for nonexistent config dir, got nil")
	}
}

// ─── Setup ───────────────────────────────────────────────────────────────────

func TestSetup(t *testing.T) {
	dir := t.TempDir()
	if err := maintenance.Setup(dir); err != nil {
		t.Fatalf("Setup error: %v", err)
	}
}

// ─── PrintHelp ───────────────────────────────────────────────────────────────

func TestPrintHelp(t *testing.T) {
	// Should not panic.
	maintenance.PrintHelp("cairnbin")
}

func TestPrintHelpUsesRequestedLanguage(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	maintenance.PrintHelp("cairnbin", "es")
	_ = writer.Close()
	os.Stdout = previous
	output, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "Comandos de mantenimiento") || !strings.Contains(string(output), "Ejemplos") {
		t.Fatalf("Spanish maintenance help not localized: %q", output)
	}
}

// ─── VerifyBackup — SQLite magic ─────────────────────────────────────────────

// makeDirsWithDB creates config/data directories and a valid SQLite database.
func makeDirsWithDB(t *testing.T) (configDir, dataDir, backupDir string) {
	t.Helper()
	configDir, dataDir, backupDir = makeTestDirs(t)
	dbDir := filepath.Join(dataDir, "db")
	db, err := database.NewDatabase("sqlite", filepath.Join(dbDir, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return configDir, dataDir, backupDir
}

func TestBackupAndVerify_WithSQLiteDB(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeDirsWithDB(t)

	opts := maintenance.BackupOptions{
		ConfigDir:  cfgDir,
		DataDir:    dataDir,
		BackupDir:  bkpDir,
		AppVersion: "v1.0.0",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup error: %v", err)
	}

	entries, err := os.ReadDir(bkpDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no backup file created: %v", err)
	}
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	if err := maintenance.VerifyBackup(bkpPath, ""); err != nil {
		t.Fatalf("VerifyBackup with SQLite db: %v", err)
	}
}

func TestVerifyBackup_EmptyFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty.tar.gz")
	if err := os.WriteFile(p, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.VerifyBackup(p, ""); err == nil {
		t.Error("expected error for empty backup file, got nil")
	}
}

func TestVerifyBackup_NotGzip_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "not-gzip.tar.gz")
	if err := os.WriteFile(p, []byte("not-a-gzip-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.VerifyBackup(p, ""); err == nil {
		t.Error("expected error for non-gzip file, got nil")
	}
}

// ─── Backup — template and theme dirs ────────────────────────────────────────

func TestBackup_WithTemplateDir(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	// Add a template file so the addDir path is exercised.
	tmplDir := filepath.Join(cfgDir, "template")
	if err := os.MkdirAll(tmplDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "index.html"), []byte("<html></html>"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: bkpDir,
		AppVersion: "v1.0.0",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup with template dir: %v", err)
	}

	entries, _ := os.ReadDir(bkpDir)
	if len(entries) == 0 {
		t.Fatal("no backup created")
	}
	bkpPath := filepath.Join(bkpDir, entries[0].Name())
	if err := maintenance.VerifyBackup(bkpPath, ""); err != nil {
		t.Fatalf("VerifyBackup with template: %v", err)
	}
}

func TestRestore_WithTemplateFile(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	// Create template dir in source so backup includes template/ entries.
	tmplDir := filepath.Join(cfgDir, "template")
	if err := os.MkdirAll(tmplDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "email.html"), []byte("<b>hello</b>"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: bkpDir,
		AppVersion: "v1.0.0",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	entries, _ := os.ReadDir(bkpDir)
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	restCfg := t.TempDir()
	restData := t.TempDir()
	if err := maintenance.Restore(bkpPath, restCfg, restData, ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Template file should be present after restore.
	restoredTmpl := filepath.Join(restCfg, "template", "email.html")
	if _, err := os.Stat(restoredTmpl); err != nil {
		t.Errorf("restored template file not found: %v", err)
	}
}

func TestRestore_WithThemeDir(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	// Create a theme file so Restore exercises the theme/ extractEntry path.
	themeDir := filepath.Join(cfgDir, "theme")
	if err := os.MkdirAll(themeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(themeDir, "dark.css"), []byte("body{background:#000}"), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: bkpDir,
		AppVersion: "v1.0.0",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	entries, _ := os.ReadDir(bkpDir)
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	restCfg := t.TempDir()
	restData := t.TempDir()
	if err := maintenance.Restore(bkpPath, restCfg, restData, ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	restoredTheme := filepath.Join(restCfg, "theme", "dark.css")
	if _, err := os.Stat(restoredTheme); err != nil {
		t.Errorf("restored theme file not found: %v", err)
	}
}

func TestRestoreRoundTrip_WithEncryption(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	opts := maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: bkpDir,
		AppVersion: "v1.0.0", Password: "myp@ss",
	}
	if err := maintenance.Backup(opts); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	entries, _ := os.ReadDir(bkpDir)
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	restCfg := t.TempDir()
	restData := t.TempDir()
	if err := maintenance.Restore(bkpPath, restCfg, restData, "myp@ss"); err != nil {
		t.Fatalf("Restore encrypted: %v", err)
	}

	if _, err := os.Stat(filepath.Join(restCfg, "server.yml")); err != nil {
		t.Errorf("server.yml not restored from encrypted backup: %v", err)
	}
}

func TestRestore_NonexistentFile_ReturnsError(t *testing.T) {
	if err := maintenance.Restore("/nonexistent/backup.tar.gz", t.TempDir(), t.TempDir(), ""); err == nil {
		t.Error("expected error for nonexistent archive file, got nil")
	}
}
