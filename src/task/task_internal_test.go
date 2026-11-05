package task

import (
	"os"
	"path/filepath"
	"testing"
)

// ─── applyRetention ───────────────────────────────────────────────────────────

func TestApplyRetention_KeepsMaxBackups(t *testing.T) {
	dir := t.TempDir()

	// Create 5 daily backup files (newest first when named by date).
	dates := []string{
		"2025-01-05",
		"2025-01-04",
		"2025-01-03",
		"2025-01-02",
		"2025-01-01",
	}
	for _, d := range dates {
		name := "cairnbin_backup_" + d + ".tar.gz"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Keep only 2 most recent.
	if err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{MaxBackups: 2}}); err != nil {
		t.Fatalf("applyRetention error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 files remaining, got %d", len(entries))
	}
}

func TestApplyRetention_KeepsWeekly(t *testing.T) {
	dir := t.TempDir()

	// 2025-01-05 is a Sunday; 2025-01-06 is a Monday.
	dates := []string{"2025-01-06", "2025-01-05"}
	for _, d := range dates {
		name := "cairnbin_backup_" + d + ".tar.gz"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// MaxBackups=1 would keep only the newest daily (Mon 01-06).
	// KeepWeekly=1 also retains the Sunday (01-05).
	if err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{
		MaxBackups: 1,
		KeepWeekly: 1,
	}}); err != nil {
		t.Fatalf("applyRetention error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Both should survive: newest daily + Sunday weekly.
	if len(entries) != 2 {
		t.Errorf("expected 2 files remaining (newest+Sunday), got %d", len(entries))
	}
}

func TestApplyRetention_YearlyKept(t *testing.T) {
	dir := t.TempDir()

	// January 1st should be kept as yearly.
	jan1 := "2025-01-01"
	jan2 := "2025-01-02"
	for _, d := range []string{jan1, jan2} {
		name := "cairnbin_backup_" + d + ".tar.gz"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// MaxBackups=1 keeps newest daily (jan2). KeepYearly=1 keeps jan1 as yearly.
	if err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{
		MaxBackups: 1,
		KeepYearly: 1,
	}}); err != nil {
		t.Fatalf("applyRetention error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 files (newest daily + jan1 yearly), got %d", len(entries))
	}
}

func TestApplyRetention_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{MaxBackups: 3}}); err != nil {
		t.Fatalf("unexpected error on empty dir: %v", err)
	}
}

func TestApplyRetention_InvalidDir(t *testing.T) {
	err := applyRetention(BackupConfig{BackupDir: "/nonexistent/path/xxx", ProjectName: "cairnbin", Retention: BackupRetention{MaxBackups: 3}})
	if err == nil {
		t.Error("expected error for nonexistent dir, got nil")
	}
}

// ─── backupFileRE ─────────────────────────────────────────────────────────────

func TestApplyRetention_KeepsMonthly(t *testing.T) {
	dir := t.TempDir()

	// 2025-02-01 is 1st of the month; 2025-02-02 is not.
	dates := []string{"2025-02-02", "2025-02-01"}
	for _, d := range dates {
		name := "cairnbin_backup_" + d + ".tar.gz"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// MaxBackups=1 keeps newest daily (Feb 2). KeepMonthly=1 also retains Feb 1st.
	if err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{
		MaxBackups:  1,
		KeepMonthly: 1,
	}}); err != nil {
		t.Fatalf("applyRetention error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 files (newest daily + Feb 1st monthly), got %d", len(entries))
	}
}

func TestApplyRetention_ExceedsAllTiers(t *testing.T) {
	dir := t.TempDir()

	// Create many backups: some yearly, some monthly, some weekly, many daily.
	dates := []string{
		// yearly + monthly + possible weekly
		"2024-01-01",
		// monthly
		"2024-12-01",
		// Sunday (weekly)
		"2025-01-05",
		"2025-01-06",
		"2025-01-07",
		"2025-01-08",
	}
	for _, d := range dates {
		name := "cairnbin_backup_" + d + ".tar.gz"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := applyRetention(BackupConfig{BackupDir: dir, ProjectName: "cairnbin", Retention: BackupRetention{
		MaxBackups:  1,
		KeepWeekly:  1,
		KeepMonthly: 1,
		KeepYearly:  1,
	}}); err != nil {
		t.Fatalf("applyRetention error: %v", err)
	}

	// Verify the dir can still be read (no panic).
	if _, err := os.ReadDir(dir); err != nil {
		t.Fatal(err)
	}
}

func TestBackupFileRE(t *testing.T) {
	cases := []struct {
		name  string
		match bool
	}{
		{"cairnbin_backup_2025-01-15.tar.gz", true},
		{"cairnbin_backup_2025-01-15.tar.gz.enc", true},
		{"cairnbin_backup_20250115.tar.gz", false},
		{"cairnbin-daily.tar.gz", false},
		{"cairnbin-hourly.tar.gz.enc", false},
		{"other_backup_2025-01-15.tar.gz", true},
	}
	for _, tc := range cases {
		got := backupFileRE.MatchString(tc.name)
		if got != tc.match {
			t.Errorf("%s: match=%v want=%v", tc.name, got, tc.match)
		}
	}
}
