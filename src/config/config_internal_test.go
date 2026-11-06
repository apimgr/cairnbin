package config

// Internal white-box tests for unexported ConfigManager methods.
// Uses package config (not config_test) so checkFileChanges and applyHotSettings
// are directly callable.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// tempInternalPath returns a path inside a fresh temp directory that does not exist yet.
func tempInternalPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cairnbin-internal-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "server.yml")
}

// TestCheckFileChanges_NoFile verifies that checkFileChanges returns nil when
// the config file does not exist (stat fails).
func TestCheckFileChanges_NoFile(t *testing.T) {
	m := &ConfigManager{
		configPath:  "/nonexistent/path/that/will/never/exist/server.yml",
		current:     DefaultConfig(),
		lastModTime: time.Time{},
	}
	if got := m.checkFileChanges(); got != nil {
		t.Errorf("expected nil for missing file, got non-nil config")
	}
}

// TestCheckFileChanges_UnchangedModTime verifies that checkFileChanges returns
// nil when the file modtime has not advanced past lastModTime.
func TestCheckFileChanges_UnchangedModTime(t *testing.T) {
	path := tempInternalPath(t)

	cfg := DefaultConfig()
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	m := &ConfigManager{
		configPath:  path,
		current:     cfg,
		lastModTime: info.ModTime(),
	}

	if got := m.checkFileChanges(); got != nil {
		t.Errorf("expected nil when modtime unchanged, got non-nil config")
	}
}

// TestCheckFileChanges_ModifiedFile verifies that checkFileChanges detects a
// newer modtime and returns a non-nil config with the updated SiteTitle.
func TestCheckFileChanges_ModifiedFile(t *testing.T) {
	path := tempInternalPath(t)

	initial := DefaultConfig()
	initial.Web.SiteTitle = "Before"
	if err := Save(path, initial); err != nil {
		t.Fatalf("Save initial: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat initial: %v", err)
	}

	m := &ConfigManager{
		configPath:  path,
		current:     initial,
		lastModTime: info.ModTime(),
	}

	// Write updated config with a different SiteTitle.
	updated := DefaultConfig()
	updated.Web.SiteTitle = "After"
	if err := Save(path, updated); err != nil {
		t.Fatalf("Save updated: %v", err)
	}

	// Force a modtime strictly in the future to guarantee the poller detects a change
	// even on filesystems with 1-second resolution.
	futureTime := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, futureTime, futureTime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	got := m.checkFileChanges()
	if got == nil {
		t.Fatal("expected non-nil config after file modification, got nil")
	}
	if got.Web.SiteTitle != "After" {
		t.Errorf("SiteTitle: got %q, want %q", got.Web.SiteTitle, "After")
	}
}

// TestApplyHotSettings_RestartRequired verifies that applyHotSettings does not
// panic when Server.Port differs between prev and next.
func TestApplyHotSettings_RestartRequired(t *testing.T) {
	prev := DefaultConfig()
	prev.Server.Port = "8080"

	next := DefaultConfig()
	next.Server.Port = "9090"

	m := &ConfigManager{
		configPath: "/dev/null",
		current:    prev,
	}

	// Must not panic.
	m.applyHotSettings(prev, next)
}

// TestApplyHotSettings_HotReloadable verifies that applyHotSettings does not
// panic when only a hot-reloadable field (Logging.Level) differs.
func TestApplyHotSettings_HotReloadable(t *testing.T) {
	prev := DefaultConfig()
	prev.Server.Logging.Level = "info"

	next := DefaultConfig()
	next.Server.Logging.Level = "debug"

	m := &ConfigManager{
		configPath: "/dev/null",
		current:    prev,
	}

	// Must not panic.
	m.applyHotSettings(prev, next)
}

// TestApplyHotSettings_AllChanges verifies that applyHotSettings does not panic
// when all tracked fields differ simultaneously.
func TestApplyHotSettings_AllChanges(t *testing.T) {
	prev := DefaultConfig()
	prev.Server.Port = "8080"
	prev.Server.Address = "0.0.0.0"
	prev.Database.Type = "sqlite"
	prev.Server.Tor.Binary = ""
	prev.Server.Logging.Level = "info"
	prev.Server.RateLimit.Enabled = true
	prev.Server.Cors.AllowedOrigins = []string{"*"}
	prev.Web.SiteTitle = "Before"

	next := DefaultConfig()
	next.Server.Port = "9090"
	next.Server.Address = "127.0.0.1"
	next.Database.Type = "sqlite"
	next.Database.Path = "/tmp/new.db"
	next.Server.Tor.Binary = "/usr/bin/tor"
	next.Server.Logging.Level = "debug"
	next.Server.RateLimit.Enabled = false
	next.Server.Cors.AllowedOrigins = []string{"https://example.com"}
	next.Web.SiteTitle = "After"

	m := &ConfigManager{
		configPath: "/dev/null",
		current:    prev,
	}

	// Must not panic.
	m.applyHotSettings(prev, next)
}
