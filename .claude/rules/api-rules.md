# API Rules (PART 13, 14, 15)

⚠️ **These rules are NON-NEGOTIABLE. Violations are bugs.** ⚠️

## CRITICAL - NEVER DO

- NEVER add sub-routes to health checks (no `/server/healthz/db`, no `/server/healthz/**`)
- NEVER expose sensitive data in `/server/healthz`: DB connection strings, API keys/tokens, passwords/secrets, internal IPs, file paths, env vars, config contents, SMTP host, operator/contact emails, stack traces, `/metrics` status, internal endpoints
- NEVER redirect `/healthz` to `/server/healthz` — it MUST mount the exact same handler, no forked logic
- NEVER keep legacy (changed/removed) endpoints from your own project — delete them completely, no backwards-compat shims, redirects, or deprecation periods
- NEVER layer new routes on top of old code when route rules change — migrate fully, delete superseded routes
- NEVER use singular resource names in routes (`/item` is wrong, `/items` is correct)
- NEVER use uppercase or underscores in routes — lowercase + hyphens only (`/api-keys`, not `/API_Keys` or `/api_keys`)
- NEVER end a route with a trailing slash
- NEVER use verbs in routes (`/getItems` is wrong; use `GET /items`)
- NEVER hardcode `v1` — always use `{api_version}` / `APIBasePath()`
- NEVER put business logic, data transformation, validation (primary), formatting, pagination/sorting, search/filtering, HTML rendering, or state management in client-side JavaScript
- NEVER build client-side routing/rendering (SPA-style React/Vue) — server renders
- NEVER require JavaScript for core/frontend functionality
- NEVER manually edit generated `openapi.json` or GraphQL schema files — always generated from code at build time
- NEVER let Swagger/GraphQL specs drift from the actual API or from each other
- NEVER place swagger/graphql files in project root — only `src/swagger/` and `src/graphql/`
- NEVER emit a bare top-level JSON array in an API response — always wrap (`{"data": [...] }`)
- NEVER return a bare/unwrapped success object — every success response (single item, list, or action) uses `{"ok": true, "data": {...}}`, except health endpoints which return the bare health object
- NEVER invent a custom error shape per-endpoint — one canonical error envelope for all 4xx/5xx
- NEVER put the human message in `error` or the code in a `code` field, NEVER include `status` in error body, NEVER return bare `{"error": "..."}`, NEVER add ad-hoc fields like `reason`/`retry_after`/`self_healing` (use `details` or headers instead)
- NEVER serve an unversioned `/api/<thing>` alias as a redirect (301/302) to the versioned URL — mount same handler at both paths directly
- NEVER add unversioned aliases for version-specific/data-shaped endpoints (only for stable, operational ones: swagger, graphql, healthz, debug)
- NEVER show icons/ASCII art/colors in log files or stdout logs — logs are always raw plain text
- NEVER set `DOMAIN` to an overlay address (`.onion`, `.i2p`, `.exit`) — app-managed automatically
- NEVER let the app manage/auto-renew certs found under `/etc/letsencrypt/live/**` — system (certbot) owns those
- NEVER auto-renew certs in `{config_dir}/ssl/local/{fqdn}/` — user-managed, manual only

## CRITICAL - ALWAYS DO

- ALWAYS version all API routes as `/api/{api_version}/...`
- ALWAYS use plural nouns for resource routes
- ALWAYS use lowercase + hyphens for multi-word routes
- ALWAYS keep frontend routes matching backend API routes when a UI exists for a public API resource
- ALWAYS make frontend fully functional without JavaScript (progressive enhancement); forms submit via standard POST
- ALWAYS validate authoritatively on the server; client-side validation is UX-only and must mirror server rules
- ALWAYS accept auth tokens from any header defined in PART 8, plus `?token=`, using PART 8's priority order
- ALWAYS keep Swagger and GraphQL in sync with each other and with the live API, regenerated at build time
- ALWAYS use JSON only for OpenAPI spec (no YAML, no `.json` suffix route)
- ALWAYS 2-space indent for JSON/YAML/HTML/CSS/JS; tabs for Go and Makefiles; every file/response ends with exactly one trailing newline
- ALWAYS research the target service's actual API docs before implementing compatibility — never guess
- ALWAYS default external-service "compatibility" requests to feature/behavior parity using your own routes, unless the user explicitly asks for route/API/client compatibility
- ALWAYS fully implement ALL relevant RFCs when the application itself implements an RFC-defined protocol (DNS, DHCP, SMTP, HTTP, FTP, NTP, WebDAV, etc.) — not optional
- ALWAYS support Let's Encrypt (HTTP-01, TLS-ALPN-01, DNS-01) in every project
- ALWAYS check certificate lookup order on startup before requesting a new cert
- ALWAYS auto-renew app-managed Let's Encrypt certs 7 days before expiry (daily check at 03:00)
- ALWAYS strip `:80` and `:443` from displayed URLs
- ALWAYS prefer reverse-proxy headers (`X-Forwarded-Host`, etc.) over static FQDN resolution at request time

## Key Rules Summary

### Health & Versioning (PART 13)

| Item | Value |
|---|---|
| Frontend health route | `/server/healthz` (content negotiation: HTML/text/JSON) |
| Optional root alias | `/healthz`, only if `server.healthz.root.enabled: true`, same handler, no redirect |
| API health route | `/api/{api_version}/server/healthz` (JSON default) |
| Unversioned health alias | `/api/healthz` (JSON, direct alias, no redirect) |
| Status values | `healthy`, `degraded`, `unhealthy` |
| Check values | `ok` / `error` only — never details (e.g. `database: "ok"`, never a connection string) |
| SemVer start | `1.0.0` (never `0.x.x`); pre-release suffix `-rc1`/`-alpha`; no `v` prefix in version string; git tags DO use `v` prefix |
| Version format (beta) | `YYYYMMDDHHMMSS-beta` |
| Version format (devel) | short commit id (7 chars), rolling `devel` tag |
| Version source order | `release.txt` → git tag → `dev` |

### API Structure (PART 14)

| Item | Value |
|---|---|
| Base pattern | `/api/{api_version}/{resource}` (plural, lowercase, hyphenated) |
| Server-owned scope | `/server/*` (web), `/api/{api_version}/server/*` (API) |
| Success shape | `{"ok": true, "data": {...}}` — for ALL success responses (single item, list, action); `data` carries an object or array; lists add a sibling `pagination` object. Exception: health endpoints return the bare health object |
| Error envelope | `{"ok": false, "error": "CODE", "message": "...", "details": {}}` |
| Pagination default | 250 items/page; `{"data": [], "pagination": {"page":1,"limit":250,"total":1000,"pages":4}}` |
| Content negotiation (API) priority | 1. `.txt` ext → 2. `Accept: text/plain` → 3. non-interactive client → 4. default JSON |
| Content negotiation (frontend) priority | 1. `Accept: text/html` → 2. `Accept: text/plain` → 3. User-Agent browser detect → 4. CLI/curl → 5. default HTML |
| Client types | Our CLI (`{project}-cli/` UA) → JSON; Text browsers (lynx/w3m/links/elinks/browsh/carbonyl/netsurf) → no-JS HTML; HTTP tools (curl/wget/httpie/no UA) → `HTML2TextConverter` text |
| Required API types | REST (primary) + Swagger + GraphQL — all three, always |
| Swagger files | `src/swagger/swagger.go`, `theme.go`, `annotations.go` |
| GraphQL files | `src/graphql/graphql.go`, `schema.go`, `resolvers.go`, `theme.go` |
| Root endpoints | `/`, `/server/healthz`, `/healthz` (opt), `/server/docs/swagger`, `/server/docs/graphql`, `/metrics`, `/api/autodiscover`, `/api/swagger`, `/api/graphql`, `/api/healthz`, `/api/{api_version}/server/swagger`, `/api/{api_version}/server/graphql`, `/api/{api_version}/server/healthz`, `/api/{api_version}/server/*` |
| Removed old paths | `/openapi`, `/openapi.json`, `/graphql` (root GET/POST) — do not redirect from these |
| Standard error codes | `BAD_REQUEST`, `VALIDATION_FAILED`, `UNAUTHORIZED`, `NOT_FOUND`, etc. (UPPER_SNAKE_CASE, see PART 9) |
| Route params | Path params for resource identity (`/items/{id}`); query params for filter/sort/paginate (`?page=2&limit=10&sort=date`) |

### SSL/TLS & Let's Encrypt (PART 15)

| Item | Value |
|---|---|
| Challenge types | HTTP-01 (port 80, default), TLS-ALPN-01 (port 443), DNS-01 (wildcard, no port req) |
| DNS-01 config | `server.tls.dns_provider` + `server.tls.dns_credentials.*` (AES-256-GCM encrypted at rest) |
| Cert lookup order | 1. `/etc/letsencrypt/live/domain/` → 2. `/etc/letsencrypt/live/{fqdn}/` → 3. `{config_dir}/ssl/letsencrypt/{fqdn}/` → 4. `{config_dir}/ssl/local/{fqdn}/` → else request new |
| App-managed cert paths | `{config_dir}/ssl/letsencrypt/{fqdn}/fullchain.pem`, `privkey.pem` (auto-renew 7 days before expiry, daily 03:00 check) |
| User-managed cert paths | `{config_dir}/ssl/local/{fqdn}/cert.pem`, `key.pem` (manual renewal only) |
| System cert path | `/etc/letsencrypt/live/**` (certbot-managed, app never renews) |
| FQDN resolution order | 1. reverse proxy headers (`X-Forwarded-Host`, `X-Real-Host`, `X-Original-Host`) → 2. `DOMAIN` env → 3. `os.Hostname()` → 4. `$HOSTNAME` → 5. public IPv6 → 6. public IPv4 → 7. `localhost` |
| Port modes | Single port (HTTP by default; 443 forces HTTPS-only); dual ports = first HTTP, second HTTPS |
| URL formatting | Always strip `:80`/`:443` from displayed URLs |
| Overlay networks (Tor/I2P) | Self-signed certs only (LE unsupported for `.onion`/`.i2p`); inherit HTTPS-only from clearnet config |
| Dev TLDs | `.local`, `.test`, `.example`, `.invalid`, `.localhost`, `.lan`, `.internal`, `.home`, `.localdomain`, `.home.arpa`, `.intranet`, `.corp`, `.private`, plus `{project_name}` / `{project_name}.local` / `{project_name}.test` |

For complete details, see AI.md PART 13, 14, 15
