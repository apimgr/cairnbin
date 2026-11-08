package server

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apimgr/cairnbin/src/common/email"
	"github.com/apimgr/cairnbin/src/common/i18n"
	"github.com/apimgr/cairnbin/src/health"
)

// criticalCheck probes the two critical subsystems defined in PART 20: the
// database connection and the ability to write files. It returns ok=true when
// both are healthy, or a reason code and message describing the fault.
func (s *Server) criticalCheck() (bool, string, string) {
	if err := s.db.Ping(); err != nil {
		return false, health.ReasonDatabaseConnection, "Database connection failed"
	}
	if !s.checkDisk() {
		return false, health.ReasonFileWrite, "Insufficient disk space for writes"
	}
	return true, "", ""
}

// maintenanceMiddleware rejects write operations with HTTP 503 while the server
// is in maintenance mode (PART 20). Reads, health checks, and the metrics
// endpoint continue to pass through so operators retain visibility.
func (s *Server) maintenanceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.maintenance == nil || !s.maintenance.InMaintenance() {
			next.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			next.ServeHTTP(w, r)
			return
		}

		snap := s.maintenance.Snapshot()
		w.Header().Set("Retry-After", strconv.Itoa(snap.RetrySeconds()))
		w.Header().Set("X-Maintenance-Mode", "true")
		w.Header().Set("X-Maintenance-Reason", snap.Reason)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"ok": false,
			"error": "MAINTENANCE",
			"message": i18n.Translate(i18n.LangFromRequest(r), "errors.maintenance_mode"),
			"details": map[string]any{
				"reason":       snap.Reason,
				"self_healing": snap.SelfHealingEnabled,
			},
		})
	})
}

// maintenanceCleanup reclaims disk space during file-write self-healing (PART 20).
// It trims old backups to the configured keep count and removes rotated log files
// older than the configured retention. All actions are scoped to the data
// directory and are best-effort; failures are logged and ignored.
func (s *Server) maintenanceCleanup() {
	if s.dataDir == "" {
		return
	}
	cfg := s.liveCfg().Server.Maintenance.Cleanup

	keep := cfg.BackupKeepCount
	if keep <= 0 {
		keep = 5
	}
	s.trimBackups(filepath.Join(s.dataDir, "backup"), keep)

	retention := cfg.LogRetentionDays
	if retention <= 0 {
		retention = 7
	}
	s.trimOldLogs(filepath.Join(s.dataDir, "logs"), time.Duration(retention)*24*time.Hour)
}

// trimBackups keeps only the most recent keep backup archives in dir, deleting
// older ones (ordered by modification time, newest first).
func (s *Server) trimBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type backupFile struct {
		path    string
		modTime time.Time
	}
	var files []backupFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.Contains(name, "_backup_") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, backupFile{path: filepath.Join(dir, name), modTime: info.ModTime()})
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	for _, f := range files[keep:] {
		if err := os.Remove(f.path); err != nil {
			log.Printf("maintenance: cleanup could not remove backup %s: %v", f.path, err)
		}
	}
}

// trimOldLogs removes *.log files in dir whose modification time is older than
// maxAge.
func (s *Server) trimOldLogs(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(dir, e.Name())
			if err := os.Remove(path); err != nil {
				log.Printf("maintenance: cleanup could not remove log %s: %v", path, err)
			}
		}
	}
}

// maintenanceNotify sends a security_alert email on maintenance enter/exit
// transitions (PART 20). It is a no-op when email is not configured or no
// operator contact address is set.
func (s *Server) maintenanceNotify(event string, snap health.Snapshot) {
	cfg := s.liveCfg()
	if !cfg.Server.Notifications.Email.Events.SecurityAlert {
		return
	}
	baseURL := cfg.Server.BaseURL
	if baseURL == "" {
		baseURL = "http://" + cfg.Server.FQDN
	}
	m := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, baseURL, cfg.Server.FQDN, cfg.Server.Lang)
	if !m.Enabled() {
		return
	}
	to := strings.TrimSpace(cfg.AdminEmail())
	if to == "" {
		return
	}

	var evt, details string
	if event == "exit" {
		evt = "Server recovered from maintenance mode"
		details = fmt.Sprintf("reason: %s\n  recovered after %d self-healing attempt(s)", snap.Reason, snap.Attempts)
	} else {
		evt = "Server entered maintenance mode: " + snap.Message
		details = fmt.Sprintf("reason: %s\n  self_healing: %t\n  retry_interval: %s",
			snap.Reason, snap.SelfHealingEnabled, snap.RetryInterval)
	}

	if err := m.Send(to, "security_alert", map[string]string{
		"event":     evt,
		"ip":        "n/a",
		"details":   details,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		log.Printf("maintenance: notification send failed: %v", err)
	}
}
