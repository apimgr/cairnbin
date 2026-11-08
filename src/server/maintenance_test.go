package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/apimgr/cairnbin/src/config"
	"github.com/apimgr/cairnbin/src/health"
)

// okHandler is a sentinel next-handler that records whether it ran.
func okHandler(ran *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestMaintenanceMiddlewarePassThroughWhenNormal(t *testing.T) {
	s := &Server{maintenance: health.New(health.Config{})}
	ran := false
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pastes", nil)
	rr := httptest.NewRecorder()
	s.maintenanceMiddleware(okHandler(&ran)).ServeHTTP(rr, req)
	if !ran {
		t.Fatal("expected next handler to run when not in maintenance")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestMaintenanceMiddlewareBlocksWrites(t *testing.T) {
	m := health.New(health.Config{SelfHealingEnabled: true})
	m.Enter(health.ReasonDatabaseConnection, "Database connection failed")
	s := &Server{maintenance: m}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		ran := false
		req := httptest.NewRequest(method, "/api/v1/pastes", nil)
		rr := httptest.NewRecorder()
		s.maintenanceMiddleware(okHandler(&ran)).ServeHTTP(rr, req)

		if ran {
			t.Fatalf("%s: next handler should not run in maintenance", method)
		}
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: expected 503, got %d", method, rr.Code)
		}
		if rr.Header().Get("X-Maintenance-Mode") != "true" {
			t.Fatalf("%s: missing X-Maintenance-Mode header", method)
		}
		if rr.Header().Get("X-Maintenance-Reason") != health.ReasonDatabaseConnection {
			t.Fatalf("%s: wrong reason header: %q", method, rr.Header().Get("X-Maintenance-Reason"))
		}
		if rr.Header().Get("Retry-After") == "" {
			t.Fatalf("%s: missing Retry-After header", method)
		}

		var body struct {
			OK      bool   `json:"ok"`
			Error   string `json:"error"`
			Message string `json:"message"`
			Details struct {
				Reason      string `json:"reason"`
				SelfHealing bool   `json:"self_healing"`
			} `json:"details"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: bad json: %v", method, err)
		}
		if body.OK || body.Error != "MAINTENANCE" {
			t.Fatalf("%s: wrong envelope: %+v", method, body)
		}
		if body.Details.Reason != health.ReasonDatabaseConnection || !body.Details.SelfHealing {
			t.Fatalf("%s: wrong details: %+v", method, body.Details)
		}
	}
}

func TestCriticalCheck(t *testing.T) {
	t.Run("db failure reports database_connection", func(t *testing.T) {
		s := &Server{db: &stubDB{pingErr: errors.New("down")}}
		ok, reason, msg := s.criticalCheck()
		if ok || reason != health.ReasonDatabaseConnection || msg == "" {
			t.Fatalf("expected db failure, got ok=%v reason=%q msg=%q", ok, reason, msg)
		}
	})
	t.Run("healthy db passes", func(t *testing.T) {
		s := &Server{db: &stubDB{}}
		ok, reason, _ := s.criticalCheck()
		// Disk is expected healthy on the test host, so this should pass.
		if !ok || reason != "" {
			t.Fatalf("expected healthy, got ok=%v reason=%q", ok, reason)
		}
	})
}

func TestMaintenanceCleanupTrimsBackupsAndLogs(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "backup")
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Now()
	for i := 0; i < 4; i++ {
		p := filepath.Join(backupDir, "cairnbin_backup_"+time.Duration(i).String()+".tar.gz")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Stagger modtimes so ordering is deterministic.
		mt := base.Add(time.Duration(i) * time.Hour)
		_ = os.Chtimes(p, mt, mt)
	}

	oldLog := filepath.Join(logDir, "old.log")
	newLog := filepath.Join(logDir, "new.log")
	if err := os.WriteFile(oldLog, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newLog, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := base.Add(-100 * 24 * time.Hour)
	_ = os.Chtimes(oldLog, old, old)

	cfg := &config.Config{}
	cfg.Server.Maintenance.Cleanup.BackupKeepCount = 2
	cfg.Server.Maintenance.Cleanup.LogRetentionDays = 7
	s := &Server{cfg: cfg, dataDir: dir}
	s.maintenanceCleanup()

	entries, _ := os.ReadDir(backupDir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 backups kept, got %d", len(entries))
	}
	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Fatal("expected old log removed")
	}
	if _, err := os.Stat(newLog); err != nil {
		t.Fatal("expected new log kept")
	}
}

func TestMaintenanceCleanupNoDataDir(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	// Must not panic when dataDir is empty.
	s.maintenanceCleanup()
}

func TestMaintenanceNotifyNoopWhenEmailDisabled(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	// Email disabled by default → must be a silent no-op (no panic).
	s.maintenanceNotify("enter", health.Snapshot{Reason: health.ReasonDatabaseConnection, Message: "db down"})
	s.maintenanceNotify("exit", health.Snapshot{Reason: health.ReasonDatabaseConnection})
}

func TestMaintenanceMiddlewareAllowsReads(t *testing.T) {
	m := health.New(health.Config{})
	m.Enter(health.ReasonDatabaseConnection, "db down")
	s := &Server{maintenance: m}

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		ran := false
		req := httptest.NewRequest(method, "/api/v1/pastes/abc", nil)
		rr := httptest.NewRecorder()
		s.maintenanceMiddleware(okHandler(&ran)).ServeHTTP(rr, req)
		if !ran {
			t.Fatalf("%s: read should pass through during maintenance", method)
		}
	}
}

// ─── trimBackups additional coverage ─────────────────────────────────────────

func TestTrimBackupsFewFilesNoDelete(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only 2 backup files; keep=5 → nothing should be deleted.
	for i := 0; i < 2; i++ {
		name := filepath.Join(backupDir, "cairnbin_backup_2026-07-0"+strconv.Itoa(i)+"_120000.tar.gz")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{cfg: &config.Config{}}
	s.trimBackups(backupDir, 5)

	entries, _ := os.ReadDir(backupDir)
	if len(entries) != 2 {
		t.Errorf("expected 2 files kept, got %d", len(entries))
	}
}

func TestTrimBackupsNonBackupFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file without "_backup_" in the name must never be deleted.
	unrelated := filepath.Join(backupDir, "README.txt")
	if err := os.WriteFile(unrelated, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &config.Config{}}
	s.trimBackups(backupDir, 0)

	if _, err := os.Stat(unrelated); err != nil {
		t.Errorf("non-backup file was incorrectly deleted: %v", err)
	}
}

func TestTrimBackupsNonExistentDirNoPanic(t *testing.T) {
	s := &Server{cfg: &config.Config{}}
	// Directory does not exist → ReadDir error → silent return, no panic.
	s.trimBackups("/this/path/does/not/exist/backup", 5)
}

// ─── trimOldLogs additional coverage ─────────────────────────────────────────

func TestTrimOldLogsNonLogFilesSkipped(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Files with non-.log extensions and subdirectories must never be removed.
	keep := filepath.Join(logDir, "config.conf")
	keepTxt := filepath.Join(logDir, "notes.txt")
	subDir := filepath.Join(logDir, "archive")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keepTxt, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Back-date everything so age is not the reason they survive.
	old := time.Now().Add(-100 * 24 * time.Hour)
	for _, p := range []string{keep, keepTxt} {
		_ = os.Chtimes(p, old, old)
	}

	s := &Server{cfg: &config.Config{}}
	s.trimOldLogs(logDir, 7*24*time.Hour)

	for _, p := range []string{keep, keepTxt} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("non-.log file %s was incorrectly removed", filepath.Base(p))
		}
	}
	if _, err := os.Stat(subDir); err != nil {
		t.Error("subdirectory was incorrectly removed")
	}
}

func TestTrimOldLogsRecentFileKept(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recent := filepath.Join(logDir, "recent.log")
	if err := os.WriteFile(recent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Modification time is ~now; it must survive a 7-day retention pass.
	s := &Server{cfg: &config.Config{}}
	s.trimOldLogs(logDir, 7*24*time.Hour)
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent log was incorrectly removed: %v", err)
	}
}

// ─── maintenanceCleanup default values ───────────────────────────────────────

func TestMaintenanceCleanupDefaultLimits(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "backup")
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create 7 backup files — more than the default keep count of 5.
	base := time.Now()
	for i := 0; i < 7; i++ {
		name := filepath.Join(backupDir, "cairnbin_backup_2026-07-0"+strconv.Itoa(i)+"_000000.tar.gz")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Hour)
		_ = os.Chtimes(name, mt, mt)
	}

	// Zero config values must trigger the defaults: BackupKeepCount=5, LogRetentionDays=7.
	cfg := &config.Config{}
	cfg.Server.Maintenance.Cleanup.BackupKeepCount = 0
	cfg.Server.Maintenance.Cleanup.LogRetentionDays = 0
	s := &Server{cfg: cfg, dataDir: dir}
	s.maintenanceCleanup()

	entries, _ := os.ReadDir(backupDir)
	if len(entries) != 5 {
		t.Errorf("expected 5 backups after default-limit cleanup, got %d", len(entries))
	}
}

// ─── maintenanceNotify with email enabled ─────────────────────────────────────

func TestMaintenanceNotifyEmailEnabledBothBranches(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Notifications.Email.Enabled = true
	cfg.Server.Notifications.Email.Events.SecurityAlert = true
	cfg.Server.Notifications.Email.SMTP.Host = "127.0.0.1"
	cfg.Server.Notifications.Email.SMTP.Port = 1
	cfg.Server.Contact.Admin.Email = "admin@example.com"

	s := &Server{cfg: cfg}

	// Both branches of the `if event == "exit"` block must be reached without panic.
	// Send calls will fail (nothing is listening on port 1) and the error is logged.
	s.maintenanceNotify("enter", health.Snapshot{
		Reason:             health.ReasonFileWrite,
		Message:            "Disk full",
		SelfHealingEnabled: true,
		RetryInterval:      30 * time.Second,
	})
	s.maintenanceNotify("exit", health.Snapshot{
		Reason:   health.ReasonFileWrite,
		Attempts: 3,
	})
}
