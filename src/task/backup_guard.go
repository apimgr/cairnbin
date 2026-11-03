// backup_guard.go implements the PART 21 backup safety rails:
//   - the pre-backup disk space check (skip when free < 2× the last backup or
//     disk usage exceeds the configured threshold), and
//   - the max_total_size retention cap (delete oldest full backups until the
//     retained set fits under an absolute or percent-of-volume size cap).
package task

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/apimgr/pastebin/src/audit"
)

// defaultDiskThreshold is the disk-usage percentage above which backups are
// skipped when no threshold is configured (PART 21 default 90%).
const defaultDiskThreshold = 90

// latestBackupSize returns the size in bytes of the most recent dated full
// backup for project in dir, or 0 when none exists.
func latestBackupSize(dir, project string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	prefix := project + "_backup_"
	var newestDate string
	var newestSize int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		m := backupFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if m[1] < newestDate {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		newestDate = m[1]
		newestSize = info.Size()
	}
	return newestSize
}

// shouldSkipBackup applies the PART 21 pre-backup decision: skip when free
// space is below 2× the most recent backup size (when one exists) or when
// disk usage exceeds thresholdPercent (≤0 falls back to the 90% default).
// It returns the human-readable reason for the skip.
func shouldSkipBackup(freeBytes, totalBytes uint64, lastBackupBytes int64, thresholdPercent int) (bool, string) {
	if thresholdPercent <= 0 {
		thresholdPercent = defaultDiskThreshold
	}
	if lastBackupBytes > 0 && freeBytes < 2*uint64(lastBackupBytes) {
		return true, fmt.Sprintf("free space %d B is less than 2x the last backup (%d B)", freeBytes, lastBackupBytes)
	}
	if totalBytes > 0 {
		usedPercent := 100 - int(freeBytes*100/totalBytes)
		if usedPercent > thresholdPercent {
			return true, fmt.Sprintf("disk usage %d%% exceeds threshold %d%%", usedPercent, thresholdPercent)
		}
	}
	return false, ""
}

// diskPreCheck runs the disk space pre-check for the backup directory and
// reports whether the backup should be skipped. Statfs failures fail open
// (the backup proceeds) so a broken stat call never blocks backups.
func diskPreCheck(cfg BackupConfig) (skip bool, reason string) {
	free, total, err := diskStats(cfg.BackupDir)
	if err != nil {
		log.Printf("backup: disk pre-check unavailable (%v) — proceeding", err)
		return false, ""
	}
	return shouldSkipBackup(free, total, latestBackupSize(cfg.BackupDir, cfg.ProjectName), cfg.DiskThreshold)
}

// auditBackupSkipped records the backup.skipped_disk_full audit event
// (level=error per AI.md PART 21) with the free space, usage, and threshold.
func auditBackupSkipped(cfg BackupConfig, reason string) {
	if cfg.Audit == nil {
		return
	}
	threshold := cfg.DiskThreshold
	if threshold <= 0 {
		threshold = defaultDiskThreshold
	}
	details := map[string]any{
		"reason":       reason,
		"threshold":    threshold,
		"backup_dir":   cfg.BackupDir,
		"project_name": cfg.ProjectName,
	}
	if free, total, err := diskStats(cfg.BackupDir); err == nil {
		details["free_bytes"] = free
		if total > 0 {
			details["disk_usage_percent"] = 100 - int(free*100/total)
		}
	}
	cfg.Audit.Log(audit.Entry{
		Event:    "backup.skipped_disk_full",
		Severity: audit.SeverityError,
		Result:   audit.ResultFailure,
		Target:   &audit.Target{Type: "backup_dir", ID: cfg.BackupDir},
		Reason:   reason,
		Details:  details,
	})
}

// compliancePreCheck applies the PART 21 / M11 compliance-mode gate: when
// cfg.Compliance is on, a scheduled backup is skipped rather than run
// unencrypted (AI.md 28945-28989: "Scheduled backups skip with audit log
// warning"). It returns the human-readable reason for the skip.
func compliancePreCheck(cfg BackupConfig) (skip bool, reason string) {
	if cfg.Compliance && cfg.Password == "" {
		return true, "compliance mode requires an encryption password; scheduled backup skipped"
	}
	return false, ""
}

// auditBackupComplianceBlocked records the backup.compliance_blocked audit
// event (severity=warn per AI.md 28983-28989) documenting a scheduled backup
// that was skipped because compliance mode requires an encryption password.
func auditBackupComplianceBlocked(cfg BackupConfig, reason string) {
	if cfg.Audit == nil {
		return
	}
	cfg.Audit.Log(audit.Entry{
		Event:    "backup.compliance_blocked",
		Severity: audit.SeverityWarn,
		Result:   audit.ResultFailure,
		Target:   &audit.Target{Type: "backup_dir", ID: cfg.BackupDir},
		Reason:   reason,
		Details: map[string]any{
			"project_name": cfg.ProjectName,
		},
	})
}

// auditBackupCreated records the backup.created audit event once a backup
// archive has been written and verified (AI.md PART 11 audit-events table).
func auditBackupCreated(cfg BackupConfig, filename string, sizeBytes int64, encrypted bool) {
	if cfg.Audit == nil {
		return
	}
	cfg.Audit.Log(audit.Entry{
		Event:    "backup.created",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Target:   &audit.Target{Type: "backup_file", ID: filename},
		Details: map[string]any{
			"filename":            filename,
			"size_bytes":          sizeBytes,
			"encrypted":           encrypted,
			"verification_status": "passed",
		},
	})
}

// auditBackupFailed records the backup.failed audit event when a backup
// archive could not be created.
func auditBackupFailed(cfg BackupConfig, filename, errMsg string) {
	if cfg.Audit == nil {
		return
	}
	cfg.Audit.Log(audit.Entry{
		Event:    "backup.failed",
		Severity: audit.SeverityError,
		Result:   audit.ResultFailure,
		Target:   &audit.Target{Type: "backup_file", ID: filename},
		Reason:   errMsg,
		Details: map[string]any{
			"filename": filename,
			"error":    errMsg,
		},
	})
}

// auditBackupVerificationFailed records the backup.verification_failed audit
// event when a written backup archive fails post-write verification. This is
// distinct from backup.failed (create-step failure) per the AI.md PART 11
// audit-events table.
func auditBackupVerificationFailed(cfg BackupConfig, filename, check string) {
	if cfg.Audit == nil {
		return
	}
	cfg.Audit.Log(audit.Entry{
		Event:    "backup.verification_failed",
		Severity: audit.SeverityError,
		Result:   audit.ResultFailure,
		Target:   &audit.Target{Type: "backup_file", ID: filename},
		Reason:   check,
		Details: map[string]any{
			"filename": filename,
			"check":    check,
		},
	})
}

// auditBackupDailyUpdated records the backup.daily_updated audit event once
// the rolling daily incremental has been replaced.
func auditBackupDailyUpdated(cfg BackupConfig, filename string) {
	if cfg.Audit == nil {
		return
	}
	cfg.Audit.Log(audit.Entry{
		Event:    "backup.daily_updated",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Target:   &audit.Target{Type: "backup_file", ID: filename},
		Details: map[string]any{
			"filename": filename,
		},
	})
}

// auditBackupRetentionCleanup records the backup.retention_cleanup audit
// event when a retention pass removes one or more old backups.
func auditBackupRetentionCleanup(cfg BackupConfig, deleted []string, reason string, remaining int) {
	if cfg.Audit == nil || len(deleted) == 0 {
		return
	}
	for _, name := range deleted {
		cfg.Audit.Log(audit.Entry{
			Event:    "backup.deleted",
			Severity: audit.SeverityInfo,
			Result:   audit.ResultSuccess,
			Target:   &audit.Target{Type: "backup_file", ID: name},
			Reason:   reason,
			Details:  map[string]any{"filename": name},
		})
	}
	cfg.Audit.Log(audit.Entry{
		Event:    "backup.retention_cleanup",
		Severity: audit.SeverityInfo,
		Result:   audit.ResultSuccess,
		Target:   &audit.Target{Type: "backup_dir", ID: cfg.BackupDir},
		Reason:   reason,
		Details: map[string]any{
			"deleted_files": deleted,
			"remaining":     remaining,
		},
	})
}

// sizeUnits maps size suffixes to binary multipliers; longer suffixes are
// listed first so "mb" is matched before "b".
var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"tb", 1 << 40},
	{"gb", 1 << 30},
	{"mb", 1 << 20},
	{"kb", 1 << 10},
	{"t", 1 << 40},
	{"g", 1 << 30},
	{"m", 1 << 20},
	{"k", 1 << 10},
	{"b", 1},
}

// parseMaxTotalSize parses the backup.retention.max_total_size setting.
// Accepted forms: percent of the backup volume ("10%"), absolute size with a
// unit ("50G", "500MB", "1TB"), or a plain byte count. Falsey values ("",
// "0", "off", "false", "no", "none", "disable", "disabled") disable the cap.
func parseMaxTotalSize(s string) (percent float64, bytes int64, enabled bool, err error) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "", "0", "false", "no", "none", "disable", "disabled", "off":
		return 0, 0, false, nil
	}
	if strings.HasSuffix(v, "%") {
		p, perr := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(v, "%")), 64)
		if perr != nil {
			return 0, 0, false, fmt.Errorf("max_total_size: invalid percent %q: %w", s, perr)
		}
		if p <= 0 || p > 100 {
			return 0, 0, false, fmt.Errorf("max_total_size: percent %q out of range (0, 100]", s)
		}
		return p, 0, true, nil
	}
	for _, u := range sizeUnits {
		if !strings.HasSuffix(v, u.suffix) {
			continue
		}
		num := strings.TrimSpace(strings.TrimSuffix(v, u.suffix))
		n, perr := strconv.ParseFloat(num, 64)
		if perr != nil {
			return 0, 0, false, fmt.Errorf("max_total_size: invalid size %q: %w", s, perr)
		}
		if n <= 0 {
			return 0, 0, false, fmt.Errorf("max_total_size: size %q must be positive", s)
		}
		return 0, int64(n * float64(u.mult)), true, nil
	}
	n, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil || n <= 0 {
		return 0, 0, false, fmt.Errorf("max_total_size: unrecognized value %q", s)
	}
	return 0, n, true, nil
}

// resolveSizeCap converts a max_total_size setting into a byte cap for dir.
// Percent values are resolved against the backup volume's total size; when
// the volume size cannot be determined the cap fails open (0 = no cap).
func resolveSizeCap(dir, setting string) int64 {
	percent, bytes, enabled, err := parseMaxTotalSize(setting)
	if err != nil {
		log.Printf("backup: retention: %v — size cap disabled", err)
		return 0
	}
	if !enabled {
		return 0
	}
	if bytes > 0 {
		return bytes
	}
	_, total, derr := diskStats(dir)
	if derr != nil || total == 0 {
		log.Printf("backup: retention: cannot resolve %s of backup volume (%v) — size cap skipped", setting, derr)
		return 0
	}
	return int64(percent / 100 * float64(total))
}

// applySizeCap deletes the oldest retained dated full backups until their
// total size fits under capBytes (PART 21 step 8: the size cap overrides all
// count limits). The newest dated backup and the rolling incrementals are
// never deleted; if the set is still over the cap a warning is logged.
// Returns the filenames actually deleted, for audit accounting.
func applySizeCap(dir, project string, capBytes int64) []string {
	if capBytes <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("backup: retention: size cap read dir: %v", err)
		return nil
	}
	type backupFile struct {
		name string
		date string
		size int64
	}
	var files []backupFile
	var total int64
	prefix := project + "_backup_"
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		m := backupFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		files = append(files, backupFile{name: e.Name(), date: m[1], size: info.Size()})
		total += info.Size()
	}
	// Oldest first, so deletion walks from the oldest toward the newest.
	sort.Slice(files, func(i, j int) bool { return files[i].date < files[j].date })

	var deleted []string
	for i := 0; total > capBytes && i < len(files)-1; i++ {
		p := filepath.Join(dir, files[i].name)
		if err := os.Remove(p); err != nil {
			log.Printf("backup: retention: size cap remove %s: %v", files[i].name, err)
			continue
		}
		total -= files[i].size
		deleted = append(deleted, files[i].name)
		log.Printf("backup: retention: size cap removed %s (%d B)", files[i].name, files[i].size)
	}
	if total > capBytes {
		log.Printf("backup: retention: warning: backups still exceed max_total_size (%d B > %d B) after pruning", total, capBytes)
	}
	return deleted
}
