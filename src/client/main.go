// pastebin-cli — command-line client for the pastebin service.
//
// Usage:
//
//	pastebin-cli [--server URL] [--output FORMAT] <command> [flags] [args]
//
// Commands:
//
//	create [file]             Create paste from stdin or file
//	get <id>                  Fetch raw paste content
//	delete <id> <token>       Delete paste using delete token
//	list [--limit N]          List recent public pastes
//	update                    Check for and apply CLI updates
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/apimgr/pastebin/src/client/paths"
	"github.com/apimgr/pastebin/src/client/tui"
	"github.com/apimgr/pastebin/src/common/i18n"
	"github.com/apimgr/pastebin/src/config"
	"github.com/apimgr/pastebin/src/shell"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Version, CommitID, BuildEpoch, and OfficialSite are injected at build time via
// -ldflags. BuildDate is NOT an ldflag itself — it is derived from BuildEpoch in
// init(), matching the server binary's pattern (see src/main.go).
var (
	Version  = "devel"
	CommitID = "N/A"
	// BuildDate is derived from BuildEpoch in init(); stays "N/A" when BuildEpoch is unset.
	BuildDate = "N/A"
	// BuildEpoch is the Unix build timestamp (seconds, UTC) set via -ldflags; "0" when unset.
	BuildEpoch   = "0"
	OfficialSite = ""
)

// init derives BuildDate (RFC 3339 UTC) from the embedded BuildEpoch.
func init() {
	if n, err := strconv.ParseInt(BuildEpoch, 10, 64); err == nil && n > 0 {
		BuildDate = time.Unix(n, 0).UTC().Format("2006-01-02T15:04:05Z")
	}
}

// projectName is the hardcoded internal name used for User-Agent and config paths.
// Display uses binName() per PART 32.
const projectName = "pastebin"

// apiVersion is the {api_version} route segment (PART 14) this CLI targets.
// Centralized here rather than scattered per call site; matches the server's
// default (config.DefaultAPIVersion) since the CLI is compiled against the
// same project's API surface.
const apiVersion = "v1"

// Exit codes per PART 32.
const (
	exitSuccess    = 0
	exitGeneral    = 1
	exitConfig     = 2
	exitConnection = 3
	exitAuth       = 4
	exitNotFound   = 5
	exitUsage      = 64
)

// uiLang is the resolved output locale for every translated CLI string
// (PART 30). It stays "en" until main() resolves the --lang flag, cli.yml, and
// the LANG/LC_ALL environment chain; the error printers below are called from
// paths that have no locale argument, so the active language lives here.
var uiLang = "en"

// setUILang stores the active output locale, silently falling back to English
// for any unsupported code (PART 30 forbids erroring on an unknown language).
func setUILang(lang string) {
	if i18n.IsSupported(lang) {
		uiLang = lang
		return
	}
	uiLang = "en"
}

// t returns the translated cli.client.{key} string for the active locale.
func t(key string) string {
	return i18n.Translate(uiLang, "cli.client."+key)
}

// tf returns the translated cli.client.{key} string for the active locale with
// {variable} placeholders replaced from alternating name/value arguments.
func tf(key string, args ...interface{}) string {
	return i18n.TranslateFormat(uiLang, "cli.client."+key, i18n.Args(args...))
}

// binName is the actual (possibly renamed) binary name shown in help, version,
// and error output per PART 32; the User-Agent keeps the hardcoded projectName.
func binName() string {
	return filepath.Base(os.Args[0])
}

// printConnectionError prints the PART 32 "Error Messages" connection-error
// format (multi-line: reason + two hints) and exits exitConnection. serverURL
// is the resolved --server value shown in the message; detail is the
// underlying error, appended so operators keep diagnostic detail the AI.md
// example omits but every other error path in this CLI includes.
func printConnectionError(serverURL string, detail error) {
	fmt.Fprintln(os.Stderr, tf("err_connect", "server", serverURL))
	fmt.Fprintln(os.Stderr, "  "+t("err_connect_hint_network"))
	fmt.Fprintln(os.Stderr, "  "+t("err_connect_hint_server"))
	if detail != nil {
		fmt.Fprintf(os.Stderr, "  (%v)\n", detail)
	}
	os.Exit(exitConnection)
}

// printAuthError prints the PART 32 "Error Messages" auth-error format
// (multi-line: reason + two hints) and exits exitAuth.
func printAuthError() {
	fmt.Fprintln(os.Stderr, t("err_auth"))
	fmt.Fprintln(os.Stderr, "  "+t("err_auth_hint_invalid"))
	fmt.Fprintln(os.Stderr, "  "+t("err_auth_hint_update"))
	os.Exit(exitAuth)
}

// printNotFoundError prints the PART 32 "Error Messages" not-found format
// (single line) and exits exitNotFound.
func printNotFoundError(resource string) {
	fmt.Fprintln(os.Stderr, tf("err_not_found", "resource", resource))
	os.Exit(exitNotFound)
}

// ─── CLI config (cli.yml) ─────────────────────────────────────────────────────

// cliConfig mirrors the complete structure of cli.yml (PART 32).
type cliConfig struct {
	Server struct {
		Primary    string `yaml:"primary"`
		APIVersion string `yaml:"api_version"`
		Timeout    string `yaml:"timeout"`
		Retry      int    `yaml:"retry"`
		RetryDelay string `yaml:"retry_delay"`
	} `yaml:"server"`
	Update struct {
		Auto          bool   `yaml:"auto"`
		CheckInterval string `yaml:"check_interval"`
		Channel       string `yaml:"channel"`
	} `yaml:"update"`
	Display struct {
		Mode string `yaml:"mode"`
	} `yaml:"display"`
	Auth struct {
		Token     string `yaml:"token"`
		TokenFile string `yaml:"token_file"`
	} `yaml:"auth"`
	Output struct {
		Format  string `yaml:"format"`
		Color   string `yaml:"color"`
		Pager   string `yaml:"pager"`
		Quiet   bool   `yaml:"quiet"`
		Verbose bool   `yaml:"verbose"`
	} `yaml:"output"`
	TUI struct {
		Enabled bool   `yaml:"enabled"`
		Theme   string `yaml:"theme"`
		Mouse   bool   `yaml:"mouse"`
		Unicode bool   `yaml:"unicode"`
	} `yaml:"tui"`
	Logging struct {
		Level    string `yaml:"level"`
		File     string `yaml:"file"`
		MaxSize  string `yaml:"max_size"`
		MaxFiles int    `yaml:"max_files"`
	} `yaml:"logging"`
	Cache struct {
		Enabled bool   `yaml:"enabled"`
		TTL     string `yaml:"ttl"`
		MaxSize string `yaml:"max_size"`
	} `yaml:"cache"`
	Debug    bool `yaml:"debug"`
	Defaults struct {
		Lang   string `yaml:"lang"`
		Public string `yaml:"public"`
		Expire string `yaml:"expire"`
		Syntax string `yaml:"syntax"`
		Output string `yaml:"output,omitempty"`
		Limit  int    `yaml:"limit"`
	} `yaml:"defaults"`
}

// loadCLIConfig reads cli.yml; returns zero-value config if absent.
func loadCLIConfig() (cliConfig, error) {
	var cfg cliConfig
	cfg.Server.APIVersion = apiVersion
	cfg.Server.Timeout = "30s"
	cfg.Server.Retry = 3
	cfg.Server.RetryDelay = "1s"
	cfg.Update.Channel = "stable"
	cfg.Update.CheckInterval = "per_invocation"
	cfg.Display.Mode = "auto"
	cfg.Output.Format = "table"
	cfg.Output.Color = "auto"
	cfg.Output.Pager = "auto"
	cfg.TUI.Enabled = true
	cfg.TUI.Unicode = true
	cfg.TUI.Mouse = true
	cfg.Logging.Level = "warn"
	cfg.Logging.MaxSize = "10MB"
	cfg.Logging.MaxFiles = 5
	cfg.Cache.Enabled = true
	cfg.Cache.TTL = "5m"
	cfg.Cache.MaxSize = "100MB"
	cfg.Defaults.Expire = "never"
	cfg.Defaults.Syntax = "auto"
	cfg.Defaults.Public = "unlisted"
	cfg.Defaults.Limit = 20

	data, err := os.ReadFile(paths.Resolved())
	if err != nil {
		if os.IsNotExist(err) {
			// PART 32: cli.yml is auto-created on first run with sane defaults.
			// Failure to write is non-fatal — the defaults still apply in memory.
			if werr := saveCLIConfig(cfg); werr != nil {
				log.Println("warning: " + tf("warn_save_config", "error", werr))
			}
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", t("err_parse_config"), err)
	}
	return cfg, nil
}

// saveCLIConfig writes cfg to cli.yml, creating parent dirs as needed.
func saveCLIConfig(cfg cliConfig) error {
	p := paths.Resolved()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	// cli.yml holds the API token as well as server connection config —
	// create it with user-only permissions (PART 32).
	if err := os.WriteFile(p, data, 0o600); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(p, 0o600)
	}
	return nil
}

// saveIfUnset updates dst with src when dst is empty or invalid, and
// returns both the resolved value and whether it should be persisted.
// Implements PART 32 Flag-to-Config Save Rules.
func saveIfUnset(current, flagValue string, validate func(string) bool) (resolved string, persist bool) {
	if flagValue == "" {
		return current, false
	}
	if !validate(flagValue) {
		log.Println("warning: " + tf("warn_invalid_server_url", "value", strconv.Quote(flagValue)))
		return current, false
	}
	if current == "" {
		return flagValue, true
	}
	if !validate(current) {
		return flagValue, true
	}
	return flagValue, false
}

// isValidURL is the validate function for server URLs.
func isValidURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// readTokenFile reads an API token from path (PART 32 --token-file / auth.token_file),
// trimming surrounding whitespace so a trailing newline from the file doesn't
// end up in the Authorization header.
func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", t("err_read_token_file"), err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New(tf("err_token_file_empty", "path", path))
	}
	return token, nil
}

// ─── Display mode detection ───────────────────────────────────────────────────

// argsAreConfigOnly reports whether args holds nothing but the config-only
// flags — no command name and no flag outside the config allowlist. Those are
// exactly the invocations that stay interactive (`pastebin-cli`,
// `pastebin-cli --server URL`) rather than dispatching a command.
func argsAreConfigOnly(args []string) bool {
	// Config-only flags that still allow an interactive launch (value: flag
	// consumes the next arg).
	configFlags := map[string]bool{
		"--config": true, "--server": true, "--token": true, "--token-file": true,
		"--debug": true, "--color": true, "--json": true, "--lang": true,
		"--output": true,
	}
	valueFlags := map[string]bool{
		"--config": true, "--server": true, "--token": true, "--token-file": true,
		"--output": true, "--color": true, "--lang": true,
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			return false
		}
		parts := strings.SplitN(arg, "=", 2)
		if !configFlags[parts[0]] {
			return false
		}
		// Space syntax: skip the flag's value (--flag value).
		if valueFlags[parts[0]] && !strings.Contains(arg, "=") && i+1 < len(args) {
			i++
		}
	}
	return true
}

// detectMode returns "tui", "gui", "cli", or "plain" based on environment and
// args. Implements PART 32 Automatic Mode Detection rules. displayMode is the
// cli.yml `display.mode` override ("auto" (default), "tui", or "gui"); it
// never comes from a CLI flag — PART 32 forbids --tui/--gui/--mode-ui flags.
func detectMode(args []string, displayMode string) string {
	// Exit-immediately flags — never TUI.
	for _, arg := range args {
		switch arg {
		case "-h", "--help", "-v", "--version":
			return "cli"
		}
	}

	// A command (or a non-config flag) always wins over any display mode:
	// `pastebin-cli list` must print a list, not open a window, even with
	// display.mode: gui set. GUI/TUI are only ever candidates for the
	// config-only invocations, which is the same set the PART 32 table
	// describes as "Interactive terminal + config flags only".
	if !argsAreConfigOnly(args) {
		// Not a terminal → plain output (piped, cron, scripts).
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			return "plain"
		}
		return "cli"
	}

	// Config override: force TUI even when stdout isn't a terminal.
	if displayMode == "tui" {
		return "tui"
	}

	// Config override: force the native GUI. This is checked before the
	// stdout-is-a-terminal test because the GUI draws into its own window
	// and does not care whether stdout is a TTY — main reports the error
	// when no display is actually present, per the PART 32 table's
	// "gui → error if no display available".
	if displayMode == "gui" {
		return "gui"
	}

	// Auto-detect: a real display outranks the terminal, per the PART 32
	// table ("auto → Detect: GUI if display, TUI if terminal, error if
	// neither"). Checked after the exit-immediately and config-override
	// rules above, and before the terminal test below.
	if guiAvailable() {
		return "gui"
	}

	// Not a terminal → plain output (piped, cron, scripts).
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return "plain"
	}

	return "tui"
}

// ─── Auto-update via autodiscover ────────────────────────────────────────────

// autodiscoverResponse is the subset of /api/autodiscover we need.
type autodiscoverResponse struct {
	CLIVersions   map[string]cliVersionInfo `json:"cli_versions"`
	CLIMinVersion string                    `json:"cli_min_version"`
}

type cliVersionInfo struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// printVersionInfo prints the CLI's own version line and, when a server URL
// is configured and reachable, the AI.md PART 32 "--version Extended Output"
// block: the server URL, a version-compatibility check against
// GET /api/{api_version}/server/version, and the Go/OS build info. The
// extended block is best-effort — any lookup failure silently falls back to
// the base version line only, matching checkCLIUpdate's non-fatal contract.
func printVersionInfo(serverURL string) {
	fmt.Println(tf("version_line", "binary", binName(), "version", Version, "commit", CommitID, "date", BuildDate))

	if serverURL == "" {
		return
	}
	serverURL = strings.TrimRight(serverURL, "/")

	httpClient := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, serverURL+"/api/"+apiVersion+"/server/version", nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s-cli/%s", projectName, Version))

	resp, err := httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var payload struct {
		OK   bool `json:"ok"`
		Data struct {
			Version   string `json:"version"`
			GoVersion string `json:"go_version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil || !payload.OK || payload.Data.Version == "" {
		return
	}

	compat := t("version_compatible")
	cliMajor := strings.SplitN(Version, ".", 2)[0]
	serverMajor := strings.SplitN(payload.Data.Version, ".", 2)[0]
	if Version != "devel" && payload.Data.Version != "unknown" && cliMajor != serverMajor {
		compat = t("version_incompatible")
	}

	fmt.Println()
	fmt.Println(tf("version_server", "server", serverURL))
	fmt.Println(tf("version_server_version", "version", payload.Data.Version, "compat", compat))
	fmt.Println()
	fmt.Println(t("version_build_info"))
	fmt.Println("  " + tf("version_go", "version", runtime.Version()))
	fmt.Println("  " + tf("version_os_arch", "os", runtime.GOOS, "arch", runtime.GOARCH))
}

// resolveOutputFormat combines the legacy --json boolean alias with the
// PART 32 "Output Formats" --output flag (json, table, plain) into a single
// resolved value. An explicit --output wins over --json; an unrecognized
// --output value falls back to "plain" rather than erroring, matching this
// codebase's config.ParseBool-style default-on-invalid convention.
func resolveOutputFormat(asJSON bool, output string) string {
	switch strings.ToLower(strings.TrimSpace(output)) {
	case "json":
		return "json"
	case "yaml", "yml":
		return "yaml"
	case "csv":
		return "csv"
	case "table":
		return "table"
	case "plain":
		return "plain"
	}
	if asJSON {
		return "json"
	}
	return "plain"
}

func normalizeExpiry(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "never" || value == "" {
		return "never", nil
	}
	if parsed, err := config.ParseBool(value, true); err == nil && !parsed {
		return "never", nil
	}
	switch value {
	case "1h", "1d", "1w", "1m", "3m", "6m", "1y", "18m", "2y":
		return value, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 1 || seconds > 10*365*24*60*60 {
		return "", fmt.Errorf("invalid expiry")
	}
	return strconv.FormatInt(seconds, 10), nil
}

// checkCLIUpdate queries /api/autodiscover and enforces cli_min_version.
// It logs a notice when a newer version is available but does not auto-update
// (cli.yml update.auto defaults to false for the CLI per PART 32).
// Returns an error only when Version < cli_min_version (must refuse further requests).
func checkCLIUpdate(serverURL, lang string) error {
	if serverURL == "" || Version == "devel" {
		return nil
	}

	httpClient := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(serverURL, "/")+"/api/autodiscover", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s-cli/%s", projectName, Version))
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var disc autodiscoverResponse
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		return nil
	}

	// Enforce minimum version requirement.
	if disc.CLIMinVersion != "" && versionLessThan(Version, disc.CLIMinVersion) {
		return errors.New(tf("update_too_old", "version", disc.CLIMinVersion, "binary", binName()))
	}

	// Notify when a newer version is available.
	osArch := runtime.GOOS + "-" + runtime.GOARCH
	if info, ok := disc.CLIVersions[osArch]; ok {
		if versionLessThan(Version, info.Version) {
			fmt.Fprintln(os.Stderr, tf("update_notice",
				"binary", binName(), "version", info.Version, "current", Version))
		}
	}

	return nil
}

// versionLessThan returns true when semver a < b.
// Compares MAJOR.MINOR.PATCH numerically so "0.9.0" < "0.10.0" is correct.
// Returns false for non-numeric or special versions (dev, unknown).
func versionLessThan(a, b string) bool {
	if a == "devel" || b == "devel" || a == "dev" || b == "dev" || a == "unknown" || b == "unknown" {
		return false
	}
	aParts := strings.SplitN(a, ".", 3)
	bParts := strings.SplitN(b, ".", 3)
	for len(aParts) < 3 {
		aParts = append(aParts, "0")
	}
	for len(bParts) < 3 {
		bParts = append(bParts, "0")
	}
	for i := range 3 {
		av, ae := strconv.Atoi(aParts[i])
		bv, be := strconv.Atoi(bParts[i])
		if ae != nil || be != nil {
			// Non-numeric component — fall back to string compare for this segment.
			c := strings.Compare(aParts[i], bParts[i])
			if c != 0 {
				return c < 0
			}
			continue
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

// ─── Entry point ─────────────────────────────────────────────────────────────

func main() {
	log.SetFlags(0)
	log.SetPrefix(binName() + ": ")

	paths.EnsureDirs()

	// PART 32: --config selects a named profile (or explicit path) before
	// cli.yml is loaded, so the chosen profile feeds every flag default.
	if name := paths.PrescanConfigFlag(os.Args[1:]); name != "" {
		paths.ActiveConfigPath = paths.Resolve(name)
	}

	// Load cli.yml.
	fileCfg, cfgErr := loadCLIConfig()
	fileCfg = applyCLIConfigEnv(fileCfg)
	initializeLogging(fileCfg)
	if cfgErr != nil {
		// A present but unreadable or malformed cli.yml is a configuration
		// failure, not a network/runtime warning. PART 32 reserves exit code 2
		// for this case so scripts can distinguish it from connection failures.
		// Help/version/shell must never be gated behind this, so the exit is
		// deferred until after those branches have had their chance to run;
		// fileCfg stays zero-valued and supplies only compiled defaults.
		log.Println(tf("err_load_config", "error", cfgErr))
	}

	server := flag.String("server", envOrDefault("PASTEBIN_SERVER_PRIMARY", fileCfg.Server.Primary), "server base URL")
	// PART 32 config precedence: CLI flag > env var > cli.yml > compiled default.
	jsonDefault := envOrDefault("PASTEBIN_OUTPUT_FORMAT", fileCfg.Output.Format) == "json"
	asJSON := flag.Bool("json", jsonDefault, "machine-readable JSON output (alias for --output json)")
	// PART 32 "Output Formats": json, table, plain. --json remains as a
	// back-compat boolean alias, folded into the resolved format below.
	outputDefault := firstNonEmpty(fileCfg.Defaults.Output, fileCfg.Output.Format)
	outputFlag := flag.String("output", envOrDefault("PASTEBIN_FORMAT", envOrDefault("PASTEBIN_OUTPUT_FORMAT", firstNonEmpty(outputDefault, "plain"))), "output format: json, yaml, table, plain, csv")
	colorFlag := flag.String("color", envOrDefault("PASTEBIN_OUTPUT_COLOR", firstNonEmpty(fileCfg.Output.Color, "auto")), "color output: auto, yes, no")
	showVersion := flag.Bool("version", false, "print version and exit")
	showHelp := flag.Bool("help", false, "show help and exit")
	debugDefault, _ := config.ParseBool(envOrDefault("PASTEBIN_DEBUG", ""), fileCfg.Debug)
	debugFlag := flag.Bool("debug", debugDefault, "enable debug output")
	verboseDefault, _ := config.ParseBool(envOrDefault("PASTEBIN_OUTPUT_VERBOSE", ""), fileCfg.Output.Verbose)
	verboseFlag := flag.Bool("verbose", verboseDefault, "show extra request diagnostics")
	quietDefault, _ := config.ParseBool(envOrDefault("PASTEBIN_OUTPUT_QUIET", ""), fileCfg.Output.Quiet)
	doUpdate := flag.String("update", "", "check for CLI updates: 'check' or 'yes'")
	// PART 32: --lang sets the output/UI language; default auto-detects from the LANG env var.
	langFlag := flag.String("lang", envOrDefault("PASTEBIN_LANG", "auto"), "output language code (default: auto-detect from LANG)")
	// PART 32: operator/owner API token. Priority: --token flag → PASTEBIN_TOKEN env → cli.yml auth.token.
	tokenFlag := flag.String("token", "", "operator/owner API token (or set PASTEBIN_TOKEN)")
	// PART 32: alternative to --token — read the token from a file instead of the command line.
	tokenFileFlag := flag.String("token-file", "", "read API token from file")
	// PART 32: --config selects a named profile or explicit path to cli.yml.
	// Pre-scanned above so it applies before config load; declared here so
	// flag.Parse accepts it and --help lists it.
	flag.String("config", "", "config profile name or path (default: cli.yml)")

	// -h and -v are aliases for --help and --version.
	flag.BoolVar(showHelp, "h", false, "show help and exit")
	flag.BoolVar(showVersion, "v", false, "print version and exit")

	flag.Usage = printUsage
	// ContinueOnError instead of the default ExitOnError: the stdlib flag
	// package hardcodes os.Exit(2) on a parse error, which collides with
	// this CLI's own exitConfig=2 (PART 32/8 exit codes reserve 2 for
	// configuration errors, not bad arguments). A malformed flag is a
	// usage error and must exit exitUsage=64 instead.
	flag.CommandLine.Init(os.Args[0], flag.ContinueOnError)
	if err := flag.CommandLine.Parse(os.Args[1:]); err != nil {
		os.Exit(exitUsage)
	}

	// Honour NO_COLOR env var (https://no-color.org/) and --color flag.
	// Spec canonical values: auto, yes, no (AI.md PART 8).
	// always/never are accepted as backward-compatible aliases.
	switch *colorFlag {
	case "never", "no":
		os.Setenv("NO_COLOR", "1")
	case "always", "yes":
		os.Unsetenv("NO_COLOR")
	}

	// PART 8: TERM=dumb forces plain output — no colors, no emoji, no ANSI.
	if os.Getenv("TERM") == "dumb" {
		os.Setenv("NO_COLOR", "1")
	}

	// PART 30 CLI detection chain: --lang flag > cli.yml > LANG/LC_ALL > en.
	// Resolved before --help/--version/--shell so every branch below is
	// translated; unsupported codes silently fall back to English.
	locale := detectLocale(*langFlag, fileCfg.Defaults.Lang)
	setUILang(locale)

	if *showHelp {
		printUsage()
		return
	}

	if *showVersion {
		printVersionInfo(*server)
		return
	}

	if *debugFlag || *verboseFlag {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}

	args := flag.Args()
	if len(args) == 0 && !term.IsTerminal(int(os.Stdin.Fd())) {
		args = []string{"create"}
	}

	// --shell completions / --shell init — handle before server check.
	if len(args) >= 1 && args[0] == "--shell" {
		shellArg := ""
		if len(args) >= 2 {
			shellArg = args[1]
		}
		switch shellArg {
		case "--help", "help", "":
			shell.PrintHelp(binName())
		case "init":
			shellShell := ""
			if len(args) >= 3 {
				shellShell = args[2]
			}
			if err := shell.PrintInit(binName(), shellShell); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_shell_init", "error", err))
				os.Exit(exitUsage)
			}
		case "completions":
			shellShell := ""
			if len(args) >= 3 {
				shellShell = args[2]
			}
			if err := shell.PrintClientCompletions(binName(), shellShell); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_shell_completions", "error", err))
				os.Exit(exitUsage)
			}
		default:
			fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("shell_unknown_sub", "sub", strconv.Quote(shellArg)))
			fmt.Fprintln(os.Stderr, tf("shell_try_help", "binary", binName()))
			os.Exit(exitUsage)
		}
		return
	}

	// --help/--version/--shell are served above and are never gated on a
	// readable cli.yml. Everything past this point needs working config, so a
	// malformed file exits with the configuration code (PART 32).
	if cfgErr != nil {
		os.Exit(exitConfig)
	}

	// Apply saveIfUnset: persist server to cli.yml when config was empty or invalid.
	// Use the current parsed value of --server as the flagValue.
	resolved, shouldPersist := saveIfUnset(fileCfg.Server.Primary, *server, isValidURL)
	if shouldPersist && resolved != "" {
		fileCfg.Server.Primary = resolved
		if err := saveCLIConfig(fileCfg); err != nil {
			log.Println("warning: " + tf("warn_save_config", "error", err))
		}
	}
	if resolved != "" {
		*server = resolved
	}

	*server = defaultServerURL(*server, OfficialSite)

	// Resolve the API token (PART 32 priority): --token flag → --token-file flag →
	// PASTEBIN_TOKEN env → cli.yml auth.token → cli.yml auth.token_file.
	// The env var never persists; the --token/--token-file flags save to cli.yml
	// only when the stored value is empty/invalid.
	token := *tokenFlag
	if token == "" && *tokenFileFlag != "" {
		t, err := readTokenFile(*tokenFileFlag)
		if err != nil {
			log.Println("warning: " + tf("warn_read_token_file", "source", "--token-file", "path", *tokenFileFlag, "error", err))
		}
		token = t
	}
	if token == "" {
		token = os.Getenv("PASTEBIN_TOKEN")
	}
	if token == "" {
		token = fileCfg.Auth.Token
	}
	if token == "" && fileCfg.Auth.TokenFile != "" {
		t, err := readTokenFile(fileCfg.Auth.TokenFile)
		if err != nil {
			log.Println("warning: " + tf("warn_read_token_file", "source", "auth.token_file", "path", fileCfg.Auth.TokenFile, "error", err))
		}
		token = t
	}
	if *tokenFlag != "" {
		if resolvedTok, persist := saveIfUnset(fileCfg.Auth.Token, *tokenFlag, func(s string) bool { return s != "" }); persist && resolvedTok != "" {
			fileCfg.Auth.Token = resolvedTok
			if err := saveCLIConfig(fileCfg); err != nil {
				log.Println("warning: " + tf("warn_save_config", "error", err))
			}
		}
	}
	if *tokenFileFlag != "" {
		if resolvedPath, persist := saveIfUnset(fileCfg.Auth.TokenFile, *tokenFileFlag, func(s string) bool { return s != "" }); persist && resolvedPath != "" {
			fileCfg.Auth.TokenFile = resolvedPath
			if err := saveCLIConfig(fileCfg); err != nil {
				log.Println("warning: " + tf("warn_save_config", "error", err))
			}
		}
	}

	// PART 32 env var mapping ({PROJECT_NAME}_SERVER_TIMEOUT, _RETRY,
	// _RETRY_DELAY, _API_VERSION) overrides cli.yml before defaults are
	// resolved, matching the documented CLI flag > env var > cli.yml >
	// compiled default precedence.
	fileCfg.Server.Timeout = envOrDefault("PASTEBIN_SERVER_TIMEOUT", fileCfg.Server.Timeout)
	fileCfg.Server.RetryDelay = envOrDefault("PASTEBIN_SERVER_RETRY_DELAY", fileCfg.Server.RetryDelay)
	fileCfg.Server.APIVersion = envOrDefault("PASTEBIN_SERVER_API_VERSION", fileCfg.Server.APIVersion)
	if v := os.Getenv("PASTEBIN_SERVER_RETRY"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			fileCfg.Server.Retry = n
		}
	}

	// Resolve server.timeout/retry/retry_delay/api_version from cli.yml
	// (PART 32) once, shared by every client instance constructed below.
	timeout, retry, retryDelay := resolveServerTiming(fileCfg)
	resolvedAPIVersion := fileCfg.Server.APIVersion
	if resolvedAPIVersion == "" {
		resolvedAPIVersion = apiVersion
	}

	// Resolve the PART 32 "Output Formats" value once. --json is a
	// back-compat boolean alias for --output json.
	outputFormat := resolveOutputFormat(*asJSON, *outputFlag)

	if *doUpdate != "" {
		quietUpdate := quietDefault || silentAutoUpdate(fileCfg, *doUpdate, term.IsTerminal(int(os.Stdin.Fd())))
		c := &client{server: strings.TrimRight(*server, "/"), asJSON: *asJSON, outputFormat: outputFormat, lang: locale, token: token, apiVersion: resolvedAPIVersion, timeout: timeout, retry: retry, retryDelay: retryDelay, quiet: quietUpdate, verbose: *verboseFlag}
		c.cmdUpdate(*doUpdate)
		return
	}

	// Auto-detect display mode per PART 32, honoring the cli.yml
	// display.mode override ("auto", "tui", or "gui" — never a CLI flag).
	mode := detectMode(args, fileCfg.Display.Mode)
	// cli.yml tui.enabled: false forces CLI-only mode even when the
	// terminal would otherwise auto-detect TUI (PART 32, AI.md line 44633).
	if mode == "tui" && !fileCfg.TUI.Enabled {
		mode = "cli"
	}
	if mode == "gui" {
		// PART 32: `gui` is "force native GUI (error if no display)", and
		// auto-detect only picks GUI when one is actually present. So a
		// failure here means the user explicitly asked for GUI and this
		// build/OS has none — report that rather than silently degrading to
		// the TUI they did not ask for.
		if !guiAvailable() {
			fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), t("gui_unavailable"))
			os.Exit(exitGeneral)
		}
		runGUI(*server, locale, fileCfg)
		return
	}
	if mode == "tui" {
		runTUI(*server, locale, fileCfg)
		return
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(exitUsage)
	}

	if *server == "" {
		printNoServerError()
		os.Exit(exitConnection)
	}

	// Check for CLI updates (non-blocking; only blocks on min_version violation).
	if err := checkCLIUpdate(*server, locale); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_update_check", "error", err))
		os.Exit(exitConnection)
	}

	c := &client{server: strings.TrimRight(*server, "/"), asJSON: *asJSON, outputFormat: outputFormat, lang: locale, token: token, apiVersion: resolvedAPIVersion, timeout: timeout, retry: retry, retryDelay: retryDelay, quiet: quietDefault, verbose: *verboseFlag, pager: envOrDefault("PASTEBIN_OUTPUT_PAGER", fileCfg.Output.Pager)}
	cacheTTL := parseDurationOr(fileCfg.Cache.TTL, 5*time.Minute)
	cacheMaxSize, _ := parseByteSize(fileCfg.Cache.MaxSize)
	c.cache = responseCacheConfig{enabled: fileCfg.Cache.Enabled, ttl: cacheTTL, maxSize: cacheMaxSize}
	c.defaults.Public = envOrDefault("PASTEBIN_PUBLIC", fileCfg.Defaults.Public)
	c.defaults.Expire = envOrDefault("PASTEBIN_EXPIRE", fileCfg.Defaults.Expire)
	c.defaults.Syntax = envOrDefault("PASTEBIN_SYNTAX", fileCfg.Defaults.Syntax)
	if limit := envOrDefault("PASTEBIN_LIMIT", ""); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil {
			c.defaults.Limit = n
		}
	} else {
		c.defaults.Limit = fileCfg.Defaults.Limit
	}

	switch args[0] {
	case "create":
		c.cmdCreate(args[1:])
	case "get":
		c.cmdGet(args[1:])
	case "delete", "del", "rm":
		c.cmdDelete(args[1:])
	case "list", "ls":
		c.cmdList(args[1:])
	case "update":
		// Positional form: pastebin-cli update [check|yes]
		// Equivalent to --update flag; defaults to "check" when no sub-arg given.
		action := "check"
		if len(args) >= 2 {
			action = args[1]
		}
		c.cmdUpdate(action)
	case "version":
		printVersionInfo(*server)
	default:
		c.cmdCreate(args)
	}
}

// saveCLIConfigURL updates the server URL field in cli.yml.
func saveCLIConfigURL(serverURL string) error {
	cfg, _ := loadCLIConfig()
	cfg.Server.Primary = serverURL
	return saveCLIConfig(cfg)
}

// runTUI launches the interactive bubbletea TUI mode.
// When no server is configured, the TUI setup wizard collects it.
func runTUI(server, lang string, cfg cliConfig) {
	// Auto-update check in TUI mode (non-fatal for version notices).
	if server != "" {
		if err := checkCLIUpdate(server, lang); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_update_check", "error", err))
			os.Exit(exitConnection)
		}
	}

	tuiCfg := tui.ClientConfig{
		Server:  server,
		Lang:    lang,
		SaveURL: saveCLIConfigURL,
		CfgPath: paths.Resolved(),
		Theme:   cfg.TUI.Theme,
		Mouse:   cfg.TUI.Mouse,
		Unicode: cfg.TUI.Unicode,
	}
	if err := tui.Run(tuiCfg); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_tui", "error", err))
		os.Exit(exitGeneral)
	}
}

// ─── Client ───────────────────────────────────────────────────────────────────

type client struct {
	server string
	asJSON bool
	// outputFormat is the resolved --output value (json, table, or plain;
	// PART 32 "Output Formats"). --json is a back-compat alias for
	// --output json and is folded into this field, not read separately
	// past flag parsing.
	outputFormat string
	// lang is the resolved output/UI locale sent as the Accept-Language header (PART 30/32).
	lang string
	// token is the resolved operator/owner API token sent as the Authorization bearer header (PART 32).
	token string
	// apiVersion is the resolved cli.yml server.api_version (PART 32), falling
	// back to the compiled apiVersion const when unset or invalid.
	apiVersion string
	// timeout is the resolved cli.yml server.timeout (PART 32), applied to every request.
	timeout time.Duration
	// retry is the resolved cli.yml server.retry (PART 32): retryable-error attempts after the first.
	retry int
	// retryDelay is the resolved cli.yml server.retry_delay (PART 32): fixed delay between retries.
	retryDelay time.Duration
	quiet      bool
	verbose    bool
	pager      string
	cache      responseCacheConfig
	defaults   struct {
		Public string
		Expire string
		Syntax string
		Limit  int
	}
}

// defaultClientTimeout/defaultClientRetry/defaultClientRetryDelay mirror the
// cli.yml server.{timeout,retry,retry_delay} compiled defaults (PART 32) and
// are used whenever cli.yml is missing or a value fails to parse.
const (
	defaultClientTimeout    = 30 * time.Second
	defaultClientRetry      = 3
	defaultClientRetryDelay = 1 * time.Second
)

// resolveServerTiming parses cli.yml's server.timeout/retry/retry_delay,
// falling back to the compiled defaults on empty or invalid values (PART 32:
// invalid config values warn and substitute the default, never crash).
func resolveServerTiming(cfg cliConfig) (timeout time.Duration, retry int, retryDelay time.Duration) {
	timeout = defaultClientTimeout
	if cfg.Server.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Server.Timeout); err == nil && d > 0 {
			timeout = d
		} else {
			log.Println("warning: " + tf("warn_invalid_timeout", "value", strconv.Quote(cfg.Server.Timeout), "default", defaultClientTimeout))
		}
	}
	retry = defaultClientRetry
	if cfg.Server.Retry > 0 {
		retry = cfg.Server.Retry
	}
	retryDelay = defaultClientRetryDelay
	if cfg.Server.RetryDelay != "" {
		if d, err := time.ParseDuration(cfg.Server.RetryDelay); err == nil && d >= 0 {
			retryDelay = d
		} else {
			log.Println("warning: " + tf("warn_invalid_retry_delay", "value", strconv.Quote(cfg.Server.RetryDelay), "default", defaultClientRetryDelay))
		}
	}
	return timeout, retry, retryDelay
}

// doWithRetry executes req via httpClient, retrying up to c.retry additional
// times (fixed delay c.retryDelay between attempts) on network errors or a
// 503 response — never on 4xx (PART 9/11 retry policy: retryable errors only).
func (c *client) doWithRetry(httpClient *http.Client, req *http.Request) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.retry; attempt++ {
		if attempt > 0 {
			if c.verbose {
				log.Printf("retrying request after attempt %d", attempt)
			}
			time.Sleep(c.retryDelay)
			// Requests with a body (POST) must have it rewound before each
			// retry — http.NewRequest populates GetBody for bytes.Reader
			// bodies, but Do() only consumes it on redirects, not on a
			// second manual call with the same *http.Request.
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, err
				}
				req.Body = body
			}
		}
		resp, err := httpClient.Do(req)
		if err == nil && resp.StatusCode != http.StatusServiceUnavailable {
			return resp, nil
		}
		if err == nil {
			resp.Body.Close()
			lastErr = errors.New(tf("err_server_status", "status", resp.StatusCode))
		} else {
			lastErr = err
		}
	}
	return nil, lastErr
}

// setAuth adds the Authorization bearer header when an API token is configured (PART 32).
func (c *client) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// detectLocale resolves the output locale from the --lang flag, falling back to
// cli.yml's defaults.lang, then to the LANG / LC_ALL environment variables, then to
// "en" (PART 30 priority order). A value of "auto" or "" triggers the next priority.
// The server silently falls back to English for unsupported codes.
func detectLocale(flagVal, configVal string) string {
	v := strings.TrimSpace(flagVal)
	if v != "" && v != "auto" {
		return v
	}
	if cv := strings.TrimSpace(configVal); cv != "" && cv != "auto" {
		return cv
	}
	for _, env := range []string{"LC_ALL", "LANG", "LANGUAGE"} {
		if val := os.Getenv(env); val != "" {
			// Strip encoding/territory suffixes: "en_US.UTF-8" -> "en".
			code := val
			if i := strings.IndexAny(code, "._@"); i >= 0 {
				code = code[:i]
			}
			if i := strings.IndexByte(code, '_'); i >= 0 {
				code = code[:i]
			}
			if code != "" && code != "C" && code != "POSIX" {
				return code
			}
		}
	}
	return "en"
}

func (c *client) cmdCreate(args []string) {
	// ContinueOnError so a bad flag falls through to the exitUsage=64 handling
	// below instead of ExitOnError's hardcoded os.Exit(2).
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	syntaxDefault := c.defaults.Syntax
	if syntaxDefault == "" {
		syntaxDefault = "auto"
	}
	lang := fs.String("lang", syntaxDefault, "syntax language")
	fs.StringVar(lang, "syntax", syntaxDefault, "alias for --lang")
	expiryDefault := c.defaults.Expire
	if expiryDefault == "" {
		expiryDefault = "never"
	}
	expiry := fs.String("expire", expiryDefault, "expiry: 1h 1d 1w 1m 3m 6m 1y 18m 2y never or seconds")
	fs.StringVar(expiry, "expiry", expiryDefault, "alias for --expire")
	burn := fs.Int("burn", 0, "delete after N views (0 = disabled)")
	publicDefault := c.defaults.Public
	if publicDefault == "" {
		publicDefault = "unlisted"
	}
	visibility := fs.String("public", publicDefault, "visibility: public or unlisted")
	unlisted := fs.Bool("unlisted", false, "alias for --public unlisted")
	title := fs.String("title", "", "paste title")
	outputFlag := fs.String("output", c.outputFormat, "output format: json, yaml, table, plain, csv")
	jsonFlag := fs.Bool("json", false, "alias for --output json")
	asLink := fs.Bool("link", false, "create as a link — content/arg must be an absolute http:// or https:// URL; server issues a 302 redirect instead of rendering")
	if err := fs.Parse(reorderCommandArgs(args, "--lang", "--syntax", "--expire", "--expiry", "--burn", "--public", "--title", "--output")); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_flags", "command", "create", "error", err))
		os.Exit(exitUsage)
	}
	c.outputFormat = resolveOutputFormat(*jsonFlag, *outputFlag)

	var content []byte
	var err error
	effLang := *lang
	effTitle := *title

	if fs.NArg() > 0 {
		if *asLink {
			// Link mode: the positional arg is the target URL itself, not a
			// file path — never read it off disk.
			content = []byte(fs.Arg(0))
		} else {
			// Smart argument detection (AI.md PART 32 "Smart Argument
			// Detection"): stat the arg first. A directory gets a dedicated
			// error, an existing file is read from disk, and anything else
			// is treated as literal paste text rather than a raw I/O error.
			arg := fs.Arg(0)
			info, statErr := os.Stat(arg)
			switch {
			case statErr == nil && info.IsDir():
				fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("create_is_dir", "path", strconv.Quote(arg)))
				os.Exit(exitUsage)
			case statErr == nil:
				content, err = os.ReadFile(arg)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_read_file", "error", err))
					os.Exit(exitGeneral)
				}
				if effLang == "text" || effLang == "auto" {
					effLang = detectLang(arg)
				}
				if effTitle == "" {
					effTitle = arg
				}
			default:
				content = []byte(strings.Join(fs.Args(), " "))
			}
		}
	} else {
		content, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_read_stdin", "error", err))
			os.Exit(exitGeneral)
		}
	}

	vis, visibilityErr := parseVisibility(*visibility)
	if visibilityErr != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid visibility %q; use public or unlisted\n", binName(), *visibility)
		os.Exit(exitUsage)
	}
	if *unlisted {
		vis = 1
	}
	expiryValue, expiryErr := normalizeExpiry(*expiry)
	if expiryErr != nil {
		fmt.Fprintf(os.Stderr, "%s: invalid expiry %q; use a supported duration, seconds, or never\n", binName(), *expiry)
		os.Exit(exitUsage)
	}

	body := map[string]interface{}{
		"content":    string(content),
		"title":      effTitle,
		"language":   effLang,
		"expires_in": expiryValue,
		"burn_after": *burn,
		"visibility": vis,
	}

	if *asLink {
		// Links carry no language/syntax mode and are never base64-encoded —
		// content is always the plain target URL string. There is no is_link
		// field to set: the server auto-detects link mode because content is
		// exactly one http(s):// URL and nothing else.
		delete(body, "language")
	} else {
		// Binary files (images, archives, etc.) cannot travel as raw bytes inside
		// a JSON string — invalid UTF-8 gets replaced and corrupts the data.
		// Detect the MIME type, base64-encode, and tell the server via
		// content_type.
		sample := content
		if len(sample) > 512 {
			sample = sample[:512]
		}
		if detected := http.DetectContentType(sample); !strings.HasPrefix(detected, "text/") {
			body["content"] = base64.StdEncoding.EncodeToString(content)
			body["content_type"] = detected
		}
	}

	resp, err := c.postJSON("/api/"+c.apiVer()+"/pastes", body)
	if err != nil {
		printConnectionError(c.server, err)
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
		os.Exit(exitGeneral)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		printAuthError()
	}
	if resp.StatusCode != http.StatusCreated {
		errMsg, _ := result["error"].(string)
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_server_error", "command", "create", "status", resp.StatusCode, "message", errMsg))
		os.Exit(exitGeneral)
	}

	if c.outputFormat == "json" {
		var output bytes.Buffer
		enc := json.NewEncoder(&output)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		_ = writePagedOutput(output.Bytes(), c.pager)
		return
	}
	if c.outputFormat == "yaml" {
		output, err := yaml.Marshal(result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: failed to encode YAML: %v\n", binName(), err)
			os.Exit(exitGeneral)
		}
		_ = writePagedOutput(output, c.pager)
		return
	}
	if c.outputFormat == "csv" {
		var output bytes.Buffer
		writer := csv.NewWriter(&output)
		_ = writer.Write([]string{"id", "link", "owner_token"})
		data := responseData(result)
		token := stringValue(data["owner_token"])
		if token == "" {
			token = stringValue(data["delete_token"])
		}
		_ = writer.Write([]string{stringValue(data["id"]), stringValue(data["link"]), token})
		writer.Flush()
		_ = writePagedOutput(output.Bytes(), c.pager)
		return
	}

	data := responseData(result)
	link := stringValue(data["link"])
	token := stringValue(data["owner_token"])
	if token == "" {
		token = stringValue(data["delete_token"])
	}
	fmt.Printf("%-14s%s\n", t("create_url_label"), link)
	if token != "" {
		fmt.Printf("%-14s%s\n", t("create_token_label"), token)
		fmt.Println(t("create_token_once"))
	}
}

func (c *client) cmdGet(args []string) {
	// ContinueOnError so a bad flag falls through to the exitUsage=64 handling
	// below instead of ExitOnError's hardcoded os.Exit(2).
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	outputFlag := fs.String("output", c.outputFormat, "output format: json, yaml, table, plain, csv")
	jsonFlag := fs.Bool("json", false, "alias for --output json")
	if err := fs.Parse(reorderCommandArgs(args, "--output")); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_flags", "command", "get", "error", err))
		os.Exit(exitUsage)
	}
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), t("get_usage"))
		os.Exit(exitUsage)
	}
	c.outputFormat = resolveOutputFormat(*jsonFlag, *outputFlag)
	id := fs.Arg(0)

	// PART 32 "Output Formats": --output json returns the full paste
	// metadata via the JSON API; plain/table (table has no meaning for a
	// single resource, so it degrades to plain) return raw content only,
	// as documented in the "--output plain" example.
	path := "/raw/" + url.PathEscape(id)
	if c.outputFormat == "json" || c.outputFormat == "yaml" {
		path = "/api/" + c.apiVer() + "/pastes/" + url.PathEscape(id)
	}

	resp, err := c.get(path)
	if err != nil {
		printConnectionError(c.server, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		printNotFoundError(id)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		printAuthError()
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_server_returned", "command", "get", "status", resp.StatusCode))
		os.Exit(exitGeneral)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_read_response", "command", "get", "error", err))
		os.Exit(exitGeneral)
	}
	switch c.outputFormat {
	case "yaml":
		var value interface{}
		if err := json.Unmarshal(content, &value); err != nil {
			fmt.Fprintf(os.Stderr, "%s: invalid JSON response: %v\n", binName(), err)
			os.Exit(exitGeneral)
		}
		content, err = yaml.Marshal(value)
	case "csv":
		var output bytes.Buffer
		writer := csv.NewWriter(&output)
		_ = writer.Write([]string{"content"})
		_ = writer.Write([]string{string(content)})
		writer.Flush()
		content = output.Bytes()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: failed to format response: %v\n", binName(), err)
		os.Exit(exitGeneral)
	}
	if err := writePagedOutput(content, c.pager); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_read_response", "command", "get", "error", err))
		os.Exit(exitGeneral)
	}
}

func (c *client) cmdDelete(args []string) {
	// ContinueOnError so a bad flag falls through to the exitUsage=64 handling
	// below instead of ExitOnError's hardcoded os.Exit(2).
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	outputFlag := fs.String("output", c.outputFormat, "output format: json, yaml, table, plain, csv")
	jsonFlag := fs.Bool("json", false, "alias for --output json")
	if err := fs.Parse(reorderCommandArgs(args, "--output")); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_flags", "command", "delete", "error", err))
		os.Exit(exitUsage)
	}
	if fs.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), t("delete_usage"))
		os.Exit(exitUsage)
	}
	c.outputFormat = resolveOutputFormat(*jsonFlag, *outputFlag)
	id, token := fs.Arg(0), fs.Arg(1)

	req, err := http.NewRequest(
		http.MethodDelete,
		c.url("/api/"+c.apiVer()+"/pastes/"+url.PathEscape(id)+"?token="+url.QueryEscape(token)),
		nil,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_build_request", "command", "delete", "error", err))
		os.Exit(exitGeneral)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s-cli/%s", projectName, Version))
	if c.lang != "" {
		req.Header.Set("Accept-Language", c.lang)
	}
	c.setAuth(req)

	httpClient := &http.Client{Timeout: c.clientTimeout()}
	resp, err := c.doWithRetry(httpClient, req)
	if err != nil {
		printConnectionError(c.server, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		printNotFoundError(id)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		printAuthError()
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_server_returned", "command", "delete", "status", resp.StatusCode))
		os.Exit(exitGeneral)
	}
	c.cache.invalidate()

	if c.outputFormat == "json" {
		io.Copy(os.Stdout, resp.Body)
		return
	}
	if c.outputFormat == "yaml" {
		var result interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
			os.Exit(exitGeneral)
		}
		output, err := yaml.Marshal(result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: failed to encode YAML: %v\n", binName(), err)
			os.Exit(exitGeneral)
		}
		_ = writePagedOutput(output, c.pager)
		return
	}
	if c.outputFormat == "csv" {
		var output bytes.Buffer
		writer := csv.NewWriter(&output)
		_ = writer.Write([]string{"id", "status"})
		_ = writer.Write([]string{id, "deleted"})
		writer.Flush()
		_ = writePagedOutput(output.Bytes(), c.pager)
		return
	}
	if !c.quiet {
		fmt.Println(tf("delete_ok", "id", id))
	}
}

func (c *client) cmdList(args []string) {
	// ContinueOnError so a bad flag falls through to the exitUsage=64 handling
	// below instead of ExitOnError's hardcoded os.Exit(2).
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	outputFlag := fs.String("output", c.outputFormat, "output format: json, yaml, table, plain, csv")
	jsonFlag := fs.Bool("json", false, "alias for --output json")
	defaultLimit := c.defaults.Limit
	if defaultLimit < 1 || defaultLimit > 250 {
		defaultLimit = 20
	}
	limit := fs.Int("limit", defaultLimit, "number of pastes to list (max 250)")
	page := fs.Int("page", 1, "page number")
	if err := fs.Parse(reorderCommandArgs(args, "--output", "--limit", "--page")); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_flags", "command", "list", "error", err))
		os.Exit(exitUsage)
	}
	c.outputFormat = resolveOutputFormat(*jsonFlag, *outputFlag)

	resp, err := c.get(fmt.Sprintf("/api/%s/pastes?page=%d&limit=%d", c.apiVer(), *page, *limit))
	if err != nil {
		printConnectionError(c.server, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		printAuthError()
	}

	var result struct {
		Pastes []struct {
			ID        string    `json:"id"`
			Title     string    `json:"title"`
			Language  string    `json:"language"`
			Views     int       `json:"views"`
			CreatedAt time.Time `json:"created_at"`
		} `json:"data"`
		Pagination struct {
			Total int `json:"total"`
			Pages int `json:"pages"`
		} `json:"pagination"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
		os.Exit(exitGeneral)
	}

	if c.outputFormat == "json" {
		var output bytes.Buffer
		enc := json.NewEncoder(&output)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		_ = writePagedOutput(output.Bytes(), c.pager)
		return
	}
	if c.outputFormat == "yaml" {
		output, err := yaml.Marshal(result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: failed to encode YAML: %v\n", binName(), err)
			os.Exit(exitGeneral)
		}
		_ = writePagedOutput(output, c.pager)
		return
	}
	if c.outputFormat == "csv" {
		var output bytes.Buffer
		writer := csv.NewWriter(&output)
		_ = writer.Write([]string{"id", "title", "language", "views", "created_at"})
		for _, paste := range result.Pastes {
			_ = writer.Write([]string{paste.ID, paste.Title, paste.Language, strconv.Itoa(paste.Views), paste.CreatedAt.Format(time.RFC3339)})
		}
		writer.Flush()
		_ = writePagedOutput(output.Bytes(), c.pager)
		return
	}

	if len(result.Pastes) == 0 {
		if !c.quiet {
			fmt.Println(t("list_empty"))
		}
		return
	}

	if c.outputFormat == "table" {
		headers := []string{t("list_col_id"), t("list_col_title"), t("list_col_lang"), t("list_col_created")}
		var output bytes.Buffer
		rows := make([][]string, 0, len(result.Pastes))
		for _, p := range result.Pastes {
			title := p.Title
			if len(title) > 45 {
				title = title[:42] + "..."
			}
			rows = append(rows, []string{p.ID, title, p.Language, p.CreatedAt.Format("2006-01-02")})
		}
		renderBorderedTable(&output, headers, rows)
		fmt.Fprintf(&output, "\n%s\n", tf("list_footer",
			"total", result.Pagination.Total, "page", *page, "pages", result.Pagination.Pages))
		_ = writePagedOutput(output.Bytes(), c.pager)
		return
	}

	var output bytes.Buffer
	tw := tabwriter.NewWriter(&output, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
		t("list_col_id"), t("list_col_title"), t("list_col_lang"), t("list_col_views"), t("list_col_created"))
	for _, p := range result.Pastes {
		title := p.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			p.ID, title, p.Language, p.Views,
			p.CreatedAt.Format("2006-01-02 15:04"),
		)
	}
	tw.Flush()
	fmt.Fprintf(&output, "\n%s\n", tf("list_footer",
		"total", result.Pagination.Total, "page", *page, "pages", result.Pagination.Pages))
	_ = writePagedOutput(output.Bytes(), c.pager)
}

// renderBorderedTable prints a PART 32 "Output Formats" style box-drawing
// table (┌─┬─┐ borders) to w. Column widths are sized to the longest cell
// (header or data) per column, mirroring the AI.md --output table example.
func renderBorderedTable(w io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len([]rune(h))
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				if n := len([]rune(cell)); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}

	border := func(left, mid, right string) {
		fmt.Fprint(w, left)
		for i, wd := range widths {
			fmt.Fprint(w, strings.Repeat("─", wd+2))
			if i < len(widths)-1 {
				fmt.Fprint(w, mid)
			}
		}
		fmt.Fprintln(w, right)
	}

	printRow := func(cells []string) {
		fmt.Fprint(w, "│")
		for i, wd := range widths {
			cell := ""
			if i < len(cells) {
				cell = cells[i]
			}
			pad := wd - len([]rune(cell))
			if pad < 0 {
				pad = 0
			}
			fmt.Fprintf(w, " %s%s │", cell, strings.Repeat(" ", pad))
		}
		fmt.Fprintln(w)
	}

	border("┌", "┬", "┐")
	printRow(headers)
	border("├", "┼", "┤")
	for _, row := range rows {
		printRow(row)
	}
	border("└", "┴", "┘")
}

// defaultServerURL falls back to the embedded OfficialSite (site.txt at build time)
// when --server, $PASTEBIN_SERVER_PRIMARY, and cli.yml are all unset. Never persisted to cli.yml.
func defaultServerURL(resolved, official string) string {
	if resolved == "" {
		return official
	}
	return resolved
}

// printNoServerError prints the exact multi-line "no server configured"
// message documented in AI.md PART 32 "Server Address Resolution" (line
// 45115-45126), for projects without a compiled {official_site} default.
func printNoServerError() {
	bin := binName()
	fmt.Fprintf(os.Stderr, "%s\n\n", t("err_no_server"))
	fmt.Fprintf(os.Stderr, "%s\n", t("err_no_server_howto"))
	fmt.Fprintf(os.Stderr, "  %s --server https://your-server.example.com list\n\n", bin)
	fmt.Fprintf(os.Stderr, "%s\n", t("err_no_server_saved"))
	fmt.Fprintf(os.Stderr, "%s\n", tf("err_no_server_edit", "path", paths.Resolved()))
}

// cmdUpdate handles 'pastebin-cli --update check|yes'.
func (c *client) cmdUpdate(action string) {
	if c.server == "" {
		printNoServerError()
		os.Exit(exitConnection)
	}

	httpClient := &http.Client{Timeout: c.clientTimeout()}
	req, err := http.NewRequest(http.MethodGet, c.url("/api/autodiscover"), nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_build_request", "command", "update", "error", err))
		os.Exit(exitGeneral)
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s-cli/%s", projectName, Version))
	if c.lang != "" {
		req.Header.Set("Accept-Language", c.lang)
	}

	resp, err := c.doWithRetry(httpClient, req)
	if err != nil {
		printConnectionError(c.server, err)
	}
	defer resp.Body.Close()

	var disc autodiscoverResponse
	if err := json.NewDecoder(resp.Body).Decode(&disc); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_decode", "error", err))
		os.Exit(exitGeneral)
	}

	osArch := runtime.GOOS + "-" + runtime.GOARCH
	info, ok := disc.CLIVersions[osArch]
	if !ok {
		fmt.Println(tf("update_no_binary", "osarch", osArch))
		return
	}

	if !versionLessThan(Version, info.Version) {
		if !c.quiet {
			fmt.Println(tf("update_up_to_date", "binary", binName(), "version", Version))
		}
		return
	}

	if !c.quiet {
		fmt.Println(tf("update_available", "current", Version, "latest", info.Version))
	}
	if action != "yes" {
		if !c.quiet {
			fmt.Println(tf("update_run_to_install", "binary", binName()))
		}
		return
	}

	if err := c.downloadAndApplyUpdate(
		fmt.Sprintf("%s/cli/binaries/%s-cli-%s-%s", c.server, projectName, runtime.GOOS, runtime.GOARCH),
		info.SHA256,
	); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %s\n", binName(), tf("op_update_failed", "error", err))
		os.Exit(exitGeneral)
	}
}

// downloadAndApplyUpdate downloads the CLI binary, verifies SHA-256, and replaces the current binary.
func (c *client) downloadAndApplyUpdate(downloadURL, expectedSHA string) error {
	// Determine current binary path.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("%s: %w", t("err_find_executable"), err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("%s: %w", t("err_resolve_symlinks"), err)
	}
	return c.downloadAndApplyUpdateAt(downloadURL, expectedSHA, exe, reExec)
}

func (c *client) downloadAndApplyUpdateAt(downloadURL, expectedSHA, exe string, reexec func(string) error) error {
	// Download to ${TMPDIR:-/tmp}/apimgr/pastebin-XXXXXX/cli.update.tmp, verify
	// there, then atomically replace the installed binary (PART 32 step 3–5).
	tmpDir, tmpFile, err := updateTempDir()
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	f, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("%s: %w", t("err_create_temp_file"), err)
	}

	httpClient := &http.Client{Timeout: 5 * time.Minute}
	resp, err := httpClient.Get(downloadURL)
	if err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", t("err_download"), err)
	}
	defer resp.Body.Close()

	// Write and hash simultaneously so we only stream the body once.
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("%s: %w", t("err_write"), err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("%s: %w", t("err_close"), err)
	}

	// Verify SHA-256.
	got := fmt.Sprintf("%x", h.Sum(nil))
	if !strings.EqualFold(got, expectedSHA) {
		return errors.New(tf("err_checksum_mismatch", "got", got, "want", expectedSHA))
	}

	// Atomically replace the current binary. os.Rename is atomic only within a
	// filesystem; the temp dir is usually a separate tmpfs, so on a cross-device
	// error stage the verified binary beside the target and rename from there.
	if err := os.Rename(tmpFile, exe); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("%s: %w", t("err_replace_binary"), err)
		}
		if err := replaceCrossDevice(tmpFile, exe); err != nil {
			return err
		}
	}

	// Re-exec or inform the user on Windows.
	return reexec(exe)
}

// updateTempDir creates the PART 32 CLI update staging directory
// (${TMPDIR:-/tmp}/apimgr/pastebin-XXXXXX/) and returns both the directory and
// the cli.update.tmp file path inside it. The caller removes the directory.
func updateTempDir() (dir, file string, err error) {
	base := filepath.Join(os.TempDir(), "apimgr")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", "", fmt.Errorf("%s: %w", tf("err_create_temp_base", "base", base), err)
	}
	dir, err = os.MkdirTemp(base, projectName+"-*")
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", t("err_create_temp_dir"), err)
	}
	return dir, filepath.Join(dir, "cli.update.tmp"), nil
}

// replaceCrossDevice copies src to a staging file beside dst (same filesystem)
// and atomically renames it over dst. Used when src and dst live on different
// filesystems and a direct rename returns EXDEV.
func replaceCrossDevice(src, dst string) error {
	staging := dst + ".new"
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("%s: %w", t("err_open_staged_binary"), err)
	}
	defer in.Close()

	out, err := os.OpenFile(staging, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("%s: %w", t("err_create_staging_file"), err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(staging)
		return fmt.Errorf("%s: %w", t("err_copy_staged_binary"), err)
	}
	if err := out.Close(); err != nil {
		os.Remove(staging)
		return fmt.Errorf("%s: %w", t("err_close_staging_file"), err)
	}
	if err := os.Rename(staging, dst); err != nil {
		os.Remove(staging)
		return fmt.Errorf("%s: %w", t("err_replace_binary"), err)
	}
	return nil
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func (c *client) url(path string) string {
	return c.server + path
}

// apiVer returns c.apiVersion, falling back to the compiled apiVersion const
// when the client was constructed without an explicit cli.yml server.api_version.
func (c *client) apiVer() string {
	if c.apiVersion != "" {
		return c.apiVersion
	}
	return apiVersion
}

// clientTimeout returns c.timeout, falling back to the compiled default when
// the client was constructed without an explicit cli.yml server.timeout.
func (c *client) clientTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}
	return defaultClientTimeout
}

func (c *client) get(path string) (*http.Response, error) {
	requestURL := c.url(path)
	cacheKey := requestURL + "\n" + c.lang
	if c.token == "" {
		if cached, ok := c.cache.get(cacheKey); ok {
			return cached, nil
		}
	}
	httpClient := &http.Client{Timeout: c.clientTimeout()}
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("%s-cli/%s", projectName, Version))
	req.Header.Set("Accept", "application/json")
	if c.lang != "" {
		req.Header.Set("Accept-Language", c.lang)
	}
	c.setAuth(req)
	resp, err := c.doWithRetry(httpClient, req)
	if err != nil || c.token != "" || !c.cache.enabled {
		return resp, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	c.cache.put(cacheKey, resp, body)
	return resp, nil
}

func (c *client) postJSON(path string, body interface{}) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Timeout: c.clientTimeout()}
	req, err := http.NewRequest(http.MethodPost, c.url(path), bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("%s-cli/%s", projectName, Version))
	if c.lang != "" {
		req.Header.Set("Accept-Language", c.lang)
	}
	c.setAuth(req)
	resp, err := c.doWithRetry(httpClient, req)
	if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.cache.invalidate()
	}
	return resp, err
}

// ─── Language detection ───────────────────────────────────────────────────────

func detectLang(filename string) string {
	ext := strings.ToLower(filename)
	if i := strings.LastIndex(ext, "."); i != -1 {
		ext = ext[i+1:]
	}
	m := map[string]string{
		"go": "go", "py": "python", "js": "javascript", "ts": "typescript",
		"rs": "rust", "java": "java", "c": "c", "cpp": "cpp", "cc": "cpp",
		"cs": "csharp", "php": "php", "rb": "ruby", "sh": "bash",
		"bash": "bash", "zsh": "bash", "ps1": "powershell",
		"html": "html", "htm": "html", "css": "css", "json": "json",
		"yaml": "yaml", "yml": "yaml", "toml": "toml", "xml": "xml",
		"sql": "sql", "md": "markdown", "txt": "text",
	}
	if lang, ok := m[ext]; ok {
		return lang
	}
	return "text"
}

// ─── Misc ─────────────────────────────────────────────────────────────────────

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// firstNonEmpty returns s if non-empty, otherwise def.
func firstNonEmpty(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func applyCLIConfigEnv(cfg cliConfig) cliConfig {
	cfg.Server.Primary = envOrDefault("PASTEBIN_SERVER_PRIMARY", cfg.Server.Primary)
	cfg.Server.APIVersion = envOrDefault("PASTEBIN_SERVER_API_VERSION", cfg.Server.APIVersion)
	cfg.Server.Timeout = envOrDefault("PASTEBIN_SERVER_TIMEOUT", cfg.Server.Timeout)
	cfg.Server.RetryDelay = envOrDefault("PASTEBIN_SERVER_RETRY_DELAY", cfg.Server.RetryDelay)
	if value := os.Getenv("PASTEBIN_SERVER_RETRY"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Server.Retry = parsed
		}
	}
	cfg.Update.Channel = envOrDefault("PASTEBIN_UPDATE_CHANNEL", cfg.Update.Channel)
	cfg.Update.CheckInterval = envOrDefault("PASTEBIN_UPDATE_CHECK_INTERVAL", cfg.Update.CheckInterval)
	applyBoolEnv("PASTEBIN_UPDATE_AUTO", &cfg.Update.Auto)
	cfg.Display.Mode = envOrDefault("PASTEBIN_DISPLAY_MODE", cfg.Display.Mode)
	cfg.Output.Format = envOrDefault("PASTEBIN_FORMAT", envOrDefault("PASTEBIN_OUTPUT_FORMAT", cfg.Output.Format))
	cfg.Output.Color = envOrDefault("PASTEBIN_OUTPUT_COLOR", cfg.Output.Color)
	cfg.Output.Pager = envOrDefault("PASTEBIN_OUTPUT_PAGER", cfg.Output.Pager)
	applyBoolEnv("PASTEBIN_OUTPUT_QUIET", &cfg.Output.Quiet)
	applyBoolEnv("PASTEBIN_OUTPUT_VERBOSE", &cfg.Output.Verbose)
	applyBoolEnv("PASTEBIN_TUI_ENABLED", &cfg.TUI.Enabled)
	cfg.TUI.Theme = envOrDefault("PASTEBIN_TUI_THEME", cfg.TUI.Theme)
	applyBoolEnv("PASTEBIN_TUI_MOUSE", &cfg.TUI.Mouse)
	applyBoolEnv("PASTEBIN_TUI_UNICODE", &cfg.TUI.Unicode)
	cfg.Logging.Level = envOrDefault("PASTEBIN_LOGGING_LEVEL", cfg.Logging.Level)
	cfg.Logging.File = envOrDefault("PASTEBIN_LOGGING_FILE", cfg.Logging.File)
	cfg.Logging.MaxSize = envOrDefault("PASTEBIN_LOGGING_MAX_SIZE", cfg.Logging.MaxSize)
	if value := os.Getenv("PASTEBIN_LOGGING_MAX_FILES"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Logging.MaxFiles = parsed
		}
	}
	applyBoolEnv("PASTEBIN_CACHE_ENABLED", &cfg.Cache.Enabled)
	cfg.Cache.TTL = envOrDefault("PASTEBIN_CACHE_TTL", cfg.Cache.TTL)
	cfg.Cache.MaxSize = envOrDefault("PASTEBIN_CACHE_MAX_SIZE", cfg.Cache.MaxSize)
	applyBoolEnv("PASTEBIN_DEBUG", &cfg.Debug)
	cfg.Defaults.Lang = envOrDefault("PASTEBIN_LANG", envOrDefault("PASTEBIN_DEFAULTS_LANG", cfg.Defaults.Lang))
	cfg.Defaults.Public = envOrDefault("PASTEBIN_PUBLIC", cfg.Defaults.Public)
	cfg.Defaults.Expire = envOrDefault("PASTEBIN_EXPIRE", cfg.Defaults.Expire)
	cfg.Defaults.Syntax = envOrDefault("PASTEBIN_SYNTAX", cfg.Defaults.Syntax)
	cfg.Defaults.Output = envOrDefault("PASTEBIN_DEFAULTS_OUTPUT", cfg.Defaults.Output)
	if value := os.Getenv("PASTEBIN_LIMIT"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Defaults.Limit = parsed
		}
	}
	return cfg
}

func applyBoolEnv(name string, target *bool) {
	if value := os.Getenv(name); value != "" {
		if parsed, err := config.ParseBool(value, *target); err == nil {
			*target = parsed
		}
	}
}

func silentAutoUpdate(cfg cliConfig, action string, interactive bool) bool {
	return cfg.Update.Auto && action == "yes" && !interactive
}

func responseData(result map[string]interface{}) map[string]interface{} {
	if data, ok := result["data"].(map[string]interface{}); ok {
		return data
	}
	return result
}

func stringValue(value interface{}) string {
	valueString, _ := value.(string)
	return valueString
}

func parseVisibility(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "public":
		return 0, nil
	case "unlisted":
		return 1, nil
	default:
		isPublic, err := config.ParseBool(value, false)
		if err != nil {
			return 0, err
		}
		if isPublic {
			return 0, nil
		}
		return 1, nil
	}
}

func reorderCommandArgs(args []string, valueFlags ...string) []string {
	withValue := make(map[string]bool, len(valueFlags))
	for _, name := range valueFlags {
		withValue[name] = true
	}
	flags := make([]string, 0, len(args))
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		name := arg
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			continue
		}
		if withValue[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

func printUsage() {
	binaryName := binName()
	// AI.md PART 32 "Server Address Resolution" (line 45128-45136): when the
	// binary was built with a compiled {official_site}, --help shows it as
	// the default; otherwise the flag stays marked required.
	serverFlagHelp := t("help_flag_server_required")
	if OfficialSite != "" {
		serverFlagHelp = tf("help_flag_server_default", "site", OfficialSite)
	}
	fmt.Fprintf(os.Stderr, "%s\n\n", tf("help_tagline", "binary", binaryName, "version", Version))
	fmt.Fprintf(os.Stderr, "%s\n", t("help_head_usage"))
	fmt.Fprintf(os.Stderr, "    %s\n\n", tf("help_usage_line", "binary", binaryName))

	fmt.Fprintf(os.Stderr, "%s\n", t("help_head_commands"))
	fmt.Fprintf(os.Stderr, "    create [file]        %s\n", t("help_cmd_create"))
	fmt.Fprintf(os.Stderr, "    get <id>             %s\n", t("help_cmd_get"))
	fmt.Fprintf(os.Stderr, "    delete <id> <token>  %s\n", t("help_cmd_delete"))
	fmt.Fprintf(os.Stderr, "    list [--limit N]     %s\n", t("help_cmd_list"))
	fmt.Fprintf(os.Stderr, "    update [check|yes]   %s\n\n", t("help_cmd_update"))
	fmt.Fprintf(os.Stderr, "    %s\n\n", t("help_cmd_tui_note"))

	fmt.Fprintf(os.Stderr, "%s\n", t("help_head_create_flags"))
	fmt.Fprintf(os.Stderr, "    --lang <lang>        %s\n", t("help_flag_lang_create"))
	fmt.Fprintf(os.Stderr, "    --expiry <duration>  %s\n", t("help_flag_expiry"))
	fmt.Fprintf(os.Stderr, "    --burn <n>           %s\n", t("help_flag_burn"))
	fmt.Fprintf(os.Stderr, "    --unlisted           %s\n", t("help_flag_unlisted"))
	fmt.Fprintf(os.Stderr, "    --title <title>      %s\n", t("help_flag_title"))
	fmt.Fprintf(os.Stderr, "    --link                %s\n", t("help_flag_link"))
	fmt.Fprintf(os.Stderr, "                          %s\n\n", t("help_flag_link_cont"))

	fmt.Fprintf(os.Stderr, "%s\n", t("help_head_list_flags"))
	fmt.Fprintf(os.Stderr, "    --limit <n>          %s\n", t("help_flag_limit"))
	fmt.Fprintf(os.Stderr, "    --page <n>           %s\n\n", t("help_flag_page"))

	fmt.Fprintf(os.Stderr, "%s\n", t("help_head_global_flags"))
	fmt.Fprintf(os.Stderr, "    --server <url>       %s\n", serverFlagHelp)
	fmt.Fprintf(os.Stderr, "    --token <token>      %s\n", t("help_flag_token"))
	fmt.Fprintf(os.Stderr, "    --token-file <file>  %s\n", t("help_flag_token_file"))
	fmt.Fprintf(os.Stderr, "    --json               %s\n", t("help_flag_json"))
	fmt.Fprintf(os.Stderr, "    --output <format>    %s\n", t("help_flag_output"))
	fmt.Fprintf(os.Stderr, "    --color <when>       %s\n", t("help_flag_color"))
	fmt.Fprintf(os.Stderr, "    --lang <code>        %s\n", t("help_flag_lang"))
	fmt.Fprintf(os.Stderr, "    --config <name>      %s\n", t("help_flag_config"))
	fmt.Fprintf(os.Stderr, "    --debug              %s\n", t("help_flag_debug"))
	fmt.Fprintf(os.Stderr, "    --verbose            %s\n", t("help_flag_verbose"))
	fmt.Fprintf(os.Stderr, "    --update check|yes   %s\n", t("help_flag_update"))
	fmt.Fprintf(os.Stderr, "    --version            %s\n", t("help_flag_version"))
	fmt.Fprintf(os.Stderr, "    --shell completions [SHELL]  %s\n", t("help_flag_shell_completions"))
	fmt.Fprintf(os.Stderr, "    --shell init [SHELL]         %s\n", t("help_flag_shell_init"))
	fmt.Fprintf(os.Stderr, "    --shell --help               %s\n\n", t("help_flag_shell_help"))

	fmt.Fprintf(os.Stderr, "%s\n", t("help_head_examples"))
	fmt.Fprintf(os.Stderr, "    PASTEBIN_SERVER_PRIMARY=https://paste.example.com %s create --lang text < file.txt\n", binaryName)
	fmt.Fprintf(os.Stderr, "    %s --server https://paste.example.com create --lang go myfile.go\n", binaryName)
	fmt.Fprintf(os.Stderr, "    %s --server https://paste.example.com get abc12345\n", binaryName)
	fmt.Fprintf(os.Stderr, "    %s --server https://paste.example.com delete abc12345 <delete-token>\n", binaryName)
	fmt.Fprintf(os.Stderr, "    %s --server https://paste.example.com list --limit 10\n\n", binaryName)
}
