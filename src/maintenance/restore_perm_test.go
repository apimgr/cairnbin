package maintenance_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/apimgr/cairnbin/src/maintenance"
)

// TestRestore_FilePermissions verifies that every file extracted by Restore
// gets mode 0600 regardless of the mode recorded in the archive (PART 21) —
// including files whose on-disk source was world-readable before backup.
func TestRestore_FilePermissions(t *testing.T) {
	cfgDir, dataDir, bkpDir := makeTestDirs(t)

	// A deliberately loose-permission source file inside a backed-up dir: the
	// archive header will carry 0644, which Restore must NOT honor.
	tmplDir := filepath.Join(cfgDir, "template")
	if err := os.MkdirAll(tmplDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "loose.html"), []byte("world readable"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := maintenance.Backup(maintenance.BackupOptions{
		ConfigDir: cfgDir, DataDir: dataDir, BackupDir: bkpDir,
		AppVersion: "v1.0.0",
	}); err != nil {
		t.Fatalf("Backup error: %v", err)
	}
	entries, err := os.ReadDir(bkpDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no backup file created: %v", err)
	}
	bkpPath := filepath.Join(bkpDir, entries[0].Name())

	restCfg := t.TempDir()
	restData := t.TempDir()

	// Pre-create one destination file with loose permissions to prove Restore
	// also tightens files it overwrites.
	if err := os.MkdirAll(filepath.Join(restCfg, "template"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restCfg, "template", "loose.html"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := maintenance.Restore(bkpPath, restCfg, restData, ""); err != nil {
		t.Fatalf("Restore error: %v", err)
	}

	// Every file Restore wrote into the config tree must be mode 0600.
	checked := 0
	err = filepath.WalkDir(restCfg, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("restored file %s has mode %o, want 0600", path, got)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", restCfg, err)
	}
	// server.yml + template/loose.html at minimum.
	if checked < 2 {
		t.Fatalf("restore produced only %d file(s) to check, want at least 2", checked)
	}
}
