package maintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeBackupTree creates a minimal config/data tree for Backup.
func writeBackupTree(t *testing.T) (cfgDir, dataDir, backupDir string) {
	t.Helper()
	root := t.TempDir()
	cfgDir = filepath.Join(root, "config")
	dataDir = filepath.Join(root, "data")
	backupDir = filepath.Join(dataDir, "backup")
	for _, d := range []string{cfgDir, filepath.Join(cfgDir, "ssl"), filepath.Join(dataDir, "db"), filepath.Join(dataDir, "uploads")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(cfgDir, "server.yml"):          "server:\n  mode: production\n",
		filepath.Join(cfgDir, "ssl", "cert.pem"):     "CERT",
		filepath.Join(dataDir, "db", "server.db"):    "SQLite format 3\x00" + strings.Repeat("x", 100),
		filepath.Join(dataDir, "uploads", "a.bin"):   "UPLOAD",
		filepath.Join(dataDir, "uploads", "zeros.bin"): strings.Repeat("\x00", 20000),
	}
	for p, c := range files {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfgDir, dataDir, backupDir
}

func TestBackupIncludeFlags_RoundTrip(t *testing.T) {
	cfgDir, dataDir, backupDir := writeBackupTree(t)
	err := Backup(BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: backupDir,
		Filename: "full.tar.gz", IncludeSSL: true, IncludeData: true,
	})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	archive := filepath.Join(backupDir, "full.tar.gz")

	cfg2 := filepath.Join(t.TempDir(), "config")
	data2 := filepath.Join(t.TempDir(), "data")
	if err := RestoreWithVersion(archive, cfg2, data2, "", ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, p := range []string{
		filepath.Join(cfg2, "ssl", "cert.pem"),
		filepath.Join(data2, "uploads", "a.bin"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected restored file %s: %v", p, err)
		}
	}
	// The backup dir itself must never be archived into data/.
	if _, err := os.Stat(filepath.Join(data2, "backup")); err == nil {
		t.Error("backup dir was archived into data/")
	}
}

func TestBackupWithoutIncludeFlags_OmitsSSLAndData(t *testing.T) {
	cfgDir, dataDir, backupDir := writeBackupTree(t)
	if err := Backup(BackupOptions{ConfigDir: cfgDir, DataDir: dataDir, BackupDir: backupDir, Filename: "min.tar.gz"}); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	cfg2 := filepath.Join(t.TempDir(), "config")
	data2 := filepath.Join(t.TempDir(), "data")
	if err := RestoreWithVersion(filepath.Join(backupDir, "min.tar.gz"), cfg2, data2, "", ""); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg2, "ssl")); err == nil {
		t.Error("ssl/ restored without --include-ssl")
	}
	if _, err := os.Stat(filepath.Join(data2, "uploads")); err == nil {
		t.Error("data/ restored without --include-data")
	}
}

func TestRestoreLimits_Rejections(t *testing.T) {
	cfgDir, dataDir, backupDir := writeBackupTree(t)
	if err := Backup(BackupOptions{ConfigDir: cfgDir, DataDir: dataDir, BackupDir: backupDir, Filename: "b.tar.gz", IncludeData: true}); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	archive := filepath.Join(backupDir, "b.tar.gz")
	cases := []struct {
		name   string
		mutate func(*RestoreLimits)
		want   string
	}{
		{"archive size", func(l *RestoreLimits) { l.MaxArchiveSize = 10 }, "max_archive_size"},
		{"file count", func(l *RestoreLimits) { l.MaxFiles = 1 }, "max_files"},
		{"file size", func(l *RestoreLimits) { l.MaxFileSize = 5 }, "max_file_size"},
		{"expanded size", func(l *RestoreLimits) { l.MaxExpandedSize = 20 }, "max_expanded_size"},
		{"ratio", func(l *RestoreLimits) { l.MaxRatio = 1; l.MaxExpandedSize = 0 }, "max_ratio"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := DefaultRestoreLimits()
			tc.mutate(&l)
			err := RestoreWithLimits(archive, filepath.Join(t.TempDir(), "c"), filepath.Join(t.TempDir(), "d"), "", "", l)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %s", err, tc.want)
			}
		})
	}
}

func TestDefaultRestoreLimits_MatchSpec(t *testing.T) {
	l := DefaultRestoreLimits()
	if l.MaxArchiveSize != 1<<30 || l.MaxExpandedSize != 4<<30 || l.MaxFiles != 10000 ||
		l.MaxFileSize != 512<<20 || l.MaxRatio != 100 {
		t.Errorf("defaults = %+v", l)
	}
}
