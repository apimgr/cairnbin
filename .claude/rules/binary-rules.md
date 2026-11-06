# Binary Rules (PART 7, 8, 32)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER build with CGO enabled — `CGO_ENABLED=0`, pure Go only
- NEVER embed security databases (GeoIP, blocklists, CVE, Trivy) in the binary — downloaded on first run, kept updated by scheduler
- NEVER hardcode host, IP, or port anywhere in project code
- NEVER display `0.0.0.0`, `127.0.0.1`, `localhost` in output — always show a valid FQDN/host/IP
- NEVER show bare paths like `GET /api/` without the full URL (`{proto}://{fqdn}:{port}/path`)
- NEVER include `:80` (HTTP) or `:443` (HTTPS) in displayed/built URLs — always stripped
- NEVER hand-roll argument parsing for the server binary's primary flag set (no manual `switch`/`os.Args` loops) — use stdlib `flag`
- NEVER use `cobra`/`viper` for the server binary — those are for the client CLI only
- NEVER resolve `~`/`$HOME` again after the privilege drop — service account HOME points at `{data_dir}`
- NEVER create a PID file inside a container (`isContainer()` true) — container runtime supervises the process
- NEVER fall back to a `$HOME`-derived backup dir in system mode — fallback is `{data_dir}/backup/`, never `$HOME`
- NEVER implement CLI UI-mode flags (`--tui`, `--cli`, `--gui`, `--mode tui/cli/gui`, `tui` subcommand) — display mode is always auto-detected; `--mode` is app-mode (production/development) ONLY
- NEVER use Electron or web views for CLI GUI mode — native toolkits only (GTK4/Qt6 Linux, Cocoa macOS, Win32/WinUI Windows)
- NEVER make the CLI's GUI a wrapper around the TUI, or feature-incomplete vs the TUI
- NEVER give CLI help/subcommand-help output a `sudo` call, privilege check, or privilege requirement
- NEVER use OS system directories for CLI runtime state (`/etc/...`, `/var/lib/...`, `/var/log/...`, `C:\ProgramData\`) — CLI is always user-scope, even when invoked as root
- NEVER use `strconv.ParseBool()` for boolean flags/config — use `config.ParseBool()`/`config.IsTruthy()`
- NEVER construct URLs with raw/unencoded user input — always `url.PathEscape`/`url.QueryEscape` via `urlutil` helpers
- NEVER store the raw `server.token` in the DB or any plaintext owner/linked token — `api_tokens` holds `SHA-256` hash (lookup) plus an AES-256-GCM encrypted copy (`token_enc`) for the owner reveal action; `server.token` isn't in `api_tokens` at all
- NEVER let API routes (`/api/...`) accept cookie auth — Authorization header only, no ambient authority
- NEVER skip SHA-256 checksum verification on CLI/server self-update — abort and delete temp on mismatch
- NEVER daemonize automatically under `--service start` when the service manager is systemd/launchd/runit/s6/container (always foreground); SysV/rc.d always daemonize
- NEVER add short flags beyond `-h`/`-v` — everything else is long-form only

## CRITICAL - ALWAYS DO

- ALWAYS build as a SINGLE STATIC BINARY with embedded assets (Go `embed`), zero runtime dependencies
- ALWAYS detect display environment (GUI/TUI/CLI/Headless) in every binary and adapt output
- ALWAYS force CLI mode (no colors, no emoji, no ANSI, ASCII tables) when `TERM=dumb`
- ALWAYS respect `NO_COLOR` (any non-empty value disables colors AND emojis); priority: CLI flag > config > `NO_COLOR` > auto-detect
- ALWAYS show the ACTUAL (possibly renamed) binary name in `--help`, `--version`, and error messages
- ALWAYS hardcode `{project_name}` internally for User-Agent, default paths, config keys, DB tables, API identifiers — regardless of binary rename
- ALWAYS create directories the flags point to if missing, and validate they're writable
- ALWAYS detect stale PID files (dead process or PID reuse) and remove them before starting
- ALWAYS bind privileged ports (<1024) while still root, then drop privileges immediately after
- ALWAYS resolve directory mode (system vs user) ONCE from EUID at startup and cache for process lifetime
- ALWAYS handle SIGTERM/SIGINT/SIGQUIT/SIGRTMIN+3(37) as graceful shutdown; SIGHUP ignored (config auto-reloads); SIGUSR1 reopens logs; SIGUSR2 dumps status (Unix)
- ALWAYS remove the PID file in the shutdown signal handler
- ALWAYS use `subtle.ConstantTimeCompare` for token/hash comparisons
- ALWAYS use `{proto}://{fqdn}:{port}/path` format for every URL, resolved per-request (never frozen at startup)
- ALWAYS prefer reverse-proxy headers over other sources when resolving `{fqdn}`/`{proto}`/`{port}`
- ALWAYS generate/validate a Request ID (UUID v4) per request, honoring `X-Request-ID`/`X-Correlation-ID`/`X-Trace-ID`, returned in response headers
- ALWAYS keep CLI config/data/cache/log dirs under the invoking user's profile, even when CLI is installed system-wide or run as root
- ALWAYS set CLI dirs `0700` and CLI files `0600` on Unix
- ALWAYS auto-create `cli.yml` on first run with sane defaults
- ALWAYS support `--config NAME` profile switching with `.yml`/`.yaml` auto-detection
- ALWAYS save `--server`/`--token` flag values to config only when the current config value is empty or invalid — never overwrite a valid value
- ALWAYS accept both `--flag=value` and `--flag value` syntax
- ALWAYS provide a config-file equivalent for every CLI flag
- ALWAYS support `--shell completions [SHELL]` and `--shell init [SHELL]` (auto-detect `$SHELL` if omitted) in ALL binaries
- ALWAYS use the compiled/hardcoded project name in the `User-Agent` header regardless of binary rename
- ALWAYS launch remote sessions (SSH/Mosh) into TUI setup, never GUI, even if X11 forwarding is available
- ALWAYS require `--server` for CLI in containers (no interactive setup there)

## Key Rules Summary

### Binary naming & identity

| Binary | Default name | User-Agent |
|--------|--------------|------------|
| Server | `{project_name}` | `{project_name}/{version}` |
| Client | `{project_name}-cli` | `{project_name}-cli/{version}` |

Get display name: `filepath.Base(os.Args[0])`. User-Agent/internal identifiers always use hardcoded `{project_name}`.

### Server CLI flags (complete set — cannot be changed)

```
--help, --version, --shell {completions,init,help} [SHELL]
--mode {production|development}
--config DIR   --data DIR   --cache DIR   --log DIR   --backup DIR   --pid FILE
--address ADDR   --port PORT   --baseurl PATH
--status                    (exit 0=healthy, 1=unhealthy)
--service {start,restart,stop,reload,--install,--uninstall,--disable,--help}
--daemon   --debug   --color {auto|yes|no}   --lang CODE
--maintenance {backup,restore,update,mode,setup,pgp,secret,token,data,compliance,--help} [arg]
--update [check|yes|branch {stable|beta|devel}|--help]
```

Commands anyone can run without privileges: `--help`, `--version`, `--status`, `--update check`.

### Server default directories

| Flag | Root default | User default |
|------|--------------|---------------|
| `--config` | `/etc/{internal_org}/{internal_name}/` | `~/.config/{internal_org}/{internal_name}/` |
| `--data` | `/var/lib/{internal_org}/{internal_name}/` | `~/.local/share/{internal_org}/{internal_name}/` |
| `--cache` | `/var/cache/{internal_org}/{internal_name}/` | `~/.cache/{internal_org}/{internal_name}/` |
| `--log` | `/var/log/{internal_org}/{internal_name}/` | `~/.local/log/{internal_org}/{internal_name}/` |
| `--backup` | `/mnt/Backups/{internal_org}/{internal_name}/` (else `{data_dir}/backup/`) | `~/.local/share/Backups/{internal_org}/{internal_name}/` |
| `--pid` | `/var/run/{internal_org}/{internal_name}.pid` | `{data_dir}/{internal_name}.pid` |

Env var fallbacks: `CONFIG_DIR`, `DATA_DIR`, `LOG_DIR`, `PID_FILE`, `PORT`, `LISTEN`, `MODE`, `DATABASE_DIR`, `BACKUP_DIR`. Priority: CLI flag > env var > config file > default.

Permissions: root dirs `0755`/files `0644`; user dirs `0700`/files `0600`.

### Signals (Unix)

| Signal | Action |
|--------|--------|
| SIGTERM, SIGINT, SIGQUIT, SIGRTMIN+3 (37, Docker STOPSIGNAL) | Graceful shutdown |
| SIGHUP | Ignored (auto config reload via watcher) |
| SIGUSR1 | Reopen logs |
| SIGUSR2 | Dump status |

Windows only supports `os.Interrupt`. Shutdown timeouts: in-flight requests 30s, child processes 10s, DB flush 5s, log flush 2s.

### Container detection

Files: `/.dockerenv` (Docker), `/run/.containerenv` (Podman), `/dev/lxc` (LXC/LXD/Incus). Env: `container`, `KUBERNETES_SERVICE_HOST`. Parent process: `tini`, `dumb-init`, `s6-svscan`, `runsv`, `runsvdir`, `catatonit`, or self (`{project_name}`).

### Database

Single SQLite file `server.db` (default) with tables: `config`, `config_meta`, `rate_limits`, `audit_log`, `scheduler_tasks`, `scheduler_history`, `backups`, `api_tokens`. Remote option: libsql/Turso. `server.token` is NEVER stored in `api_tokens` — validated via SHA-256 + constant-time compare against `server.yml` value, cached in memory.

### Token model

- Format: `tok_` + 32 URL-safe base62 chars. Header: `Authorization: Bearer tok_...`
- `server.token` (in `server.yml`) = global operator token, auto-generated on first run
- Owner and linked tokens: `SHA-256` hash + AES-256-GCM `token_enc` in `api_tokens` (resolve to an `owner_id`); viewable masked on `/server/preferences`, reveal/rotate/restore/linked-token management require the primary token
- Revoke: `{project_name} --maintenance token revoke <prefix>`; list: `{project_name} --maintenance token list`
- Cookie name (web fallback only, never for `/api/...`): `{project_name}_owner_token_XXXXXX`

### URL variable resolution priority

`{fqdn}`: tor onion match (0) > reverse-proxy headers (1) > `DOMAIN` env (2) > `os.Hostname()` (3) > `$HOSTNAME` (4) > public IPv6 (5) > public IPv4 (6) > localhost (7).
`{proto}`: `X-Forwarded-Proto` > `X-Forwarded-Ssl` > `X-Url-Scheme` > TLS-on-connection > `http` default.
`{port}`: `X-Forwarded-Port` > Host header port > server listen port > proto default.

Client IP priority: `CF-Connecting-IP` > `True-Client-IP` > `X-Real-IP` > `X-Forwarded-For` (leftmost) > `X-Client-IP` > `r.RemoteAddr`. Headers 1–5 honored only when peer is a trusted proxy.

### CLI (`{project_name}-cli`) directories

| Dir | Unix | Windows |
|-----|------|---------|
| Config | `~/.config/{internal_org}/{internal_name}/cli.yml` | `%APPDATA%\{internal_org}\{internal_name}\cli.yml` |
| Data | `~/.local/share/{internal_org}/{internal_name}/` | `%LOCALAPPDATA%\...\data\` |
| Cache | `~/.cache/{internal_org}/{internal_name}/` | `%LOCALAPPDATA%\...\cache\` |
| Logs | `~/.local/log/{internal_org}/{internal_name}/cli.log` | `%LOCALAPPDATA%\...\log\cli.log` |

### CLI flags

```
-h/--help   -v/--version
--shell {completions,init,help} [SHELL]
--server URL   --token TOKEN   --token-file FILE
--config NAME   --debug   --color {auto|yes|no}   --lang CODE
```

Server address priority: `--server` flag > `server.primary` in `cli.yml` > `{official_site}` compiled default > error.
Token priority: `--token` flag > `{PROJECT_NAME}_TOKEN` env > `auth.token` in `cli.yml`.

`--config` resolution: `--config test` → `{config_dir}/test.yml`; absolute/`~` paths used as-is; `.yml` checked before `.yaml`; default is `cli.yml`.

### CLI mode detection (never a flag)

| Condition | Mode |
|-----------|------|
| `-h`/`--help`/`-v`/`--version` | CLI, exit |
| Interactive terminal, no command | TUI |
| Interactive terminal, config-only flags (`--config`,`--server`,`--token`,`--debug`) | TUI |
| Interactive terminal + command/args | CLI |
| Piped/redirected or non-interactive | Plain output |

Setup wizard priority: SSH/Mosh → TUI; local display → GUI; terminal only → TUI; neither → error. Container → requires `--server` flag, no wizard.

### Output formats: `json`, `table`, `plain` via `--output`/`output.format` in `cli.yml`.

### Exit codes (CLI)

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Configuration error |
| 3 | Connection error |
| 4 | Authentication error |
| 5 | Not found |
| 64 | Usage error (bad arguments) |

### Boolean parsing

Truthy: `true, yes, on, 1, enable, enabled`. Falsey: `false, no, off, 0, disable, disabled, none`. Use `config.ParseBool()`/`config.IsTruthy()` — never `strconv.ParseBool()`.

### GeoIP / security data (never embedded)

Downloaded to `{data_dir}/security/{geoip,blocklists,cve,trivy}/` from ip-location-db (GitHub Releases), NVD/NIST, Aqua Trivy. Update cadence: daily/monthly/twice-weekly per source tier.

For complete details, see AI.md PART 7, 8, 32
