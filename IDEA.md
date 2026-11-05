## Project description

Pastebin is a full-stack Go web application for creating and sharing text snippets anonymously. It is a drop-in replacement for pastebin.com, microbin, lenpaste (fork: forksmgr/lcomrade-lenpaste), stikked, hastebin/haste-server, dpaste, the curl-upload family (0x0.st, sprunge.us, ix.io), and termbin/fiche (raw-TCP, enabled by default) — existing scripts, CLIs, and integrations targeting any of those services work against this server without modification. Users submit text or upload a file and receive a short shareable URL. No user accounts. Fully public. Deployed as a single self-contained static binary. The companion CLI (`pastebin-cli`) provides full API access from the terminal.

## Project variables

project_name: pastebin
project_org: apimgr
# STABLE — set at creation, defaults to project_org; not changed by a rename
internal_org: apimgr
# STABLE — equals project_name on first install; reset only on an explicit org/repo rename or a fork's first setup
internal_name: pastebin
app_name: Pastebin
official_site: https://pste.us
maintainer_name: apimgr
maintainer_email: git-admin@casjaysdev.pro
repo: https://github.com/apimgr/pastebin
license: MIT
binary: pastebin
client_binary: pastebin-cli
api_version: v1
owner_token: pastebin_owner_token_rU3uW5Ze
tagline: Drop-in replacement for pastebin.com, hastebin, dpaste, microbin, and more

## Business logic

### Target users

- Developers and sysadmins sharing logs, diffs, config snippets, and stack traces
- Terminal and script users who pipe output straight from a shell (`curl`, `nc`, `pastebin-cli`)
- Operators of existing pastebin.com / hastebin / dpaste / microbin / lenpaste / stikked / termbin scripts, CLIs, and bots who need a self-hosted drop-in without editing those integrations
- Self-hosters wanting a single-binary, account-free paste service they can run on their own hardware, optionally reachable over Tor or I2P
- Privacy-conscious users who want anonymous, expiring, or burn-after-read snippets with no registration

### Product scope & non-goals

**In scope:**
- Anonymous paste creation via web form, JSON API, raw body (curl pipe), or multipart file upload
- Server-side syntax highlighting — rendered on the server, no client-side JS library required
- Language auto-detection from file extension on upload; manual selection otherwise
- Expiry options: `1h`, `1d`, `1w`, `1m`, `3m`, `6m`, `1y`, `18m`, `2y`, `never` (default), or a custom duration in seconds
- Burn after N reads: paste is permanently deleted once its view count reaches a user-set threshold (1–9999); `0` = disabled
- Visibility: public (listed in recent pastes) or unlisted (URL-only, not listed)
- Owner token: a `tok_`-prefixed cryptographically random token returned in every API create response; reusable across pastes created by the same caller; stored as a SHA-256 hash; required to delete the paste before natural expiry
- Token reuse: callers may supply an existing owner token on paste creation to link the new paste to that token; the web UI saves the token to the `pastebin_owner_token_rU3uW5Ze` cookie (HttpOnly, primary) and, as a JS convenience copy, to `localStorage` under the same key; the create form is pre-filled by JS only, never load-bearing
- Raw paste view, file download, iframe-embeddable view with copy-ready HTML and Markdown embed snippets offered on the paste view page, and a QR code available as both a page and a PNG image
- View count tracking; automatic background cleanup of expired and burned pastes
- Full web frontend (server-side Go templates, dark/light/auto theme, PWA, mobile-first)
- Static info pages: about, help, health check, privacy, terms
- CLI client (`pastebin-cli`) — full API access from the terminal
- OpenAPI/Swagger docs
- GraphQL read-only query interface (create and delete are REST-only)
- MCP (Model Context Protocol) endpoint so AI clients can use the paste API as tools: Streamable HTTP JSON-RPC at `/api/v1/server/mcp` (aliases `/api/mcp`, `/mcp`), on by default (`server.mcp.enabled`), same Bearer tokens as the API (no MCP-specific token), tools generated from the API routes (create/get/list/delete paste, health, version); an anonymous create tool call returns the `owner_token` exactly like the REST API; token management is never exposed as a tool
- Drop-in compat layer for pastebin.com, microbin, lenpaste, stikked, hastebin/haste-server, dpaste, the curl-upload family (0x0.st, sprunge.us, ix.io), and termbin/fiche (raw-TCP listener, enabled by default; disable via `server.termbin.enabled: false` or `TERMBIN_ENABLED=false`) — compat routes use their own delete-token mechanism (stored in paste row) separate from the owner token system
- Two-tier operator token: server operator may set a `server.token` in `server.yml`; this operator token allows deleting any paste unconditionally
- i18n: 7 supported locales (en, es, fr, de, zh, ar, ja); automatic language selection via `Accept-Language` header; RTL layout for Arabic (`dir="rtl"` on `<html>`); fallback to English when locale unknown
- Tor hidden service: auto-enabled when the `tor` binary is present; the site is reachable at a persistent v3 `.onion` address; non-fatal when Tor is absent — see PART 31.1 for HOW
- I2P eepsite: optional, disabled by default (`server.i2p.enabled` / `I2P_ENABLED` / `--i2p`); when enabled the site is reachable at a persistent `.b32.i2p` address; non-fatal when no provider is available — see PART 31.2 for HOW
- GeoIP: country-based access control (allowlist or denylist) applied per request, with a fail-open when the country database is unavailable — see PART 19 for HOW
- Prometheus metrics (`pastebin_` namespace); internal-only, never exposed publicly — see PART 20 for HOW
- Built-in task scheduler (no external cron): the PART 18 mandated task set plus a project-specific `expire-pastes` task that removes expired and burned pastes
- Backup and restore: encrypted when a backup password is set, integrity-verified before any restore, scheduled hourly and daily, CLI-triggerable — see PART 21 for HOW
- Self-update: checks GitHub Releases for new versions and installs a checksum-verified binary — see PART 22 for HOW
- Service lifecycle management: `--install`/`--uninstall` (requires root privilege; uninstall prompts interactive `[y/N]` confirmation before data purge); `--start`/`--stop`/`--restart`/`--reload`/`--disable`/`--enable`; systemd unit (Linux), launchd plist (macOS), SCM service (Windows)
- Rate limiting: per-endpoint, IP-based, configurable; proxy-aware (`X-Forwarded-For`, `CF-Connecting-IP`, `True-Client-IP`)
- IP/CIDR/country blocklist: loaded from a URL or local file; refreshed hourly by `blocklist_update` scheduled task
- Email notifications: SMTP; 7 template types (`security_alert`, `backup_complete`, `backup_failed`, `ssl_expiring`, `ssl_renewed`, `scheduler_error`, `test`); silently disabled when unconfigured
- Security report system: `/server/security` report submission flow with optional PGP-encrypted reports, `security.txt` (RFC 9116) at `/.well-known/security.txt`, and operator PGP key at `/.well-known/pgp-key.asc`
- Cookie consent and CCPA opt-out: consent banner (works without JavaScript) plus a do-not-sell opt-out submission; preferences stored client-side, no server-side tracking
- App-specific guest preferences (AI.md "Client-Side Preferences"): the `/pastes` recents list's sort column, sort direction, and page size are remembered across visits via `pastebin_pref_sort` (`date`\|`name`\|`views`, default `date`), `pastebin_pref_order` (`asc`\|`desc`, default `desc`), and `pastebin_pref_per_page` (`20`\|`50`\|`100`\|`250`, default `20`) cookies — set whenever the visitor changes a filter-bar control, read as the default on the next visit when no explicit query param is given, editable on `/server/preferences`, and included in preference export/import like every other `pastebin_pref_*` key
- URL shortening: `is_link` is auto-detected from content, never a client-settable flag — a paste becomes a link when its entire (trimmed) content is exactly one absolute `http://`/`https://` URL and nothing else (no extra whitespace, no additional lines, no surrounding text); no checkbox, form field, JSON field, or header controls it; auto-generated short ID only (no vanity or custom slugs); visiting the short link, from the web or the API, redirects to the target — the target is never fetched or validated server-side beyond scheme and format, so there is no SSRF exposure; if no title is supplied, a link paste's title defaults to the target URL itself rather than the generic "Untitled" fallback; the raw, download, and paste-detail views never redirect regardless of `is_link` — they always render or return the literal stored content, so the target URL stays retrievable as text and viewable as a normal paste; link pastes share the paste record, expiry, burn-after, visibility, owner-token deletion, and view-count tracking unchanged; language and syntax highlighting do not apply to links

**Non-goals:**
- No user accounts, registration, or login of any kind
- No admin web panel — server configured via `server.yml` only; no general-purpose runtime configuration API (the scheduler management endpoints under `/api/v1/scheduler/*` are the spec-mandated exception)
- No paste editing after creation — pastes are immutable
- No password-protected pastes (microbin per-paste encryption not implemented)
- No paid tiers, no rate-limited access tiers, no feature gating
- No cluster mode, no horizontal scaling, no node election — single instance only

### Features

- **Anonymous paste creation** — web form, JSON API, raw body (curl pipe), or multipart file upload; no account required
- **Server-side syntax highlighting** — language auto-detected from file extension or manually selected
- **Expiry options** — `1h`, `1d`, `1w`, `1m`, `3m`, `6m`, `1y`, `18m`, `2y`, `never` (default), or a custom duration in seconds
- **Burn after N reads** — paste is permanently deleted once its view count reaches a user-set threshold (1–9999)
- **Visibility control** — public (listed in recent pastes) or unlisted (URL-only)
- **Owner token deletion** — a `tok_`-prefixed token returned once at creation; reusable across pastes by the same caller; required to delete a paste before natural expiry
- **URL shortening** — a paste whose entire content is exactly one `http`/`https` URL auto-becomes a redirecting short link; the raw and download views always return the literal stored URL text instead of redirecting
- **Raw view, file download, and embeds** — raw content view, download-as-file, an iframe-embeddable view with copy-ready HTML/Markdown snippets, and a QR code page plus PNG image for every paste
- **View count tracking** with automatic background cleanup of expired and burned pastes
- **Full web frontend** — server-rendered Go templates, dark/light/auto theme, PWA, mobile-first, fully usable without JavaScript
- **CLI client** (`pastebin-cli`) — full API access from the terminal
- **OpenAPI/Swagger docs** and a **read-only GraphQL query interface** (create/delete remain REST-only)
- **Drop-in compatibility layer** for pastebin.com, microbin, lenpaste, stikked, hastebin/haste-server, dpaste, the curl-upload family (0x0.st, sprunge.us, ix.io), and termbin/fiche (raw-TCP, enabled by default) — see Compat targets below
- **i18n** — 7 locales (en, es, fr, de, zh, ar, ja), automatic selection via `Accept-Language`, RTL layout for Arabic, fallback to English
- **Tor hidden service** — auto-enabled when the `tor` binary is present; persistent v3 `.onion` address via torrc `HiddenServiceDir`
- **I2P eepsite** — optional, opt-in; persistent `.b32.i2p` address via dedicated i2pd process or SAM bridge
- **GeoIP country blocking** — allowlist or denylist, fail-open when the database is unavailable
- **Prometheus metrics** — internal-only, never exposed publicly (see PART 20)
- **Built-in task scheduler** — no external cron; expiry/burn cleanup, backups, SSL renewal, GeoIP/blocklist/CVE updates, health checks
- **Encrypted backup and restore** — AES-256-GCM + Argon2id, manifest-verified before extraction
- **Self-update** — checksum-verified binary swap from GitHub Releases
- **Service lifecycle management** — install/uninstall/start/stop/restart across systemd, launchd, and Windows SCM

### Compat targets

The server must be 100% wire-compatible with the following services — existing scripts, CLIs, and API integrations targeting these services must work without modification:

| Target | Guarantee |
|--------|-----------|
| **pastebin.com public API** | Paste creation, retrieval, and login-stub endpoints match the pastebin.com wire protocol (URL paths, request fields, response fields, HTTP status codes, content types) |
| **lenpaste** (protocol reference: `forksmgr/lcomrade-lenpaste`) | `POST /api/new` and `POST /api/v1/new` (form-urlencoded `title`/`body`/`syntax`/`expiration` seconds/`oneUse`) return `{"id","url","deleteToken"}`; `GET /api/get?id=` and `GET /api/v1/get?id=` return the paste JSON; `DELETE` (or `GET`) `/api/remove?id=&deleteToken=` deletes with the compat delete token; `GET /api/list`; `GET /api/v1/getServerInfo` returns the server-info JSON |
| **microbin** | `POST /api/v1/pasta` (form-urlencoded, multipart, or JSON with `content`/`title`/`visibility`/`expiry`/`burn_after`) creates a paste — e.g. `curl -d "content=Hello" {base}/api/v1/pasta`; `GET /api/v1/pasta` lists, `GET /api/v1/pasta/{id}` retrieves, `DELETE /api/v1/pasta/{id}` deletes; text for plain clients, JSON when `Accept: application/json` |
| **stikked** | `POST /api/create` (form-urlencoded `text`/`title`/`name`/`lang`/`expire`/`private`; `apikey` accepted and ignored) returns the plain-text view URL `{base}/view/{id}` or `Error: <msg>`; `GET /view/raw/{id}` raw content; `GET /view/{id}` renders the native paste-detail view directly (no redirect — even for link pastes, which the root `/{id}` route would otherwise redirect to their target); `GET /api/paste/{id}` returns the stikked JSON shape (`pid`, `title`, `name`, `created`, `lang`, `raw`, `hits`) |
| **hastebin / haste-server** | `POST /documents` (raw request body) returns `{"key":"<id>"}`; `GET /documents/{key}` returns `{"data":"...","key":"..."}` (404 `{"message":"..."}`); `GET /raw/{key}` served by the native raw route |
| **dpaste** | `POST /api/` and `POST /api/v2/` (form-urlencoded `content`/`lexer`/`syntax`/`filename`/`expires`/`format`) return the view URL — quoted by default, bare with `format=url`, or `{"url","content","lexer"}` with `format=json`; keyless |
| **curl-upload family** (0x0.st, sprunge.us, ix.io) | `POST /` dispatches by field: multipart `file` (0x0.st, returns an `X-Token` header), form `sprunge` (sprunge.us), or form `f:1` (ix.io); each returns a bare raw-content URL `{base}/raw/{id}` followed by a newline. Absent any of these fields, the request falls through to the native paste-create handler |
| **termbin / fiche** (raw-TCP) | Plain-TCP listener (enabled by default; disable via `server.termbin.enabled: false` / `TERMBIN_ENABLED=false`, default port `9999`): client connects, streams content, half-closes the write side; server stores the paste and responds with `{base}/{id}\n`, then closes. Max payload `server.termbin.max_size` (default 32768 bytes); idle/read timeout `server.termbin.timeout` (default `5s`). Wire-compatible with the `termbin.com` netcat workflow (`echo text \| nc host 9999`) and the fiche server protocol |

Compatibility is wire-level only: URL paths, request/response field names, HTTP status codes, and content types must match. Internal implementation details (storage, ID format, auth mechanism, delete convention) do not need to match. Compat-created pastes use each target's own deletion convention — not the native owner token system — and the two systems must never be mixed.

### Help Page Structure

`/server/help` presents the native REST API first (route table, envelope, Swagger/GraphiQL links, create/get/delete examples), then the web and curl workflows, then a dedicated **Compatibility** section. The Compatibility section documents every compat target (pastebin.com, microbin, lenpaste, stikked, hastebin, dpaste, the curl-upload family, termbin/fiche) with a copyable example and route table per target, and a table of the subdomain routing labels. API paths on the page use the configured `{api_version}`, never a hardcoded version.

### Compat Subdomain Routing

Any of the compat targets above can also be reached by putting a recognized short/long label as the leftmost DNS label of the request Host, in addition to their normal path-based routes — no `server.yml` config is involved; recognition is automatic and hardcoded. The trusted-peer FQDN resolution order (reverse-proxy headers only when the peer is trusted) applies exactly as it does elsewhere in the app.

| Label(s) | Target |
|----------|--------|
| `pb`, `pastebin` | pastebin.com |
| `lp`, `lenpaste` | lenpaste |
| `sk`, `stikked` | stikked |
| `hb`, `hastebin` | hastebin / haste-server |
| `dp`, `dpaste` | dpaste |
| `mb`, `microbin` | microbin |
| `tb`, `termbin` | termbin (HTTP alias of the raw-TCP protocol) |
| `ix` | ix.io |
| `sprunge` | sprunge.us |
| `0x0`, `st` | 0x0.st |

Two different enforcement mechanisms apply depending on the target, because the two target groups don't share routes the same way:

- **pastebin/lenpaste/stikked/hastebin/dpaste/microbin** each own a distinct, non-overlapping set of routes. On a matched subdomain, every other target's compat-owned routes are hidden behind a themed 404 so the subdomain "mirrors the app routes exactly" for that one target. Native app routes (and the matched target's own routes) are unaffected.
- **termbin/ix.io/sprunge.us/0x0.st** (the curl-upload family) all share a single route, `POST /`, dispatched by request field (multipart `file`, form `sprunge`, form `f:1`). These four are therefore never listed as route-owners and are never blocked from each other's subdomains. Instead, a matched subdomain's resolved target is threaded through the request context; when `POST /` arrives with none of the distinguishing fields present, the raw body is treated as that subdomain's content instead of falling through to native paste creation. This is what makes plain `curl --data-binary @- https://tb.{fqdn}/` (or `ix.`, `sprunge.`, `0x0.`, `st.`) work without a distinguishing field, mirroring the netcat/curl UX of the real services.

Every subdomain-forced compat create path continues to honor the operator-configured `paste.max_size`, bounded at the HTTP body layer exactly like every other create path, and requires no auth token — compat create is anonymous/tokenless exactly like the field-dispatched routes, never gated on `server.token`.

### Business rules

- Paste IDs are 8-character random alphanumeric strings generated from a cryptographic random source; there are no vanity or user-chosen IDs
- Owner tokens are `tok_` + 32 random base62 characters; only their SHA-256 hash is stored, and the raw value is shown exactly once at creation
- Maximum paste size is operator-configured (`paste.max_size`, default 10 MiB, `0` = unlimited) and enforced at the HTTP body layer on every create path, native and compat alike, before the body is read into memory
- Compat request bodies are additionally capped at 10 MiB regardless of `paste.max_size`, because raw-body compat protocols read the whole body into memory
- Expiry is chosen at creation from `1h`, `1d`, `1w`, `1m`, `3m`, `6m`, `1y`, `18m`, `2y`, `never` (default), or a custom duration in seconds; pastes are immutable after creation, so expiry cannot be changed later
- Burn-after-reads is an integer 1–9999, or `0` to disable; the paste is deleted permanently once its view count reaches the threshold
- Visibility is public (listed in recent pastes) or unlisted (reachable by URL only, never listed); there is no private visibility, because there are no accounts
- A paste is a link when, and only when, its entire trimmed content is exactly one absolute `http://` or `https://` URL and nothing else; this is auto-detected and is never a client-settable field
- Link targets are validated for scheme and format only and are never fetched server-side
- A paste with no supplied title falls back to `Untitled`, except a link paste, which falls back to its target URL
- Recent-paste listings are paginated with a maximum page size of 250 items; requested page sizes above 250 are silently capped, never rejected
- Recent-paste listings support `search` (matches paste ID, title, and content, case-insensitive substring), `sort` (`date` (default), `name`, `views`, or `id`), and `order` (`desc` (default) or `asc`) query parameters, on both the frontend `/recent` page and the JSON API; unrecognized `sort` values fall back to `date` rather than erroring; unlisted pastes are never returned regardless of search/sort
- Guests may save their recents-list settings as client-side cookies: `pastebin_pref_sort` (`date` \| `name` \| `views`), `pastebin_pref_order` (`desc` \| `asc`), and `pastebin_pref_per_page` (`20` \| `50` \| `100` \| `250`), each editable from `/server/preferences` and never persisted server-side
- Saved preferences are a deliberate subset of what a `/pastes` query accepts: that query also permits `title` (an alias of `name`) and `id` as sort columns and any page size up to 250, but none of those are offered as a control on the preferences page, so none may be saved — a cookie must never hold a value the page has no control to render back, or the page would display one ordering while the list used another
- An explicit `?sort=`/`?order=`/`?limit=` on a recents request is persisted to its cookie only when the value passes the same allowlist the read path applies; a wider value applies to that one request and is not remembered. Ordering is a one-way door: preferences may narrow what a query accepts, never widen it
- Deletion before natural expiry requires the paste's owner token; the operator token deletes any paste unconditionally; compat-created pastes use their protocol's own delete token and the two systems are never mixed
- An owner token may be reused across multiple pastes by the same caller; the server verifies the token is active before linking a new paste to it
- The termbin/fiche raw-TCP listener is enabled by default on port 9999, capped at `server.termbin.max_size` (default 32768 bytes) with a `server.termbin.timeout` idle timeout (default `5s`)
- Compat create paths are always anonymous and tokenless; any API key a compat protocol supplies is accepted and ignored — they carry no CSRF token or cookie either, so every compat create route and the whole `/api/*` tree is exempt from CSRF validation (browser form routes such as `POST /pastes` keep it)
- All token, hash, and signature comparisons are constant-time; plain equality comparison is never used for a security-sensitive value

### Endpoints (WHAT, not paths — see PART 14)

Native capabilities, exposed on both the API surface and the browser frontend:

- Create a paste — from a JSON body, an HTML form post, a raw request body (shell pipe), or a multipart file upload
- Retrieve a paste — rendered view page, raw content, and download-as-file variants; the raw and download variants never redirect, even for link pastes
- View a paste embedded — iframe-embeddable rendering of a paste
- Get a paste's QR code — as a page and as a PNG image
- Delete a paste — requires the paste's owner token, or the operator token which may delete any paste
- List recent public pastes — paginated (max 250/page), searchable by ID/title/content, sortable by date/name/views/id in either order; unlisted pastes are never included
- Scheduler management — operator-token gated; the sole runtime-configuration exception (see Non-goals)
- API documentation UIs — Swagger and GraphQL browsers

Compat endpoints are the one place where concrete paths, field names, status codes, and content types are business logic rather than implementation detail: they are external wire contracts owned by the services being emulated, not routes this project is free to choose. They are specified in the Compat targets table above, and in the raw-TCP termbin/fiche listener described there.

### FAQ

**Do I need an account to create a paste?**
No. Pastebin is fully anonymous — no registration, no login.

**How long are pastes stored?**
Until the expiry you choose at creation (`1h` up to `2y`), or forever if you pick `never` (the default).

**What's the maximum paste size?**
Operator-configured (`paste.max_size`, default 10 MiB, `0` = unlimited) and enforced at the HTTP body layer before the request is read into memory.

**Can I delete a paste I created?**
Yes — save the `owner_token` returned at creation and use it to delete the paste before it expires. Losing the token means the paste can't be deleted early.

**What's "burn after N reads"?**
An optional setting that permanently deletes the paste once its view count reaches a threshold you set (1–9999).

**Can I embed a paste on my website?**
Yes — every paste has an iframe-embeddable view, and the paste view page provides copy-ready HTML and Markdown embed snippets.

**Can I use this from the command line?**
Yes — pipe content to the create endpoint with `curl`, or use the `pastebin-cli` client for full API access.

**Does it work with my existing pastebin.com/hastebin/dpaste scripts?**
Yes — the compat layer is wire-compatible with pastebin.com, microbin, lenpaste, stikked, hastebin/haste-server, dpaste, the curl-upload family, and termbin/fiche; existing scripts work unmodified.

**Is there a rate limit?**
Yes, per-endpoint and IP-based; configurable in `server.yml`.

**Can I access it over Tor?**
Yes, when the `tor` binary is present the server auto-enables a persistent `.onion` hidden service address, shown on `/server/help`.

**Can I access it over I2P?**
Only if the operator opts in (`server.i2p.enabled` / `I2P_ENABLED` / `--i2p`, disabled by default). When enabled and a provider (i2pd or a local SAM bridge) is available, a persistent `.b32.i2p` eepsite address is shown on `/server/help`.

### Roles & permissions

No user roles exist. All native API endpoints are public. The only privilege distinction is the operator token configured server-side.

| Actor | Access |
|-------|--------|
| **Anonymous visitor (browser)** | Create pastes; view any public or unlisted paste by URL; browse recent public pastes; delete own paste with owner token |
| **Anonymous API client (curl / CLI)** | Create pastes; retrieve paste content; delete paste with owner token; list recent public pastes; token list and revoke |
| **Server operator** | Configures server via `server.yml`; holds operator token that can delete any paste unconditionally |

### Data model & sensitivity

**Paste record** (no PII stored):

| Field | Sensitivity |
|-------|-------------|
| `id` | Public |
| `title` | Public (user-controlled) |
| `content` | Public (user-controlled; may contain sensitive data — service makes no confidentiality guarantee) |
| `language` | Public |
| `visibility` | Public |
| `burn_after` | Public |
| `expires_at` | Public |
| `delete_token_hash` | Internal — used only by compat layer deletion; never returned after creation |
| `views` | Public |
| `created_at` | Public |
| `is_link` | Public — when true, `content` is a redirect target (`http`/`https` only) and visiting the short link redirects instead of rendering |

**API token record** (owner token system, separate from compat delete tokens):

| Field | Sensitivity |
|-------|-------------|
| `token_hash` | Internal — SHA-256 hex of the raw token; never returned |
| `token_prefix` | Semi-public — first 12 chars of the raw token; used for CLI revoke lookup |
| `resource_type` | Internal |
| `resource_id` | Internal |
| `created_at`, `expires_at`, `last_used_at`, `revoked_at` | Internal |

**Owner token delivery** — returned in `owner_token` field of every native API create response; shown once in web UI with copy button; saved to `localStorage` in browser; never logged.

### Trust boundaries & external services

| Boundary | Trust | Notes |
|----------|-------|-------|
| Embedded SQLite | Trusted — local disk only | No network-accessible database |
| Incoming HTTP requests | **Untrusted** | Paste size capped at HTTP layer before reading |
| Paste content | **Untrusted** | Stored as opaque text; never executed; HTML-escaped in all web views |
| Multipart file uploads | **Untrusted** | Content extracted as text only; filename used only for language detection |
| Owner token (inbound) | **Untrusted** | Hashed before comparison; constant-time compare enforced |
| Operator token (inbound) | **Untrusted** | Hashed; constant-time compare against cached hash |
| Compat delete token (inbound) | **Untrusted** | Hashed before comparison |
| GitHub Releases API | **Untrusted network source** | Used by self-update; SHA-256 checksum verified before applying the downloaded binary; update is operator-initiated, not automatic |
| Tor binary (host-installed) | Trusted — operator-controlled binary | Forked as subprocess via `github.com/cretz/bine`; hidden service declared via torrc `HiddenServiceDir`; non-fatal when absent |
| I2P provider (i2pd binary or local SAM bridge) | Trusted — operator-controlled binary/service, opt-in only | i2pd forked as subprocess, or raw SAMv3 session against `sam_address`; non-fatal when neither is available; disabled by default |
| SMTP server (operator-configured) | Semi-trusted — operator-controlled | Used for email notifications; credentials in `server.yml`; silently disabled when unconfigured |
| GeoIP CDN (jsDelivr) | Semi-trusted network source | GeoLite2-Country database downloaded by `geoip_update` scheduled task over HTTPS; no separate checksum beyond HTTPS; fail-open on download error |
| `forksmgr/lcomrade-lenpaste` (protocol reference) | **Untrusted source** — wire protocol spec consulted at development time; no code imported at runtime | Failure mode: compat layer continues to function even if the upstream fork is deleted or unmaintained; the protocol is implemented independently from the published spec |

### Threat model & abuse cases

**Primary assets:** service availability; disk space; paste integrity (preventing unauthorized deletion).

**Attacker / abuser goals:**

| Threat | Required defense |
|--------|-----------------|
| DoS via high-rate paste creation to exhaust disk | Rate limiting on create endpoint |
| XSS via paste content rendered in browser | All paste content HTML-escaped; Chroma output is sanitized HTML |
| Owner token brute-force to delete others' pastes | `crypto/rand` generation; SHA-256 storage; constant-time comparison |
| Compat delete-token brute-force | Same protections as owner token |
| Operator / metrics token timing attack | `crypto/subtle.ConstantTimeCompare` for all token and hash comparisons — plain `==` is forbidden |
| Storing illegal or abusive content | Operator responsibility; acceptable-use policy via `server.yml`; country blocklist for geographic restrictions |
| Large paste upload to exhaust memory | Maximum paste size enforced at HTTP body layer before reading into memory |
| Malicious backup restore (corrupted or tampered archive) | `VerifyBackup()` checks `sha256_manifest.txt` before any extraction; restore aborts if verification fails |
| Backup key compromise via weak derivation | Backup encryption password derives key via Argon2id (not bcrypt, not PBKDF2) |
| Unauthorized service install / data purge | `Install()` and `Uninstall()` require root privilege; `Uninstall()` requires interactive `[y/N]` confirmation |
| Self-update supply-chain attack (tampered release binary) | SHA-256 checksum verified against the release asset list before `os.Rename`; update aborted on mismatch |
| Country-based abuse or legal/regulatory blocking requirement | Country denylist via GeoIP middleware; allowlist mode also supported; bypassed for RFC1918 and loopback |
| Open-redirect / phishing abuse of `is_link` short URLs | Scheme allow-list (`http`/`https` only) enforced on create; same rate limiting, GeoIP, and blocklist middleware as paste creation; operator token can delete any abusive link unconditionally |

### Security decisions & exceptions

- **No authentication on any native endpoint:** intentional — fully public anonymous service.
- **`api_dev_key` and `api_user_key` silently ignored in compat layer:** intentional — compatibility shim; these fields have no meaning on a keyless public instance.
- **`POST /api/api_login.php` returns `"ANONYMOUS"`:** intentional — allows pastebin.com API scripts to proceed without modification.
- **Auth web routes redirect to `/` rather than 404:** intentional — scripts probing `/login` should not hard-fail.
- **Owner token reuse across pastes:** intentional — one token may authorize deletion of multiple pastes created by the same caller; the server validates the token is active before linking it to a new paste.
- **Owner token stored as SHA-256 hash only:** intentional — raw token shown once at creation; loss means the paste cannot be deleted early (compat-created pastes are unaffected, they use their own delete-token mechanism).
- **Compat layer uses separate delete-token mechanism (paste row `delete_token_hash`):** intentional — compat protocols have their own delete conventions incompatible with the owner token system; the two systems must not be mixed.
- **stikked `apikey` / dpaste keyless / hastebin keyless creation accepted without auth:** intentional — these protocols are keyless on a public instance; any supplied API key is accepted and ignored so existing scripts proceed without modification.
- **`POST /` is a multiplexed dispatcher for the curl-upload family:** intentional — sprunge.us (`sprunge`), ix.io (`f:1`), and 0x0.st (multipart `file`) all upload to the bare root path with mutually exclusive field names; the handler dispatches on the present field and falls through to the native paste-create handler when none match, so native form posts are unaffected.
- **curl-upload family returns raw-content URLs (`{base}/raw/{id}`):** intentional — these tools expect the returned URL to serve content directly (for piping back into a shell), unlike stikked/dpaste which return human view URLs.
- **0x0.st `X-Token` header returned on create:** intentional — mirrors the 0x0.st management-token convention; the value is the paste's compat delete token, consistent with the separate compat delete-token mechanism above.
- **Compat request bodies capped at 10 MiB (`maxCompatBody`):** intentional — raw-body compat creators (hastebin) read the full body into memory; the cap bounds memory use and is enforced via `http.MaxBytesReader`.
- **All token and hash comparisons use `crypto/subtle.ConstantTimeCompare`:** intentional — prevents timing-based side-channel attacks on owner tokens, operator tokens, compat delete tokens, and metrics bearer tokens; plain `==` is never used for security-sensitive comparisons.
- **Backup encryption uses Argon2id (not bcrypt, not PBKDF2):** intentional — Argon2id is memory-hard, resists GPU brute-force; bcrypt is forbidden for this use case per spec.
- **`VerifyBackup()` is required before restore:** intentional — ensures backup archive integrity via `sha256_manifest.txt`; restore aborts on any mismatch rather than silently applying a corrupt or tampered archive.
- **`Install()` and `Uninstall()` require root privilege:** intentional — writing systemd units, launchd plists, and SCM entries requires elevated access; both commands check `isPrivileged()` and exit with an error if not root/admin.
- **`Uninstall()` requires interactive `[y/N]` confirmation:** intentional — destructive operation; cannot be triggered accidentally or by a non-interactive process without explicit acknowledgment.
- **Self-update uses atomic `os.Rename` after SHA-256 verification:** intentional — prevents a partially-written binary from replacing the running binary; aborts the update on checksum mismatch before `os.Rename` is called.
- **Paste content not encrypted at rest:** intentional for a public service; operators requiring encryption should use full-disk encryption at the host level.
- **`Access-Control-Allow-Origin: *` on all responses:** intentional — public API designed for cross-origin browser use.
- **Single SQLite instance, no cluster:** intentional — single-binary deployment; no external database dependency; see non-goals.
- **GeoIP loaded from local database file, not queried per-request to an external API:** intentional — avoids network latency on every request, preserves availability when CDN is unreachable, and prevents data leakage of client IPs to third parties.
- **Container runs as root:** intentional — the container binds on port 80 (< 1024), which requires root or `CAP_NET_BIND_SERVICE`. Per PART 26, the exception applies; no non-root user is created in the runtime stage. Operators who wish to use an unprivileged port should set `PORT` and map externally, then add a non-root USER in a derived image.
- **`!important` in the `@media (prefers-reduced-motion: reduce)` block (`common.css.tmpl`):** intentional and spec-sanctioned. The general PART 16 rule restricts `!important` to print styles, but the same PART supplies the reduced-motion override as canonical CSS that itself uses `!important` (AI.md lines 22061-22068). The declarations must win over every per-component animation/transition rule to honor the user's motion preference, which is only guaranteed with `!important`; this is the WCAG 2.1 AA "animation-from-interactions" technique. The exception is limited to `animation-duration`, `animation-iteration-count`, `transition-duration`, and `scroll-behavior` inside that single media query.
- **termbin port 9999 binds all interfaces in production compose (no `172.17.0.1:` prefix):** intentional — the PART 26 bridge-IP rule exists so a reverse proxy handles external HTTP traffic, but termbin/fiche is a raw-TCP protocol that an HTTP reverse proxy cannot front; netcat clients must reach the listener directly, so the mapping is `9999:9999` in the production, dev, and test compose files (`TERMBIN_ENABLED: true` is set alongside it, and the image exposes `9999`). Only the HTTP port keeps the `172.17.0.1:` bridge binding. Operators can firewall or disable it via `TERMBIN_ENABLED=false`.
- **GUI mode (`src/client/gui/`) uses `github.com/gogpu/ui`, not GTK4/Qt6/Cocoa/Win32-WinUI, Gio, or Fyne:** intentional, recorded as an override in `SPEC.md` since `AI.md` PART 32 is read-only. Every toolkit AI.md names (native or Gio/Fyne) was verified to require cgo, contradicting the absolute `CGO_ENABLED=0` pure-Go rule; `gogpu/ui` was verified cgo-free across all 4 required OS families via real Docker builds. Accepted risk: pre-1.0 library (v0.1.54), no long-term track record — an explicit, informed project-owner decision made after ruling out every cgo-free alternative in AI.md.

### Data sources

- **User-submitted content** — every paste originates from an anonymous request; there is no seeded, curated, or imported paste corpus
- **Embedded SQLite database** — the sole store for pastes, owner tokens, and server state; created on first run — see PART 10
- **GeoLite2-Country database** — downloaded over HTTPS from a public CDN by the `geoip_update` scheduled task, refreshed weekly, fail-open when unavailable — see PART 19
- **IP/CIDR/country blocklist** — loaded from an operator-configured URL or local file, refreshed hourly by the `blocklist_update` scheduled task
- **CVE feed** — refreshed by the `cve_update` scheduled task — see PART 19
- **GitHub Releases** — polled for self-update version checks — see PART 22
- **Embedded assets** — templates, CSS, JS, images, fonts, and the seven locale files are compiled into the binary at build time; no security database is ever embedded
