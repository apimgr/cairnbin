// Package task provides the bodies for the built-in PART 18 scheduler tasks.
// Each exported function returns a TaskFunc (func(ctx context.Context) error)
// ready to pass to scheduler.Register. The closures capture only the paths
// they need, so callers
// in main.go can construct them once and register them directly.
package task

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/apimgr/pastebin/src/audit"
	"github.com/apimgr/pastebin/src/maintenance"
	"github.com/apimgr/pastebin/src/updater"
)

// BackupRetention controls how many old backups to keep for each period.
type BackupRetention struct {
	// MaxBackups is the number of dated full backups to keep (≥1, default 1).
	MaxBackups int
	// KeepWeekly keeps Sunday backups; 0 = disabled.
	KeepWeekly int
	// KeepMonthly keeps 1st-of-month backups; 0 = disabled.
	KeepMonthly int
	// KeepYearly keeps Jan 1st backups; 0 = disabled.
	KeepYearly int
	// MaxTotalSize caps the combined size of retained dated backups: percent
	// of the backup volume ("10%") or an absolute size ("50G", "500MB");
	// falsey values disable the cap. Overrides the count limits (PART 21).
	MaxTotalSize string
}

// Mailer is the subset of email.Mailer used by task functions. Callers inject a
// concrete *email.Mailer so the task package stays free of direct email imports.
type Mailer interface {
	Enabled() bool
	Send(to, tmplName string, vars map[string]string) error
}

// BackupConfig is the configuration passed to BackupDaily and BackupHourly.
type BackupConfig struct {
	ProjectName string
	ConfigDir   string
	DataDir     string
	BackupDir   string
	AppVersion  string
	// Password is the AES-256-GCM encryption password; empty = no encryption.
	Password string
	// Compliance mirrors server.compliance.enabled (AI.md 28945-28989): when
	// true and Password is empty, scheduled backups are skipped (not run
	// unencrypted) and an audit warning is logged.
	Compliance bool
	// OperatorEmail is the admin recipient for backup_complete/backup_failed emails.
	// Empty or nil Mailer disables email for that task invocation.
	OperatorEmail string
	// Mailer sends backup outcome emails; nil = no email.
	Mailer Mailer
	// SendOnComplete controls whether backup_complete emails are sent (AI.md:26591).
	SendOnComplete bool
	// SendOnFailed controls whether backup_failed emails are sent (AI.md:26592).
	SendOnFailed bool
	Retention    BackupRetention
	// DiskThreshold is the disk-usage percentage above which backups are
	// skipped (PART 21); ≤0 uses the 90% default.
	DiskThreshold int
	// Audit records backup.skipped_disk_full events; nil = no audit logging.
	Audit *audit.Writer
}

// sslEmailThresholds are the days-before-expiry at which ssl_expiring emails
// are actually sent — 30/14-day warnings are log-only per the Operator
// Notifications matrix (AI.md:26824: "SSL expiring (30/14 days) | WARN | ✗").
var sslEmailThresholds = []int{7, 3, 1}

// SSLRenewalConfig carries optional email settings for the ssl_renewal task.
type SSLRenewalConfig struct {
	ConfigDir string
	FQDN      string
	// OperatorEmail is the admin recipient for ssl_expiring/ssl_renewed/
	// ssl_renewal_failed emails.
	OperatorEmail string
	// Mailer sends SSL-expiry emails; nil = no email.
	Mailer Mailer
	// SendExpiring controls whether ssl_expiring emails are sent (AI.md:26593).
	SendExpiring bool
	// SendRenewed controls whether ssl_renewed emails are sent (AI.md:26594).
	SendRenewed bool
	// SendRenewalFailed controls whether ssl_renewal_failed emails are sent
	// (AI.md:26827).
	SendRenewalFailed bool
}

// SSLRenewal returns a task that checks certificates in
// {configDir}/ssl/letsencrypt/{fqdn}/ and logs a warning for any cert that
// expires within 30 days. ssl_expiring emails are sent only at the urgent
// 7/3/1-day thresholds (30/14-day warnings are log-only per the Operator
// Notifications matrix, AI.md:26824-26827) when a Mailer and OperatorEmail
// are provided. Actual ACME renewal is delegated to autocert; this task
// provides visibility and alerting only.
func SSLRenewal(configDir, fqdn string) func(ctx context.Context) error {
	return SSLRenewalWithEmail(SSLRenewalConfig{ConfigDir: configDir, FQDN: fqdn})
}

// SSLRenewalWithEmail is the full SSLRenewal implementation that also sends
// ssl_expiring emails at the PART 17 thresholds (30/14/7/3/1 days).
func SSLRenewalWithEmail(cfg SSLRenewalConfig) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		certRoot := filepath.Join(cfg.ConfigDir, "ssl", "letsencrypt", cfg.FQDN)
		if _, err := os.Stat(certRoot); os.IsNotExist(err) {
			return nil
		}

		return filepath.WalkDir(certRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".pem" && ext != ".crt" {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				log.Printf("ssl_renewal: cannot read %s: %v", path, err)
				return nil
			}

			// Try to decode as DER directly; fall back to PEM.
			var certs []*x509.Certificate
			if cert, parseErr := x509.ParseCertificate(data); parseErr == nil {
				certs = append(certs, cert)
			} else {
				// crypto/tls can decode PEM chains.
				tlsCert, tlsErr := tls.X509KeyPair(data, data)
				if tlsErr == nil {
					for _, der := range tlsCert.Certificate {
						if c, e := x509.ParseCertificate(der); e == nil {
							certs = append(certs, c)
						}
					}
				}
			}

			for _, cert := range certs {
				remaining := time.Until(cert.NotAfter)
				remainingDays := int(remaining.Hours() / 24)

				if remaining < 30*24*time.Hour {
					log.Printf("ssl_renewal: WARNING — %s expires in %d day(s) (at %s)",
						path, remainingDays, cert.NotAfter.Format(time.RFC3339))
				} else {
					log.Printf("ssl_renewal: %s valid for %d day(s) (expires %s)",
						path, remainingDays, cert.NotAfter.Format("2006-01-02"))
				}

				stateFile := path + ".ssl_state.json"
				prevExpiry := sslLoadExpiry(stateFile)
				renewed := !prevExpiry.IsZero() && cert.NotAfter.Sub(prevExpiry) >= 24*time.Hour

				// Detect renewal: NotAfter advanced by ≥24h vs the stored state.
				if renewed && cfg.SendRenewed && cfg.Mailer != nil && cfg.Mailer.Enabled() && cfg.OperatorEmail != "" {
					if mailErr := cfg.Mailer.Send(cfg.OperatorEmail, "ssl_renewed", map[string]string{
						"fqdn":        cfg.FQDN,
						"valid_until": cert.NotAfter.Format("2006-01-02"),
					}); mailErr != nil {
						log.Printf("ssl_renewal: failed to send ssl_renewed email: %v", mailErr)
					}
				}

				// A cert that is still within the urgent window and has not
				// advanced since the last check has failed to auto-renew —
				// this supersedes the ssl_expiring notice for this run
				// (AI.md:26827 "SSL renewal failed | ERROR | ✓").
				renewalFailed := !renewed && !prevExpiry.IsZero() && remainingDays <= 7
				if renewalFailed && cfg.SendRenewalFailed && cfg.Mailer != nil && cfg.Mailer.Enabled() && cfg.OperatorEmail != "" {
					if mailErr := cfg.Mailer.Send(cfg.OperatorEmail, "ssl_renewal_failed", map[string]string{
						"fqdn":       cfg.FQDN,
						"error":      "certificate has not renewed automatically",
						"expires_in": fmt.Sprintf("%d", remainingDays),
						"next_retry": "next daily ssl_renewal check",
					}); mailErr != nil {
						log.Printf("ssl_renewal: failed to send ssl_renewal_failed email: %v", mailErr)
					}
				}

				// Persist current expiry for next run's renewal detection.
				sslSaveExpiry(stateFile, cert.NotAfter)

				// Send ssl_expiring email only at the urgent 7/3/1-day
				// thresholds; 30/14-day warnings are log-only
				// (AI.md:26824-26827). Suppressed when this run already
				// reported a renewal failure for the same certificate.
				if !renewalFailed && cfg.SendExpiring && cfg.Mailer != nil && cfg.Mailer.Enabled() && cfg.OperatorEmail != "" {
					for _, threshold := range sslEmailThresholds {
						if remainingDays <= threshold {
							if mailErr := cfg.Mailer.Send(cfg.OperatorEmail, "ssl_expiring", map[string]string{
								"fqdn":        cfg.FQDN,
								"expires_in":  fmt.Sprintf("%d", remainingDays),
								"expiry_date": cert.NotAfter.Format("2006-01-02"),
							}); mailErr != nil {
								log.Printf("ssl_renewal: failed to send ssl_expiring email: %v", mailErr)
							}
							break
						}
					}
				}
			}
			return nil
		})
	}
}

// BlocklistUpdate returns a task that ensures the blocklist directory exists.
// Any blocklist sources configured externally (e.g., ip-location-db, ipsum)
// should be placed in this directory by the operator or via a separate
// sidecar process.  This task creates the directory and logs its content count.
// Source identifies a remote security-data file to download. Name is the
// destination filename within the target directory; URL is the download source.
type Source struct {
	Name string
	URL  string
}

// BlocklistUpdate returns a task that refreshes the IP/domain blocklists in
// {dataDir}/security/blocklists/. Each configured source is downloaded
// atomically; a failed download is logged and the existing copy (if any) is
// kept — GeoIP/blocklist data is a risk signal, never a hard gate, so the task
// degrades gracefully (PART 18/19, AI.md:9135). With no sources configured the
// task only ensures the directory exists.
func BlocklistUpdate(dataDir string, sources ...Source) func(ctx context.Context) error {
	return securityFetchTask("blocklist_update",
		filepath.Join(dataDir, "security", "blocklists"), sources)
}

// CVEUpdate returns a task that refreshes the CVE/security databases in
// {dataDir}/security/cve/. Behaves identically to BlocklistUpdate: each
// configured source is downloaded atomically with graceful degradation, and
// with no sources configured the task only ensures the directory exists.
func CVEUpdate(dataDir string, sources ...Source) func(ctx context.Context) error {
	return securityFetchTask("cve_update",
		filepath.Join(dataDir, "security", "cve"), sources)
}

// securityFetchTask builds the common download-with-graceful-degradation body
// shared by BlocklistUpdate and CVEUpdate.
func securityFetchTask(name, dir string, sources []Source) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("%s: mkdir %s: %w", name, dir, err)
		}

		updated := 0
		for _, src := range sources {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if src.Name == "" || src.URL == "" {
				continue
			}
			dst := filepath.Join(dir, filepath.Base(src.Name))
			if err := downloadFile(ctx, src.URL, dst); err != nil {
				log.Printf("%s: %s: %v (keeping existing copy)", name, src.Name, err)
				continue
			}
			updated++
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("%s: readdir: %w", name, err)
		}

		if updated > 0 {
			if err := os.WriteFile(filepath.Join(dir, ".last_updated"),
				[]byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o640); err != nil {
				log.Printf("%s: write .last_updated: %v", name, err)
			}
		}

		log.Printf("%s: %s — %d source(s) updated, %d file(s) present", name, dir, updated, len(entries))
		return nil
	}
}

// downloadFile fetches url and writes the body to dst atomically (temp file in
// the same directory, then rename). An empty body or non-2xx status is treated
// as an error and dst is left untouched. ctx bounds the request so a scheduler
// shutdown aborts an in-flight download instead of holding the task's slot.
func downloadFile(ctx context.Context, url, dst string) error {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("get %s: status %d", url, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		tmp.Close()
		return fmt.Errorf("write body: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("empty response body")
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// LogRotator applies each log file's rotate/keep policy (server.logs).
// Implemented by *logging.Manager; declared here so the task package does
// not depend on the logging package directly.
type LogRotator interface {
	RotateCheck() error
}

// LogRotation returns the PART 18 log_rotation task. Size and age limits come
// from each log's rotate/keep policy in server.logs, applied by the rotator
// (the logging manager owns the per-file writers and their policies).
func LogRotation(rotator LogRotator) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if rotator == nil {
			return nil
		}
		if err := rotator.RotateCheck(); err != nil {
			return fmt.Errorf("log_rotation: %w", err)
		}
		return nil
	}
}

// BackupDaily returns a task that runs the PART 21 backup protocol:
//  1. Create a dated full backup using maintenance.Backup (with encryption if configured).
//  2. Verify the backup immediately (6 checks per spec).
//  3. Create/replace the rolling daily incremental ({project}-daily.tar.gz[.enc]).
//  4. Verify the daily incremental.
//  5. Apply retention policy (daily + optional weekly/monthly/yearly).
//
// If any verification step fails the failed file is deleted, existing backups
// are preserved, and an error is returned for the scheduler to log.
func BackupDaily(cfg BackupConfig) func(ctx context.Context) error {
	if cfg.Retention.MaxBackups < 1 {
		cfg.Retention.MaxBackups = 1
	}
	return func(ctx context.Context) error {
		if err := os.MkdirAll(cfg.BackupDir, 0o750); err != nil {
			return fmt.Errorf("backup_daily: mkdir: %w", err)
		}

		// Compliance-mode pre-check (AI.md 28945-28989 / M11): skip rather than
		// run an unencrypted backup when compliance mode requires a password.
		if skip, reason := compliancePreCheck(cfg); skip {
			log.Printf("backup_daily: skipped: %s", reason)
			auditBackupComplianceBlocked(cfg, reason)
			return nil
		}

		// Step 2 (PART 21): disk space pre-check — skip (without error) when
		// free space or disk usage make a new backup unsafe.
		if skip, reason := diskPreCheck(cfg); skip {
			log.Printf("backup_daily: skipped: %s", reason)
			auditBackupSkipped(cfg, reason)
			return nil
		}

		ext := ".tar.gz"
		if cfg.Password != "" {
			ext = ".tar.gz.enc"
		}

		// Step 1: full dated backup.
		date := time.Now().Format("2006-01-02")
		fullName := fmt.Sprintf("%s_backup_%s%s", cfg.ProjectName, date, ext)
		fullPath := filepath.Join(cfg.BackupDir, fullName)

		if err := maintenance.Backup(maintenance.BackupOptions{
			ConfigDir:         cfg.ConfigDir,
			DataDir:           cfg.DataDir,
			BackupDir:         cfg.BackupDir,
			AppVersion:        cfg.AppVersion,
			Password:          cfg.Password,
			Filename:          fullName,
			ComplianceEnabled: cfg.Compliance,
			Audit:             cfg.Audit,
		}); err != nil {
			os.Remove(fullPath)
			backupSendFailed(cfg, fullName, err.Error())
			auditBackupFailed(cfg, fullName, err.Error())
			return fmt.Errorf("backup_daily: create full backup: %w", err)
		}

		// Step 2: verify the full backup immediately.
		if err := maintenance.VerifyBackup(fullPath, cfg.Password); err != nil {
			os.Remove(fullPath)
			backupSendFailed(cfg, fullName, err.Error())
			auditBackupVerificationFailed(cfg, fullName, err.Error())
			return fmt.Errorf("backup_daily: verification failed for %s: %w", fullName, err)
		}

		info, _ := os.Stat(fullPath)
		sizeKB := int64(0)
		sizeBytes := int64(0)
		if info != nil {
			sizeBytes = info.Size()
			sizeKB = sizeBytes / 1024
		}
		log.Printf("backup_daily: full backup verified: %s (%d KB)", fullName, sizeKB)
		backupSendComplete(cfg, fullName, fmt.Sprintf("%d KB", sizeKB))
		auditBackupCreated(cfg, fullName, sizeBytes, cfg.Password != "")

		// Step 3: create/replace the daily incremental.
		dailyName := fmt.Sprintf("%s-daily%s", cfg.ProjectName, ext)
		dailyPath := filepath.Join(cfg.BackupDir, dailyName)
		dailyTmp := dailyPath + ".tmp"

		if err := maintenance.Backup(maintenance.BackupOptions{
			ConfigDir:         cfg.ConfigDir,
			DataDir:           cfg.DataDir,
			BackupDir:         cfg.BackupDir,
			AppVersion:        cfg.AppVersion,
			Password:          cfg.Password,
			Filename:          dailyName + ".tmp",
			ComplianceEnabled: cfg.Compliance,
			Audit:             cfg.Audit,
		}); err != nil {
			os.Remove(dailyTmp)
			log.Printf("backup_daily: daily incremental create warning: %v", err)
		} else {
			// Step 4: verify the daily incremental before replacing the existing one.
			if err := maintenance.VerifyBackup(dailyTmp, cfg.Password); err != nil {
				os.Remove(dailyTmp)
				log.Printf("backup_daily: daily incremental verification warning: %v", err)
			} else {
				if err := os.Rename(dailyTmp, dailyPath); err != nil {
					os.Remove(dailyTmp)
					log.Printf("backup_daily: daily incremental rename warning: %v", err)
				} else {
					log.Printf("backup_daily: daily incremental updated: %s", dailyName)
					auditBackupDailyUpdated(cfg, dailyName)
				}
			}
		}

		// Step 5: apply retention policy.
		if err := applyRetention(cfg); err != nil {
			log.Printf("backup_daily: retention warning: %v", err)
		}

		return nil
	}
}

// BackupHourly returns a task that replaces the rolling hourly incremental
// ({project}-hourly.tar.gz[.enc]) with a fresh snapshot, then verifies it.
func BackupHourly(cfg BackupConfig) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if err := os.MkdirAll(cfg.BackupDir, 0o750); err != nil {
			return fmt.Errorf("backup_hourly: mkdir: %w", err)
		}

		// Compliance-mode pre-check (AI.md 28945-28989 / M11): skip rather than
		// run an unencrypted backup when compliance mode requires a password.
		if skip, reason := compliancePreCheck(cfg); skip {
			log.Printf("backup_hourly: skipped: %s", reason)
			auditBackupComplianceBlocked(cfg, reason)
			return nil
		}

		if skip, reason := diskPreCheck(cfg); skip {
			log.Printf("backup_hourly: skipped: %s", reason)
			auditBackupSkipped(cfg, reason)
			return nil
		}

		ext := ".tar.gz"
		if cfg.Password != "" {
			ext = ".tar.gz.enc"
		}

		hourlyName := fmt.Sprintf("%s-hourly%s", cfg.ProjectName, ext)
		hourlyPath := filepath.Join(cfg.BackupDir, hourlyName)
		tmpName := hourlyName + ".tmp"
		tmpPath := filepath.Join(cfg.BackupDir, tmpName)

		if err := maintenance.Backup(maintenance.BackupOptions{
			ConfigDir:         cfg.ConfigDir,
			DataDir:           cfg.DataDir,
			BackupDir:         cfg.BackupDir,
			AppVersion:        cfg.AppVersion,
			Password:          cfg.Password,
			Filename:          tmpName,
			ComplianceEnabled: cfg.Compliance,
			Audit:             cfg.Audit,
		}); err != nil {
			os.Remove(tmpPath)
			backupSendFailed(cfg, hourlyName, err.Error())
			auditBackupFailed(cfg, hourlyName, err.Error())
			return fmt.Errorf("backup_hourly: create archive: %w", err)
		}

		if err := maintenance.VerifyBackup(tmpPath, cfg.Password); err != nil {
			os.Remove(tmpPath)
			backupSendFailed(cfg, hourlyName, err.Error())
			auditBackupVerificationFailed(cfg, hourlyName, err.Error())
			return fmt.Errorf("backup_hourly: verification failed: %w", err)
		}

		if err := os.Rename(tmpPath, hourlyPath); err != nil {
			os.Remove(tmpPath)
			backupSendFailed(cfg, hourlyName, err.Error())
			auditBackupFailed(cfg, hourlyName, err.Error())
			return fmt.Errorf("backup_hourly: rename: %w", err)
		}

		info, _ := os.Stat(hourlyPath)
		sizeBytes := int64(0)
		if info != nil {
			sizeBytes = info.Size()
		}
		log.Printf("backup_hourly: updated %s", hourlyName)
		auditBackupCreated(cfg, hourlyName, sizeBytes, cfg.Password != "")
		return nil
	}
}

// sslStateFile is the JSON structure persisted alongside each cert to track the
// last-seen NotAfter for renewal detection.
type sslStateFile struct {
	NotAfter time.Time `json:"not_after"`
}

// sslLoadExpiry reads the stored NotAfter for a cert state file. Returns zero
// time if the file is missing or unreadable (safe first-run behaviour).
func sslLoadExpiry(path string) time.Time {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	var s sslStateFile
	if err := json.Unmarshal(data, &s); err != nil {
		return time.Time{}
	}
	return s.NotAfter
}

// sslSaveExpiry persists the cert's NotAfter to path for next-run renewal
// detection. Errors are logged and ignored (non-fatal).
func sslSaveExpiry(path string, notAfter time.Time) {
	data, err := json.Marshal(sslStateFile{NotAfter: notAfter})
	if err != nil {
		log.Printf("ssl_renewal: failed to marshal state: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("ssl_renewal: failed to write state %s: %v", path, err)
	}
}

// backupSendComplete sends a backup_complete email when the backup succeeded and
// the BackupConfig has a Mailer, OperatorEmail, and SendOnComplete set (AI.md:26204).
func backupSendComplete(cfg BackupConfig, filename, size string) {
	if !cfg.SendOnComplete || cfg.Mailer == nil || !cfg.Mailer.Enabled() || cfg.OperatorEmail == "" {
		return
	}
	if err := cfg.Mailer.Send(cfg.OperatorEmail, "backup_complete", map[string]string{
		"filename": filename,
		"size":     size,
	}); err != nil {
		log.Printf("backup: failed to send backup_complete email: %v", err)
	}
}

// backupSendFailed sends a backup_failed email when the backup errored and the
// BackupConfig has a Mailer, OperatorEmail, and SendOnFailed set (AI.md:26205).
func backupSendFailed(cfg BackupConfig, filename, errMsg string) {
	if !cfg.SendOnFailed || cfg.Mailer == nil || !cfg.Mailer.Enabled() || cfg.OperatorEmail == "" {
		return
	}
	if err := cfg.Mailer.Send(cfg.OperatorEmail, "backup_failed", map[string]string{
		"filename": filename,
		"size":     "",
		"error":    errMsg,
	}); err != nil {
		log.Printf("backup: failed to send backup_failed email: %v", err)
	}
}

// RetentionSweep runs one full retention pass over the backup directory.
// Called once before the scheduler starts (startup step 16, AI.md:10595) to
// clear accumulation left behind by crashed or failed prior runs.
func RetentionSweep(cfg BackupConfig) error {
	if _, err := os.Stat(cfg.BackupDir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return applyRetention(cfg)
}

// backupFileRE matches both daily-full backup filenames
// (project_backup_2025-01-15.tar.gz[.enc]) and manual/timestamped backup
// filenames (project_backup_2025-01-15_143022.tar.gz[.enc]) — per AI.md
// PART 21, timestamped manual backups count toward max_backups alongside
// daily fulls, sorted by date, oldest deleted first; nothing matching the
// app's naming is exempt from pruning.
var backupFileRE = regexp.MustCompile(`_backup_(\d{4}-\d{2}-\d{2})(?:_\d{6})?\.tar\.gz(\.enc)?$`)

// applyRetention removes old backups according to the retention policy (PART 21).
// Priority order: yearly > monthly > weekly > daily.
// The daily incremental (*-daily.*) and hourly (*-hourly.*) are never deleted here.
// Emits backup.retention_cleanup (when cfg.Audit is set and at least one
// backup was removed) with the deleted filenames and the remaining count.
func applyRetention(cfg BackupConfig) error {
	dir, project, ret := cfg.BackupDir, cfg.ProjectName, cfg.Retention
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	// Collect dated full backup files.
	type backupEntry struct {
		name string
		date time.Time
	}
	var backups []backupEntry
	prefix := project + "_backup_"
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		m := backupFileRE.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		t, err := time.Parse("2006-01-02", m[1])
		if err != nil {
			continue
		}
		backups = append(backups, backupEntry{name: e.Name(), date: t})
	}

	// Sort newest first.
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].date.After(backups[j].date)
	})

	// Classify each backup by its highest-priority retention tier.
	type tier int
	const (
		// highest priority
		tierYearly  tier = iota
		tierMonthly tier = iota
		tierWeekly  tier = iota
		tierDaily   tier = iota
	)

	classify := func(t time.Time) tier {
		if t.Month() == time.January && t.Day() == 1 && ret.KeepYearly > 0 {
			return tierYearly
		}
		if t.Day() == 1 && ret.KeepMonthly > 0 {
			return tierMonthly
		}
		if t.Weekday() == time.Sunday && ret.KeepWeekly > 0 {
			return tierWeekly
		}
		return tierDaily
	}

	// Count how many of each tier we have seen (newest first).
	countYearly, countMonthly, countWeekly, countDaily := 0, 0, 0, 0
	var deleted []string
	for _, b := range backups {
		t := classify(b.date)
		keep := false
		switch t {
		case tierYearly:
			countYearly++
			keep = countYearly <= ret.KeepYearly
		case tierMonthly:
			countMonthly++
			keep = countMonthly <= ret.KeepMonthly
		case tierWeekly:
			countWeekly++
			keep = countWeekly <= ret.KeepWeekly
		case tierDaily:
			countDaily++
			keep = countDaily <= ret.MaxBackups
		}
		if !keep {
			p := filepath.Join(dir, b.name)
			if err := os.Remove(p); err != nil {
				log.Printf("backup: retention: remove %s: %v", b.name, err)
			} else {
				log.Printf("backup: retention: removed %s", b.name)
				deleted = append(deleted, b.name)
			}
		}
	}

	// PART 21 step 8: after count-based pruning, enforce the max_total_size
	// cap — it overrides all count limits.
	deleted = append(deleted, applySizeCap(dir, project, resolveSizeCap(dir, ret.MaxTotalSize))...)

	auditBackupRetentionCleanup(cfg, deleted, "retention policy", len(backups)-len(deleted))

	return nil
}

// TorHealth returns a task that checks whether Tor is running and restarts it
// when it is unhealthy (PART 18, restart_on_fail). torRunning reports the
// current Tor state and torRestart restarts the hidden service; both are
// injected from the server so the task package has no import cycle. torRestart
// may be nil, in which case an unhealthy service is only logged. When Tor is
// not installed, torRestart is a no-op and torRunning stays false — the task
// simply logs and never errors.
// UpdateCheck returns a task that checks for a newer release on the configured
// channel (PART 18/22, daily at 06:00, skippable). When autoInstall is false
// the task only logs and, if notifyAvailable is true, fires an update_available
// email. When autoInstall is true and an eligible release exists, the full
// --update yes flow runs; on success, if notifyInstalled is true, an
// update_installed email is sent before the process restarts. deferDays delays
// eligibility: a release must be at least that many days old before the task
// acts on it. A nil mailer or empty operatorEmail silently skips email.
func UpdateCheck(currentVersion, branch string, buildEpoch int64, operatorEmail string, autoInstall bool, deferDays int, notifyAvailable, notifyInstalled bool, mailer Mailer) func(ctx context.Context) error {
	// lastAnnounced remembers the version already announced to operators so the
	// WARN log and email are emitted once per version, not on every run.
	var lastAnnounced string
	return func(ctx context.Context) error {
		if branch == "" {
			branch = "stable"
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		// PART 22: the scheduled task selects the newest release that is both
		// newer than the running version and at least defer_days old.
		rel, err := updater.CheckForUpdateDeferred(checkCtx, currentVersion, branch, buildEpoch, deferDays)
		if err != nil {
			return fmt.Errorf("update_check: %w", err)
		}
		if rel == nil {
			log.Printf("update_check: %s is up to date on branch %s", currentVersion, branch)
			return nil
		}
		// PART 22: "update available" fires once per newly-seen eligible
		// version, not on every task run.
		announced := rel.TagName == lastAnnounced
		if rel.TagName == "devel" {
			announced = rel.TagName+"@"+rel.PublishedAt.UTC().Format(time.RFC3339) == lastAnnounced
		}
		if !announced {
			log.Printf("update_check: update available %s → %s (branch %s)", currentVersion, rel.TagName, branch)
			updateSendAvailable(mailer, operatorEmail, notifyAvailable, currentVersion, rel.TagName, branch)
			lastAnnounced = rel.TagName
			if rel.TagName == "devel" {
				lastAnnounced = rel.TagName + "@" + rel.PublishedAt.UTC().Format(time.RFC3339)
			}
		}
		if !autoInstall {
			return nil
		}
		log.Printf("update_check: auto_install enabled — installing %s", rel.TagName)
		installCtx, installCancel := context.WithTimeout(ctx, 5*time.Minute)
		defer installCancel()
		if err := updater.DoUpdate(installCtx, rel); err != nil {
			return fmt.Errorf("update_check: auto-install failed: %w", err)
		}
		log.Printf("update_check: installed %s — restarting", rel.TagName)
		updateSendInstalled(mailer, operatorEmail, notifyInstalled, currentVersion, rel.TagName, branch)
		if restartErr := updater.RestartSelf(); restartErr != nil {
			return fmt.Errorf("update_check: restart failed: %w", restartErr)
		}
		return nil
	}
}

// updateSendAvailable sends an update_available email when notify is true and
// the Mailer, OperatorEmail preconditions are met (PART 17/18).
func updateSendAvailable(mailer Mailer, operatorEmail string, notify bool, currentVersion, newVersion, branch string) {
	if !notify || mailer == nil || !mailer.Enabled() || operatorEmail == "" {
		return
	}
	if err := mailer.Send(operatorEmail, "update_available", map[string]string{
		"current_version": currentVersion,
		"new_version":     newVersion,
		"branch":          branch,
	}); err != nil {
		log.Printf("update_check: failed to send update_available email: %v", err)
	}
}

// updateSendInstalled sends an update_installed email when notify is true and
// the Mailer, OperatorEmail preconditions are met (PART 17/18).
func updateSendInstalled(mailer Mailer, operatorEmail string, notify bool, currentVersion, newVersion, branch string) {
	if !notify || mailer == nil || !mailer.Enabled() || operatorEmail == "" {
		return
	}
	if err := mailer.Send(operatorEmail, "update_installed", map[string]string{
		"current_version": currentVersion,
		"new_version":     newVersion,
		"branch":          branch,
	}); err != nil {
		log.Printf("update_check: failed to send update_installed email: %v", err)
	}
}

// I2PHealth checks the opt-in I2P eepsite every 10 minutes and restarts the
// provider when it is found down (PART 18). Like tor_health it never fails the
// scheduler run for a missing provider — I2P is best-effort by contract.
func I2PHealth(i2pRunning func() bool, i2pRestart func() error) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if i2pRunning == nil {
			return nil
		}
		if i2pRunning() {
			log.Printf("i2p_health: I2P eepsite is running")
			return nil
		}
		if i2pRestart == nil {
			log.Printf("i2p_health: I2P eepsite is not running (no restart configured)")
			return nil
		}
		log.Printf("i2p_health: I2P eepsite is not running — attempting restart")
		if err := i2pRestart(); err != nil {
			return fmt.Errorf("i2p_health: restart failed: %w", err)
		}
		if i2pRunning() {
			log.Printf("i2p_health: I2P eepsite restarted successfully")
		} else {
			log.Printf("i2p_health: I2P eepsite still not running after restart (no i2pd binary and no SAM bridge)")
		}
		return nil
	}
}

func TorHealth(torRunning func() bool, torRestart func() error) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if torRunning == nil {
			return nil
		}
		if torRunning() {
			log.Printf("tor_health: Tor hidden service is running")
			return nil
		}
		if torRestart == nil {
			log.Printf("tor_health: Tor hidden service is not running (no restart configured)")
			return nil
		}
		log.Printf("tor_health: Tor hidden service is not running — attempting restart")
		if err := torRestart(); err != nil {
			return fmt.Errorf("tor_health: restart failed: %w", err)
		}
		if torRunning() {
			log.Printf("tor_health: Tor hidden service restarted successfully")
		} else {
			log.Printf("tor_health: Tor hidden service still not running after restart (no Tor binary found or startup failed)")
		}
		return nil
	}
}
