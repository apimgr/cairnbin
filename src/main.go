package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	// Embed the IANA timezone database so time.LoadLocation works in CGO_ENABLED=0 static binaries.
	_ "time/tzdata"

	"golang.org/x/term"

	"github.com/apimgr/pastebin/src/audit"
	"github.com/apimgr/pastebin/src/common/banner"
	"github.com/apimgr/pastebin/src/common/email"
	"github.com/apimgr/pastebin/src/common/i18n"
	"github.com/apimgr/pastebin/src/config"
	"github.com/apimgr/pastebin/src/daemon"
	"github.com/apimgr/pastebin/src/database"
	"github.com/apimgr/pastebin/src/logging"
	"github.com/apimgr/pastebin/src/maintenance"
	"github.com/apimgr/pastebin/src/mode"
	"github.com/apimgr/pastebin/src/path"
	"github.com/apimgr/pastebin/src/pid"
	"github.com/apimgr/pastebin/src/scheduler"
	"github.com/apimgr/pastebin/src/server"
	"github.com/apimgr/pastebin/src/service"
	"github.com/apimgr/pastebin/src/shell"
	"github.com/apimgr/pastebin/src/task"
	"github.com/apimgr/pastebin/src/tor"
	"github.com/apimgr/pastebin/src/updater"
)

// Version, CommitID, BuildEpoch, and OfficialSite are injected at build time via
// -ldflags. BuildDate is NOT an ldflag itself — it is derived from BuildEpoch in
// init() below (AI.md PART 25 "Embedded Build Info").
var (
	Version  = "dev"
	CommitID = "unknown"
	// BuildDate is derived from BuildEpoch in init(); stays "unknown" when BuildEpoch is unset.
	BuildDate = "unknown"
	// BuildEpoch is the Unix build timestamp (seconds, UTC) set via -ldflags; "0" when unset.
	BuildEpoch   = "0"
	OfficialSite = ""
)

const appName = "pastebin"

// buildEpoch parses the embedded BuildEpoch ldflag; 0 when unset or invalid.
func buildEpoch() int64 {
	n, err := strconv.ParseInt(BuildEpoch, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// init derives BuildDate (RFC 3339 UTC) from the embedded BuildEpoch.
func init() {
	if n := buildEpoch(); n > 0 {
		BuildDate = time.Unix(n, 0).UTC().Format("2006-01-02T15:04:05Z")
	}
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. rawArgs is os.Args[1:]; stdout and stderr are
// the output writers (os.Stdout / os.Stderr in production, captured buffers in
// tests). It returns a POSIX exit code: 0 = success, 1 = runtime error, 2 = usage error.
func run(rawArgs []string, stdout, stderr io.Writer) int {
	binaryName := filepath.Base(os.Args[0])

	// Pre-process args: expand -h/-v short aliases to their long form.
	args := normalizeArgs(rawArgs)

	// A bare trailing "--update" (no subcommand following) defaults to "yes"
	// (check + install). stdlib flag requires an explicit value for a
	// non-boolean flag, so the default is injected here rather than in the
	// flag.FlagSet itself.
	if len(args) > 0 && args[len(args)-1] == "--update" {
		args = append(args, "yes")
	}

	var (
		portFlag    string
		addressFlag string
		modeFlag    string
		configFlag  string
		dataFlag    string
		logFlag     string
		cacheFlag   string
		backupFlag  string
		pidFlag     string
		baseurlFlag string
		colorFlag   string
		langFlag    string
		showVersion bool
		showStatus  bool
		showHelp    bool
		daemonFlag  bool
		debugFlag   bool
		i2pFlag     bool
	)

	// Subcommands that take optional secondary positional arguments.
	var (
		shellCmd       string
		shellArg       string
		serviceCmd     string
		maintenanceCmd string
		// second positional arg after --maintenance subcommand
		maintenanceArg string
		// remaining positionals for "--maintenance pgp <action> [args...]",
		// "--maintenance token <list|revoke> [prefix]", and
		// "--maintenance data <export|delete> <prefix>"
		maintenancePGP []string
		updateCmd      string
	)

	fs := flag.NewFlagSet(binaryName, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	fs.BoolVar(&showHelp, "help", false, "Show help")
	fs.BoolVar(&showHelp, "h", false, "Show help")
	fs.BoolVar(&showVersion, "version", false, "Show version")
	fs.BoolVar(&showVersion, "v", false, "Show version")
	fs.BoolVar(&showStatus, "status", false, "Show server status and health")
	fs.BoolVar(&daemonFlag, "daemon", false, "Run as daemon (detach from terminal)")
	fs.BoolVar(&debugFlag, "debug", false, "Enable debug mode")
	fs.BoolVar(&i2pFlag, "i2p", false, "Enable the optional I2P eepsite (opt-in; PART 31.2)")
	fs.StringVar(&portFlag, "port", "", "Listen port")
	fs.StringVar(&addressFlag, "address", "", "Listen address")
	fs.StringVar(&modeFlag, "mode", "", "Application mode (production|development)")
	fs.StringVar(&configFlag, "config", "", "Config directory")
	fs.StringVar(&dataFlag, "data", "", "Data directory")
	fs.StringVar(&logFlag, "log", "", "Log directory")
	fs.StringVar(&cacheFlag, "cache", "", "Cache directory")
	fs.StringVar(&backupFlag, "backup", "", "Backup directory")
	fs.StringVar(&pidFlag, "pid", "", "PID file path")
	fs.StringVar(&baseurlFlag, "baseurl", "", "URL path prefix")
	fs.StringVar(&colorFlag, "color", "auto", "Color output (auto|yes|no)")
	fs.StringVar(&langFlag, "lang", "", "Language for output")
	fs.StringVar(&shellCmd, "shell", "", "Shell integration (completions|init|--help)")
	fs.StringVar(&serviceCmd, "service", "", "Service management")
	fs.StringVar(&maintenanceCmd, "maintenance", "", "Maintenance operations")
	fs.StringVar(&updateCmd, "update", "", "Check/perform updates")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
		fmt.Fprintf(stderr, "Run '%s --help' for usage.\n", binaryName)
		return 2
	}

	// Positional arguments left over after the flag(s) that take a secondary
	// value (--shell, --maintenance, --update branch).
	positional := fs.Args()

	if shellCmd != "" && len(positional) > 0 {
		// optional SHELL name (bash, zsh, fish, …)
		shellArg = positional[0]
	}

	if maintenanceCmd != "" && len(positional) > 0 {
		// Second positional argument (e.g. filename for "restore" or mode
		// name for "mode", or the action for "pgp").
		maintenanceArg = positional[0]
		// "pgp"/"token"/"data" take further positionals (e.g. "export
		// private <path>", "revoke <prefix>", "delete <prefix>").
		switch maintenanceCmd {
		case "pgp", "token", "data":
			maintenancePGP = positional[1:]
		}
	}

	if strings.HasPrefix(updateCmd, "branch") && len(positional) > 0 {
		updateCmd = "branch " + positional[0]
	}

	// Apply --color flag before any output.
	applyColor(colorFlag)

	if showHelp {
		printHelp(stdout, binaryName)
		return 0
	}

	if showVersion {
		fmt.Fprintf(stdout, "%s %s\n", binaryName, Version)
		fmt.Fprintf(stdout, "Built: %s\n", BuildDate)
		fmt.Fprintf(stdout, "Go: %s\n", runtime.Version())
		fmt.Fprintf(stdout, "OS/Arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		return 0
	}

	// ── Shell integration ─────────────────────────────────────────────────────

	if shellCmd != "" {
		switch shellCmd {
		case "--help":
			shell.PrintHelp(binaryName)
		case "completions":
			if err := shell.PrintCompletions(binaryName, shellArg); err != nil {
				fmt.Fprintf(stderr, "%s: --shell completions: %v\n", binaryName, err)
				return 1
			}
		case "init":
			if err := shell.PrintInit(binaryName, shellArg); err != nil {
				fmt.Fprintf(stderr, "%s: --shell init: %v\n", binaryName, err)
				return 1
			}
		default:
			fmt.Fprintf(stderr, "%s: --shell: unknown subcommand %q\n", binaryName, shellCmd)
			fmt.Fprintf(stderr, "Run '%s --shell --help' for usage.\n", binaryName)
			return 2
		}
		return 0
	}

	// ── Service management ────────────────────────────────────────────────────

	if serviceCmd != "" {
		switch serviceCmd {
		case "--help":
			service.PrintHelp(binaryName)
		case "start":
			if err := service.Start(); err != nil {
				fmt.Fprintf(stderr, "%s: service start: %v\n", binaryName, err)
				return 1
			}
			fmt.Fprintf(stdout, "Service started.\n")
		case "stop":
			if err := service.Stop(); err != nil {
				fmt.Fprintf(stderr, "%s: service stop: %v\n", binaryName, err)
				return 1
			}
			fmt.Fprintf(stdout, "Service stopped.\n")
		case "restart":
			if err := service.Restart(); err != nil {
				fmt.Fprintf(stderr, "%s: service restart: %v\n", binaryName, err)
				return 1
			}
			fmt.Fprintf(stdout, "Service restarted.\n")
		case "reload":
			if err := service.Reload(); err != nil {
				fmt.Fprintf(stderr, "%s: service reload: %v\n", binaryName, err)
				return 1
			}
			fmt.Fprintf(stdout, "Service reloaded.\n")
		case "--install":
			if err := service.Install(); err != nil {
				fmt.Fprintf(stderr, "%s: service install: %v\n", binaryName, err)
				return 1
			}
		case "--disable":
			if err := service.Disable(); err != nil {
				fmt.Fprintf(stderr, "%s: service disable: %v\n", binaryName, err)
				return 1
			}
			fmt.Fprintf(stdout, "Service disabled.\n")
		case "--uninstall":
			if err := service.Uninstall(); err != nil {
				fmt.Fprintf(stderr, "%s: service uninstall: %v\n", binaryName, err)
				return 1
			}
		default:
			fmt.Fprintf(stderr, "%s: unknown --service subcommand: %s\n", binaryName, serviceCmd)
			fmt.Fprintf(stderr, "Run '%s --service --help' for usage.\n", binaryName)
			return 2
		}
		return 0
	}

	// ── Maintenance operations ────────────────────────────────────────────────

	if maintenanceCmd != "" {
		// Paths must be resolved before maintenance commands.
		mcConfigDir := path.GetConfigDir(appName)
		mcDataDir := path.GetDataDir(appName)
		// Backups always live in the canonical backup dir (PART 21), matching
		// the scheduled backup tasks.
		mcBackupDir := path.GetBackupDir(appName)
		// Sensitive maintenance operations (setup/restore/mode) require
		// authorization proof (AI.md PART 5 sensitive operations).
		mcAuthOpts := maintenance.AuthOptions{
			ConfigDir:   mcConfigDir,
			DBPath:      path.GetDBPath(appName),
			LogDir:      path.GetLogsDir(appName),
			ServiceUser: appName,
		}

		switch maintenanceCmd {
		case "--help":
			maintenance.PrintHelp(binaryName)
		case "backup":
			// "backup test" is a dry-run verification of the security keypair,
			// not a backup creation (AI.md 14213).
			if maintenanceArg == "test" {
				btCfgFile := filepath.Join(mcConfigDir, "server.yml")
				btCfg, _ := config.Load(btCfgFile)
				if btCfg.Database.Path == "" {
					btCfg.Database.Path = path.GetDBPath(appName)
				}
				btDB, dbErr := database.NewDatabase(btCfg.Database.Type, btCfg.Database.Path)
				if dbErr != nil {
					fmt.Fprintf(stderr, "%s: backup test: database: %v\n", binaryName, dbErr)
					return 1
				}
				installSecret, secErr := btDB.EnsureAppSecret("installation_secret")
				btDB.Close()
				if secErr != nil {
					fmt.Fprintf(stderr, "%s: backup test: installation_secret: %v\n", binaryName, secErr)
					return 1
				}
				if err := maintenance.TestSecurityKeypair(mcConfigDir, installSecret); err != nil {
					fmt.Fprintf(stderr, "%s: %v\n", binaryName, err)
					return 1
				}
				return 0
			}
			// Passwords are never accepted via CLI flag (shell history / process
			// list leakage) — prompt interactively when encryption is enabled
			// (AI.md 29017).
			var backupPass string
			btCfgFile := filepath.Join(mcConfigDir, "server.yml")
			btCfg, btCfgErr := config.Load(btCfgFile)
			if btCfgErr == nil && btCfg.Server.Backup.Encryption.Enabled {
				fmt.Fprint(stderr, "Backup password: ")
				pw, pwErr := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(stderr)
				if pwErr != nil {
					fmt.Fprintf(stderr, "%s: reading password: %v\n", binaryName, pwErr)
					return 1
				}
				backupPass = string(pw)
			}
			opts := maintenance.BackupOptions{
				ConfigDir:  mcConfigDir,
				DataDir:    mcDataDir,
				BackupDir:  mcBackupDir,
				AppVersion: Version,
				Password:   backupPass,
				// optional custom filename
				Filename: maintenanceArg,
				// M11: compliance mode blocks unencrypted backups (AI.md 28945-28989).
				ComplianceEnabled: btCfgErr == nil && btCfg.Server.Compliance.Enabled,
			}
			if err := maintenance.Backup(opts); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance backup: %v\n", binaryName, err)
				return 1
			}
		case "restore":
			if maintenanceArg == "" {
				fmt.Fprintf(stderr, "%s: --maintenance restore requires a filename argument\n", binaryName)
				fmt.Fprintf(stderr, "Usage: %s --maintenance restore <backup-file>\n", binaryName)
				return 2
			}
			if err := maintenance.AuthorizeRestore(mcAuthOpts); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance restore: %v\n", binaryName, err)
				return 1
			}
			// Passwords are never accepted via CLI flag (shell history / process
			// list leakage) — always prompt interactively for encrypted backups
			// (AI.md 29017).
			var restorePass string
			if strings.HasSuffix(maintenanceArg, ".enc") {
				fmt.Fprint(stderr, "Backup password: ")
				pw, pwErr := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(stderr)
				if pwErr != nil {
					fmt.Fprintf(stderr, "%s: reading password: %v\n", binaryName, pwErr)
					return 1
				}
				restorePass = string(pw)
			}
			if err := maintenance.RestoreWithVersion(maintenanceArg, mcConfigDir, mcDataDir, restorePass, Version); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance restore: %v\n", binaryName, err)
				return 1
			}
			maintenance.AuditMaintenanceEvent(mcAuthOpts, "backup.restored", map[string]any{
				"filename": filepath.Base(maintenanceArg),
			})
			return 0
		case "update":
			// Alias for --update yes: handled by the update block below.
			updateCmd = "yes"
		case "mode":
			if maintenanceArg == "" {
				fmt.Fprintf(stderr, "%s: --maintenance mode requires a mode argument\n", binaryName)
				fmt.Fprintf(stderr, "Usage: %s --maintenance mode <production|development>\n", binaryName)
				return 2
			}
			if err := maintenance.AuthorizeMode(mcAuthOpts); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance mode: %v\n", binaryName, err)
				return 1
			}
			if err := maintenance.SetMode(mcConfigDir, maintenanceArg); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance mode: %v\n", binaryName, err)
				return 1
			}
			maintenance.AuditMaintenanceEvent(mcAuthOpts, "config.updated", map[string]any{
				"changed_keys": []string{"mode"},
				"mode":         maintenanceArg,
			})
			return 0
		case "setup":
			if err := maintenance.AuthorizeSetup(mcAuthOpts); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance setup: %v\n", binaryName, err)
				return 1
			}
			if err := maintenance.Setup(mcConfigDir); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance setup: %v\n", binaryName, err)
				return 1
			}
			return 0
		case "pgp":
			// Project security keypair management (AI.md 14180-14188).
			if maintenanceArg == "" {
				fmt.Fprintf(stderr, "%s: --maintenance pgp requires an action\n", binaryName)
				fmt.Fprintf(stderr, "Usage: %s --maintenance pgp <generate|rotate|publish|export|import|delete>\n", binaryName)
				return 2
			}
			pgpOpts := maintenance.PGPOptions{
				ConfigDir: mcConfigDir,
				DataDir:   mcDataDir,
				DBPath:    path.GetDBPath(appName),
				LogDir:    path.GetLogsDir(appName),
			}
			if err := maintenance.RunPGP(maintenanceArg, maintenancePGP, pgpOpts); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance pgp: %v\n", binaryName, err)
				return 1
			}
			return 0
		case "token":
			if maintenanceArg == "" {
				fmt.Fprintf(stderr, "%s: --maintenance token requires a subcommand\n", binaryName)
				fmt.Fprintf(stderr, "Usage: %s --maintenance token {list|revoke} [prefix]\n", binaryName)
				return 2
			}
			tkCfgFile := filepath.Join(mcConfigDir, "server.yml")
			tkCfg, _ := config.Load(tkCfgFile)
			if tkCfg.Database.Path == "" {
				tkCfg.Database.Path = path.GetDBPath(appName)
			}
			tkDB, err := database.NewDatabase(tkCfg.Database.Type, tkCfg.Database.Path)
			if err != nil {
				fmt.Fprintf(stderr, "%s: token: database: %v\n", binaryName, err)
				return 1
			}
			defer tkDB.Close()

			var tokenPrefix string
			if len(maintenancePGP) > 0 {
				tokenPrefix = maintenancePGP[0]
			}
			switch maintenanceArg {
			case "list":
				ownerDB, ok := tkDB.(database.OwnerDB)
				if !ok {
					fmt.Fprintf(stderr, "%s: token list: owner token storage unavailable\n", binaryName)
					return 1
				}
				tokens, err := ownerDB.ListAllAPITokens()
				if err != nil {
					fmt.Fprintf(stderr, "%s: token list: %v\n", binaryName, err)
					return 1
				}
				if len(tokens) == 0 {
					fmt.Fprintln(stdout, "No active tokens.")
				} else {
					fmt.Fprintf(stdout, "%-14s %-8s %-14s %-18s %-24s %-24s %-10s\n", "PREFIX", "KIND", "OWNER", "NAME", "CREATED", "LAST USED", "STATUS")
					fmt.Fprintf(stdout, "%-14s %-8s %-14s %-18s %-24s %-24s %-10s\n", "--------------", "--------", "--------------", "------------------", "------------------------", "------------------------", "----------")
					for _, t := range tokens {
						status := "active"
						if t.RevokedAt != nil {
							status = "revoked"
						} else if t.ExpiresAt != nil && !t.ExpiresAt.After(time.Now()) {
							status = "expired"
						}
						lastUsed := "never"
						if t.LastUsedAt != nil {
							lastUsed = t.LastUsedAt.Format("2006-01-02 15:04:05")
						}
						fmt.Fprintf(stdout, "%-14s %-8s %-14s %-18s %-24s %-24s %-10s\n", t.TokenPrefix, t.Kind, t.OwnerID, t.Name, t.CreatedAt.Format("2006-01-02 15:04:05"), lastUsed, status)
					}
				}
			case "revoke":
				if tokenPrefix == "" {
					fmt.Fprintf(stderr, "Usage: %s --maintenance token revoke <prefix>\n", binaryName)
					return 2
				}
				if err := tkDB.RevokeAPIToken(tokenPrefix, "revoked via CLI"); err != nil {
					fmt.Fprintf(stderr, "%s: token revoke: %v\n", binaryName, err)
					return 1
				}
				fmt.Fprintf(stdout, "Token %s revoked.\n", tokenPrefix)
			default:
				fmt.Fprintf(stderr, "%s: unknown token subcommand: %s\n", binaryName, maintenanceArg)
				fmt.Fprintf(stderr, "Usage: %s --maintenance token {list|revoke} [prefix]\n", binaryName)
				return 2
			}
			return 0
		case "data":
			if maintenanceArg == "" {
				fmt.Fprintf(stderr, "%s: --maintenance data requires an action\n", binaryName)
				fmt.Fprintf(stderr, "Usage: %s --maintenance data {export|delete} <prefix>\n", binaryName)
				return 2
			}
			var dataPrefix string
			if len(maintenancePGP) > 0 {
				dataPrefix = maintenancePGP[0]
			}
			dataOpts := maintenance.DataOptions{
				ConfigDir: mcConfigDir,
				DBPath:    path.GetDBPath(appName),
				LogDir:    path.GetLogsDir(appName),
			}
			if err := maintenance.AuthorizeDataOp(mcAuthOpts); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance data: %v\n", binaryName, err)
				return 1
			}
			if err := maintenance.RunData(maintenanceArg, dataPrefix, dataOpts); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance data: %v\n", binaryName, err)
				return 1
			}
			return 0
		case "compliance":
			if maintenanceArg == "" {
				maintenanceArg = "report"
			}
			complianceOpts := maintenance.ComplianceOptions{
				ConfigDir: mcConfigDir,
				LogDir:    path.GetLogsDir(appName),
			}
			if err := maintenance.RunCompliance(maintenanceArg, complianceOpts); err != nil {
				fmt.Fprintf(stderr, "%s: maintenance compliance: %v\n", binaryName, err)
				return 1
			}
			return 0
		default:
			fmt.Fprintf(stderr, "%s: unknown --maintenance subcommand: %s\n", binaryName, maintenanceCmd)
			fmt.Fprintf(stderr, "Run '%s --maintenance --help' for usage.\n", binaryName)
			return 2
		}
		// All maintenance subcommands return here except "update" which falls
		// through to the --update block below.
		if maintenanceCmd != "update" {
			return 0
		}
	}

	// ── Tor hidden service operations (AI.md PART 31.1) ─────────────────────────
	// Bare positional subcommand: "{project_name} tor {status|validate|restart|
	// regenerate|vanity start|vanity stop|vanity apply|import-keys <path>}". Tor
	// is configured via server.yml and CLI only — there is no REST API for it.

	if len(positional) > 0 && positional[0] == "tor" {
		return runTorCommand(binaryName, positional[1:], stdout, stderr)
	}

	// ── Update ────────────────────────────────────────────────────────────────

	if updateCmd != "" {
		// Load config to resolve configured update branch.
		updateCfgFile := filepath.Join(path.GetConfigDir(appName), "server.yml")
		updateCfg, _ := config.Load(updateCfgFile)
		configuredBranch := updateCfg.Server.Update.Branch
		if configuredBranch == "" {
			configuredBranch = "stable"
		}

		switch {
		case updateCmd == "--help":
			fmt.Fprintf(stdout, `Update: %s --update [command]

Commands:
  check              Check for updates without installing
  yes                Download and install the latest update, then restart
  branch <name>      Switch update branch (stable|beta|daily)

Examples:
  %s --update check
  %s --update yes
  %s --update branch beta
`, binaryName, binaryName, binaryName, binaryName)
			return 0

		case updateCmd == "check":
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			rel, err := updater.CheckForUpdate(ctx, Version, configuredBranch, buildEpoch())
			if err != nil {
				fmt.Fprintf(stderr, "%s: update check: %v\n", binaryName, err)
				return 1
			}
			if rel == nil {
				fmt.Fprintf(stdout, "%s is up to date (%s) on branch %s.\n", binaryName, Version, configuredBranch)
			} else {
				fmt.Fprintf(stdout, "Update available: %s → %s\n", Version, rel.TagName)
				fmt.Fprintf(stdout, "Run '%s --update yes' to install.\n", binaryName)
			}
			return 0

		case updateCmd == "yes":
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			rel, err := updater.CheckForUpdate(ctx, Version, configuredBranch, buildEpoch())
			if err != nil {
				fmt.Fprintf(stderr, "%s: update check: %v\n", binaryName, err)
				return 1
			}
			if rel == nil {
				fmt.Fprintf(stdout, "%s is already up to date (%s) on branch %s.\n", binaryName, Version, configuredBranch)
				return 0
			}
			fmt.Fprintf(stdout, "Downloading %s %s…\n", binaryName, rel.TagName)
			if err := updater.DoUpdate(ctx, rel); err != nil {
				fmt.Fprintf(stderr, "%s: update failed: %v\n", binaryName, err)
				return 1
			}
			fmt.Fprintf(stdout, "Update installed. Restarting…\n")
			if err := updater.RestartSelf(); err != nil {
				fmt.Fprintf(stderr, "%s: restart failed: %v\n", binaryName, err)
				return 1
			}
			return 0

		case strings.HasPrefix(updateCmd, "branch"):
			parts := strings.Fields(updateCmd)
			if len(parts) < 2 {
				fmt.Fprintf(stderr, "Usage: %s --update branch <stable|beta|daily>\n", binaryName)
				return 2
			}
			branch := parts[1]
			switch branch {
			case "stable", "beta", "daily":
				// Persist the branch choice to the config file (server.update.branch).
				if writeErr := config.SetUpdateBranch(updateCfgFile, branch); writeErr != nil {
					fmt.Fprintf(stderr, "%s: could not persist update branch: %v\n", binaryName, writeErr)
					return 1
				}
				fmt.Fprintf(stdout, "Update branch set to: %s\n", branch)
			default:
				fmt.Fprintf(stderr, "%s: unknown branch: %s (use stable|beta|daily)\n", binaryName, branch)
				return 2
			}
			return 0

		default:
			fmt.Fprintf(stderr, "%s: unknown --update subcommand: %s\n", binaryName, updateCmd)
			fmt.Fprintf(stderr, "Run '%s --update --help' for usage.\n", binaryName)
			return 2
		}
	}

	// Resolve active language for CLI/startup-banner output (PART 30).
	// The config file has not been loaded at this point, so only the flag and
	// the environment participate here; activeLang is re-resolved after the
	// config load below so server.yml's lang: key takes effect.
	activeLang := i18n.GetLanguage(langFlag, "")

	// ── Daemon ────────────────────────────────────────────────────────────────

	if daemonFlag {
		if err := daemon.Daemonize(activeLang); err != nil {
			log.Printf("daemon: %v", err)
			// EX_OSERR
			os.Exit(71)
		}
		// If Daemonize returned without exiting, we are the daemon child.
		// Continue with normal server startup below.
	}

	// ── Application mode ─────────────────────────────────────────────────────

	// Priority order for mode: (1) --mode CLI flag, (2) MODE env var, (3) default "production" (PART 6).
	// Priority order for debug: (1) --debug CLI flag, (2) DEBUG env var, (3) --mode debug/MODE=debug
	// alias (default-on), (4) default false. An explicit DEBUG env var always wins over the alias,
	// regardless of whether "debug" came from the MODE env var or the --mode CLI flag.
	mode.ApplyModeAndDebug(modeFlag, debugFlag)

	if debugFlag {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
		log.Printf("debug mode enabled")
	} else if mode.IsDebugEnabled() {
		log.SetFlags(log.LstdFlags | log.Lshortfile)
		log.Printf("debug mode enabled via DEBUG env var")
	}

	// ── Directory resolution ─────────────────────────────────────────────────

	configDir := path.GetConfigDir(appName)
	dataDir := path.GetDataDir(appName)
	logsDir := path.GetLogsDir(appName)
	cacheDir := path.GetCacheDir(appName)
	backupDir := path.GetBackupDir(appName)
	pidFile := path.GetPIDFile(appName)

	if dataFlag != "" {
		dataDir = dataFlag
		// Spec (PART 4): cache and backup are subdirectories of dataDir.
		// Cascade the override so --data moves all data-relative dirs together,
		// unless the operator has explicitly overridden each one separately.
		if cacheFlag == "" {
			cacheDir = filepath.Join(dataDir, "cache")
		}
		if backupFlag == "" {
			backupDir = filepath.Join(dataDir, "backup")
		}
	}
	if logFlag != "" {
		logsDir = logFlag
	}
	if cacheFlag != "" {
		cacheDir = cacheFlag
	}
	if backupFlag != "" {
		backupDir = backupFlag
	}
	if pidFlag != "" {
		pidFile = pidFlag
	}

	for _, dir := range []string{configDir, dataDir, logsDir, cacheDir, backupDir} {
		if err := path.EnsureDir(dir); err != nil {
			log.Printf("warning: could not create directory %s: %v", dir, err)
		}
	}

	// Ensure parent of PID file exists.
	if err := path.EnsureDir(filepath.Dir(pidFile)); err != nil {
		log.Printf("warning: could not create pid file directory: %v", err)
	}

	// --status probes the live server via HTTP and exits 0=healthy, 1=unhealthy (PART 8).
	// Must run after paths are resolved but loads config independently.
	if showStatus {
		cfgPath := filepath.Join(configDir, "server.yml")
		if configFlag != "" {
			cfgPath = configFlag
		}
		statusCfg, _ := config.Load(cfgPath)
		port := statusCfg.Server.Port
		if port == "" {
			port = "80"
		}
		// AI.md PART 11 Display Rules: never hardcode a static IP such as
		// 127.0.0.1 — use the "localhost" name for this loopback self-probe.
		address := statusCfg.Server.Address
		if address == "" || address == "0.0.0.0" {
			address = "localhost"
		}
		apiVersion := strings.TrimSpace(statusCfg.Server.APIVersion)
		if apiVersion == "" {
			apiVersion = "v1"
		}
		probeURL := "http://" + address + ":" + port + "/api/" + apiVersion + "/server/healthz"
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(probeURL)
		if err != nil || resp.StatusCode >= 500 {
			fmt.Fprintf(stderr, "%s: unhealthy — %v\n", binaryName, err)
			return 1
		}
		resp.Body.Close()
		fmt.Fprintf(stdout, "%s: healthy (port %s)\n", binaryName, port)
		return 0
	}

	// ── Load config ───────────────────────────────────────────────────────────

	cfgFile := filepath.Join(configDir, "server.yml")
	if configFlag != "" {
		cfgFile = configFlag
	}

	// Auto-migrate server.yaml → server.yml if the old filename exists and the new one does not.
	if configFlag == "" {
		yamlPath := filepath.Join(configDir, "server.yaml")
		if _, statErr := os.Stat(yamlPath); statErr == nil {
			if _, statErr2 := os.Stat(cfgFile); os.IsNotExist(statErr2) {
				if renameErr := os.Rename(yamlPath, cfgFile); renameErr == nil {
					log.Printf("config: migrated server.yaml → server.yml")
				} else {
					log.Printf("config: could not migrate server.yaml → server.yml: %v", renameErr)
				}
			}
		}
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		log.Printf("warning: config load: %v", err)
	}

	// PART 17: custom email templates override embedded defaults from
	// {config_dir}/template/email/ when present; empty dir falls back silently.
	cfg.Server.Notifications.Email.TemplateDir = filepath.Join(configDir, "template", "email")

	// PART 23 startup step 8a: while still root, create the dedicated service
	// account (if missing) before directory setup and the later privilege drop.
	// No-op for non-root and on Windows (VSA). --service --install never does this.
	if err := service.EnsureServiceAccount(cfg.Server.User); err != nil {
		log.Printf("warning: could not ensure service account: %v", err)
	}

	// CLI flag overrides on config.
	if portFlag != "" {
		cfg.Server.Port = portFlag
	}
	if addressFlag != "" {
		cfg.Server.Address = addressFlag
	}
	if baseurlFlag != "" {
		cfg.Server.BaseURL = baseurlFlag
	}
	// Priority order for I2P: (1) --i2p CLI flag, (2) I2P_ENABLED env var
	// (applied in config.Load), (3) server.yml, (4) default false (PART 31.2).
	if i2pFlag {
		cfg.Server.I2P.Enabled = true
	}
	// Reconcile the application mode into the config so the health/version response
	// (PART 13) and CSP report-only logic report the mode the server actually runs in.
	// Priority (PART 6): --mode flag → MODE env → config-file mode → default production.
	// mode.Get() already resolved flag+env; a config-file mode applies only when neither was set.
	switch {
	case modeFlag != "":
		cfg.Server.Mode = modeFlag
	case os.Getenv("MODE") != "":
		cfg.Server.Mode = mode.Get().String()
	case cfg.Server.Mode != "":
		if err := mode.Set(cfg.Server.Mode); err != nil {
			log.Printf("warning: invalid config mode %q, using %s: %v", cfg.Server.Mode, mode.Get(), err)
			cfg.Server.Mode = mode.Get().String()
		}
		// mode.Set() unconditionally re-triggers the debug-on alias when the
		// config-file mode is "debug"; re-apply the already-resolved DEBUG
		// env/--debug precedence (PART 6) so a config-file debug alias never
		// overrides an explicit DEBUG=false or the absence of --debug.
		if debugEnvVal, debugEnvSet := os.LookupEnv("DEBUG"); debugEnvSet {
			mode.SetDebugEnabled(config.IsTruthy(debugEnvVal))
		} else if !debugFlag {
			mode.SetDebugEnabled(false)
		}
		if debugFlag {
			mode.SetDebugEnabled(true)
		}
	default:
		cfg.Server.Mode = mode.Get().String()
	}

	// Re-resolve the output language now that server.yml is loaded, so the
	// config-file lang: key takes its documented priority-2 position between the
	// --lang flag and the system locale (PART 30).
	activeLang = i18n.GetLanguage(langFlag, cfg.Server.Lang)

	// ── Port resolution (random 64xxx on first run; 80 in container) ──────────

	if err := config.ResolvePort(cfgFile, cfg, path.IsContainer()); err != nil {
		log.Printf("warning: %v", err)
		if cfg.Server.Port == "" {
			// last-resort fallback
			cfg.Server.Port = "64080"
		}
	}

	// ── SMTP auto-detect (first run, host not yet configured) ─────────────────

	if cfg.Server.Notifications.Email.SMTP.Host == "" {
		if host, port, ok := email.AutoDetect(cfg.Server.FQDN); ok {
			cfg.Server.Notifications.Email.SMTP.Host = host
			cfg.Server.Notifications.Email.SMTP.Port = port
			cfg.Server.Notifications.Email.Enabled = true
			if saveErr := config.Save(cfgFile, cfg); saveErr != nil {
				log.Printf("warning: could not persist SMTP config: %v", saveErr)
			}
		}
	}

	// ── Config hot-reload watcher ─────────────────────────────────────────────

	cfgMgr := config.NewConfigManager(cfgFile, cfg)

	// ── DataDir in config (used by blocklist middleware and tasks) ───────────

	if cfg.Server.DataDir == "" {
		cfg.Server.DataDir = dataDir
	}

	// ── GeoIP directory ───────────────────────────────────────────────────────

	if cfg.Server.GeoIP.Dir == "" {
		cfg.Server.GeoIP.Dir = filepath.Join(dataDir, "security", "geoip")
	}
	if cfg.Server.GeoIP.Enabled {
		if err := path.EnsureDir(cfg.Server.GeoIP.Dir); err != nil {
			log.Printf("warning: geoip dir: %v", err)
		}
	}

	// ── Database ──────────────────────────────────────────────────────────────

	// libsql/turso is remote-only: its "path" is a server URL, so neither the
	// on-disk default nor the parent-directory creation applies to it.
	remoteDB := false
	switch strings.ToLower(strings.TrimSpace(cfg.Database.Type)) {
	case "libsql", "turso":
		remoteDB = true
	}
	if !remoteDB {
		if cfg.Database.Path == "" {
			// Derive from the (possibly --data-overridden) dataDir, not the global
			// path.GetDBPath() which ignores the --data flag.
			cfg.Database.Path = filepath.Join(dataDir, "db", "server.db")
		}
		if err := path.EnsureDir(filepath.Dir(cfg.Database.Path)); err != nil {
			log.Printf("warning: db dir: %v", err)
		}
	}

	db, err := database.NewDatabaseWithToken(cfg.Database.Type, cfg.Database.Path, cfg.Database.Token, database.PoolConfig{
		MaxOpen:     cfg.Database.Pool.MaxOpen,
		MaxIdle:     cfg.Database.Pool.MaxIdle,
		MaxLifetime: cfg.Database.Pool.MaxLifetime,
		MaxIdleTime: cfg.Database.Pool.MaxIdleTime,
	})
	if err != nil {
		log.Printf("database: %v", err)
		// EX_SOFTWARE
		os.Exit(70)
	}
	defer db.Close()

	log.Printf("%s %s — database: %s (%s)", appName, Version, db.Type(), cfg.Database.Path)

	// ── Background scheduler ──────────────────────────────────────────────────

	sched := scheduler.New(db)

	// Scheduler timezone (PART 18): config → TZ env → America/New_York default.
	schedTZ := strings.TrimSpace(cfg.Server.Scheduler.Timezone)
	if schedTZ == "" {
		schedTZ = "America/New_York"
	}
	if loc, err := time.LoadLocation(schedTZ); err != nil {
		log.Printf("scheduler: invalid timezone %q, using system local: %v", schedTZ, err)
	} else {
		sched.SetLocation(loc)
	}

	// Catch-up window (PART 18): re-run tasks missed while down within this span.
	if w := strings.TrimSpace(cfg.Server.Scheduler.CatchUpWindow); w != "" {
		if d, err := time.ParseDuration(w); err != nil {
			log.Printf("scheduler: invalid catch_up_window %q, using default: %v", w, err)
		} else if d > 0 {
			sched.SetCatchUpWindow(d)
		}
	}

	// Project-specific: expire and burn-after pastes every 10 minutes.
	logSchedErr(sched.Register("expire-pastes", "Expire Pastes", "@every 10m", true, func(ctx context.Context) error {
		n, err := db.DeleteExpiredPastes()
		if err != nil {
			return err
		}
		b, _ := db.DeleteBurnedPastes()
		if n+b > 0 {
			log.Printf("scheduler: removed %d expired + %d burned pastes", n, b)
		}
		return nil
	}))

	// Build the mailer once for task email dispatch (PART 17: backup/ssl events).
	// The mailer is fail-open — if SMTP is unconfigured, Enabled() returns false
	// and all email sends are silently skipped.
	taskMailer := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, startupBaseURL(cfg), cfg.Server.FQDN)
	taskOperatorEmail := strings.TrimSpace(cfg.AdminEmail())

	// JSON Lines audit log (AI.md server.logs.audit): configuration, security,
	// backup, and server-lifecycle events, one JSON object per line. Built here
	// so backup tasks can record backup.skipped_disk_full events.
	ac := cfg.Server.Logging.Audit
	auditW := audit.New(audit.Config{
		Enabled:          ac.Enabled,
		Dir:              logsDir,
		Filename:         ac.Filename,
		IncludeUserAgent: ac.IncludeUserAgent,
		MaskEmails:       ac.MaskEmails,
		Events: audit.EventCategories{
			Configuration: ac.Events.Configuration,
			Security:      ac.Events.Security,
			Backup:        ac.Events.Backup,
			Server:        ac.Events.Server,
		},
	})

	// Logging manager (AI.md server.logs): owns access/server/error/app/auth/
	// debug log files plus scheduled rotation for audit.log and security.log.
	logCfg := cfg.Server.Logging
	logMgr := logging.New(logging.Options{
		Dir:       logsDir,
		Level:     logCfg.Level,
		Tag:       appName,
		DebugGate: mode.IsDebugEnabled,
		Access: logging.FileOptions{
			Enabled:  true,
			Filename: logCfg.Access.Filename,
			Format:   logCfg.Access.Format,
			Custom:   logCfg.Access.Custom,
			Rotate:   logCfg.Access.Rotate,
			Keep:     logCfg.Access.Keep,
		},
		Server: logFileOpts(logCfg.Server),
		Error:  logFileOpts(logCfg.Error),
		App:    logFileOpts(logCfg.App),
		Auth: logging.FileOptions{
			Enabled:  true,
			Filename: logCfg.Auth.Filename,
			Format:   logCfg.Auth.Format,
			Rotate:   logCfg.Auth.Rotate,
			Keep:     logCfg.Auth.Keep,
		},
		Debug: logging.FileOptions{
			Enabled:  logCfg.Debug.Enabled,
			Filename: logCfg.Debug.Filename,
			Format:   logCfg.Debug.Format,
			Custom:   logCfg.Debug.Custom,
			Rotate:   logCfg.Debug.Rotate,
			Keep:     logCfg.Debug.Keep,
		},
		Audit: logging.FileOptions{
			Enabled:  ac.Enabled,
			Filename: ac.Filename,
			Rotate:   ac.Rotate,
			Keep:     ac.Keep,
			Compress: ac.Compress,
		},
		Security: logFileOpts(logCfg.Security),
	})
	defer logMgr.Close()

	// Required PART 18 tasks — full implementations in src/task/task.go.
	logSchedErr(sched.Register("ssl_renewal", "SSL Renewal", "0 3 * * *", true,
		task.SSLRenewalWithEmail(task.SSLRenewalConfig{
			ConfigDir:         configDir,
			FQDN:              cfg.Server.FQDN,
			OperatorEmail:     taskOperatorEmail,
			Mailer:            taskMailer,
			SendExpiring:      cfg.Server.Notifications.Email.Events.SSLExpiring,
			SendRenewed:       cfg.Server.Notifications.Email.Events.SSLRenewed,
			SendRenewalFailed: cfg.Server.Notifications.Email.Events.SSLRenewalFailed,
		})))
	logSchedErr(sched.Register("blocklist_update", "Blocklist Update", "0 4 * * *", true,
		task.BlocklistUpdate(dataDir, blocklistSources(cfg)...)))
	cveTask := task.CVEUpdate(dataDir, cveSources(cfg)...)
	trivyCfg := cfg.Web.Security.Trivy
	// PART 18: trivy has no scheduled task of its own — cve_update owns both the
	// NVD CVE feed and the Trivy vulnerability database.
	logSchedErr(sched.Register("cve_update", "CVE Update", "0 5 * * *", true, func(ctx context.Context) error {
		if err := cveTask(ctx); err != nil {
			return err
		}
		return task.TrivyUpdate(ctx, dataDir, trivyCfg.Source, trivyCfg.Enabled)
	}))
	logSchedErr(sched.Register("token_cleanup", "Token Cleanup", "@every 15m", true, func(ctx context.Context) error {
		n, err := db.DeleteExpiredAPITokens()
		if err != nil {
			return err
		}
		if n > 0 {
			log.Printf("scheduler: removed %d expired/revoked API tokens", n)
		}
		if cfg.Server.Tokens.IdlePurgeDays > 0 {
			if ownerDB, ok := db.(database.OwnerDB); ok {
				purged, err := ownerDB.PurgeIdleOwners(cfg.Server.Tokens.IdlePurgeDays)
				if err != nil {
					return err
				}
				if purged > 0 {
					log.Printf("scheduler: purged %d idle owners", purged)
				}
			}
		}
		return nil
	}))
	logSchedErr(sched.Register("log_rotation", "Log Rotation", "0 0 * * *", true,
		task.LogRotation(logMgr)))
	backupCfg := task.BackupConfig{
		ProjectName:    appName,
		ConfigDir:      configDir,
		DataDir:        dataDir,
		BackupDir:      backupDir,
		AppVersion:     Version,
		OperatorEmail:  taskOperatorEmail,
		Mailer:         taskMailer,
		SendOnComplete: cfg.Server.Notifications.Email.Events.BackupComplete,
		SendOnFailed:   cfg.Server.Notifications.Email.Events.BackupFailed,
		Retention: task.BackupRetention{
			MaxBackups:   cfg.Server.Backup.Retention.MaxBackups,
			KeepWeekly:   cfg.Server.Backup.Retention.KeepWeekly,
			KeepMonthly:  cfg.Server.Backup.Retention.KeepMonthly,
			KeepYearly:   cfg.Server.Backup.Retention.KeepYearly,
			MaxTotalSize: cfg.Server.Backup.Retention.MaxTotalSize,
		},
		DiskThreshold: cfg.Server.Maintenance.Cleanup.DiskThreshold,
		Audit:         auditW,
		Compliance:    cfg.Server.Compliance.Enabled,
	}
	logSchedErr(sched.Register("backup_daily", "Backup Daily", "0 2 * * *", true,
		task.BackupDaily(backupCfg)))
	logSchedErr(sched.Register("backup_hourly", "Backup Hourly", "@hourly", false,
		task.BackupHourly(backupCfg)))
	// Public IP refresh (startup step 16, AI.md:10593): runs at startup and
	// every 12h; the schedule is hardcoded per spec, not operator-configurable.
	config.RefreshPublicIP()
	logSchedErr(sched.Register("public_ip_refresh", "Public IP Refresh", "@every 12h", true, func(ctx context.Context) error {
		config.RefreshPublicIP()
		return nil
	}))
	logSchedErr(sched.Register("healthcheck_self", "Health Check", "@every 5m", true, func(ctx context.Context) error {
		if err := db.Ping(); err != nil {
			return fmt.Errorf("healthcheck_self: database ping failed: %w", err)
		}
		log.Printf("healthcheck_self: ok")
		return nil
	}))

	// ── Runtime detection (PART 7/8) ──────────────────────────────────────────
	// Detect hostname and CPU count at startup — never hardcode these values.

	hostname, hostErr := os.Hostname()
	if hostErr != nil {
		hostname = "unknown"
		log.Printf("warning: could not detect hostname: %v", hostErr)
	}
	numCPU := runtime.NumCPU()
	log.Printf("host: %s | cpus: %d | mode: %s", hostname, numCPU, mode.Get())

	// ── HTTP server ───────────────────────────────────────────────────────────

	srv := server.New(db, cfg, cfgMgr, Version, CommitID, BuildDate, configDir, dataDir)
	srv.SetLogDir(logsDir)
	srv.SetAuditWriter(auditW)
	srv.SetLogManager(logMgr)

	// Weekly GeoIP database refresh (Sunday 03:00).
	logSchedErr(sched.Register("geoip_update", "GeoIP Update", "0 3 * * 0", srv.GeoIPEnabled(), func(ctx context.Context) error {
		if err := srv.UpdateGeoIP(); err != nil {
			return err
		}
		log.Printf("scheduler: geoip databases updated")
		return nil
	}))

	// Branding image refresh (PART 16 "Image Scaling"): re-fetches/re-renders
	// the cached favicon/logo/OG-image variants on the operator-configured
	// cadence (default 24h) so a remote branding URL's content changes
	// eventually propagate without requiring a restart.
	logSchedErr(sched.Register("branding_refresh", "Branding Image Refresh",
		fmt.Sprintf("@every %s", cfg.Server.Branding.EffectiveImageRefreshInterval()), true, func(ctx context.Context) error {
			if err := srv.RefreshBrandingImages(); err != nil {
				return err
			}
			log.Printf("scheduler: branding images refreshed")
			return nil
		}))

	// Tor health check — registered after srv so it can query srv.TorRunning().
	logSchedErr(sched.Register("tor_health", "Tor Health", "@every 10m", true,
		task.TorHealth(srv.TorRunning, srv.TorRestart)))

	// I2P health check (PART 18): only registered when the eepsite is opt-in
	// enabled — there is no I2P provider to watch otherwise.
	if cfg.Server.I2P.Enabled {
		logSchedErr(sched.Register("i2p_health", "I2P Health", "@every 10m", true,
			task.I2PHealth(srv.I2PRunning, srv.I2PRestart)))
	}

	// Daily update check (PART 18/22): runs at 06:00, notify-only by default;
	// auto-installs when server.update.auto_install is true (default false).
	logSchedErr(sched.Register("update_check", "Update Check", "0 6 * * *", true,
		task.UpdateCheck(Version, cfg.Server.Update.Branch, buildEpoch(), taskOperatorEmail, cfg.Server.Update.AutoInstall, cfg.Server.Update.DeferDays,
			cfg.Server.Notifications.Email.Events.UpdateAvailable, cfg.Server.Notifications.Email.Events.UpdateInstalled, taskMailer)))

	// Retry policy (PART 18). Network-dependent tasks retry on failure by
	// default with a 1h base delay; operators override per task in server.yml.
	sched.SetRetry("blocklist_update", true, time.Hour, 0)
	sched.SetRetry("cve_update", true, time.Hour, 0)
	sched.SetRetry("branding_refresh", true, time.Hour, 0)
	for id, tc := range cfg.Server.Scheduler.Tasks {
		var delay time.Duration
		if tc.RetryDelay != "" {
			if d, err := time.ParseDuration(tc.RetryDelay); err == nil {
				delay = d
			}
		}
		sched.SetRetry(id, tc.RetryOnFail, delay, 0)
	}

	// Failure notification + audit logging (PART 18 task execution flow): every
	// run is audited; failures also send a scheduler_error email to the operator.
	sched.SetNotifier(schedulerNotifier(cfg, auditW))

	// One full retention sweep of the backup dir before the scheduler starts
	// (startup step 16, AI.md:10595–10596) — clears accumulation from crashed
	// or failed prior runs.
	if err := task.RetentionSweep(backupCfg); err != nil {
		log.Printf("backup: startup retention sweep: %v", err)
	}

	sched.Start()
	defer sched.Stop()

	// Register scheduler health callback with the server for /server/healthz.
	srv.SetSchedulerHealthFn(sched.Running)
	srv.SetSchedulerAPI(sched)

	// ── PID file ──────────────────────────────────────────────────────────────
	// WritePIDFile also calls CheckPIDFile; it exits non-zero if another
	// instance of our binary is already running. Skipped entirely inside a
	// container: the container runtime supervises the process, so a PID file is
	// never created or checked there (PART 8).

	if !path.IsContainer() {
		if err := pid.WritePIDFile(pidFile); err != nil {
			log.Printf("pid file: %v", err)
			// EX_IOERR
			os.Exit(74)
		}
		defer pid.RemovePIDFile(pidFile) //nolint:errcheck
	}

	addr := cfg.Server.Address + ":" + cfg.Server.Port

	// Register privilege drop: after binding the listen port as root, switch to the
	// unprivileged service account, chowning runtime paths first (PART 23 step 8g).
	srv.SetPrivilegeDrop(cfg.Server.User, cfg.Server.Group, []string{
		configDir, dataDir, logsDir, cacheDir, backupDir, filepath.Dir(pidFile),
	})

	// runServer is the shared startup closure used both for interactive mode
	// and for the Windows Service Control Manager path.
	runServer := func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Graceful-shutdown signals (platform-specific set, PART 8).
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, shutdownSignals()...)

		// Non-shutdown platform signals (Unix only): SIGHUP ignored (config
		// auto-reloads), SIGUSR1 reopens logs, SIGUSR2 dumps a status summary.
		installPlatformSignals(
			func() {
				log.Printf("SIGUSR1: reopening log files")
				if err := logMgr.Reopen(); err != nil {
					log.Printf("reopen logs: %v", err)
				}
			},
			func() {
				log.Printf("SIGUSR2: status pid=%d version=%s mode=%s listen=%s goroutines=%d",
					os.Getpid(), Version, string(mode.Get()), addr, runtime.NumGoroutine())
			},
		)

		stopCfgMgr := make(chan struct{})
		cfgMgr.Start(stopCfgMgr, srv.OnConfigChange)

		go func() {
			<-sig
			log.Printf("shutting down…")
			logMgr.Server("info", "server shutting down")
			if !path.IsContainer() {
				pid.RemovePIDFile(pidFile) //nolint:errcheck
			}
			close(stopCfgMgr)
			cancel()
		}()

		// Emit the startup banner with the protocol-correct URL (PART 12/15).
		// startupScheme() resolves {proto} from config (TLS, LE, port 443 → https,
		// otherwise http). The listen address addr is raw ":PORT"; the banner URLs
		// show the public base URL and the listen address with protocol.
		proto := startupScheme(cfg)
		publicURL := startupBaseURL(cfg)
		// Listen URL is a diagnostic showing the bound interface. Never display a
		// wildcard bind address (0.0.0.0/::) — resolve the real FQDN/IP instead
		// (PART 7); strip :80/:443 for display.
		listenHost := strings.TrimSpace(cfg.Server.Address)
		switch listenHost {
		case "", "0.0.0.0", "::", "[::]":
			listenHost = cfg.ResolveFQDN()
			if listenHost == "" {
				listenHost = "localhost"
			}
		}
		listenPort := cfg.Server.Port
		if strings.Contains(listenPort, ",") {
			listenPort = ""
		} else if (proto == "http" && listenPort == "80") || (proto == "https" && listenPort == "443") {
			listenPort = ""
		}
		listenURL := proto + "://" + listenHost
		if listenPort != "" {
			listenURL += ":" + listenPort
		}
		// Tor URL: http:// on overlays unless clearnet is HTTPS-only (single
		// port 443) — overlay networks inherit HTTPS-only mode (AI.md:19916-19942).
		torURL := ""
		if onion := cfg.Server.Tor.OnionAddress; onion != "" {
			torProto := "http"
			if strings.TrimSpace(cfg.Server.Port) == "443" {
				torProto = "https"
			}
			torURL = torProto + "://" + onion
		}
		banner.PrintStartupBanner(banner.BannerConfig{
			AppName:   appName,
			Version:   Version,
			AppMode:   string(mode.Get()),
			Debug:     mode.IsDebugEnabled(),
			Lang:      activeLang,
			PublicURL: publicURL,
			ListenURL: listenURL,
			TorURL:    torURL,
			FirstRun:  cfg.FirstRun,
		})
		log.Printf("listening on %s", listenURL)
		// First run: point the operator at the generated config file
		// (startup step 20, AI.md:10631).
		if cfg.FirstRun {
			log.Printf("first run: generated config at %s", cfgFile)
		}
		logMgr.Server("info", "server started",
			"address", listenURL,
			"version", Version,
			"mode", string(mode.Get()))

		if err := srv.Run(ctx, addr); err != nil {
			log.Printf("server: %v", err)
			logMgr.Error("server run failed", "error", err.Error())
			// EX_SOFTWARE
			os.Exit(70)
		}
	}

	// On Windows, if we were started by the SCM, hand control to the service
	// handler. On all other platforms (and interactive Windows), run directly.
	if service.IsWindowsService() {
		if err := service.RunAsWindowsService(appName, runServer); err != nil {
			log.Printf("windows service: %v", err)
			// EX_SOFTWARE
			os.Exit(70)
		}
		return 0
	}

	runServer()
	return 0
}

// torControlEnvelope decodes the standard {"ok":bool,"data"/"error"+"message"}
// response envelope returned by every /server/tor/* internal endpoint
// (AI.md PART 31.1).
type torControlEnvelope struct {
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

// torServerBaseURL detects whether a server is already running, mirroring the
// --status probe (config-resolved port + HTTP GET to the healthz endpoint,
// AI.md PART 8/31.1 — "no new discovery mechanism is introduced"). The CLI
// always runs on the same host as the server, so control requests target
// loopback explicitly; the internal /server/tor/* endpoints are themselves
// loopback-gated.
func torServerBaseURL(tcConfigDir string) (string, bool) {
	statusCfg, _ := config.Load(filepath.Join(tcConfigDir, "server.yml"))
	port := statusCfg.Server.Port
	if port == "" {
		port = "80"
	}
	apiVersion := strings.TrimSpace(statusCfg.Server.APIVersion)
	if apiVersion == "" {
		apiVersion = "v1"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/api/" + apiVersion + "/server/healthz")
	if err != nil || resp.StatusCode >= 500 {
		return "", false
	}
	resp.Body.Close()
	return "http://127.0.0.1:" + port, true
}

// torControlRequest issues an HTTP request to one of the running server's
// internal /server/tor/* control endpoints (AI.md PART 31.1) and decodes the
// response envelope. body is sent as-is (no Content-Type assumed) — callers
// that need JSON must marshal it themselves.
func torControlRequest(method, baseURL, urlPath string, body []byte, contentType string) (*torControlEnvelope, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, baseURL+urlPath, reqBody)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var env torControlEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !env.OK {
		msg := env.Message
		if msg == "" {
			msg = "request failed"
		}
		return &env, fmt.Errorf("%s", msg)
	}
	return &env, nil
}

// torControlTorInfo mirrors server.TorInfo for decoding /server/tor/status
// and /server/tor/restart responses.
type torControlTorInfo struct {
	Enabled  bool                 `json:"enabled"`
	Running  bool                 `json:"running"`
	Status   string               `json:"status"`
	Hostname string               `json:"hostname"`
	Vanity   torControlVanityInfo `json:"vanity"`
}

// torControlVanityInfo mirrors tor.VanityStatus for decoding the vanity
// progress block of /server/tor/status and /server/tor/vanity/* responses
// (AI.md PART 31.1, "Progress").
type torControlVanityInfo struct {
	State          string   `json:"state"`
	Prefix         string   `json:"prefix"`
	Workers        int      `json:"workers"`
	Attempts       uint64   `json:"attempts"`
	Rate           float64  `json:"rate"`
	ElapsedSeconds float64  `json:"elapsed_seconds"`
	Candidates     []string `json:"candidates"`
}

// printTorInfo renders a torControlTorInfo the same way the on-disk fallback
// status output reads, followed by the vanity-search block whenever a search
// is running or a candidate is waiting.
func printTorInfo(stdout io.Writer, info torControlTorInfo) {
	if !info.Enabled {
		fmt.Fprintf(stdout, "Tor Hidden Service: disabled (tor binary not found)\n")
		printVanityInfo(stdout, info.Vanity)
		return
	}
	if !info.Running {
		fmt.Fprintf(stdout, "Tor Hidden Service: not running\n")
		printVanityInfo(stdout, info.Vanity)
		return
	}
	fmt.Fprintf(stdout, "Tor Hidden Service: Connected\n")
	if info.Hostname != "" {
		fmt.Fprintf(stdout, "  Address: %s\n", info.Hostname)
	}
	printVanityInfo(stdout, info.Vanity)
}

// printVanityInfo renders the vanity-search progress block. Nothing is printed
// while the search is idle with no candidates on disk. Attempt counts are
// approximate (per-worker counters, sampled).
func printVanityInfo(stdout io.Writer, v torControlVanityInfo) {
	if v.State == "" || (v.State == "idle" && len(v.Candidates) == 0) {
		return
	}
	fmt.Fprintf(stdout, "Vanity Search: %s\n", v.State)
	if v.Prefix != "" {
		fmt.Fprintf(stdout, "  Prefix: %s\n", v.Prefix)
	}
	if v.Workers > 0 {
		fmt.Fprintf(stdout, "  Workers: %d\n", v.Workers)
	}
	if v.State == "running" {
		fmt.Fprintf(stdout, "  Attempts: ~%d (%.0f/sec, %.0fs elapsed)\n", v.Attempts, v.Rate, v.ElapsedSeconds)
	}
	for _, c := range v.Candidates {
		fmt.Fprintf(stdout, "  Candidate: %s\n", c)
	}
}

// confirmInput is the reader destructive-action confirmation prompts read
// from. It is a variable so tests can drive the prompt without a terminal.
var confirmInput io.Reader = os.Stdin

// confirmDestructive prints prompt and returns true only when the operator
// answers "y"/"yes". Used by the destructive Tor identity operations
// (regenerate, vanity apply) — the current .onion address stops working
// permanently once either completes (AI.md PART 31.1).
func confirmDestructive(stdout io.Writer, prompt string) bool {
	fmt.Fprintf(stdout, "%s [y/N]: ", prompt)
	reader := bufio.NewReader(confirmInput)
	line, _ := reader.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

// runTorCommand implements the "{project_name} tor <subcommand>" surface
// (AI.md PART 31.1). Tor is configured via server.yml and CLI only — there is
// no public REST API for it. The server binary owns the embedded Tor
// process, so mutating subcommands reach it over the internal, loopback-only
// /server/tor/* control channel; "status"/"validate" fall back to reading
// on-disk state when no server is detected.
func runTorCommand(binaryName string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "%s: tor: requires a subcommand\n", binaryName)
		fmt.Fprintf(stderr, "Usage: %s tor {status|validate|restart|regenerate|vanity {start|stop|apply}|import-keys <path>}\n", binaryName)
		return 2
	}

	tcConfigDir := path.GetConfigDir(appName)
	tcDataDir := path.GetDataDir(appName)
	tcCfg, _ := config.Load(filepath.Join(tcConfigDir, "server.yml"))
	torCfg := tor.Config{
		Binary:                    tcCfg.Server.Tor.Binary,
		UseNetwork:                tcCfg.Server.Tor.UseNetwork,
		MaxCircuits:               tcCfg.Server.Tor.MaxCircuits,
		CircuitTimeout:            tcCfg.Server.Tor.CircuitTimeout,
		BootstrapTimeout:          tcCfg.Server.Tor.BootstrapTimeout,
		SafeLogging:               tcCfg.Server.Tor.SafeLogging,
		MaxStreamsPerCircuit:      tcCfg.Server.Tor.MaxStreamsPerCircuit,
		CloseCircuitOnStreamLimit: tcCfg.Server.Tor.CloseCircuitOnStreamLimit,
		BandwidthRate:             tcCfg.Server.Tor.BandwidthRate,
		BandwidthBurst:            tcCfg.Server.Tor.BandwidthBurst,
		MaxMonthlyBandwidth:       tcCfg.Server.Tor.MaxMonthlyBandwidth,
		NumIntroPoints:            tcCfg.Server.Tor.NumIntroPoints,
		VirtualPort:               tcCfg.Server.Tor.VirtualPort,
		ConfigDir:                 tcConfigDir,
		DataDir:                   tcDataDir,
	}

	baseURL, serverUp := torServerBaseURL(tcConfigDir)

	sub := args[0]
	switch sub {
	case "status":
		if serverUp {
			env, err := torControlRequest(http.MethodGet, baseURL, "/server/tor/status", nil, "")
			if err != nil {
				fmt.Fprintf(stderr, "%s: tor status: %v\n", binaryName, err)
				return 1
			}
			var info torControlTorInfo
			if err := json.Unmarshal(env.Data, &info); err != nil {
				fmt.Fprintf(stderr, "%s: tor status: %v\n", binaryName, err)
				return 1
			}
			printTorInfo(stdout, info)
			return 0
		}
		bin := tor.FindBinary(torCfg.Binary)
		if bin == "" {
			fmt.Fprintf(stdout, "Tor Hidden Service: disabled (tor binary not found)\n")
			return 0
		}
		mgr := tor.NewManager(context.Background(), 0, torCfg, http.NotFoundHandler())
		if err := mgr.Start(); err != nil {
			fmt.Fprintf(stderr, "%s: tor status: %v\n", binaryName, err)
			return 1
		}
		defer mgr.Close()
		if !mgr.Running() {
			fmt.Fprintf(stdout, "Tor Hidden Service: not running\n")
			return 0
		}
		fmt.Fprintf(stdout, "Tor Hidden Service: Connected\n")
		fmt.Fprintf(stdout, "  Address: %s\n", mgr.OnionAddress())
		return 0

	case "validate":
		if serverUp {
			env, err := torControlRequest(http.MethodPost, baseURL, "/server/tor/validate", nil, "")
			if err != nil {
				fmt.Fprintf(stderr, "%s: tor validate: %v\n", binaryName, err)
				return 1
			}
			var data struct {
				Valid  bool `json:"valid"`
				Checks []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
					Detail string `json:"detail"`
				} `json:"checks"`
			}
			if err := json.Unmarshal(env.Data, &data); err != nil {
				fmt.Fprintf(stderr, "%s: tor validate: %v\n", binaryName, err)
				return 1
			}
			for _, c := range data.Checks {
				label := "OK"
				switch c.Status {
				case "warn":
					label = "WARN"
				case "fail":
					label = "FAIL"
				}
				fmt.Fprintf(stdout, "  [%s]   %s: %s\n", label, c.Name, c.Detail)
			}
			if !data.Valid {
				fmt.Fprintf(stderr, "%s: tor validate: configuration invalid\n", binaryName)
				return 1
			}
			fmt.Fprintf(stdout, "Tor configuration valid.\n")
			return 0
		}
		return validateTorConfig(binaryName, torCfg, stdout, stderr)

	case "restart":
		if !serverUp {
			fmt.Fprintf(stderr, "Error: no running server detected — start the server first\n")
			return 1
		}
		env, err := torControlRequest(http.MethodPost, baseURL, "/server/tor/restart", nil, "")
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor restart: %v\n", binaryName, err)
			return 1
		}
		var info torControlTorInfo
		if err := json.Unmarshal(env.Data, &info); err != nil {
			fmt.Fprintf(stderr, "%s: tor restart: %v\n", binaryName, err)
			return 1
		}
		fmt.Fprintf(stdout, "Tor restarted.\n")
		if info.Hostname != "" {
			fmt.Fprintf(stdout, "  Address: %s\n", info.Hostname)
		}
		return 0

	case "regenerate":
		if !serverUp {
			fmt.Fprintf(stderr, "Error: no running server detected — start the server first\n")
			return 1
		}
		if !confirmDestructive(stdout, "This permanently replaces the current .onion address. Continue?") {
			fmt.Fprintf(stdout, "Aborted.\n")
			return 1
		}
		env, err := torControlRequest(http.MethodPost, baseURL, "/server/tor/regenerate", nil, "")
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor regenerate: %v\n", binaryName, err)
			return 1
		}
		var data struct {
			Hostname string `json:"hostname"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fmt.Fprintf(stderr, "%s: tor regenerate: %v\n", binaryName, err)
			return 1
		}
		fmt.Fprintf(stdout, "New .onion address generated.\n")
		if data.Hostname != "" {
			fmt.Fprintf(stdout, "  Address: %s\n", data.Hostname)
		}
		return 0

	case "vanity":
		if len(args) < 2 {
			fmt.Fprintf(stderr, "%s: tor vanity: requires {start|stop|apply}\n", binaryName)
			return 2
		}
		return runTorVanity(binaryName, args[1], args[2:], baseURL, serverUp, stdout, stderr)

	case "import-keys":
		if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
			fmt.Fprintf(stderr, "%s: tor import-keys requires a path argument\n", binaryName)
			fmt.Fprintf(stderr, "Usage: %s tor import-keys <path>\n", binaryName)
			return 2
		}
		if !serverUp {
			fmt.Fprintf(stderr, "Error: no running server detected — start the server first\n")
			return 1
		}
		keyPath, err := filepath.Abs(strings.TrimSpace(args[1]))
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor import-keys: %v\n", binaryName, err)
			return 1
		}
		if _, err := os.Stat(keyPath); err != nil {
			fmt.Fprintf(stderr, "%s: tor import-keys: %v\n", binaryName, err)
			return 1
		}
		reqBody, err := json.Marshal(map[string]string{"path": keyPath})
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor import-keys: %v\n", binaryName, err)
			return 1
		}
		env, err := torControlRequest(http.MethodPost, baseURL, "/server/tor/import-keys", reqBody, "application/json")
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor import-keys: %v\n", binaryName, err)
			return 1
		}
		var data struct {
			Hostname string `json:"hostname"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fmt.Fprintf(stderr, "%s: tor import-keys: %v\n", binaryName, err)
			return 1
		}
		fmt.Fprintf(stdout, "Keys imported, Tor restarted.\n")
		if data.Hostname != "" {
			fmt.Fprintf(stdout, "  Address: %s\n", data.Hostname)
		}
		return 0

	default:
		fmt.Fprintf(stderr, "%s: tor: unknown subcommand %q\n", binaryName, sub)
		fmt.Fprintf(stderr, "Usage: %s tor {status|validate|restart|regenerate|vanity {start|stop|apply}|import-keys <path>}\n", binaryName)
		return 2
	}
}

// validateTorConfig checks the Tor configuration and directory state without
// starting a Tor process: binary presence, directory writability, and the
// virtual port range. Does not touch the network or any running instance.
func validateTorConfig(binaryName string, cfg tor.Config, stdout, stderr io.Writer) int {
	ok := true
	bin := tor.FindBinary(cfg.Binary)
	if bin == "" {
		fmt.Fprintf(stdout, "  [WARN] tor binary not found (hidden service will be disabled, not an error)\n")
	} else {
		fmt.Fprintf(stdout, "  [OK]   tor binary: %s\n", bin)
	}
	if cfg.VirtualPort <= 0 || cfg.VirtualPort > 65535 {
		fmt.Fprintf(stdout, "  [FAIL] virtual_port %d out of range 1-65535\n", cfg.VirtualPort)
		ok = false
	} else {
		fmt.Fprintf(stdout, "  [OK]   virtual_port: %d\n", cfg.VirtualPort)
	}
	for _, dir := range []string{filepath.Join(cfg.ConfigDir, "tor"), filepath.Join(cfg.DataDir, "tor")} {
		if err := path.EnsureDir(dir); err != nil {
			fmt.Fprintf(stdout, "  [FAIL] %s: %v\n", dir, err)
			ok = false
			continue
		}
		fmt.Fprintf(stdout, "  [OK]   %s writable\n", dir)
	}
	if !ok {
		fmt.Fprintf(stderr, "%s: tor validate: configuration invalid\n", binaryName)
		return 1
	}
	fmt.Fprintf(stdout, "Tor configuration valid.\n")
	return 0
}

// runTorVanity dispatches "tor vanity {start|stop|apply}" to the running
// server's internal /server/tor/vanity/* control endpoints (AI.md PART 31.1).
// The search itself runs in-process inside the server — it brute-forces v3
// onion keypairs the same way mkp224o does, with no external tool dependency —
// so every subcommand requires a live server; there is no on-disk fallback.
// "start" takes a prefix (1-6 chars of the base32 onion charset) and an
// optional --workers count; "stop" cancels a running search but keeps the
// candidates already written to disk; "apply" swaps a found candidate into the
// live hidden-service identity after a destructive-action confirmation.
func runTorVanity(binaryName, action string, rest []string, baseURL string, serverUp bool, stdout, stderr io.Writer) int {
	if !serverUp {
		fmt.Fprintf(stderr, "Error: no running server detected — start the server first\n")
		return 1
	}
	switch action {
	case "start":
		prefix, workers, err := parseVanityStartArgs(rest)
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity start: %v\n", binaryName, err)
			fmt.Fprintf(stderr, "Usage: %s tor vanity start <prefix> [--workers N]\n", binaryName)
			return 2
		}
		if prefix == "" {
			fmt.Fprintf(stderr, "%s: tor vanity start requires a prefix argument\n", binaryName)
			fmt.Fprintf(stderr, "Usage: %s tor vanity start <prefix> [--workers N]\n", binaryName)
			return 2
		}
		body := map[string]interface{}{"prefix": prefix}
		if workers > 0 {
			body["workers"] = workers
		}
		reqBody, err := json.Marshal(body)
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity start: %v\n", binaryName, err)
			return 1
		}
		env, err := torControlRequest(http.MethodPost, baseURL, "/server/tor/vanity/start", reqBody, "application/json")
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity start: %v\n", binaryName, err)
			return 1
		}
		var data torControlVanityInfo
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity start: %v\n", binaryName, err)
			return 1
		}
		fmt.Fprintf(stdout, "Vanity search started for prefix %q using %d worker(s).\n", data.Prefix, data.Workers)
		fmt.Fprintf(stdout, "Run '%s tor status' to watch progress.\n", binaryName)
		fmt.Fprintf(stdout, "Run '%s tor vanity apply' once a match is found.\n", binaryName)
		return 0

	case "stop":
		env, err := torControlRequest(http.MethodPost, baseURL, "/server/tor/vanity/stop", nil, "")
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity stop: %v\n", binaryName, err)
			return 1
		}
		var data struct {
			Stopped bool                 `json:"stopped"`
			Vanity  torControlVanityInfo `json:"vanity"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity stop: %v\n", binaryName, err)
			return 1
		}
		if !data.Stopped {
			fmt.Fprintf(stdout, "No vanity search is running.\n")
		} else {
			fmt.Fprintf(stdout, "Vanity search stopped; found candidates are kept.\n")
		}
		printVanityInfo(stdout, data.Vanity)
		return 0

	case "apply":
		address := ""
		if len(rest) > 0 {
			address = strings.TrimSpace(rest[0])
		}
		if !confirmDestructive(stdout, "This permanently replaces the current .onion address. Continue?") {
			fmt.Fprintf(stdout, "Aborted.\n")
			return 1
		}
		reqBody, err := json.Marshal(map[string]string{"address": address})
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity apply: %v\n", binaryName, err)
			return 1
		}
		env, err := torControlRequest(http.MethodPost, baseURL, "/server/tor/vanity/apply", reqBody, "application/json")
		if err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity apply: %v\n", binaryName, err)
			return 1
		}
		var data struct {
			Hostname string `json:"hostname"`
		}
		if err := json.Unmarshal(env.Data, &data); err != nil {
			fmt.Fprintf(stderr, "%s: tor vanity apply: %v\n", binaryName, err)
			return 1
		}
		fmt.Fprintf(stdout, "Vanity address applied, Tor restarted.\n")
		if data.Hostname != "" {
			fmt.Fprintf(stdout, "  Address: %s\n", data.Hostname)
		}
		return 0

	default:
		fmt.Fprintf(stderr, "%s: tor vanity: unknown subcommand %q\n", binaryName, action)
		return 2
	}
}

// parseVanityStartArgs pulls the positional prefix and the optional
// --workers N flag out of "tor vanity start" arguments. Both "--workers N"
// and "--workers=N" are accepted, per the project-wide flag syntax rule.
func parseVanityStartArgs(rest []string) (string, int, error) {
	prefix := ""
	workers := 0
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		switch {
		case strings.HasPrefix(arg, "--workers="):
			n, err := strconv.Atoi(strings.TrimPrefix(arg, "--workers="))
			if err != nil {
				return "", 0, fmt.Errorf("invalid --workers value %q", strings.TrimPrefix(arg, "--workers="))
			}
			workers = n
		case arg == "--workers":
			if i+1 >= len(rest) {
				return "", 0, fmt.Errorf("--workers requires a value")
			}
			i++
			n, err := strconv.Atoi(rest[i])
			if err != nil {
				return "", 0, fmt.Errorf("invalid --workers value %q", rest[i])
			}
			workers = n
		case strings.HasPrefix(arg, "-"):
			return "", 0, fmt.Errorf("unknown flag %q", arg)
		default:
			if prefix != "" {
				return "", 0, fmt.Errorf("unexpected argument %q", arg)
			}
			prefix = strings.TrimSpace(arg)
		}
	}
	return prefix, workers, nil
}

// normalizeArgs expands short flags (-h → --help, -v → --version) to their
// long form. --flag=value forms need no pre-processing — stdlib flag.Parse
// accepts both "--flag value" and "--flag=value" natively. Single-dash
// multi-character flags are NOT converted — spec mandates short flags are
// ONLY -h and -v (PART 8).
func normalizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		switch a {
		case "-h":
			out = append(out, "--help")
		case "-v":
			out = append(out, "--version")
		default:
			out = append(out, a)
		}
	}
	return out
}

// applyColor applies the --color flag, updating NO_COLOR as needed.
// Spec canonical values: auto, yes, no (AI.md PART 8).
// always/never are accepted as backward-compatible aliases.
func applyColor(v string) {
	switch v {
	case "never", "no":
		os.Setenv("NO_COLOR", "1")
	case "always", "yes":
		os.Unsetenv("NO_COLOR")
	}
	// "auto" or empty: leave NO_COLOR as-is
}

// printHelp writes usage information to w.
func printHelp(w io.Writer, name string) {
	fmt.Fprintf(w, `%s %s - a fast, public pastebin service

Usage:
  %s [flags]

Information:
  -h, --help                        Show help (--help for any command shows its help)
  -v, --version                     Show version
      --status                      Show server status and health

Shell Integration:
      --shell completions [SHELL]   Print shell completions
      --shell init [SHELL]          Print shell init command
      --shell --help                Show shell help

Server Configuration:
      --mode {production|development}  Application mode (default: production)
      --config DIR                  Config directory
      --data DIR                    Data directory
      --cache DIR                   Cache directory
      --log DIR                     Log directory
      --backup DIR                  Backup directory
      --pid FILE                    PID file path
      --address ADDR                Listen address (default: 0.0.0.0)
      --port PORT                   Listen port (default: random 64xxx, 80 in container)
      --baseurl PATH                URL path prefix (default: /)
      --daemon                      Run as daemon (detach from terminal)
      --debug                       Enable debug mode
      --i2p                         Enable the optional I2P eepsite (opt-in)
      --color {auto|yes|no}         Color output (default: auto)
      --lang CODE                   Language for output (default: auto)

Service Management:
      --service CMD                 Service management (--service --help for details)
      --maintenance CMD             Maintenance operations (--maintenance --help for details)
      --update [CMD]                Check/perform updates (--update --help for details)

Run '%s <command> --help' for detailed help on any command.
`, name, Version, name, name)
}

// logFileOpts maps a config log-file block onto logging.FileOptions. The
// Enabled flag defaults true — log files are always-on unless the type has an
// explicit toggle (auth, debug, audit), which is handled at the call site.
func logFileOpts(c config.LogFileConfig) logging.FileOptions {
	return logging.FileOptions{
		Enabled:  true,
		Filename: c.Filename,
		Format:   c.Format,
		Custom:   c.Custom,
		Rotate:   c.Rotate,
		Keep:     c.Keep,
	}
}

// logSchedErr logs a scheduler registration error and continues. Registration
// errors are programming errors (bad cron expression) and should never occur
// at runtime — log them so they are visible without crashing the server.
func logSchedErr(err error) {
	if err != nil {
		log.Printf("warning: scheduler registration: %v", err)
	}
}

// schedulerNotifier returns a scheduler notifier that audits every task run and
// emails the operator a scheduler_error notification on failure (PART 18).
func schedulerNotifier(cfg *config.Config, auditW *audit.Writer) scheduler.NotifyFunc {
	return func(o scheduler.Outcome) {
		if auditW != nil {
			event := "scheduler.task_completed"
			severity := audit.SeverityInfo
			result := audit.ResultSuccess
			if o.Status != "success" {
				event = "scheduler.task_failed"
				severity = audit.SeverityError
				result = audit.ResultFailure
			}
			auditW.Log(audit.Entry{
				Event:    event,
				Severity: severity,
				Result:   result,
				Target:   &audit.Target{Type: "scheduler_task", ID: o.TaskID},
				Details: map[string]any{
					"task_name":   o.TaskName,
					"duration_ms": o.FinishedAt.Sub(o.StartedAt).Milliseconds(),
					"attempt":     o.Attempt,
					"will_retry":  o.WillRetry,
				},
				Reason: o.Err,
			})
		}

		if o.Status == "success" {
			return
		}

		// PART 17: scheduler_error is suppressed when a more specific failure
		// event already fires an email for this same execution — backup_daily
		// and backup_hourly send backup_failed (task.backupSendFailed),
		// ssl_renewal sends ssl_renewal_failed (task.SSLRenewalWithEmail).
		switch o.TaskID {
		case "backup_daily", "backup_hourly", "ssl_renewal":
			return
		}

		if !cfg.Server.Notifications.Email.Events.SchedulerError {
			return
		}
		to := strings.TrimSpace(cfg.AdminEmail())
		if to == "" {
			return
		}
		m := email.New(&cfg.Server.Notifications.Email, cfg.Web.SiteTitle, startupBaseURL(cfg), cfg.Server.FQDN)
		if !m.Enabled() {
			return
		}
		if err := m.Send(to, "scheduler_error", map[string]string{
			"task_name": o.TaskName,
			"error":     o.Err,
			"next_run":  o.NextRun.Format("2006-01-02 15:04:05 MST"),
		}); err != nil {
			log.Printf("scheduler: failed to send scheduler_error email: %v", err)
		}
	}
}

// startupScheme returns "https" when the server is configured to serve TLS,
// and "http" otherwise. Without a live request we derive the protocol from
// config: port 443, server.tls.enabled, or Let's Encrypt enabled all imply
// HTTPS. Called at startup and for background-task email links (PART 12/15).
func startupScheme(cfg *config.Config) string {
	if cfg.Server.TLS.Enabled || cfg.Server.TLS.LetsEncrypt.Enabled {
		return "https"
	}
	// Dual-port "80,443" or plain "443" → HTTPS is the primary scheme.
	for _, p := range strings.Split(cfg.Server.Port, ",") {
		if strings.TrimSpace(p) == "443" {
			return "https"
		}
	}
	return "http"
}

// startupBaseURL builds the canonical base URL from config when no HTTP
// request is yet available (startup, background tasks, email links). Mirrors
// the runtime baseURL(r) logic for the static config path (PART 12).
func startupBaseURL(cfg *config.Config) string {
	if u := strings.TrimRight(cfg.Server.BaseURL, "/"); u != "" {
		return u
	}
	proto := startupScheme(cfg)
	fqdn := cfg.ResolveFQDN()
	if fqdn == "" || strings.EqualFold(fqdn, "localhost") {
		fqdn = "localhost"
	}
	port := cfg.Server.Port
	// Dual-port "80,443" — omit port from the public URL.
	if strings.Contains(port, ",") {
		port = ""
	}
	// Strip standard :80/:443.
	if (proto == "http" && port == "80") || (proto == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		return proto + "://" + fqdn + ":" + port
	}
	return proto + "://" + fqdn
}

// blocklistSources maps the configured blocklist sources to task.Source values.
// Returns nil when blocklists are disabled so the task only ensures the
// directory exists (graceful degradation, PART 19).
func blocklistSources(cfg *config.Config) []task.Source {
	bl := cfg.Web.Security.Blocklists
	if !bl.Enabled {
		return nil
	}
	sources := make([]task.Source, 0, len(bl.Sources))
	for _, s := range bl.Sources {
		if s.File == "" || s.URL == "" {
			continue
		}
		sources = append(sources, task.Source{Name: s.File, URL: s.URL})
	}
	return sources
}

// cveSources maps the configured CVE source to task.Source values. Returns nil
// when CVE updates are disabled or unconfigured.
func cveSources(cfg *config.Config) []task.Source {
	c := cfg.Web.Security.CVE
	if !c.Enabled || c.File == "" || c.Source == "" {
		return nil
	}
	return []task.Source{{Name: c.File, URL: c.Source}}
}
