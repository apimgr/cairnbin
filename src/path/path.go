package path

import (
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const orgName = "apimgr"

// Path security errors, per AI.md PART 5 (Path Normalization & Validation).
var (
	ErrPathTraversal = errors.New("path traversal attempt detected")
	ErrInvalidPath   = errors.New("invalid path characters")
	ErrPathTooLong   = errors.New("path exceeds maximum length")
)

// validPathSegment matches a single safe path segment: lowercase
// alphanumeric, hyphens, underscores.
var validPathSegment = regexp.MustCompile(`^[a-z0-9_-]+$`)

// normalizePath cleans a path for safe use: strips leading/trailing
// slashes, collapses repeated slashes, and removes "." / ".." segments.
// Returns "" for empty or still-traversal-containing input.
func normalizePath(input string) string {
	if input == "" {
		return ""
	}

	cleaned := path.Clean(input)
	cleaned = strings.Trim(cleaned, "/")

	if strings.Contains(cleaned, "..") {
		return ""
	}

	return cleaned
}

// validatePathSegment checks a single path segment (e.g. "admin" in
// "/server/admin/dashboard").
func validatePathSegment(segment string) error {
	if segment == "" {
		return ErrInvalidPath
	}
	if len(segment) > 64 {
		return ErrPathTooLong
	}
	if !validPathSegment.MatchString(segment) {
		return ErrInvalidPath
	}
	if segment == "." || segment == ".." {
		return ErrPathTraversal
	}
	return nil
}

// validatePath checks an entire path for traversal attempts, length, and
// per-segment validity.
func validatePath(p string) error {
	if len(p) > 2048 {
		return ErrPathTooLong
	}

	if strings.Contains(p, "..") {
		return ErrPathTraversal
	}

	segments := strings.Split(strings.Trim(p, "/"), "/")
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		if err := validatePathSegment(seg); err != nil {
			return err
		}
	}

	return nil
}

// SafePath normalizes and validates a path, returning an error if it is
// invalid or contains a path traversal attempt. Used for configuration
// values (static_path, etc.), CLI flag paths, and API parameters that
// contain paths. Per AI.md PART 5 — a GLOBAL security rule for all binaries.
func SafePath(input string) (string, error) {
	if err := validatePath(input); err != nil {
		return "", err
	}
	return normalizePath(input), nil
}

// SafeFilePath joins userPath onto baseDir after SafePath validation and
// verifies the resolved absolute path stays within baseDir. Per AI.md PART 5
// (File Path Security).
func SafeFilePath(baseDir, userPath string) (string, error) {
	safe, err := SafePath(userPath)
	if err != nil {
		return "", err
	}

	absPath, err := filepath.Abs(filepath.Join(baseDir, safe))
	if err != nil {
		return "", err
	}

	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(absPath, absBase+string(filepath.Separator)) && absPath != absBase {
		return "", ErrPathTraversal
	}

	return absPath, nil
}

// containerCheck and rootCheck are overridable in tests so that non-container
// and privileged code-paths can be exercised from any environment.
var containerCheck = isContainer
var rootCheck = isRoot

// detectedOS holds the current GOOS — overridable in tests to exercise
// platform-specific branches without running on the target OS.
var detectedOS = runtime.GOOS

// GetConfigDir returns the platform-correct config directory for appName.
// See AI.md PART 4 (OS-Specific Paths) and PART 26 (Docker container paths).
func GetConfigDir(appName string) string {
	if dir := os.Getenv("CONFIG_DIR"); dir != "" {
		return dir
	}

	// Container: spec PART 26 says /config/{project_name}/
	if containerCheck() {
		return filepath.Join("/config", appName)
	}

	if rootCheck() {
		switch detectedOS {
		case "darwin":
			return filepath.Join("/Library", "Application Support", orgName, appName)
		case "windows":
			return filepath.Join(os.Getenv("ProgramData"), orgName, appName)
		case "freebsd", "openbsd", "netbsd":
			return filepath.Join("/usr/local/etc", orgName, appName)
		default:
			return filepath.Join("/etc", orgName, appName)
		}
	}

	switch detectedOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", orgName, appName)
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), orgName, appName)
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", orgName, appName)
	}
}

// GetDataDir returns the platform-correct data directory for appName.
func GetDataDir(appName string) string {
	if dir := os.Getenv("DATA_DIR"); dir != "" {
		return dir
	}

	// Container: spec PART 26 says /data/{project_name}/
	if containerCheck() {
		return filepath.Join("/data", appName)
	}

	if rootCheck() {
		switch detectedOS {
		case "darwin":
			// PART 4 macOS root: /Library/Application Support/{org}/{app}/data/
			return filepath.Join("/Library", "Application Support", orgName, appName, "data")
		case "windows":
			// PART 4 Windows root: %ProgramData%\{org}\{app}\data\
			return filepath.Join(os.Getenv("ProgramData"), orgName, appName, "data")
		case "freebsd", "openbsd", "netbsd":
			// PART 4 BSD root: /var/db/{org}/{app}/
			return filepath.Join("/var/db", orgName, appName)
		default:
			return filepath.Join("/var/lib", orgName, appName)
		}
	}

	switch detectedOS {
	case "darwin":
		// PART 4 macOS user: ~/Library/Application Support/{org}/{app}/
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", orgName, appName)
	case "windows":
		// PART 4 Windows user: %LocalAppData%\{org}\{app}\
		return filepath.Join(os.Getenv("LOCALAPPDATA"), orgName, appName)
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "share", orgName, appName)
	}
}

// GetBackupDir returns the platform-correct backup directory for appName.
// PART 4 / PART 5: prefer system-wide backup path when privileged, user-local otherwise.
func GetBackupDir(appName string) string {
	if dir := os.Getenv("BACKUP_DIR"); dir != "" {
		return dir
	}

	// Container: PART 26 says /data/backups/{project_name}/
	if containerCheck() {
		return filepath.Join("/data", "backups", appName)
	}

	if rootCheck() {
		switch detectedOS {
		case "darwin":
			return filepath.Join("/Library", "Backups", orgName, appName)
		case "windows":
			return filepath.Join(os.Getenv("ProgramData"), "Backups", orgName, appName)
		case "freebsd", "openbsd", "netbsd":
			return filepath.Join("/var", "backups", orgName, appName)
		default:
			return filepath.Join("/mnt", "Backups", orgName, appName)
		}
	}

	switch detectedOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Backups", orgName, appName)
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "Backups", orgName, appName)
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "share", "Backups", orgName, appName)
	}
}

// GetPIDFile returns the platform-correct PID file path for appName.
// PART 4 / PART 8 — privileged installs use a system run dir; user installs
// keep the PID inside the data dir.
func GetPIDFile(appName string) string {
	if p := os.Getenv("PID_FILE"); p != "" {
		return p
	}

	if containerCheck() {
		return filepath.Join(GetDataDir(appName), appName+".pid")
	}

	if rootCheck() {
		switch detectedOS {
		case "windows":
			return filepath.Join(os.Getenv("ProgramData"), orgName, appName, appName+".pid")
		default:
			return filepath.Join("/var", "run", orgName, appName+".pid")
		}
	}

	return filepath.Join(GetDataDir(appName), appName+".pid")
}

// GetLogsDir returns the platform-correct logs directory for appName.
func GetLogsDir(appName string) string {
	// LOG_DIR is the AI.md-canonical name (AI.md 7574); LOGS_DIR is an accepted alias.
	if dir := os.Getenv("LOG_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("LOGS_DIR"); dir != "" {
		return dir
	}

	// Container: spec PART 26 says /data/log/{project_name}/
	if containerCheck() {
		return filepath.Join("/data", "log", appName)
	}

	if rootCheck() {
		switch detectedOS {
		case "darwin":
			// PART 4 macOS root: /Library/Logs/{org}/{app}/
			return filepath.Join("/Library", "Logs", orgName, appName)
		case "windows":
			// PART 4 Windows root: %ProgramData%\{org}\{app}\logs\
			return filepath.Join(os.Getenv("ProgramData"), orgName, appName, "logs")
		default:
			return filepath.Join("/var/log", orgName, appName)
		}
	}

	switch detectedOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Logs", orgName, appName)
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), orgName, appName, "logs")
	default:
		// PART 4 (Linux/BSD user): ~/.local/log/{org}/{app}/
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "log", orgName, appName)
	}
}

// GetCacheDir returns the platform-correct cache directory for appName.
func GetCacheDir(appName string) string {
	if dir := os.Getenv("CACHE_DIR"); dir != "" {
		return dir
	}

	// Container: spec PART 26 says /data/{project_name}/cache/
	if containerCheck() {
		return filepath.Join("/data", appName, "cache")
	}

	if rootCheck() {
		switch detectedOS {
		case "darwin":
			// PART 4 macOS root: /Library/Caches/{org}/{app}/
			return filepath.Join("/Library", "Caches", orgName, appName)
		case "windows":
			// PART 4 Windows root: %ProgramData%\{org}\{app}\cache\
			return filepath.Join(os.Getenv("ProgramData"), orgName, appName, "cache")
		default:
			return filepath.Join("/var/cache", orgName, appName)
		}
	}

	switch detectedOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Caches", orgName, appName)
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), orgName, appName, "cache")
	default:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".cache", orgName, appName)
	}
}

// GetDBPath returns the platform-correct SQLite database file path for appName.
// Container path is /data/db/sqlite/server.db per PART 4; all other platforms
// use {dataDir}/db/server.db.
func GetDBPath(appName string) string {
	if p := os.Getenv("DB_PATH"); p != "" {
		return p
	}
	// DATABASE_DIR overrides only the directory; the file stays server.db (AI.md 7575).
	if dir := os.Getenv("DATABASE_DIR"); dir != "" {
		return filepath.Join(dir, "server.db")
	}

	// Container: PART 4 says /data/db/sqlite/server.db
	if containerCheck() {
		return "/data/db/sqlite/server.db"
	}

	// PART 4: on macOS and Windows the privileged DB directory is a sibling of
	// the data directory ({base}/db), not a child of it ({base}/data/db).
	if os.Getenv("DATA_DIR") == "" && rootCheck() && (detectedOS == "darwin" || detectedOS == "windows") {
		return filepath.Join(filepath.Dir(GetDataDir(appName)), "db", "server.db")
	}

	return filepath.Join(GetDataDir(appName), "db", "server.db")
}

// EnsureDir creates path with sensible default permissions for the running user.
func EnsureDir(path string) error {
	perm := os.FileMode(0o700)
	if rootCheck() {
		perm = 0o755
	}
	return os.MkdirAll(path, perm)
}

// IsContainer reports whether the process is running inside a Linux container.
func IsContainer() bool {
	return isContainer()
}

// isContainer reports whether the process is running inside a Linux container.
func isContainer() bool {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv", "/dev/lxc"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}

	if os.Getenv("container") != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return true
	}

	switch parentProcessName() {
	case "tini", "dumb-init", "s6-svscan", "runsv", "runsvdir", "catatonit", projectBinaryName:
		return true
	}

	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		content := string(data)
		if strings.Contains(content, "docker") ||
			strings.Contains(content, "kubepods") ||
			strings.Contains(content, "containerd") ||
			strings.Contains(content, "lxc") {
			return true
		}
	}

	return false
}

// projectBinaryName is the hardcoded project name used to recognise a parent
// process that is our own entrypoint wrapper.
const projectBinaryName = "pastebin"

// parentProcessName returns the command name of the parent process, read from
// /proc on Linux and via ps elsewhere. It returns "" when it cannot be found.
func parentProcessName() string {
	ppid := os.Getppid()
	if data, err := os.ReadFile("/proc/" + strconv.Itoa(ppid) + "/comm"); err == nil {
		return strings.TrimSpace(string(data))
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(ppid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}
