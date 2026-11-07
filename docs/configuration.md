# Configuration

The server reads `server.yml` from the config directory on startup. First run creates this file automatically. CLI flags always override config file values.

## Config File Location

| Platform | Path |
|----------|------|
| Linux    | `/etc/apimgr/cairnbin/server.yml` |
| macOS    | `/Library/Application Support/apimgr/cairnbin/server.yml` |
| Docker   | `/config/cairnbin/server.yml` |

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
  cors:
    allowed_origins: ["*"] # list of origins; [""] disables CORS headers
    allow_credentials: true # effective only for explicitly allowed origins
    max_age: 86400
  rate_limit:
    enabled: true
    read: {requests: 120, window: 60}
    write: {requests: 10, window: 60}
    health: {requests: 120, window: 60}
    global_burst: 240
  termbin:
    enabled: true # raw-TCP termbin/fiche listener (on by default)
    port: 9999
    max_size: 32768
    timeout: "5s"

database:
  driver: "sqlite"
  url: "" # default: {data_dir}/db/server.db

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

## Complete Settings Index

The following groups are also accepted in `server.yml`. Fields are nested
under the path shown; runtime-generated values such as `server.tor.onion_address`
and `server.i2p.b32_address` are normally maintained by the server.

| Path | Settings | Purpose |
|------|----------|---------|
| `server` | `port`, `address`, `fqdn`, `mode`, `lang`, `api_version`, `baseurl`, `user`, `group`, `pidfile`, `daemonize`, `token`, `data_dir` | Listener, branding inputs, service identity, operator token, and data location. |
| `server.tokens` | `max_linked`, `cookie_max_age`, `idle_purge_days` | Owner-token limits and cleanup. |
| `server.contact.{admin,security,abuse,general}` | `email`, `webhooks` | Per-role notification destinations; empty roles use the documented fallback chain. |
| `server.pages.{about,privacy,contact,help,terms}` | `content`; `help.mcp`; `contact.enabled`, `contact.captcha`, `contact.success_message` | Operator-authored page content and contact form behavior. |
| `database` | `driver`, `url`, `token`, `pool.max_open`, `pool.max_idle`, `pool.max_lifetime`, `pool.max_idle_time` | SQLite or remote libSQL/Turso storage and connection pooling. SQLite is the local default. |
| `paste` | `max_size`, `default_expiry`, `default_language`, `max_burn_after`, `allow_unlisted` | Paste size, expiry, language, burn-after-read, and visibility defaults. |
| `server.cache` | `type`, `url`, `host`, `port`, `username`, `password`, `db`, `tls`, `tls_skip_verify`, `pool_size`, `min_idle`, `timeout`, `prefix`, `ttl` | In-memory or remote Redis/Valkey cache. `url` takes precedence over individual connection fields. |
| `server.metrics` | `enabled`, `include_system`, `include_runtime`, `token` (legacy Prometheus token), `auth.allow_unauthenticated`, `auth.tokens.{prometheus,grafana,loki}`, `root.enabled`, `loki.{max_entries,max_age}`, `duration_buckets`, `size_buckets` | Internal metrics services, their credentials, optional `/metrics` alias, and bounded Loki output. |
| `server.geoip` | `enabled`, `dir`, `deny_countries`, `allow_countries`, `presets`, `databases.{asn,country,city}` | Local GeoIP databases and country filtering. `allow_countries` takes precedence when both country lists are set. |
| `server.i2p` | `enabled`, `binary`, `sam_address`, `virtual_port`, `inbound_length`, `outbound_length`, `inbound_quantity`, `outbound_quantity`, `signature_type`, `bootstrap_timeout`, `b32_address` | Optional I2P eepsite; disabled by default. The address is generated and persisted after startup. |
| `server.tor` | `binary`, `use_network`, `max_circuits`, `circuit_timeout`, `bootstrap_timeout`, `safe_logging`, `max_streams_per_circuit`, `close_circuit_on_stream_limit`, `bandwidth_rate`, `bandwidth_burst`, `max_monthly_bandwidth`, `num_intro_points`, `virtual_port`, `onion_address`, `contact_email` | Tor hidden-service settings; the service is auto-enabled when a Tor binary is found. The onion address is generated and persisted after successful bootstrap. When `use_network: true`, app-owned downloads and configured keyserver requests use Tor and fail closed until Tor is ready. Caller-supplied webhook/report destinations stay direct; branding URLs keep their SSRF-safe fetch path. |
| `server.tls` | `enabled`, `cert`, `key`, `min_version`, `letsencrypt.{enabled,email,challenge,staging}`, `dns_provider`, `dns_credentials_encrypted` | HTTPS certificates, ACME, and encrypted DNS-provider credentials. |
| `server.notifications.email` | `enabled`, `smtp.{host,port,username,password,tls}`, `from.{name,email}`, `reply_to`, `template_dir`, `events.*` | SMTP transport, sender identity, custom template directory, and per-event email switches. TLS accepts `auto`, `starttls`, `tls`, or `none`. |
| `server.privacy` | `data.{sold,stored_on_server,sharing}`, `retention.{period,export_available,deletion_available}`, `consent.*`, `cookies.{essential,preferences,analytics}`, `third_party.services`, `content.*` | Data-disclosure, consent banner, cookie categories, third-party list, and privacy-page text. `data.sold` controls the CCPA opt-out control. |
| `server.tracking` | `type`, `id`, `url` | Optional analytics provider; empty or `none` disables tracking. |
| `server.security` | `encryption_key`, `allowlist`, `blocked_ips`, `abuse_detection.{request_flood,auto_block_ip,auto_alert}`, `blocklists.{enabled,sources}`, `cve.*`, `trivy.*` | At-rest encryption key, IP controls, abuse response, and downloaded security data. |
| `server.rate_limit` | `enabled`, `read.{requests,window}`, `write.{requests,window}`, `health.{requests,window}`, `global_burst` | Per-IP request limits by endpoint class. |
| `server.limits` | `max_body_size`, `read_timeout`, `write_timeout`, `idle_timeout` | HTTP request and connection limits. |
| `server.trusted_proxies` | `additional` | Additional trusted proxy IPs/CIDRs beyond the built-in private ranges. |
| `server.url_detection` | `learning`, `min_samples`, `sample_window`, `log_changes`, `live_reload` | Hostname learning behavior. |
| `server.termbin` | `enabled`, `port`, `max_size`, `timeout` | Raw-TCP termbin/fiche listener; enabled by default on port 9999. |
| `server.maintenance` | `self_healing.{enabled,retry_interval,max_attempts}`, `cleanup.{disk_threshold,log_retention_days,backup_keep_count}`, `notify.{on_enter,on_exit}` | Automatic recovery and cleanup after critical storage or disk problems. |
| `server.restore` | `max_archive_size`, `max_expanded_size`, `max_files`, `max_file_size`, `max_ratio` | Safety limits for backup restore extraction. |
| `server.compliance` | `enabled`, `gdpr`, `ccpa`, `hipaa`, `soc2`, `pci_dss`, `iso27001`, `fedramp`, `lgpd`, `pipeda`, `appi`, `pdpa` | Compliance behavior switches; each standard is off unless enabled. |
| `server.scheduler` | `timezone`, `catch_up_window`, `tasks.<task_id>.{enabled,schedule,retry_on_fail,retry_delay,max_retries,max_age,max_size,retention,renew_before}` | Built-in scheduler timezone, missed-run window, and task-specific options. |
| `web` | `site_title`, `theme`, `robots.{allow,deny,ai_bots}`, `security.*`, `hsts.*`, `csp.*`, `reports.*`, `headers.*`, `llms.*`, `permissions_policy`, `footer.custom_html`, `announcements.*` | Browser theme, crawler policy, transport/security headers, discovery content, footer branding, and site banners. |
| `server.cors` | `allowed_origins`, `allow_credentials`, `max_age` | Cross-origin access. Wildcard origins do not receive credentialed CORS. |
| `server.healthz.root.enabled` | boolean | Opt-in root `/healthz` alias; `/server/healthz` remains canonical. |
| `server.mcp.enabled` | boolean | Enables the MCP endpoint and its aliases; enabled by default. |
| `server.compression` | `enabled`, `level`, `types` | Response compression and MIME types. |
| `server.debug` | `pprof`, `log_queries`, `log_cache`, `log_bodies`, `max_body_log_size`, `block_profile_rate`, `mutex_profile_fraction`, `runtime_endpoints` | Debug-only profiling and diagnostics. These settings require debug mode. |

The generated first-run `server.yml` contains defaults. For fields with special
validation or privacy behavior, see the relevant section above and the
[Security](security.md), [Integrations](integrations.md), and [API](api.md)
references.

`server.cors.allowed_origins` defaults to `*`; configured origins are checked against the list. An explicit origin may receive `Access-Control-Allow-Credentials: true`; wildcard origins never do. Preflight responses list the exact supported content, authorization, CSRF, and fetch headers.

`web.footer.custom_html` may use `{current_year}`, `{project_name}`, `{project_org}`, `{project_version}`, `{build_datetime}`, `{onion_address}` and `{i2p_address}`; the last two are empty unless Tor or I2P is running with a published address.

`server.pages.{about,help,privacy,terms}.content` takes Markdown. For `about` and `help` it is appended to the page; for `privacy` and `terms` a non-empty value replaces the default body. `server.pages.help.mcp` adds project-specific Markdown after the base sections on `/server/help/mcp` and in its API mirror; the route is available only when `server.mcp.enabled` is true. `server.pages.contact` has `enabled`, `captcha` and `success_message`.

### Branding and SEO (`server.branding`, `server.seo`)

```yaml
server:
  branding:
    # display name; empty falls back to the project name
    title: "cairnbin"
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

The server hands out `cairnbin-cli` builds for CLI self-update. `/api/autodiscover`
lists each available build under `cli_versions` (`{os}-{arch}` → `version` and
`sha256`), and `GET /cli/binaries/cairnbin-cli-{os}-{arch}[.exe]` streams it.
Builds come from `{data_dir}/cli/binaries/` plus the `cairnbin-cli` installed
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

## Languages and Accessibility

The server, web UI, API messages, and CLI are translated into English (`en`),
Spanish (`es`), Chinese (`zh`), French (`fr`), Arabic (`ar`, right-to-left),
German (`de`), and Japanese (`ja`). Unsupported values silently fall back to
English.

Web language order: `?lang=` (sets a one-year `lang` cookie) -> `lang`
cookie -> `Accept-Language` -> `en`. CLI and server order: `--lang` flag ->
config file -> `LANG` / `LC_ALL` -> `en`.

Settings live under `server.i18n`:

| Key | Meaning |
|-----|---------|
| `enabled` | Turn language negotiation on or off |
| `default_language` | Language used when nothing else matches |
| `available_languages` | Subset of the seven languages offered in the selector |
| `fallback_language` | Language used when a key is missing |
| `cookie_name` | Name of the language cookie (default `lang`) |
| `cookie_max_age` | Lifetime of the language cookie (default one year) |

The web UI targets WCAG 2.1 AA: skip links, keyboard navigation, visible focus
indicators, ARIA live regions for notifications, and 4.5:1 text contrast in
both the dark and light themes.

## Environment Variables

Environment variables override the matching `server.yml` values at startup;
they are read individually (no `.env` file is ever loaded).

### Server

| Variable | Description |
|----------|-------------|
| `PORT`   | Override listen port (dual format `"8090,8443"` supported) |
| `CAIRNBIN_PORT` | Alias for `PORT` |
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

Verify the SMTP setup with `cairnbin email test [recipient]`. It checks the SMTP handshake, sends the `test` template (subject prefixed `[TEST]`) to the admin contact or the given recipient, and records the result in the audit log. Without SMTP configured it exits 1 with "email features require SMTP configuration".

Custom email templates in `{config_dir}/template/email/{name}.txt` are validated when loaded. A file with an unknown `{variable}` (the log suggests the closest match), an empty subject or body, or a missing `Subject:` line or `---` separator is logged and ignored, and the built-in default is used instead. Subjects over 78 characters only log a warning.

Built-in email subjects and bodies use the server's configured `server.lang`;
when it is empty or `auto`, the server locale fallback applies (`LC_ALL`, then
`LANG`, then English). Custom templates remain operator-authored text.

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
| `TERMBIN_ENABLED` | Enable the raw-TCP termbin/fiche compat listener (default: `true`) |
| `TERMBIN_PORT`    | termbin listener port (default: `9999`) |
| `TERMBIN_MAX_SIZE`| Max termbin payload in bytes (default: `32768`) |
| `TERMBIN_TIMEOUT` | termbin idle/read timeout (default: `5s`) |

### I2P compatibility listener

| Variable | Description |
|----------|-------------|
| `I2P_ENABLED` | Enable the I2P sam-bridge listener (default: `false`) |
