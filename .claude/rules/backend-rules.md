# Backend Rules (PART 9, 10, 11, 31.1, 31.2)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER expose stack traces, full Go error chains, or internal error detail in production API responses — `_debug` field only under `DEBUG=true`, stripped in production by the Output Sanitization Pipeline.
- NEVER expose Tier 1 secrets publicly, ever (not even in debug mode): DB credentials/DSN, internal IPs/hostnames, any user's API/session/CSRF tokens, PGP private keys, other users' PII, filesystem paths, account-existence signals, exact rate-limit thresholds.
- NEVER use `DROP COLUMN`, `DROP TABLE`, `DELETE` for schema changes — schema updates are additive only (add column/table/index).
- NEVER rename a DB column directly — add new column, migrate in app code, deprecate old (3-step process); old column stays in DB forever.
- NEVER run raw/unparameterized SQL with user input (`fmt.Sprintf` SQL) — parameterized queries only.
- NEVER compare tokens/password-hashes/HMACs/signatures with `==`, `bytes.Equal`, or `strings.EqualFold` — always `crypto/subtle.ConstantTimeCompare`.
- NEVER return different auth-failure messages/timings for "wrong password" vs "no such user" vs "locked" vs "expired" vs "revoked" — identical message, identical HTTP status, ≥100ms floor.
- NEVER expose sequential integer primary keys in public URLs/JSON/logs — external IDs must be opaque UUID v4/v7.
- NEVER echo submitted password/token in error responses, logs, or audit events, even hashed.
- NEVER cast user-controlled file content (pastes, uploads, markdown-derived HTML) to `template.HTML` without sanitization; never inline untrusted SVG/XML/HTML in templates.
- NEVER shell out with raw user content, filenames, refs, or repo metadata; never execute user-supplied content on the server.
- NEVER log secrets: private keys, TLS secrets, SMTP credentials, full API credentials, credit card/financial data. Any field named `secret`/`key`/`password`/`token` is redacted at the log layer.
- NEVER emit `Expect-CT`, `Public-Key-Pins` (HPKP), or `Feature-Policy` headers — all deprecated/dangerous.
- NEVER emit `Server-Timing` header in production (debug-only).
- NEVER let `/.well-known/**` be claimed by user slugs/vanity routes; never directory-list it; never serve secrets there.
- NEVER use default Tor ports (9050/9051) — always `127.0.0.1:auto` for control, `SocksPort auto` or `0`.
- NEVER use/depend on system Tor — server MUST start its own dedicated, isolated Tor process (own DataDir, own control port).
- NEVER hardcode or persist a Tor control/SOCKS port — always runtime `auto` detection.
- NEVER let Tor be a relay/exit node: `ExitRelay 0`, `ExitPolicy reject *:*`, `ORPort 0`, `DirPort 0`.
- NEVER fail server startup because Tor is missing or errors — Tor is optional, best-effort, non-blocking (log INFO/WARN only).
- NEVER use `control.AddOnion()` to create the hidden service — the hidden service is declared in torrc (`HiddenServiceDir`/`HiddenServicePort`); Tor itself generates/persists the v3 key and `.onion` hostname.
- NEVER skip the key-format migration check on startup — an old 64-byte raw key file must be upgraded (32-byte native header prepended) before Tor starts, never left as-is or regenerated (regenerating silently changes the `.onion` address).
- NEVER touch a `hs_ed25519_secret_key` file whose size is neither 64 bytes (legacy) nor 96 bytes (native) — log a WARN and let Tor fail loudly rather than risk corrupting/replacing an unknown key.
- NEVER omit `HiddenServiceExportCircuitID haproxy` from torrc — the backend listener depends on the PROXY-protocol header it adds.
- NEVER let the Tor backend HTTP server bind anything but a random loopback port (`127.0.0.1:0`) — never a fixed or non-loopback port.
- NEVER wrap the I2P backend listener in `go-proxyproto` — unlike Tor, neither i2pd nor a SAM bridge prepends a PROXY-protocol header; the I2P backend listener is a plain loopback listener.
- NEVER enable I2P by default — it is strictly opt-in (`server.i2p.enabled` / `I2P_ENABLED` / `--i2p`, all default false); no backend port, provider process, or SAM session is created unless explicitly enabled.
- NEVER let the app reconfigure or depend on a system-wide i2pd — the app spawns/manages its own dedicated i2pd child process (Model A) when the binary is found.
- NEVER fail server startup because I2P is missing/misconfigured/unreachable — same non-blocking, best-effort contract as Tor (log WARN, disable I2P, continue).
- NEVER expose I2P configuration via a REST API or admin web UI — `server.yml` + CLI only, same as Tor.
- NEVER implement archive upload/extract unless the project explicitly needs it (path traversal / zip-bomb / symlink risk otherwise).
- Audit log: application must NEVER modify, delete, or truncate entries — append-only; only rotation removes old entries.

## CRITICAL - ALWAYS DO

- ALWAYS use the canonical response envelope: `{"ok": bool, "data"/"error"+"message"}`; error `details` is optional structured context.
- ALWAYS log every error server-side with request_id, error_code, http_status, internal error (never sent to client).
- ALWAYS use exponential backoff (0s,1s,2s,4s,8s, cap 30s) for retryable errors only (network/timeout/503); never retry 4xx.
- ALWAYS use `CREATE TABLE IF NOT EXISTS` / idempotent `ALTER TABLE ... ADD COLUMN` for schema — no migration files, no version table.
- ALWAYS wrap every DB query in a context with a timeout (5s simple SELECT, 15s JOIN, 10s write, 60s bulk, 5m migration, 2m report).
- ALWAYS use connection pooling with configured `max_open`/`max_idle`/`max_lifetime`/`max_idle_time`.
- ALWAYS treat user-controlled content as data, never trusted HTML: escape plain text, sanitize markdown-rendered HTML (no raw HTML passthrough), force `Content-Disposition: attachment` for user-supplied HTML/SVG/XML/XHTML.
- ALWAYS serve private files with `Cache-Control: private, no-store`, exact `Content-Type` + `X-Content-Type-Options: nosniff`, and normal authz on every request.
- ALWAYS pad failed-auth response time to a fixed floor (≥100ms).
- ALWAYS run every public response through the Output Sanitization Pipeline: allow-list fields → redact sensitive query params → strip internal IPs/paths → truncate long strings → strip `dev_only` fields in prod → constant-time finalize.
- ALWAYS send the full security header set on every response (see table below); add HSTS when SSL enabled.
- ALWAYS serve `robots.txt`, `/.well-known/security.txt` (RFC 9116), and `/.well-known/llms.txt` (+ `/llms.txt` alias) — generated if missing.
- ALWAYS write audit log entries as JSON Lines with required fields: `id` (ULID), `time` (ISO8601 ms UTC), `event`, `category`, `severity`, `actor`, `result`.
- ALWAYS mask tokens/IDs in logs to first 8 chars (`token_abc12345...`) or a separate non-reversible ID field.
- ALWAYS use Argon2id for passwords, SHA-256 for API token hashing, AES-256-GCM (`server.security.encryption_key`) for at-rest sensitive data.
- ALWAYS generate all project secrets (`installation_secret`, `cookie_signing_key`, `csrf_token_secret`, `server.security.encryption_key`) on first start, before any user-visible operation, and include them in every backup.
- ALWAYS start Tor (if binary found) as a child process of the server, inheriting the server's dropped-privilege user; terminate it on server shutdown.
- ALWAYS use `HiddenServiceVersion 3` (ed25519) declared via torrc `HiddenServiceDir`/`HiddenServicePort`, and persist the key at `{data_dir}/tor/site/hs_ed25519_secret_key` (mode 0600, native 96-byte format) for a stable `.onion` address.
- ALWAYS regenerate torrc unconditionally on every startup (never write-if-changed/skip-if-exists).
- ALWAYS allocate a dedicated loopback backend listener (`127.0.0.1:0`) wrapped in `proxyproto.Listener` (`github.com/pires/go-proxyproto`) for the Tor `HiddenServicePort` target, and run the app's handler on it via its own `http.Server`.
- ALWAYS read the `.onion` address from `{data_dir}/tor/site/hostname` after bootstrap (poll with short backoff up to `bootstrap_timeout`) — never derive/construct it from a service ID.
- ALWAYS create Tor dirs with 0700 and torrc with 0600.
- ALWAYS treat missing Tor binary as INFO-level, non-fatal; continue without Tor features.
- ALWAYS run the key-format migration (§ Tor Hidden Service below) before starting Tor on every `startLocked()`.
- ALWAYS delete the full `{data_dir}/tor/site/` key material (`hs_ed25519_secret_key`, `hs_ed25519_public_key`, `hostname`) on `RegenerateAddress()`, then restart so Tor generates a fresh native-format identity.
- ALWAYS treat `ApplyKeys()` input as Tor's native on-disk secret-key file format (e.g. from `mkp224o`) — write directly to `hs_ed25519_secret_key` (0600), delete stale `hostname`/public-key files, restart.
- ALWAYS start I2P (if enabled AND a provider — i2pd binary or reachable SAM bridge — is resolved) as a child process/session inheriting the server's dropped-privilege user; terminate/close it on server shutdown.
- ALWAYS allocate the I2P backend port only after a provider is confirmed, as a plain loopback listener (no proxyproto).
- ALWAYS create I2P dirs with 0700 (`{config_dir}/i2p/`, `{data_dir}/i2p/`, `{data_dir}/i2p/site/`) and the destination key file with 0600 (`{data_dir}/i2p/site/site-keys.dat`).
- ALWAYS regenerate `{config_dir}/i2p/tunnels.conf` on every startup when using the i2pd provider (Model A).
- ALWAYS treat a missing i2pd binary and an unreachable SAM bridge as INFO/WARN-level, non-fatal; continue without I2P features.

## Key Rules Summary

### Error handling (PART 9)
| Code | HTTP | Message |
|---|---|---|
| BAD_REQUEST / VALIDATION_FAILED | 400 | Invalid request / validation failed |
| UNAUTHORIZED / TOKEN_EXPIRED / TOKEN_INVALID | 401 | Auth required / expired / invalid |
| FORBIDDEN / ACCOUNT_LOCKED / CSRF_FAILED | 403 | Permission denied / locked / CSRF failed |
| NOT_FOUND | 404 | Resource not found |
| METHOD_NOT_ALLOWED | 405 | Method not allowed |
| CONFLICT | 409 | Resource already exists |
| RATE_LIMITED | 429 | Too many requests |
| SERVER_ERROR | 500 | Internal server error |
| MAINTENANCE | 503 | Service unavailable |

### Caching (PART 9)
- Drivers: `memory` (dev), `valkey`/`redis` (prod). Key pattern: `{type}:{id}`, lowercase, colon-separated, versioned (`v1:user:123`).
- TTLs: API tokens = no expiry; rate-limit counters = 1m; user profile = 5m; config = 1m; static hash = 24h; GeoIP = 7d; blocklist = 1h; page cache = 5m; API response = 30s.
- Cache-Control: static assets `public, max-age=31536000, immutable`; HTML/authenticated/errors `no-store`; public API `public, max-age=60`; private API `private, no-store`.

### Database (PART 10)
- No migrations table; idempotent `EnsureSchema()` runs every startup.
- Pool sizing: dev 5/2, small 25/5, medium 50/10, large 100/20 (max_open/max_idle).
- Serializable isolation + retry-with-backoff for contested writes; optimistic locking via `version` column.

### Security headers (PART 11)
Always: `X-Content-Type-Options: nosniff`, `X-Frame-Options: SAMEORIGIN`, `X-XSS-Protection: 1; mode=block`, `Referrer-Policy: strict-origin-when-cross-origin`, `X-Permitted-Cross-Domain-Policies: none`, `Origin-Agent-Cluster: ?1`, `COOP`/`COEP` (default `unsafe-none`), `CORP` (default `cross-origin`), `Content-Security-Policy`, `Permissions-Policy`, `Reporting-Endpoints`, `Report-To`, `NEL`, `X-Request-ID`.
With SSL: `Strict-Transport-Security: max-age=63072000; includeSubDomains; preload`.
On token revocation/consent withdrawal: `Clear-Site-Data: "cache", "cookies", "storage"`.

### CSP default (PART 11)
`default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' https:; connect-src 'self' {learned_origins}; media-src 'self' blob:; worker-src 'self' blob:; manifest-src 'self'; frame-src 'self'; frame-ancestors 'self'; base-uri 'self'; form-action 'self'; object-src 'none'; upgrade-insecure-requests; report-to default; report-uri /api/{api_version}/server/reports/csp`

### Defense-in-depth layers (PART 11)
Input validation → parameterized queries/constant-time compare → output escaping/CSP → transport (min-priv DB user, TLS). Never assume another layer already caught it.

### Logging (PART 11)
| Log | Default format | Notes |
|---|---|---|
| access.log | apache | health-check 2xx suppressed by default |
| server.log / error.log | text | |
| app.log | logfmt | |
| auth.log | syslog (RFC 3164) | `result=success|fail`, `reason=` stable code |
| audit.log | json only | JSON Lines, append-only, tamper-evident, 0640 |
| security.log | fail2ban | also syslog/cef/json/text |
| debug.log | text | dev only |

Log files: raw text only, no ANSI/emoji/control chars. Console output may be pretty (respects `NO_COLOR`).
Rotation defaults: access=monthly, audit=daily, others=weekly,50MB. `keep: none` default (delete on rotation).

### Privacy signals and browser reports (PART 11)
- Honor `Sec-GPC: 1` by default, suppress non-essential cookies/tracking, and record `compliance.gpc_honored` only for an honored Sec-GPC signal.
- Honor `DNT: 1` only when `web.headers.honor_dnt` is enabled; DNT suppresses non-essential behavior but is never labeled as GPC.
- Network Error Logging reports go to `/api/{api_version}/server/reports/nel`, share report limits, and log sanitized fields without report URLs, credentials, or server IPs.

### Cryptographic keys (PART 11)
| Secret | Length | Storage | Rotation |
|---|---|---|---|
| installation_secret | 32B | server.db | manual, 7-day grace |
| cookie_signing_key | 32B (HMAC-SHA256) | server.db | auto every 90d, 7-day grace |
| csrf_token_secret | 32B | server.db | auto on API key regen + 180d |
| server.security.encryption_key | 32B AES-256-GCM | server.yml | manual, 30-day grace |

Secrets: never in any API response, never logged, always in backups, restored verbatim (all-or-nothing).

### IP blocking (PART 11)
Temporary (default 1h, auto-release) vs permanent (config-file only). Allowlist bypasses IP block/rate-limit/GeoIP/auto-block but NEVER CSRF, path security, or SSL/TLS.

### Tor Hidden Service (PART 31.1)
- Library: `github.com/cretz/bine` (CGO_ENABLED=0 compatible). Auto-enabled if Tor binary found — no toggle.
- Control: `ControlPort 127.0.0.1:auto` on all OSes. SOCKS: `SocksPort 0` (HS only) or `auto` (outbound enabled via `server.tor.use_network`).
- Hidden service declared via torrc (`HiddenServiceDir {data_dir}/tor/site`, `HiddenServiceVersion 3`, `HiddenServicePort {virtual_port} 127.0.0.1:{backend_port}`, `HiddenServiceExportCircuitID haproxy`) — **not** `control.AddOnion()`. Tor itself generates/persists the key and writes `hostname`.
- Backend: dedicated loopback listener (`127.0.0.1:0`) wrapped in `proxyproto.Listener` (`github.com/pires/go-proxyproto`, parses the HAProxy PROXY v1 header Tor prepends via `HiddenServiceExportCircuitID haproxy`), running its own `http.Server{Handler: s.router}`.
- Onion address: read from `{data_dir}/tor/site/hostname` after bootstrap (poll with short backoff up to `bootstrap_timeout`), cached — never constructed from a service ID.
- Dirs: config `{config_dir}/tor/` (0700, torrc 0600), data `{data_dir}/tor/` (0700), keys `{data_dir}/tor/site/` (0700, `hs_ed25519_secret_key`/`hs_ed25519_public_key`/`hostname`, key file 0600), log `{log_dir}/tor.log`.
- torrc always sets: `SafeLogging 1`, `ExitRelay 0`, `ExitPolicy reject *:*`, `ORPort 0`, `DirPort 0`, `MaxCircuitDirtiness 600`, `VanguardsLiteEnabled 1`, `HiddenServiceSingleHopMode 0`, `FetchDirInfoEarly 1`, `FetchDirInfoExtraEarly 1`, `DisableDebuggerAttachment 1`. Regenerated unconditionally every startup.
- Config defaults: `max_circuits=32`, `circuit_timeout=60s`, `bootstrap_timeout=180s`, `max_streams_per_circuit=100`, `bandwidth_rate=1MB`, `bandwidth_burst=2MB`, `max_monthly_bandwidth=100GB`, `num_intro_points=3`, `virtual_port=80`.
- **Key-format migration** (runs before every Tor start): legacy format = 64-byte raw expanded ed25519 key (no header, written by the old `AddOnion`-era `saveKey()`); native format = 96 bytes = 32-byte header `"== ed25519v1-secret: type0 ==\0\0"` + the same 64-byte key. 64-byte file found → prepend header, rewrite 0600, log the migration. 96-byte file found → already native, leave untouched. Any other size → do not touch, log WARN, let Tor fail loudly (never silently regenerate — that changes the `.onion` address).
- `RegenerateAddress()`: stop Tor → delete all of `{data_dir}/tor/site/` (`hs_ed25519_secret_key`, `hs_ed25519_public_key`, `hostname`) → start Tor (fresh native identity).
- `ApplyKeys(keyData)`: stop Tor → write `keyData` (Tor's native on-disk secret-key format, e.g. from a vanity tool like `mkp224o`) directly to `hs_ed25519_secret_key` (0600) → delete stale `hostname`/public-key files → start Tor.
- Logging: Tor-not-found = INFO; startup/runtime errors = WARN; server never fails to start due to Tor. Console: silent during bootstrap, show `Tor: connecting...` after 30s, show `.onion` address once on success.
- Config change: stop Tor → mutate torrc → start Tor (torrc always regenerated on start, so this is just a restart).

### I2P Eepsite (PART 31.2, optional)
- **Opt-in only, disabled by default**: `server.i2p.enabled` / `I2P_ENABLED` env / `--i2p` flag, all default `false`. No backend port, provider process, or SAM session exists unless explicitly enabled — unlike Tor's auto-enable-when-binary-found behavior.
- Scope: eepsite (`.b32.i2p`) hosting only — never outbound anonymized requests (that's Tor's job), never floodfill/relay/SOCKS-proxy functionality.
- Two provider models, resolved in order at start: **Model A** (preferred) — dedicated i2pd child process the app spawns/manages, regenerates `tunnels.conf` every startup, i2pd persists the destination key and derives `.b32.i2p`. **Model B** (fallback) — raw SAMv3 over TCP to `sam_address` (default `127.0.0.1:7656`): `HELLO VERSION` → load-or-create persisted destination → `SESSION CREATE STYLE=STREAM` → `STREAM FORWARD` to the backend port. Neither available → log WARN, disable I2P, continue (non-fatal, mirrors Tor).
- Backend: dedicated loopback listener (`127.0.0.1:0`), **plain, no `go-proxyproto` wrapping** — neither i2pd nor a SAM bridge prepends a PROXY-protocol header.
- `.b32.i2p` derivation: `base32(sha256(destination))`, no padding, lowercased, `+ ".b32.i2p"` — stdlib `crypto/sha256` + `encoding/base32`, no new dependency.
- Config (`server.i2p.*`): `enabled` (false), `binary` (auto-detect), `sam_address` ("127.0.0.1:7656"), `virtual_port` (80), `inbound_length`/`outbound_length` (3/3), `inbound_quantity`/`outbound_quantity` (5/5), `signature_type` (7, EdDSA-SHA512-Ed25519), `bootstrap_timeout` (5m). Directories are NOT configurable, always derived: config `{config_dir}/i2p/`, data `{data_dir}/i2p/`, destination key `{data_dir}/i2p/site/site-keys.dat`, log `{log_dir}/i2pd.log` (Model A only).
- Dirs: 0700 (`{config_dir}/i2p/`, `{data_dir}/i2p/`, `{data_dir}/i2p/site/`); destination key file 0600.
- i2pd binary lookup order: `server.i2p.binary` config → common per-OS locations → `$PATH` → not found: fall back to SAM; SAM unreachable too → disable I2P.
- No `RegenerateAddress`/`ApplyKeys`/vanity surface for I2P — not in the spec. Regenerate identity by stopping I2P, deleting `{data_dir}/i2p/site/`, and restarting.
- No REST API/admin web UI for I2P config — `server.yml` + CLI only, same model as Tor.
- `.b32.i2p` requests get the same priority-0 FQDN-trust treatment as `.onion` (no reverse-proxy header check, no clearnet FQDN/email leaked into the response).

For complete details, see AI.md PART 9, 10, 11, 31.1, 31.2
