# Features Rules (PART 17, 18, 19, 20, 21, 22)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER attempt to send an email without a valid, working SMTP server ("No SMTP = No emails. Don't even try.")
- NEVER queue emails hoping SMTP will be configured later
- NEVER log "would have sent email" messages
- NEVER show email-dependent UI/options when SMTP is not configured
- NEVER render operator events (backups, SSL, updates, scheduler failures, abuse detection, GeoIP, version) in the public WebUI — no notification center, no bell icon, no notification history
- NEVER use external schedulers (cron, crond, crontab, systemd timers, at, anacron, Windows Task Scheduler, launchd, Kubernetes CronJob, AWS CloudWatch Events, Azure Scheduler, GCP Cloud Scheduler) for ANY scheduled task — the built-in scheduler is the only allowed mechanism, with no exceptions
- NEVER treat GeoIP/country as the sole access-control gate — it is a risk signal only, never a substitute for authentication
- NEVER let a blocked-country request skip rate limiting, authentication, authorization, or audit logging
- NEVER block a request solely because GeoIP lookup failed or the database is missing/stale (fail-open for GeoIP; fail-closed only for real auth)
- NEVER country-block private/internal IPs (RFC 1918) or IPs on the security allowlist
- NEVER use `geoip2-golang` for MMDB lookups — ip-location-db files require `github.com/oschwald/maxminddb-golang`
- NEVER expose `/metrics` publicly — it is INTERNAL ONLY (firewall, proxy-block, NetworkPolicy, or security-group restrict)
- NEVER use a raw client IP (or any high-cardinality value: user_id, request_id, timestamp) as a Prometheus metric label — unbounded cardinality is a memory-DoS vector
- NEVER store the backup encryption password anywhere — operator must remember it; no recovery if lost
- NEVER accept a backup password as a CLI flag — interactive prompt only (flags leak via shell history/process list)
- NEVER delete existing backups unless the new backup passes ALL verification checks
- NEVER proceed with a restore unless ALL verification checks pass
- NEVER allow backups to run under compliance mode without an encryption password set (backups are BLOCKED until password is set)
- NEVER inject update/version notices into API responses, headers, or any public endpoint (Tier 3 information, operator-only)
- NEVER skip SHA256 checksum verification of a downloaded update binary — mandatory before install
- NEVER let `defer_days` affect an explicit manual `--update check` / `--update yes` — only the scheduled `update_check` task is gated by it

## CRITICAL - ALWAYS DO

- ALWAYS provide fully customizable email templates with sane, working defaults out-of-the-box (no config required)
- ALWAYS fall back to the embedded default template if no custom template file exists in `{config_dir}/template/email/`
- ALWAYS auto-detect local SMTP on first run (try hosts/ports in priority order) and test the configured connection on every startup
- ALWAYS allow `SMTP_*` env vars to override `server.yml` SMTP config
- ALWAYS include why-sent explanation, app identity (name + FQDN), and a visible plaintext link in every operator notification email
- ALWAYS suppress `scheduler_error` when a more specific failure event (`backup_failed`, `ssl_renewal_failed`) fires for the same execution
- ALWAYS run the built-in scheduler continuously from application start until shutdown, with state persisted in `server.db`
- ALWAYS run missed tasks on startup if within `catch_up_window` (default 1h), in original scheduled order
- ALWAYS complete running scheduler tasks before shutdown (max 30s wait), then force-release locks and mark interrupted tasks for retry
- ALWAYS download GeoIP MMDB databases on first run and update them via the scheduler — never embed them in the binary
- ALWAYS treat GeoIP blocking decisions as advisory (bypassable via VPN/proxy/Tor)
- ALWAYS require login/API keys/session cookies/tokens regardless of source country
- ALWAYS expose metrics at `/server/metrics`; mount `/metrics[/{service}]` only when `server.metrics.root.enabled` is true (default true)
- ALWAYS prefix every metric with `{project_name}_`, use snake_case, use base units (seconds, bytes), and suffix counters with `_total`
- ALWAYS verify a backup immediately after creation (file exists, size>0, checksum, decrypt test, manifest parse, content extraction, DB integrity) — all checks must pass
- ALWAYS check free disk space before creating a scheduled backup; skip and log an error if insufficient
- ALWAYS require backup password interactively (CLI prompt / WebUI dialog / API 400) when restoring an encrypted backup
- ALWAYS verify a backup (file exists, readable, valid format, decrypt test, checksum, manifest, version compat) before restoring, and only restore if all checks pass
- ALWAYS require authorization for restore per PART 5 (allowed on empty DB or as root with confirmation; requires operator token as service user; denied for random users)
- ALWAYS verify the SHA256 checksum of a downloaded update binary against the release's `checksums.txt` before installing
- ALWAYS surface "update available" only to operators — WARN log, optional `update_available` email, and `--update check` / `--status` CLI output — fired once per newly-seen eligible version

## PART 17: Email & Notifications

### Templates
| Item | Detail |
|---|---|
| Default location | Embedded in binary (`src/server/template/email/`) |
| Custom location | `{config_dir}/template/email/` |
| Format | `Subject: ...` line, `---` separator, plain-text body with `{variable}` syntax |
| Reset to default | Delete the custom template file |
| Templates | `security_alert`, `backup_complete`, `backup_failed`, `ssl_expiring`, `ssl_renewed`, `ssl_renewal_failed`, `scheduler_error`, `update_available`, `update_installed`, `test` |
| Test send | `{project_name} email test`; subject prefixed `[TEST]`; logged to audit log |

### SMTP auto-detection priority
1. `127.0.0.1` (loopback)
2. `172.17.0.1` (Docker bridge gateway)
3. `{gateway_ip}`
4. `{fqdn}`
5. `{global_ipv4}`
6. `mail.{fqdn}`
7. `smtp.{fqdn}`

Ports tried at each host: 25, 465, 587. Handshake = EHLO. First success wins, saved to `server.yml`.

### SMTP config keys
`server.notifications.email.smtp.{host,port,username,password,tls}` (tls: auto/starttls/tls/none), `server.notifications.email.from.{name,email}`.
Env overrides: `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_TLS`, `SMTP_FROM_NAME`, `SMTP_FROM_EMAIL`.
Default from: name = app title, email = `no-reply@{fqdn}` (or `no-reply@localhost`).

### Three notification channels
| Channel | Audience | Availability |
|---|---|---|
| Public WebUI (toast/banner) | Visitors | Always, client-side |
| Logs | Operators | Always |
| Email | Operators | Requires valid SMTP |

Toast auto-dismiss: success/info 3s, warning 5s, error manual. Banner dismissal stored in `dismissed_announcements` cookie.

### Operator Notifications matrix (event → log level → email)
| Event | Log | Email |
|---|---|---|
| Config validation error | ERROR | ✗ |
| Backup failed | ERROR | ✓ |
| SSL expiring 30/14d | WARN | ✗ |
| SSL expiring 7/3/1d | WARN | ✓ |
| SSL renewal failed | ERROR | ✓ |
| Update available | WARN | Optional |
| Update installed | INFO | ✓ |
| Scheduler task failed | ERROR | ✓ (suppressed if backup_failed/ssl_renewal_failed fired) |
| Security alert | ERROR | ✓ |
| Database connection issue | ERROR | ✓ |
| Disk space low | WARN | ✓ |
| GeoIP database outdated | WARN | ✗ |

Default per-event email toggles (`server.notifications.email.events`): `backup_failed: true`, `ssl_expiring: true`, `ssl_renewal_failed: true`, `security_alert: true`, `scheduler_error: true`, `update_installed: true`; all others `false`.

## PART 18: Scheduler

### Built-in tasks (required)
| Task | Default Schedule | Skippable |
|---|---|---|
| `ssl_renewal` | Daily 03:00 (`0 3 * * *`) | No |
| `geoip_update` | Weekly Sun 03:00 (`0 3 * * 0`) | Yes |
| `blocklist_update` | Daily 04:00 (`0 4 * * *`) | Yes |
| `cve_update` | Daily 05:00 (`0 5 * * *`) | Yes |
| `update_check` | Daily 06:00 (`0 6 * * *`) | Yes |
| `token_cleanup` | Every 15m (`@every 15m`) | No |
| `log_rotation` | Daily 00:00 (`0 0 * * *`) | No |
| `backup_daily` | Daily 02:00 (`0 2 * * *`) | Yes |
| `backup_hourly` | Hourly (`@hourly`), disabled by default | Yes |
| `healthcheck_self` | Every 5m (`@every 5m`) | No |
| `tor_health` | Every 10m (`@every 10m`) | No (only if Tor installed) |

Schedule formats: standard cron, `@hourly`, `@daily`, `@weekly`, `@monthly`, `@every Xm`, `@every Xh`.

Config: `server.scheduler.timezone` (default `America/New_York`), `server.scheduler.catch_up_window` (default `1h`).

Persistent state columns in `server.db`: `task_id`, `task_name`, `schedule`, `last_run`, `last_status`, `last_error`, `next_run`, `run_count`, `fail_count`, `enabled`.

Retry policy defaults: `max_retries: 3`, `retry_delay: 5m`, `backoff: exponential` (5m, 10m, 20m).

CLI: `scheduler list`, `scheduler show <id>`, `scheduler run <id>`, `scheduler enable <id>`, `scheduler disable <id>`, `scheduler history <id>`.

Implementation: Go `time`/ticker (no external cron libs); state in `server.db`; graceful shutdown (30s wait, force-release + retry-on-restart on timeout).

## PART 19: GeoIP

### Data source
[sapics/ip-location-db](https://github.com/sapics/ip-location-db) — MMDB format, no API key.

| Database | File | Enabled Config |
|---|---|---|
| ASN | `asn.mmdb` | `geoip.databases.asn` |
| Country | `country.mmdb` | `geoip.databases.country` |
| City IPv4 | `dbip-city-ipv4.mmdb` | `geoip.databases.city` |
| City IPv6 | `dbip-city-ipv6.mmdb` | `geoip.databases.city` |
| WHOIS | (combined query, no separate file) | `geoip.databases.whois` |

Go library: `github.com/oschwald/maxminddb-golang` (NOT `geoip2-golang`).

Config: `server.geoip.enabled`, `server.geoip.dir` (default `{data_dir}/security/geoip`), `server.geoip.deny_countries` / `server.geoip.allow_countries` (ISO 3166-1 alpha-2; `allow_countries` wins if both set), update schedule handled by `geoip_update` scheduler task (weekly Sun 03:00).

Country blocking: allowlisted IPs and private/RFC1918 IPs are never blocked; missing `country.mmdb` skips blocking with a warning; Tor exit nodes judged by exit-node country.

## PART 20: Metrics

| Item | Detail |
|---|---|
| Format | Prometheus text exposition format |
| Endpoint | `/server/metrics` with `/metrics[/{service}]` root aliases gated by `server.metrics.root.enabled` |
| Library | `github.com/prometheus/client_golang` |
| Access | INTERNAL ONLY — firewall/proxy/NetworkPolicy/security-group restricted |
| Auth | Separate required bearer tokens for Prometheus, Grafana, and Loki; empty token disables that service (403). Only `server.metrics.auth.allow_unauthenticated: true` skips checks for firewalled networks. |

Config keys: `server.metrics.enabled`, `.root.enabled`, `.auth.allow_unauthenticated`, `.auth.tokens.{prometheus,grafana,loki}`, `.include_system`, `.include_runtime`, `.loki.{max_entries,max_age}`, `.duration_buckets`, `.size_buckets`.

Naming: prefix `{project_name}_`, snake_case, unit suffixes (`_seconds`, `_bytes`), counters end `_total`.

### Required metric groups
| Group | Examples |
|---|---|
| App info | `app_info` (gauge, labels version/commit/build_date/go_version), `app_uptime_seconds`, `app_start_timestamp` |
| HTTP (required) | `http_requests_total{method,path,status}`, `http_request_duration_seconds`, `http_request_size_bytes`, `http_response_size_bytes`, `http_active_requests` |
| Database (if used) | `db_queries_total{operation,table}`, `db_query_duration_seconds`, `db_connections_open`, `db_connections_in_use`, `db_errors_total{operation,error_type}` |
| Auth (required) | `auth_attempts_total{method,status}`, `auth_sessions_active` |
| Cache (if used) | `cache_hits_total{cache}`, `cache_misses_total{cache}`, `cache_evictions_total{cache}`, `cache_size{cache}`, `cache_bytes{cache}` |
| Scheduler (if used) | `scheduler_tasks_total{task,status}`, `scheduler_task_duration_seconds{task}`, `scheduler_tasks_running{task}`, `scheduler_last_run_timestamp{task}` |
| System (`include_system`) | `system_cpu_usage_percent`, `system_memory_usage_percent`, `system_memory_used_bytes`, `system_memory_total_bytes`, `system_disk_usage_percent{path}` |
| Go runtime (`include_runtime`) | `go_goroutines`, `go_mem_alloc_bytes`, `go_mem_sys_bytes`, `go_gc_runs_total`, `go_gc_pause_total_seconds` |
| Tor | `tor_enabled`, `tor_running`, `tor_circuit_established`, `tor_requests_total` |
| Rate limiting | `ratelimit_requests_total{limit,status}`, `ratelimit_blocked_total{limit}` — `limit` is a value like `per_ip`, never a raw per-IP label |

Path label normalization: replace UUIDs/IDs with `:id`.

Duration histogram buckets (default): `[0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]`. Size buckets: `[100, 1000, 10000, 100000, 1000000, 10000000]`.

## PART 21: Backup & Restore

### Command
`{project_name} --maintenance backup [filename]` / `{project_name} --maintenance restore <backup-file>`

### Contents
| Content | Included |
|---|---|
| `server.yml` | Always |
| `server.db` (config, rate limits, audit log, scheduler state, backup metadata) | Always |
| `{config_dir}/template/` | If exists |
| `{config_dir}/theme/` | If exists |
| `{config_dir}/ssl/` | Optional (`--include-ssl`) |
| `{data_dir}/` | Optional (`--include-data`) |

### Format
- `.tar.gz` (unencrypted) or `.tar.gz.enc` (encrypted)
- Manual: `{project_name}_backup_YYYY-MM-DD_HHMMSS.tar.gz[.enc]`
- Scheduled daily full: `{project_name}_backup_YYYY-MM-DD.tar.gz[.enc]`
- Daily incremental: `{project_name}-daily.tar.gz[.enc]` (always 1 file)
- Hourly incremental: `{project_name}-hourly.tar.gz[.enc]` (always 1 file, if enabled)
- Manifest `manifest.json`: version, created_at, created_by, app_version, contents[], encrypted, encryption_method, checksum (sha256)

### Encryption
| Compliance | Password | Result |
|---|---|---|
| Disabled | Not set | Unencrypted |
| Disabled | Set | Encrypted |
| Enabled | Not set | Backups BLOCKED |
| Enabled | Set | Encrypted (required) |

Algorithm: AES-256-GCM; key derivation: Argon2id; unencrypted archive never touches disk; password never stored; no password flag (interactive prompt only).

### Retention
| Setting | Default | Range |
|---|---|---|
| `max_backups` | 1 | ≥1 |
| `keep_weekly` | 0 | ≥0 |
| `keep_monthly` | 0 | ≥0 |
| `keep_yearly` | 0 | ≥0 |
| `max_total_size` | `"10%"` | % or absolute (e.g. `"50G"`), `0`=disabled, overrides count limits |

Priority order for keeping: yearly > monthly > weekly > daily. Falsey/disabled values: `0, false, no, none, disable, disabled, off`.

### Verification checks (must all pass)
File exists, size>0, checksum valid (SHA-256), decrypt test (if encrypted), manifest readable, content extraction, database integrity.

### Backup flow (scheduled, 02:00)
1. Retention sweep
2. Disk check (skip+log error if free < 2× last backup size or usage > 90%)
3. Create full backup, verify
4. Create daily incremental, verify
5. If all pass: apply retention; if any fail: delete failed file, keep existing, alert operator, retry next run

### Restore authorization
| Condition | Result |
|---|---|
| DB empty (first-run) | Allowed |
| Running as root | Allowed (with confirmation) |
| Running as service user | Requires operator token (`server.token`) |
| Random user | Denied |

Restore verification (must all pass before restoring): file exists, readable, valid format, decrypt test, checksum, manifest valid, version compatible (warning only, not fatal).

## PART 22: Update Command

### Command
`--update [check|yes|branch {stable|beta|devel}]` (default `yes`). Alias: `--maintenance update [cmd]`.

Exit codes: `0` = success/no update, `1` = error. GitHub API 404 = no update available.

### Channels (cumulative)
| Channel | Tag pattern | Selects |
|---|---|---|
| `stable` (default) | `v*`, `*.*.*` | Newest stable |
| `beta` | `*-beta` | Newest of beta + stable |
| `devel` | rolling `devel` tag | Newest of devel + beta + stable (nightly detected via `published_at` vs embedded `BUILD_EPOCH`) |

### Config
`server.update.branch` (default `stable`), `server.update.auto_install` (default `false`), `server.update.defer_days` (0-365, default `0`).

`defer_days` applies to stable and beta only (no-op on devel); it gates only the scheduled `update_check` task (both notify and auto-install); manual `--update check`/`--update yes` always see/install the true latest.

### Scheduled task
`update_check` (daily 06:00). `auto_install: false` → notify only (`update_available` event, WARN log, optional email, no binary touch). `auto_install: true` → runs full `--update yes` for eligible releases. Fires once per newly-seen eligible version. Never surfaced on public endpoints.

### Update flow
1. Check GitHub Releases API for updates
2. Download new binary to temp location
3. Verify SHA256 checksum against release's `checksums.txt` (mandatory)
4. Replace running binary (platform-specific — Unix: atomic rename over running exe; Windows: rename current to `.old`, move new into place, schedule `.old` deletion on reboot)
5. Restart service (`systemctl restart`, `launchctl kickstart -k`, `service restart`, `sc stop && sc start`) or re-exec (`syscall.Exec` on Unix, spawn+exit on Windows)

Endpoint used: `https://api.github.com/repos/{project_org}/{project_name}/releases/latest` (stable) or `.../releases` (beta/devel, filtered client-side).

For complete details, see AI.md PART 17, 18, 19, 20, 21, 22
