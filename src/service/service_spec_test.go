package service

import (
	"path/filepath"
	"testing"

	"github.com/apimgr/cairnbin/src/path"
)

// ─── reservedIDs: nobody/nogroup ─────────────────────────────────────────────

// TestReservedIDs_NobodyReserved verifies 65534 (nobody/nogroup) is reserved
// per the PART 23 Reserved/Well-Known UIDs table.
func TestReservedIDs_NobodyReserved(t *testing.T) {
	if !reservedIDs[65534] {
		t.Error("reservedIDs[65534] = false; 65534 (nobody/nogroup) must be reserved per PART 23")
	}
}

// ─── purgeDirs: paths-derived uninstall directory list ───────────────────────

// TestPurgeDirs_MatchesPathsPackage verifies purgeDirs() returns exactly the
// five PART 23 uninstall targets (config, data, cache, log, backup) derived
// from the paths package so BSD and macOS layouts are honored.
func TestPurgeDirs_MatchesPathsPackage(t *testing.T) {
	got := purgeDirs()

	want := []string{
		path.GetConfigDir(appName),
		path.GetDataDir(appName),
		path.GetCacheDir(appName),
		path.GetLogsDir(appName),
		path.GetBackupDir(appName),
	}

	if len(got) != len(want) {
		t.Fatalf("purgeDirs() returned %d dirs; want %d", len(got), len(want))
	}
	for i, dir := range want {
		if got[i] != dir {
			t.Errorf("purgeDirs()[%d] = %q; want %q", i, got[i], dir)
		}
	}
}

// TestPurgeDirs_IncludesBackupDir verifies the backup directory is part of the
// uninstall purge list (PART 23 step 4: remove {backup_dir}).
func TestPurgeDirs_IncludesBackupDir(t *testing.T) {
	backupDir := path.GetBackupDir(appName)

	for _, dir := range purgeDirs() {
		if dir == backupDir {
			return
		}
	}
	t.Errorf("purgeDirs() = %v; missing backup dir %q", purgeDirs(), backupDir)
}

// TestPurgeDirs_AbsolutePaths verifies every purge target is an absolute path
// so os.RemoveAll never operates relative to the working directory.
func TestPurgeDirs_AbsolutePaths(t *testing.T) {
	for _, dir := range purgeDirs() {
		if dir == "" || !filepath.IsAbs(dir) {
			t.Errorf("purgeDirs() contains non-absolute path %q", dir)
		}
	}
}

// ─── findAvailableMacOSSystemID ──────────────────────────────────────────────

// TestFindAvailableMacOSSystemID_RangeCheck verifies the returned ID falls in
// the PART 23 macOS safe range 200-399.
func TestFindAvailableMacOSSystemID_RangeCheck(t *testing.T) {
	id, err := findAvailableMacOSSystemID()
	if err != nil {
		t.Skipf("findAvailableMacOSSystemID: %v (all IDs may be taken)", err)
	}
	if id < 200 || id > 399 {
		t.Errorf("findAvailableMacOSSystemID() = %d; must be in range 200-399", id)
	}
}

// TestFindAvailableMacOSSystemID_NotReserved verifies the returned ID is never
// in the reserved/well-known UID list.
func TestFindAvailableMacOSSystemID_NotReserved(t *testing.T) {
	id, err := findAvailableMacOSSystemID()
	if err != nil {
		t.Skipf("findAvailableMacOSSystemID: %v", err)
	}
	if reservedIDs[id] {
		t.Errorf("findAvailableMacOSSystemID() = %d; should not be a reserved ID", id)
	}
}
