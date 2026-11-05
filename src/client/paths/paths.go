// Package paths resolves the CLI client's cli.yml location across platforms
// and the --config profile-selection flow (PART 32).
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	internalOrg  = "apimgr"
	internalName = "cairnbin"
)

// ActiveConfigPath, when non-empty, overrides the default cli.yml path.
// It is set from the --config flag (profile name or explicit path) during
// startup, before cli.yml is loaded, so the chosen profile feeds every
// flag default. Empty means "use ConfigFile()".
var ActiveConfigPath string

// ConfigFile returns the platform-correct path to cli.yml.
// The CLI always uses user-scope directories regardless of privilege level;
// it never falls back to system directories like /etc/.
func ConfigFile() string {
	if p := os.Getenv("CLI_CONFIG"); p != "" {
		return p
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), internalOrg, internalName, "cli.yml")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", internalOrg, internalName, "cli.yml")
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, internalOrg, internalName, "cli.yml")
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", internalOrg, internalName, "cli.yml")
	}
}

// DataDir returns the invoking user's persistent data directory.
func DataDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("LOCALAPPDATA"), internalOrg, internalName, "data")
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "share", internalOrg, internalName)
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, internalOrg, internalName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", internalOrg, internalName)
}

// CacheDir returns the invoking user's cache directory for API responses.
func CacheDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("LOCALAPPDATA"), internalOrg, internalName, "cache")
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".cache", internalOrg, internalName)
	}
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, internalOrg, internalName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", internalOrg, internalName)
}

// LogDir returns the invoking user's log directory.
func LogDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("LOCALAPPDATA"), internalOrg, internalName, "log")
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "log", internalOrg, internalName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "log", internalOrg, internalName)
}

// LogFile returns the default per-user CLI log path.
func LogFile() string {
	return filepath.Join(LogDir(), "cli.log")
}

// Resolved returns the active config path, falling back to the default
// cli.yml location when no --config profile/path was selected.
func Resolved() string {
	if ActiveConfigPath != "" {
		return ActiveConfigPath
	}
	return ConfigFile()
}

// PrescanConfigFlag scans args for --config NAME / --config=NAME before
// flag.Parse runs, so the selected profile can be loaded first (PART 32).
func PrescanConfigFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--config" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if strings.HasPrefix(a, "--config=") {
			return strings.TrimPrefix(a, "--config=")
		}
	}
	return ""
}

// Resolve maps a --config value to a concrete file path (PART 32):
//   - empty         → default cli.yml
//   - ~ or absolute → expanded, then extension-resolved
//   - relative name → {config_dir}/{name}, then extension-resolved
func Resolve(name string) string {
	if name == "" {
		return ConfigFile()
	}
	if strings.HasPrefix(name, "~") {
		home, _ := os.UserHomeDir()
		name = filepath.Join(home, strings.TrimPrefix(name, "~"))
	}
	if filepath.IsAbs(name) {
		return resolveYamlExtension(name)
	}
	dir := filepath.Dir(ConfigFile())
	return resolveYamlExtension(filepath.Join(dir, name))
}

// resolveYamlExtension applies PART 32 rules 3-5: an explicit .yml/.yaml (or any
// other) extension is kept as-is; an extensionless path prefers an existing
// .yml, then .yaml, defaulting to .yml for a new config.
func resolveYamlExtension(path string) string {
	switch filepath.Ext(path) {
	case ".yml", ".yaml":
		return path
	case "":
		if fileExists(path + ".yml") {
			return path + ".yml"
		}
		if fileExists(path + ".yaml") {
			return path + ".yaml"
		}
		return path + ".yml"
	default:
		return path
	}
}

// fileExists reports whether path exists and is not a directory.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// EnsureDirs creates the standard user-scope directories for the CLI client
// (config, data, cache, log). Called at startup before any config is loaded.
func EnsureDirs() {
	dirs := []string{
		filepath.Dir(ConfigFile()),
		DataDir(),
		CacheDir(),
		LogDir(),
	}
	for _, d := range dirs {
		os.MkdirAll(d, 0o700)
		// PART 32 CLI Startup Sequence step 2: re-assert 0700 on every startup —
		// MkdirAll leaves an already-existing directory's permissions untouched.
		if runtime.GOOS != "windows" {
			os.Chmod(d, 0o700)
		}
	}
}
