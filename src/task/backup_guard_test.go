// Tests for the PART 21 backup safety rails: max_total_size parsing, the
// size-cap pruning, and the disk space pre-check decision logic. Internal
// package so the unexported helpers can be exercised directly.
package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apimgr/cairnbin/src/audit"
)

func TestParseMaxTotalSize(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantPercent float64
		wantBytes   int64
		wantEnabled bool
		wantErr     bool
	}{
		{name: "empty disables", in: "", wantEnabled: false},
		{name: "zero disables", in: "0", wantEnabled: false},
		{name: "false disables", in: "false", wantEnabled: false},
		{name: "no disables", in: "no", wantEnabled: false},
		{name: "none disables", in: "none", wantEnabled: false},
		{name: "disable disables", in: "disable", wantEnabled: false},
		{name: "disabled disables", in: "disabled", wantEnabled: false},
		{name: "off disables", in: "off", wantEnabled: false},
		{name: "default percent", in: "10%", wantPercent: 10, wantEnabled: true},
		{name: "fractional percent", in: "2.5%", wantPercent: 2.5, wantEnabled: true},
		{name: "percent with spaces", in: "  25 %", wantPercent: 25, wantEnabled: true},
		{name: "percent too big", in: "150%", wantErr: true},
		{name: "percent zero", in: "0%", wantErr: true},
		{name: "percent negative", in: "-5%", wantErr: true},
		{name: "gigabytes short", in: "10G", wantBytes: 10 << 30, wantEnabled: true},
		{name: "gigabytes long", in: "10GB", wantBytes: 10 << 30, wantEnabled: true},
		{name: "megabytes", in: "500MB", wantBytes: 500 << 20, wantEnabled: true},
		{name: "terabytes", in: "1TB", wantBytes: 1 << 40, wantEnabled: true},
		{name: "kilobytes", in: "512k", wantBytes: 512 << 10, wantEnabled: true},
		{name: "lowercase gb", in: "2gb", wantBytes: 2 << 30, wantEnabled: true},
		{name: "fractional size", in: "1.5G", wantBytes: 3 << 29, wantEnabled: true},
		{name: "plain bytes", in: "1048576", wantBytes: 1 << 20, wantEnabled: true},
		{name: "explicit byte suffix", in: "2048B", wantBytes: 2048, wantEnabled: true},
		{name: "garbage", in: "banana", wantErr: true},
		{name: "negative size", in: "-1G", wantErr: true},
		{name: "negative plain", in: "-42", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			percent, bytes, enabled, err := parseMaxTotalSize(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseMaxTotalSize(%q): want error, got percent=%v bytes=%d enabled=%v", tc.in, percent, bytes, enabled)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMaxTotalSize(%q): unexpected error: %v", tc.in, err)
			}
			if enabled != tc.wantEnabled {
				t.Errorf("parseMaxTotalSize(%q): enabled = %v, want %v", tc.in, enabled, tc.wantEnabled)
			}
			if percent != tc.wantPercent {
				t.Errorf("parseMaxTotalSize(%q): percent = %v, want %v", tc.in, percent, tc.wantPercent)
			}
			if bytes != tc.wantBytes {
				t.Errorf("parseMaxTotalSize(%q): bytes = %d, want %d", tc.in, bytes, tc.wantBytes)
			}
		})
	}
}

// writeBackupFile creates a dated backup file of size bytes in dir.
func writeBackupFile(t *testing.T, dir, project, date string, size int) string {
	t.Helper()
	name := fmt.Sprintf("%s_backup_%s.tar.gz", project, date)
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestApplySizeCap(t *testing.T) {
	const project = "cairnbin"
	tests := []struct {
		name string
		// sizes maps date → file size.
		sizes    map[string]int
		capBytes int64
		// wantKept are the dates that must survive; all others must be gone.
		wantKept []string
	}{
		{
			name:     "cap disabled keeps everything",
			sizes:    map[string]int{"2026-01-01": 100, "2026-01-02": 100},
			capBytes: 0,
			wantKept: []string{"2026-01-01", "2026-01-02"},
		},
		{
			name:     "under cap keeps everything",
			sizes:    map[string]int{"2026-01-01": 100, "2026-01-02": 100},
			capBytes: 500,
			wantKept: []string{"2026-01-01", "2026-01-02"},
		},
		{
			name:     "oldest deleted first",
			sizes:    map[string]int{"2026-01-01": 100, "2026-01-02": 100, "2026-01-03": 100},
			capBytes: 250,
			wantKept: []string{"2026-01-02", "2026-01-03"},
		},
		{
			name:     "deletes until under cap",
			sizes:    map[string]int{"2026-01-01": 100, "2026-01-02": 100, "2026-01-03": 100, "2026-01-04": 100},
			capBytes: 150,
			wantKept: []string{"2026-01-04"},
		},
		{
			name:     "newest backup never deleted even over cap",
			sizes:    map[string]int{"2026-01-01": 100, "2026-01-02": 400},
			capBytes: 200,
			wantKept: []string{"2026-01-02"},
		},
		{
			name:     "single oversized backup survives",
			sizes:    map[string]int{"2026-01-05": 1000},
			capBytes: 100,
			wantKept: []string{"2026-01-05"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for date, size := range tc.sizes {
				writeBackupFile(t, dir, project, date, size)
			}
			// Rolling incrementals must always survive the size cap.
			for _, name := range []string{project + "-daily.tar.gz", project + "-hourly.tar.gz"} {
				if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 100), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			applySizeCap(dir, project, tc.capBytes)

			kept := map[string]bool{}
			for _, d := range tc.wantKept {
				kept[d] = true
			}
			for date := range tc.sizes {
				name := fmt.Sprintf("%s_backup_%s.tar.gz", project, date)
				_, err := os.Stat(filepath.Join(dir, name))
				if kept[date] && err != nil {
					t.Errorf("%s should have been kept: %v", name, err)
				}
				if !kept[date] && !os.IsNotExist(err) {
					t.Errorf("%s should have been deleted (err=%v)", name, err)
				}
			}
			for _, name := range []string{project + "-daily.tar.gz", project + "-hourly.tar.gz"} {
				if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
					t.Errorf("incremental %s must never be deleted by the size cap: %v", name, err)
				}
			}
		})
	}
}

func TestShouldSkipBackup(t *testing.T) {
	tests := []struct {
		name       string
		free       uint64
		total      uint64
		lastBackup int64
		threshold  int
		wantSkip   bool
	}{
		{name: "plenty of space", free: 500, total: 1000, lastBackup: 100, threshold: 90, wantSkip: false},
		{name: "free below 2x last backup", free: 150, total: 1000, lastBackup: 100, threshold: 90, wantSkip: true},
		{name: "free exactly 2x last backup", free: 200, total: 1000, lastBackup: 100, threshold: 90, wantSkip: false},
		{name: "no previous backup skips 2x rule", free: 300, total: 1000, lastBackup: 0, threshold: 90, wantSkip: false},
		{name: "usage over threshold", free: 50, total: 1000, lastBackup: 0, threshold: 90, wantSkip: true},
		{name: "usage at threshold ok", free: 100, total: 1000, lastBackup: 0, threshold: 90, wantSkip: false},
		{name: "default threshold when zero", free: 50, total: 1000, lastBackup: 0, threshold: 0, wantSkip: true},
		{name: "default threshold when negative", free: 200, total: 1000, lastBackup: 0, threshold: -5, wantSkip: false},
		{name: "custom low threshold", free: 400, total: 1000, lastBackup: 0, threshold: 50, wantSkip: true},
		{name: "zero total skips usage check", free: 300, total: 0, lastBackup: 100, threshold: 90, wantSkip: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			skip, reason := shouldSkipBackup(tc.free, tc.total, tc.lastBackup, tc.threshold)
			if skip != tc.wantSkip {
				t.Errorf("shouldSkipBackup(free=%d,total=%d,last=%d,thr=%d) = %v (%q), want %v",
					tc.free, tc.total, tc.lastBackup, tc.threshold, skip, reason, tc.wantSkip)
			}
			if skip && reason == "" {
				t.Error("skip decision must carry a reason")
			}
		})
	}
}

func TestLatestBackupSize(t *testing.T) {
	const project = "cairnbin"
	t.Run("empty dir returns zero", func(t *testing.T) {
		if got := latestBackupSize(t.TempDir(), project); got != 0 {
			t.Errorf("latestBackupSize = %d, want 0", got)
		}
	})
	t.Run("missing dir returns zero", func(t *testing.T) {
		if got := latestBackupSize(filepath.Join(t.TempDir(), "nope"), project); got != 0 {
			t.Errorf("latestBackupSize = %d, want 0", got)
		}
	})
	t.Run("picks newest dated backup", func(t *testing.T) {
		dir := t.TempDir()
		writeBackupFile(t, dir, project, "2026-01-01", 100)
		writeBackupFile(t, dir, project, "2026-03-01", 300)
		writeBackupFile(t, dir, project, "2026-02-01", 200)
		// Incrementals and foreign files are ignored.
		if err := os.WriteFile(filepath.Join(dir, project+"-daily.tar.gz"), make([]byte, 999), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := latestBackupSize(dir, project); got != 300 {
			t.Errorf("latestBackupSize = %d, want 300", got)
		}
	})
}

// TestCompliancePreCheck covers the M11 / AI.md 28945-28989 scheduler gate:
// scheduled backups are skipped (never run unencrypted) when compliance mode
// is on and no encryption password is configured.
func TestCompliancePreCheck(t *testing.T) {
	tests := []struct {
		name       string
		compliance bool
		password   string
		wantSkip   bool
	}{
		{name: "compliance enabled, no password: skipped", compliance: true, password: "", wantSkip: true},
		{name: "compliance enabled, password set: not skipped", compliance: true, password: "s3cur3p@ss", wantSkip: false},
		{name: "compliance disabled, no password: not skipped", compliance: false, password: "", wantSkip: false},
		{name: "compliance disabled, password set: not skipped", compliance: false, password: "s3cur3p@ss", wantSkip: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := BackupConfig{Compliance: tc.compliance, Password: tc.password}
			skip, reason := compliancePreCheck(cfg)
			if skip != tc.wantSkip {
				t.Errorf("compliancePreCheck(compliance=%v,password=%q) = %v, want %v",
					tc.compliance, tc.password, skip, tc.wantSkip)
			}
			if skip && reason == "" {
				t.Error("skip decision must carry a reason")
			}
		})
	}
}

// TestAuditBackupComplianceBlocked verifies the backup.compliance_blocked
// audit event is recorded with severity=warn (AI.md 28983-28989) and is a
// no-op when no Audit writer is configured.
func TestAuditBackupComplianceBlocked(t *testing.T) {
	t.Run("nil audit writer is a no-op", func(t *testing.T) {
		cfg := BackupConfig{BackupDir: t.TempDir(), ProjectName: "cairnbin"}
		auditBackupComplianceBlocked(cfg, "compliance mode requires an encryption password; scheduled backup skipped")
	})

	t.Run("writes backup.compliance_blocked entry", func(t *testing.T) {
		auditDir := t.TempDir()
		w := audit.New(audit.Config{
			Enabled: true,
			Dir:     auditDir,
			Events:  audit.EventCategories{Backup: true},
		})
		cfg := BackupConfig{
			BackupDir:   t.TempDir(),
			ProjectName: "cairnbin",
			Audit:       w,
		}
		auditBackupComplianceBlocked(cfg, "compliance mode requires an encryption password; scheduled backup skipped")

		data, err := os.ReadFile(filepath.Join(auditDir, "audit.log"))
		if err != nil {
			t.Fatalf("audit log not written: %v", err)
		}
		if !strings.Contains(string(data), "backup.compliance_blocked") {
			t.Fatalf("audit log missing backup.compliance_blocked event: %s", data)
		}
		if !strings.Contains(string(data), `"severity":"warn"`) {
			t.Fatalf("audit log entry missing severity=warn: %s", data)
		}
	})
}

func TestResolveSizeCap_Absolute(t *testing.T) {
	if got := resolveSizeCap(t.TempDir(), "500MB"); got != 500<<20 {
		t.Errorf("resolveSizeCap(500MB) = %d, want %d", got, int64(500<<20))
	}
	if got := resolveSizeCap(t.TempDir(), "off"); got != 0 {
		t.Errorf("resolveSizeCap(off) = %d, want 0", got)
	}
	if got := resolveSizeCap(t.TempDir(), "banana"); got != 0 {
		t.Errorf("resolveSizeCap(banana) = %d, want 0 (fail-open)", got)
	}
}

// TestAuditBackupRetentionCleanupEmitsDeleted verifies each removed backup is
// recorded as backup.deleted alongside the summary backup.retention_cleanup.
func TestAuditBackupRetentionCleanupEmitsDeleted(t *testing.T) {
	auditDir := t.TempDir()
	w := audit.New(audit.Config{
		Enabled: true,
		Dir:     auditDir,
		Events:  audit.EventCategories{Backup: true},
	})
	cfg := BackupConfig{BackupDir: t.TempDir(), ProjectName: "cairnbin", Audit: w}
	auditBackupRetentionCleanup(cfg, []string{"a.tar.gz", "b.tar.gz"}, "retention policy", 1)

	data, err := os.ReadFile(filepath.Join(auditDir, "audit.log"))
	if err != nil {
		t.Fatalf("audit log not written: %v", err)
	}
	if n := strings.Count(string(data), `"event":"backup.deleted"`); n != 2 {
		t.Fatalf("want 2 backup.deleted entries, got %d: %s", n, data)
	}
	if !strings.Contains(string(data), "backup.retention_cleanup") {
		t.Fatalf("missing backup.retention_cleanup: %s", data)
	}
}
