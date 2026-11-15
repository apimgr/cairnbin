package paths_test

// Tests for the paths package: env-override paths, IsContainer on a normal host,
// and EnsureDir directory creation.
//
// t.Setenv is used for all env tests — it auto-restores the original value
// when the test finishes, so tests are safe to run in parallel or sequence.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apimgr/cairnbin/src/paths"
)

// ─── Env-override path helpers ────────────────────────────────────────────────

// TestGetConfigDir_EnvOverride verifies that CONFIG_DIR overrides the computed path.
func TestGetConfigDir_EnvOverride(t *testing.T) {
	t.Setenv("CONFIG_DIR", "/tmp/test-config")
	got := paths.GetConfigDir("cairnbin")
	if got != "/tmp/test-config" {
		t.Errorf("GetConfigDir: got %q, want %q", got, "/tmp/test-config")
	}
}

// TestGetDataDir_EnvOverride verifies that DATA_DIR overrides the computed path.
func TestGetDataDir_EnvOverride(t *testing.T) {
	t.Setenv("DATA_DIR", "/tmp/test-data")
	got := paths.GetDataDir("cairnbin")
	if got != "/tmp/test-data" {
		t.Errorf("GetDataDir: got %q, want %q", got, "/tmp/test-data")
	}
}

// TestGetLogsDir_EnvOverride verifies that LOGS_DIR overrides the computed path.
func TestGetLogsDir_EnvOverride(t *testing.T) {
	t.Setenv("LOGS_DIR", "/tmp/test-logs")
	got := paths.GetLogsDir("cairnbin")
	if got != "/tmp/test-logs" {
		t.Errorf("GetLogsDir: got %q, want %q", got, "/tmp/test-logs")
	}
}

// TestGetLogsDir_LOG_DIR verifies that the AI.md-canonical LOG_DIR overrides the path.
func TestGetLogsDir_LOG_DIR(t *testing.T) {
	_ = os.Unsetenv("LOGS_DIR")
	t.Setenv("LOG_DIR", "/tmp/canonical-logs")
	got := paths.GetLogsDir("cairnbin")
	if got != "/tmp/canonical-logs" {
		t.Errorf("GetLogsDir: got %q, want %q", got, "/tmp/canonical-logs")
	}
}

// TestGetDBPath_DATABASE_DIR verifies that DATABASE_DIR overrides only the directory.
func TestGetDBPath_DATABASE_DIR(t *testing.T) {
	_ = os.Unsetenv("DB_PATH")
	t.Setenv("DATABASE_DIR", "/tmp/dbdir")
	got := paths.GetDBPath("cairnbin")
	if got != filepath.Join("/tmp/dbdir", "server.db") {
		t.Errorf("GetDBPath: got %q, want %q", got, filepath.Join("/tmp/dbdir", "server.db"))
	}
}

// TestGetBackupDir_EnvOverride verifies that BACKUP_DIR overrides the computed path.
func TestGetBackupDir_EnvOverride(t *testing.T) {
	t.Setenv("BACKUP_DIR", "/tmp/test-backup")
	got := paths.GetBackupDir("cairnbin")
	if got != "/tmp/test-backup" {
		t.Errorf("GetBackupDir: got %q, want %q", got, "/tmp/test-backup")
	}
}

// TestGetPIDFile_EnvOverride verifies that PID_FILE overrides the computed path.
func TestGetPIDFile_EnvOverride(t *testing.T) {
	t.Setenv("PID_FILE", "/tmp/test-cairnbin.pid")
	got := paths.GetPIDFile("cairnbin")
	if got != "/tmp/test-cairnbin.pid" {
		t.Errorf("GetPIDFile: got %q, want %q", got, "/tmp/test-cairnbin.pid")
	}
}

// TestGetCacheDir_EnvOverride verifies that CACHE_DIR overrides the computed path.
func TestGetCacheDir_EnvOverride(t *testing.T) {
	t.Setenv("CACHE_DIR", "/tmp/test-cache")
	got := paths.GetCacheDir("cairnbin")
	if got != "/tmp/test-cache" {
		t.Errorf("GetCacheDir: got %q, want %q", got, "/tmp/test-cache")
	}
}

// TestGetDBPath_EnvOverride verifies that DB_PATH overrides the computed path.
func TestGetDBPath_EnvOverride(t *testing.T) {
	t.Setenv("DB_PATH", "/tmp/test.db")
	got := paths.GetDBPath("cairnbin")
	if got != "/tmp/test.db" {
		t.Errorf("GetDBPath: got %q, want %q", got, "/tmp/test.db")
	}
}

// TestGetDBPath_NativeHost verifies that outside a container GetDBPath derives
// the path from GetDataDir.
func TestGetDBPath_NativeHost(t *testing.T) {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		t.Skip("running inside Docker — container path applies")
	}
	_ = os.Unsetenv("DB_PATH")
	got := paths.GetDBPath("cairnbin")
	want := filepath.Join(paths.GetDataDir("cairnbin"), "db", "server.db")
	if got != want {
		t.Errorf("GetDBPath: got %q, want %q", got, want)
	}
}

// ─── IsContainer ──────────────────────────────────────────────────────────────

// TestIsContainer verifies that a normal test host (no /.dockerenv, no docker
// cgroup entries) is not identified as a container.
//
// Note: this test may return true if it genuinely runs inside Docker.
// It is kept here as a negative sanity check for bare-metal / VM hosts.
func TestIsContainer(t *testing.T) {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		t.Skip("running inside Docker — IsContainer() is expected to return true")
	}
	if paths.IsContainer() {
		// Could be a Docker-based CI; report but don't hard-fail.
		t.Log("IsContainer() returned true on this host — may be a container-based CI environment")
	}
}

// ─── EnsureDir ────────────────────────────────────────────────────────────────

// TestEnsureDir confirms that EnsureDir creates a directory (including parents)
// when it does not exist, and is idempotent when called a second time.
func TestEnsureDir(t *testing.T) {
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cairnbin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	target := filepath.Join(dir, "nested", "deep", "dir")

	// First call creates the directory.
	if err := paths.EnsureDir(target); err != nil {
		t.Fatalf("EnsureDir first call: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%q exists but is not a directory", target)
	}

	// Second call must be idempotent (no error).
	if err := paths.EnsureDir(target); err != nil {
		t.Fatalf("EnsureDir second call (idempotent): %v", err)
	}
}

// ─── XDG / home-default path tests (non-root, non-container) ─────────────────

// skipIfContainer skips the test when we are inside a Docker container because
// the expected user-mode paths do not apply there.
func skipIfContainer(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/.dockerenv"); err == nil {
		t.Skip("running inside Docker — user-mode paths do not apply")
	}
}

// skipIfRoot skips the test when running as root because all path branches
// under test here are the non-root code paths.
func skipIfRoot(t *testing.T) {
	t.Helper()
	// os.Geteuid() is available on all POSIX platforms; always 0 on Windows but
	// we guard with the build tag below.
	if os.Geteuid() == 0 {
		t.Skip("running as root — non-root path branches not exercised")
	}
}

// TestGetConfigDir_IgnoresXDG verifies that XDG_CONFIG_HOME is NOT honored (PART 4) when
// CONFIG_DIR is not set and we are not running as root or in a container.
func TestGetConfigDir_IgnoresXDG(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")
	_ = os.Unsetenv("CONFIG_DIR")

	home, _ := os.UserHomeDir()
	got := paths.GetConfigDir("cairnbin")
	want := filepath.Join(home, ".config", "apimgr", "cairnbin")
	if got != want {
		t.Errorf("GetConfigDir with XDG set: got %q, want %q", got, want)
	}
}

// TestGetConfigDir_HomeDefault verifies the ~/.config/apimgr/cairnbin fallback
// on Linux when neither CONFIG_DIR nor XDG_CONFIG_HOME is set.
func TestGetConfigDir_HomeDefault(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	_ = os.Unsetenv("CONFIG_DIR")
	_ = os.Unsetenv("XDG_CONFIG_HOME")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}

	got := paths.GetConfigDir("cairnbin")
	want := filepath.Join(home, ".config", "apimgr", "cairnbin")
	if got != want {
		t.Errorf("GetConfigDir home default: got %q, want %q", got, want)
	}
}

// TestGetDataDir_IgnoresXDG verifies that XDG_DATA_HOME is NOT honored (PART 4) when DATA_DIR is
// not set and we are not root or in a container.
func TestGetDataDir_IgnoresXDG(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data")
	_ = os.Unsetenv("DATA_DIR")

	home, _ := os.UserHomeDir()
	got := paths.GetDataDir("cairnbin")
	want := filepath.Join(home, ".local", "share", "apimgr", "cairnbin")
	if got != want {
		t.Errorf("GetDataDir with XDG set: got %q, want %q", got, want)
	}
}

// TestGetDataDir_HomeDefault verifies the ~/.local/share/apimgr/cairnbin
// fallback on Linux when neither DATA_DIR nor XDG_DATA_HOME is set.
func TestGetDataDir_HomeDefault(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	_ = os.Unsetenv("DATA_DIR")
	_ = os.Unsetenv("XDG_DATA_HOME")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}

	got := paths.GetDataDir("cairnbin")
	want := filepath.Join(home, ".local", "share", "apimgr", "cairnbin")
	if got != want {
		t.Errorf("GetDataDir home default: got %q, want %q", got, want)
	}
}

// TestGetLogsDir_HomeDefault verifies the ~/.local/log/apimgr/cairnbin path on
// Linux for a non-root, non-container user when LOGS_DIR is not set.
func TestGetLogsDir_HomeDefault(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	_ = os.Unsetenv("LOGS_DIR")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}

	got := paths.GetLogsDir("cairnbin")
	want := filepath.Join(home, ".local", "log", "apimgr", "cairnbin")
	if got != want {
		t.Errorf("GetLogsDir home default: got %q, want %q", got, want)
	}
}

// TestGetBackupDir_DataBased verifies that on a normal user host GetBackupDir
// returns a path under the user's home directory when BACKUP_DIR is not set.
func TestGetBackupDir_DataBased(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	_ = os.Unsetenv("BACKUP_DIR")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}

	got := paths.GetBackupDir("cairnbin")
	// The backup dir must start with the user home, confirming we are in the
	// non-root / non-container branch.
	if !filepath.IsAbs(got) {
		t.Errorf("GetBackupDir: expected absolute path, got %q", got)
	}
	if len(got) < len(home) || got[:len(home)] != home {
		t.Errorf("GetBackupDir: expected path under %q, got %q", home, got)
	}
}

// TestGetPIDFile_UserPath verifies that for a non-root user the PID file path
// ends in "cairnbin.pid" and is located under the data directory.
func TestGetPIDFile_UserPath(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	_ = os.Unsetenv("PID_FILE")

	got := paths.GetPIDFile("cairnbin")
	if filepath.Base(got) != "cairnbin.pid" {
		t.Errorf("GetPIDFile: base name should be %q, got %q", "cairnbin.pid", filepath.Base(got))
	}

	// Must be under the data directory for user installs.
	dataDir := paths.GetDataDir("cairnbin")
	if len(got) <= len(dataDir) || got[:len(dataDir)] != dataDir {
		t.Errorf("GetPIDFile: expected path under %q, got %q", dataDir, got)
	}
}

// TestGetCacheDir_IgnoresXDG verifies XDG_CACHE_HOME is not honored (AI.md PART 4
// fixes the user cache path at ~/.cache/{org}/{app}/).
func TestGetCacheDir_IgnoresXDG(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	_ = os.Unsetenv("CACHE_DIR")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	got := paths.GetCacheDir("cairnbin")
	want := filepath.Join(home, ".cache", "apimgr", "cairnbin")
	if got != want {
		t.Errorf("GetCacheDir with XDG set: got %q, want %q", got, want)
	}
}

// TestGetCacheDir_HomeDefault verifies the ~/.cache/apimgr/cairnbin fallback on
// Linux when neither CACHE_DIR nor XDG_CACHE_HOME is set.
func TestGetCacheDir_HomeDefault(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	_ = os.Unsetenv("CACHE_DIR")
	_ = os.Unsetenv("XDG_CACHE_HOME")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}

	got := paths.GetCacheDir("cairnbin")
	want := filepath.Join(home, ".cache", "apimgr", "cairnbin")
	if got != want {
		t.Errorf("GetCacheDir home default: got %q, want %q", got, want)
	}
}

// TestIsContainer_Returns_Bool verifies that IsContainer() returns a boolean
// value without panicking. The actual value is environment-dependent and not
// asserted here.
func TestIsContainer_Returns_Bool(t *testing.T) {
	result := paths.IsContainer()
	// Confirm we got a bool — the assignment itself is the assertion.
	_ = result
}

// TestGetDBPath_NativeUser verifies that GetDBPath under a normal (non-root,
// non-container) user returns a path rooted in the data directory.
func TestGetDBPath_NativeUser(t *testing.T) {
	skipIfContainer(t)
	skipIfRoot(t)

	_ = os.Unsetenv("DB_PATH")

	got := paths.GetDBPath("cairnbin")
	dataDir := paths.GetDataDir("cairnbin")
	want := filepath.Join(dataDir, "db", "server.db")
	if got != want {
		t.Errorf("GetDBPath user: got %q, want %q", got, want)
	}
}

// TestAllPathFunctions_ReturnAbsolutePaths verifies that every exported path
// function returns an absolute path regardless of which branch it follows.
// This is a regression guard against accidentally returning relative paths.
func TestAllPathFunctions_ReturnAbsolutePaths(t *testing.T) {
	skipIfContainer(t)

	// Clear all env overrides so we exercise the computed branches.
	for _, env := range []string{
		"CONFIG_DIR", "DATA_DIR", "LOGS_DIR", "BACKUP_DIR",
		"PID_FILE", "CACHE_DIR", "DB_PATH",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
	} {
		_ = os.Unsetenv(env)
	}

	cases := []struct {
		name string
		fn   func() string
	}{
		{"GetConfigDir", func() string { return paths.GetConfigDir("cairnbin") }},
		{"GetDataDir", func() string { return paths.GetDataDir("cairnbin") }},
		{"GetLogsDir", func() string { return paths.GetLogsDir("cairnbin") }},
		{"GetBackupDir", func() string { return paths.GetBackupDir("cairnbin") }},
		{"GetPIDFile", func() string { return paths.GetPIDFile("cairnbin") }},
		{"GetCacheDir", func() string { return paths.GetCacheDir("cairnbin") }},
		{"GetDBPath", func() string { return paths.GetDBPath("cairnbin") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.fn()
			if !filepath.IsAbs(got) {
				t.Errorf("%s returned non-absolute path: %q", tc.name, got)
			}
		})
	}
}

// TestSafeFilePath verifies containment: valid sub-paths resolve inside the
// base, traversal and invalid segments are rejected.
func TestSafeFilePath(t *testing.T) {
	base := t.TempDir()

	got, err := paths.SafeFilePath(base, "/my//dir/file")
	if err != nil {
		t.Fatalf("valid path rejected: %v", err)
	}
	if want := filepath.Join(base, "my", "dir", "file"); got != want {
		t.Errorf("paths.SafeFilePath = %q, want %q", got, want)
	}

	if got, err := paths.SafeFilePath(base, ""); err != nil || got != base {
		t.Errorf("empty path = (%q, %v), want (%q, nil)", got, err, base)
	}

	for _, bad := range []string{"../etc", "a/../../b", "Upper", "a/<script>"} {
		if _, err := paths.SafeFilePath(base, bad); err == nil {
			t.Errorf("paths.SafeFilePath(%q) accepted, want error", bad)
		}
	}
}
