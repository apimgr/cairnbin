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
	containerCheck = func() bool { return e.isContainer }
	rootCheck = func() bool { return e.isRoot }
	detectedOS = e.goos
	t.Cleanup(func() {
		containerCheck = oldC
		rootCheck = oldR
		detectedOS = oldOS
	})
}

// ── Container paths ───────────────────────────────────────────────────────────

func TestGetConfigDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	os.Unsetenv("CONFIG_DIR")
	if got := GetConfigDir("pastebin"); got != "/config/pastebin" {
		t.Errorf("GetConfigDir container: got %q, want %q", got, "/config/pastebin")
	}
}

func TestGetDataDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	os.Unsetenv("DATA_DIR")
	if got := GetDataDir("pastebin"); got != "/data/pastebin" {
		t.Errorf("GetDataDir container: got %q, want %q", got, "/data/pastebin")
	}
}

func TestGetBackupDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	os.Unsetenv("BACKUP_DIR")
	want := "/data/backups/pastebin"
	if got := GetBackupDir("pastebin"); got != want {
		t.Errorf("GetBackupDir container: got %q, want %q", got, want)
	}
}

func TestGetPIDFile_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	os.Unsetenv("PID_FILE")
	os.Unsetenv("DATA_DIR")
	got := GetPIDFile("pastebin")
	if filepath.Base(got) != "pastebin.pid" {
		t.Errorf("GetPIDFile container: base=%q, want pastebin.pid", filepath.Base(got))
	}
}

func TestGetLogsDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	os.Unsetenv("LOGS_DIR")
	if got := GetLogsDir("pastebin"); got != "/data/log/pastebin" {
		t.Errorf("GetLogsDir container: got %q, want %q", got, "/data/log/pastebin")
	}
}

func TestGetCacheDir_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	os.Unsetenv("CACHE_DIR")
	if got := GetCacheDir("pastebin"); got != "/data/pastebin/cache" {
		t.Errorf("GetCacheDir container: got %q, want %q", got, "/data/pastebin/cache")
	}
}

func TestGetDBPath_Container(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: true})
	os.Unsetenv("DB_PATH")
	if got := GetDBPath("pastebin"); got != "/data/db/sqlite/server.db" {
		t.Errorf("GetDBPath container: got %q, want %q", got, "/data/db/sqlite/server.db")
	}
}

// ── Linux root paths ──────────────────────────────────────────────────────────

func TestGetConfigDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	os.Unsetenv("CONFIG_DIR")
	want := "/etc/apimgr/pastebin"
	if got := GetConfigDir("pastebin"); got != want {
		t.Errorf("GetConfigDir linux root: got %q, want %q", got, want)
	}
}

func TestGetDataDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	os.Unsetenv("DATA_DIR")
	want := "/var/lib/apimgr/pastebin"
	if got := GetDataDir("pastebin"); got != want {
		t.Errorf("GetDataDir linux root: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	os.Unsetenv("BACKUP_DIR")
	want := "/mnt/Backups/apimgr/pastebin"
	if got := GetBackupDir("pastebin"); got != want {
		t.Errorf("GetBackupDir linux root: got %q, want %q", got, want)
	}
}

func TestGetPIDFile_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	os.Unsetenv("PID_FILE")
	want := "/var/run/apimgr/pastebin.pid"
	if got := GetPIDFile("pastebin"); got != want {
		t.Errorf("GetPIDFile linux root: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	os.Unsetenv("LOGS_DIR")
	want := "/var/log/apimgr/pastebin"
	if got := GetLogsDir("pastebin"); got != want {
		t.Errorf("GetLogsDir linux root: got %q, want %q", got, want)
	}
}

func TestGetCacheDir_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	os.Unsetenv("CACHE_DIR")
	want := "/var/cache/apimgr/pastebin"
	if got := GetCacheDir("pastebin"); got != want {
		t.Errorf("GetCacheDir linux root: got %q, want %q", got, want)
	}
}

func TestGetDBPath_LinuxRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "linux"})
	os.Unsetenv("DB_PATH")
	os.Unsetenv("DATA_DIR")
	want := filepath.Join("/var/lib/apimgr/pastebin", "db", "server.db")
	if got := GetDBPath("pastebin"); got != want {
		t.Errorf("GetDBPath linux root: got %q, want %q", got, want)
	}
}

// ── Linux non-root paths (home) ──────────────────────────────────────

// XDG overrides are not part of AI.md PART 4; the fixed home path always wins.
func TestGetConfigDir_LinuxUser_IgnoresXDG(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	os.Unsetenv("CONFIG_DIR")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-ignored")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "apimgr", "pastebin")
	if got := GetConfigDir("pastebin"); got != want {
		t.Errorf("GetConfigDir linux user with XDG set: got %q, want %q", got, want)
	}
}

// XDG overrides are not part of AI.md PART 4; the fixed home path always wins.
func TestGetDataDir_LinuxUser_IgnoresXDG(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	os.Unsetenv("DATA_DIR")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-ignored")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "share", "apimgr", "pastebin")
	if got := GetDataDir("pastebin"); got != want {
		t.Errorf("GetDataDir linux user with XDG set: got %q, want %q", got, want)
	}
}

// XDG overrides are not part of AI.md PART 4; the fixed home path always wins.
func TestGetCacheDir_LinuxUser_IgnoresXDG(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	os.Unsetenv("CACHE_DIR")
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-ignored")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".cache", "apimgr", "pastebin")
	if got := GetCacheDir("pastebin"); got != want {
		t.Errorf("GetCacheDir linux user with XDG set: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_LinuxUser_Home(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	os.Unsetenv("LOGS_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "log", "apimgr", "pastebin")
	if got := GetLogsDir("pastebin"); got != want {
		t.Errorf("GetLogsDir linux user home: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_LinuxUser_Home(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	os.Unsetenv("BACKUP_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".local", "share", "Backups", "apimgr", "pastebin")
	if got := GetBackupDir("pastebin"); got != want {
		t.Errorf("GetBackupDir linux user home: got %q, want %q", got, want)
	}
}

func TestGetPIDFile_LinuxUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "linux"})
	os.Unsetenv("PID_FILE")
	os.Unsetenv("DATA_DIR")
	os.Unsetenv("XDG_DATA_HOME")
	got := GetPIDFile("pastebin")
	if filepath.Base(got) != "pastebin.pid" {
		t.Errorf("GetPIDFile linux user: base=%q, want pastebin.pid", filepath.Base(got))
	}
}

// ── macOS paths ───────────────────────────────────────────────────────────────

func TestGetConfigDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	os.Unsetenv("CONFIG_DIR")
	want := "/Library/Application Support/apimgr/pastebin"
	if got := GetConfigDir("pastebin"); got != want {
		t.Errorf("GetConfigDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetDataDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	os.Unsetenv("DATA_DIR")
	want := "/Library/Application Support/apimgr/pastebin/data"
	if got := GetDataDir("pastebin"); got != want {
		t.Errorf("GetDataDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	os.Unsetenv("LOGS_DIR")
	want := "/Library/Logs/apimgr/pastebin"
	if got := GetLogsDir("pastebin"); got != want {
		t.Errorf("GetLogsDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetCacheDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	os.Unsetenv("CACHE_DIR")
	want := "/Library/Caches/apimgr/pastebin"
	if got := GetCacheDir("pastebin"); got != want {
		t.Errorf("GetCacheDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	os.Unsetenv("BACKUP_DIR")
	want := "/Library/Backups/apimgr/pastebin"
	if got := GetBackupDir("pastebin"); got != want {
		t.Errorf("GetBackupDir darwin root: got %q, want %q", got, want)
	}
}

func TestGetConfigDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	os.Unsetenv("CONFIG_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Application Support", "apimgr", "pastebin")
	if got := GetConfigDir("pastebin"); got != want {
		t.Errorf("GetConfigDir darwin user: got %q, want %q", got, want)
	}
}

func TestGetDataDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	os.Unsetenv("DATA_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Application Support", "apimgr", "pastebin")
	if got := GetDataDir("pastebin"); got != want {
		t.Errorf("GetDataDir darwin user: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	os.Unsetenv("LOGS_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Logs", "apimgr", "pastebin")
	if got := GetLogsDir("pastebin"); got != want {
		t.Errorf("GetLogsDir darwin user: got %q, want %q", got, want)
	}
}

func TestGetCacheDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	os.Unsetenv("CACHE_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Caches", "apimgr", "pastebin")
	if got := GetCacheDir("pastebin"); got != want {
		t.Errorf("GetCacheDir darwin user: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_DarwinUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "darwin"})
	os.Unsetenv("BACKUP_DIR")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "Backups", "apimgr", "pastebin")
	if got := GetBackupDir("pastebin"); got != want {
		t.Errorf("GetBackupDir darwin user: got %q, want %q", got, want)
	}
}

// ── BSD paths ─────────────────────────────────────────────────────────────────

func TestGetConfigDir_FreeBSDRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "freebsd"})
	os.Unsetenv("CONFIG_DIR")
	want := "/usr/local/etc/apimgr/pastebin"
	if got := GetConfigDir("pastebin"); got != want {
		t.Errorf("GetConfigDir freebsd root: got %q, want %q", got, want)
	}
}

func TestGetDataDir_FreeBSDRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "freebsd"})
	os.Unsetenv("DATA_DIR")
	want := "/var/db/apimgr/pastebin"
	if got := GetDataDir("pastebin"); got != want {
		t.Errorf("GetDataDir freebsd root: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_FreeBSDRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "freebsd"})
	os.Unsetenv("BACKUP_DIR")
	want := "/var/backups/apimgr/pastebin"
	if got := GetBackupDir("pastebin"); got != want {
		t.Errorf("GetBackupDir freebsd root: got %q, want %q", got, want)
	}
}

// ── Windows paths ─────────────────────────────────────────────────────────────

func TestGetConfigDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	os.Unsetenv("CONFIG_DIR")
	t.Setenv("APPDATA", "C:\\Users\\test\\AppData\\Roaming")
	want := filepath.Join("C:\\Users\\test\\AppData\\Roaming", "apimgr", "pastebin")
	if got := GetConfigDir("pastebin"); got != want {
		t.Errorf("GetConfigDir windows user: got %q, want %q", got, want)
	}
}

func TestGetDataDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	os.Unsetenv("DATA_DIR")
	t.Setenv("LOCALAPPDATA", "C:\\Users\\test\\AppData\\Local")
	want := filepath.Join("C:\\Users\\test\\AppData\\Local", "apimgr", "pastebin")
	if got := GetDataDir("pastebin"); got != want {
		t.Errorf("GetDataDir windows user: got %q, want %q", got, want)
	}
}

func TestGetLogsDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	os.Unsetenv("LOGS_DIR")
	t.Setenv("LOCALAPPDATA", "C:\\Users\\test\\AppData\\Local")
	want := filepath.Join("C:\\Users\\test\\AppData\\Local", "apimgr", "pastebin", "logs")
	if got := GetLogsDir("pastebin"); got != want {
		t.Errorf("GetLogsDir windows user: got %q, want %q", got, want)
	}
}

func TestGetCacheDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	os.Unsetenv("CACHE_DIR")
	t.Setenv("LOCALAPPDATA", "C:\\Users\\test\\AppData\\Local")
	want := filepath.Join("C:\\Users\\test\\AppData\\Local", "apimgr", "pastebin", "cache")
	if got := GetCacheDir("pastebin"); got != want {
		t.Errorf("GetCacheDir windows user: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_WindowsUser(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: false, goos: "windows"})
	os.Unsetenv("BACKUP_DIR")
	t.Setenv("LOCALAPPDATA", "C:\\Users\\test\\AppData\\Local")
	want := filepath.Join("C:\\Users\\test\\AppData\\Local", "Backups", "apimgr", "pastebin")
	if got := GetBackupDir("pastebin"); got != want {
		t.Errorf("GetBackupDir windows user: got %q, want %q", got, want)
	}
}

func TestGetBackupDir_WindowsRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "windows"})
	os.Unsetenv("BACKUP_DIR")
	t.Setenv("ProgramData", "C:\\ProgramData")
	want := filepath.Join("C:\\ProgramData", "Backups", "apimgr", "pastebin")
	if got := GetBackupDir("pastebin"); got != want {
		t.Errorf("GetBackupDir windows root: got %q, want %q", got, want)
	}
}

func TestGetPIDFile_WindowsRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "windows"})
	os.Unsetenv("PID_FILE")
	t.Setenv("ProgramData", "C:\\ProgramData")
	want := filepath.Join("C:\\ProgramData", "apimgr", "pastebin", "pastebin.pid")
	if got := GetPIDFile("pastebin"); got != want {
		t.Errorf("GetPIDFile windows root: got %q, want %q", got, want)
	}
}

// ── EnsureDir with non-root ───────────────────────────────────────────────────

func TestEnsureDir_NonRoot(t *testing.T) {
	oldR := rootCheck
	rootCheck = func() bool { return false }
	defer func() { rootCheck = oldR }()

	// Use the required /tmp/apimgr/pastebin-XXXXXX structure (PART 28).
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "pastebin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

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
		os.Unsetenv(k)
	}
	t.Setenv("ProgramData", "C:\\ProgramData")
	base := filepath.Join("C:\\ProgramData", "apimgr", "pastebin")
	cases := map[string][2]string{
		"config": {GetConfigDir("pastebin"), base},
		"data":   {GetDataDir("pastebin"), filepath.Join(base, "data")},
		"cache":  {GetCacheDir("pastebin"), filepath.Join(base, "cache")},
		"logs":   {GetLogsDir("pastebin"), filepath.Join(base, "logs")},
		"db":     {GetDBPath("pastebin"), filepath.Join(base, "db", "server.db")},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Errorf("windows root %s: got %q, want %q", name, c[0], c[1])
		}
	}
}

func TestGetDBPath_DarwinRoot(t *testing.T) {
	applyEnv(t, pathEnv{isContainer: false, isRoot: true, goos: "darwin"})
	os.Unsetenv("DATA_DIR")
	os.Unsetenv("DATABASE_DIR")
	os.Unsetenv("DB_PATH")
	want := filepath.Join("/Library", "Application Support", "apimgr", "pastebin", "db", "server.db")
	if got := GetDBPath("pastebin"); got != want {
		t.Errorf("GetDBPath darwin root: got %q, want %q", got, want)
	}
}
