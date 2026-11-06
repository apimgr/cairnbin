//go:build !windows

package path

// Internal tests for paths package — exercises non-container and OS-specific
// branches by overriding containerCheck, rootCheck, and detectedOS.
// These tests are in the same package so they can manipulate unexported vars.

import (
	"os"
	"path/filepath"
	"testing"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// withEnv sets up injectable vars and restores them after the test.
type pathEnv struct {
	isContainer bool
	isRoot      bool
	goos        string
}

func applyEnv(t *testing.T, e pathEnv) {
	t.Helper()
	oldC := containerCheck
	oldR := rootCheck
	oldOS := detectedOS
	oldW := writableCheck
	containerCheck = func() bool { return e.isContainer }
	rootCheck = func() bool { return e.isRoot }
	detectedOS = e.goos
	writableCheck = func(string) bool { return true }
	t.Cleanup(func() {
		containerCheck = oldC
		rootCheck = oldR
		detectedOS = oldOS
		writableCheck = oldW
	})
}

// ── Container paths ───────────────────────────────────────────────────────────

func TestGetConfigDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	t.Setenv("CONFIG_DIR", "")
	if got := GetConfigDir("cairnbin"); got != "/config/cairnbin" {
		t.Errorf("GetConfigDir container: got %q, want %q", got, "/config/cairnbin")
	}
}

func TestGetDataDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	t.Setenv("DATA_DIR", "")
	if got := GetDataDir("cairnbin"); got != "/data/cairnbin" {
		t.Errorf("GetDataDir container: got %q, want %q", got, "/data/cairnbin")
	}
}

func TestGetBackupDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	t.Setenv("BACKUP_DIR", "")
	want := "/data/backups/cairnbin"
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir container: got %q, want %q", got, want)
	}
}

func TestGetPIDFile_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	t.Setenv("PID_FILE", "")
	t.Setenv("DATA_DIR", "")
	got := GetPIDFile("cairnbin")
	if filepath.Base(got) != "cairnbin.pid" {
		t.Errorf("GetPIDFile container: base=%q, want cairnbin.pid", filepath.Base(got))
	}
}

func TestGetLogsDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	_ = os.Unsetenv("LOGS_DIR")
	if got := GetLogsDir("cairnbin"); got != "/data/log/cairnbin" {
		t.Errorf("GetLogsDir container: got %q, want %q", got, "/data/log/cairnbin")
	}
}

func TestGetCacheDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	_ = os.Unsetenv("CACHE_DIR")
	if got := GetCacheDir("cairnbin"); got != "/data/cairnbin/cache" {
		t.Errorf("GetCacheDir container: got %q, want %q", got, "/data/cairnbin/cache")
	}
}

func TestGetDBPath_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	t.Setenv("DB_PATH", "")
	if got := GetDBPath("cairnbin"); got != "/data/db/sqlite/server.db" {
		t.Errorf("GetDBPath container: got %q, want %q", got, "/data/db/sqlite/server.db")
	}
}

// ── Linux root paths ──────────────────────────────────────────────────────────

func TestGetConfigDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	t.Setenv("CONFIG_DIR", "")
	want := "/etc/apimgr/cairnbin"
	if got := GetConfigDir("cairnbin"); got != want {
		t.Errorf("GetConfigDir linux root: got %q, want %q", got, want)
	}
}

func TestGetDataDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	t.Setenv("DATA_DIR", "")
	want := "/var/lib/apimgr/cairnbin"
	if got := GetDataDir("cairnbin"); got != want {
		t.Errorf("GetDataDir linux root: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	_ = os.Unsetenv("BACKUP_DIR")
	want := "/mnt/Backups/apimgr/cairnbin"
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir linux root: got %q, want %q", got, want)
	}
}

func TestGetPIDFile_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	t.Setenv("PID_FILE", "")
	want := "/var/run/apimgr/cairnbin.pid"
	if got := GetPIDFile("cairnbin"); got != want {
		t.Errorf("GetPIDFile linux root: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	t.Setenv("LOGS_DIR", "")
	want := "/var/log/apimgr/cairnbin"
	if got := GetLogsDir("cairnbin"); got != want {
		t.Errorf("GetLogsDir linux root: got %q, want %q", got, want)
	}
}

func TestGetCacheDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	t.Setenv("CACHE_DIR", "")
	want := "/var/cache/apimgr/cairnbin"
	if got := GetCacheDir("cairnbin"); got != want {
		t.Errorf("GetCacheDir linux root: got %q, want %q", got, want)
	}
}

func TestGetDBPath_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	_ = os.Unsetenv("DB_PATH")
	_ = os.Unsetenv("DATA_DIR")
	want := filepath.Join("/var/lib/apimgr/cairnbin", "db", "server.db")
	if got := GetDBPath("cairnbin"); got != want {
		t.Errorf("GetDBPath linux root: got %q, want %q", got, want)
	}
}

// ── Linux non-root paths (home) ──────────────────────────────────────

// XDG overrides are not part of AI.md PART 4; the fixed home path always wins.
func TestGetConfigDir_LinuxUser_IgnoresXDG(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	_ = os.Unsetenv("CONFIG_DIR")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-ignored")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "apimgr", "cairnbin")
	if got := GetConfigDir("cairnbin"); got != want {
		t.Errorf("GetConfigDir linux user with XDG set: got %q, want %q", got, want)
	}
}

// XDG overrides are not part of AI.md PART 4; the fixed home path always wins.
func TestGetDataDir_LinuxUser_IgnoresXDG(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	_ = os.Unsetenv("DATA_DIR")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-ignored")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "share", "apimgr", "cairnbin")
	if got := GetDataDir("cairnbin"); got != want {
		t.Errorf("GetDataDir linux user with XDG set: got %q, want %q", got, want)
	}
}

// XDG overrides are not part of AI.md PART 4; the fixed home path always wins.
func TestGetCacheDir_LinuxUser_IgnoresXDG(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	_ = os.Unsetenv("CACHE_DIR")
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-ignored")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".cache", "apimgr", "cairnbin")
	if got := GetCacheDir("cairnbin"); got != want {
		t.Errorf("GetCacheDir linux user with XDG set: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_LinuxUser_Home(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	_ = os.Unsetenv("LOGS_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "log", "apimgr", "cairnbin")
	if got := GetLogsDir("cairnbin"); got != want {
		t.Errorf("GetLogsDir linux user home: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_LinuxUser_Home(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	writableCheck = func(string) bool { return false }
	_ = os.Unsetenv("BACKUP_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "share", "Backups", "apimgr", "cairnbin")
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir linux user home: got %q, want %q", got, want)
	}
}

func TestGetPIDFile_LinuxUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	_ = os.Unsetenv("PID_FILE")
	_ = os.Unsetenv("DATA_DIR")
	_ = os.Unsetenv("XDG_DATA_HOME")
	got := GetPIDFile("cairnbin")
	if filepath.Base(got) != "cairnbin.pid" {
		t.Errorf("GetPIDFile linux user: base=%q, want cairnbin.pid", filepath.Base(got))
	}
}

// ── macOS paths ───────────────────────────────────────────────────────────────

func TestGetConfigDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	_ = os.Unsetenv("CONFIG_DIR")
	want := "/Library/Application Support/apimgr/cairnbin"
	if got := GetConfigDir("cairnbin"); got != want {
		t.Errorf("GetConfigDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetDataDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	_ = os.Unsetenv("DATA_DIR")
	want := "/Library/Application Support/apimgr/cairnbin/data"
	if got := GetDataDir("cairnbin"); got != want {
		t.Errorf("GetDataDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	_ = os.Unsetenv("LOGS_DIR")
	want := "/Library/Logs/apimgr/cairnbin"
	if got := GetLogsDir("cairnbin"); got != want {
		t.Errorf("GetLogsDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetCacheDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	_ = os.Unsetenv("CACHE_DIR")
	want := "/Library/Caches/apimgr/cairnbin"
	if got := GetCacheDir("cairnbin"); got != want {
		t.Errorf("GetCacheDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	_ = os.Unsetenv("BACKUP_DIR")
	want := "/Library/Backups/apimgr/cairnbin"
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetConfigDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	_ = os.Unsetenv("CONFIG_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Application Support", "apimgr", "cairnbin")
	if got := GetConfigDir("cairnbin"); got != want {
		t.Errorf("GetConfigDir darwin user: got %q, want %q", got, want)
	}
}

func TestGetDataDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	_ = os.Unsetenv("DATA_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Application Support", "apimgr", "cairnbin")
	if got := GetDataDir("cairnbin"); got != want {
		t.Errorf("GetDataDir darwin user: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	_ = os.Unsetenv("LOGS_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Logs", "apimgr", "cairnbin")
	if got := GetLogsDir("cairnbin"); got != want {
		t.Errorf("GetLogsDir darwin user: got %q, want %q", got, want)
	}
}

func TestGetCacheDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	_ = os.Unsetenv("CACHE_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Caches", "apimgr", "cairnbin")
	if got := GetCacheDir("cairnbin"); got != want {
		t.Errorf("GetCacheDir darwin user: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	writableCheck = func(string) bool { return false }
	_ = os.Unsetenv("BACKUP_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Backups", "apimgr", "cairnbin")
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir darwin user: got %q, want %q", got, want)
	}
}

// ── BSD paths ─────────────────────────────────────────────────────────────────

func TestGetConfigDir_FreeBSDRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "freebsd"})
	_ = os.Unsetenv("CONFIG_DIR")
	want := "/usr/local/etc/apimgr/cairnbin"
	if got := GetConfigDir("cairnbin"); got != want {
		t.Errorf("GetConfigDir freebsd root: got %q, want %q", got, want)
	}
}

func TestGetDataDir_FreeBSDRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "freebsd"})
	_ = os.Unsetenv("DATA_DIR")
	want := "/var/db/apimgr/cairnbin"
	if got := GetDataDir("cairnbin"); got != want {
		t.Errorf("GetDataDir freebsd root: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_FreeBSDRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "freebsd"})
	_ = os.Unsetenv("BACKUP_DIR")
	want := "/var/backups/apimgr/cairnbin"
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir freebsd root: got %q, want %q", got, want)
	}
}

// ── Windows paths ─────────────────────────────────────────────────────────────

func TestGetConfigDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	_ = os.Unsetenv("CONFIG_DIR")
	t.Setenv("APPDATA", "C:\\Users\\test\\AppData\\Roaming")
	want := filepath.Join("C:\\Users\\test\\AppData\\Roaming", "apimgr", "cairnbin")
	if got := GetConfigDir("cairnbin"); got != want {
		t.Errorf("GetConfigDir windows user: got %q, want %q", got, want)
	}
}

func TestGetDataDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	_ = os.Unsetenv("DATA_DIR")
	t.Setenv("LOCALAPPDATA", "C:\\Users\\test\\AppData\\Local")
	want := filepath.Join("C:\\Users\\test\\AppData\\Local", "apimgr", "cairnbin")
	if got := GetDataDir("cairnbin"); got != want {
		t.Errorf("GetDataDir windows user: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	_ = os.Unsetenv("LOGS_DIR")
	t.Setenv("LOCALAPPDATA", "C:\\Users\\test\\AppData\\Local")
	want := filepath.Join("C:\\Users\\test\\AppData\\Local", "apimgr", "cairnbin", "logs")
	if got := GetLogsDir("cairnbin"); got != want {
		t.Errorf("GetLogsDir windows user: got %q, want %q", got, want)
	}
}

func TestGetCacheDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	_ = os.Unsetenv("CACHE_DIR")
	t.Setenv("LOCALAPPDATA", "C:\\Users\\test\\AppData\\Local")
	want := filepath.Join("C:\\Users\\test\\AppData\\Local", "apimgr", "cairnbin", "cache")
	if got := GetCacheDir("cairnbin"); got != want {
		t.Errorf("GetCacheDir windows user: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	writableCheck = func(string) bool { return false }
	_ = os.Unsetenv("BACKUP_DIR")
	t.Setenv("LOCALAPPDATA", "C:\\Users\\test\\AppData\\Local")
	want := filepath.Join("C:\\Users\\test\\AppData\\Local", "Backups", "apimgr", "cairnbin")
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir windows user: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_WindowsRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "windows"})
	_ = os.Unsetenv("BACKUP_DIR")
	t.Setenv("ProgramData", "C:\\ProgramData")
	want := filepath.Join("C:\\ProgramData", "Backups", "apimgr", "cairnbin")
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir windows root: got %q, want %q", got, want)
	}
}

func TestGetPIDFile_WindowsRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "windows"})
	_ = os.Unsetenv("PID_FILE")
	t.Setenv("ProgramData", "C:\\ProgramData")
	want := filepath.Join("C:\\ProgramData", "apimgr", "cairnbin", "cairnbin.pid")
	if got := GetPIDFile("cairnbin"); got != want {
		t.Errorf("GetPIDFile windows root: got %q, want %q", got, want)
	}
}

// ── EnsureDir with non-root ───────────────────────────────────────────────────

func TestEnsureDir_NonRoot(t *testing.T) {
	oldR := rootCheck
	rootCheck = func() bool { return false }
	defer func() { rootCheck = oldR }()

	// Use the required /tmp/apimgr/cairnbin-XXXXXX structure (PART 28).
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "cairnbin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	target := filepath.Join(dir, "nested")
	if err := EnsureDir(target); err != nil {
		t.Fatalf("EnsureDir non-root: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDir: result is not a directory")
	}
}

// ── Windows privileged paths (PART 4) ─────────────────────────────────────────

func TestWindowsRootPaths(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "windows"})
	for _, k := range []string{"CONFIG_DIR", "DATA_DIR", "CACHE_DIR", "LOG_DIR", "LOGS_DIR", "DATABASE_DIR", "DB_PATH"} {
		_ = os.Unsetenv(k)
	}
	t.Setenv("ProgramData", "C:\\ProgramData")
	base := filepath.Join("C:\\ProgramData", "apimgr", "cairnbin")
	cases := map[string][2]string{
		"config": {GetConfigDir("cairnbin"), base},
		"data":   {GetDataDir("cairnbin"), filepath.Join(base, "data")},
		"cache":  {GetCacheDir("cairnbin"), filepath.Join(base, "cache")},
		"logs":   {GetLogsDir("cairnbin"), filepath.Join(base, "logs")},
		"db":     {GetDBPath("cairnbin"), filepath.Join(base, "db", "server.db")},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Errorf("windows root %s: got %q, want %q", name, c[0], c[1])
		}
	}
}

func TestGetDBPath_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	_ = os.Unsetenv("DATA_DIR")
	_ = os.Unsetenv("DATABASE_DIR")
	_ = os.Unsetenv("DB_PATH")
	want := filepath.Join("/Library", "Application Support", "apimgr", "cairnbin", "db", "server.db")
	if got := GetDBPath("cairnbin"); got != want {
		t.Errorf("GetDBPath darwin root: got %q, want %q", got, want)
	}
}

// TestGetBackupDir_RootFallsBackToDataDir verifies system mode never falls
// back to a $HOME-derived path when the system backup dir is not writable.
func TestGetBackupDir_RootFallsBackToDataDir(t *testing.T) {
	t.Setenv("BACKUP_DIR", "")
	t.Setenv("DATA_DIR", "/var/lib/apimgr/cairnbin")
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	writableCheck = func(string) bool { return false }

	want := "/var/lib/apimgr/cairnbin/backup"
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("GetBackupDir root, system dir unwritable: got %q, want %q", got, want)
	}
}

// TestGetBackupDir_UserPrefersWritableSystemDir verifies user mode uses the
// system backup dir when writable and its own dir otherwise.
func TestGetBackupDir_UserPrefersWritableSystemDir(t *testing.T) {
	t.Setenv("BACKUP_DIR", "")
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})

	if got, want := GetBackupDir("cairnbin"), "/mnt/Backups/apimgr/cairnbin"; got != want {
		t.Errorf("writable system dir: got %q, want %q", got, want)
	}

	writableCheck = func(string) bool { return false }
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "share", "Backups", "apimgr", "cairnbin")
	if got := GetBackupDir("cairnbin"); got != want {
		t.Errorf("unwritable system dir: got %q, want %q", got, want)
	}
}

// TestIsWritable verifies existing writable dirs, creatable paths under them,
// and non-directories.
func TestIsWritable(t *testing.T) {
	dir := t.TempDir()
	if !isWritable(dir) {
		t.Error("existing temp dir should be writable")
	}
	if !isWritable(filepath.Join(dir, "a", "b")) {
		t.Error("not-yet-created path under a writable dir should be writable")
	}
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if isWritable(filepath.Join(file, "sub")) {
		t.Error("path under a regular file must not be writable")
	}
}
