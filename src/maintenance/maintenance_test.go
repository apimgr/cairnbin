package maintenance_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/audit"
	"github.com/apimgr/cairnbin/src/maintenance"
)

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

	if err := maintenance.VerifyBackup(bkpPath, ""); err != nil {
		t.Fatalf("VerifyBackup error: %v", err)
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

	// Verify with correct password.
	if err := maintenance.VerifyBackup(bkpPath, "s3cur3p@ssw0rd"); err != nil {
		t.Fatalf("VerifyBackup (correct password) error: %v", err)
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

// ─── VerifyBackup — SQLite magic ─────────────────────────────────────────────

const sqliteMagic = "SQLite format 3\x00"

// makeDirsWithDB creates config/data directories and a minimal SQLite-magic db.
func makeDirsWithDB(t *testing.T) (configDir, dataDir, backupDir string) {
	t.Helper()
	configDir, dataDir, backupDir = makeTestDirs(t)
	dbDir := filepath.Join(dataDir, "db")
	// Write a server.db with the SQLite magic header (+ padding to meet ReadFull len).
	magic := sqliteMagic + strings.Repeat("\x00", 100)
	if err := os.WriteFile(filepath.Join(dbDir, "server.db"), []byte(magic), 0o600); err != nil {
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
