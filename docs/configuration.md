# Configuration

The server reads `server.yml` from the config directory on startup. First run creates this file automatically. CLI flags always override config file values.

## Config File Location

| Platform | Path |
|----------|------|
| Linux    | `/etc/apimgr/pastebin/server.yml` |
| macOS    | `/Library/Application Support/apimgr/pastebin/server.yml` |
| Docker   | `/config/pastebin/server.yml` |

Override the directory with: `--config /path/to/config-dir` (the file inside is always `server.yml`)

## CLI Flags

```
--help / -h               Show help
--version / -v            Show version
--status                  Show server status and health
--mode {production|development}  Application mode (default: production)
--config DIR              Config directory
--data DIR                Data directory
--log DIR                 Log directory
--cache DIR               Cache directory
--backup DIR              Backup directory
--pid FILE                PID file path
--address ADDR            Listen address (default: 0.0.0.0)
--port PORT               Listen port (default: random 64xxx local, 80 container)
--baseurl PATH            URL path prefix (default: /)
--debug                   Enable debug mode
--daemon                  Run as daemon
--color {auto|yes|no}     Color output (default: auto)
--lang CODE               Language for output (default: auto)
--shell CMD [SHELL]       Shell integration (completions, init)
--service CMD             Service management
--maintenance CMD         Maintenance operations
--update [CMD]            Check/perform updates
```

## server.yml Reference

```yaml
server:
  address: "0.0.0.0"
  # first run picks a random unused 64000-64999 port and saves it here
  port: "64123"
  mode: "production"
  base_url: ""

database:
  type: "sqlite"
  path: ""   # default: {data_dir}/db/server.db

rate_limit:
  enabled: true
  create_per_minute: 30

termbin:
  enabled: false     # raw-TCP termbin/fiche compat listener (off by default)
  port: 9999
  max_size: 32768    # max payload in bytes
  timeout: "5s"      # idle/read timeout

web:
  csp:
    # frame-ancestors for the embeddable /emb/{id} endpoint only
    # default "*" (any site may iframe embeds); restrict with origins,
    # e.g. "'self' https://example.com"
    embed_frame_ancestors: "*"
  footer:
    # branding HTML above the footer; "" = default, " " = none; sanitized (no scripts or style)
    custom_html: ""
  reports:
    # max browser reports per minute per client IP, across all report types
    rate_limit_per_minute: 60
    # short-burst allowance per client IP (reports in one second)
    rate_limit_per_ip_burst: 10
```

`web.reports.*` tunes the browser-report intake at
`/api/v1/server/reports/{name}`. Non-positive or invalid values warn at load
and fall back to the defaults above.

`web.footer.custom_html` may use `{current_year}`, `{project_name}`, `{project_org}`, `{project_version}`, `{build_datetime}`, `{onion_address}` and `{i2p_address}`; the last two are empty unless Tor or I2P is running with a published address.

`server.pages.{about,help,privacy,terms}.content` takes Markdown. For `about` and `help` it is appended to the page; for `privacy` and `terms` a non-empty value replaces the default body. `server.pages.contact` has `enabled`, `captcha` and `success_message`.

### Branding and SEO (`server.branding`, `server.seo`)

```yaml
server:
  branding:
    # display name; empty falls back to the project name
    title: "pastebin"
    tagline: ""
    description: ""
  seo:
    keywords: []
    author: ""
    og_image: ""
    twitter_handle: ""
    # only valid, non-empty codes are rendered; invalid ones are logged at startup
    verification:
      google: ""
      bing: ""
      custom: []
    sitemap:
      # false makes /sitemap.xml return 404
      enabled: true
      # 1-50000; out-of-range values warn and use 50000
      max_urls: 50000
      # add the OpenGraph image to the homepage entry
      include_images: false
```

### CSRF (`server.csrf`)

```yaml
server:
  csrf:
    # double-submit cookie protection for browser forms; Bearer requests are exempt
    enabled: true
    token_length: 32
    cookie_name: csrf_token
    header_name: X-CSRF-Token
    # auto sets Secure when the request is https; true | false force it
    secure: auto
    # glob patterns that skip CSRF validation
    exempt_paths: []
```

### Tor (`server.tor`)

Tor hidden-service support is auto-enabled when a Tor binary is found —
there is no on/off toggle. Two keys matter for privacy:

| Key | Behavior |
|-----|----------|
| `tor.onion_address` | v3 `.onion` hostname (no `http://` prefix). Set automatically on the first successful Tor bootstrap and persisted to `server.yml`; enables Tor request detection (onion base URLs, Tor `security.txt`, Tor CORS). Never overwritten once set. |
| `tor.contact_email` | Contact address shown on Tor responses. When unset, no email is shown on Tor — the clearnet contact/security/abuse emails are never disclosed to Tor visitors. |

### Logging (`server.logs`)

Each log file accepts `filename`, `format`, `custom` (only when
`format: custom`), `rotate`, and `keep`.

| Key | Default | Formats |
|-----|---------|---------|
| `logs.level` | `warn` | `debug`/`info`/`warn`/`error` (server.log, app.log gate) |
| `logs.access` | `access.log`, `apache`, rotate `monthly`, keep `none` | `apache`, `nginx`, `json`, `custom` |
| `logs.server` | `server.log`, `text`, rotate `weekly,50MB`, keep `none` | `text`, `json` |
| `logs.error` | `error.log`, `text`, rotate `weekly,50MB`, keep `none` | `text`, `json` |
| `logs.app` | `app.log`, `logfmt`, rotate `weekly,50MB`, keep `none` | `logfmt`, `json` |
| `logs.auth` | `auth.log`, `syslog`, rotate `weekly,50MB`, keep `none` | `syslog` (RFC 3164), `json` |
| `logs.audit` | `audit.log`, `json`, rotate `daily`, keep `none`, `compress: true` | `json` only |
| `logs.security` | `security.log`, `fail2ban`, rotate `weekly,50MB`, keep `none` | `fail2ban`, `syslog`, `cef`, `json`, `text` |
| `logs.debug` | `enabled: true`, `debug.log`, `text`, rotate `weekly,50MB`, keep `none` | `text`, `json`; only written when debug mode is active |

**`rotate` values:** `never`, `daily`, `weekly`, `monthly`, `yearly`,
`{N}MB`, `{N}GB`, or a time+size combo like `weekly,50MB` (whichever
hits first). Rotation renames the file to `{name}.YYYY-MM-DD` and
reopens a fresh file.

**`keep` values:** `none` (delete rotated files immediately), `{N}`
(keep newest N rotated files), `{N}d`/`{N}w`/`{N}m` (age-based), or
`forever`.

**`format: custom`** (access log) substitutes these variables in
`custom`: `{time}` `{date}` `{datetime}` `{remote_ip}` `{method}`
`{path}` `{query}` `{status}` `{bytes}` `{latency}` `{latency_ms}`
`{user_agent}` `{referer}` `{request_id}` `{fqdn}` `{protocol}`
`{tls_version}` `{country}` `{asn}`.

All log lines are strict raw text: ANSI escape sequences, control
characters, and emoji are stripped before writing. Invalid `rotate`,
`keep`, or `format` values warn and fall back to defaults — startup
never fails. Rotation is checked on every write (size) and by the
`log_rotation` scheduler task (time + retention pruning).

### Updates (`server.update`)

| Key | Default | Meaning |
|-----|---------|---------|
| `branch` | `stable` | Release channel: `stable`, `beta`, or `devel` (aliases `daily`, `rolling`, `dev`, `development`) |
| `auto_install` | `false` | Let the `update_check` task install eligible releases; off means notify only |
| `defer_days` | `0` | Scheduled task only: a release must be this many days old (0-365); ignored on `devel` |
| `cli_min_version` | empty | Minimum CLI version, returned as `cli_min_version` by `/api/autodiscover` |

Channels are cumulative: `beta` also sees stable releases, and `devel` also
sees beta and stable. `--update branch {name}` writes `server.update.branch`.
Updates are verified against the release's `sha256.txt` before install.

### CLI distribution (`server.cli`)

The server hands out `pastebin-cli` builds for CLI self-update. `/api/autodiscover`
lists each available build under `cli_versions` (`{os}-{arch}` → `version` and
`sha256`), and `GET /cli/binaries/pastebin-cli-{os}-{arch}[.exe]` streams it.
Builds come from `{data_dir}/cli/binaries/` plus the `pastebin-cli` installed
next to the server binary (the container image ships it).

| Key | Default | Meaning |
|-----|---------|---------|
| `binary_download.require_auth` | `false` | Require the server token (bearer) for `/cli/binaries/*`; public by default so new users can install the CLI first |

### Backups (`server.backup`)

Retention keys under `backup.retention`:

| Key | Default | Meaning |
|-----|---------|---------|
| `max_backups` | `1` | Daily full backups kept (at least 1) |
| `keep_weekly` | `0` | Sunday backups kept |
| `keep_monthly` | `0` | First-of-month backups kept |
| `keep_yearly` | `0` | Jan 1st backups kept |
| `max_total_size` | `10%` | Hard size cap on retained backups |

`max_total_size` accepts a percent of the backup volume (`10%`) or an
absolute size (`50G`, `500MB`); falsey values (`0`, `off`, `none`)
disable the cap. When over the cap, oldest dated backups are pruned
first — the newest backup is never deleted.

Before each daily backup the server checks free disk space: the backup
is skipped (with a `backup.skipped_disk_full` audit event) when free
space is less than 2x the last backup size or disk usage exceeds
`maintenance.cleanup.disk_threshold` (default 90%).

`--maintenance backup` archives `server.yml`, the databases, and the
`template/` and `theme/` directories; add `--include-ssl` for
`{config_dir}/ssl/` and `--include-data` for `{data_dir}/` (excluding
`db/` and the backup directory).

Restore applies hard limits from `server.restore`, aborting extraction
when any is crossed:

| Key | Default | Meaning |
|-----|---------|---------|
| `max_archive_size` | `1G` | Largest accepted archive |
| `max_expanded_size` | `4G` | Largest total expanded size |
| `max_files` | `10000` | Largest file count |
| `max_file_size` | `512M` | Largest single extracted file |
| `max_ratio` | `100` | Largest expanded-to-compressed ratio (`100:1`) |

The `--maintenance setup`, `restore`, and `mode` subcommands require
authorization: allowed on first run (empty database), as root (with
confirmation), or as the service user with the operator password.

## Environment Variables

Environment variables override the matching `server.yml` values at startup;
they are read individually (no `.env` file is ever loaded).

### Server

| Variable | Description |
|----------|-------------|
| `PORT`   | Override listen port (dual format `"8090,8443"` supported) |
| `PASTEBIN_PORT` | Alias for `PORT` |
| `ADDRESS` | Listen address (e.g. `0.0.0.0`) |
| `LISTEN` | Full listen spec (`address:port`) |
| `BASE_URL` | Public base URL / path prefix |
| `DOMAIN` | Primary FQDN used for links and certificates |
| `MODE`   | Application mode (`production` or `development`) |
| `DEBUG`  | Enable debug endpoints when truthy |
| `TZ`     | Timezone (default: `America/New_York`) |

### Identity and limits

| Variable | Description |
|----------|-------------|
| `SITE_TITLE` | Site title shown in the web UI |
| `APPLICATION_NAME` | Application name (About/Help pages) |
| `APPLICATION_TAGLINE` | Application tagline |
| `MAX_SIZE` | Maximum paste size; accepts `kb`/`mb`/`gb`/`tb` suffixes (e.g. `100kb`, `20mb`, `1gb`), a bare number defaulting to MB (`5` = `5mb`), or `0`/negative for unlimited (default `10mb`) |
| `MAX_SIZE_BYTES` | Deprecated alias for `MAX_SIZE` in raw bytes |
| `THEME` | Default theme (`dark`, `light`, or `auto`) |
| `NO_COLOR` | Disable all ANSI color output when set |

### Database and cache

| Variable | Description |
|----------|-------------|
| `DATABASE_DRIVER` | Database driver (`sqlite`, `libsql`) |
| `DATABASE_URL` | Remote database URL (libsql/Turso) |
| `DATABASE_DIR` | Directory holding the SQLite DB |
| `DB_PATH` | Full path to the SQLite DB file |
| `CACHE_URL` | Cache backend URL (memory/valkey/redis) |

### SMTP (email notifications)

| Variable | Description |
|----------|-------------|
| `SMTP_HOST` | SMTP server host (empty = autodetect) |
| `SMTP_PORT` | SMTP server port (default: `587`) |
| `SMTP_USERNAME` | SMTP auth username |
| `SMTP_PASSWORD` | SMTP auth password |
| `SMTP_TLS` | TLS mode (`auto`, `starttls`, `tls`, `none`) |
| `SMTP_FROM_NAME` | From display name (default: app name) |
| `SMTP_FROM_EMAIL` | From address (default: `no-reply@{fqdn}`) |

Verify the SMTP setup with `pastebin email test [recipient]`. It checks the SMTP handshake, sends the `test` template (subject prefixed `[TEST]`) to the admin contact or the given recipient, and records the result in the audit log. Without SMTP configured it exits 1 with "email features require SMTP configuration".

Custom email templates in `{config_dir}/template/email/{name}.txt` are validated when loaded. A file with an unknown `{variable}` (the log suggests the closest match), an empty subject or body, or a missing `Subject:` line or `---` separator is logged and ignored, and the built-in default is used instead. Subjects over 78 characters only log a warning.

### Directory overrides

| Variable | Description |
|----------|-------------|
| `CONFIG_DIR` | Configuration directory |
| `DATA_DIR` | Data directory |
| `CACHE_DIR` | Cache directory |
| `LOG_DIR` / `LOGS_DIR` | Log directory |
| `BACKUP_DIR` | Backup directory |
| `PID_FILE` | PID file path |

### termbin compatibility listener

| Variable | Description |
|----------|-------------|
| `TERMBIN_ENABLED` | Enable the raw-TCP termbin/fiche compat listener (default: `false`) |
| `TERMBIN_PORT`    | termbin listener port (default: `9999`) |
| `TERMBIN_MAX_SIZE`| Max termbin payload in bytes (default: `32768`) |
| `TERMBIN_TIMEOUT` | termbin idle/read timeout (default: `5s`) |

### I2P compatibility listener

| Variable | Description |
|----------|-------------|
| `I2P_ENABLED` | Enable the I2P sam-bridge listener (default: `false`) |
