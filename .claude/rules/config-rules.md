# Config Rules (PART 5, 6, 12)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER use inline YAML comments — all comments go ABOVE the setting (exception: GitHub Actions SHA-pin `# vX.Y.Z` annotations stay inline)
- NEVER use `strconv.ParseBool()` directly — only accepts `true/false/1/0/t/f`; always use `config.ParseBool()`/`config.IsTruthy()`
- NEVER store user accounts or operator-editable configuration in the database — `server.yml` is the sole source of truth for configuration; database stores resource state, owners, owner and linked tokens, audit log only (owners are anonymous credential-only identities, not accounts)
- NEVER silently default an invalid boolean value — empty/unset uses default, but an actually invalid value is an error
- NEVER fail startup on invalid config — warn and replace with default instead
- NEVER let path traversal through — reject any path containing `..`, reject uppercase/invalid-char segments, reject segments >64 chars
- NEVER trust `X-Forwarded-*` headers from a peer not in `trusted_proxies` (private ranges always trusted; public IPs need explicit `additional` allow-list entry)
- NEVER let debug mode bypass `server.token` auth or any security/auth check — debug affects verbosity/diagnostics ONLY, never auth, in any mode including production
- NEVER expose `/debug/*`, `/debug/pprof/*`, `/debug/vars` unless `--debug`/`DEBUG=true` is explicitly set — otherwise 404
- NEVER expose sensitive data via `/server/healthz` — no connection strings, API keys, passwords, internal IPs, file paths, env vars, or config contents; database/cache checks must be vague (`"ok"`/`"error"` only)
- NEVER auto-advertise `abuse@{fqdn}` — unlike `security@{fqdn}`, abuse email is opt-in only (empty by default) since an unprovisioned mailbox would bounce reports
- NEVER let `server.contact.admin.email` become public — it is server-internal only
- NEVER expose `webhooks.*` URLs publicly — they contain secrets/chat IDs
- NEVER re-resolve the backup directory path at cleanup time — use the one resolved and cached at startup step 7
- NEVER run permanently as root/Administrator without an explicit, justified exception documented in IDEA.md — default is privilege drop to the dedicated service user after privileged setup
- NEVER hardcode a Docker `USER` directive when the binary must start privileged then drop privileges (breaks privileged port binding)
- NEVER put `Retry-After` or `X-Maintenance-*` in the JSON response body — these are headers only (RFC 9110 §10.2.3 compliance, avoids wire duplication)
- NEVER embed clearnet FQDN, clearnet email, or `Preferred-Languages` in any response served to a Tor (.onion) request
- NEVER cache/freeze the Tor `security.txt` variant at startup — always generated per-request via `BuildURL(r, path)`
- NEVER introduce flat aliases or duplicate names for the four canonical contact keys (`admin`, `security`, `abuse`, `general`)

## CRITICAL - ALWAYS DO

- ALWAYS normalize and validate every path (config values, HTTP paths, file paths, API params) via `SafePath()`/`normalizePath()`/`validatePath()`
- ALWAYS run `PathSecurityMiddleware` at execution position #3 (after URL normalize + RequestID, before auth/routing) — see Middleware Order below
- ALWAYS use `config.ParseBool()` / `config.IsTruthy()` / `config.IsFalsy()` for ALL boolean parsing (env vars, config file, CLI flags, API params, form inputs, query strings)
- ALWAYS accept case-insensitive truthy/falsy value sets (yes/no, on/off, enable/disable, oui/non, si/no, etc. — see table below)
- ALWAYS auto-migrate `server.yaml` → `server.yml` on startup if found
- ALWAYS save the selected port (random or specified) to `server.yml` so it persists across restarts — random selection only happens once, on first run
- ALWAYS bind privileged ports (<1024) while still root, then drop privileges (Unix)
- ALWAYS check actual access AND authorization (not just file ownership) for sensitive operations (`--maintenance setup/restore/mode/pgp/secret rotate`)
- ALWAYS attempt self-healing on non-critical errors; only DB connection failure and file write failure are truly critical (enter maintenance mode)
- ALWAYS log fix instructions and retry every 30s while in maintenance mode
- ALWAYS use `server.token`-authenticated status endpoint / server logs for maintenance-mode operator guidance
- ALWAYS validate all config values on load; on invalid value, warn + substitute the default; never crash
- ALWAYS honor `trusted_proxies` gate before trusting any `X-Forwarded-*`/`X-Real-IP`/etc. header
- ALWAYS preserve the original TCP peer address in request context before any real-IP middleware rewrites `r.RemoteAddr`; trust checks (`isTrustedPeer()`) evaluate the original peer, never the rewritten value
- ALWAYS treat Tor Host-header match as priority 0 in FQDN resolution — before proxy headers, no IP check needed
- ALWAYS fall back role-specific contact email/webhooks to `admin` when explicitly empty (except `abuse`, which chains through `general` first)
- ALWAYS sign outbound webhook POSTs with `X-Webhook-Signature` (HMAC-SHA256), `X-Webhook-Timestamp`, `X-Webhook-ID` (UUIDv7), `X-Webhook-Event`
- ALWAYS retry failed webhooks with exponential backoff (1m, 5m, 15m, 1h, 6h, 24h) reusing the same `X-Webhook-ID`
- ALWAYS gate debug endpoints strictly behind `--debug`/`DEBUG=true` — mode (production/development) alone never enables them
- ALWAYS keep config comments single-line, under 140 characters

## Key Rules Summary

### Config file
| Item | Value |
|---|---|
| Filename | `server.yml` (never `.yaml`; auto-migrated) |
| Root path | `/etc/{internal_org}/{internal_name}/server.yml` |
| Regular user path | `~/.config/{internal_org}/{internal_name}/server.yml` |
| Source of truth | Configuration lives in `server.yml`; DB never holds user accounts / operator config |

### Boolean truthy/falsy sets
| Truthy | Falsy |
|---|---|
| 1,y,t,yes,true,on,ok,enable,enabled,yep,yup,yeah,aye,si,oui,da,hai,affirmative,accept,allow,grant,sure,totally | 0,n,f,no,false,off,disable,disabled,nope,nah,nay,nein,non,niet,iie,lie,negative,reject,block,revoke,deny,never,noway |

Implementation: `src/config/bool.go` — `ParseBool(s, default)`, `MustParseBool`, `IsTruthy`, `IsFalsy`.

### Env vars
| Runtime (always checked) | Init-only (first run) |
|---|---|
| `NO_COLOR`, `TERM`, `DOMAIN`, `MODE`, `DATABASE_DRIVER`, `DATABASE_URL`, `SMTP_*` | `CONFIG_DIR`, `DATA_DIR`, `LOG_DIR`, `DATABASE_DIR`, `BACKUP_DIR`, `PORT`, `LISTEN`, `APPLICATION_NAME`, `APPLICATION_TAGLINE` |

URL variable resolution order: `{fqdn}`: Reverse Proxy → `DOMAIN` → `os.Hostname()` → `$HOSTNAME` → Global IP → `localhost`. `{proto}`: `X-Forwarded-Proto` → `X-Forwarded-Ssl` → `X-Url-Scheme` → TLS detect → `http`. `{port}`: `X-Forwarded-Port` → Host header → server port → proto default. `{baseurl}`: `X-Forwarded-Prefix` → `X-Forwarded-Path` → `X-Script-Name` → `server.baseurl` → `/`.

### Mode / Debug precedence
- Mode: `--mode` CLI > `MODE` env > default `production`
- Debug: `--debug` CLI > `DEBUG` env (truthy) > `--mode debug`/`MODE=debug` alias > default `false`
- `MODE=debug` alias = development + debug on; explicit `DEBUG` env (even `DEBUG=false`) always wins over the alias
- Mode shortcuts: `dev`, `devel`, `development` → development; `prod`, `production` → production; `debug` → development + debug on

### Ports
| Rule | Value |
|---|---|
| Default range | random unused port 64000-64999, saved to config on first run |
| Port 80 | enables Let's Encrypt HTTP-01 |
| Port 443 | enables Let's Encrypt TLS-ALPN-01, auto-enables SSL |
| Port 0 | OS assigns any available port |
| Dual port format | `"8090,8443"` (HTTP,HTTPS) |
| Port change | config-file only, requires restart, no runtime API |
| Privileged (<1024) | needs `isElevated()`; service install escalates once, binds, then drops privileges |
| Display | strip `:80`/`:443`, show all other ports |

### Service user
| Property | Value |
|---|---|
| Username/Group | `{project_name}` |
| Shell | `/usr/sbin/nologin` |
| Home | `/var/lib/{internal_org}/{internal_name}` |
| Dirs owned | `/etc/`, `/var/lib/`, `/var/cache/`, `/var/log/{internal_org}/{internal_name}/` |
| Perms | `755` on base dirs; `700` on `security/`, `ssl/`, `tor/` subdirs |

### Escalation / authorization matrix
| Op | Requires |
|---|---|
| `--service --install/--uninstall/start/stop/restart/reload/disable` | escalation if system service installed |
| `--service status` | none (read-only) |
| `--port <1024` | `isElevated()` else fallback to random >1024 |
| `--maintenance backup` | none (dir already owned) |
| `--maintenance restore` | `server.token` OR root OR empty database |
| `--maintenance setup` | first-run OR root |
| `--maintenance mode` | `server.token` OR root |
| `--maintenance pgp <action>` | `server.token` OR root (+ typed confirmation for export private/import/delete) |
| `--maintenance secret rotate installation_secret\|encryption_key` | `server.token` OR root + typed confirmation |

### Maintenance mode
| Critical error | Non-critical (self-healed) |
|---|---|
| Database connection error | Everything else |
| Cannot write files (disk full/perms) | |

Response: HTTP 503, body `{"ok":false,"error":"MAINTENANCE","message":"...","details":{"reason":...,"self_healing":true}}`. Headers: `Retry-After`, `X-Maintenance-Mode`, `X-Maintenance-Reason`. Self-healing retries every 30s.

### baseurl resolution
`X-Forwarded-Prefix` → `X-Forwarded-Path` → `X-Script-Name` → `server.baseurl`/`--baseurl` → `/`

### Trusted proxies (always trusted, no config)
`127.0.0.0/8`, `::1`, `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`, `169.254.0.0/16`, `fe80::/10`, same `/24` as listen address. Extend via `server.trusted_proxies.additional` (IP/CIDR/DNS, refreshed every 5 min).

### Tor (`tor.onion_address`, `tor.contact_email`)
Priority-0 FQDN match on Host header. Proto always `http://`, port always stripped. No clearnet FQDN/email/`Preferred-Languages` in any Tor response. CORS auto-adds onion origin, never leaks clearnet origin.

### Rate limiting defaults
| Class | Limit | Window |
|---|---|---|
| Read (GET/HEAD) | 120/min | 60s |
| Write (POST/PUT/PATCH/DELETE) | 10/min | 60s |
| Health/status | 120/min | 60s |
| Global burst | 240/min | 60s |

429 response: `Retry-After` header, body `{"ok":false,"error":"RATE_LIMITED","message":"Too many requests"}`.

### Contact config (canonical keys only)
`server.contact.admin.email` (never public, required, fallback target) · `server.contact.security.email` (default `security@{fqdn}`, public) · `server.contact.abuse.email` (default `""`, opt-in) · `server.contact.general.email` (default `""`). Each has `webhooks: {telegram, discord, slack, generic}`. Fallback chain: `security`→`admin`; `abuse`→`general`→`admin`; `general`→`admin`.

### Analytics types
`google, matomo, piwik, owa, fathom, plausible, umami, simple, cloudflare` under `server.tracking.{type,id,url}`.

### Cache config
`server.cache.type`: `none | memory (default) | valkey | redis`. Use `url` OR `host/port/password` (url takes precedence). Prefix key with `{project_name}:`.

### Middleware order (execution 1→10)
1. URLNormalize 2. RequestID 3. PathSecurity 4. SecurityHeaders 5. Allowlist 6. Blocklist 7. RateLimit 8. GeoIP 9. Auth 10. Logging

For complete details, see AI.md PART 5, 6, 12
