# Security

## Design Principles

- **No user accounts**: eliminates credential stuffing and privilege escalation entirely
- **Anonymous creation**: paste creation is public; owner and operator actions require the appropriate Bearer credential
- **Fail closed**: invalid or missing tokens deny access; no silent allows
- **Crypto/rand everywhere**: paste IDs and delete tokens use `crypto/rand`, never `math/rand`

## Owner Tokens

- Generated from cryptographically secure randomness as `tok_` plus 32 base62 characters
- Stored as a SHA-256 hash and an AES-256-GCM encrypted copy; plaintext is never logged or stored unencrypted
- Returned in the response when a new owner is created and viewable, masked by default, at `/server/preferences/tokens`
- Native API credentials use `Authorization: Bearer {token}` only; alternate headers and query tokens are ignored
- Comparisons use constant-time checks; web management forms can use the HttpOnly owner cookie with CSRF protection

## Paste Content

- HTML-escaped in all web views
- Chroma highlighting output is sanitized HTML
- Never executed or evaluated
- Size-capped at the HTTP layer before reading into memory (10 MiB by default; configurable with `paste.max_size`, where `0` disables the native paste limit). Raw compatibility request bodies also have their protocol-specific 10 MiB cap.
- Never logged

## Transport

- CORS defaults to `*`; configured origins are checked against the allowlist, and credentialed CORS is only enabled for explicit origins
- Native API credentials use the `Authorization` header; query tokens are not accepted for API authentication. Security-report status links have a separate one-time token.
- HTTPS strongly recommended in production (use a reverse proxy or `--tls`)

## Framing & Embeds

- All routes send `X-Frame-Options: SAMEORIGIN` and CSP `frame-ancestors 'self'` — third-party sites cannot iframe the app
- **Exception:** `/emb/{id}` is the designated embeddable endpoint — it omits `X-Frame-Options` and sends a configurable `frame-ancestors` directive instead
- Default embed policy is `*` (any site may iframe an embed); restrict it via `web.csp.embed_frame_ancestors` in `server.yml`, e.g. `'self' https://example.com`
- Defense in depth: when a browser sends `Sec-Fetch-Dest: iframe|frame|embed|object` cross-site for any endpoint other than `/emb/{id}`, the request is rejected with 403 (requires `web.headers.sec_fetch_validation`)
- The paste view page offers copy-ready HTML (`<iframe>` of `/emb/{id}`) and Markdown embed snippets

## Security Header Configuration

- `web.headers`: `coop`, `coep`, `corp`, `origin_agent_cluster`, `cross_domain_policies`, `dns_prefetch_control`, `honor_sec_gpc`, `honor_dnt`, `clear_site_data.*`, and `nel.*` tune the cross-origin, privacy, and reporting headers; defaults are the broad-compatibility values
- `web.permissions_policy` maps each Permissions-Policy feature to its allowlist (`"()"`, `"(self)"`, `"*"`); an empty value omits the feature
- `web.csp` supports `*_src_extra` (append), `*_src_override` (replace), `reports_enabled`, and `reports_sample_rate` (0.0-1.0 fraction of CSP reports logged, default 1.0); `{request_nonce}` inside any `*_src_override` is replaced per response with a fresh nonce that is also rendered on server-side `<script>` tags
- `Sec-GPC: 1` is honored as a privacy opt-out, suppresses non-essential cookies and tracking, and is recorded as `compliance.gpc_honored` in the audit log. `DNT: 1` is ignored unless `web.headers.honor_dnt` is enabled; honored DNT suppresses non-essential behavior without being reported as GPC.
- Browser Network Error Logging reports are sent to `/api/v1/server/reports/nel`, sampled by `web.headers.nel.sample_rate`, and logged with sanitized fields; CSP and default reports use their own report endpoints.
- Requests arriving over Tor or I2P never receive HSTS, `upgrade-insecure-requests`, or clearnet reporting URLs; clearnet HTML responses advertise the onion service with `Onion-Location`
- `web.robots.ai_bots` (`default` and per-bot `allow`/`deny`) controls AI-crawler stanzas in `robots.txt`; `web.llms` (`enabled`, `include_endpoints`, `include_schemas`, `custom_sections`) controls `llms.txt`

## Rate Limiting

- Defaults, per IP per minute: 120 reads, 10 writes (create/delete), 120 health checks, 240 total (global burst)
- Configurable under `server.rate_limit` (`read`, `write`, `health`, `global_burst`) in `server.yml`
- A client that sends more than 10x the read limit in a minute is blocked for an hour (`server.security.abuse_detection`)
- Rate limiting protects the server against abuse — it is not a usage cap

## Public Security Endpoints

| Endpoint | Purpose |
|----------|---------|
| `/.well-known/security.txt` | RFC 9116 contact and policy file (canonical path only; bare `/security.txt` returns 404) |
| `/.well-known/pgp-key.asc` | Coordinated-disclosure PGP public key; `404` until a keypair exists and publishing is enabled |
| `/.well-known/llms.txt` (alias `/llms.txt`) | Generated description of the service for AI agents |
| `/server/security` | Human-readable overview of `security.txt` and how to report |
| `/server/security/policy` | Disclosure policy |
| `/server/security/thanks` | Acknowledgment after a report is received |
| `/server/security/report/{tracking_id}` | Researcher status page; the acknowledgment email's token can be used once per day and expires 30 days after the report closes |
| `/server/healthz` | Health page (HTML, text or JSON by content negotiation) |
| `/api/v1/server/healthz`, `/api/healthz` | Health as JSON by default; text via `.txt`, `Accept: text/plain` or curl-style clients (same handler, no redirect) |

Health responses report only `ok`/`error` per check and never include connection strings, paths or configuration. `/healthz` is an optional root alias for `/server/healthz`, mounted only when `server.healthz.root.enabled: true`. `/server/metrics` and `/metrics` are internal only; restrict them at the firewall or reverse proxy and protect them with the per-service bearer tokens under `server.metrics`.

## Security Reporting

- Researchers read `/.well-known/security.txt` for the contact address, policy link and PGP key.
- `/server/contact?security_id=...` opens the contact form pre-filled as a security report; the submitter receives a tracking id and can follow progress at `/server/security/report/{tracking_id}`.
- `/server/security` explains the process in plain language and `/server/security/policy` states the disclosure policy.

## Well-Known Namespace

Only these `/.well-known/*` entries are served: `security.txt`, `pgp-key.asc` (when enabled) and `llms.txt`. Every other path under `/.well-known/` returns `404`. WebFinger, OpenID Provider metadata, Android App Links, Apple app-site association and MTA-STS are not enabled.

## Tor Privacy

Requests whose `Host` matches the configured `tor.onion_address` are
treated as Tor hidden-service traffic:

- All absolute URLs (base URL, `security.txt` links, pagination, API
  docs) use `http://{onion}` — the clearnet FQDN never appears
- `/.well-known/security.txt` serves a Tor variant: onion URLs only,
  `Contact: mailto:` uses `tor.contact_email` (omitted entirely when
  unset — never the clearnet email), and `Preferred-Languages` is
  omitted to reduce fingerprinting
- CORS headers answer with the onion origin; the clearnet or
  operator-configured origin is never emitted on Tor responses
- Contact, abuse, and security emails shown on Tor pages come from
  `tor.contact_email` only; when unset, no email is disclosed

## Secret Rotation

Two secrets can be rotated manually from the command line. There is no web UI or API route for this.

```bash
cairnbin --maintenance secret rotate installation_secret
cairnbin --maintenance secret rotate encryption_key
```

Both require root, or the operator token (`server.token`) when run as the service user, plus typing `rotate` to confirm. Restart the server afterwards.

- `installation_secret`: generates a new secret and re-wraps the PGP private key under it. The previous secret keeps validating in-flight `security_id` URLs for 7 days. Logged as `security.installation_secret_rotated`.
- `encryption_key`: generates a new AES-256-GCM key, then re-encrypts the stored copies of owner and linked tokens, AES-sealed security report bodies and the DNS-01 credentials. The previous key stays available for 30 days to decrypt anything not yet re-encrypted. Logged as `security.encryption_key_rotated`.

`cookie_signing_key` and `csrf_token_secret` rotate automatically and are rejected by this command.

## Reporting Vulnerabilities

See [SECURITY.md](https://github.com/apimgr/cairnbin/blob/main/.github/SECURITY.md) for the responsible disclosure process.
Do NOT file public issues for security vulnerabilities.
